package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

// Hover returns markdown about the hardware meaning of what sits at byte
// offset off, and the byte range it describes; "" when there is nothing to
// add. The engine appends it to the ordinary hover. Block-local, it covers:
//
//   - a descriptor or gate in a GDT/IDT, and an entry of a page table
//   - `int $N` with AH (and AL) set in the same block: the BIOS service
//   - `in`/`out` with an immediate port
//   - an immediate that a consumer in the same block uses: the port in
//     `movw $0x3F8, %dx`, the MSR in `movl $0xC0000080, %ecx`, the CPUID
//     leaf in `movl $7, %eax`, a CR0/CR4 value or the bits an orl sets
//   - a constant selector: the GDT entry it names
//
// With a flow result (NewIndexFlow) the values reach across labels, and
// more is said (regvalues.go): the MSR an rdmsr/wrmsr names and the value a
// wrmsr writes, the CPUID leaf, the value a mov to a control register or
// xsetbv writes, the port of `in`/`out %dx`, every BIOS service an int may
// call when paths disagree about AH, and the known value of a register
// operand itself.
func (ix *Index) Hover(db *hwdb.DB, off int) (string, int, int) {
	i := ix.stmtAt(off)
	if i < 0 {
		return "", 0, 0
	}
	s := &ix.Prog.Stmts[i]
	switch s.Kind {
	case asm.Directive:
		md := ix.dataHover(i)
		return md, s.Start, s.End
	case asm.Instr:
	default:
		return "", 0, 0
	}
	opIdx := -1
	for k := range s.Ops {
		if s.Ops[k].Start <= off && off < s.Ops[k].End {
			opIdx = k
		}
	}
	onMnem := off >= s.MnemStart && off < s.MnemStart+len(s.Mnemonic)
	if opIdx < 0 && !onMnem {
		return "", 0, 0
	}
	start, end := s.MnemStart, s.MnemStart+len(s.Mnemonic)
	if opIdx >= 0 {
		start, end = s.Ops[opIdx].Start, s.Ops[opIdx].End
	}
	var parts []string
	add := func(md string) {
		if md != "" {
			parts = append(parts, md)
		}
	}
	// The instruction itself consumes constants.
	lastOp := opIdx >= 0 && opIdx == len(s.Ops)-1
	for _, k := range ix.evAt[i] {
		e := ix.trk.events[k]
		switch {
		case e.kind == evInt && (onMnem || opIdx == 0):
			add(ix.intHover(db, e))
		case e.kind == evPort && e.imm && (onMnem || s.Ops[opIdx].Kind == asm.OpImm):
			add(portHover(db, e, uint64(e.n)))
		case e.kind == evPort && !e.imm && onMnem && e.d.full(16):
			add(portHover(db, e, e.d.val&0xFFFF))
		case e.kind == evMSR && onMnem:
			add(msrEventHover(db, e))
		case e.kind == evCPUID && onMnem && e.a.full(32):
			add(db.CPUIDHover(e.a.val&0xFFFFFFFF, cpuidSub(e)))
		case e.kind == evCR && (onMnem || lastOp && s.Ops[opIdx].Kind == asm.OpReg):
			add(crWriteHover(e, ix.Prog.Stmts[e.stmt].Mode))
		}
	}
	if opIdx >= 0 && s.Ops[opIdx].Kind == asm.OpReg {
		add(ix.regHover(db, i, s.Ops[opIdx].RegName))
	}
	if opIdx >= 0 && s.Ops[opIdx].Kind == asm.OpImm {
		op := &s.Ops[opIdx]
		// A far jump's selector.
		for _, fj := range ix.fjs {
			if fj.stmt == i && opIdx == 0 {
				add(ix.selectorHover(fj.sel, fj.protected))
			}
		}
		// Constants this statement feeds into a consumer.
		seen := map[string]bool{}
		for _, r := range ix.trk.roles[i] {
			md := ix.roleHover(db, s, op, r)
			if md != "" && !seen[md] {
				seen[md] = true
				add(md)
			}
		}
		if len(parts) == 0 {
			add(ix.bareImmHover(db, s, op, i))
		}
	}
	return strings.Join(parts, "\n\n---\n\n"), start, end
}

