// Package hw holds the hardware rules of roadmap sections 2, 5 and 7 that
// work from constants in the source: descriptor tables and the selectors
// that name them (far-jump-mode, selector-type, descriptor-invalid,
// gdt-limit), page tables (pte-reserved), SMP trampolines, boot sectors,
// BIOS structures and constant writes to MSRs and control registers. It
// also builds the hardware hover the engine adds to assembly and C, and
// registers the hwdb loader behind lint.Tables.
//
// Constants are followed through one block at a time (see regs.go) and,
// when the caller has the dataflow solution, widened with what it knows
// across labels (flowvals.go).
package hw

import (
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Index is everything this package derives from one program: the data
// layout, the constants the register tracker saw reach a consumer, the
// descriptor and page tables found, and the falcon-asm comments that mark
// structures. It is built once per document version (the rules share it
// through the lint memo, the engine keeps one per snapshot for hover) and
// is read-only afterwards.
type Index struct {
	Prog   *asm.Program
	lay    *layout
	trk    *tracker
	tables []*DescTable // GDTs and IDTs, from lgdt/lidt and by shape
	pages  map[string]*pageTable
	notes  map[int][]string // line -> falcon-asm comment items
	// commentLines[l] is set for a line holding nothing but comments.
	commentLines []bool
	// tramp are the SIPI trampolines (sysrules.go), found once: three
	// rules ask.
	trampOnce sync.Once
	tramp     map[string]int
	code64    bool          // the document has 64-bit code
	evAt      map[int][]int // statement -> tracker events recorded there
	fjs       []farJump
	uses      []selectorUse
	// fl is the dataflow solution the constants were widened with (see
	// flowvals.go); nil for the block-local index.
	fl *flow.Result
}

// NewIndex builds the block-local index of p.
func NewIndex(p *asm.Program) *Index { return newIndex(p, nil) }

func newIndex(p *asm.Program, res *flow.Result) *Index {
	ix := &Index{Prog: p, lay: newLayout(p), notes: map[int][]string{}, fl: res}
	ix.trk = &tracker{prog: p, roles: map[int][]role{}, isLabel: ix.isLabel}
	ix.trk.run()
	ix.evAt = map[int][]int{}
	for k, e := range ix.trk.events {
		ix.evAt[e.stmt] = append(ix.evAt[e.stmt], k)
	}
	ix.applyFlow()
	for i := range p.Stmts {
		if p.Stmts[i].Mode == asm.Mode64 && p.Stmts[i].Kind == asm.Instr {
			ix.code64 = true
			break
		}
	}
	ix.collectNotes()
	ix.findTables()
	ix.findPageTables()
	ix.fjs = ix.farJumps()
	ix.uses = ix.selectorUses()
	return ix
}

const indexKey = "hw/index"

// indexOf is the document's index, shared by every rule of the run.
func indexOf(c *lint.Context) *Index {
	if c.Prog == nil || c.Prog.Doc == nil {
		return nil
	}
	// The rules see values across labels whenever the run has a flow
	// solution; a fragment without one (a macro expansion) gets the
	// block-local index.
	v := c.Memo(indexKey, func() any {
		_, res := c.Flow()
		return NewIndexFlow(c.Prog, res)
	})
	ix, _ := v.(*Index)
	return ix
}

// Seed hands the rules an index the caller already built for the same
// program.
func Seed(m *lint.Memo, ix *Index) { m.Set(indexKey, ix) }

// Provide makes fn the source of the run's index (see lint.Memo.Provide):
// the engine builds one per document version for hover and register
// hints, and the rules then share it instead of building their own. fn
// must index the run's program with the run's flow solution.
func Provide(m *lint.Memo, fn func() *Index) { m.Provide(indexKey, func() any { return fn() }) }

// isLabel reports whether name is a label defined in this document and not
// a macro: its value is an address, not a number.
func (ix *Index) isLabel(name string) bool {
	if _, ok := ix.Prog.Labels[name]; !ok {
		return false
	}
	_, macro := ix.Prog.Macros[name]
	return !macro
}

// labelStmt is the statement defining label name.
func (ix *Index) labelStmt(name string) (int, bool) {
	i, ok := ix.Prog.Labels[name]
	return i, ok
}

// collectNotes reads `falcon-asm: trampoline`, `falcon-asm: mbr` and
// `falcon-asm: struct=dap` items from comments. Suppression items
// (ignore=...) share the prefix and are skipped by the readers.
func (ix *Index) collectNotes() {
	// Line-indexed slices rather than maps: this walks every token of the
	// document, and a map operation per token was most of its cost.
	toks := ix.Prog.Toks
	n := 0
	for i := range toks {
		n = max(n, toks[i].Line+1)
	}
	ix.commentLines = make([]bool, n)
	other := make([]bool, n)
	for _, t := range toks {
		if t.Kind != lexer.Comment {
			other[t.Line] = true
			continue
		}
		ix.commentLines[t.Line] = true
		_, rest, ok := strings.Cut(t.Text, "falcon-asm:")
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(strings.TrimSpace(rest), "*/")
		for _, f := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' }) {
			ix.notes[t.Line] = append(ix.notes[t.Line], strings.ToLower(f))
		}
	}
	for l, o := range other {
		if o {
			ix.commentLines[l] = false
		}
	}
}

// isCommentLine reports whether line holds nothing but comments.
func (ix *Index) isCommentLine(line int) bool {
	return line >= 0 && line < len(ix.commentLines) && ix.commentLines[line]
}

// note returns the value of a falcon-asm comment item ("struct" -> "dap",
// "trampoline" -> "") that applies to statement i: on its own line, or on
// the comment lines directly above it.
func (ix *Index) note(i int, key string) (string, bool) {
	s := &ix.Prog.Stmts[i]
	for line := s.Line; line >= 0 && line >= s.Line-8; line-- {
		items, has := ix.notes[line]
		for _, it := range items {
			k, v, _ := strings.Cut(it, "=")
			if k == key {
				return v, true
			}
		}
		if line < s.Line && !has && !ix.isCommentLine(line) && !ix.silentLine(line) {
			break
		}
	}
	return "", false
}

// stmtAt is the statement whose text covers byte offset off, or -1.
func (ix *Index) stmtAt(off int) int {
	st := ix.Prog.Stmts
	lo, hi := 0, len(st)
	for lo < hi {
		m := (lo + hi) / 2
		if st[m].End <= off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(st) && st[lo].Start <= off && off <= st[lo].End {
		return lo
	}
	return -1
}

// firstCode is the first instruction at or after statement i, skipping
// labels and directives that emit nothing; -1 when data or the end of the
// file comes first.
func (ix *Index) firstCode(i int) int {
	for ; i < len(ix.Prog.Stmts); i++ {
		s := &ix.Prog.Stmts[i]
		switch s.Kind {
		case asm.Instr:
			return i
		case asm.Directive:
			if !silent[s.Name] && !alignDirective(s.Name) {
				return -1
			}
		case asm.MacroCall:
			return -1
		}
	}
	return -1
}

func alignDirective(name string) bool {
	switch name {
	case ".align", ".balign", ".p2align", ".balignw", ".balignl":
		return true
	}
	return false
}

// silentLine reports a line holding only directives that emit nothing
// (`.code16` between a marking comment and its label).
func (ix *Index) silentLine(line int) bool {
	idx := ix.Prog.StmtAt(line)
	for _, k := range idx {
		s := &ix.Prog.Stmts[k]
		if s.Kind != asm.Directive || !silent[s.Name] && !alignDirective(s.Name) || len(s.Labels) > 0 {
			return false
		}
	}
	return len(idx) > 0
}
