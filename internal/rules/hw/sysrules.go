package hw

import (
	"fmt"
	"regexp"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func checkPTEReserved(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	for _, pt := range ix.pages {
		for _, e := range pt.elems {
			v, ok := entryValue(e)
			if !ok || v&1 == 0 {
				continue
			}
			if res, msg := reservedPTE(pt.kinds, v); res != 0 {
				what := fmt.Sprintf("%#x", v)
				if e.Sym != "" {
					what = fmt.Sprintf("%s + %#x", e.Sym, v)
				}
				c.ReportStmt(e.Stmt, fmt.Sprintf("%s entry %s in page table %s: %s", strings.Join(pt.kinds, "/"), what, pt.label, msg))
			}
		}
	}
}

// sipiRe finds `label >> 12` inside an expression: the vector field of a
// startup IPI is the page number of the trampoline.
var sipiRe = regexp.MustCompile(`([A-Za-z_.$][A-Za-z0-9_.$]*)\s*\)?\s*>>\s*12`)

// trampolines are the labels used as SIPI vectors: written into an ICR
// value with delivery mode 110b (`$(0x4600 | (ap_start >> 12))`), or marked
// with a `falcon-asm: trampoline` comment.
func (ix *Index) trampolines() map[string]int {
	out := map[string]int{}
	for i := range ix.Prog.Stmts {
		s := &ix.Prog.Stmts[i]
		if s.InMacroDef {
			continue
		}
		if len(s.Labels) > 0 {
			if _, ok := ix.note(i, "trampoline"); ok {
				for _, l := range s.Labels {
					if ix.isLabel(l) {
						out[l] = i
					}
				}
			}
		}
		if s.Kind != asm.Instr {
			continue
		}
		for _, op := range s.Ops {
			if op.Kind != asm.OpImm {
				continue
			}
			m := sipiRe.FindStringSubmatch(op.Imm)
			if m == nil || !ix.isLabel(m[1]) {
				continue
			}
			// The rest of the expression, with the label as 0, is the
			// ICR command: delivery mode in bits 10:8.
			rest := strings.Replace(op.Imm, m[1], "0", 1)
			if v, ok := ix.lay.evalConst(rest); ok && v>>8&7 == 6 {
				li, _ := ix.labelStmt(m[1])
				out[m[1]] = li
			}
		}
	}
	return out
}

// SIPI checks, split by what they need: the mode needs nothing, the
// alignment verdict needs the listing, and "cannot tell" is information.
const (
	sipiMode = iota
	sipiListing
	sipiUnproven
)

func checkSIPI(c *lint.Context, what int) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	for label, li := range ix.trampolines() {
		switch what {
		case sipiMode:
			code := ix.firstCode(li)
			if code >= 0 && ix.Prog.Stmts[code].Mode != asm.Mode16 {
				c.ReportStmt(li, fmt.Sprintf("SIPI trampoline %s is assembled as .code%d: a startup IPI starts the processor in real mode, so its first instructions must be .code16",
					label, ix.Prog.Stmts[code].Mode))
			}
		case sipiListing:
			if off, ok := listingOffset(c, ix, li); ok && off%4096 != 0 {
				c.ReportStmt(li, fmt.Sprintf("SIPI trampoline %s is at offset %#x in its section, not on a 4 KiB boundary: the vector names a page and the processor starts at its first byte", label, off))
			}
		case sipiUnproven:
			if !ix.pageAligned(li) {
				c.ReportStmt(li, fmt.Sprintf("SIPI trampoline %s: nothing here proves 4 KiB alignment (no .balign 4096 right before it); the vector names a page and the processor starts at its first byte", label))
			}
		}
	}
}

// pageAligned reports an alignment directive of at least 4 KiB directly
// before statement i.
func (ix *Index) pageAligned(i int) bool {
	for k := i; k >= 0 && k >= i-4; k-- {
		s := &ix.Prog.Stmts[k]
		if k < i && (s.Kind == asm.Instr || len(s.Labels) > 0) {
			return false
		}
		if s.Kind != asm.Directive || !alignDirective(s.Name) || len(s.Args) == 0 {
			continue
		}
		n, ok := ix.lay.evalConst(s.Args[0])
		if !ok {
			return false
		}
		if s.Name == ".p2align" {
			return n >= 12
		}
		return n >= 4096 && n%4096 == 0
	}
	return false
}

