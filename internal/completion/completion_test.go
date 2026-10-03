package completion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// at builds a document from src with the cursor at the last '|'. '|' is
// also the GAS or-operator, and the falcon-bios fixtures use it, so the
// marker is always the final one, in the text appended after a fixture.
func at(t testing.TB, src string) (*docstore.Document, protocol.Position) {
	t.Helper()
	i := strings.LastIndex(src, "|")
	if i < 0 {
		t.Fatalf("no cursor marker in %q", src)
	}
	store := docstore.New()
	store.Open("file:///t.S", 1, src[:i]+src[i+1:])
	doc, _ := store.Get("file:///t.S")
	return doc, doc.Position(i)
}

func labels(l *protocol.CompletionList) []string {
	if l == nil {
		return nil
	}
	out := make([]string, len(l.Items))
	for i, it := range l.Items {
		out[i] = it.Label
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func symbolCtx(mode int) Context {
	return Context{
		Mode:   mode,
		Labels: func() []string { return []string{"foo", "foobar", "bar", ".Lloop", "", "FOO"} },
		Macros: func() []string { return []string{"FOO", "FLAG_PE", "CR0_PG"} },
	}
}

func TestComplete(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		mode    int
		nilList bool
		want    []string // must all be present
		absent  []string // must all be missing
		first   string   // expected first label, when set
		allPfx  string   // every label must start with this, when set
	}{
		{name: "register after sigil", src: "\tmovw $1, %e|", want: []string{"%eax", "%esp", "%es"}, allPfx: "%e"},
		{name: "register in memory operand", src: "\tmovl (%e|", want: []string{"%ebx"}},
		{name: "register bare sigil", src: "\tmovl %|", want: []string{"%eax", "%al"}},
		{name: "register uppercase", src: "\tmovl %E|", want: []string{"%eax"}},
		{name: "no 64-bit registers in 16-bit mode", src: "\tmovw %r|", mode: 16, absent: []string{"%rax", "%r8", "%r8d"}},
		{name: "no sil in 32-bit mode", src: "\tmovb %s|", mode: 32, want: []string{"%si", "%sp", "%ss"}, absent: []string{"%sil", "%spl"}},
		{name: "64-bit registers in default mode", src: "\tmovq %r|", want: []string{"%rax", "%r8", "%r15d", "%rip"}},
		{name: "gpr before vector", src: "\t%|", first: "%ah"},

		{name: "mov first for mo", src: "\tmo|", first: "mov", want: []string{"movs", "movzbl", "movaps"}, allPfx: "mo"},
		{name: "jmp first for j", src: "j|", first: "jmp", want: []string{"je", "jrcxz"}},
		{name: "exact match first", src: "\tjz|", first: "jz"},
		{name: "exact symbol first", src: "\tjmp FOO|", first: "FOO"},
		{name: "mnemonic case-insensitive", src: "\tMO|", first: "mov"},
		{name: "suffixed forms after full base", src: "\tmov|", first: "mov", want: []string{"movb", "movw", "movl", "movq"}},
		{name: "no suffixed forms for short prefix", src: "\tm|", absent: []string{"movl", "mulb"}},
		{name: "no q suffix in 16-bit", src: "\tadd|", mode: 16, want: []string{"addl"}, absent: []string{"addq"}},
		{name: "no pushl in 64-bit", src: "\tpush|", want: []string{"pushq", "pushw"}, absent: []string{"pushl", "pusha"}},
		{name: "pusha in 32-bit", src: "\tpush|", mode: 32, want: []string{"pusha", "pushl"}, absent: []string{"pushq"}},
		{name: "mode-only instruction filtered", src: "\tswap|", mode: 32, absent: []string{"swapgs"}},
		{name: "prefixes offered", src: "\tre|", want: []string{"rep", "repne", "ret"}},
		{name: "rex prefix only in 64-bit", src: "\trex|", mode: 32, absent: []string{"rex", "rex64"}},
		{name: "after prefix", src: "\trep mo|", want: []string{"movsb"}},
		{name: "after bare segment word", src: "\tes |", want: []string{"lodsb"}},
		{name: "after label", src: "start: cl|", first: "cli"},
		{name: "after local label", src: "1:\tlg|", first: "lgdt"},
		{name: "second statement", src: "\tcli; st|", want: []string{"sti"}},
		{name: "after closed block comment", src: "/* x */ mo|", first: "mov"},

		{name: "directive at statement start", src: "\t.co|", want: []string{".code16", ".code32", ".code64", ".code16gcc"}, allPfx: ".co"},
		{name: "directive after label", src: "msg: .asc|", want: []string{".ascii", ".asciz"}},
		{name: "directive bare dot", src: ".|", want: []string{".globl", ".text"}},
		{name: "directive arg is a symbol", src: "\t.globl .L|", want: []string{".Lloop"}, absent: []string{".long"}},

		{name: "labels in operand", src: "\tjmp fo|", want: []string{"foo", "foobar", "FOO"}, absent: []string{"bar"}, first: "foo"},
		{name: "labels after empty operand", src: "\tcall |", want: []string{"foo", "bar", "CR0_PG"}},
		{name: "immediate symbol", src: "\tmovl $F|", want: []string{"FOO", "FLAG_PE", "foo"}, first: "FLAG_PE"},
		{name: "immediate bare", src: "\tmovl $|", want: []string{"bar"}},
		{name: "directive operand symbol", src: "\t.long fo|", want: []string{"foo"}},

		{name: "line comment hash", src: "\tmov %eax, %ebx # mo|", nilList: true},
		{name: "line comment slashes", src: "// %e|", nilList: true},
		{name: "line comment trailing space", src: "# mov |", nilList: true},
		{name: "open block comment", src: "\tmov /* %e|", nilList: true},
		{name: "open block comment slash-star-slash", src: "/*/ mo|", nilList: true},
		{name: "multi-line block comment", src: "/* first\n * mo|", nilList: true},
		{name: "blank line in block comment", src: "/* first\n\n|", nilList: true},
		{name: "hash line inside block comment", src: "/* first\n# second\n mo|", nilList: true},
		{name: "after multi-line block comment", src: "/* first\n# second */\n\tmo|", first: "mov"},
		{name: "after hash line after block", src: "/* a */\n# b\n\tmo|", first: "mov"},
		{name: "line comment after block on same line", src: "/* a */ # mo|", nilList: true},
		{name: "line comment after closed block line", src: "/* a */\n# b|", nilList: true},
		{name: "line comment after multi-line block", src: "/* a\n b */\n# c|", nilList: true},
		{name: "third line of block comment", src: "/* a\n b\n c|", nilList: true},
		{name: "right after closed string", src: "\t.ascii \"x\"|", want: []string{"foo"}},
		{name: "string", src: "\t.ascii \"mo|", nilList: true},
		{name: "string with escaped quote", src: "\t.ascii \"a\\\"b|", nilList: true},
		{name: "after closed string", src: "\t.ascii \"x\", fo|", want: []string{"foo"}},
		{name: "string trailing space", src: "\t.asciz \"x |", nilList: true},
		{name: "cpp include", src: "#include <st|", nilList: true},
		{name: "cpp define body", src: "#define X mo|", nilList: true},
		{name: "cpp continuation", src: "#define X \\\n\tmo|", nilList: true},
		{name: "number", src: "\tmovl $0x1|", nilList: true},
		{name: "local label", src: "1|", nilList: true},
		{name: "char constant", src: "\tmovb $'a|", nilList: true},
		{name: "macro parameter", src: "\tmovl \\ar|", nilList: true},
		{name: "no match is an empty list", src: "\tzzzz|", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, pos := at(t, tc.src)
			got := Complete(doc, pos, Context{Mode: tc.mode, Labels: symbolCtx(0).Labels, Macros: symbolCtx(0).Macros})
			if tc.nilList {
				if got != nil {
					t.Fatalf("want nil, got %v", labels(got))
				}
				return
			}
			if got == nil {
				t.Fatal("got nil list")
				return
			}
			if got.Items == nil {
				t.Error("Items is nil; want an empty slice so it marshals as []")
			}
			ls := labels(got)
			for _, w := range tc.want {
				if !contains(ls, w) {
					t.Errorf("missing %q in %v", w, head(ls))
				}
			}
			for _, a := range tc.absent {
				if contains(ls, a) {
					t.Errorf("unexpected %q", a)
				}
			}
			if tc.first != "" && (len(ls) == 0 || ls[0] != tc.first) {
				t.Errorf("first = %v, want %q", head(ls), tc.first)
			}
			for _, l := range ls {
				if tc.allPfx != "" && !strings.HasPrefix(l, tc.allPfx) {
					t.Errorf("%q does not start with %q", l, tc.allPfx)
				}
			}
		})
	}
}

