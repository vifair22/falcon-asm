package hover

import (
	"os"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// at returns the LSP position of the nth (0-based) occurrence of sub on the
// given zero-based line, plus extra bytes into it.
func at(t *testing.T, doc *docstore.Document, line int, sub string, extra int) protocol.Position {
	t.Helper()
	text, ok := doc.Line(line)
	if !ok {
		t.Fatalf("no line %d", line)
	}
	i := strings.Index(text, sub)
	if i < 0 {
		t.Fatalf("line %d %q has no %q", line, text, sub)
	}
	return protocol.Position{Line: line, Character: docstore.UTF16Len(text[:i+extra])}
}

// lineOf finds the zero-based line whose trimmed text starts with prefix.
func lineOf(t *testing.T, doc *docstore.Document, prefix string) int {
	t.Helper()
	for n := 0; n < doc.LineCount(); n++ {
		text, _ := doc.Line(n)
		if strings.HasPrefix(strings.TrimSpace(text), prefix) {
			return n
		}
	}
	t.Fatalf("no line starting with %q", prefix)
	return -1
}

func loadPmode(t *testing.T) *docstore.Document {
	t.Helper()
	src, err := os.ReadFile("../../testdata/falcon-bios/pmode.S")
	if err != nil {
		t.Fatal(err)
	}
	return &docstore.Document{URI: "file:///pmode.S", Text: string(src)}
}

func requireXED(t *testing.T) {
	t.Helper()
	if _, _, ok := isa.Lookup("mov"); !ok {
		t.Skip("xed.json not populated yet: mov does not resolve")
	}
}

func TestHoverPmode(t *testing.T) {
	doc := loadPmode(t)
	tests := []struct {
		name    string
		line    string // trimmed line prefix that identifies the line
		sub     string // text on that line to hover
		xed     bool   // needs generated data
		want    []string
		notWant []string
	}{
		{name: "movw", line: "movw    $0x2401", sub: "movw", xed: true,
			want: []string{"**movw** = mov + w (16-bit)", "Flags: "}},
		{name: "testb", line: "testb", sub: "testb", xed: true,
			want: []string{"**testb** = test + b (8-bit)", "writes"}},
		{name: "jnz", line: "jnz", sub: "jnz", xed: true, want: []string{"**jnz**", "reads"}},
		{name: "register ax", line: "movw    $0x2401", sub: "%ax",
			want: []string{"**%ax** - Accumulator", "gpr, 16-bit"}, notWant: []string{"64-bit mode only"}},
		{name: "cli", line: "cli", sub: "cli",
			want:    []string{"**cli** - Clear Interrupt Flag", "FLAGOP", "Flags: writes IF VIF; reads IOPL VM", "IOPL-sensitive"},
			notWant: []string{"CPL 0"}},
		{name: "inb", line: "inb", sub: "inb",
			want: []string{"**inb** = in + b (8-bit) - Input from Port", "`in imm8, %al`", "`in %dx, %al`", "IO", "Flags: none"}},
		{name: "outb", line: "outb", sub: "outb",
			want: []string{"**outb** = out + b (8-bit) - Output to Port", "`out %al, imm8`"}},
		{name: "code16", line: ".code16", sub: ".code16",
			want: []string{"**.code16** - Assemble following code for 16-bit mode", "`.code16`"}},
		{name: "section", line: ".section", sub: ".section",
			want: []string{"**.section**", "`.section name[, \"flags\""}},
		{name: "local label forward", line: "jnz", sub: "1f",
			want: []string{"**1f**", "Local label 1, forward -> line 55", "`1:`"}},
		{name: "immediate hex", line: "inb", sub: "$0x92",
			want: []string{"**$0x92**", "hex `0x92` - dec `146` - bin `0b10010010`"}},
		{name: "cr0", line: "movl    %cr0", sub: "%cr0",
			want: []string{"**%cr0** - Control register 0", "control, 64-bit", "Bits: `PE(0) MP(1)"}},
		{name: "lgdt", line: "lgdt", sub: "lgdt",
			want:    []string{"**lgdt** - Load Global Descriptor Table Register", "`lgdt m16&32` `lgdt m16&64`", "SYSTEM - CPL 0", "Flags: none", "Valid in real mode"},
			notWant: []string{"x86-64-v", "more)"}},
		{name: "label ref", line: "lgdt", sub: "gdt_desc",
			want: []string{"**gdt\\_desc** - label", "Label defined at line 147", "Exported (.globl)"}},
		{name: "label def", line: "a20_ok:", sub: "a20_ok",
			want: []string{"**a20\\_ok** - label", "Label defined at line 74"}, notWant: []string{"Exported"}},
		{name: "globl arg", line: ".globl  pmode_enter", sub: "pmode_enter",
			want: []string{"Label defined at line 28", "Exported (.globl)"}},
		{name: "big number no binary", line: ".quad   0x00CF9A", sub: "0x00CF",
			want: []string{"hex `0xCF9A000000FFFF` - dec `58434644969848831`"}, notWant: []string{"bin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.xed {
				requireXED(t)
			}
			line := lineOf(t, doc, tc.line)
			pos := at(t, doc, line, tc.sub, 0)
			h, err := Hover(doc, pos)
			if err != nil {
				t.Fatal(err)
			}
			if h == nil {
				t.Fatalf("nil hover for %q", tc.sub)
				return
			}
			checkHover(t, h, tc.want, tc.notWant)
			if h.Range == nil || h.Range.Start != pos || h.Range.End.Character-h.Range.Start.Character < len(tc.sub) {
				t.Errorf("range %+v does not start at %+v and cover %q", h.Range, pos, tc.sub)
			}
		})
	}
}

