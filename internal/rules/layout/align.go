package layout

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// table is a structure whose address the CPU is given, found from the
// instruction that hands it over.
type table struct {
	label string
	off   int64  // offset from label (`.long gdt + 8` style bases)
	what  string // "the GDT", "the IDT", "page table"
	align int64
	via   int // statement that loads it (lgdt, lidt, mov to %cr3); -1 for a table found through an entry
	from  string
}

// descTables finds the GDT and IDT: `lgdt X` / `lidt X` where X is a label
// whose pseudo-descriptor is `.word limit` followed by `.long base` (or
// `.quad base`), and base is a label. The SDM (vol. 3, 3.5.1 and 6.10)
// recommends an 8-byte aligned base for both; long mode IDT gates are 16
// bytes, so there the IDT wants 16.
func descTables(p *asm.Program) []table {
	var out []table
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.InMacroDef || len(st.Ops) != 1 {
			continue
		}
		name := st.Inst.Name
		if name != "lgdt" && name != "lidt" {
			continue
		}
		desc, ok := memLabel(p, &st.Ops[0])
		if !ok {
			continue
		}
		base, off, ok := pseudoDescriptorBase(p, desc)
		if !ok {
			continue
		}
		t := table{label: base, off: off, what: "the GDT", align: 8, via: i, from: name}
		if name == "lidt" {
			t.what = "the IDT"
			if st.Mode == asm.Mode64 {
				t.align = 16
			}
		}
		out = append(out, t)
	}
	return out
}

// memLabel is the label a memory operand names with no register part
// (`gdt_desc`, `%cs:gdt_desc`, `gdt_desc(%rip)`).
func memLabel(p *asm.Program, op *asm.Operand) (string, bool) {
	if op.Kind != asm.OpMem || op.Index != "" || (op.Base != "" && !op.RIPRel) {
		return "", false
	}
	base, off, ok := parseRef(op.Disp, macroLookup(p), false)
	if !ok || base == "" || off != 0 {
		return "", false
	}
	_, isLabel := p.Labels[base]
	return base, isLabel
}

// dataItems collects up to n data items (size, expression) from the
// statement defining label onwards, stopping at anything that is not
// data, a blank, a cpp line or a label-only line.
func dataItems(p *asm.Program, label string, n int) []dataItem {
	idx, ok := p.Labels[label]
	if !ok {
		return nil
	}
	var out []dataItem
	for i := idx; i < len(p.Stmts) && len(out) < n; i++ {
		st := &p.Stmts[i]
		switch st.Kind {
		case asm.Empty, asm.Cpp, asm.Label:
			continue
		case asm.Directive:
			size := dataSizes[st.Name]
			if size == 0 {
				return out
			}
			for _, a := range st.Args {
				out = append(out, dataItem{size: size, expr: a, stmt: i})
			}
		default:
			return out
		}
	}
	return out
}

type dataItem struct {
	size int
	expr string
	stmt int
}

func pseudoDescriptorBase(p *asm.Program, desc string) (string, int64, bool) {
	items := dataItems(p, desc, 2)
	if len(items) < 2 || items[0].size != 2 || (items[1].size != 4 && items[1].size != 8) {
		return "", 0, false
	}
	base, off, ok := parseRef(items[1].expr, macroLookup(p), false)
	if !ok || base == "" {
		return "", 0, false
	}
	if _, isLabel := p.Labels[base]; !isLabel {
		return "", 0, false
	}
	return base, off, true
}