func head(xs []string) []string {
	if len(xs) > 8 {
		return xs[:8]
	}
	return xs
}

// TestTextEdit checks the replaced range: it must cover the sigil for '%'
// and '.', stop short of '$', and be counted in UTF-16 units.
func TestTextEdit(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		label      string
		start, end int // characters on the cursor line
	}{
		{name: "register includes percent", src: "\tmovw $1, %e|", label: "%eax", start: 10, end: 12},
		{name: "directive includes dot", src: "  .co|", label: ".code16", start: 2, end: 5},
		{name: "immediate excludes dollar", src: "\tmovl $fo|", label: "foo", start: 7, end: 9},
		{name: "empty partial", src: "\tjmp |", label: "foo", start: 5, end: 5},
		{name: "after BMP non-ASCII", src: "/* \u00e9\u00e9 */ movl %e|", label: "%eax", start: 14, end: 16},
		{name: "after astral non-ASCII", src: "/* \U0001F600 */ mo|", label: "mov", start: 9, end: 11},
		{name: "second line", src: "\tcli\n\tmovl %e|", label: "%eax", start: 6, end: 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, pos := at(t, tc.src)
			got := Complete(doc, pos, symbolCtx(64))
			if got == nil {
				t.Fatal("nil list")
				return
			}
			var item *protocol.CompletionItem
			for i := range got.Items {
				if got.Items[i].Label == tc.label {
					item = &got.Items[i]
				}
			}
			if item == nil {
				t.Fatalf("no item %q in %v", tc.label, head(labels(got)))
				return
			}
			e := item.TextEdit
			want := protocol.Range{
				Start: protocol.Position{Line: pos.Line, Character: tc.start},
				End:   protocol.Position{Line: pos.Line, Character: tc.end},
			}
			if e == nil || e.Range != want || e.NewText != tc.label {
				t.Errorf("TextEdit = %+v, want %+v %q", e, want, tc.label)
			}
			if item.FilterText != tc.label {
				t.Errorf("FilterText = %q, want %q", item.FilterText, tc.label)
			}
			if item.SortText == "" {
				t.Error("SortText empty")
			}
		})
	}
}

