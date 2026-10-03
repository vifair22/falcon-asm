package lexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
)

// tk is the compact form tests compare against: kind plus text, ignoring
// positions, which the invariant checks cover separately.
type tk struct {
	k Kind
	s string
}

func compact(toks []Token) []tk {
	out := make([]tk, 0, len(toks))
	for _, t := range toks {
		out = append(out, tk{t.Kind, t.Text})
	}
	return out
}

func equal(a, b []tk) bool {
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

func TestTokenizeRules(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []tk
	}{
		{"instruction", "\tmovw    $0x2401, %ax", []tk{
			{Mnemonic, "movw"}, {Immediate, "$0x2401"}, {Punct, ","}, {Register, "%ax"},
		}},
		{"no operands", "cli", []tk{{Mnemonic, "cli"}}},
		{"label def", "pmode_enter:", []tk{{LabelDef, "pmode_enter"}, {Punct, ":"}}},
		{"label then insn", "a20_ok: cli", []tk{{LabelDef, "a20_ok"}, {Punct, ":"}, {Mnemonic, "cli"}}},
		{"two labels", "a: b: nop", []tk{
			{LabelDef, "a"}, {Punct, ":"}, {LabelDef, "b"}, {Punct, ":"}, {Mnemonic, "nop"},
		}},
		{"space before colon", "foo :", []tk{{LabelDef, "foo"}, {Punct, ":"}}},
		{"dot label", ".Lloop: ret", []tk{{LabelDef, ".Lloop"}, {Punct, ":"}, {Mnemonic, "ret"}}},
		{"local label def", "1:\tmovw %ax, (%edi)", []tk{
			{LocalLabelDef, "1"}, {Punct, ":"}, {Mnemonic, "movw"}, {Register, "%ax"},
			{Punct, ","}, {Punct, "("}, {Register, "%edi"}, {Punct, ")"},
		}},
		{"local refs", "jnz 1f; loop 9903b", []tk{
			{Mnemonic, "jnz"}, {LocalLabelRef, "1f"}, {Punct, ";"},
			{Mnemonic, "loop"}, {LocalLabelRef, "9903b"},
		}},
		{"binary not local ref", ".byte 0b101", []tk{{Directive, ".byte"}, {Number, "0b101"}}},
		{"hex ending in b", "movb $0x1b, %al; .byte 0x1b", []tk{
			{Mnemonic, "movb"}, {Immediate, "$0x1b"}, {Punct, ","}, {Register, "%al"},
			{Punct, ";"}, {Directive, ".byte"}, {Number, "0x1b"},
		}},
		{"far jump", "ljmp $SEL_CODE16, $thunk_16", []tk{
			{Mnemonic, "ljmp"}, {Immediate, "$SEL_CODE16"}, {Punct, ","}, {Immediate, "$thunk_16"},
		}},
		{"memory operand", "movw %es:0x10(%bx,%si,2), %ax", []tk{
			{Mnemonic, "movw"}, {Register, "%es"}, {Punct, ":"}, {Number, "0x10"},
			{Punct, "("}, {Register, "%bx"}, {Punct, ","}, {Register, "%si"}, {Punct, ","},
			{Number, "2"}, {Punct, ")"}, {Punct, ","}, {Register, "%ax"},
		}},
		{"displacement macro", "movb %dh, DAP_HEADS(%si)", []tk{
			{Mnemonic, "movb"}, {Register, "%dh"}, {Punct, ","}, {LabelRef, "DAP_HEADS"},
			{Punct, "("}, {Register, "%si"}, {Punct, ")"},
		}},
		{"indirect", "jmp *%eax", []tk{{Mnemonic, "jmp"}, {Punct, "*"}, {Register, "%eax"}}},
		{"indirect mem", "jmp *pmode_entry", []tk{{Mnemonic, "jmp"}, {Punct, "*"}, {LabelRef, "pmode_entry"}}},
		{"paren immediate", "andl $((1 << 24) | 2), %edx", []tk{
			{Mnemonic, "andl"}, {Immediate, "$"}, {Punct, "("}, {Punct, "("}, {Number, "1"},
			{Punct, "<<"}, {Number, "24"}, {Punct, ")"}, {Punct, "|"}, {Number, "2"},
			{Punct, ")"}, {Punct, ","}, {Register, "%edx"},
		}},
		{"negative immediate", "andl $-16, %esp", []tk{
			{Mnemonic, "andl"}, {Immediate, "$"}, {Punct, "-"}, {Number, "16"},
			{Punct, ","}, {Register, "%esp"},
		}},
		{"char immediates", `movb $' ', %al; movb $'\r', %al; movb $'a, %al`, []tk{
			{Mnemonic, "movb"}, {Immediate, "$' '"}, {Punct, ","}, {Register, "%al"}, {Punct, ";"},
			{Mnemonic, "movb"}, {Immediate, `$'\r'`}, {Punct, ","}, {Register, "%al"}, {Punct, ";"},
			{Mnemonic, "movb"}, {Immediate, "$'a"}, {Punct, ","}, {Register, "%al"},
		}},
		{"char expr", "addb $('A' - '9' - 1), %al", []tk{
			{Mnemonic, "addb"}, {Immediate, "$"}, {Punct, "("}, {Number, "'A'"}, {Punct, "-"},
			{Number, "'9'"}, {Punct, "-"}, {Number, "1"}, {Punct, ")"}, {Punct, ","}, {Register, "%al"},
		}},
		{"octal char", `.byte '\012'`, []tk{{Directive, ".byte"}, {Number, `'\012'`}}},
		{"macro arg immediate", `movb $\ch, %al`, []tk{
			{Mnemonic, "movb"}, {Immediate, `$\ch`}, {Punct, ","}, {Register, "%al"},
		}},
		{"macro arg operand", `movl \src, \dst`, []tk{
			{Mnemonic, "movl"}, {Identifier, `\src`}, {Punct, ","}, {Identifier, `\dst`},
		}},
		{"macro arg statement", `\op %eax`, []tk{{Identifier, `\op`}, {Register, "%eax"}}},
		{"macro arg label", `\name: nop`, []tk{{Identifier, `\name`}, {Punct, ":"}, {Mnemonic, "nop"}}},
		{"lone backslash", `\ x`, []tk{{Punct, `\`}, {LabelRef, "x"}}},
		{"macro def", ".macro MARK32 ch, n=1", []tk{
			{Directive, ".macro"}, {Identifier, "MARK32"}, {Identifier, "ch"}, {Punct, ","},
			{Identifier, "n"}, {Punct, "="}, {Number, "1"},
		}},
		{"endm", ".endm", []tk{{Directive, ".endm"}}},
		{"st register", "fld %st(1)", []tk{{Mnemonic, "fld"}, {Register, "%st(1)"}}},
		{"st bare", "fld %st", []tk{{Mnemonic, "fld"}, {Register, "%st"}}},
		{"control register", "movl %cr0, %eax", []tk{
			{Mnemonic, "movl"}, {Register, "%cr0"}, {Punct, ","}, {Register, "%eax"},
		}},
		{"percent operator", ".if x % 2", []tk{
			{Directive, ".if"}, {LabelRef, "x"}, {Punct, "%"}, {Number, "2"},
		}},
		{"section", `.section .text.stage2, "ax"`, []tk{
			{Directive, ".section"}, {Identifier, ".text.stage2"}, {Punct, ","}, {String, `"ax"`},
		}},
		{"section type", `.section .note, "a", @progbits`, []tk{
			{Directive, ".section"}, {Identifier, ".note"}, {Punct, ","}, {String, `"a"`},
			{Punct, ","}, {Identifier, "@progbits"},
		}},
		{"directive case", `.SECTION .data`, []tk{{Directive, ".SECTION"}, {Identifier, ".data"}}},
		{"type directive", ".type foo, @function", []tk{
			{Directive, ".type"}, {LabelRef, "foo"}, {Punct, ","}, {Identifier, "@function"},
		}},
		{"lone at", "x @", []tk{{Mnemonic, "x"}, {Punct, "@"}}},
		{"plt", "call foo@PLT", []tk{{Mnemonic, "call"}, {LabelRef, "foo"}, {Identifier, "@PLT"}}},
		{"data directives", ".byte 0xEB, 0x58\n.word gdt_end - gdt - 1\n.long 0\n.quad 0x00CF9A000000FFFF", []tk{
			{Directive, ".byte"}, {Number, "0xEB"}, {Punct, ","}, {Number, "0x58"},
			{Directive, ".word"}, {LabelRef, "gdt_end"}, {Punct, "-"}, {LabelRef, "gdt"}, {Punct, "-"}, {Number, "1"},
			{Directive, ".long"}, {Number, "0"},
			{Directive, ".quad"}, {Number, "0x00CF9A000000FFFF"},
		}},
		{"local symbol arg", ".long .Lfoo, .", []tk{
			{Directive, ".long"}, {LabelRef, ".Lfoo"}, {Punct, ","}, {Identifier, "."},
		}},
		{"strings", `.asciz "a\"b\r\n"` + "\n" + `.ascii "FAT12   "`, []tk{
			{Directive, ".asciz"}, {String, `"a\"b\r\n"`}, {Directive, ".ascii"}, {String, `"FAT12   "`},
		}},
		{"unterminated string", ".ascii \"abc  \r\nnop", []tk{
			{Directive, ".ascii"}, {String, `"abc`}, {Mnemonic, "nop"},
		}},
		{"string trailing backslash", ".ascii \"a\\", []tk{{Directive, ".ascii"}, {String, `"a\`}}},
		{"equ set", ".equ FOO, 5\n.set vec, vec + 1\n.equiv X, 1\n.eqv Y, 2", []tk{
			{Directive, ".equ"}, {LabelDef, "FOO"}, {Punct, ","}, {Number, "5"},
			{Directive, ".set"}, {LabelDef, "vec"}, {Punct, ","}, {LabelRef, "vec"}, {Punct, "+"}, {Number, "1"},
			{Directive, ".equiv"}, {LabelDef, "X"}, {Punct, ","}, {Number, "1"},
			{Directive, ".eqv"}, {LabelDef, "Y"}, {Punct, ","}, {Number, "2"},
		}},
		{"set macro arg", `.set \n, 1`, []tk{{Directive, ".set"}, {Identifier, `\n`}, {Punct, ","}, {Number, "1"}}},
		{"assignment", "FOO = 0x10 + BAR", []tk{
			{LabelDef, "FOO"}, {Punct, "="}, {Number, "0x10"}, {Punct, "+"}, {LabelRef, "BAR"},
		}},
		{"location counter", ". = _start + 510", []tk{
			{Identifier, "."}, {Punct, "="}, {LabelRef, "_start"}, {Punct, "+"}, {Number, "510"},
		}},
		{"macro arg assignment", `\x = 1`, []tk{{Identifier, `\x`}, {Punct, "="}, {Number, "1"}}},
		{"equality is not assignment", "x == 1", []tk{{Mnemonic, "x"}, {Punct, "=="}, {Number, "1"}}},
		{"code16", ".code16", []tk{{Directive, ".code16"}}},
		{"separator", "cli; hlt ; jmp 1b", []tk{
			{Mnemonic, "cli"}, {Punct, ";"}, {Mnemonic, "hlt"}, {Punct, ";"},
			{Mnemonic, "jmp"}, {LocalLabelRef, "1b"},
		}},
		{"rep prefix", "rep movsw", []tk{{Prefix, "rep"}, {Mnemonic, "movsw"}}},
		{"prefix alone then sep", "rep; movsb", []tk{{Prefix, "rep"}, {Punct, ";"}, {Mnemonic, "movsb"}}},
		{"stacked prefixes", "lock addr32 data16 incl (%eax)", []tk{
			{Prefix, "lock"}, {Prefix, "addr32"}, {Prefix, "data16"}, {Mnemonic, "incl"},
			{Punct, "("}, {Register, "%eax"}, {Punct, ")"},
		}},
		{"addr32 loop", "addr32 loop 2b", []tk{{Prefix, "addr32"}, {Mnemonic, "loop"}, {LocalLabelRef, "2b"}}},
		{"segment prefix", "es movsb", []tk{{Prefix, "es"}, {Mnemonic, "movsb"}}},
		{"segment alone", "es", []tk{{Mnemonic, "es"}}},
		{"segment before sep", "fs; nop", []tk{{Mnemonic, "fs"}, {Punct, ";"}, {Mnemonic, "nop"}}},
		{"pseudo prefix", "{vex} vpaddd %xmm1, %xmm2, %xmm3", []tk{
			{Prefix, "{vex}"}, {Mnemonic, "vpaddd"}, {Register, "%xmm1"}, {Punct, ","},
			{Register, "%xmm2"}, {Punct, ","}, {Register, "%xmm3"},
		}},
		{"open brace not prefix", "{ x", []tk{{Punct, "{"}, {LabelRef, "x"}}},
		{"avx512 mask", "vaddps %zmm1, %zmm2, %zmm3{%k1}{z}", []tk{
			{Mnemonic, "vaddps"}, {Register, "%zmm1"}, {Punct, ","}, {Register, "%zmm2"}, {Punct, ","},
			{Register, "%zmm3"}, {Punct, "{"}, {Register, "%k1"}, {Punct, "}"},
			{Punct, "{"}, {LabelRef, "z"}, {Punct, "}"},
		}},
		{"hash comment", "nop # trailing", []tk{{Mnemonic, "nop"}, {Comment, "# trailing"}}},
		{"hash comment line", "# just text\r\nnop", []tk{{Comment, "# just text"}, {Mnemonic, "nop"}}},
		{"slash comment", "nop // c\nhlt", []tk{{Mnemonic, "nop"}, {Comment, "// c"}, {Mnemonic, "hlt"}}},
		{"block comment inline", "movl %eax, /* x */ %ebx", []tk{
			{Mnemonic, "movl"}, {Register, "%eax"}, {Punct, ","}, {Comment, "/* x */"}, {Register, "%ebx"},
		}},
		{"block comment multiline", "/* a\n *\n * b */\nnop", []tk{
			{Comment, "/* a"}, {Comment, "*"}, {Comment, "* b */"}, {Mnemonic, "nop"},
		}},
		{"block comment blank line", "/* a\n\n b */", []tk{{Comment, "/* a"}, {Comment, "b */"}}},
		{"block comment crlf", "/* a \r\n b */\r\n", []tk{{Comment, "/* a"}, {Comment, "b */"}}},
		{"block comment joins statement", "movl %eax, /* x\n y */ %ebx", []tk{
			{Mnemonic, "movl"}, {Register, "%eax"}, {Punct, ","}, {Comment, "/* x"},
			{Comment, "y */"}, {Register, "%ebx"},
		}},
		{"unterminated block", "/* a\n b  ", []tk{{Comment, "/* a"}, {Comment, "b"}}},
		{"include", "#include <start/biosdefs.h>", []tk{
			{CppDirective, "#include"}, {CppArg, "<start/biosdefs.h>"},
		}},
		{"include quoted", `#include "a//b.h" // why`, []tk{
			{CppDirective, "#include"}, {CppArg, `"a//b.h"`}, {Comment, "// why"},
		}},
		{"define", "#define FAULT_STUB 16", []tk{{CppDirective, "#define"}, {CppArg, "FAULT_STUB 16"}}},
		{"spaced directive", "  #  define X 1", []tk{{CppDirective, "#  define"}, {CppArg, "X 1"}}},
		{"define comment", "#define X 1 /* why */ + 2", []tk{
			{CppDirective, "#define"}, {CppArg, "X 1"}, {Comment, "/* why */"}, {CppArg, "+ 2"},
		}},
		{"define continuation", "#define M(a) \\\n\tmovl a, %eax ; \\\r\n\tret\nnop", []tk{
			{CppDirective, "#define"}, {CppArg, "M(a) \\"}, {CppArg, "movl a, %eax ; \\"},
			{CppArg, "ret"}, {Mnemonic, "nop"},
		}},
		{"define multiline comment", "#define X 1 /* a\n b */ 2\nnop", []tk{
			{CppDirective, "#define"}, {CppArg, "X 1"}, {Comment, "/* a"}, {Comment, "b */"},
			{CppArg, "2"}, {Mnemonic, "nop"},
		}},
		{"if endif", "#if X\n#ifdef Y\n#else\n#endif\n#endif /* X */", []tk{
			{CppDirective, "#if"}, {CppArg, "X"}, {CppDirective, "#ifdef"}, {CppArg, "Y"},
			{CppDirective, "#else"}, {CppDirective, "#endif"},
			{CppDirective, "#endif"}, {Comment, "/* X */"},
		}},
		{"error apostrophe", "#error don't /* c */", []tk{
			{CppDirective, "#error"}, {CppArg, "don't"}, {Comment, "/* c */"},
		}},
		{"not a directive", "#defined x", []tk{{Comment, "#defined x"}}},
		{"hash alone", "#", []tk{{Comment, "#"}}},
		{"hash number", "# 1 \"file.S\"", []tk{{Comment, "# 1 \"file.S\""}}},
		{"hash after code", "nop #define x", []tk{{Mnemonic, "nop"}, {Comment, "#define x"}}},
		{"directive at eof", "#endif", []tk{{CppDirective, "#endif"}}},
		{"tabs", "\t\tmovl\t%eax,\t%ebx", []tk{
			{Mnemonic, "movl"}, {Register, "%eax"}, {Punct, ","}, {Register, "%ebx"},
		}},
		{"non ascii skipped", "nop \u00e9", []tk{{Mnemonic, "nop"}}},
		{"non ascii char literal", ".byte '\u00e9'", []tk{{Directive, ".byte"}, {Number, "'\u00e9'"}}},
		{"quote at eol", "x '\r\ny '", []tk{{Mnemonic, "x"}, {Number, "'"}, {Mnemonic, "y"}, {Number, "'"}}},
		{"quote before newline", "'\nnop", []tk{{Number, "'"}, {Mnemonic, "nop"}}},
		{"lone dollar", "push $", []tk{{Mnemonic, "push"}, {Immediate, "$"}}},
		{"lone percent", "%", []tk{{Punct, "%"}}},
		{"number at start", "5 x", []tk{{Number, "5"}, {LabelRef, "x"}}},
		{"float", ".float 1.5", []tk{{Directive, ".float"}, {Number, "1.5"}}},
		{"punct at start", "(x)", []tk{{Punct, "("}, {LabelRef, "x"}, {Punct, ")"}}},
		{"hex before colon", "0x10: x", []tk{{Number, "0x10"}, {Punct, ":"}, {LabelRef, "x"}}},
		{"slash at eof", "x /", []tk{{Mnemonic, "x"}, {Punct, "/"}}},
		{"define escaped string", `#define S "a\"b//" // c`, []tk{
			{CppDirective, "#define"}, {CppArg, `S "a\"b//"`}, {Comment, "// c"},
		}},
		{"empty", "", []tk{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := Tokenize(tt.src)
			checkInvariants(t, tt.src, toks)
			if got := compact(toks); !equal(got, tt.want) {
				t.Errorf("Tokenize(%q)\n got  %v\n want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestPositions(t *testing.T) {
	src := "/* x\r\n */\r\n\tmovl\t%eax, %ebx\r\n"
	toks := Tokenize(src)
	want := []Token{
		{Comment, 0, 0, 4, "/* x"},
		{Comment, 1, 1, 2, "*/"},
		{Mnemonic, 2, 1, 4, "movl"},
		{Register, 2, 6, 4, "%eax"},
		{Punct, 2, 10, 1, ","},
		{Register, 2, 12, 4, "%ebx"},
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens %v, want %d", len(toks), toks, len(want))
	}
	for i := range want {
		if toks[i] != want[i] {
			t.Errorf("token %d: got %+v want %+v", i, toks[i], want[i])
		}
	}
}

func TestKindString(t *testing.T) {
	tests := []struct {
		k    Kind
		want string
	}{
		{Mnemonic, "Mnemonic"}, {LocalLabelRef, "LocalLabelRef"}, {Punct, "Punct"},
		{Kind(-1), "Kind(?)"}, {Kind(99), "Kind(?)"},
	}
	for _, tt := range tests {
		if got := tt.k.String(); got != tt.want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(tt.k), got, tt.want)
		}
	}
}

// lineStarts returns the byte offset each line starts at.
func lineStarts(src string) []int {
	starts := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// checkInvariants enforces what every consumer relies on: positions that
// round-trip to the source, single-line tokens, and strict source order.
func checkInvariants(t testing.TB, src string, toks []Token) {
	t.Helper()
	starts := lineStarts(src)
	prevEnd := -1
	for i, tok := range toks {
		if tok.Len <= 0 {
			t.Fatalf("token %d %+v: non-positive length", i, tok)
		}
		if tok.Line < 0 || tok.Line >= len(starts) {
			t.Fatalf("token %d %+v: line out of range", i, tok)
		}
		off := starts[tok.Line] + tok.Col
		end := off + tok.Len
		if tok.Col < 0 || end > len(src) {
			t.Fatalf("token %d %+v: span out of range", i, tok)
		}
		if src[off:end] != tok.Text {
			t.Fatalf("token %d %+v: text does not match source %q", i, tok, src[off:end])
		}
		if strings.ContainsRune(tok.Text, '\n') {
			t.Fatalf("token %d %+v: spans a newline", i, tok)
		}
		if off < prevEnd {
			t.Fatalf("token %d %+v: overlaps or precedes the previous token", i, tok)
		}
		prevEnd = end
	}
}

func fixtures(t testing.TB) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "falcon-bios", "*.S"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found")
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = string(b)
	}
	return out
}

// isCppDirectiveLine mirrors the DESIGN.md rule independently of the lexer
// so the fixture test is not the lexer checking itself.
func isCppDirectiveLine(line string) bool {
	s := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(s, "#") {
		return false
	}
	s = strings.TrimLeft(s[1:], " \t")
	for _, w := range []string{"include", "define", "undef", "ifdef", "ifndef", "if", "elif", "else", "endif", "error", "warning", "pragma", "line"} {
		if strings.HasPrefix(s, w) {
			rest := s[len(w):]
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\r' || rest[0] == '<' || rest[0] == '"' {
				return true
			}
		}
	}
	return false
}

func TestFixtures(t *testing.T) {
	for name, src := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			toks := Tokenize(src)
			checkInvariants(t, src, toks)

			cppLines := map[int]bool{}
			mnemonics := 0
			for _, tok := range toks {
				switch tok.Kind {
				case CppDirective:
					cppLines[tok.Line] = true
				case Mnemonic:
					mnemonics++
				}
			}
			for i, line := range strings.Split(src, "\n") {
				if isCppDirectiveLine(line) && !cppLines[i] {
					t.Errorf("line %d %q: no CppDirective", i+1, line)
				}
			}
			if mnemonics == 0 {
				t.Error("no mnemonics found")
			}
		})
	}
}