// pageTables finds the page table hierarchy: the table whose address is
// moved into %cr3, then every table an entry of a known table points at
// (`.quad pdpt + 3`, `.long pt | 7`). The register feeding %cr3 is traced
// back through straight-line code only, and only through instructions that
// cannot write it implicitly; anything else ends the search unanswered.
func pageTables(p *asm.Program) []table {
	var out []table
	seen := map[string]bool{}
	var queue []string
	add := func(t table) {
		if seen[t.label] {
			return
		}
		seen[t.label] = true
		out = append(out, t)
		queue = append(queue, t.label)
	}
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.InMacroDef || !st.IsGPRMove() || len(st.Ops) != 2 {
			continue
		}
		dst, src := &st.Ops[1], &st.Ops[0]
		if dst.Kind != asm.OpReg || dst.RegName != "cr3" || src.Kind != asm.OpReg {
			continue
		}
		if l, off, ok := traceReg(p, i, regFamily(src.RegName)); ok {
			add(table{label: l, off: off, what: "page table", align: 4096, via: i, from: "%cr3"})
		}
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, it := range tableEntries(p, parent) {
			l, off, ok := parseRef(it.expr, macroLookup(p), true)
			if !ok || l == "" || off < 0 || off >= 4096 {
				continue
			}
			if _, isLabel := p.Labels[l]; !isLabel {
				continue
			}
			add(table{label: l, what: "page table", align: 4096, via: -1, from: parent})
		}
	}
	return out
}

// tableEntries are the 4- and 8-byte items from a table's label up to the
// next non-local label, at most one table's worth.
func tableEntries(p *asm.Program, label string) []dataItem {
	idx := p.Labels[label]
	var out []dataItem
	for i := idx; i < len(p.Stmts) && len(out) < 1024; i++ {
		st := &p.Stmts[i]
		if i > idx && hasNamedLabel(st) {
			break
		}
		if st.Kind != asm.Directive {
			continue
		}
		if size := dataSizes[st.Name]; size == 4 || size == 8 {
			for _, a := range st.Args {
				out = append(out, dataItem{size: size, expr: a, stmt: i})
			}
		}
	}
	return out
}

func hasNamedLabel(st *asm.Stmt) bool {
	for _, l := range st.Labels {
		if l != "" && (l[0] < '0' || l[0] > '9') {
			return true
		}
	}
	return false
}

// implicitWriters write general registers they do not name. Seeing one
// between the load and the %cr3 write ends the trace.
var implicitWriters = map[string]bool{
	"cpuid": true, "rdmsr": true, "rdtsc": true, "rdtscp": true, "rdpmc": true, "xgetbv": true,
	"mul": true, "div": true, "idiv": true, "cwd": true, "cdq": true, "cqo": true, "cbw": true,
	"cwde": true, "cdqe": true, "popa": true, "lods": true, "stos": true, "movs": true,
	"scas": true, "cmps": true, "ins": true, "outs": true, "loop": true, "enter": true, "leave": true,
}

// traceReg walks back from statement at to the instruction that loaded the
// register family reg, and returns the label it loaded: `mov $tbl, %reg`
// or `lea tbl, %reg`.
func traceReg(p *asm.Program, at int, reg string) (string, int64, bool) {
	if reg == "" {
		return "", 0, false
	}
	for j := at - 1; j >= 0 && at-j <= 32; j-- {
		st := &p.Stmts[j]
		if st.InMacroDef || st.Kind == asm.Label || st.Kind == asm.Directive || st.Kind == asm.MacroCall {
			return "", 0, false
		}
		if st.Kind != asm.Instr {
			continue
		}
		if st.Inst == nil || st.IsBranch() || st.IsRet() || st.IsIret() || implicitWriters[st.Inst.Name] ||
			len(st.Ops) == 0 || st.Inst.Name == "imul" && len(st.Ops) == 1 {
			return "", 0, false
		}
		writes := false
		for k := range st.Ops {
			op := &st.Ops[k]
			// The destination is last in AT&T order; xchg, xadd and
			// cmpxchg write their other register operand too.
			isDst := k == len(st.Ops)-1 || st.Inst.Name == "xchg" || st.Inst.Name == "xadd" || st.Inst.Name == "cmpxchg"
			if isDst && op.Kind == asm.OpReg && regFamily(op.RegName) == reg {
				writes = true
			}
		}
		if writes {
			return loadedLabel(p, st)
		}
		if len(st.Labels) > 0 {
			// A label is a join point: another path may arrive here
			// with something else in the register.
			return "", 0, false
		}
	}
	return "", 0, false
}

