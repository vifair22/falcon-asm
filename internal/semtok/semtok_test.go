package semtok

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/server"
)

// tok is one decoded semantic token in absolute coordinates, the form a
// human can check against the source.
type tok struct {
	line, col, n int
	typ, mods    uint32
}

func decode(t *testing.T, data []uint32) []tok {
	t.Helper()
	if len(data)%5 != 0 {
		t.Fatalf("stream length %d is not a multiple of 5", len(data))
	}
	var out []tok
	line, col := 0, 0
	for i := 0; i < len(data); i += 5 {
		if data[i] != 0 {
			line += int(data[i])
			col = 0
		}
		col += int(data[i+1])
		out = append(out, tok{line, col, int(data[i+2]), data[i+3], data[i+4]})
	}
	return out
}

func encode(t *testing.T, src string) []uint32 {
	t.Helper()
	data, err := Encode(&docstore.Document{Text: src})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return data
}

// checkWellFormed asserts the invariants a client relies on: tokens in
// order, non-empty, non-overlapping, inside their line, with a legend type.
func checkWellFormed(t *testing.T, name, src string, data []uint32) {
	t.Helper()
	doc := &docstore.Document{Text: src}
	toks := decode(t, data)
	prevLine, prevEnd := -1, 0
	for i, k := range toks {
		if k.n <= 0 {
			t.Errorf("%s: token %d %+v is empty", name, i, k)
		}
		if k.typ >= uint32(len(server.TokenTypes)) {
			t.Errorf("%s: token %d %+v has type outside the legend", name, i, k)
		}
		if k.mods >= 1<<len(server.TokenModifiers) {
			t.Errorf("%s: token %d %+v has unknown modifier bits", name, i, k)
		}
		if k.line < prevLine || (k.line == prevLine && k.col < prevEnd) {
			t.Errorf("%s: token %d %+v overlaps or precedes the previous one", name, i, k)
		}
		text, ok := doc.Line(k.line)
		if !ok {
			t.Errorf("%s: token %d %+v is past the last line", name, i, k)
			continue
		}
		if w := docstore.UTF16Len(text); k.col+k.n > w {
			t.Errorf("%s: token %d %+v runs past line end %d", name, i, k, w)
		}
		prevLine, prevEnd = k.line, k.col+k.n
	}
}

const (
	kw   = server.TokenKeyword
	mod  = server.TokenModifier
	vr   = server.TokenVariable
	num  = server.TokenNumber
	lbl  = server.TokenLabel
	mac  = server.TokenMacro
	cmt  = server.TokenComment
	str  = server.TokenString
	op   = server.TokenOperator
	typ  = server.TokenType
	prm  = server.TokenParameter
	decl = server.ModDeclaration
	priv = server.ModDefaultLibrary
)

