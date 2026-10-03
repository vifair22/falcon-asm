package flowrules

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodePartialRead,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "32/64-bit read of a register whose last write set only its low 8 or 16 bits (movw $x, %ax ... %eax).",
			Why: "Writing %ax or %al leaves the rest of %eax as it was; only a 32-bit write in 64-bit mode " +
				"zero-extends. Real-mode code that loads a value with movw and then uses %eax (an address " +
				"for addr32, a 32-bit compare, a CR or MSR write) uses whatever the upper half held before, " +
				"which on real hardware is rarely zero. The rule reports a 32- or 64-bit read when the last " +
				"write on every path was narrower and the upper bits are not known on some path. When the " +
				"upper half was zeroed or written separately (xorl %eax, %eax first) the bits are known and " +
				"nothing is reported. A push is not a use: saving a register keeps whatever it holds. The " +
				"fix widens a movw of an immediate to movl.",
			Bad:  "\t.code16\nf:\n\tmovw $0x1000, %ax\n\tmovl %eax, %ebx\n\tret",
			Good: "\t.code16\nf:\n\tmovl $0x1000, %eax\n\tmovl %eax, %ebx\n\tret",
		},
		Check: checkPartial,
	})
	lint.Register(&lint.Rule{
		Code:      CodeUninitRead,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Boot entry code reading a register nothing has set since the BIOS jumped to it (a rep count, an I/O port in %dx).",
			Why: "When the BIOS jumps to a boot sector the only register with a defined meaning is DL (the " +
				"boot drive); AX, BX, CX, BP and the high byte of DX hold whatever the BIOS last used. A rep " +
				"whose count was never loaded copies a random number of bytes, an in or out whose port was " +
				"never loaded talks to a random device. The rule only looks at the function the BIOS enters " +
				"(see segment-unset) and reports the first read of each register that no path from the " +
				"entry has written. SI and DI are left out because chain loaders pass a partition entry in " +
				"DS:SI and PnP BIOSes a pointer in ES:DI.",
			Bad:  "_start:\n\tcld\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x500, %di\n\trep stosb\n\t. = _start + 510\n\t.word 0xAA55",
			Good: "_start:\n\tcld\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x500, %di\n\tmovw $16, %cx\n\trep stosb\n\t. = _start + 510\n\t.word 0xAA55",
		},
		Check: checkUninit,
	})
}

func checkPartial(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, res := f.p, f.res
	var buf []regRead
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.Inst.Name == "push" || !res.Reachable(i) {
			continue
		}
		buf = appendReads(buf[:0], p, i)
		for _, u := range buf {
			if !u.r.IsGPR() || u.lo != 0 || u.width() < 32 {
				continue
			}
			v := res.In[i].Get(u.r)
			if v.Def < 0 {
				continue
			}
			wd, dop := narrowWrite(p, v.Def, u.r)
			if wd == 0 || wd >= u.width() {
				continue
			}
			upper := u.bits() &^ (uint64(1)<<uint(wd) - 1)
			if v.Mask&upper == upper || mergesOnPurpose(p, res, v.Def, u) {
				continue
			}
			name := u.name()
			msg := fmt.Sprintf("%%%s is read as %d bits, but its last write (%s at line %d) set only %%%s: bits %d-%d hold whatever they held before",
				name, u.width(), p.Stmts[v.Def].Mnemonic, lineOf(p, v.Def), u.r.Name(wd), wd, u.width()-1)
			var fixes []lint.Fix
			if fx, ok := widenFix(c, p, v.Def, dop, wd, u); ok {
				fixes = append(fixes, fx)
			}
			if u.op >= 0 {
				c.ReportOp(i, u.op, msg, fixes...)
			} else {
				c.ReportStmt(i, msg, fixes...)
			}
			break
		}
	}
}

// narrowWrite returns the width of the low part of family r that statement
// d writes as its destination, and the operand index, when that part
// starts at bit 0 and is 8 or 16 bits wide; 0 otherwise (an implicit write,
// a high-byte write, a full-width write).
func narrowWrite(p *asm.Program, d int, r flow.Reg) (int, int) {
	st := &p.Stmts[d]
	if st.Kind != asm.Instr || st.Inst == nil || len(st.Ops) == 0 || readOnly[st.Inst.Name] {
		return 0, -1
	}
	// orb $1, %al on a full value is an edit of the value, not a new
	// one: the upper bits are meant to be kept.
	if rmwDest[st.Inst.Name] {
		return 0, -1
	}
	k := len(st.Ops) - 1
	op := &st.Ops[k]
	if op.Kind != asm.OpReg {
		return 0, -1
	}
	fr, lo, hi, ok := flow.RegOf(op.RegName)
	if !ok || fr != r || lo != 0 {
		return 0, -1
	}
	if w := hi + 1; w == 8 || w == 16 {
		return w, k
	}
	return 0, -1
}