func loadedLabel(p *asm.Program, st *asm.Stmt) (string, int64, bool) {
	if len(st.Ops) != 2 {
		return "", 0, false
	}
	src := &st.Ops[0]
	switch {
	case st.IsGPRMove() && src.Kind == asm.OpImm:
		l, off, ok := parseRef(src.Imm, macroLookup(p), false)
		if !ok || l == "" {
			return "", 0, false
		}
		_, isLabel := p.Labels[l]
		return l, off, isLabel
	case st.Inst.Name == "lea" && src.Kind == asm.OpMem && src.Index == "" && (src.Base == "" || src.RIPRel):
		l, off, ok := parseRef(src.Disp, macroLookup(p), false)
		if !ok || l == "" {
			return "", 0, false
		}
		_, isLabel := p.Labels[l]
		return l, off, isLabel
	}
	return "", 0, false
}

// regFamily maps a general register to its 64-bit name, so %eax, %ax and
// %al all trace as rax.
func regFamily(name string) string {
	n := strings.ToLower(strings.TrimPrefix(name, "%"))
	switch n {
	case "al", "ah", "ax", "eax", "rax":
		return "rax"
	case "bl", "bh", "bx", "ebx", "rbx":
		return "rbx"
	case "cl", "ch", "cx", "ecx", "rcx":
		return "rcx"
	case "dl", "dh", "dx", "edx", "rdx":
		return "rdx"
	case "sil", "si", "esi", "rsi":
		return "rsi"
	case "dil", "di", "edi", "rdi":
		return "rdi"
	case "bpl", "bp", "ebp", "rbp":
		return "rbp"
	case "spl", "sp", "esp", "rsp":
		return "rsp"
	}
	if strings.HasPrefix(n, "r") && len(n) >= 2 && n[1] >= '0' && n[1] <= '9' {
		return strings.TrimRight(n, "dwb")
	}
	return ""
}

// placementCheck is what the listing says about one table's alignment.
type placementCheck int

const (
	alignOK        placementCheck = iota // aligned in its section, or unknown: quiet
	alignWrong                           // misaligned whatever the section's address
	alignPlacement                       // misaligned unless the section is placed to make up for it
)

func checkAlign(c *lint.Context, label string, off, align int64) (placementCheck, string, uint64, int64) {
	sec, loff, _, ok := labelOffset(c.Prog, c.Listing, label)
	if !ok {
		return alignOK, "", 0, 0
	}
	at := int64(loff) + off
	if at%align == 0 {
		return alignOK, sec, uint64(at), 0
	}
	sa := sectionAlign(c, sec)
	if sa >= align {
		return alignWrong, sec, uint64(at), sa
	}
	return alignPlacement, sec, uint64(at), sa
}

func balignFix(c *lint.Context, label string, align int64) []lint.Fix {
	idx, ok := c.Prog.Labels[label]
	if !ok {
		return nil
	}
	return []lint.Fix{lint.NewFix(fmt.Sprintf("Insert .balign %d before %s", align, label),
		c.InsertLineAbove(idx, fmt.Sprintf(".balign %d", align)))}
}

func (t table) related(p *asm.Program) []protocol.Location {
	if t.via < 0 {
		if idx, ok := p.Labels[t.from]; ok {
			st := &p.Stmts[idx]
			return []protocol.Location{{URI: p.Doc.URI, Range: p.Range(st.Start, st.End)}}
		}
		return nil
	}
	st := &p.Stmts[t.via]
	return []protocol.Location{{URI: p.Doc.URI, Range: p.Range(st.Start, st.End)}}
}

func (t table) origin(p *asm.Program) string {
	if t.via < 0 {
		return "referenced from an entry of " + t.from
	}
	return fmt.Sprintf("loaded by %s at line %d", t.from, p.Stmts[t.via].Line+1)
}

