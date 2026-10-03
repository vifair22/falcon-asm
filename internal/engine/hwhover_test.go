package engine

import (
	"fmt"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

func posOf(t *testing.T, text, sub string, delta int) protocol.Position {
	t.Helper()
	i := strings.Index(text, sub)
	if i < 0 {
		t.Fatalf("%q not found", sub)
	}
	i += delta
	line := strings.Count(text[:i], "\n")
	return protocol.Position{Line: line, Character: i - strings.LastIndex(text[:i], "\n") - 1}
}

// Hardware hovers ride on the ordinary hover: a decoded descriptor, the
// BIOS service an int calls, a port constant.
func TestHardwareHover(t *testing.T) {
	h := newHarness(t, setup{})
	src := "\t.code16\n\tmovb $0x0E, %ah\n\tint $0x10\n\tmovw $0x3F8, %dx\n\toutb %al, %dx\n\tlgdt gdt_desc\n" +
		"gdt:\t.quad 0\n\t.quad 0x00CF9A000000FFFF\ngdt_end:\ngdt_desc:\t.word gdt_end - gdt - 1\n\t.long gdt\n"
	_, uri := h.file("hw.S", src)
	doc := h.open(uri, "falcon-asm", 1, src)
	cases := []struct {
		at, want, base string
	}{
		{"int $0x10", "INT 10h AH=0Eh", "nterrupt"},
		{"$0x3F8", "COM1", "1016"},
		{"0x00CF9A000000FFFF", "code32", ""},
	}
	for _, c := range cases {
		hv, err := h.e.Hover(doc, posOf(t, src, c.at, 1))
		if err != nil || hv == nil || !strings.Contains(hv.Contents.Value, c.want) || !strings.Contains(hv.Contents.Value, c.base) {
			t.Errorf("hover at %q = %+v %v", c.at, hv, err)
		}
	}
	// The index is built once per version.
	s := h.e.snapshotFor(doc)
	if first := s.hwIndex(); first != s.hwIndex() {
		t.Fatal("index rebuilt")
	}
	// Somewhere with nothing hardware about it: the ordinary hover alone.
	hv, _ := h.e.Hover(doc, posOf(t, src, "lgdt", 1))
	if hv == nil || strings.Contains(hv.Contents.Value, "---") {
		t.Fatalf("lgdt hover = %+v", hv)
	}
	if hv, err := h.e.Hover(doc, protocol.Position{Line: 99}); hv != nil || err == nil {
		// The ordinary hover reports the bad position; nothing is added.
		if hv != nil {
			t.Fatalf("out of range = %+v", hv)
		}
	}
}

// C helper calls get the hardware hover; the rest of C stays clangd's.
func TestCHelperHover(t *testing.T) {
	h := newHarness(t, setup{})
	src := "#define MSR_EFER 0xC0000080\nvoid f(void) {\n\toutb(0x92, 2);\n\tx = rdmsr(MSR_EFER);\n\ty = g(1);\n}\n"
	_, uri := h.file("c.c", src)
	doc := h.open(uri, "c", 1, src)
	hv, err := h.e.Hover(doc, posOf(t, src, "outb", 1))
	if err != nil || hv == nil || !strings.Contains(hv.Contents.Value, "0x92") || hv.Range == nil || hv.Range.Start.Line != 2 {
		t.Fatalf("outb hover = %+v %v", hv, err)
	}
	if hv, _ := h.e.Hover(doc, posOf(t, src, "rdmsr", 1)); hv == nil || !strings.Contains(hv.Contents.Value, "EFER") {
		t.Fatalf("rdmsr hover = %+v", hv)
	}
	if hv, _ := h.e.Hover(doc, posOf(t, src, "g(1)", 0)); hv != nil {
		t.Fatalf("plain call = %+v", hv)
	}
	if hv := h.e.snapshotFor(doc).cHelperHover(protocol.Position{Line: 99}); hv != nil {
		t.Fatal("out of range")
	}
}

// Project tables: a helper added in .falcon-asm.json is hovered; a bad
// entry is a configuration problem at its JSON path.
func TestProjectTables(t *testing.T) {
	h := newHarness(t, setup{opts: `{"toolchain":false}`})
	if _, found := lint.FindConfig(h.dir); found {
		t.Skip("a .falcon-asm.json exists above the temp dir")
	}
	h.file(lint.ConfigFile, `{"tables": {"helpers": [{"name": "port_out8", "kind": "port", "write": true, "value": 1}]}}`)
	src := "void f(void) { port_out8(0x80, 1); }\n"
	_, uri := h.file("p.c", src)
	doc := h.open(uri, "c", 1, src)
	hv, _ := h.e.Hover(doc, posOf(t, src, "port_out8", 1))
	if hv == nil || !strings.Contains(hv.Contents.Value, "0x80") {
		t.Fatalf("override hover = %+v", hv)
	}

	h2 := newHarness(t, setup{opts: `{"toolchain":false}`})
	cfgText := "{\"tables\": {\"ports\": [{\"port\": \"zz\", \"name\": \"x\"}]}}"
	_, cfgURI := h2.file(lint.ConfigFile, cfgText)
	_, uri2 := h2.file("q.S", "\tnop\n")
	h2.open(uri2, "falcon-asm", 1, "\tnop\n")
	p := h2.waitPub(cfgURI, hasMsg("ports[0]"))
	if d := p.Diagnostics[0]; d.Code != "config" || d.Range.Start.Character != strings.Index(cfgText, `{"port"`) {
		t.Fatalf("config diagnostic = %+v", d)
	}
}

// Macros from the toolchain's table resolve helper arguments; function-like
// ones do not.
func TestCHelperToolchainMacros(t *testing.T) {
	src := "void f(void) { outb(PORT, 1); inb(FN(1)); inb(FN); }\n"
	s := &snapshot{
		doc: &docstore.Document{URI: "file:///m.c", Text: src},
		macros: map[string]toolchain.Macro{
			"PORT": {Value: "0x92"},
			"FN":   {Value: "x", Function: true},
		},
	}
	if hv := s.cHelperHover(posOf(t, src, "outb", 0)); hv == nil || !strings.Contains(hv.Contents.Value, "0x92") {
		t.Fatalf("toolchain macro = %+v", hv)
	}
	if hv := s.cHelperHover(posOf(t, src, "inb(FN)", 0)); hv != nil {
		t.Fatalf("function-like macro = %+v", hv)
	}
}

// The hardware tables' reference material stays out of hovers, a
// paragraph two sections share is said once, and the rest is capped.
func TestTrimHardware(t *testing.T) {
	md := "**0xCF9** - Reset control\n\nchipset - 8-bit - write\n\nNotes:\n\n- write 2 then 6\n- byte access only\n\n" +
		"_Confidence: chipset register_\n\nwrites 0x02: arm\n\n---\n\n**0xCF9** - Reset control\n\nchipset - 8-bit - write\n\n" +
		"| off | size |\n|---|---|\n\nwrites 0x06: reset"
	want := "**0xCF9** - Reset control\n\nchipset - 8-bit - write\n\nwrites 0x02: arm\n\n---\n\nwrites 0x06: reset"
	if got := trimHardware(md); got != want {
		t.Fatalf("trimHardware =\n%s\nwant\n%s", got, want)
	}
	if trimHardware("") != "" || trimHardware("Quirks:\n\n- a") != "" {
		t.Fatal("nothing left must be empty")
	}
	var long []string
	for i := range maxHardwareLines + 3 {
		long = append(long, fmt.Sprintf("- item %d", i))
	}
	got := trimHardware("**T**\n\n" + strings.Join(long, "\n"))
	if n := strings.Count(got, "- item"); n != maxHardwareLines-1 || !strings.HasSuffix(got, "_(4 more lines)_") {
		t.Fatalf("capped hover:\n%s", got)
	}
	if got := capLines("a\nb\nc", 2); got != "a\nb\n\n_(1 more line)_" {
		t.Fatalf("capLines = %q", got)
	}
}

// A hover the hardware index cannot add to (an ordinary mnemonic, a mov
// between general registers, a label, a comment) does not ask for it.
// The check is on mayHaveHardware itself: the debounced lint builds the
// index too, so its presence after a hover proves nothing.
func TestHoverSkipsIndex(t *testing.T) {
	h := newHarness(t, setup{})
	src := "\t.code32\nstart:\taddl $1, %eax # note\n\tmovl %eax, %ebx\n\tmovl %eax, %cr0\n\tinb $0x60, %al\n"
	_, uri := h.file("skip.S", src)
	doc := h.open(uri, "falcon-asm", 1, src)
	s := h.e.snapshotFor(doc)
	for _, at := range []string{"addl", "movl %eax, %ebx", "start", "note"} {
		if s.mayHaveHardware(posOf(t, src, at, 1)) {
			t.Fatalf("%q: hardware hover not ruled out", at)
		}
	}
	for _, at := range []string{"movl %eax, %cr0", "%eax, %cr0", "inb", "$0x60", "%eax, %ebx"} {
		if !s.mayHaveHardware(posOf(t, src, at, 1)) {
			t.Fatalf("%q: hardware hover ruled out", at)
		}
	}
	if s.mayHaveHardware(protocol.Position{Line: 99}) {
		t.Fatal("out of range")
	}
}