func TestDetails(t *testing.T) {
	find := func(src, label string) protocol.CompletionItem {
		t.Helper()
		doc, pos := at(t, src)
		for _, it := range Complete(doc, pos, symbolCtx(64)).Items {
			if it.Label == label {
				return it
			}
		}
		t.Fatalf("no %q", label)
		return protocol.CompletionItem{}
	}
	tests := []struct {
		src, label, detail string
		kind               protocol.CompletionItemKind
	}{
		{"\tmovl %e|", "%eax", "gpr, 32-bit", protocol.CompletionKindVariable},
		{"\tmovl %c|", "%cr0", regDetail(t, "cr0"), protocol.CompletionKindVariable},
		{"\tlg|", "lgdt", mustSummary(t, "lgdt"), protocol.CompletionKindFunction},
		{"\tmovl|", "movl", mustSummary(t, "mov") + " (l = 32-bit)", protocol.CompletionKindFunction},
		{"\tlo|", "lock", prefixDetails["lock"], protocol.CompletionKindKeyword},
		{"\t.code1|", ".code16", mustDirective(t, ".code16"), protocol.CompletionKindKeyword},
		{"\tjmp fo|", "foo", "label", protocol.CompletionKindReference},
		{"\tjmp CR|", "CR0_PG", "macro", protocol.CompletionKindConstant},
		{"\tjmp FO|", "FOO", "macro", protocol.CompletionKindConstant}, // a label and a macro: macro wins
	}
	for _, tc := range tests {
		it := find(tc.src, tc.label)
		if it.Detail != tc.detail || it.Kind != tc.kind {
			t.Errorf("%s: detail %q kind %d, want %q %d", tc.label, it.Detail, it.Kind, tc.detail, tc.kind)
		}
	}
}

func mustSummary(t *testing.T, name string) string {
	t.Helper()
	inst, _, ok := isa.Lookup(name)
	if !ok {
		t.Fatalf("no instruction %q", name)
	}
	return inst.Summary
}

func regDetail(t *testing.T, name string) string {
	t.Helper()
	r, ok := isa.LookupRegister(name)
	if !ok {
		t.Fatalf("no register %q", name)
	}
	return fmt.Sprintf("%s, %d-bit", r.Class, r.Width)
}