func reportTable(c *lint.Context, t table, want placementCheck) {
	got, sec, at, sa := checkAlign(c, t.label, t.off, t.align)
	if got != want {
		return
	}
	idx := c.Prog.Labels[t.label]
	st := &c.Prog.Stmts[idx]
	var msg string
	switch {
	case got == alignWrong && t.what == "page table":
		msg = fmt.Sprintf("page table %s (%s) is at offset %#x of %s, which is %d-byte aligned, so the table is not 4 KiB aligned: "+
			"CR3 and page table entries keep flags in the low 12 bits, and the CPU walks the aligned address below it",
			t.label, t.origin(c.Prog), at, sec, sa)
	case got == alignWrong:
		msg = fmt.Sprintf("%s (%s) is at offset %#x of %s, which is %d-byte aligned, so its base is not %d-byte aligned; "+
			"the SDM recommends a %d-byte aligned base for the best performance",
			t.what, t.origin(c.Prog), at, sec, sa, t.align, t.align)
	default:
		// "page table pml4 (loaded by ...)", "the GDT (gdt, loaded by ...)".
		name := fmt.Sprintf("%s %s (%s)", t.what, t.label, t.origin(c.Prog))
		if t.what != "page table" {
			name = fmt.Sprintf("%s (%s, %s)", t.what, t.label, t.origin(c.Prog))
		}
		msg = fmt.Sprintf("%s is at offset %#x of %s, not a multiple of %d; whether it ends up %d-byte aligned "+
			"depends on where %s is placed (this file aligns it to %d)", name, at, sec, t.align, t.align, sec, sa)
	}
	c.ReportFinding(lint.Finding{
		Range:   c.Prog.Range(st.Start, st.End),
		Message: msg,
		Fixes:   balignFix(c, t.label, t.align),
		Related: t.related(c.Prog),
	})
}

func checkTableAlignment(c *lint.Context) {
	for _, t := range descTables(c.Prog) {
		reportTable(c, t, alignWrong)
		reportTable(c, t, alignPlacement)
	}
	for _, t := range pageTablesOf(c) {
		reportTable(c, t, alignPlacement)
	}
}

func checkPageTableAlignment(c *lint.Context) {
	for _, t := range pageTablesOf(c) {
		reportTable(c, t, alignWrong)
	}
}

func pageTablesOf(c *lint.Context) []table {
	v, _ := c.Memo("layout/pagetables", func() any { return pageTables(c.Prog) }).([]table)
	return v
}

// alignedMoves are the SIMD moves that fault (#GP) on a memory operand not
// aligned to the vector width; the unaligned forms (movdqu, movups ...)
// exist precisely so these can be used where alignment is guaranteed.
var alignedMoves = map[string]bool{
	"movdqa": true, "movaps": true, "movapd": true, "movntdq": true, "movntps": true, "movntpd": true, "movntdqa": true,
	"vmovdqa": true, "vmovaps": true, "vmovapd": true, "vmovdqa32": true, "vmovdqa64": true,
	"vmovntdq": true, "vmovntps": true, "vmovntpd": true, "vmovntdqa": true,
}

func vectorBytes(st *asm.Stmt) int64 {
	for k := range st.Ops {
		op := &st.Ops[k]
		if op.Kind != asm.OpReg || op.Reg == nil {
			continue
		}
		switch op.Reg.Class {
		case "xmm":
			return 16
		case "ymm":
			return 32
		case "zmm":
			return 64
		}
	}
	return 0
}