// mergesOnPurpose reports a narrow write over a value the code loaded at
// full width just before (movl (mem), %eax; movw $x, %ax): the upper half
// is the loaded one, kept deliberately.
func mergesOnPurpose(p *asm.Program, res *flow.Result, d int, u regRead) bool {
	prev := res.In[d].Get(u.r).Def
	if prev < 0 {
		return false
	}
	st := &p.Stmts[prev]
	if st.Kind != asm.Instr || len(st.Ops) == 0 {
		return true
	}
	op := &st.Ops[len(st.Ops)-1]
	r, lo, hi, ok := flow.RegOf(op.RegName)
	if op.Kind != asm.OpReg || !ok || r != u.r {
		// An implicit write (cpuid, a call, an int) of the whole family.
		return true
	}
	return lo == 0 && hi+1 >= u.width()
}

// widenFix turns "movw $imm, %ax" into "movl $imm, %eax" when the read is
// 32 bits: the one form where widening the write is obviously what the
// code meant and changes nothing else.
func widenFix(c *lint.Context, p *asm.Program, d, dop, wd int, u regRead) (lint.Fix, bool) {
	st := &p.Stmts[d]
	if st.Inst.Name != "mov" || wd != 16 || u.width() != 32 || len(st.Ops) != 2 || st.Ops[0].Kind != asm.OpImm {
		return lint.Fix{}, false
	}
	op := &st.Ops[dop]
	edits := []lintEdit{{op.Start, op.End, "%" + u.r.Name(32)}}
	switch st.Mnemonic {
	case "movw":
		edits = append(edits, lintEdit{st.MnemStart, st.MnemStart + len(st.Mnemonic), "movl"})
	case "mov":
	default:
		return lint.Fix{}, false
	}
	fx := lint.Fix{Title: fmt.Sprintf("Write all of %%%s", u.r.Name(32))}
	for _, e := range edits {
		fx.Edits = append(fx.Edits, c.ReplaceRange(e.start, e.end, e.text))
	}
	return fx, true
}

type lintEdit struct {
	start, end int
	text       string
}

// uninitFamilies are the registers the BIOS gives no meaning at a boot
// sector's entry (DL is the drive; SI and DI may carry chain-loader and
// PnP pointers, SP is the BIOS's stack).
var uninitFamilies = map[flow.Reg]bool{flow.RAX: true, flow.RBX: true, flow.RCX: true, flow.RDX: true, flow.RBP: true}

func checkUninit(c *lint.Context) {
	f := factsOf(c)
	if f == nil || f.bootFunc < 0 {
		return
	}
	p, g, res := f.p, f.g, f.res
	done := map[flow.Reg]bool{}
	for _, b := range g.Funcs[f.bootFunc].Blocks {
		stmts(g, b, func(i int) bool {
			st := &p.Stmts[i]
			if st.Kind != asm.Instr || st.Inst == nil || st.Inst.Name == "push" || !res.Reachable(i) {
				return true
			}
			for _, u := range readsOf(p, i) {
				if !uninitFamilies[u.r] || done[u.r] || res.In[i].Get(u.r).Def != flow.DefEntry {
					continue
				}
				// DL alone is the boot drive the BIOS passes.
				if u.r == flow.RDX && u.bits()&^0xFF == 0 {
					continue
				}
				done[u.r] = true
				msg := fmt.Sprintf("%s reads %%%s, which nothing has set since the BIOS entered this boot sector: it holds whatever the BIOS left", st.Mnemonic, u.name())
				switch {
				case u.r == flow.RCX && u.op < 0 && hasRep(p, i):
					msg = fmt.Sprintf("the count of this rep %s is %%%s, which nothing has set since the BIOS entered this boot sector", st.Mnemonic, u.name())
				case u.r == flow.RDX && (st.Inst.Name == "in" || st.Inst.Name == "out" || st.Inst.Name == "ins" || st.Inst.Name == "outs"):
					msg = fmt.Sprintf("the I/O port of this %s is %%dx, which nothing has set since the BIOS entered this boot sector (only DL, the boot drive, is defined)", st.Mnemonic)
				}
				if u.op >= 0 {
					c.ReportOp(i, u.op, msg)
				} else {
					c.ReportStmt(i, msg)
				}
			}
			return true
		})
	}
}
