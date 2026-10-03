package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// regSrc has values that cross labels: AH behind 2: reaching the int after
// 4:, DX set before a polling loop.
const regSrc = "\t.code16\n\ttestw %bx, %bx\n\tjz 2f\n\tmovw $0x0201, %ax\n\tjmp 4f\n2:\tmovb $0x42, %ah\n" +
	"4:\tmovb $0x80, %dl\n\tint $0x13\n" +
	"\tmovw $0x3FD, %dx\n1:\tinb %dx, %al\n\ttestb $0x20, %al\n\tjz 1b\n" +
	"\tmovl $0xC0000080, %ecx\n3:\trdmsr\n"

func TestFlowHover(t *testing.T) {
	h := newHarness(t, setup{})
	_, uri := h.file("r.S", regSrc)
	doc := h.open(uri, "falcon-asm", 1, regSrc)
	cases := []struct {
		at    string
		delta int
		want  string
	}{
		{"int $0x13", 1, "AH depends on the path here: 02h or 42h"},
		{"inb %dx", 1, "COM1 LSR"},
		{"inb %dx", 6, "`%dx` = `0x3fd` (COM1 LSR)"},
		{"rdmsr", 1, "EFER"},
		{"$0x42", 1, "Extended read"},
	}
	for _, c := range cases {
		hv, err := h.e.Hover(doc, posOf(t, regSrc, c.at, c.delta))
		if err != nil || hv == nil || !strings.Contains(hv.Contents.Value, c.want) {
			t.Errorf("hover at %q+%d = %+v %v, want %q", c.at, c.delta, hv, err, c.want)
		}
	}
	// One flow analysis per version, shared by hover and hints.
	s := h.e.snapshotFor(doc)
	if s.hwIndex().Flow() == nil {
		t.Fatal("hover index has no flow result")
	}
	if a, b := s.regHints(), s.regHints(); len(a) == 0 || &a[0] != &b[0] {
		t.Fatal("hints recomputed")
	}
}

func TestRegisterInlayHints(t *testing.T) {
	// Off by default.
	h := newHarness(t, setup{})
	_, uri := h.file("r.S", regSrc)
	doc := h.open(uri, "falcon-asm", 1, regSrc)
	all := protocol.Range{End: protocol.Position{Line: 99}}
	if hs := h.e.InlayHints(doc, all); len(hs) != 0 {
		t.Fatalf("hints while off: %+v", hs)
	}

	h = newHarness(t, setup{opts: `{"registerHints":true,"toolchain":false}`})
	_, uri = h.file("r.S", regSrc)
	doc = h.open(uri, "falcon-asm", 1, regSrc)
	got := map[int]string{}
	for _, hint := range h.e.InlayHints(doc, all) {
		got[hint.Position.Line] = hint.Label
		if !hint.PaddingLeft || hint.Tooltip == "" {
			t.Errorf("hint %+v", hint)
		}
	}
	want := map[int]string{7: "ah=2h|42h", 9: "dx=3FDh", 13: "ecx=C0000080h (EFER)"}
	for line, label := range want {
		if !strings.HasPrefix(got[line], label) {
			t.Errorf("line %d: hint %q, want %q (all %v)", line, got[line], label, got)
		}
	}
	// The requested range is respected.
	part := h.e.InlayHints(doc, protocol.Range{Start: protocol.Position{Line: 8}, End: protocol.Position{Line: 10}})
	if len(part) != 1 || part[0].Position.Line != 9 {
		t.Fatalf("range [8,10] = %+v", part)
	}
	// C documents get none.
	csrc := "void f(void) {}\n"
	_, curi := h.file("c.c", csrc)
	cdoc := h.open(curi, "c", 1, csrc)
	if hs := h.e.InlayHints(cdoc, all); len(hs) != 0 {
		t.Fatalf("C hints: %+v", hs)
	}
}