func checkHover(t *testing.T, h *protocol.Hover, want, notWant []string) {
	t.Helper()
	v := h.Contents.Value
	if h.Contents.Kind != protocol.Markdown {
		t.Errorf("kind %q", h.Contents.Kind)
	}
	for _, w := range want {
		if !strings.Contains(v, w) {
			t.Errorf("missing %q in:\n%s", w, v)
		}
	}
	for _, w := range notWant {
		if strings.Contains(v, w) {
			t.Errorf("unexpected %q in:\n%s", w, v)
		}
	}
	for i := 0; i < len(v); i++ {
		if v[i] >= 0x80 {
			t.Fatalf("non-ASCII byte in hover:\n%s", v)
		}
	}
	if n := strings.Count(v, "\n\n") + 1; n > 6 {
		t.Errorf("%d lines, want at most 6:\n%s", n, v)
	}
}

func TestHoverSuffixedSystemMnemonic(t *testing.T) {
	// lgdtl is not in the fixture, but it is what 32-bit code writes.
	doc := &docstore.Document{Text: "\tlgdtl gdt_desc\n"}
	h, err := Hover(doc, protocol.Position{Line: 0, Character: 2})
	if err != nil || h == nil {
		t.Fatalf("hover = %v, %v", h, err)
	}
	checkHover(t, h, []string{"**lgdtl** = lgdt + l (32-bit) - Load Global Descriptor Table Register"}, nil)
	if want := (protocol.Range{Start: protocol.Position{Character: 1}, End: protocol.Position{Character: 6}}); *h.Range != want {
		t.Errorf("range %+v, want %+v", *h.Range, want)
	}
}

