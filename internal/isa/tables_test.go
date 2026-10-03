package isa

import (
	"strings"
	"testing"
)

func TestLevelForISASet(t *testing.T) {
	tests := []struct {
		set  string
		want Level
	}{
		{"I86", 1}, {"I186", 1}, {"I286REAL", 1}, {"I286PROTECTED", 1}, {"I386", 1},
		{"I486REAL", 1}, {"PENTIUMREAL", 1}, {"CMOV", 1}, {"PPRO", 1}, {"FAT_NOP", 1},
		{"LONGMODE", 1}, {"SSE", 1}, {"SSE2", 1}, {"MMX", 1}, {"X87", 1}, {"FXSAVE", 1},
		{"sse2", 1}, {" I386 ", 1},
		{"CMPXCHG16B", 2}, {"LAHF", 2}, {"POPCNT", 2}, {"SSE3", 2}, {"SSSE3", 2},
		{"SSE4", 2}, {"SSE42", 2},
		{"AVX", 3}, {"AVX2", 3}, {"AVX2GATHER", 3}, {"BMI1", 3}, {"BMI2", 3}, {"FMA", 3},
		{"F16C", 3}, {"LZCNT", 3}, {"MOVBE", 3}, {"XSAVE", 3},
		{"AVX512F_128", 4}, {"AVX512F_256", 4}, {"AVX512F_512", 4}, {"AVX512F_SCALAR", 4},
		{"AVX512F_KOPW", 4}, {"AVX512BW_512", 4}, {"AVX512BW_KOPD", 4}, {"AVX512CD_256", 4},
		{"AVX512DQ_128N", 4}, {"AVX512F", 4},
		{"AVX512_VNNI_512", 0}, {"AVX512_BF16_128", 0}, {"AVX512ER_512", 0}, {"AES", 0},
		{"SHA", 0}, {"VTX", 0}, {"XSAVEOPT", 0}, {"SEP", 0}, {"AVXAES", 0}, {"APX_F_BMI1", 0},
		{"AVX512FX", 0}, {"", 0},
	}
	for _, tt := range tests {
		if got := LevelForISASet(tt.set); got != tt.want {
			t.Errorf("LevelForISASet(%q) = %d, want %d", tt.set, got, tt.want)
		}
	}
}

func TestLookupRegister(t *testing.T) {
	tests := []struct {
		name, want, class, modes string
		width                    int
	}{
		{"%eax", "eax", "gpr", "", 32},
		{"eax", "eax", "gpr", "", 32},
		{"%RAX", "rax", "gpr", "64", 64},
		{"%ah", "ah", "gpr", "", 8},
		{"%sil", "sil", "gpr", "64", 8},
		{"%r8", "r8", "gpr", "64", 64},
		{"%r15d", "r15d", "gpr", "64", 32},
		{"%r9w", "r9w", "gpr", "64", 16},
		{"%r10b", "r10b", "gpr", "64", 8},
		{"%r10l", "r10l", "gpr", "64", 8},
		{"%es", "es", "segment", "", 16},
		{"%cr0", "cr0", "control", "", 64},
		{"%cr8", "cr8", "control", "64", 64},
		{"%dr7", "dr7", "debug", "", 64},
		{"%st", "st", "x87", "", 80},
		{"%st(3)", "st(3)", "x87", "", 80},
		{"%ST (3)", "st(3)", "x87", "", 80},
		{"%mm7", "mm7", "mmx", "", 64},
		{"%xmm0", "xmm0", "xmm", "", 128},
		{"%xmm8", "xmm8", "xmm", "64", 128},
		{"%xmm31", "xmm31", "xmm", "64", 128},
		{"%ymm15", "ymm15", "ymm", "64", 256},
		{"%zmm7", "zmm7", "zmm", "", 512},
		{"%k1", "k1", "mask", "", 64},
		{"%rip", "rip", "ip", "64", 64},
		{"%eflags", "eflags", "flags", "", 32},
	}
	for _, tt := range tests {
		r, ok := LookupRegister(tt.name)
		if !ok {
			t.Errorf("LookupRegister(%q) missing", tt.name)
			continue
		}
		if r.Name != tt.want || r.Class != tt.class || r.Modes != tt.modes || r.Width != tt.width {
			t.Errorf("LookupRegister(%q) = %+v", tt.name, r)
		}
		if r.Summary == "" {
			t.Errorf("%s has no summary", r.Name)
		}
	}
	for _, bad := range []string{"", "%", "%cr1", "%gdtr", "%r16", "%xmm32", "%st(8)", "%k8", "%foo"} {
		if r, ok := LookupRegister(bad); ok {
			t.Errorf("LookupRegister(%q) = %+v, want miss", bad, r)
		}
	}
	for _, name := range []string{"cr0", "cr3", "cr4", "cr8", "dr6", "dr7", "eflags", "rflags"} {
		if r, _ := LookupRegister(name); r == nil || r.Bits == "" {
			t.Errorf("%s should carry a bit layout", name)
		}
	}
	if r, _ := LookupRegister("cr0"); !strings.Contains(r.Bits, "PE(0)") || !strings.Contains(r.Bits, "PG(31)") {
		t.Errorf("cr0 bits = %q", r.Bits)
	}
	if !strings.Contains(registers["xmm16"].Summary, "AVX-512") {
		t.Error("xmm16 should note its AVX-512 requirement")
	}
	// 16 GPR families x (64/32/16/8) plus the legacy high bytes and the
	// AMD r8l spellings: guards against a family silently losing members.
	gprs := 0
	for _, r := range registers {
		if r.Class == "gpr" {
			gprs++
		}
	}
	if want := 16*4 + 4 + 8; gprs != want {
		t.Errorf("gpr count = %d, want %d", gprs, want)
	}
}

func TestLookupDirective(t *testing.T) {
	for _, name := range []string{".code16", "code16", ".CODE32", ".section", ".globl", ".org",
		".balign", ".asciz", ".macro", ".endm", ".rept", ".endr", ".if", ".else", ".endif",
		".set", ".extern", ".fill", ".space", ".cfi_startproc", ".code16gcc", ".incbin"} {
		d, ok := LookupDirective(name)
		if !ok {
			t.Errorf("LookupDirective(%q) missing", name)
			continue
		}
		if !strings.HasPrefix(d.Name, ".") || d.Summary == "" || !strings.HasPrefix(d.Syntax, d.Name) {
			t.Errorf("%q: malformed %+v", name, d)
		}
	}
	for _, name := range []string{"", ".", ".nosuch", ".noprefix"} {
		if d, ok := LookupDirective(name); ok {
			t.Errorf("LookupDirective(%q) = %+v, want miss", name, d)
		}
	}
}

func TestDirectiveTableClean(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range directiveList {
		if seen[d.Name] {
			t.Errorf("duplicate directive %s", d.Name)
		}
		seen[d.Name] = true
		if d.Name != strings.ToLower(d.Name) || !strings.HasPrefix(d.Syntax, d.Name) {
			t.Errorf("%s: name must be lowercase and lead its syntax %q", d.Name, d.Syntax)
		}
		for _, s := range []string{d.Name, d.Summary, d.Syntax} {
			if strings.IndexFunc(s, func(r rune) bool { return r > 0x7e || r < 0x20 }) >= 0 {
				t.Errorf("%s: non-ASCII text %q", d.Name, s)
			}
		}
	}
}