// dataHover decodes a data statement inside a descriptor or page table.
func (ix *Index) dataHover(i int) string {
	if t, e, ok := ix.tableOf(i); ok && e.Known {
		var b strings.Builder
		name := "GDT"
		if t.IDT {
			name = "IDT"
		}
		where := ""
		if t.Label != "" {
			where = " of `" + t.Label + "`"
		}
		if t.IDT {
			fmt.Fprintf(&b, "**%s vector %d**%s\n\n", name, e.Index/(e.Size/8), where)
		} else {
			fmt.Fprintf(&b, "**%s entry %d** (selector %#x)%s\n\n", name, e.Index, e.Index*8, where)
		}
		writeDecoded(&b, t.decode(e, ix.code64))
		return b.String()
	}
	if pt, es := ix.pageEntries(i); pt != nil {
		var parts []string
		for _, e := range es {
			if md := pteHover(pt, e); md != "" {
				parts = append(parts, md)
			}
		}
		if len(parts) > 0 {
			return fmt.Sprintf("Page table `%s`\n\n%s", pt.label, strings.Join(parts, "\n\n"))
		}
	}
	return ""
}

// writeDecoded renders a decoded value: summary, fields, problems, notes.
func writeDecoded(b *strings.Builder, d *hwdb.Decoded) {
	b.WriteString(d.Summary)
	if len(d.Fields) > 0 {
		var f []string
		for _, x := range d.Fields {
			f = append(f, "`"+x.Text+"`")
		}
		fmt.Fprintf(b, "\n\n%s", strings.Join(f, " "))
	}
	for _, p := range d.Problems {
		fmt.Fprintf(b, "\n\n- **problem:** %s", p)
	}
	for _, n := range d.Notes {
		fmt.Fprintf(b, "\n\n- %s", n)
	}
}

func biosHover(db *hwdb.DB, e event) string {
	if e.a.known&0xFF00 == 0xFF00 {
		return db.BIOSHover(uint64(e.n), e.a.val>>8&0xFF, e.a.val&0xFF, e.a.known&0xFF == 0xFF)
	}
	// A vector that takes no function number (INT 12h, INT 19h) needs no AH.
	if svc, ok := db.LookupBIOS(uint64(e.n), 0, 0, false); ok && svc.AH == nil {
		return db.BIOSHover(uint64(e.n), 0, 0, false)
	}
	return ""
}

func portHover(db *hwdb.DB, e event, port uint64) string {
	md := db.PortHover(port, int(e.width), e.write)
	if md == "" {
		return ""
	}
	if e.write && e.width == 8 && e.a.known&0xFF == 0xFF {
		if name, ok := db.PortIndex(port, e.a.val&0xFF); ok {
			md += fmt.Sprintf("\n\nwrites %#02x: selects %s", e.a.val&0xFF, name)
		}
	}
	return md
}

// roleHover explains what an immediate turned out to be for.
func (ix *Index) roleHover(db *hwdb.DB, s *asm.Stmt, op *asm.Operand, r role) string {
	e := ix.trk.events[r.ev]
	switch e.kind {
	case evPort:
		if r.reg == "d" && e.d.full(16) {
			return portHover(db, e, e.d.val&0xFFFF)
		}
	case evMSR:
		if !e.c.full(32) {
			return ""
		}
		index := e.c.val & 0xFFFFFFFF
		var val *uint64
		if e.write && e.a.full(32) && e.d.full(32) {
			v := e.d.val<<32 | e.a.val&0xFFFFFFFF
			val = &v
		}
		return db.MSRHover(index, val)
	case evCPUID:
		if !e.a.full(32) {
			return ""
		}
		sub := uint64(0)
		if e.c.full(32) {
			sub = e.c.val & 0xFFFFFFFF
		}
		return db.CPUIDHover(e.a.val&0xFFFFFFFF, sub)
	case evInt:
		if md := biosHover(db, e); md != "" || r.reg != "a" {
			return md
		}
		// Paths disagree about AH at the int; this statement's own AH is
		// the service on its path.
		if ah, ok := immToAH(s, op); ok {
			return db.BIOSHover(uint64(e.n), ah, 0, false)
		}
	case evSeg:
		if e.src.full(16) {
			return ix.selectorHover(int64(e.src.val&0xFFFF), s.Mode != asm.Mode16)
		}
	case evCR:
		return crHover(e, s, op)
	}
	return ""
}

