// Package boot holds the boot protocol checks of roadmap section 5 that
// need the dataflow analysis: the order of the steps of a mode switch
// (item 38, mode-switch-order), the sector count a loader passes to INT 13h
// against the size of the stage it loads (item 43, loader-sector-count),
// and what real-mode code can reach (item 44, real-mode-reach).
//
// Every check here rests on values the dataflow proves (internal/flow) and
// on the CFG's paths. Unknown values are silent, as everywhere: a CR0
// write whose PE bit the analysis cannot see is not a mode switch to these
// rules, and a sector count read from memory is not compared with
// anything (it is information, at most).
//
// Files: boot.go has the per-document facts (the mode-switch events of
// every statement, and which of them may have happened before a given
// statement), modeswitch.go rule 38, a20.go what lets a file that sets PE
// without opening A20 off (a SIPI trampoline, `falcon-asm: a20=`),
// loader.go rule 43, reach.go rule 44, annot.go the falcon-asm annotations
// these rules read and the constants they name.
package boot

import (
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
)

// ev is a set of mode-switch events a statement performs.
type ev uint16

// Events. Each is recorded only when the values involved are proven: a
// CR0 write is a PE set only when the bit written is known to be 1.
const (
	evLGDT    ev = 1 << iota // lgdt
	evPESet                  // CR0.PE 0 -> 1: real -> protected
	evPEClear                // CR0.PE 1 -> 0: protected -> real
	evPGSet                  // CR0.PG 0 -> 1: paging on
	evPAESet                 // CR4.PAE set
	evCR3                    // CR3 loaded
	evLME                    // EFER.LME set by wrmsr
	evA20                    // A20 enabled: port 0x92 bit 1, KBC 0xD1, INT 15h AX=2401h
	evFar                    // a far jump or call

	evAll ev = 1<<iota - 1
)

// switchEvents are the transitions that start a new window: the forward
// search for an out-of-order step stops at the next one, since a step
// after it belongs to the next switch (a thunk's ascent re-loads the GDT
// that its descent has no use for).
const switchEvents = evPESet | evPEClear | evPGSet

const eferIndex = 0xC0000080

// facts is what the rules of one document share.
type facts struct {
	p   *asm.Program
	g   *flow.CFG
	res *flow.Result
	ix  *hw.Index
	ev  []ev // per statement
	any ev   // every event in the document
	// in is, per block, the events that may have happened on some path
	// from a function start to the block's first statement.
	in []ev
}

const factsKey = "boot/facts"

// factsOf builds the document's facts once per run; nil when there is no
// flow result (a macro expansion) or nothing to say.
func factsOf(c *lint.Context) *facts {
	if c.Prog == nil || c.Prog.Doc == nil {
		return nil
	}
	v := c.Memo(factsKey, func() any {
		g, res := c.Flow()
		if g == nil || res == nil {
			return (*facts)(nil)
		}
		f := &facts{p: c.Prog, g: g, res: res, ix: hw.IndexOf(c)}
		f.events()
		f.mayIn()
		return f
	})
	f, _ := v.(*facts)
	return f
}

func instName(s *asm.Stmt) string {
	if s.Kind != asm.Instr || s.Inst == nil {
		return ""
	}
	return s.Inst.Name
}

// events classifies every reachable statement.
func (f *facts) events() {
	f.ev = make([]ev, len(f.p.Stmts))
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.InMacroDef || !f.res.Reachable(i) {
			continue
		}
		f.ev[i] = f.classify(i, s)
		f.any |= f.ev[i]
	}
}

func (f *facts) classify(i int, s *asm.Stmt) ev {
	name := instName(s)
	switch {
	case name == "":
		return 0
	case name == "lgdt":
		return evLGDT
	case s.IsFar() && (s.IsUncondJump() || s.IsCall()):
		return evFar
	case name == "mov" && len(s.Ops) == 2 && s.Ops[1].Kind == asm.OpReg && s.Ops[1].Reg != nil && s.Ops[1].Reg.Class == "control":
		return f.crWrite(i, s.Ops[1].RegName)
	case name == "lmsw":
		return f.crWrite(i, "cr0")
	case name == "wrmsr":
		idx, ok := f.res.KnownAt(i, "ecx")
		v, m, _ := f.res.ValueAt(i, "eax")
		if ok && idx == eferIndex && m&0x100 != 0 && v&0x100 != 0 {
			return evLME
		}
	case name == "out":
		if f.a20Out(i, s) {
			return evA20
		}
	case name == "int":
		if len(s.Ops) == 1 && s.Ops[0].ImmVal.Known && s.Ops[0].ImmVal.V == 0x15 {
			if ax, ok := f.res.KnownAt(i, "ax"); ok && ax == 0x2401 {
				return evA20
			}
		}
	}
	return 0
}

// bitChange reports whether a write makes bit b known set (or known clear,
// set false) when it was not known to be so before.
func bitChange(before, after flow.Value, b uint64, set bool) bool {
	if after.Mask&b == 0 || (after.V&b != 0) != set {
		return false
	}
	return before.Mask&b == 0 || (before.V&b != 0) != set
}