func mustDirective(t *testing.T, name string) string {
	t.Helper()
	d, ok := isa.LookupDirective(name)
	if !ok {
		t.Fatalf("no directive %q", name)
	}
	return d.Summary
}

func TestCapAndIncomplete(t *testing.T) {
	doc, pos := at(t, "\t|")
	got := Complete(doc, pos, Context{})
	if len(got.Items) != MaxItems || !got.IsIncomplete {
		t.Fatalf("got %d items, incomplete=%v; want %d, true", len(got.Items), got.IsIncomplete, MaxItems)
	}
	if got.Items[0].Label != "mov" {
		t.Errorf("first = %q, want mov", got.Items[0].Label)
	}
	doc, pos = at(t, "\tlgd|")
	if got := Complete(doc, pos, Context{}); got.IsIncomplete {
		t.Error("short list marked incomplete")
	}
}

func TestNoSymbolSources(t *testing.T) {
	doc, pos := at(t, "\tjmp fo|")
	got := Complete(doc, pos, Context{})
	if got == nil || len(got.Items) != 0 {
		t.Fatalf("got %v, want empty list", got)
	}
}

func TestInvalidInput(t *testing.T) {
	if Complete(nil, protocol.Position{}, Context{}) != nil {
		t.Error("nil doc")
	}
	doc, _ := at(t, "mo|")
	if Complete(doc, protocol.Position{Line: -1}, Context{}) != nil {
		t.Error("negative position")
	}
	// A position past the line end clamps to it, as the LSP spec says.
	if got := Complete(doc, protocol.Position{Line: 0, Character: 99}, Context{}); got == nil || got.Items[0].Label != "mov" {
		t.Errorf("clamped position: %v", labels(got))
	}
}

// TestCommonMnemonicsResolve keeps the boost list honest: a name that does
// not resolve would never be offered, so its rank would be dead weight.
func TestCommonMnemonicsResolve(t *testing.T) {
	for _, m := range commonMnemonics {
		if _, suffix, ok := isa.Lookup(m); !ok || suffix != "" {
			t.Errorf("common mnemonic %q does not resolve exactly", m)
		}
	}
	for base := range suffixed {
		if !contains(commonMnemonics, base) {
			t.Errorf("suffixed base %q is not in commonMnemonics, so it would never be generated", base)
		}
	}
}

func TestHelpers(t *testing.T) {
	if !validInMode("", 16) || !validInMode("16, 32", 32) || validInMode("64", 16) {
		t.Error("validInMode")
	}
	if !naturalLess("r8", "r10") || naturalLess("xmm10", "xmm2") || !naturalLess("ah", "al") {
		t.Error("naturalLess")
	}
	if classRank("nonsense") != len(classOrder) {
		t.Error("classRank fallback")
	}
	if !closesBlock("x */") || closesBlock("/*/") || !closesBlock("/**/") {
		t.Error("closesBlock")
	}
}

// largeFixture concatenates the falcon-bios sources several times over:
// about 11k lines with block comments throughout, so the cursor line can
// only be classified by lexing the whole prefix (the slow path).
func largeFixture(b testing.TB) string {
	files, err := filepath.Glob("../../testdata/falcon-bios/*.S")
	if err != nil || len(files) == 0 {
		b.Fatalf("no fixtures: %v", err)
	}
	var sb strings.Builder
	for range 4 {
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				b.Fatal(err)
			}
			sb.Write(data)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func TestLargeFixture(t *testing.T) {
	src := largeFixture(t)
	for _, tail := range []string{"\tmovl %e|", "\tmo|", "\t.co|"} {
		doc, pos := at(t, src+tail)
		if got := Complete(doc, pos, Context{Mode: 16}); got == nil || len(got.Items) == 0 {
			t.Errorf("%q at the end of the fixture: %v", tail, labels(got))
		}
	}
}

func BenchmarkComplete(b *testing.B) {
	src := largeFixture(b)
	cases := []struct{ name, tail string }{
		{"register", "\tmovl %e|"},
		{"mnemonic", "\tmo|"},
		{"mnemonic-empty", "\t|"},
		{"symbol", "\tjmp fo|"},
	}
	ctx := symbolCtx(64)
	for _, c := range cases {
		doc, pos := at(b, src+c.tail)
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				Complete(doc, pos, ctx)
			}
		})
	}
	doc, pos := at(b, "\tcli\n\tmovl %e|")
	b.Run("single-line-fast-path", func(b *testing.B) {
		for b.Loop() {
			Complete(doc, pos, ctx)
		}
	})
}
