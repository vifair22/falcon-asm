package isa

import (
	"fmt"
	"strings"
)

// legacyGPR describes one of the eight original general-purpose registers
// and the names of its narrower views. low8/high8 are empty where the
// register has no such view outside long mode (si, di, bp, sp have only the
// REX-only low byte sil/dil/bpl/spl).
type legacyGPR struct {
	r64, r32, r16, low8, high8, rexLow8 string
	role                                string
}

var legacyGPRs = []legacyGPR{
	{"rax", "eax", "ax", "al", "ah", "", "Accumulator"},
	{"rbx", "ebx", "bx", "bl", "bh", "", "Base"},
	{"rcx", "ecx", "cx", "cl", "ch", "", "Count"},
	{"rdx", "edx", "dx", "dl", "dh", "", "Data"},
	{"rsi", "esi", "si", "", "", "sil", "Source index"},
	{"rdi", "edi", "di", "", "", "dil", "Destination index"},
	{"rbp", "ebp", "bp", "", "", "bpl", "Base pointer"},
	{"rsp", "esp", "sp", "", "", "spl", "Stack pointer"},
}

const (
	cr0Bits = "PE(0) MP(1) EM(2) TS(3) ET(4) NE(5) WP(16) AM(18) NW(29) CD(30) PG(31)"
	cr3Bits = "PWT(3) PCD(4) page-table base(12+); with CR4.PCIDE=1 bits 0-11 are the PCID"
	cr4Bits = "VME(0) PVI(1) TSD(2) DE(3) PSE(4) PAE(5) MCE(6) PGE(7) PCE(8) OSFXSR(9) " +
		"OSXMMEXCPT(10) UMIP(11) LA57(12) VMXE(13) SMXE(14) FSGSBASE(16) PCIDE(17) " +
		"OSXSAVE(18) KL(19) SMEP(20) SMAP(21) PKE(22) CET(23) PKS(24)"
	cr8Bits    = "TPR(0-3): task priority, mirrors the local APIC TPR bits 7:4"
	eflagsBits = "CF(0) PF(2) AF(4) ZF(6) SF(7) TF(8) IF(9) DF(10) OF(11) IOPL(12-13) " +
		"NT(14) RF(16) VM(17) AC(18) VIF(19) VIP(20) ID(21); bit 1 reads as 1"
	dr6Bits = "B0-B3(0-3) BD(13) BS(14) BT(15) RTM(16)"
	dr7Bits = "L0(0) G0(1) L1(2) G1(3) L2(4) G2(5) L3(6) G3(7) LE(8) GE(9) RTM(11) GD(13) " +
		"R/W0(16-17) LEN0(18-19) R/W1(20-21) LEN1(22-23) R/W2(24-25) LEN2(26-27) R/W3(28-29) LEN3(30-31)"
)

// registers is built once at package init. The repetitive families (r8-r15
// views, vector and mask registers, x87 stack, debug registers) are
// generated, so a typo cannot silently drop one member of a family.
var registers = buildRegisters()