// crWrite is the events of a write to a control register, from the
// register's value before and after it.
func (f *facts) crWrite(i int, cr string) ev {
	before, after := f.res.Before(i), f.res.After(i)
	if before == nil || after == nil {
		return 0
	}
	var out ev
	switch cr {
	case "cr0":
		b, a := before.Get(flow.CR0), after.Get(flow.CR0)
		// Real mode runs .code16; a CR0 write with PE set there whose
		// old PE is unknown is the switch. In 32- or 64-bit code PE is
		// already set and the write is about other bits.
		if bitChange(b, a, 1, true) && (b.Mask&1 != 0 || f.p.Stmts[i].Mode == asm.Mode16) {
			out |= evPESet
		}
		if bitChange(b, a, 1, false) {
			out |= evPEClear
		}
		if bitChange(b, a, 1<<31, true) {
			out |= evPGSet
		}
	case "cr3":
		out |= evCR3
	case "cr4":
		if bitChange(before.Get(flow.CR4), after.Get(flow.CR4), 1<<5, true) {
			out |= evPAESet
		}
	}
	return out
}

// a20Out recognizes the fast A20 gate (bit 1 written to port 0x92) and the
// keyboard controller's write-output-port command (0xD1 to port 0x64).
func (f *facts) a20Out(i int, s *asm.Stmt) bool {
	if len(s.Ops) != 2 {
		return false
	}
	var port uint64
	switch op := &s.Ops[1]; {
	case op.Kind == asm.OpImm && op.ImmVal.Known:
		port = uint64(op.ImmVal.V)
	case op.Kind == asm.OpReg && op.RegName == "dx":
		v, ok := f.res.KnownAt(i, "dx")
		if !ok {
			return false
		}
		port = v
	default:
		return false
	}
	v, m, _ := f.res.ValueAt(i, "al")
	switch port {
	case 0x92:
		return m&2 != 0 && v&2 != 0
	case 0x64:
		return m == 0xFF && v == 0xD1
	}
	return false
}

// mayIn solves "which events may have happened before this block" over the
// CFG: a forward union, empty at function starts. A call may do anything,
// so after one every event may have happened; that is the side that keeps
// an out-of-order verdict from resting on code the analysis did not see.
func (f *facts) mayIn() {
	n := len(f.g.Blocks)
	f.in = make([]ev, n)
	out := make([]ev, n)
	for b := range out {
		out[b] = f.gen(b, f.g.Blocks[b].Last+1)
	}
	for changed := true; changed; {
		changed = false
		for b := range f.g.Blocks {
			var in ev
			for _, p := range f.g.Blocks[b].Pred {
				in |= out[p]
			}
			if in&^f.in[b] != 0 {
				f.in[b] |= in
				changed = true
			}
			if o := f.in[b] | out[b]; o != out[b] {
				out[b] = o
				changed = true
			}
		}
	}
}

// gen is the events of block b's statements before statement end.
func (f *facts) gen(b, end int) ev {
	bl := &f.g.Blocks[b]
	var out ev
	for i := bl.First; i < end && i <= bl.Last; i++ {
		out |= f.ev[i]
		if s := &f.p.Stmts[i]; s.Kind == asm.Instr && s.IsCall() && !s.IsFar() || s.Kind == asm.MacroCall {
			out = evAll
		}
	}
	return out
}

// mayBefore reports whether an event in want may have happened on some
// path from a function start to statement i.
func (f *facts) mayBefore(i int, want ev) bool {
	b := f.g.BlockOf[i]
	if b < 0 {
		return true
	}
	return (f.in[b]|f.gen(b, i))&want != 0
}

// maxForward bounds the forward search, in statements visited.
const maxForward = 400

// after finds the nearest statement after i, on some path, performing an
// event in want, before the path reaches the next mode switch (or leaves
// the function: ret, iret, an indirect jump). -1 when there is none.
func (f *facts) after(i int, want ev) int {
	type item struct{ b, from int }
	b0 := f.g.BlockOf[i]
	if b0 < 0 {
		return -1
	}
	queue := []item{{b0, i + 1}}
	seen := map[int]bool{}
	visited := 0
	for len(queue) > 0 && visited < maxForward {
		it := queue[0]
		queue = queue[1:]
		bl := &f.g.Blocks[it.b]
		stop := false
		for k := it.from; k <= bl.Last && !stop; k++ {
			visited++
			if f.ev[k]&want != 0 {
				return k
			}
			if f.ev[k]&switchEvents != 0 {
				stop = true
			}
		}
		if stop {
			continue
		}
		switch bl.Exit {
		case flow.ExitRet, flow.ExitIret, flow.ExitIndirect, flow.ExitHalt, flow.ExitTrap:
			continue
		}
		for _, s := range bl.Succ {
			if !seen[s] {
				seen[s] = true
				queue = append(queue, item{s, f.g.Blocks[s].First})
			}
		}
	}
	return -1
}

// stmtsOf calls fn for every statement index with an event in want, in
// order.
func (f *facts) stmtsOf(want ev, fn func(i int)) {
	if f.any&want == 0 {
		return
	}
	for i, e := range f.ev {
		if e&want != 0 {
			fn(i)
		}
	}
}

func (f *facts) line(i int) int { return f.p.Stmts[i].Line + 1 }