func TestHoverInline(t *testing.T) {
	src := strings.Join([]string{
		/* 0 */ ".macro SAVE reg",
		/* 1 */ "\tpush \\reg",
		/* 2 */ ".endm",
		/* 3 */ "start:",
		/* 4 */ "\tSAVE %rax",
		/* 5 */ "\trep movsb",
		/* 6 */ "\tlock; incl (%eax)",
		/* 7 */ "\trex.w movsb",
		/* 8 */ "\tmovl $start, %eax",
		/* 9 */ "\tmovl $undefined_sym, %eax",
		/* 10 */ "\tmovb $'A, %al",
		/* 11 */ "\tjmp 2b",
		/* 12 */ "2:\tjmp 2b",
		/* 13 */ "\tjmp 3f",
		/* 14 */ "#include <foo.h>",
		/* 15 */ "\t.asciz \"hi\" // done",
		/* 16 */ "\tswapgs",
		/* 17 */ "\tpusha",
		/* 18 */ "\trdtscp",
		/* 19 */ "\tlahf",
		/* 20 */ "\t.nosuchdirective",
		/* 21 */ "\tmovl %nosuch, %eax",
		/* 22 */ "\tcs movsb",
		/* 23 */ "\t{vex} vpaddd %ymm0, %ymm1, %ymm2",
		/* 24 */ "\tmovl 0x10(%r9), %eax",
		/* 25 */ "\t.long 1.5",
		/* 26 */ "\tmovl $0, %eax",
		/* 27 */ "\tmovl $(1<<4), %eax",
		/* 28 */ "\tunknownmacro",
		/* 29 */ "\tiretq",
	}, "\n")
	doc := &docstore.Document{Text: src}
	tests := []struct {
		name    string
		line    int
		sub     string
		extra   int
		want    []string
		notWant []string
		isNil   bool
	}{
		{name: "macro use", line: 4, sub: "SAVE", want: []string{"**SAVE** - macro", "Macro defined at line 1", "`.macro SAVE reg`"}},
		{name: "unknown macro", line: 28, sub: "unknownmacro", isNil: true},
		{name: "rep prefix", line: 5, sub: "rep", want: []string{"**rep** - prefix - Repeat"}},
		{name: "lock prefix", line: 6, sub: "lock", want: []string{"**lock** - prefix - Make the read-modify-write"}},
		{name: "rex.w", line: 7, sub: "rex.w", want: []string{"**rex.w** - prefix - Explicit REX"}},
		{name: "segment prefix", line: 22, sub: "cs", want: []string{"**cs** - prefix - Segment override"}},
		{name: "pseudo prefix", line: 23, sub: "{vex}", want: []string{"**{vex}** - prefix - Pseudo prefix"}},
		{name: "64-bit register", line: 4, sub: "%rax", want: []string{"gpr, 64-bit, 64-bit mode only"}},
		{name: "r9 in memory operand", line: 24, sub: "%r9", want: []string{"**%r9**", "64-bit mode only"}},
		{name: "displacement number", line: 24, sub: "0x10", want: []string{"dec `16`"}},
		{name: "immediate label", line: 8, sub: "$start", want: []string{"**start** - label", "Label defined at line 4"}},
		{name: "immediate unknown symbol", line: 9, sub: "$undefined", isNil: true},
		{name: "char immediate", line: 10, sub: "$'A", want: []string{"hex `0x41` - dec `65`"}},
		{name: "local backward", line: 11, sub: "2b", want: []string{"Local label 2, backward: no definition found"}},
		{name: "local backward same line", line: 12, sub: "2b", want: []string{"Local label 2, backward -> line 13", "`2:\tjmp 2b`"}},
		{name: "local forward missing", line: 13, sub: "3f", want: []string{"Local label 3, forward: no definition found"}},
		{name: "local def", line: 12, sub: "2:", isNil: true},
		{name: "cpp directive", line: 14, sub: "#include", isNil: true},
		{name: "cpp arg", line: 14, sub: "<foo", isNil: true},
		{name: "string", line: 15, sub: "\"hi\"", isNil: true},
		{name: "comment", line: 15, sub: "// done", extra: 3, isNil: true},
		{name: "64-bit only", line: 16, sub: "swapgs", want: []string{"64-bit only"}},
		{name: "not in 64-bit", line: 17, sub: "pusha", want: []string{"not in 64-bit mode"}},
		{name: "outside level scheme", line: 18, sub: "rdtscp", want: []string{"ISA RDTSCP"}},
		{name: "level v2", line: 19, sub: "lahf", want: []string{"x86-64-v2 (LAHF)"}},
		{name: "unknown directive", line: 20, sub: ".nosuch", isNil: true},
		{name: "unknown register", line: 21, sub: "%nosuch", isNil: true},
		{name: "float is not an integer", line: 25, sub: "1.5", isNil: true},
		{name: "zero", line: 26, sub: "$0", want: []string{"hex `0x0` - dec `0` - bin `0b0`"}},
		{name: "bare dollar", line: 27, sub: "$", isNil: true},
		{name: "whitespace", line: 26, sub: "\tmovl", isNil: true},
		{name: "end of word", line: 16, sub: "swapgs", extra: 6, want: []string{"**swapgs**"}},
		{name: "comma after unknown register", line: 21, sub: ", %eax", isNil: true},
		{name: "end of register before comma", line: 4, sub: "%rax", extra: 4, want: []string{"**%rax**"}},
		{name: "iretq", line: 29, sub: "iretq", want: []string{"**iretq**", "64-bit only"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := Hover(doc, at(t, doc, tc.line, tc.sub, tc.extra))
			if err != nil {
				t.Fatal(err)
			}
			if tc.isNil {
				if h != nil {
					t.Fatalf("want nil, got:\n%s", h.Contents.Value)
				}
				return
			}
			if h == nil {
				t.Fatalf("nil hover")
			}
			checkHover(t, h, tc.want, tc.notWant)
		})
	}
}