// lineTokens returns the non-comment tokens on a one-based line of src.
func lineTokens(toks []Token, line int) []tk {
	var out []tk
	for _, tok := range toks {
		if tok.Line == line-1 && tok.Kind != Comment {
			out = append(out, tk{tok.Kind, tok.Text})
		}
	}
	return out
}

func TestFixtureLines(t *testing.T) {
	fx := fixtures(t)
	tests := []struct {
		file string
		line int // one based, as an editor shows it
		want []tk
	}{
		{"pmode.S", 19, []tk{{CppDirective, "#include"}, {CppArg, "<start/biosdefs.h>"}}},
		{"pmode.S", 21, []tk{{Directive, ".code16"}}},
		{"pmode.S", 22, []tk{{Directive, ".section"}, {Identifier, ".text.stage2"}, {Punct, ","}, {String, `"ax"`}}},
		{"pmode.S", 28, []tk{{LabelDef, "pmode_enter"}, {Punct, ":"}}},
		{"pmode.S", 39, []tk{{Mnemonic, "movw"}, {Immediate, "$0x2401"}, {Punct, ","}, {Register, "%ax"}}},
		{"pmode.S", 51, []tk{{Mnemonic, "jnz"}, {LocalLabelRef, "1f"}}},
		{"pmode.S", 54, []tk{{Mnemonic, "outb"}, {Register, "%al"}, {Punct, ","}, {Immediate, "$0x92"}}},
		{"pmode.S", 55, []tk{{LocalLabelDef, "1"}, {Punct, ":"}}},
		{"pmode.S", 66, []tk{
			{Mnemonic, "movw"}, {Immediate, "$0x5678"}, {Punct, ","}, {Register, "%es"}, {Punct, ":"},
			{Punct, "("}, {Number, "0x0510"}, {Punct, ")"},
		}},
		{"pmode.S", 107, []tk{{Mnemonic, "ljmpl"}, {Immediate, "$SEL_CODE32"}, {Punct, ","}, {Immediate, "$pmode_32"}}},
		{"pmode.S", 123, []tk{{Mnemonic, "jmp"}, {Punct, "*"}, {LabelRef, "pmode_entry"}}},
		{"pmode.S", 152, []tk{{Directive, ".asciz"}, {String, `"falcon-bios: A20 will not open; this machine cannot be booted\r\n"`}}},
		{"idt.S", 30, []tk{{CppDirective, "#define"}, {CppArg, "FAULT_STUB 16"}}},
		{"idt.S", 63, []tk{{Directive, ".set"}, {LabelDef, "vec"}, {Punct, ","}, {Number, "0"}}},
		{"thunk.S", 40, []tk{{Directive, ".macro"}, {Identifier, "MARK32"}, {Identifier, "ch"}}},
		{"thunk.S", 45, []tk{{LocalLabelDef, "9903"}, {Punct, ":"}, {Mnemonic, "inb"}, {Register, "%dx"}, {Punct, ","}, {Register, "%al"}}},
		{"thunk.S", 49, []tk{{Mnemonic, "movb"}, {Immediate, `$\ch`}, {Punct, ","}, {Register, "%al"}}},
		{"stage2.S", 305, []tk{{Prefix, "addr32"}, {Mnemonic, "loop"}, {LocalLabelRef, "2b"}}},
		{"nobar.S", 34, []tk{{Prefix, "rep"}, {Mnemonic, "movsw"}}},
		{"boot.S", 286, []tk{{Identifier, "."}, {Punct, "="}, {LabelRef, "_start"}, {Punct, "+"}, {Number, "510"}}},
		{"noe820.S", 99, []tk{
			{Mnemonic, "ljmp"}, {Punct, "*"}, {Register, "%cs"}, {Punct, ":"}, {Punct, "("}, {LabelRef, "RELOC"},
			{Punct, "+"}, {Punct, "("}, {LabelRef, "old15"}, {Punct, "-"}, {LabelRef, "_start"}, {Punct, ")"}, {Punct, ")"},
		}},
		{"memprobe.S", 165, []tk{{LabelDef, "msg_int12"}, {Punct, ":"}, {Directive, ".asciz"}, {String, `"MEMPROBE INT12 "`}}},
	}
	cache := map[string][]Token{}
	for _, tt := range tests {
		toks, ok := cache[tt.file]
		if !ok {
			src, found := fx[tt.file]
			if !found {
				t.Fatalf("fixture %s missing", tt.file)
			}
			toks = Tokenize(src)
			cache[tt.file] = toks
		}
		if got := lineTokens(toks, tt.line); !equal(got, tt.want) {
			t.Errorf("%s:%d\n got  %v\n want %v", tt.file, tt.line, got, tt.want)
		}
	}
}

