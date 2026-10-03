package hover

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

func TestParseNumber(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"0", 0, true},
		{"146", 146, true},
		{"0x92", 0x92, true},
		{"0XfE", 0xFE, true},
		{"0b1010", 10, true},
		{"0B11", 3, true},
		{"017", 15, true},
		{"0xFFFFFFFFFFFFFFFF", 1<<64 - 1, true},
		{"0x10000000000000000", 0, false}, // bignum
		{"08", 0, false},
		{"0x", 0, false},
		{"0b", 0, false},
		{"1_000", 0, false},
		{"0x1_0", 0, false},
		{"1.5", 0, false},
		{"12abc", 0, false},
		{"", 0, false},
		{"'A", 'A', true},
		{"'A'", 'A', true},
		{"' ", ' ', true},
		{"'\\n", '\n', true},
		{"'\\t'", '\t', true},
		{"'\\\\", '\\', true},
		{"'\\''", '\'', true},
		{"'\\x41", 0x41, true},
		{"'\\X7f", 0x7F, true},
		{"'\\101", 0o101, true},
		{"'\\0", 0, true},
		{"'\\x", 0, false},
		{"'\\x100", 0, false},
		{"'\\q", 0, false},
		{"'\\", 0, false},
		{"'", 0, false},
		{"''", 0, false},
		{"'ab", 0, false},
		{"'\u00e9", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseNumber(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseNumber(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "hex `0x0` - dec `0` - bin `0b0`"},
		{0x92, "hex `0x92` - dec `146` - bin `0b10010010`"},
		{0xFFFFFFFF, "hex `0xFFFFFFFF` - dec `4294967295` - bin `0b11111111111111111111111111111111`"},
		{0x100000000, "hex `0x100000000` - dec `4294967296`"},
	}
	for _, tc := range tests {
		if got := formatNumber(tc.in); got != tc.want {
			t.Errorf("formatNumber(%#x) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "`plain`"},
		{"a`b", "``a`b``"},
		{"a``b`c", "```a``b`c```"},
		{"`x", "`` `x ``"},
		{"x`", "`` x` ``"},
		{"a\nb\r\nc", "`a b c`"},
	}
	for _, tc := range tests {
		if got := code(tc.in); got != tc.want {
			t.Errorf("code(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"__start", "\\_\\_start"},
		{"*p <x> #GP", "\\*p \\<x\\> \\#GP"},
		{"a`b", "a\\`b"},
	}
	for _, tc := range tests {
		if got := escape(tc.in); got != tc.want {
			t.Errorf("escape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsSymbol(t *testing.T) {
	for in, want := range map[string]bool{
		"start": true, ".Lfoo": true, "_x1": true, "": false, "1abc": false, "a-b": false,
	} {
		if got := isSymbol(in); got != want {
			t.Errorf("isSymbol(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFormHasWidth(t *testing.T) {
	tests := []struct {
		ops   string
		bits  int
		want  bool
		sized bool
	}{
		{"imm8, r/m8", 8, true, true},
		{"imm32, r/m64", 32, false, true},
		{"imm32, r/m64", 64, true, true},
		{"r16, r/m16", 16, true, true},
		{"m16, r16", 16, true, true},
		{"%dx, %al", 8, true, true},
		{"%dx, %al", 16, false, true},
		{"imm8, %eax", 32, true, true},
		{"rel8", 8, false, false},
		{"es", 16, false, false},
		{"(%si), %es:(%di)", 8, false, false},
		{"", 8, false, false},
	}
	for _, tt := range tests {
		got, sized := formWidth(tt.ops, tt.bits)
		if got != tt.want || sized != tt.sized {
			t.Errorf("formWidth(%q, %d) = %v, %v; want %v, %v", tt.ops, tt.bits, got, sized, tt.want, tt.sized)
		}
	}
}

func TestFormsLineSuffixAndOrder(t *testing.T) {
	inst := &isa.Instruction{
		Name: "x",
		Forms: []isa.Form{
			{Operands: "zmm, zmm", Level: 4},
			{Operands: "xmm, xmm", Level: 0},
			{Operands: "ymm, ymm", Level: 3},
		},
	}
	if got, want := formsLine(inst, ""), "`x ymm, ymm` `x zmm, zmm` `x xmm, xmm`"; got != want {
		t.Errorf("order: got %q, want %q", got, want)
	}

	sized := &isa.Instruction{
		Name: "mov",
		Forms: []isa.Form{
			{Operands: "imm8, r/m8"},
			{Operands: "imm16, r/m16"},
			{Operands: "r/m16, r16"},
			{Operands: "imm32, r/m64"},
			{Operands: "es"},
		},
	}
	if got, want := formsLine(sized, "w"), "`mov imm16, r/m16` `mov r/m16, r16` `mov es`"; got != want {
		t.Errorf("suffix w: got %q, want %q", got, want)
	}
	// No form of the suffix width: keep everything rather than show nothing.
	str := &isa.Instruction{Name: "movs", Forms: []isa.Form{{Operands: "(%si), %es:(%di)"}}}
	if got := formsLine(str, "b"); got == "" {
		t.Error("suffix with no sized forms must fall back to all forms")
	}
}