// listingOffset is the section offset of statement i from the assembler
// listing: the first line at or after it that emits something.
func listingOffset(c *lint.Context, ix *Index, i int) (uint64, bool) {
	if c.Listing == nil {
		return 0, false
	}
	return lineOffset(c.Listing.Listing, ix.Prog.Stmts[i].Line, ix.Prog.Stmts[i].Section)
}

// bios-struct: the structure a BIOS service reads, built as data.

// pointerReg is the offset register of the first segment:offset input
// ("DS:SI" -> "si"), which is where the service takes its structure.
func pointerReg(svc *hwdb.BIOSService) string {
	for _, in := range svc.In {
		if seg, off, ok := strings.Cut(in[0], ":"); ok && len(seg) == 2 {
			return strings.ToLower(off)
		}
	}
	return ""
}

func checkBIOSStruct(c *lint.Context) {
	ix := indexOf(c)
	db, _ := c.Tables.(*hwdb.DB)
	if ix == nil || db == nil {
		return
	}
	done := map[string]bool{}
	check := func(label, name string) {
		key := label + "/" + name
		if done[key] {
			return
		}
		done[key] = true
		st, ok := db.Struct(name)
		if !ok {
			return
		}
		li, _ := ix.labelStmt(label)
		for _, m := range structProblems(ix, label, st) {
			c.ReportStmt(li, fmt.Sprintf("%s at %s: %s", st.Title, label, m))
		}
	}
	for _, e := range ix.trk.events {
		if e.kind != evInt || e.a.known&0xFF00 != 0xFF00 {
			continue
		}
		ah, al := e.a.val>>8&0xFF, e.a.val&0xFF
		svc, ok := db.LookupBIOS(uint64(e.n), ah, al, e.a.known&0xFF == 0xFF)
		if !ok || len(svc.Struct) == 0 {
			continue
		}
		reg := pointerReg(svc)
		if reg == "" {
			continue
		}
		v := ix.regBefore(e.stmt, reg)
		if v.sym == "" {
			v.sym = ix.flowSym(e.stmt, reg)
		}
		if v.sym == "" {
			continue
		}
		for _, name := range svc.Struct {
			check(v.sym, name)
		}
	}
	for i := range ix.Prog.Stmts {
		if len(ix.Prog.Stmts[i].Labels) == 0 {
			continue
		}
		name, ok := ix.note(i, "struct")
		if !ok || name == "" {
			continue
		}
		for _, l := range ix.Prog.Stmts[i].Labels {
			if ix.isLabel(l) {
				check(l, name)
			}
		}
	}
}

// structProblems validates the data at label against st. A structure whose
// first member is its own size (the DAP's 10h/18h) is checked over that
// many bytes; one without is checked over the bytes up to the next label,
// and only for being too short, since a buffer may be followed by unlabeled
// data. Bytes that are not constants are not compared.
func structProblems(ix *Index, label string, st *hwdb.BIOSStruct) []string {
	maxSize := 0
	for _, s := range st.Sizes {
		maxSize = max(maxSize, s)
	}
	p := ix.lay.labels[label]
	if p.run < 0 {
		return nil
	}
	es := ix.lay.elemsAt(p, int64(maxSize)+64)
	es = untilLabel(ix, es)
	data, known := bytesOf(es, p.off)
	var out []string
	length := len(data)
	declared := -1
	if f := sizeField(st); f != nil && f.Off+f.Size <= len(data) && allKnown(known[f.Off:f.Off+f.Size]) {
		declared = int(leN(data[f.Off : f.Off+f.Size]))
	}
	switch {
	case declared >= 0:
		valid := false
		for _, s := range st.Sizes {
			valid = valid || s == declared
		}
		if valid && length < declared {
			out = append(out, fmt.Sprintf("the size member says %d bytes but only %d are laid out before the next label", declared, length))
		}
		if valid {
			length = declared
		}
	case length < st.Sizes[0]:
		out = append(out, fmt.Sprintf("%d bytes laid out before the next label; the service writes at least %d", length, st.Sizes[0]))
	}
	for _, f := range st.Fields {
		if len(f.Const) == 0 || f.Off+f.Size > min(length, len(data)) || !allKnown(known[f.Off:f.Off+f.Size]) {
			continue
		}
		v := leN(data[f.Off : f.Off+f.Size])
		match := false
		var want []string
		for _, cv := range f.Const {
			match = match || uint64(cv) == v
			want = append(want, fmt.Sprintf("%#x", uint64(cv)))
		}
		if !match {
			out = append(out, fmt.Sprintf("%s (offset %d) is %#x, want %s", f.Name, f.Off, v, strings.Join(want, " or ")))
		}
	}
	return out
}