func TestHoverOutOfRange(t *testing.T) {
	// Trailing spaces, so a character clamped to the line end is not at the
	// end of "cli" (where hovering is legitimate).
	doc := &docstore.Document{Text: "\tcli   \n"}
	if _, err := Hover(doc, protocol.Position{Line: -1}); err == nil {
		t.Error("negative position: want error")
	}
	for _, p := range []protocol.Position{{Line: 5}, {Line: 1}, {Line: 0, Character: 99}} {
		h, err := Hover(doc, p)
		if err != nil || h != nil {
			t.Errorf("%+v: got %v, %v; want nil, nil", p, h, err)
		}
	}
}

// TestHoverUTF16Range puts multi-byte characters (a 2-byte e-acute and a
// 4-byte emoji, which is a surrogate pair in UTF-16) in a comment before
// the hovered token, so byte columns and UTF-16 columns disagree.
func TestHoverUTF16Range(t *testing.T) {
	doc := &docstore.Document{Text: "/* caf\u00e9 \U0001F600 */ cli\n"}
	text, _ := doc.Line(0)
	byteCol := strings.Index(text, "cli")
	char := docstore.UTF16Len(text[:byteCol])
	if char == byteCol {
		t.Fatal("test text does not exercise UTF-16 conversion")
	}
	h, err := Hover(doc, protocol.Position{Line: 0, Character: char + 1})
	if err != nil || h == nil {
		t.Fatalf("hover = %v, %v", h, err)
	}
	want := protocol.Range{Start: protocol.Position{Character: char}, End: protocol.Position{Character: char + 3}}
	if *h.Range != want {
		t.Errorf("range %+v, want %+v", *h.Range, want)
	}
	if !strings.Contains(h.Contents.Value, "**cli**") {
		t.Errorf("wrong hover:\n%s", h.Contents.Value)
	}
}

