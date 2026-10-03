package flowrules

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeUnreachable,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Code after a jmp, ret or halt that nothing jumps to.",
			Why: "An instruction with no label after an unconditional transfer can only run if something " +
				"jumps into the middle of it, which nothing in the document does. It is usually the leftover " +
				"of an edit (a jmp added above the old path) or a missing label. Labels whose address " +
				"escapes (global, $label, data) are entries already and are never reported, and padding is " +
				"exempt: a run of nop, int3, hlt or ud2, which is how the BPB jump-over (jmp start; nop) and " +
				"alignment fill look.",
			Bad:  "f:\n\tret\n\tmovw $1, %ax\n\tret",
			Good: "f:\n\tret\ng:\n\tmovw $1, %ax\n\tret",
		},
		Check: checkUnreachable,
	})
	lint.Register(&lint.Rule{
		Code:      CodeFallIntoData,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Code that falls through into data (a string, a table, padding) instead of jumping or returning.",
			Why: "When the last instruction of a path neither jumps nor returns, the processor executes " +
				"whatever bytes follow: the message string or the GDT after the function decodes as " +
				"instructions. The rule reports a reachable fallthrough into a labeled data directive, a " +
				"string or a fill. Unlabeled .byte/.word/.long right after code are taken to be " +
				"hand-encoded instructions (a far jump or a prefix gas would not emit), and a call or hlt " +
				"before data (a call that never returns, a halt) is not a fallthrough.",
			Bad:  "f:\n\tmovw $msg, %si\n\tcall puts\n\tmovw $1, %ax\nmsg:\n\t.asciz \"hi\"",
			Good: "f:\n\tmovw $msg, %si\n\tcall puts\n\tmovw $1, %ax\n\tret\nmsg:\n\t.asciz \"hi\"",
		},
		Check: checkIntoData,
	})
}

// padding reports the instructions alignment fill and the BPB jump-over
// are made of.
func padding(st *asm.Stmt) bool {
	switch instName(st) {
	case "nop", "int3", "hlt", "ud2", "ud0", "ud1":
		return true
	case "xchg":
		return len(st.Ops) == 2 && st.Ops[0].Kind == asm.OpReg && st.Ops[0].RegName == st.Ops[1].RegName
	}
	return false
}

func checkUnreachable(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g := f.p, f.g
	for _, b := range g.Unreachable() {
		bl := &g.Blocks[b]
		first := &p.Stmts[bl.First]
		if first.Kind == asm.MacroCall || first.InMacroDef {
			// A macro may define the labels that make its body reachable.
			continue
		}
		code, pad := 0, true
		stmts(g, b, func(i int) bool {
			st := &p.Stmts[i]
			if st.Kind == asm.MacroCall {
				pad = false
				code++
			}
			if st.Kind == asm.Instr && st.Mnemonic != "" {
				code++
				pad = pad && padding(st)
			}
			return true
		})
		if code == 0 || pad {
			continue
		}
		prev := prevInstr(p, bl.First)
		if prev >= 0 && returnsHere(f, prev, bl.First) {
			continue
		}
		msg := "unreachable: nothing jumps here"
		if prev >= 0 {
			msg = fmt.Sprintf("unreachable: %s at line %d does not continue here and nothing jumps here", p.Stmts[prev].Mnemonic, lineOf(p, prev))
		}
		c.ReportRange(first.Start, p.Stmts[bl.Last].End, msg)
	}
}

// returnsHere reports a return the code may have aimed at the next
// statement: a ret, lret or iret with something pushed above the
// function's frame (lea 3(%rip), %rax; push %rax; iretq) or an unknown
// depth, or a .code16/32/64 between the two, the mark of a mode switch
// that continues in the next instruction.
func returnsHere(f *facts, prev, next int) bool {
	p := f.p
	st := &p.Stmts[prev]
	if !st.IsRet() && !st.IsIret() {
		return false
	}
	for i := prev + 1; i < next; i++ {
		switch p.Stmts[i].Name {
		case ".code16", ".code32", ".code64", ".code16gcc":
			return true
		}
	}
	s := &f.res.In[prev]
	return !s.Reachable || !s.StackKnown || s.Stack != 0
}

// prevInstr is the instruction before statement i in its section, -1 when
// there is none.
func prevInstr(p *asm.Program, i int) int {
	sec := p.Stmts[i].Section
	for j := i - 1; j >= 0; j-- {
		st := &p.Stmts[j]
		if st.InMacroDef || st.Section != sec {
			continue
		}
		if st.Kind == asm.Instr && st.Mnemonic != "" {
			return j
		}
	}
	return -1
}

func checkIntoData(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	for b := range g.Blocks {
		bl := &g.Blocks[b]
		if bl.IntoData < 0 || bl.Exit != flow.ExitFall && bl.Exit != flow.ExitCond || !res.Reachable(bl.Last) {
			continue
		}
		last := &p.Stmts[bl.Last]
		if last.Kind != asm.Instr || last.Mnemonic == "" {
			continue
		}
		d := &p.Stmts[bl.IntoData]
		if !realData(p, bl.IntoData) {
			continue
		}
		what := d.Name
		if len(d.Labels) > 0 {
			what = d.Labels[0]
		}
		c.ReportStmt(bl.Last, fmt.Sprintf("execution falls through from %s into data (%s at line %d): the bytes there run as instructions", last.Mnemonic, what, lineOf(p, bl.IntoData)))
	}
}

// realData reports data that cannot be a hand-encoded instruction: a
// labeled directive (or a label naming data), a string, a fill or a move
// of the location counter.
func realData(p *asm.Program, i int) bool {
	st := &p.Stmts[i]
	if st.Kind == asm.Label || len(st.Labels) > 0 {
		return true
	}
	switch st.Name {
	case ".byte", ".word", ".short", ".hword", ".value", ".long", ".int", ".quad",
		".2byte", ".4byte", ".8byte", ".dc.b", ".dc.w", ".dc.l":
		return false
	}
	return true
}
