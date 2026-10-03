package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// farJump is a direct far jump or call with a constant selector:
// `ljmp $SEL, $label`.
type farJump struct {
	stmt   int
	sel    int64
	target int // the first code statement at the target label; -1 when not in this document
	// protected is set when the jump certainly runs with CR0.PE set, so
	// its selector indexes the GDT: it is in 32- or 64-bit code (and no
	// CR0 write clearing PE precedes it in its block), or in 16-bit code
	// right after a CR0 write that sets PE. A real-mode far jump
	// (`ljmp $0x07C0, $start`) names a segment, not a selector.
	protected bool
}

func (ix *Index) farJumps() []farJump {
	var out []farJump
	for i := range ix.Prog.Stmts {
		s := &ix.Prog.Stmts[i]
		if s.Kind != asm.Instr || s.InMacroDef || !s.IsFar() || !(s.IsUncondJump() || s.IsCall()) || len(s.Ops) != 2 {
			continue
		}
		selOp, tgtOp := &s.Ops[0], &s.Ops[1]
		if selOp.Kind != asm.OpImm || !selOp.ImmVal.Known || tgtOp.Kind != asm.OpImm || selOp.Indirect {
			continue
		}
		fj := farJump{stmt: i, sel: selOp.ImmVal.V & 0xFFFF, target: -1}
		if name := strings.TrimSpace(tgtOp.Imm); ix.isLabel(name) {
			if li, ok := ix.labelStmt(name); ok {
				fj.target = ix.firstCode(li)
			}
		}
		pe, known := ix.peBefore(i)
		if !known {
			// Past the block: what the dataflow knows of CR0.PE, which
			// reaches 16-bit code that is several labels away from the
			// CR0 write.
			pe, known = ix.peAt(i)
		}
		switch {
		case s.Mode != asm.Mode16:
			fj.protected = !known || pe
		default:
			fj.protected = known && pe
		}
		out = append(out, fj)
	}
	return out
}

// peBefore looks back through statement i's block for a write to CR0 and
// reports the PE bit it leaves, when that bit is known.
func (ix *Index) peBefore(i int) (pe, known bool) {
	for k := i - 1; k >= 0; k-- {
		s := &ix.Prog.Stmts[k]
		if ev, ok := ix.eventAt(k, evCR); ok && ix.trk.events[ev].cr == "cr0" {
			v := ix.trk.events[ev].src
			if v.known&1 != 0 {
				return v.val&1 != 0, true
			}
			return false, false
		}
		if len(s.Labels) > 0 || s.Kind == asm.Instr && (s.IsUncondJump() || s.IsRet() || s.IsIret()) {
			return false, false
		}
	}
	return false, false
}

// eventAt finds the tracker event of kind recorded at statement i.
func (ix *Index) eventAt(i int, kind string) (int, bool) {
	for _, k := range ix.evAt[i] {
		if ix.trk.events[k].kind == kind {
			return k, true
		}
	}
	return 0, false
}

// selectorUse is a constant selector loaded into a segment register: CS by
// a far jump, the others by `movw $SEL, %ax; movw %ax, %ds`.
type selectorUse struct {
	stmt int    // the far jump, or the mov to the segment register
	op   int    // operand index of the selector (far jump), -1 for a register move
	sreg string // "cs", "ds" ...
	sel  int64
}

// selectorUses lists every constant selector the document loads in
// protected mode. 16-bit code counts only when its block is the target of a
// protected-mode far jump (thunk_16 after `ljmp $SEL_CODE16, $thunk_16`);
// otherwise `movw %ax, %ds` in .code16 is a real-mode segment load.
func (ix *Index) selectorUses() []selectorUse {
	pmTargets := map[int]bool{}
	var out []selectorUse
	for _, fj := range ix.fjs {
		if !fj.protected || fj.sel&^3 == 0 {
			continue
		}
		out = append(out, selectorUse{stmt: fj.stmt, op: 0, sreg: "cs", sel: fj.sel})
		if fj.target >= 0 {
			pmTargets[ix.blockHead(fj.target)] = true
		}
	}
	for _, e := range ix.trk.events {
		if e.kind != evSeg || !e.src.full(16) || e.sreg == "cs" {
			continue
		}
		s := &ix.Prog.Stmts[e.stmt]
		pe, known := ix.peAt(e.stmt)
		if s.Mode == asm.Mode16 && (known && !pe || !known && !pmTargets[ix.blockHead(e.stmt)]) {
			continue
		}
		out = append(out, selectorUse{stmt: e.stmt, op: -1, sreg: e.sreg, sel: int64(e.src.val & 0xFFFF)})
	}
	return out
}