func buildRegisters() map[string]*Register {
	m := map[string]*Register{}
	add := func(r Register) { m[r.Name] = &r }

	for _, g := range legacyGPRs {
		add(Register{Name: g.r64, Class: "gpr", Width: 64, Modes: "64", Summary: g.role + ", 64 bits"})
		add(Register{Name: g.r32, Class: "gpr", Width: 32, Summary: g.role + ", low 32 bits of " + upper(g.r64)})
		add(Register{Name: g.r16, Class: "gpr", Width: 16, Summary: g.role + ", low 16 bits of " + upper(g.r32)})
		if g.low8 != "" {
			add(Register{Name: g.low8, Class: "gpr", Width: 8, Summary: g.role + ", low 8 bits of " + upper(g.r16)})
			add(Register{Name: g.high8, Class: "gpr", Width: 8,
				Summary: g.role + ", high 8 bits of " + upper(g.r16) + " (bits 8-15); not encodable with a REX prefix"})
		}
		if g.rexLow8 != "" {
			add(Register{Name: g.rexLow8, Class: "gpr", Width: 8, Modes: "64",
				Summary: g.role + ", low 8 bits of " + upper(g.r16) + " (needs a REX prefix)"})
		}
	}
	for n := 8; n <= 15; n++ {
		r := fmt.Sprintf("r%d", n)
		add(Register{Name: r, Class: "gpr", Width: 64, Modes: "64", Summary: "General purpose register " + upper(r)})
		add(Register{Name: r + "d", Class: "gpr", Width: 32, Modes: "64", Summary: "Low 32 bits of " + upper(r)})
		add(Register{Name: r + "w", Class: "gpr", Width: 16, Modes: "64", Summary: "Low 16 bits of " + upper(r)})
		// GAS accepts both the Intel r8b and the AMD r8l spelling.
		add(Register{Name: r + "b", Class: "gpr", Width: 8, Modes: "64", Summary: "Low 8 bits of " + upper(r)})
		add(Register{Name: r + "l", Class: "gpr", Width: 8, Modes: "64", Summary: "Low 8 bits of " + upper(r) + " (same as " + upper(r) + "B)"})
	}

	segs := []struct{ name, summary string }{
		{"cs", "Code segment"},
		{"ds", "Data segment (default for most memory operands)"},
		{"es", "Extra segment (destination of string instructions)"},
		{"fs", "FS segment (base settable via MSR or WRFSBASE in long mode)"},
		{"gs", "GS segment (base settable via MSR, WRGSBASE or SWAPGS in long mode)"},
		{"ss", "Stack segment"},
	}
	for _, s := range segs {
		add(Register{Name: s.name, Class: "segment", Width: 16, Summary: s.summary})
	}

	ctlWidth := " (64 bits in long mode, 32 bits otherwise)"
	add(Register{Name: "cr0", Class: "control", Width: 64, Bits: cr0Bits,
		Summary: "Control register 0: operating mode and cache control" + ctlWidth})
	add(Register{Name: "cr2", Class: "control", Width: 64,
		Summary: "Control register 2: linear address of the last page fault" + ctlWidth})
	add(Register{Name: "cr3", Class: "control", Width: 64, Bits: cr3Bits,
		Summary: "Control register 3: page-table root (PDBR)" + ctlWidth})
	add(Register{Name: "cr4", Class: "control", Width: 64, Bits: cr4Bits,
		Summary: "Control register 4: architectural feature enables" + ctlWidth})
	add(Register{Name: "cr8", Class: "control", Width: 64, Modes: "64", Bits: cr8Bits,
		Summary: "Control register 8: task priority register"})

	for n := 0; n <= 7; n++ {
		r := Register{Name: fmt.Sprintf("dr%d", n), Class: "debug", Width: 64}
		switch {
		case n <= 3:
			r.Summary = fmt.Sprintf("Debug register %d: breakpoint %d linear address", n, n)
		case n <= 5:
			r.Summary = fmt.Sprintf("Debug register %d: reserved; aliases DR%d when CR4.DE=0, #UD when CR4.DE=1", n, n+2)
		case n == 6:
			r.Summary = "Debug register 6: debug status"
			r.Bits = dr6Bits
		default:
			r.Summary = "Debug register 7: debug control"
			r.Bits = dr7Bits
		}
		add(r)
	}

	add(Register{Name: "st", Class: "x87", Width: 80, Summary: "x87 stack top, same as ST(0)"})
	for n := 0; n <= 7; n++ {
		add(Register{Name: fmt.Sprintf("st(%d)", n), Class: "x87", Width: 80,
			Summary: fmt.Sprintf("x87 floating-point stack register %d (relative to TOP)", n)})
		add(Register{Name: fmt.Sprintf("mm%d", n), Class: "mmx", Width: 64,
			Summary: fmt.Sprintf("MMX register %d (aliases the mantissa of x87 physical register %d)", n, n)})
		add(Register{Name: fmt.Sprintf("k%d", n), Class: "mask", Width: 64,
			Summary: maskSummary(n)})
	}

	vecs := []struct {
		prefix, class string
		width         int
		ext           string
	}{
		{"xmm", "xmm", 128, "SSE"},
		{"ymm", "ymm", 256, "AVX"},
		{"zmm", "zmm", 512, "AVX-512"},
	}
	for _, v := range vecs {
		for n := 0; n <= 31; n++ {
			r := Register{Name: fmt.Sprintf("%s%d", v.prefix, n), Class: v.class, Width: v.width}
			r.Summary = fmt.Sprintf("%s vector register %d, %d bits", v.ext, n, v.width)
			if v.prefix != "xmm" {
				r.Summary += fmt.Sprintf("; low 128 bits are XMM%d", n)
			}
			switch {
			case n >= 16:
				r.Modes = "64"
				r.Summary += "; needs AVX-512 (EVEX encoding)"
			case n >= 8:
				r.Modes = "64"
			}
			add(r)
		}
	}

	add(Register{Name: "rip", Class: "ip", Width: 64, Modes: "64", Summary: "Instruction pointer; usable as a base for RIP-relative addressing"})
	add(Register{Name: "eip", Class: "ip", Width: 32, Summary: "Instruction pointer, 32 bits; in 64-bit code EIP-relative addressing needs addr32"})
	add(Register{Name: "ip", Class: "ip", Width: 16, Summary: "Instruction pointer, 16 bits"})

	add(Register{Name: "eflags", Class: "flags", Width: 32, Bits: eflagsBits, Summary: "Flags register, 32 bits"})
	add(Register{Name: "rflags", Class: "flags", Width: 64, Modes: "64", Bits: eflagsBits + "; bits 32-63 reserved",
		Summary: "Flags register, 64 bits"})

	return m
}

func maskSummary(n int) string {
	if n == 0 {
		return "AVX-512 opmask register 0; as a write mask it means no masking"
	}
	return fmt.Sprintf("AVX-512 opmask register %d", n)
}

func upper(s string) string { return strings.ToUpper(s) }

// LookupRegister finds a register by name, with or without the leading '%',
// ignoring case and whitespace so "%ST (1)" and "st(1)" both resolve.
func LookupRegister(name string) (*Register, bool) {
	n := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "%"))
	// "%st (1)" is legal; squeezing the blanks out allocates, so it is done
	// only when there are any. Every register operand of a document comes
	// through here during analysis.
	if strings.ContainsAny(n, " \t") {
		n = strings.Join(strings.Fields(n), "")
	}
	r, ok := registers[n]
	return r, ok
}