// sizeField is a structure's leading size member: at offset 0, constrained
// to the valid sizes.
func sizeField(st *hwdb.BIOSStruct) *hwdb.StructField {
	if len(st.Fields) == 0 || st.Fields[0].Off != 0 || len(st.Fields[0].Const) == 0 {
		return nil
	}
	f := &st.Fields[0]
	for _, c := range f.Const {
		ok := false
		for _, s := range st.Sizes {
			ok = ok || int(c) == s
		}
		if !ok {
			return nil
		}
	}
	return f
}

func leN(b []byte) uint64 {
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// untilLabel cuts elements at the first statement after the first one that
// defines a label: the next labeled object starts there.
func untilLabel(ix *Index, es []Elem) []Elem {
	if len(es) == 0 {
		return es
	}
	first := es[0].Stmt
	for k, e := range es {
		if e.Stmt != first && len(ix.Prog.Stmts[e.Stmt].Labels) > 0 {
			return es[:k]
		}
		for s := first + 1; s < e.Stmt; s++ {
			if len(ix.Prog.Stmts[s].Labels) > 0 {
				return es[:k]
			}
		}
		first = e.Stmt
	}
	return es
}

// regBefore is the tracked value of reg as statement i sees it, from the
// event recorded there (only consumers record state).
func (ix *Index) regBefore(i int, reg string) rv {
	var f regFile
	// Replay the block: cheap, and only done for the few int statements
	// with a structure.
	start := i
	for start > 0 && len(ix.Prog.Stmts[start].Labels) == 0 {
		start--
	}
	t := &tracker{prog: ix.Prog, roles: map[int][]role{}, isLabel: ix.isLabel}
	for k := start; k < i; k++ {
		s := &ix.Prog.Stmts[k]
		if s.Kind != asm.Instr || s.Inst == nil || len(s.Prefixes) > 0 && !onlyLock(s.Prefixes) {
			if s.Kind == asm.Instr || s.Kind == asm.Directive && !silent[s.Name] || s.Kind == asm.Cpp || s.Kind == asm.MacroCall {
				f = regFile{}
			}
			continue
		}
		t.step(&f, k, s)
	}
	v, _ := f.read(reg)
	return v
}

func init() {
	lint.Register(&lint.Rule{
		Code:    "pte-reserved",
		Section: 5,
		Default: lint.Warning,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "Page table entry built with reserved bits set.",
			Why: "A present paging-structure entry with a reserved bit set makes every walk through it raise #PF " +
				"with the RSVD error code, which before an IDT exists is a triple fault the moment paging is " +
				"enabled. Tables are found from the label moved into CR3 and followed through entries written " +
				"as `.quad next_table + flags`; for those the flags are checked. With 8-byte entries and no 64-bit " +
				"code in the file the paging mode could be PAE or 4-level, and only bits reserved under both are " +
				"reported (PAE's PDPTEs reserve RW and US, which 4-level paging uses).",
			Bad: "\t.code32\n\tmovl $pml4, %eax\n\tmovl %eax, %cr3\n\t.code64\n\tnop\n\t.balign 4096\n" +
				"pml4:\t.quad pdpt + 0x83\n\t.balign 4096\npdpt:\t.quad 0",
			Good: "\t.code32\n\tmovl $pml4, %eax\n\tmovl %eax, %cr3\n\t.code64\n\tnop\n\t.balign 4096\n" +
				"pml4:\t.quad pdpt + 0x3\n\t.balign 4096\npdpt:\t.quad 0",
		},
		Check: checkPTEReserved,
	})
	lint.Register(&lint.Rule{
		Code:    "sipi-trampoline",
		Section: 5,
		Default: lint.Warning,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "SIPI trampoline not assembled as 16-bit code.",
			Why: "A startup IPI starts an application processor in real mode at the first byte of the page its " +
				"vector names (CS = vector << 8, IP = 0). The trampoline therefore has to be .code16 and page " +
				"aligned. A label is taken as the trampoline when the vector field of an ICR value is built from " +
				"it with delivery mode 110b (`$(0x4600 | (ap_start >> 12))`), or when a `falcon-asm: trampoline` " +
				"comment marks it. sipi-alignment checks the page alignment against the listing.",
			Bad:  "\t# falcon-asm: trampoline\n\t.code32\nap_start:\n\tcli",
			Good: "\t.balign 4096\n\t# falcon-asm: trampoline\n\t.code16\nap_start:\n\tcli",
		},
		Check: func(c *lint.Context) { checkSIPI(c, sipiMode) },
	})
	lint.Register(&lint.Rule{
		Code:         "sipi-alignment",
		Section:      5,
		Default:      lint.Warning,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "SIPI trampoline at a section offset that is not a multiple of 4 KiB.",
			Why: "The startup IPI's vector is a page number: the processor starts at offset 0 of that page. A " +
				"trampoline the listing places at, say, offset 0x120 of its section cannot be the first byte of a " +
				"page however the section is aligned. A trampoline that is copied to its page before the IPIs " +
				"are sent is not this rule's business; mark only the in-place one.",
			Bad:  "\t.code16\n\tnop\n\t# falcon-asm: trampoline\nap_start:\n\tcli",
			Good: "\t.code16\n\t# falcon-asm: trampoline\nap_start:\n\tcli",
		},
		Check: func(c *lint.Context) { checkSIPI(c, sipiListing) },
	})
	lint.Register(&lint.Rule{
		Code:    "sipi-unproven",
		Section: 5,
		Default: lint.Info,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "SIPI trampoline whose 4 KiB alignment cannot be shown.",
			Why: "When there is no listing and no `.balign 4096` (or `.p2align 12`) right before the trampoline " +
				"label, nothing in the file proves the page alignment a startup IPI needs. Reported as " +
				"information: the linker script may well place it, which is outside what the linter sees.",
			Bad:  "\t# falcon-asm: trampoline\n\t.code16\nap_start:\n\tcli",
			Good: "\t.balign 4096\n\t# falcon-asm: trampoline\n\t.code16\nap_start:\n\tcli",
		},
		Check: func(c *lint.Context) { checkSIPI(c, sipiUnproven) },
	})
	lint.Register(&lint.Rule{
		Code:    "bios-struct",
		Section: 5,
		Default: lint.Warning,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "BIOS structure (disk address packet, E820 buffer ...) built with a wrong size or member.",
			Why: "INT 13h AH=42h reads a disk address packet whose first byte must be 10h or 18h and whose second " +
				"must be zero; E820h writes 20 or 24 bytes into ES:DI. A packet with the wrong size byte fails " +
				"on some BIOSes and silently works on others, which is the worst way to find out. The structure " +
				"is found where the pointer register is loaded with its label before the call (`movw $dap, %si; " +
				"movb $0x42, %ah; int $0x13`, the move possibly before a label the dataflow follows), or marked " +
				"with `falcon-asm: struct=dap`.",
			Bad:  "\tmovw $dap, %si\n\tmovb $0x42, %ah\n\tint $0x13\ndap:\t.byte 0x18, 0\n\t.word 1, 0x7E00, 0\n\t.quad 1",
			Good: "\tmovw $dap, %si\n\tmovb $0x42, %ah\n\tint $0x13\ndap:\t.byte 0x10, 0\n\t.word 1, 0x7E00, 0\n\t.quad 1",
		},
		Check: checkBIOSStruct,
	})
}
