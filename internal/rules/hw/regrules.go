package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// eferIndex is IA32_EFER, checked with the control registers.
const eferIndex = 0xC0000080

// crKind maps a control register write to its hwdb decoder.
var crKind = map[string]string{"cr0": "cr0", "cr4": "cr4", "xcr0": "xcr0"}

func checkMSRWrite(c *lint.Context) {
	ix := indexOf(c)
	chk, ok := c.Tables.(lint.MSRChecker)
	if ix == nil || !ok {
		return
	}
	for _, e := range ix.trk.events {
		if e.kind != evMSR || !e.write || !e.c.full(32) {
			continue
		}
		index := e.c.val & 0xFFFFFFFF
		if index == eferIndex {
			continue // cr-reserved-write decodes EFER with the control registers
		}
		for _, m := range msrProblems(chk, index, e.a, e.d) {
			c.ReportStmt(e.stmt, fmt.Sprintf("wrmsr to MSR %#x: %s", index, m))
		}
	}
}

// msrProblems checks a wrmsr. A fully known EDX:EAX goes through the whole
// check; a partly known one (`rdmsr; orl $X, %eax; wrmsr`) only through the
// reserved mask, against the bits known to be set, since a memory-type or
// read-only verdict about half a value would be a guess.
func msrProblems(chk lint.MSRChecker, index uint64, a, d rv) []string {
	if a.full(32) && d.full(32) {
		return chk.CheckMSRWrite(index, d.val<<32|a.val&0xFFFFFFFF)
	}
	res, known := chk.MSRReserved(index)
	set := (d.set()&0xFFFFFFFF)<<32 | a.set()&0xFFFFFFFF
	if !known || set&res == 0 {
		return nil
	}
	return []string{fmt.Sprintf("sets reserved bits %#x (wrmsr raises #GP)", set&res)}
}

func checkCRWrite(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	for _, e := range ix.trk.events {
		var v rv
		var kind string
		switch {
		case e.kind == evCR && e.cr == "xcr0":
			kind = "xcr0"
			v = rv{known: e.d.known<<32 | e.a.known&0xFFFFFFFF, val: e.d.val<<32 | e.a.val&0xFFFFFFFF}
		case e.kind == evCR:
			kind = crKind[e.cr]
			v = e.src
		case e.kind == evMSR && e.write && e.c.full(32) && e.c.val&0xFFFFFFFF == eferIndex:
			kind = "efer"
			v = rv{known: e.d.known<<32 | e.a.known&0xFFFFFFFF, val: e.d.val<<32 | e.a.val&0xFFFFFFFF}
		}
		if kind == "" {
			continue
		}
		name := strings.ToUpper(kind)
		width := uint(32)
		if kind == "xcr0" || kind == "efer" || ix.Prog.Stmts[e.stmt].Mode == 64 {
			width = 64
		}
		if v.full(width) {
			d, ok := hwdb.Decode(kind, v.val&maskW(width))
			if !ok {
				continue
			}
			for _, p := range d.Problems {
				c.ReportStmt(e.stmt, fmt.Sprintf("%s value %#x: %s", name, v.val&maskW(width), p))
			}
			continue
		}
		// Only part of the value is known: the bits known to be set can
		// still be reserved ones.
		if d, ok := hwdb.Decode(kind, v.set()&maskW(width)); ok && d.Reserved != 0 {
			c.ReportStmt(e.stmt, fmt.Sprintf("%s write sets reserved bits %#x (the write raises #GP)", name, d.Reserved))
		}
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:    "msr-reserved-write",
		Section: 7,
		Default: lint.Warning,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "wrmsr with a constant value that sets reserved bits, writes a read-only MSR or a reserved memory type.",
			Why: "wrmsr raises #GP for a reserved bit set, a write to a read-only register or an undefined PAT or " +
				"MTRR memory type, and in early boot code nothing catches it. When ECX, EAX and EDX are set from " +
				"constants before the wrmsr (`movl $lo, %eax; movl $hi, %edx; movl $msr, %ecx; wrmsr`, across " +
				"labels as far as the dataflow proves them), or EAX is a read-modify-write of the register " +
				"(`rdmsr; orl $bit, %eax; wrmsr`), the value is checked against the MSR's layout. EFER is " +
				"checked by cr-reserved-write.",
			Bad:  "\tmovl $0x277, %ecx\n\tmovl $0x02020202, %eax\n\tmovl $0x02020202, %edx\n\twrmsr",
			Good: "\tmovl $0x277, %ecx\n\tmovl $0x00070406, %eax\n\tmovl $0x00070406, %edx\n\twrmsr",
		},
		Check: checkMSRWrite,
	})
	lint.Register(&lint.Rule{
		Code:    "cr-reserved-write",
		Section: 7,
		Default: lint.Warning,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "Constant written to CR0, CR4, XCR0 or EFER that sets reserved bits or an invalid combination.",
			Why: "A mov to CR0 or CR4 with a reserved bit set raises #GP, as does CR0.PG without PE or NW without " +
				"CD, and xsetbv and the EFER wrmsr have their own invalid combinations. The value is followed " +
				"from constants (`movl $X, %eax; movl %eax, %cr0`), across labels as far as the dataflow proves " +
				"it; after a read-modify-write " +
				"(`movl %cr4, %eax; orl $X, %eax`) only the bits known to be set are checked, against the " +
				"reserved mask.",
			Bad:  "\tmovl %cr4, %eax\n\torl $0x8000, %eax\n\tmovl %eax, %cr4",
			Good: "\tmovl %cr4, %eax\n\torl $0x20, %eax\n\tmovl %eax, %cr4",
		},
		Check: checkCRWrite,
	})
}