// A C file with compile flags gets its toolchain macros fetched (no
// assembly), so a helper call naming a header macro resolves.
func TestCMacroFetch(t *testing.T) {
	h := newHarness(t, setup{})
	src := "void f(void) { outb(HDR_PORT, 1); }\n"
	p, uri := h.file("m.c", src)
	db := fakeDB{p: {Compiler: "gcc", File: p, Exact: true}}
	h = newHarness(t, setup{db: db, root: h.dir})
	h.tc.defaultM = map[string]toolchain.Macro{"HDR_PORT": {Value: "0x92"}}
	doc := h.open(uri, "c", 1, src)
	deadline := time.Now().Add(5 * time.Second)
	for {
		hv, _ := h.e.Hover(doc, posOf(t, src, "outb", 1))
		if hv != nil && strings.Contains(hv.Contents.Value, "0x92") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("macro never resolved; calls %v", h.tc.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, c := range h.tc.Calls() {
		if strings.HasPrefix(c, "assemble") {
			t.Fatalf("a C file was assembled: %v", h.tc.Calls())
		}
	}
}

// The real toolchain on airies-boot: falcon-core's wim.c includes wim.h,
// whose WIM_RES_COMPRESSED is only known through gcc -E -dM with the
// file's flags.
func TestCMacroFetchRealGCC(t *testing.T) {
	root := "/home/vifair/git/home/airies-boot"
	file := filepath.Join(root, "falcon-core/src/wim/wim.c")
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("no gcc")
	}
	if _, err := os.Stat(file); err != nil {
		t.Skip("airies-boot not present")
	}
	db, err := toolchain.LoadDB(root)
	if err != nil {
		t.Skip(err)
	}
	h := newHarness(t, setup{cfg: Config{Toolchain: &toolchain.Runner{}}, root: root, db: nil})
	h.e.mu.Lock()
	h.e.db = db
	h.e.mu.Unlock()
	disk, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// The buffer adds a helper call; the macros come from the file on disk.
	src := string(disk) + "\nstatic void zz(void) { (void)inb(WIM_RES_COMPRESSED); }\n"
	doc := &docstore.Document{URI: toolchain.PathToURI(file), Version: 1, Text: src}
	h.e.Open(doc, "c")
	deadline := time.Now().Add(30 * time.Second)
	for {
		hv, _ := h.e.Hover(doc, posOf(t, src, "inb(WIM", 1))
		if hv != nil && strings.Contains(hv.Contents.Value, "0x4") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("WIM_RES_COMPRESSED never resolved: %+v\n%s", hv, h.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Register hints are a filter over a per-version list.
func BenchmarkRegisterHints(b *testing.B) {
	data, err := os.ReadFile("../rules/hw/testdata/boot_mbr.S")
	if err != nil {
		b.Skip(err)
	}
	src := string(data)
	e := New(Config{Toolchain: &fakeTC{}})
	e.regHints = true
	doc := &docstore.Document{URI: "file:///bench/boot.S", Version: 1, Text: src}
	e.Open(doc, "falcon-asm")
	defer e.Shutdown()
	rng := protocol.Range{Start: protocol.Position{Line: 100}, End: protocol.Position{Line: 160}}
	e.InlayHints(doc, rng) // the first request builds the index and the flow
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.InlayHints(doc, rng)
	}
}

// The first request on a new version: parse, flow, index, hints.
func BenchmarkRegisterHintsCold(b *testing.B) {
	data, err := os.ReadFile("../rules/hw/testdata/boot_mbr.S")
	if err != nil {
		b.Skip(err)
	}
	src := string(data)
	e := New(Config{Toolchain: &fakeTC{}})
	e.regHints = true
	defer e.Shutdown()
	rng := protocol.Range{End: protocol.Position{Line: 300}}
	for i := 0; i < b.N; i++ {
		doc := &docstore.Document{URI: "file:///bench/boot.S", Version: i + 1, Text: src}
		e.Open(doc, "falcon-asm")
		e.InlayHints(doc, rng)
	}
}

// Hover on a register operand of the MBR, flow included, once warm.
func BenchmarkFlowHover(b *testing.B) {
	data, err := os.ReadFile("../rules/hw/testdata/boot_mbr.S")
	if err != nil {
		b.Skip(err)
	}
	src := string(data)
	e := New(Config{Toolchain: &fakeTC{}})
	defer e.Shutdown()
	// Through a store, as the server does: a bare Document recomputes its
	// line starts on every position conversion.
	store := docstore.New()
	store.Open("file:///bench/boot.S", 1, src)
	doc, _ := store.Get("file:///bench/boot.S")
	e.Open(doc, "falcon-asm")
	i := strings.Index(src, "inb     %dx")
	line := strings.Count(src[:i], "\n")
	pos := protocol.Position{Line: line, Character: i - strings.LastIndex(src[:i], "\n") - 1 + 10}
	if _, err := e.Hover(doc, pos); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for k := 0; k < b.N; k++ {
		if _, err := e.Hover(doc, pos); err != nil {
			b.Fatal(err)
		}
	}
}
