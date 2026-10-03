package analysis

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func doc(text string) *docstore.Document {
	return &docstore.Document{URI: "file:///t.S", Text: text}
}

func messages(r Result) []string {
	var out []string
	for _, d := range r.Diagnostics {
		out = append(out, d.Code+": "+d.Message)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestModeAtLine(t *testing.T) {
	src := "\tnop\n" + // 0
		"\t.code16\n" + // 1
		"\tnop\n" + // 2
		"\t.code32\n" + // 3
		"\tnop\n" + // 4
		"\t.CODE64\n" + // 5
		"\t.code16gcc\n" + // 6
		"\tnop" // 7
	tests := []struct {
		name string
		init Mode
		want []Mode
	}{
		{"default 64", 0, []Mode{64, 64, 16, 16, 32, 32, 64, 16}},
		{"from flags 32", 32, []Mode{32, 32, 16, 16, 32, 32, 64, 16}},
		{"invalid treated as 64", 48, []Mode{64, 64, 16, 16, 32, 32, 64, 16}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Analyze(doc(src), Config{InitialMode: tt.init}).ModeAtLine
			if len(got) != len(tt.want) {
				t.Fatalf("len %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("line %d: got %v, want %v (all %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// ModeAtLine follows docstore's line numbering, including CRLF, a lone CR
// and a final empty line.
func TestModeAtLineLineEndings(t *testing.T) {
	tests := []struct {
		src  string
		want []Mode
	}{
		{"", []Mode{64}},
		{".code32\r\nnop\r\n", []Mode{64, 32, 32}},
		{".code16\nnop\n", []Mode{64, 16, 16}},
		// The lexer does not split at a lone '\r' (DESIGN known limit), so
		// .code32 reads as an operand there; the table still has one entry
		// per docstore line.
		{"nop\r.code32\nnop", []Mode{64, 64, 64}},
	}
	for _, tt := range tests {
		d := doc(tt.src)
		got := Analyze(d, Config{}).ModeAtLine
		if len(got) != d.LineCount() {
			t.Fatalf("%q: %d entries for %d lines", tt.src, len(got), d.LineCount())
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("%q: got %v, want %v", tt.src, got, tt.want)
			}
		}
	}
}

func TestModeDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		src  string
		init Mode
		want []string
	}{
		{
			"64-bit register in 16-bit code after .code16",
			"\tnop\n\t.code16\n\tmov %rax, %rbx\n", 32,
			[]string{
				"mode-register: %rax is only valid in 64-bit code (current mode: 16-bit, set by .code16 at line 2)",
				"mode-register: %rbx is only valid in 64-bit code (current mode: 16-bit, set by .code16 at line 2)",
			},
		},
		{
			"r8d sil xmm8 in 32-bit from flags",
			"\tmovl %r8d, %eax\n\tmovb %sil, %al\n\tmovdqa %xmm8, %xmm7\n", 32,
			[]string{
				"mode-register: %r8d is only valid in 64-bit code (current mode: 32-bit, from compile flags)",
				"mode-register: %sil is only valid in 64-bit code (current mode: 32-bit, from compile flags)",
				"mode-register: %xmm8 is only valid in 64-bit code (current mode: 32-bit, from compile flags)",
			},
		},
		{
			"register case is normalised in the message",
			"\tmov %RAX, %eax\n", 32,
			[]string{"mode-register: %rax is only valid in 64-bit code (current mode: 32-bit, from compile flags)"},
		},
		{
			"64-bit registers are fine in 64-bit code",
			"\tmov %rax, %r8\n\tmovb %sil, %r9b\n\tmovdqa %xmm15, %xmm8\n", 64, nil,
		},
		{
			"legacy registers are fine everywhere",
			"\t.code16\n\tmov %eax, %ebx\n\tmov %cr0, %eax\n\tmovdqa %xmm7, %xmm0\n", 0, nil,
		},
		{
			"pusha in 64-bit code by default",
			"\tpusha\n", 0,
			[]string{"mode-instruction: pusha is only valid in 16/32-bit code (current mode: 64-bit, default)"},
		},
		{
			"pushal alias in 64-bit code",
			"\tpushal\n", 64,
			[]string{"mode-instruction: pushal is only valid in 16/32-bit code (current mode: 64-bit, from compile flags)"},
		},
		{
			"iretq in 32-bit code",
			"\t.code32\n\tiretq\n", 64,
			[]string{"mode-instruction: iretq is only valid in 64-bit code (current mode: 32-bit, set by .code32 at line 1)"},
		},
		{
			"swapgs in 16-bit code",
			"\t.code16gcc\n\tSWAPGS\n", 64,
			[]string{"mode-instruction: swapgs is only valid in 64-bit code (current mode: 16-bit, set by .code16gcc at line 1)"},
		},
		{
			"pusha in 16 and 32 is fine",
			"\t.code16\n\tpusha\n\t.code32\n\tpusha\n", 64, nil,
		},
		{
			"mode switch mid-line applies to the next statement",
			"\t.code64; push %rax\n", 32, nil,
		},
		{
			"prefix and label before the mnemonic",
			"foo: lock incl (%r8)\n", 32,
			[]string{"mode-register: %r8 is only valid in 64-bit code (current mode: 32-bit, from compile flags)"},
		},
		{
			"unknown mnemonics are not checked",
			"\tfrobnicate %rax\n", 32, nil,
		},
		{
			"registers in directives are not checked",
			"\t.cfi_offset %rbp, -16\n", 32, nil,
		},
		{
			".macro body is not checked and does not switch mode",
			".macro M r\n\t.code64\n\tpush %rax\n\tpusha\n.endm\n\tpush %rax\n", 32,
			[]string{"mode-register: %rax is only valid in 64-bit code (current mode: 32-bit, from compile flags)"},
		},
		{
			"nested .macro bodies",
			".macro A\n.macro B\n\tpush %rax\n.endm\n\tpush %rax\n.endm\n\tpushl %eax\n", 32, nil,
		},
		{
			"stray .endm is harmless",
			".endm\n\tpush %rax\n", 32,
			[]string{"mode-register: %rax is only valid in 64-bit code (current mode: 32-bit, from compile flags)"},
		},
		{
			"comments and strings are not code",
			"# push %rax\n/* pusha */ nop // push %rax\n\t.ascii \"%rax\"\n", 0, nil,
		},
		{
			"cpp macro bodies are not code",
			"#define SAVE push %rax ; \\\n\tpusha\n\tnop\n", 32, nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messages(Analyze(doc(tt.src), Config{InitialMode: tt.init}))
			if !sameStrings(got, tt.want) {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// A mnemonic-position word that names a macro is an invocation: its
// operands are macro arguments and must not be checked.
func TestMacroInvocationsSkipped(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		macros map[string]string
	}{
		{"toolchain macro", "\tpusha %rax\n", map[string]string{"pusha": "x"}},
		{"in-file #define", "#define pusha nop\n\tpusha %rax\n", nil},
		{"in-file #define with spaces", "# define pusha() nop\n\tpusha %rax\n", nil},
		{"in-file .macro", ".macro pusha r\n\tnop\n.endm\n\tpusha %rax\n", nil},
		{".macro used before its definition", "\tPUSHA %rax\n.macro pusha r\n.endm\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Analyze(doc(tt.src), Config{InitialMode: 64, Level: 1, Macros: tt.macros})
			if len(r.Diagnostics) != 0 {
				t.Fatalf("unexpected: %q", messages(r))
			}
		})
	}
}

func TestLevelDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		level  isa.Level
		source string
		want   []string
	}{
		{
			"vpermd ymm with target v3 is fine",
			"\tvpermd %ymm0, %ymm1, %ymm2\n", 3, "-march=x86-64-v3", nil,
		},
		{
			"vpermd zmm with target v3",
			"\tvpermd %zmm0, %zmm1, %zmm2\n", 3, "-march=x86-64-v3",
			[]string{"level: vpermd with zmm operands needs x86-64-v4 (AVX512F); target is v3 (from -march=x86-64-v3)"},
		},
		{
			"vpermd ymm with target v2",
			"\tvpermd (%rax), %ymm1, %ymm2\n", 2, "",
			[]string{"level: vpermd with ymm operands needs x86-64-v3 (AVX2); target is v2"},
		},
		{
			"vpaddd xmm is the VEX form",
			"\tvpaddd %xmm0, %xmm1, %xmm2\n", 2, "",
			[]string{"level: vpaddd needs x86-64-v3 (AVX); target is v2"},
		},
		{
			"vpaddd xmm with target v3 is fine",
			"\tvpaddd %xmm0, %xmm1, %xmm2\n", 3, "", nil,
		},
		{
			"opmask decoration selects EVEX",
			"\tvpaddd %xmm0, %xmm1, %xmm2{%k1}{z}\n", 3, "",
			[]string{"level: vpaddd with AVX-512 operands needs x86-64-v4 (AVX512F); target is v3"},
		},
		{
			"xmm16 selects EVEX",
			"\tvpaddd %xmm16, %xmm1, %xmm2\n", 3, "",
			[]string{"level: vpaddd with AVX-512 operands needs x86-64-v4 (AVX512F); target is v3"},
		},
		{
			"ymm17 selects EVEX",
			"\tvpermd %ymm17, %ymm1, %ymm2\n", 3, "",
			[]string{"level: vpermd with AVX-512 operands needs x86-64-v4 (AVX512F); target is v3"},
		},
		{
			"{evex} pseudo prefix selects EVEX",
			"\t{evex} vpaddd %xmm0, %xmm1, %xmm2\n", 3, "",
			[]string{"level: vpaddd with AVX-512 operands needs x86-64-v4 (AVX512F); target is v3"},
		},
		{
			"kmovw opmask registers",
			"\tkmovw %k1, %eax\n", 3, "",
			[]string{"level: kmovw with AVX-512 operands needs x86-64-v4 (AVX512F); target is v3"},
		},
		{
			"EVEX-only instruction with xmm operands falls back to all forms",
			"\tvpternlogd $0x96, %xmm0, %xmm1, %xmm2\n", 3, "",
			[]string{"level: vpternlogd needs x86-64-v4 (AVX512F); target is v3"},
		},
		{
			"popcnt above v1",
			"\tpopcntl %eax, %ebx\n", 1, "-march=x86-64",
			[]string{"level: popcntl needs x86-64-v2 (POPCNT); target is v1 (from -march=x86-64)"},
		},
		{
			"baseline instruction",
			"\tmovl %eax, %ebx\n\tlgdt (%eax)\n", 1, "", nil,
		},
		{
			"level 0 ISA set is information",
			"\taesenc %xmm0, %xmm1\n", 3, "",
			[]string{"level-outside: aesenc is outside the x86-64 level scheme: requires AES"},
		},
		{
			"no target means no level diagnostics",
			"\tvpermd %zmm0, %zmm1, %zmm2\n\taesenc %xmm0, %xmm1\n", 0, "", nil,
		},
		{
			"in-file override beats the config",
			"/* falcon-asm: level=v2 */\n\tvpaddd %xmm0, %xmm1, %xmm2\n", 4, "-march=x86-64-v4",
			[]string{"level: vpaddd needs x86-64-v3 (AVX); target is v2 (from falcon-asm: level comment at line 1)"},
		},
		{
			"in-file none disables",
			"# falcon-asm: level=none\n\tvpermd %zmm0, %zmm1, %zmm2\n", 3, "", nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Analyze(doc(tt.src), Config{InitialMode: 64, Level: tt.level, LevelSource: tt.source})
			got := messages(r)
			if !sameStrings(got, tt.want) {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
			for _, d := range r.Diagnostics {
				want := protocol.SeverityWarning
				if d.Code == CodeLevelOutside {
					want = protocol.SeverityInformation
				}
				if d.Severity != want || d.Source != Source {
					t.Fatalf("severity %v source %q", d.Severity, d.Source)
				}
			}
		})
	}
}

func TestDiagnosticRangeUTF16(t *testing.T) {
	// The block comment holds a character outside the BMP (two UTF-16
	// units, four bytes) and one inside it (one unit, two bytes).
	src := "\tnop\n/* \U0001F600\u00e9 */ mov %rax, %eax\n"
	r := Analyze(doc(src), Config{InitialMode: 32})
	if len(r.Diagnostics) != 1 {
		t.Fatalf("got %q", messages(r))
	}
	got := r.Diagnostics[0].Range
	// "/* " = 3, emoji = 2, e-acute = 1, " */ mov " = 8 -> 14.
	want := protocol.Range{Start: protocol.Position{Line: 1, Character: 14}, End: protocol.Position{Line: 1, Character: 18}}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLevelOverride(t *testing.T) {
	tests := []struct {
		src  string
		want isa.Level
		ok   bool
	}{
		{"/* falcon-asm: level=v3 */", 3, true},
		{"# falcon-asm: level=v1\n", 1, true},
		{"nop // FALCON-ASM: Level = V4", 4, true},
		{"/* falcon-asm:level=x86-64-v2 */", 2, true},
		{"/* falcon-asm: level=x86-64 */", 1, true},
		{"/* falcon-asm: level=none */", 0, true},
		{"/* falcon-asm: level=v5 */", 0, false},
		{"/* falcon-asm: level v3 */", 0, false},
		{"/* falcon-asm: mode=16 */", 0, false},
		{"/* falcon-asm: level=v9 falcon-asm: level=v2 */", 2, true},
		{"/* falcon-asm: level=v3 */\n# falcon-asm: level=v1", 3, true},
		{"/* falcon-asm: level=v7 */\n# falcon-asm: level=v1", 1, true},
		{"\t.ascii \"falcon-asm: level=v3\"\n", 0, false},
		{"#define X falcon-asm: level=v3\n", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := LevelOverride(tt.src)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%q: got (%d, %v), want (%d, %v)", tt.src, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSymbols(t *testing.T) {
	src := `#include <x.h>
#define PORT 0x3F8
#define TWO(a, b) \
	((a) + \
	 (b)) /* sum */
	.set	SEL_C32, 0x08
	.equ	SEL_D32,0x10
LIMIT = 0xFFFF
	.section .text.boot, "ax"
	.globl	start, LIMIT
start:	cli
1:	hlt
	jmp 1b
.macro MARK ch, n=1
local:	nop
2:	nop
	.set INNER, 1
.endm
	.data
msg:	.ascii "hi"
	.previous
after:	nop
.macro OPEN
`
	r := Analyze(doc(src), Config{InitialMode: 32})
	type sym struct {
		Name   string
		Kind   SymbolKind
		Global bool
		Detail string
	}
	want := []sym{
		{"PORT", Macro, false, "0x3F8"},
		{"TWO", Macro, false, "(a, b) ((a) + (b))"},
		{"SEL_C32", Equ, false, "0x08"},
		{"SEL_D32", Equ, false, "0x10"},
		{"LIMIT", Equ, true, "0xFFFF"},
		{".text.boot", Section, false, `"ax"`},
		{"start", Label, true, ".text.boot"},
		{"1", LocalLabel, false, ".text.boot"},
		{"MARK", AsmMacro, false, "ch, n=1"},
		{".data", Section, false, ""},
		{"msg", Label, false, ".data"},
		{"after", Label, false, ""},
		{"OPEN", AsmMacro, false, ""},
	}
	if len(r.Symbols) != len(want) {
		for _, s := range r.Symbols {
			t.Logf("%+v", s)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, s := range r.Symbols {
		g := sym{s.Name, s.Kind, s.Global, s.Detail}
		if g != want[i] {
			t.Errorf("symbol %d: got %+v, want %+v", i, g, want[i])
		}
		if !contains(s.Range, s.SelectionRange) {
			t.Errorf("%s: range %+v does not contain selection %+v", s.Name, s.Range, s.SelectionRange)
		}
	}

	rng := func(l1, c1, l2, c2 int) protocol.Range {
		return protocol.Range{Start: protocol.Position{Line: l1, Character: c1}, End: protocol.Position{Line: l2, Character: c2}}
	}
	ranges := map[string][2]protocol.Range{
		// #define with continuations runs to the end of its last segment.
		"TWO": {rng(2, 0, 4, 6), rng(2, 8, 2, 11)},
		// .set range opens at the directive.
		"SEL_C32": {rng(5, 1, 5, 19), rng(5, 6, 5, 13)},
		// A section ends on the line before the next switch.
		".text.boot": {rng(8, 1, 17, 5), rng(8, 10, 8, 20)},
		// .previous closes .data without opening anything.
		".data": {rng(18, 1, 19, 16), rng(18, 1, 18, 6)},
		// A label covers its line.
		"start": {rng(10, 0, 10, 10), rng(10, 0, 10, 5)},
		// .macro runs through .endm, or to the end of the file when open.
		"MARK": {rng(13, 0, 17, 5), rng(13, 7, 13, 11)},
		"OPEN": {rng(22, 0, 23, 0), rng(22, 7, 22, 11)},
	}
	for _, s := range r.Symbols {
		w, ok := ranges[s.Name]
		if !ok {
			continue
		}
		if s.Range != w[0] || s.SelectionRange != w[1] {
			t.Errorf("%s: got %+v / %+v, want %+v / %+v", s.Name, s.Range, s.SelectionRange, w[0], w[1])
		}
	}
}

func TestSymbolsEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string // Kind:Name
	}{
		{"label then .set on one line", "x: .set y, 1\n", []string{"Label:x", "Equ:y"}},
		{"#define without a name", "#define\n#define 9x\n", nil},
		{"#undef is not a definition", "#undef FOO\n", nil},
		{".macro without a name", ".macro\n\tnop\n.endm\nfoo:\n", []string{"Label:foo"}},
		{"section switch on the first line", ".text\n.data\n", []string{"Section:.text", "Section:.data"}},
		{"two sections on one line", ".text; .data\n", []string{"Section:.text", "Section:.data"}},
		{".section without a name", ".section\nfoo:\n", []string{"Label:foo"}},
		{".pushsection", ".pushsection .rodata\nfoo:\n.popsection\n", []string{"Section:.rodata", "Label:foo"}},
		{".set without a value", ".set foo\n", []string{"Equ:foo"}},
		{".globl applies to labels defined earlier", "foo:\n.global foo\n", []string{"Label:foo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Analyze(doc(tt.src), Config{})
			var got []string
			for _, s := range r.Symbols {
				got = append(got, s.Kind.String()+":"+s.Name)
				if !contains(s.Range, s.SelectionRange) {
					t.Errorf("%s: range %+v does not contain selection %+v", s.Name, s.Range, s.SelectionRange)
				}
			}
			if !sameStrings(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	r := Analyze(doc("foo:\n.global foo\n"), Config{})
	if !r.Symbols[0].Global {
		t.Fatal(".globl after the label was not applied")
	}
}

func before(a, b protocol.Position) bool {
	return a.Line < b.Line || (a.Line == b.Line && a.Character <= b.Character)
}

func contains(outer, inner protocol.Range) bool {
	return before(outer.Start, inner.Start) && before(inner.End, outer.End)
}

func TestStrings(t *testing.T) {
	if Mode16.String() != "16-bit" || Mode64.String() != "64-bit" {
		t.Fatal("Mode.String")
	}
	for k, want := range map[SymbolKind]string{
		Label: "Label", LocalLabel: "LocalLabel", Macro: "Macro",
		AsmMacro: "AsmMacro", Equ: "Equ", Section: "Section", SymbolKind(99): "SymbolKind(?)",
	} {
		if k.String() != want {
			t.Errorf("%d: %q", k, k.String())
		}
	}
}

func TestHelpers(t *testing.T) {
	if regNumber("xmm17") != 17 || regNumber("rax") != -1 {
		t.Fatal("regNumber")
	}
	if modesText("16, 32") != "16/32-bit" {
		t.Fatal(modesText("16, 32"))
	}
	if !modeIn(Mode32, "16, 32") || modeIn(Mode64, "16,32") {
		t.Fatal("modeIn")
	}
	for set, want := range map[string]string{
		"AVX512F_512": "AVX512F", "AVX512BW_KOPD": "AVX512BW", "AVX2": "AVX2",
	} {
		if got := setName(set, isa.LevelForISASet(set)); got != want {
			t.Errorf("setName(%q) = %q", set, got)
		}
	}
	if got := setName("AVX512_VNNI_256", 0); got != "AVX512_VNNI" {
		t.Errorf("setName level 0 = %q", got)
	}
}

// The falcon-bios fixtures are real, assembling code built with -m32 and no
// -march: every diagnostic on them would be a false positive. A baseline
// level target must not fire either, since the code is baseline x86.
func TestFixturesNoFalsePositives(t *testing.T) {
	files, err := filepath.Glob("../../testdata/falcon-bios/*.S")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		d := doc(string(b))
		for _, lvl := range []isa.Level{0, 1} {
			r := Analyze(d, Config{InitialMode: 32, Level: lvl})
			for _, diag := range r.Diagnostics {
				t.Errorf("%s (level %d) line %d: %s", filepath.Base(f), lvl, diag.Range.Start.Line+1, diag.Message)
			}
			if len(r.ModeAtLine) != d.LineCount() {
				t.Errorf("%s: ModeAtLine has %d entries for %d lines", filepath.Base(f), len(r.ModeAtLine), d.LineCount())
			}
		}
	}
}

// pmode.S is the mode-switching fixture: real mode from .code16 at line 21,
// protected mode from .code32 at line 109, back to 16 at line 125.
func TestFixturePmodeModes(t *testing.T) {
	b, err := os.ReadFile("../../testdata/falcon-bios/pmode.S")
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(doc(string(b)), Config{InitialMode: 32})
	for line, want := range map[int]Mode{1: 32, 21: 32, 22: 16, 109: 16, 110: 32, 125: 32, 126: 16} {
		if got := r.ModeAtLine[line-1]; got != want {
			t.Errorf("line %d: %v, want %v", line, got, want)
		}
	}
	var globals []string
	for _, s := range r.Symbols {
		if s.Global {
			globals = append(globals, s.Name)
		}
	}
	if strings.Join(globals, ",") != "pmode_enter,gdt_desc,pmode_entry" {
		t.Errorf("globals %q", globals)
	}
	// The same code assembled as 64-bit from the start still switches to
	// 16 before any instruction, so nothing in it may fire.
	if r := Analyze(doc(string(b)), Config{}); len(r.Diagnostics) != 0 {
		t.Errorf("unexpected: %q", messages(r))
	}
}

// AnalyzeTokens must be exactly Analyze when handed the document's tokens;
// the server relies on that to share one token stream per version.
func TestAnalyzeTokensMatchesAnalyze(t *testing.T) {
	doc := &docstore.Document{URI: "file:///t.S", Text: "\t.code16\n\tpushq %rax\n#define X 1\nfoo:\n"}
	want := Analyze(doc, Config{InitialMode: Mode32})
	got := AnalyzeTokens(doc, lexer.Tokenize(doc.Text), Config{InitialMode: Mode32})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AnalyzeTokens = %+v\nAnalyze = %+v", got, want)
	}
}