// blockHead is the statement opening the block statement i is in: the
// nearest statement at or above it that defines a label.
func (ix *Index) blockHead(i int) int {
	for k := i; k >= 0; k-- {
		if len(ix.Prog.Stmts[k].Labels) > 0 {
			return k
		}
	}
	return 0
}

// related is the location of a table entry, for a finding's Related list.
func (ix *Index) related(e Entry) []protocol.Location {
	if e.Stmt < 0 {
		return nil
	}
	s := &ix.Prog.Stmts[e.Stmt]
	return []protocol.Location{{URI: ix.Prog.Doc.URI, Range: ix.Prog.Range(s.Start, s.End)}}
}

// modeOfDescriptor is the code size a code descriptor runs at.
func modeOfDescriptor(d hwdb.Descriptor) asm.Mode {
	switch {
	case d.L:
		return asm.Mode64
	case d.DB:
		return asm.Mode32
	}
	return asm.Mode16
}

func checkFarJumpMode(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	g := ix.GDT()
	if g == nil {
		return
	}
	for _, fj := range ix.fjs {
		if !fj.protected || fj.target < 0 || fj.sel&^3 == 0 || fj.sel&4 != 0 {
			continue
		}
		e, ok := g.entryAt(int(fj.sel >> 3))
		if !ok || !e.Known {
			continue
		}
		d := hwdb.ParseDescriptor(e.Lo, nil)
		if !d.S || !d.Code || d.L && d.DB {
			continue // not a code descriptor: selector-type and descriptor-invalid say so
		}
		want := modeOfDescriptor(d)
		got := ix.Prog.Stmts[fj.target].Mode
		if want == got {
			continue
		}
		s := &ix.Prog.Stmts[fj.stmt]
		target := strings.TrimSpace(s.Ops[1].Imm)
		c.ReportFinding(lint.Finding{
			Range: ix.Prog.Range(s.Ops[0].Start, s.Ops[1].End),
			Message: fmt.Sprintf("selector %#x is %d-bit code (GDT entry %d), but %s is assembled as .code%d: "+
				"the processor will decode it as %d-bit instructions", fj.sel, want, e.Index, target, got, want),
			Related: ix.related(e),
		})
	}
}

func checkSelectorType(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	g := ix.GDT()
	if g == nil {
		return
	}
	for _, u := range ix.uses {
		if u.sel&4 != 0 || u.sel&^3 == 0 {
			continue // LDT selectors and the null selector are not checked
		}
		idx := int(u.sel >> 3)
		report := func(msg string, rel []protocol.Location) {
			s := &ix.Prog.Stmts[u.stmt]
			r := ix.Prog.Range(s.Start, s.End)
			if u.op >= 0 {
				r = ix.Prog.Range(s.Ops[u.op].Start, s.Ops[u.op].End)
			}
			c.ReportFinding(lint.Finding{Range: r, Message: msg, Related: rel})
		}
		if g.LimitKnown && int64(idx)*8+7 > g.Limit {
			report(fmt.Sprintf("selector %#x indexes GDT entry %d, beyond the GDT limit %#x (%d entries): loading %%%s raises #GP",
				u.sel, idx, g.Limit, (g.Limit+1)/8, u.sreg), nil)
			continue
		}
		e, ok := g.entryAt(idx)
		if !ok || !e.Known || e.Lo == 0 {
			if ok && e.Known {
				report(fmt.Sprintf("selector %#x names GDT entry %d, which is all zero: loading %%%s raises #GP", u.sel, idx, u.sreg), ix.related(e))
			}
			continue
		}
		d := hwdb.ParseDescriptor(e.Lo, nil)
		var problem string
		switch u.sreg {
		case "cs":
			if !d.S || !d.Code {
				problem = "a far jump needs a code descriptor"
			}
		case "ss":
			if !d.S || d.Code || d.Type&2 == 0 {
				problem = "SS needs a writable data descriptor"
			}
		default:
			if !d.S || d.Code && d.Type&2 == 0 {
				problem = "%" + u.sreg + " needs a data or readable code descriptor"
			}
		}
		if problem == "" {
			continue
		}
		kind := "system descriptor"
		if d.S {
			kind = d.TypeNm
		}
		report(fmt.Sprintf("selector %#x names GDT entry %d (%s): %s; the load raises #GP", u.sel, idx, kind, problem), ix.related(e))
	}
}