// crHover decodes a value written to a control register: the whole value
// when it is known, else the bits this statement sets or clears.
func crHover(e event, s *asm.Stmt, op *asm.Operand) string {
	kind := crKind[e.cr]
	if e.cr == "cr3" {
		kind = "cr3"
	}
	if kind == "" {
		return ""
	}
	name := strings.ToUpper(kind)
	v := e.src
	if e.cr == "xcr0" {
		v = rv{known: e.d.known<<32 | e.a.known&0xFFFFFFFF, val: e.d.val<<32 | e.a.val&0xFFFFFFFF}
	}
	if v.full(32) {
		var b strings.Builder
		fmt.Fprintf(&b, "**%s** value written: %#x\n\n", name, v.val)
		if d, ok := hwdb.Decode(kind, v.val); ok {
			writeDecoded(&b, d)
		}
		return b.String()
	}
	if !op.ImmVal.Known {
		return ""
	}
	imm := uint64(op.ImmVal.V) & 0xFFFFFFFF
	verb, bits := "sets", imm
	if s.Inst.Name == "and" {
		verb, bits = "clears", ^imm&0xFFFFFFFF
	}
	d, ok := hwdb.Decode(kind, bits)
	if !ok || len(d.Fields) == 0 {
		return fmt.Sprintf("**%s**: %s bits %#x", name, verb, bits)
	}
	var names []string
	for _, f := range d.Fields {
		names = append(names, "`"+f.Name+"`")
	}
	return fmt.Sprintf("**%s**: %s %s (%#x)", name, verb, strings.Join(names, " "), bits)
}

// selectorHover names the GDT entry a selector loads. In real mode the
// value is a segment, which hover says rather than guess.
func (ix *Index) selectorHover(sel int64, protected bool) string {
	g := ix.GDT()
	if g == nil || !protected {
		return ""
	}
	if sel&4 != 0 {
		return fmt.Sprintf("**Selector %#x**: LDT, index %d, RPL %d", sel, sel>>3, sel&3)
	}
	e, ok := g.entryAt(int(sel >> 3))
	if !ok || !e.Known {
		return fmt.Sprintf("**Selector %#x**: GDT index %d, RPL %d (beyond the entries laid out in `%s`)", sel, sel>>3, sel&3, g.Label)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Selector %#x**: GDT entry %d of `%s`, RPL %d\n\n", sel, e.Index, g.Label, sel&3)
	writeDecoded(&b, g.decode(e, ix.code64))
	return b.String()
}

// bareImmHover covers constants that are unambiguous without a consumer in
// the block: an MSR index in the architectural or AMD ranges moved into
// ECX, a port number of 0x100 or more moved into DX (below that, DX more
// often holds a drive or a head for INT 13h).
func (ix *Index) bareImmHover(db *hwdb.DB, s *asm.Stmt, op *asm.Operand, i int) string {
	if !s.IsGPRMove() || len(s.Ops) != 2 || &s.Ops[0] != op || !op.ImmVal.Known {
		return ""
	}
	v := uint64(op.ImmVal.V)
	switch s.Ops[1].RegName {
	case "ecx", "rcx":
		if v >= 0x40000000 && v <= 0xFFFFFFFF {
			if _, ok := db.LookupMSR(v); ok {
				return db.MSRHover(v, nil)
			}
		}
	case "dx":
		for _, r := range ix.trk.roles[i] {
			if ix.trk.events[r.ev].kind == evInt {
				return ""
			}
		}
		if v >= 0x100 && v <= 0xFFFF {
			return db.PortHover(v, 0, false)
		}
	}
	return ""
}

// immToAH is the AH an immediate move writes: `movb $0x42, %ah`, or the
// high byte of `movw $0x0201, %ax`.
func immToAH(s *asm.Stmt, op *asm.Operand) (uint64, bool) {
	if !s.IsGPRMove() || len(s.Ops) != 2 || &s.Ops[0] != op || !op.ImmVal.Known {
		return 0, false
	}
	v := uint64(op.ImmVal.V)
	switch s.Ops[1].RegName {
	case "ah":
		return v & 0xFF, true
	case "ax", "eax", "rax":
		return v >> 8 & 0xFF, true
	}
	return 0, false
}