func TestTokenAt(t *testing.T) {
	toks := lexer.Tokenize("\tmovl %eax,%ebx\nx:\n")
	// Columns: \t=0 movl=1..4 ' '=5 %eax=6..9 ,=10 %ebx=11..14
	tests := []struct {
		line, col int
		want      string
	}{
		{0, 0, ""},
		{0, 1, "movl"},
		{0, 5, "movl"},  // end of word, before a space
		{0, 6, "%eax"},  // start of register
		{0, 10, "%eax"}, // comma starts here but %eax ends here
		{0, 11, "%ebx"},
		{0, 15, "%ebx"},
		{0, 16, ""},
		{1, 0, "x"},
		{1, 1, "x"}, // colon starts here; the label ends here
		{2, 0, ""},
	}
	for _, tc := range tests {
		got := ""
		if i := tokenAt(toks, tc.line, tc.col); i >= 0 {
			got = toks[i].Text
		}
		if got != tc.want {
			t.Errorf("tokenAt(%d,%d) = %q, want %q", tc.line, tc.col, got, tc.want)
		}
	}
}

func TestLocalLabelResolution(t *testing.T) {
	src := "1:\n\tjmp 1f\n\tjmp 1b\n1:\tnop\n\tjmp 1b\n10:\n\tjmp 10b\n"
	doc := &docstore.Document{Text: src}
	toks := lexer.Tokenize(src)
	tests := []struct {
		line int
		ref  string
		want string
	}{
		{1, "1f", "forward -> line 4"},
		{2, "1b", "backward -> line 1"},
		{4, "1b", "backward -> line 4"},
		{6, "10b", "Local label 10, backward -> line 6"},
	}
	for _, tc := range tests {
		idx := -1
		for i, tk := range toks {
			if tk.Line == tc.line && tk.Text == tc.ref {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("no %q on line %d", tc.ref, tc.line)
		}
		got := localLabelHover(doc, toks, idx)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s on line %d: %q lacks %q", tc.ref, tc.line, got, tc.want)
		}
	}
}

func TestLevelText(t *testing.T) {
	tests := []struct {
		name  string
		forms []isa.Form
		want  string
	}{
		{"none", nil, ""},
		{"baseline", []isa.Form{{ISASet: "I386", Level: 1}}, ""},
		{"lowest wins", []isa.Form{{ISASet: "AVX2", Level: 3}, {ISASet: "SSE2", Level: 1}}, ""},
		{"v3", []isa.Form{{ISASet: "AVX512F_512", Level: 4}, {ISASet: "BMI2", Level: 3}}, "x86-64-v3 (BMI2)"},
		{"in-scheme beats outside", []isa.Form{{ISASet: "AES"}, {ISASet: "AVX", Level: 3}}, "x86-64-v3 (AVX)"},
		{"outside only", []isa.Form{{ISASet: "VMX"}}, "ISA VMX"},
		{"unknown set", []isa.Form{{}}, ""},
	}
	for _, tc := range tests {
		if got := levelText(tc.forms); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFlagsAndMeta(t *testing.T) {
	inst := &isa.Instruction{
		Name: "x", CPL: 3, Modes: "16", FlagsWritten: "CF  ZF", FlagsUndefined: "AF",
	}
	if got, want := flagsLine(inst), "Flags: writes CF ZF; undefined AF"; got != want {
		t.Errorf("flags %q, want %q", got, want)
	}
	if got, want := metaLine(inst), "modes 16"; got != want {
		t.Errorf("meta %q, want %q", got, want)
	}
	if got := modesText("16, 32, 64"); got != "" {
		t.Errorf("all modes: %q", got)
	}
	many := &isa.Instruction{Name: "m"}
	for _, ops := range []string{"a", "b", "a", "c", "d", "e", "f", ""} {
		many.Forms = append(many.Forms, isa.Form{Operands: ops})
	}
	if got, want := formsLine(many, ""), "`m a` `m b` `m c` `m d` (+2 more)"; got != want {
		t.Errorf("forms %q, want %q", got, want)
	}
}

func TestPrefixHoverUnknown(t *testing.T) {
	if got := prefixHover("movl"); got != "" {
		t.Errorf("non-prefix rendered %q", got)
	}
}