func checkSIMDAlignment(c *lint.Context) {
	p := c.Prog
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.InMacroDef || !alignedMoves[st.Inst.Name] {
			continue
		}
		width := vectorBytes(st)
		if width == 0 {
			continue
		}
		for k := range st.Ops {
			op := &st.Ops[k]
			if op.Kind != asm.OpMem || op.Index != "" || (op.Base != "" && !op.RIPRel) {
				continue
			}
			l, off, ok := parseRef(op.Disp, macroLookup(p), false)
			if !ok || l == "" {
				continue
			}
			got, sec, at, sa := checkAlign(c, l, off, width)
			if got != alignWrong {
				continue
			}
			c.ReportOp(i, k, fmt.Sprintf("%s needs a %d-byte aligned operand, but %s is at offset %#x of %s (aligned to %d): "+
				"this faults with #GP; align the data or use the unaligned form", st.Mnemonic, width, op.Text, at, sec, sa))
		}
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:         "table-alignment",
		Section:      6,
		Default:      lint.Info,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "GDT or IDT base not 8-byte aligned (16 for a long mode IDT), or a table whose alignment depends on section placement.",
			Why: "The SDM recommends an 8-byte aligned base for the GDT (vol. 3, 3.5.1) and the IDT " +
				"(6.10) so descriptor loads do not straddle cache lines; long mode gates are 16 bytes. " +
				"The tables are found from `lgdt`/`lidt` and their pseudo-descriptor (`.word limit` then " +
				"`.long base`), and their offsets come from the listing. When the offset is misaligned but " +
				"the section itself asks for less alignment than needed, the final address depends on where " +
				"the linker puts the section; that case is reported here for page tables too (see " +
				"page-table-alignment), as information. A quick fix inserts `.balign` before the table. " +
				"Needs the toolchain.",
			Bad:  "\t.balign 8\n\t.byte 0\ngdt:\n\t.quad 0, 0x00cf9a000000ffff\ngdt_desc:\n\t.word . - gdt - 1\n\t.long gdt\n\tlgdt gdt_desc",
			Good: "\t.balign 8\ngdt:\n\t.quad 0, 0x00cf9a000000ffff\ngdt_desc:\n\t.word . - gdt - 1\n\t.long gdt\n\tlgdt gdt_desc",
		},
		Check: checkTableAlignment,
	})
	lint.Register(&lint.Rule{
		Code:         "page-table-alignment",
		Section:      6,
		Default:      lint.Error,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "A page table is not 4 KiB aligned.",
			Why: "CR3 and every paging-structure entry hold a 4 KiB aligned physical address with flags in " +
				"the low 12 bits, so a table that is not aligned is not where the CPU looks: the walk reads " +
				"the aligned address below it and the first memory access after enabling paging faults or " +
				"maps garbage. Page tables are found from the register moved into %cr3 (traced back to " +
				"`mov $table, %reg` or `lea table, %reg` in straight-line code) and then through entries " +
				"of known tables that name another label (`.quad pdpt + 3`). An error is reported only when " +
				"the offset is misaligned within a section that is itself aligned to 4 KiB, which makes the " +
				"result certain; otherwise table-alignment says it depends on placement. Needs the toolchain.",
			Bad:  "\t.section .data\n\t.balign 4096\n\t.long 0\npml4:\n\t.quad pdpt + 3\n\t.balign 4096\npdpt:\n\t.quad 0x83\n\t.text\n\tmovq $pml4, %rax\n\tmovq %rax, %cr3",
			Good: "\t.section .data\n\t.balign 4096\npml4:\n\t.quad pdpt + 3\n\t.balign 4096\npdpt:\n\t.quad 0x83\n\t.text\n\tmovq $pml4, %rax\n\tmovq %rax, %cr3",
		},
		Check: checkPageTableAlignment,
	})
	lint.Register(&lint.Rule{
		Code:         "simd-alignment",
		Section:      6,
		Default:      lint.Error,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "An aligned SIMD move (movdqa, movaps, vmovdqa ...) names a label that is not aligned to the vector width.",
			Why: "movdqa, movaps, movapd, the non-temporal stores and their VEX and EVEX forms raise #GP " +
				"when the memory operand is not aligned to the vector width (16, 32 or 64 bytes). When the " +
				"operand is a label in this file and the listing puts it at a misaligned offset of a section " +
				"aligned at least that much, the fault is certain. Operands through registers, and labels in " +
				"sections with weaker alignment, are left alone. Needs the toolchain.",
			Bad:  "\t.data\n\t.balign 16\n\t.long 0\nmask:\n\t.quad 0, 0\n\t.text\n\tmovdqa mask(%rip), %xmm0",
			Good: "\t.data\n\t.balign 16\nmask:\n\t.quad 0, 0\n\t.text\n\tmovdqa mask(%rip), %xmm0",
		},
		Check: checkSIMDAlignment,
	})
}
