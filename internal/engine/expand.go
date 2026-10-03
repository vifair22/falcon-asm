package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// lineExpander is the optional toolchain capability behind cpp macro
// expansion hovers; *toolchain.Runner has it. It is asserted rather than
// added to Toolchain so fakes that predate it keep compiling.
type lineExpander interface {
	ExpandLines(ctx context.Context, file string, f toolchain.Flags, text string, first, last int) (string, error)
}

// expandTimeout bounds a hover's preprocessor run; a hover that waits
// longer is worse than one without the expansion.
const expandTimeout = 3 * time.Second

// maxHoverExpansions is how many invocations the snapshot expands in
// order to number \@ the way gas does; a hover past them expands its own
// invocation with the counter where the walk stopped.
const maxHoverExpansions = 2000

// expandState is a snapshot's macro expansion cache: the gas expansions
// of the version (computed on the first expansion hover) and the cpp
// expansions asked for so far, by statement.
type expandState struct {
	tc lineExpander

	once   sync.Once
	prog   *asm.Program
	x      *asm.Expander
	byStmt map[int]asm.Expansion

	mu  sync.Mutex
	cpp map[int]string // statement -> preprocessor output; "" when it failed

	// What the tokens alone say about macros, for a cheap "nothing to
	// expand here" before the parse (see mayExpand).
	namesOnce sync.Once
	names     map[string]bool // .macro names the document defines, lower case
	asmInc    bool            // the document includes something not a C header
}

// mayExpand reports whether word, an unknown first word at a hover, can
// have an expansion: a gas macro the document defines, any word when it
// includes assembly (whose macros only the parse finds), or a cpp macro.
// Every other unknown word (a typo, a half-typed mnemonic) is answered
// from the tokens without parsing the whole version.
func (s *snapshot) mayExpand(word string) bool {
	e := &s.exp
	e.namesOnce.Do(func() {
		toks := s.tokens()
		for i, t := range toks {
			switch {
			case t.Kind == lexer.Directive && strings.EqualFold(t.Text, ".macro"):
				if i+1 < len(toks) && toks[i+1].Line == t.Line {
					if e.names == nil {
						e.names = map[string]bool{}
					}
					e.names[strings.ToLower(strings.TrimRight(toks[i+1].Text, ","))] = true
				}
			case t.Kind == lexer.Directive && strings.EqualFold(t.Text, ".include"):
				e.asmInc = true
			case t.Kind == lexer.CppDirective && strings.Contains(t.Text, "include") && i+1 < len(toks) && toks[i+1].Line == t.Line:
				arg := strings.Trim(strings.TrimSpace(toks[i+1].Text), "<>\"")
				switch strings.ToLower(filepath.Ext(arg)) {
				case ".h", ".hh", ".hpp":
				default:
					e.asmInc = true
				}
			}
		}
	})
	if e.asmInc || e.names[strings.ToLower(word)] {
		return true
	}
	_, ok := s.macro(word)
	return ok
}

// expansions walks, once per version, every
// invocation of a gas macro the file defines or .includes, in order.
func (s *snapshot) expansions() *expandState {
	e := &s.exp
	e.once.Do(func() {
		// The version's shared parse (the one lint and the hardware
		// hover use); the expander only reads it.
		e.prog = s.program()
		e.x = e.prog.NewExpander()
		for name, d := range s.includedMacros() {
			if e.x.Defs == nil {
				e.x.Defs = map[string]*asm.MacroDef{}
			}
			if _, own := e.x.Defs[name]; !own {
				e.x.Defs[name] = d
			}
		}
		e.byStmt = map[int]asm.Expansion{}
		if len(e.x.Defs) == 0 {
			return
		}
		for i := range e.prog.Stmts {
			if len(e.byStmt) >= maxHoverExpansions {
				break
			}
			if x, ok := e.x.ExpandStmt(e.prog, i); ok {
				e.byStmt[i] = x
			}
		}
	})
	return e
}

// includedMacros are the .macro definitions of the assembly files the
// document includes (not C headers, whose macros are cpp's).
func (s *snapshot) includedMacros() map[string]*asm.MacroDef {
	out := map[string]*asm.MacroDef{}
	for _, p := range s.includes() {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".h", ".hh", ".hpp":
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := string(data)
		doc := &docstore.Document{URI: toolchain.PathToURI(p), Text: text}
		prog := asm.Parse(doc, lexer.Tokenize(text), asm.Config{})
		for name, d := range prog.MacroDefs() {
			if _, seen := out[name]; !seen {
				out[name] = d
			}
		}
	}
	return out
}

// expansion answers hover.Options.Expansion: the invocation whose name
// token starts at (line, col) of this version.
func (s *snapshot) expansion(line, col int) (string, bool) {
	if s.lang != langAsm {
		return "", false
	}
	start, err := s.doc.Offset(protocol.Position{Line: line})
	if err != nil {
		return "", false
	}
	off := start + col
	if word := wordAt(s.doc.Text, off); word == "" || !s.mayExpand(word) {
		return "", false
	}
	e := s.expansions()
	for _, i := range e.prog.StmtAt(line) {
		st := &e.prog.Stmts[i]
		if st.Kind != asm.MacroCall || st.MnemStart != off {
			continue
		}
		if x, ok := e.byStmt[i]; ok {
			return expansionText(x), true
		}
		if _, ok := e.x.Defs[st.Mnemonic]; ok {
			// Past the walk's cap: expand with the counter where it
			// stopped, on a copy so concurrent hovers do not race.
			cp := *e.x
			if x, ok := cp.ExpandStmt(e.prog, i); ok {
				return expansionText(x), true
			}
		}
		word := s.doc.Text[off : off+len(st.Mnemonic)]
		if _, ok := s.macro(word); ok {
			return s.cppExpansion(e, i)
		}
	}
	return "", false
}

func expansionText(x asm.Expansion) string {
	t := strings.TrimRight(x.Text, "\n")
	if x.Err != "" {
		t += "\n# " + x.Err
	}
	return t
}

// cppExpansion runs the preprocessor over the buffer for statement i's
// lines, once per version and statement. A file without compile flags is
// preprocessed with the host's cc, which still applies the file's own
// #defines and the headers next to it.
func (s *snapshot) cppExpansion(e *expandState, i int) (string, bool) {
	if e.tc == nil || s.path == "" {
		return "", false
	}
	e.mu.Lock()
	if v, ok := e.cpp[i]; ok {
		e.mu.Unlock()
		return v, v != ""
	}
	e.mu.Unlock()
	f := s.flags
	if !s.hasFlags {
		f = toolchain.Flags{Compiler: "cc", Dir: filepath.Dir(s.path), File: s.path}
	}
	st := &e.prog.Stmts[i]
	ctx, cancel := context.WithTimeout(context.Background(), expandTimeout)
	defer cancel()
	out, err := e.tc.ExpandLines(ctx, s.path, f, s.doc.Text, st.Line, st.EndLine)
	if err != nil {
		out = ""
	}
	e.mu.Lock()
	if e.cpp == nil {
		e.cpp = map[int]string{}
	}
	e.cpp[i] = out
	e.mu.Unlock()
	return out, out != ""
}

// wordAt is the identifier starting at byte off of text ("" when none).
func wordAt(text string, off int) string {
	if off < 0 || off > len(text) {
		return ""
	}
	end := off
	for end < len(text) {
		c := text[end]
		if c == '_' || c == '.' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			end++
			continue
		}
		break
	}
	return text[off:end]
}
