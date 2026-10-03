package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// A20 is a gate in the chipset, not a processor state: once one stage
// opens it, every later stage and every other processor sees it open. A
// single file can therefore need A20 without opening it, and the rule has
// two ways to hear that:
//
//   - the switch is in a SIPI trampoline (hw.Trampolines): an application
//     processor started after boot runs on the boot processor's platform
//     state, and has nothing to open;
//   - `falcon-asm: a20=<target>` names what opened it, and the rule checks
//     that the target really does (port 0x92 bit 1, the keyboard
//     controller's 0xD1 command, INT 15h AX=2401h). The target is a file
//     relative to this one (`../start/pmode.S`), optionally narrowed to a
//     function (`../start/pmode.S:pmode_enter`), or a label: of this file,
//     or a global of a file linked with it, found through the workspace.
//
// Which stage runs first is not something the linter can see, so the
// annotation is the proof of order; only "the named code opens A20" is
// checked. Nothing is inferred from another file opening A20 on its own:
// two files in one tree may be different programs.

// trampolineSwitch reports a PE write inside a SIPI trampoline: code the
// trampoline's label reaches without leaving the function.
func (f *facts) trampolineSwitch(c *lint.Context, p int) bool {
	tr := hw.Trampolines(c)
	if len(tr) == 0 {
		return false
	}
	target := f.g.BlockOf[p]
	if target < 0 {
		return false
	}
	for _, li := range tr {
		b := f.blockAt(li)
		if b >= 0 && reaches(f.g, b, target, false) {
			return true
		}
	}
	return false
}

// blockAt is the block of the first code statement at or after i.
func (f *facts) blockAt(i int) int {
	for ; i < len(f.p.Stmts); i++ {
		if b := f.g.BlockOf[i]; b >= 0 {
			return b
		}
		if s := &f.p.Stmts[i]; s.Kind == asm.Instr {
			return -1
		}
	}
	return -1
}

// reaches walks the CFG from block from. With calls set the walk also
// enters direct callees, so a function that opens A20 through a helper it
// calls counts. target -1 visits everything and returns false; visit, when
// set, sees each block.
func reaches(g *flow.CFG, from, target int, calls bool, visit ...func(b int)) bool {
	seen := map[int]bool{from: true}
	queue := []int{from}
	for len(queue) > 0 {
		b := queue[0]
		queue = queue[1:]
		if b == target {
			return true
		}
		for _, v := range visit {
			v(b)
		}
		next := g.Blocks[b].Succ
		if calls && g.Blocks[b].Callee >= 0 {
			next = append(append([]int(nil), next...), g.Blocks[b].Callee)
		}
		for _, s := range next {
			if s >= 0 && !seen[s] {
				seen[s] = true
				queue = append(queue, s)
			}
		}
	}
	return false
}

// a20Proof checks a `falcon-asm: a20=` annotation. ok means the named code
// opens A20; otherwise why says what is wrong with the annotation.
func (f *facts) a20Proof(c *lint.Context, target string) (ok bool, why string) {
	file, label := splitA20Target(target)
	if file == "" {
		if _, in := f.p.Labels[label]; in {
			if f.opensA20(label) {
				return true, ""
			}
			return false, fmt.Sprintf("%s, in this file, does not enable A20", label)
		}
		if ws := c.Workspace(); ws != nil {
			locs := ws.GlobalDefinitions(label)
			for _, l := range locs {
				path, ok := toolchain.URIToPath(l.URI)
				if !ok {
					continue
				}
				opens, why := fileOpensA20(path, label, c.Target.Mode)
				if opens || why != "" {
					return opens, why
				}
			}
			if len(locs) > 0 {
				return false, fmt.Sprintf("%s (%s) does not enable A20", label, filepath.Base(locs[0].URI))
			}
		}
		// A bare name may still be a file next to this one.
		file, label = label, ""
	}
	path := file
	if !filepath.IsAbs(path) {
		doc, ok := toolchain.URIToPath(f.p.Doc.URI)
		if !ok {
			return false, fmt.Sprintf("a20=%s cannot be resolved: the document has no path", target)
		}
		path = filepath.Join(filepath.Dir(doc), file)
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		if strings.ContainsAny(target, "/.") {
			return false, fmt.Sprintf("a20=%s names no file (looked for %s)", target, path)
		}
		return false, fmt.Sprintf("a20=%s names no label of this file, no global of a file linked with it and no file", target)
	}
	opens, why := fileOpensA20(path, label, c.Target.Mode)
	if opens {
		return true, ""
	}
	if why != "" {
		return false, why
	}
	if label != "" {
		return false, fmt.Sprintf("%s in %s does not enable A20", label, file)
	}
	return false, fmt.Sprintf("%s does not enable A20", file)
}