// TestPmodeComments checks a multi-line block comment yields one Comment per
// line and nothing else, using the file header as a known span.
func TestPmodeComments(t *testing.T) {
	src := fixtures(t)["pmode.S"]
	toks := Tokenize(src)
	for line := 0; line < 17; line++ {
		var kinds []Kind
		for _, tok := range toks {
			if tok.Line == line {
				kinds = append(kinds, tok.Kind)
			}
		}
		if len(kinds) != 1 || kinds[0] != Comment {
			t.Errorf("line %d: kinds %v, want one Comment", line+1, kinds)
		}
	}
}

func TestCRLFMatchesLF(t *testing.T) {
	for name, src := range fixtures(t) {
		lf := compact(Tokenize(src))
		crlfSrc := strings.ReplaceAll(src, "\n", "\r\n")
		crlfToks := Tokenize(crlfSrc)
		checkInvariants(t, crlfSrc, crlfToks)
		if !equal(lf, compact(crlfToks)) {
			t.Errorf("%s: CRLF tokens differ from LF tokens", name)
		}
	}
}

func FuzzTokenize(f *testing.F) {
	for _, src := range fixtures(f) {
		f.Add(src)
	}
	for _, s := range []string{
		"", "#", "#define X \\\n", "/*", "\"", "'", "'\\", "$", "%st(", "1:", "a: b = c",
		"{vex", "\\", "@", "#include <x> /* a\n b */ c\n", "x '\r", ".macro m a\n\\a\n.endm",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkInvariants(t, src, Tokenize(src))
	})
}

func BenchmarkTokenizeFixtures(b *testing.B) {
	fx := fixtures(b)
	total := 0
	for _, src := range fx {
		total += len(src)
	}
	b.SetBytes(int64(total))
	b.ResetTimer()
	for b.Loop() {
		for _, src := range fx {
			Tokenize(src)
		}
	}
}

// BenchmarkTokenizeLarge lexes a 20k-line document, the size where the
// token slice is large enough for its allocation to matter.
func BenchmarkTokenizeLarge(b *testing.B) {
	src := benchdata.Large(b, 20000)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		Tokenize(src)
	}
}
