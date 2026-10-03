package cc

import (
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

// use is one general register read: the family and the operand that names
// it (-1 for an implicit read).
type use struct {
	r  flow.Reg
	op int
}

// reads returns the general register families statement i reads, each
// once with the first operand that names it, by the flow package's read
// model. The stack pointer is left out: the stack is tracked separately
// and every push reads it.
func reads(p *asm.Program, i int) []use {
	var out []use
next:
	for _, a := range flow.Reads(p, i) {
		if !a.Reg.IsGPR() || a.Reg == flow.RSP {
			continue
		}
		for _, u := range out {
			if u.r == a.Reg {
				continue next
			}
		}
		out = append(out, use{a.Reg, a.Op})
	}
	return out
}

// readsReg reports whether statement i reads family r, and which operand
// names it.
func readsReg(p *asm.Program, i int, r flow.Reg) (int, bool) {
	for _, u := range reads(p, i) {
		if u.r == r {
			return u.op, true
		}
	}
	return 0, false
}
