package boot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// The annotations these rules read (loads=, sectors=, max=, a20=) state
// facts that live outside the file: the stage a loader reads, the bound a
// build check enforces on it, the earlier stage that opened A20. Each is
// checked as far as the linter can reach rather than taken on trust, so an
// annotation that has gone stale (a renamed file, a header that lost its
// #define) brings the finding back instead of hiding it.

// note is one `falcon-asm:` comment's key=value items. Keys are lower
// case; values keep their case, since they name files and symbols.
type note struct {
	items map[string]string
	line  int
}

// notesOf reads every falcon-asm comment of the document. A comment is
// read up to its first suppression item: the reason after
// `ignore=rule:` is free text, and its words are not annotations.
func notesOf(p *asm.Program) []note {
	var out []note
	for _, t := range p.Toks {
		if t.Kind != lexer.Comment {
			continue
		}
		_, rest, ok := strings.Cut(t.Text, "falcon-asm:")
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(strings.TrimSpace(rest), "*/")
		n := note{items: map[string]string{}, line: t.Line}
		for _, it := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' }) {
			k, v, _ := strings.Cut(it, "=")
			k = strings.ToLower(k)
			if strings.HasPrefix(k, "ignore") {
				break
			}
			n.items[k] = v
		}
		if len(n.items) > 0 {
			out = append(out, n)
		}
	}
	return out
}

// noteValue is the value of the first item key in the document's
// falcon-asm comments.
func noteValue(p *asm.Program, key string) (string, int, bool) {
	for _, n := range notesOf(p) {
		if v, ok := n.items[key]; ok {
			return v, n.line, true
		}
	}
	return "", 0, false
}

// fileMacros returns the object-like macros a file sees after
// preprocessing with its compile flags. It is the fallback for a symbol an
// annotation names when the run has no toolchain macros (the CLI without
// --toolchain, the editor before its first assemble): the bound a build
// enforces usually lives in a header. Tests replace it.
var fileMacros = func(path string) (map[string]string, error) {
	db, err := toolchain.FindDB(path)
	if err != nil {
		return nil, err
	}
	flags, ok := db.Flags(path)
	if !ok {
		return nil, fmt.Errorf("no compile flags for %s", path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ms, err := (&toolchain.Runner{}).Macros(ctx, path, flags)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ms))
	for k, m := range ms {
		if !m.Function {
			out[k] = m.Value
		}
	}
	return out, nil
}

// macroCache keeps fileMacros results for a few seconds: one lint of a
// workspace asks for the same file's macros from more than one rule, while
// an editor session must still see a header edit soon after it is saved.
var macroCache = struct {
	sync.Mutex
	m map[string]cachedMacros
}{m: map[string]cachedMacros{}}

type cachedMacros struct {
	at  time.Time
	m   map[string]string
	err error
}

const macroTTL = 5 * time.Second

func macrosOf(path string) (map[string]string, error) {
	macroCache.Lock()
	c, ok := macroCache.m[path]
	macroCache.Unlock()
	if ok && time.Since(c.at) < macroTTL {
		return c.m, c.err
	}
	m, err := fileMacros(path)
	macroCache.Lock()
	macroCache.m[path] = cachedMacros{at: time.Now(), m: m, err: err}
	macroCache.Unlock()
	return m, err
}

// sets are the symbols the program assigns exactly once (`.equ`, `.set`,
// `name = expr`): a symbol assigned twice (a .rept counter) has no single
// value.
func sets(p *asm.Program) map[string]string {
	out := map[string]string{}
	n := map[string]int{}
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if s.Kind != asm.Directive || s.Name != ".set" || len(s.Args) != 2 || s.Args[0] == "." || s.InMacroDef {
			continue
		}
		n[s.Args[0]]++
		out[s.Args[0]] = s.Args[1]
	}
	for k, c := range n {
		if c > 1 {
			delete(out, k)
		}
	}
	return out
}

// resolveConst evaluates an annotation's number or symbol expression: the
// run's macros (toolchain and in-file #define), the file's own .equ/.set,
// then the macros the file sees through its compile flags. why says what
// could not be resolved when ok is false.
func resolveConst(p *asm.Program, expr string) (v uint64, ok bool, why string) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, false, "it is empty"
	}
	local := sets(p)
	var tc map[string]string
	var tcErr error
	tcDone := false
	lookup := func(name string) (string, bool) {
		if b, ok := p.Macros[name]; ok {
			return b, true
		}
		if b, ok := local[name]; ok {
			return b, true
		}
		if !tcDone {
			tcDone = true
			if path, ok := toolchain.URIToPath(p.Doc.URI); ok {
				tc, tcErr = macrosOf(path)
			} else {
				tcErr = errors.New("the document has no path")
			}
		}
		b, ok := tc[name]
		return b, ok
	}
	n, err := constexpr.Eval(expr, lookup)
	switch {
	case err == nil && n >= 0:
		return uint64(n), true, ""
	case err == nil:
		return 0, false, fmt.Sprintf("%s is negative (%d)", expr, n)
	case errors.Is(err, constexpr.ErrUnknown) && tcErr != nil:
		return 0, false, fmt.Sprintf("%s is not a number, a macro of this run or a .equ/.set of this file, "+
			"and the macros its headers define could not be read (%v)", expr, tcErr)
	case errors.Is(err, constexpr.ErrUnknown):
		return 0, false, fmt.Sprintf("%s is not a number, nor a macro or .equ/.set this file or its headers define", expr)
	}
	return 0, false, fmt.Sprintf("%s is not a constant expression (%v)", expr, err)
}
