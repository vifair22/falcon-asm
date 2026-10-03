package encoding

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// rel8Only reports branches with no longer form: loop, loope, loopne and
// jcxz/jecxz/jrcxz. GAS relaxes jmp and jcc to rel16/rel32 when a target
// is far, but these have nowhere to grow.
func rel8Only(inst *isa.Instruction) bool {
	if inst == nil || inst.Category != "COND_BR" || len(inst.Forms) == 0 {
		return false
	}
	for _, f := range inst.Forms {
		if f.Operands != "rel8" {
			return false
		}
	}
	return true
}

// checkBranchRange reports rel8-only branches whose target, placed by the
// assembler listing, is more than -128..+127 bytes from the end of the
// branch. Both addresses must be known, in the same section, and the branch
// must be the only instruction on its line (the listing places lines, not
// statements).
func checkBranchRange(c *lint.Context) {
	if c.Prog == nil || c.Listing == nil || len(c.Listing.Listing) == 0 {
		return
	}
	byLine := make(map[int]int, len(c.Listing.Listing))
	for i := range c.Listing.Listing {
		byLine[c.Listing.Listing[i].Line] = i
	}
	p := c.Prog
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if s.Kind != asm.Instr || s.InMacroDef || !rel8Only(s.Inst) || len(s.Ops) != 1 {
			continue
		}
		li, ok := byLine[s.Line]
		if !ok || !onlyInstr(p, i) {
			continue
		}
		from := &c.Listing.Listing[li]
		if !from.HasOffset || from.Unknown || len(from.Bytes) == 0 {
			continue
		}
		t, ok := p.LabelTarget(i, &s.Ops[0])
		if !ok {
			continue
		}
		to, ok := labelAddress(p, c.Listing.Listing, byLine, t, from.Section)
		if !ok {
			continue
		}
		disp := int64(to) - int64(from.Offset+uint64(len(from.Bytes)))
		if disp >= -128 && disp <= 127 {
			continue
		}
		c.ReportOp(i, 0, fmt.Sprintf("%s reaches only -128..+127 bytes, but %s is %d bytes away; "+
			"use a dec/jnz pair (or jcxz to a near jmp) instead", s.Mnemonic, s.Ops[0].Symbol, disp))
	}
}

// onlyInstr reports whether statement i is the only instruction or data
// statement on its line.
func onlyInstr(p *asm.Program, i int) bool {
	for _, k := range p.StmtAt(p.Stmts[i].Line) {
		if k == i {
			continue
		}
		switch p.Stmts[k].Kind {
		case asm.Instr, asm.Directive, asm.MacroCall:
			return false
		}
	}
	return true
}

// labelAddress is the address of the label statement t defines: its
// line's offset when the label opens the line, else the offset of the
// first line after it that emits something, provided nothing between
// them can move the location counter.
func labelAddress(p *asm.Program, lst []toolchain.ListingLine, byLine map[int]int, t int, section string) (uint64, bool) {
	s := &p.Stmts[t]
	if s.Section != section {
		return 0, false
	}
	at := p.StmtAt(s.Line)
	if len(at) == 0 || at[0] != t {
		return 0, false // something precedes the label on its line
	}
	li, ok := byLine[s.Line]
	if !ok {
		return 0, false
	}
	for k := li; k < len(lst); k++ {
		l := &lst[k]
		if l.Section != section || l.Unknown {
			return 0, false
		}
		if l.HasOffset {
			return l.Offset, true
		}
		// A line that emits nothing may still be a directive that moves
		// the location counter (.org, ". = x", .skip with no listing
		// bytes); only plain labels and blank lines are stepped over.
		for _, j := range p.StmtAt(l.Line) {
			if kind := p.Stmts[j].Kind; kind != asm.Label && kind != asm.Empty && kind != asm.Cpp {
				return 0, false
			}
		}
	}
	return 0, false
}