func checkDescriptorInvalid(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	used := map[int][]selectorUse{}
	if g := ix.GDT(); g != nil {
		for _, u := range ix.uses {
			if u.sel&4 == 0 {
				used[int(u.sel>>3)] = append(used[int(u.sel>>3)], u)
			}
		}
	}
	g := ix.GDT()
	for _, t := range ix.tables {
		// Tables found only by their shape are for hover; the checks
		// stay with tables an lgdt loads, or ones named as a GDT.
		if t.IDT || t.Run < 0 || !t.Loaded && !strings.Contains(strings.ToLower(t.Label), "gdt") {
			continue
		}
		for _, e := range t.Entries {
			if !e.Known || e.Lo == 0 || e.Stmt < 0 {
				continue
			}
			dec := t.decode(e, ix.code64)
			var msgs []string
			msgs = append(msgs, dec.Problems...)
			d := hwdb.ParseDescriptor(e.Lo, nil)
			if !d.P && t == g {
				for _, u := range used[e.Index] {
					msgs = append(msgs, fmt.Sprintf("not present, but selector %#x loads it into %%%s at line %d (#NP)",
						u.sel, u.sreg, ix.Prog.Stmts[u.stmt].Line+1))
					break
				}
			}
			for _, m := range msgs {
				s, l := &ix.Prog.Stmts[e.Stmt], &ix.Prog.Stmts[max(e.Last, e.Stmt)]
				c.ReportRange(s.Start, l.End, fmt.Sprintf("GDT entry %d (selector %#x): %s", e.Index, e.Index*8, m))
			}
		}
	}
}