// splitA20Target splits `file:label`, `file` and `label`. A target with a
// path separator or a source extension is a file.
func splitA20Target(t string) (file, label string) {
	if i := strings.LastIndexByte(t, ':'); i > 0 {
		return t[:i], t[i+1:]
	}
	switch strings.ToLower(filepath.Ext(t)) {
	case ".s", ".sx", ".asm", ".inc":
		return t, ""
	}
	if strings.ContainsRune(t, '/') {
		return t, ""
	}
	return "", t
}

// opensA20 reports an A20 event in the code label reaches (direct callees
// included) in this document.
func (f *facts) opensA20(label string) bool {
	li, ok := f.p.Labels[label]
	if !ok {
		return false
	}
	return f.reachesEvent(f.blockAt(li), evA20)
}

func (f *facts) reachesEvent(b int, want ev) bool {
	if b < 0 {
		return false
	}
	found := false
	reaches(f.g, b, -1, true, func(x int) {
		bl := &f.g.Blocks[x]
		for i := bl.First; i <= bl.Last && !found; i++ {
			found = f.ev[i]&want != 0
		}
	})
	return found
}

// Verdicts on other files are cached by path, label and the file's size
// and modification time: the annotation is read on every lint of the file
// that carries it, the file it names changes rarely.
var a20Cache = struct {
	sync.Mutex
	m map[string]cachedA20
}{m: map[string]cachedA20{}}

type cachedA20 struct {
	mod   time.Time
	len   int64
	opens bool
	why   string
}

// fileOpensA20 parses another file (with the macros its compile flags
// give it, when it has a compile database entry) and runs the dataflow
// over it, so A20 is recognised there exactly as it is here. With label
// set only the code that label reaches counts.
func fileOpensA20(path, label string, mode asm.Mode) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("%s cannot be read (%v)", filepath.Base(path), err)
	}
	key := path + "\x00" + label
	a20Cache.Lock()
	cv, hit := a20Cache.m[key]
	a20Cache.Unlock()
	if hit && cv.mod.Equal(st.ModTime()) && cv.len == st.Size() {
		return cv.opens, cv.why
	}
	cv = cachedA20{mod: st.ModTime(), len: st.Size()}
	cv.opens, cv.why = scanA20(path, label, mode)
	a20Cache.Lock()
	a20Cache.m[key] = cv
	a20Cache.Unlock()
	return cv.opens, cv.why
}

func scanA20(path, label string, mode asm.Mode) (bool, string) {
	text, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Sprintf("%s cannot be read (%v)", filepath.Base(path), err)
	}
	if mode == 0 {
		mode = asm.Mode16
	}
	macros, _ := macrosOf(path)
	doc := &docstore.Document{URI: toolchain.PathToURI(path), Text: string(text)}
	p := asm.Parse(doc, lexer.Tokenize(doc.Text), asm.Config{InitialMode: mode, Macros: macros})
	g := flow.Build(p)
	of := &facts{p: p, g: g, res: flow.Analyze(g, flow.Config{})}
	of.events()
	if label == "" {
		return of.any&evA20 != 0, ""
	}
	if _, ok := p.Labels[label]; !ok {
		return false, fmt.Sprintf("%s has no label %s", filepath.Base(path), label)
	}
	return of.opensA20(label), ""
}