func TestEncode(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []tok
	}{
		{"empty", "", nil},
		{"suffix split", "\tpushfl\n", []tok{{0, 1, 5, kw, 0}, {0, 6, 1, op, 0}}},
		{"suffix split upper case", "\tPUSHFL\n", []tok{{0, 1, 5, kw, 0}, {0, 6, 1, op, 0}}},
		{"privileged suffix split", "\tlgdtl gdt_desc\n", []tok{
			{0, 1, 4, kw, priv}, {0, 5, 1, op, 0}, {0, 7, 8, lbl, 0},
		}},
		{"alias resolves whole", "\tinb $0x60, %al\n", []tok{
			{0, 1, 3, kw, 0}, {0, 5, 5, prm, 0}, {0, 12, 3, vr, 0},
		}},
		{"string alias not split", "\trep movsb\n", []tok{{0, 1, 3, mod, 0}, {0, 5, 5, kw, 0}}},
		{"cli unprivileged", "\tcli\n", []tok{{0, 1, 3, kw, 0}}},
		{"hlt privileged", "\thlt\n", []tok{{0, 1, 3, kw, priv}}},
		{"lgdt privileged", "\tlgdt (%eax)\n", []tok{{0, 1, 4, kw, priv}, {0, 7, 4, vr, 0}}},
		{"unresolved mnemonic", "\tzzzq %eax\n", []tok{{0, 1, 4, kw, 0}, {0, 6, 4, vr, 0}}},
		{"registers known and unknown", "\toutl %cr0, %bogus\n", []tok{
			{0, 1, 4, kw, 0}, {0, 6, 4, vr, 0}, {0, 12, 6, vr, 0},
		}},
		{"labels", "start:\n1:\tjmp 1b\n\tjmp start\n", []tok{
			{0, 0, 5, lbl, decl},
			{1, 0, 1, lbl, decl}, {1, 3, 3, kw, 0}, {1, 7, 2, lbl, 0},
			{2, 1, 3, kw, 0}, {2, 5, 5, lbl, 0},
		}},
		{"set defines a label", ".set ADDR, 0x10\n", []tok{
			{0, 0, 4, typ, 0}, {0, 5, 4, lbl, decl}, {0, 11, 4, num, 0},
		}},
		{"cpp include", "#include <start/biosdefs.h>\n", []tok{
			{0, 0, 8, mac, 0}, {0, 9, 18, str, 0},
		}},
		{"cpp spaced include", "# include \"a.h\"\n", []tok{
			{0, 0, 9, mac, 0}, {0, 10, 5, str, 0},
		}},
		{"cpp if args skipped", "#if FOO > 1\n", []tok{{0, 0, 3, mac, 0}}},
		{"define then use", "#define FOO 0x10\n\toutl $FOO, %eax\n\toutl FOO, %eax\n.long FOO\n", []tok{
			{0, 0, 7, mac, 0},
			{1, 1, 4, kw, 0}, {1, 6, 1, prm, 0}, {1, 7, 3, mac, 0}, {1, 12, 4, vr, 0},
			{2, 1, 4, kw, 0}, {2, 6, 3, mac, 0}, {2, 11, 4, vr, 0},
			{3, 0, 5, typ, 0}, {3, 6, 3, mac, 0},
		}},
		{"use before define is a label", "\tjmp FOO\n#define FOO 1\n", []tok{
			{0, 1, 3, kw, 0}, {0, 5, 3, lbl, 0}, {1, 0, 7, mac, 0},
		}},
		{"undef forgets", "#define FOO 1\n#undef FOO\n\tjmp FOO\n", []tok{
			{0, 0, 7, mac, 0}, {1, 0, 6, mac, 0}, {2, 1, 3, kw, 0}, {2, 5, 3, lbl, 0},
		}},
		{"define with comment before name", "#define /* c */ SEC .text\n.section SEC\n.section .data\n", []tok{
			{0, 0, 7, mac, 0}, {0, 8, 7, cmt, 0},
			{1, 0, 8, typ, 0}, {1, 9, 3, mac, 0},
			{2, 0, 8, typ, 0},
		}},
		{"define continuation", "#define M(x) \\\n\tmovl x, %eax\n\tM(1)\n", []tok{
			{0, 0, 7, mac, 0}, {2, 1, 1, mac, 0}, {2, 3, 1, num, 0},
		}},
		{"multi-line block comment", "/* one\n   two\n */ cli\n", []tok{
			{0, 0, 6, cmt, 0}, {1, 3, 3, cmt, 0}, {2, 1, 2, cmt, 0}, {2, 4, 3, kw, 0},
		}},
		{"line comments", "\tcli # off\n\thlt // stop\n", []tok{
			{0, 1, 3, kw, 0}, {0, 5, 5, cmt, 0},
			{1, 1, 3, kw, priv}, {1, 5, 7, cmt, 0},
		}},
		{"string", "\t.ascii \"hi\\n\"\n", []tok{{0, 1, 6, typ, 0}, {0, 8, 6, str, 0}}},
		{"bmp char shifts columns", "/* \u00e9\u4e2d */ cli\n", []tok{
			{0, 0, 8, cmt, 0}, {0, 9, 3, kw, 0},
		}},
		{"astral char is a surrogate pair", "/* \U0001F600 */ cli\n", []tok{
			{0, 0, 8, cmt, 0}, {0, 9, 3, kw, 0},
		}},
		{"non-ascii in a string", "\t.ascii \"\u00e9\U0001F600\" # x\n", []tok{
			{0, 1, 6, typ, 0}, {0, 8, 5, str, 0}, {0, 14, 3, cmt, 0},
		}},
		{"crlf", "\tcli\r\n\thlt\r\n", []tok{{0, 1, 3, kw, 0}, {1, 1, 3, kw, priv}}},
		{"lone cr splits a token", "// a\rbc\n\tcli\n", []tok{
			{0, 0, 4, cmt, 0}, {1, 0, 2, cmt, 0}, {2, 1, 3, kw, 0},
		}},
		// The lexer reads a lone '\r' as a blank, so hlt is an operand of cli;
		// the client still sees two lines and gets positions on both.
		{"lone cr between tokens", "\tcli\r\thlt\n", []tok{{0, 1, 3, kw, 0}, {1, 1, 3, lbl, 0}}},
		{"punct and plain identifiers skipped", "\t.type f, @function\n", []tok{
			{0, 1, 5, typ, 0}, {0, 7, 1, lbl, 0},
		}},
		{"prefix and pseudo prefix", "\tlock {vex} cli\n", []tok{
			{0, 1, 4, mod, 0}, {0, 6, 5, mod, 0}, {0, 12, 3, kw, 0},
		}},
		{"no trailing newline", "\tcli", []tok{{0, 1, 3, kw, 0}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := encode(t, tc.src)
			checkWellFormed(t, tc.name, tc.src, data)
			got := decode(t, data)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d tokens %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("token %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// A character literal can end in the middle of a multibyte code point
// ('\ takes one byte after the backslash). The stream must stay well formed
// and the column after it must still be right.
func TestPartialCodePoint(t *testing.T) {
	src := "\t.byte '\\\u00e9, 1 # z\n"
	data := encode(t, src)
	checkWellFormed(t, "partial", src, data)
	got := decode(t, data)
	last := got[len(got)-1]
	// Tab, ".byte", space, quote, backslash, e-acute, comma, space, "1",
	// space: the comment starts at UTF-16 column 14.
	if last.typ != cmt || last.col != 14 || last.n != 3 {
		t.Fatalf("comment after partial code point: got %+v", last)
	}
}

func TestInvalidUTF8(t *testing.T) {
	src := "/* \xff\xfe */ cli\n"
	data := encode(t, src)
	checkWellFormed(t, "invalid", src, data)
	got := decode(t, data)
	want := []tok{{0, 0, 8, cmt, 0}, {0, 9, 3, kw, 0}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLeadingIdent(t *testing.T) {
	tests := map[string]string{
		"FOO 1": "FOO", "M(x) y": "M", "_a9 b": "_a9", "9x": "", "": "",
	}
	for in, want := range tests {
		if got := leadingIdent(in); got != want {
			t.Errorf("leadingIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func fixtures(tb testing.TB) map[string]string {
	tb.Helper()
	paths, err := filepath.Glob("../../testdata/falcon-bios/*.S")
	if err != nil || len(paths) == 0 {
		tb.Fatalf("no fixtures: %v", err)
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			tb.Fatal(err)
		}
		out[filepath.Base(p)] = string(b)
	}
	return out
}

func TestFixturesWellFormed(t *testing.T) {
	for name, src := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			data := encode(t, src)
			if len(data) == 0 {
				t.Fatal("no tokens")
			}
			checkWellFormed(t, name, src, data)
			// The same text with CRLF endings must paint identically: the
			// lexer trims '\r' and the walker treats CRLF as one break.
			crlf := encode(t, strings.ReplaceAll(src, "\n", "\r\n"))
			if len(crlf) != len(data) {
				t.Fatalf("CRLF stream has %d values, LF has %d", len(crlf), len(data))
			}
			for i := range data {
				if crlf[i] != data[i] {
					t.Fatalf("CRLF stream differs at %d", i)
				}
			}
		})
	}
}

// Every mnemonic in pmode.S is a real instruction, and its privileged ones
// carry the modifier; a regression in the isa wiring shows up here first.
func TestFixturePrivileged(t *testing.T) {
	src := fixtures(t)["pmode.S"]
	toks := decode(t, encode(t, src))
	privileged := 0
	for _, k := range toks {
		if k.typ == kw && k.mods&priv != 0 {
			privileged++
		}
	}
	if privileged == 0 {
		t.Fatal("pmode.S produced no privileged keyword (expected lgdt)")
	}
}

func BenchmarkEncodeFixtures(b *testing.B) {
	fx := fixtures(b)
	docs := make([]*docstore.Document, 0, len(fx))
	size := 0
	for _, src := range fx {
		docs = append(docs, &docstore.Document{Text: src})
		size += len(src)
	}
	b.SetBytes(int64(size))
	b.ReportAllocs()
	for b.Loop() {
		for _, d := range docs {
			if _, err := Encode(d); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// EncodeWith applies the toolchain's macro table and the mode diagnostics'
// positions on top of what the text alone gives.
func TestEncodeWith(t *testing.T) {
	src := "\tmovw $SEL, %ax\n\tljmp $SEL, $go\n\tSETUP 3\n\tpushq %rax\n"
	doc := &docstore.Document{Text: src}
	data := EncodeWith(doc, Options{
		Tokens:  lexer.Tokenize(src),
		IsMacro: func(n string) bool { return n == "SEL" || n == "SETUP" },
		Deprecated: map[protocol.Position]bool{
			{Line: 3, Character: 1}: true, // pushq: only the base piece is marked
			{Line: 3, Character: 7}: true, // %rax
		},
	})
	checkWellFormed(t, "with", src, data)
	got := decode(t, data)
	dep := server.ModDeprecated
	want := []tok{
		{0, 1, 3, kw, 0}, {0, 4, 1, op, 0}, {0, 6, 1, prm, 0}, {0, 7, 3, mac, 0}, {0, 12, 3, vr, 0},
		{1, 1, 4, kw, 0}, {1, 6, 1, prm, 0}, {1, 7, 3, mac, 0}, {1, 12, 3, prm, 0},
		{2, 1, 5, mac, 0}, {2, 7, 1, num, 0},
		{3, 1, 4, kw, dep}, {3, 5, 1, op, 0}, {3, 7, 4, vr, dep},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("token %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	plain, _ := Encode(doc)
	if same := EncodeWith(doc, Options{}); len(same) != len(plain) {
		t.Fatalf("zero Options differs from Encode")
	}
}