func checkGDTLimit(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	for _, t := range ix.tables {
		if !t.Loaded || !t.LimitKnown || t.Limit == 0 || t.IDT && t.Run < 0 {
			continue
		}
		unit := int64(8)
		if t.IDT && ix.code64 && ix.Prog.Stmts[t.LoadStmt].Mode == asm.Mode64 {
			unit = 16
		}
		if (t.Limit+1)%unit == 0 {
			continue
		}
		what := "GDT"
		if t.IDT {
			what = "IDT"
		}
		msg := fmt.Sprintf("%s limit %#x is not a multiple of %d minus 1: the limit is the offset of the last valid byte", what, t.Limit, unit)
		if t.Limit%unit == 0 {
			msg = fmt.Sprintf("%s limit %#x covers %d entries and one byte: the limit is the last valid byte, so it should be %#x (size - 1)",
				what, t.Limit, t.Limit/unit, t.Limit-1)
		}
		c.ReportStmt(t.DescStmt, msg)
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:    "far-jump-mode",
		Section: 2,
		Default: lint.Warning,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "Far jump into code assembled for a different mode than the selector's descriptor runs it in.",
			Why: "`ljmp $SEL, $label` loads CS from the GDT entry SEL names, and the descriptor's D and L bits, not " +
				"the .code16/.code32/.code64 directive, decide how the processor decodes what it finds at label. " +
				"A 32-bit code selector in front of a .code16 label (or the reverse) runs the bytes with the wrong " +
				"operand and address size, which usually triple faults a few instructions later with nothing on " +
				"the screen. The rule finds the GDT through the lgdt in the same file, decodes the entry and " +
				"compares; the finding links to the entry. Jumps in 16-bit code count only right after a CR0 write " +
				"that sets PE, so a real-mode `ljmp $0x07C0, $start` is never read as a selector.",
			Bad: "\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n\tljmpl $0x08, $pm\n\t.code16\npm:\n\thlt\n" +
				"gdt:\t.quad 0\n\t.quad 0x00CF9A000000FFFF\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt",
			Good: "\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n\tljmpl $0x08, $pm\n\t.code32\npm:\n\thlt\n" +
				"gdt:\t.quad 0\n\t.quad 0x00CF9A000000FFFF\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt",
		},
		Check: checkFarJumpMode,
	})
	lint.Register(&lint.Rule{
		Code:    "selector-type",
		Section: 5,
		Default: lint.Error,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "Selector names a GDT entry beyond the limit or of the wrong type for the segment register it is loaded into.",
			Why: "CS must be loaded with a code descriptor, SS with a writable data descriptor, and DS, ES, FS " +
				"and GS with data or readable code; a selector past the GDT limit or naming a zero entry faults " +
				"too. Every one of these is a #GP at the load, which in a boot path without an IDT is a reset. " +
				"Checked for constant selectors: far jumps (`ljmp $SEL, $label`) and the two-step " +
				"`movw $SEL, %ax; movw %ax, %ds` (across labels as far as the dataflow proves the value), in " +
				"protected-mode code only (16-bit code counts when the dataflow proves CR0.PE set, or when a " +
				"protected-mode far jump leads to it), against the single GDT the file loads.",
			Bad:  "\t.code32\n\tmovw $0x08, %ax\n\tmovw %ax, %ss\n\tlgdt gdt_desc\ngdt:\t.quad 0\n\t.quad 0x00CF9A000000FFFF\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt",
			Good: "\t.code32\n\tmovw $0x10, %ax\n\tmovw %ax, %ss\n\tlgdt gdt_desc\ngdt:\t.quad 0\n\t.quad 0x00CF9A000000FFFF\n\t.quad 0x00CF92000000FFFF\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt",
		},
		Check: checkSelectorType,
	})
	lint.Register(&lint.Rule{
		Code:    "descriptor-invalid",
		Section: 5,
		Default: lint.Warning,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "GDT descriptor that cannot be loaded: L and D both set, a reserved system type, a short TSS, or not present but used.",
			Why: "A code descriptor with both L and D set is reserved and faults when loaded into CS in long mode; " +
				"a reserved system type or a TSS shorter than 0x67 bytes faults at ltr; a descriptor with P clear " +
				"raises #NP the moment a selector naming it is loaded. The entries are decoded from the .quad " +
				"(or .word/.byte) data of a GDT the file loads with lgdt or names gdt. Hover on an entry shows " +
				"the decoded fields.",
			Bad:  "gdt:\t.quad 0\n\t.quad 0x00EF9A000000FFFF   # L and D\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt\n\tlgdt gdt_desc",
			Good: "gdt:\t.quad 0\n\t.quad 0x00AF9A000000FFFF   # L only: 64-bit code\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt\n\tlgdt gdt_desc",
		},
		Check: checkDescriptorInvalid,
	})
	lint.Register(&lint.Rule{
		Code:    "gdt-limit",
		Section: 5,
		Default: lint.Warning,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "GDT or IDT pseudo-descriptor limit is not size - 1.",
			Why: "The limit lgdt and lidt take is the offset of the table's last valid byte, so a table of n " +
				"eight-byte entries has limit 8n - 1. Writing the size instead (`gdt_end - gdt`) makes one byte " +
				"past the table addressable, which a selector can then reach; any value that is not 7 modulo 8 " +
				"cuts the last descriptor short or exposes part of the next one.",
			Bad:  "gdt_desc:\n\t.word gdt_end - gdt\n\t.long gdt\n\tlgdt gdt_desc\ngdt:\t.quad 0\ngdt_end:",
			Good: "gdt_desc:\n\t.word gdt_end - gdt - 1\n\t.long gdt\n\tlgdt gdt_desc\ngdt:\t.quad 0\ngdt_end:",
		},
		Check: checkGDTLimit,
	})
}
