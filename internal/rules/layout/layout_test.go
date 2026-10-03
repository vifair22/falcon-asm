package layout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

var allCodes = []string{"budget-exceeded", "org-overlap", "section-overlap", "table-alignment", "page-table-alignment", "simd-alignment"}

func prog(path, text string, macros map[string]string) *asm.Program {
	doc := &docstore.Document{URI: toolchain.PathToURI(path), Text: text}
	toks := lexer.Tokenize(text)
	p := asm.Parse(doc, toks, asm.Config{Macros: macros})
	p.Doc, p.Toks = doc, toks
	return p
}

// assemble writes text to dir/name and runs the real toolchain on it.
func assemble(t *testing.T, dir, name, text string, args ...string) (string, *toolchain.AsmResult) {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not installed")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	var r toolchain.Runner
	res, err := r.Assemble(context.Background(), path, toolchain.Flags{Compiler: "gcc", Args: args, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return path, res
}

func lintIt(t *testing.T, p *asm.Program, res *toolchain.AsmResult, codes ...string) []lint.Finding {
	t.Helper()
	if len(codes) == 0 {
		codes = allCodes
	}
	var rules []*lint.Rule
	for _, c := range codes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		rules = append(rules, r)
	}
	return lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Listing: res, Rules: rules})
}

func summary(fs []lint.Finding) string {
	var s []string
	for _, f := range fs {
		s = append(s, f.Code+"@"+itoa(f.Range.Start.Line))
	}
	return strings.Join(s, " ")
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

const bootOK = `	.code16
	.section .text.stage1, "ax"
	.globl _start
_start:
	cli
	.fill 439, 1, 0x90
	. = _start + 510
	.byte 0x55, 0xAA
`

func TestBudgetLensBootSector(t *testing.T) {
	dir := t.TempDir()
	path, res := assemble(t, dir, "boot.S", bootOK)
	p := prog(path, bootOK, nil)
	bs := Budgets(p, res)
	if len(bs) != 1 {
		t.Fatalf("budgets = %+v", bs)
	}
	b := bs[0]
	if b.Title() != "stage1: 440 / 510 bytes (70 free)" || b.Line != 3 || b.SetBy != 6 || !b.Exact || b.Base != "_start" {
		t.Fatalf("budget = %+v %q", b, b.Title())
	}
	if got := lintIt(t, p, res); len(got) != 0 {
		t.Fatalf("clean boot sector: %v", got)
	}
	if Budgets(p, nil) != nil || Budgets(nil, res) != nil {
		t.Fatal("no listing or program: no budgets")
	}
}

func TestBudgetExceeded(t *testing.T) {
	dir := t.TempDir()
	// Instructions in a .rept list one row each, so the listing's bytes
	// are a usable lower bound even though gas gave up on addresses.
	text := "\t.code16\n\t.section .text.stage1, \"ax\"\n_start:\n\t.rept 520\n\tnop\n\t.endr\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n"
	path, res := assemble(t, dir, "over.S", text)
	p := prog(path, text, nil)
	got := lintIt(t, p, res)
	if summary(got) != "budget-exceeded@6" || got[0].Severity != lint.Error ||
		!strings.Contains(got[0].Message, "stage1 is over its 510-byte budget") ||
		!strings.Contains(got[0].Message, "by at least 10 bytes") || !strings.Contains(got[0].Message, "_start (line 3)") {
		t.Fatalf("findings = %v", got)
	}
	bs := Budgets(p, res)
	if len(bs) != 1 || bs[0].Exact || bs[0].Title() != "stage1: at least 520 / 510 bytes (10+ over)" {
		t.Fatalf("budgets = %+v", bs)
	}
}

func TestBudgetSlotOverrun(t *testing.T) {
	dir := t.TempDir()
	text := "\t.section .text.s, \"ax\"\n_start:\n\tnop\n\t.org _start + 4\nslot:\n\t.long 1, 2\n\t.org _start + 8\n\t.byte 0\n"
	path, res := assemble(t, dir, "slot.S", text)
	p := prog(path, text, nil)
	got := lintIt(t, p, res)
	if summary(got) != "budget-exceeded@6" || !strings.Contains(got[0].Message, "placed at _start + 0x4 by line 4") ||
		!strings.Contains(got[0].Message, "the slot holds 4 bytes and at least 8 are placed there") {
		t.Fatalf("findings = %v", got)
	}
}

func TestOrgOverlap(t *testing.T) {
	dir := t.TempDir()
	text := "\t.text\n\t.org 0x10\n\t.byte 1\n\t.org 0x8\n\t.byte 2\n\t.org 0x20\n\t.byte 3\n\t.org 0x20\n"
	path, res := assemble(t, dir, "org.S", text)
	p := prog(path, text, nil)
	got := lintIt(t, p, res)
	// gas also rejects line 3; org-overlap explains it, and budget-exceeded
	// stays out of the way.
	if summary(got) != "org-overlap@3 org-overlap@7" || got[0].Severity != lint.Error ||
		!strings.Contains(got[0].Message, "behind the move at line 2 to offset 0x10") ||
		!strings.Contains(got[1].Message, "same place as the move at line 6") || len(got[0].Related) != 1 {
		t.Fatalf("findings = %v", got)
	}
	// Static: needs no listing.
	if got := lintIt(t, p, nil, "org-overlap"); len(got) != 2 {
		t.Fatalf("without listing: %v", got)
	}
	// Different bases are not compared; a move to the same place with
	// nothing between is fine; unknown targets are skipped.
	quiet := "a:\n\t.org a + 4\nb:\n\t.org b + 0\n\t.org 4\n\t.org 4\n\t.org UNKNOWN_THING\n\t. = . + 4\n"
	if got := lintIt(t, prog("/x/q.S", quiet, nil), nil, "org-overlap"); len(got) != 0 {
		t.Fatalf("quiet: %v", got)
	}
}

func TestSectionBudgetComment(t *testing.T) {
	dir := t.TempDir()
	text := "\t.section .text.s2, \"ax\" /* falcon-asm: budget=16 */\n\t.fill 20, 1, 0x90\n" +
		"# falcon-asm: budget=.data:0x100 ignore=nothing\n\t.data\n\t.long 1\n\t.section .bss\n\t# falcon-asm: budget=1K\n\t.skip 8\n"
	path, res := assemble(t, dir, "sec.S", text)
	p := prog(path, text, nil)
	got := lintIt(t, p, res, "budget-exceeded")
	if summary(got) != "budget-exceeded@0" || !strings.Contains(got[0].Message, "section .text.s2 is over its 16-byte budget: 20 bytes (4 over)") {
		t.Fatalf("findings = %v", got)
	}
	var titles []string
	for _, b := range Budgets(p, res) {
		titles = append(titles, b.Title())
	}
	want := ".text.s2: 20 / 16 bytes (4 over)|.data: 4 / 256 bytes (252 free)|.bss: 8 / 1024 bytes (1016 free)"
	if strings.Join(titles, "|") != want {
		t.Fatalf("titles = %q", strings.Join(titles, "|"))
	}
	// Without an object, only listed bytes over the budget count.
	res.SectionSizes = nil
	bs := Budgets(p, res)
	if len(bs) != 1 || bs[0].Exact || bs[0].Name != ".text.s2" {
		t.Fatalf("lower bound budgets = %+v", bs)
	}
}

func TestTableAlignment(t *testing.T) {
	dir := t.TempDir()
	text := "\t.code32\n\t.section .data\n\t.balign 8\n\t.byte 0\ngdt:\n\t.quad 0\ngdt_desc:\n\t.word 7\n\t.long gdt\n" +
		"\t.section .rodata\n\t.byte 0\nidt:\n\t.quad 0\nidt_desc: .word 7\n\t.long idt\n" +
		"\t.text\n\tlgdt gdt_desc\n\tlidt %cs:idt_desc\n"
	path, res := assemble(t, dir, "gdt.S", text, "-m32")
	p := prog(path, text, nil)
	got := lintIt(t, p, res)
	if summary(got) != "table-alignment@4 table-alignment@11" {
		t.Fatalf("findings = %v", got)
	}
	if !strings.Contains(got[0].Message, "the GDT (loaded by lgdt at line 17) is at offset 0x1 of .data, which is 8-byte aligned") ||
		got[0].Severity != lint.Info || len(got[0].Fixes) != 1 || got[0].Fixes[0].Edits[0].NewText != ".balign 8\n" {
		t.Fatalf("gdt = %+v", got[0])
	}
	if !strings.Contains(got[1].Message, "depends on where .rodata is placed") {
		t.Fatalf("idt = %+v", got[1])
	}
}

func TestPageTableAlignment(t *testing.T) {
	dir := t.TempDir()
	text := "\t.code32\n\t.section .data\n\t.balign 4096\n\t.long 0\npml4:\n\t.quad pdpt + 3\n\t.balign 4096\npdpt:\n\t.quad pd + 0x3\n" +
		"\t.long 0\npd:\n\t.quad 0x83\n\t.text\n\tmovl $pml4, %eax\n\torl $0, %ebx\n\tmovl %eax, %cr3\n"
	path, res := assemble(t, dir, "pt.S", text)
	p := prog(path, text, nil)
	got := lintIt(t, p, res)
	if summary(got) != "page-table-alignment@4 page-table-alignment@10" {
		t.Fatalf("findings = %v", got)
	}
	if !strings.Contains(got[0].Message, "page table pml4 (loaded by %cr3 at line 16)") ||
		!strings.Contains(got[1].Message, "referenced from an entry of pdpt") || got[1].Related[0].Range.Start.Line != 7 {
		t.Fatalf("messages = %v", got)
	}
	// Weak section alignment: information only, under table-alignment.
	weak := strings.Replace(strings.Replace(text, ".balign 4096", ".balign 16", 1), "\t.balign 4096\npdpt", "\t.balign 16\npdpt", 1)
	path, res = assemble(t, dir, "weak.S", weak)
	got = lintIt(t, prog(path, weak, nil), res)
	if summary(got) != "table-alignment@4 table-alignment@7 table-alignment@10" || !strings.Contains(got[0].Message, "page table pml4") {
		t.Fatalf("weak = %v", got)
	}
}

func TestPageTableTrace(t *testing.T) {
	cases := map[string]bool{
		"\tmovl $pml4, %eax\n\tmovl %eax, %cr3\n":                    true,
		"\tleal pml4, %ecx\n\tmovl %ecx, %cr3\n":                     true,
		"\tmov $pml4, %rax\n\txchg %rbx, %rcx\n\tmov %rax, %cr3\n":   true,
		"\tmovl $pml4, %eax\n\tcpuid\n\tmovl %eax, %cr3\n":           false,
		"\tmovl $pml4, %eax\nl:\n\tmovl %eax, %cr3\n":                false,
		"\tmovl $pml4, %eax\n\tjmp 1f\n1:\tmovl %eax, %cr3\n":        false,
		"\tmovl %ebx, %eax\n\tmovl %eax, %cr3\n":                     false,
		"\tmovl $5, %eax\n\tmovl %eax, %cr3\n":                       false,
		"\tmovl %eax, %cr3\n":                                        false,
		"\tmovl $pml4, %eax\n\tmull %ebx\n\tmovl %eax, %cr3\n":       false,
		"\tmovl $pml4, %eax\n\txchg %ebx, %eax\n\tmovl %eax, %cr3\n": false,
		"\tmovl $pml4, %r8d\n\tmov %r8, %cr3\n":                      true,
	}
	for src, want := range cases {
		text := "pml4:\n\t.quad 0\n" + src
		got := len(pageTables(prog("/x/t.S", text, nil))) == 1
		if got != want {
			t.Errorf("%q: found = %v", src, got)
		}
	}
}

func TestSIMDAlignment(t *testing.T) {
	dir := t.TempDir()
	text := "\t.data\n\t.balign 16\n\t.long 0\nmask:\n\t.quad 0, 0\n\t.balign 16\nok:\n\t.quad 0, 0\n\t.text\n" +
		"\tmovdqa mask(%rip), %xmm0\n\tmovaps %xmm1, ok(%rip)\n\tvmovdqa ok(%rip), %ymm0\n\tmovdqu mask(%rip), %xmm0\n\tmovdqa (%rax), %xmm0\n\tmovdqa ok+4(%rip), %xmm2\n"
	path, res := assemble(t, dir, "simd.S", text)
	got := lintIt(t, prog(path, text, nil), res)
	if summary(got) != "simd-alignment@9 simd-alignment@14" || !strings.Contains(got[0].Message, "movdqa needs a 16-byte aligned operand") {
		t.Fatalf("findings = %v", got)
	}
}

func TestSectionOverlap(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "/* boot */\nENTRY(_start)\nSECTIONS\n{\n  . = 0x7C00;\n  .stage1 : { *(.text.stage1) }\n" +
		"  .stage2 0x7D00 : AT(0x1000) { KEEP(*(.text.stage2)) . = ALIGN(8); end2 = .; } > RAM\n  /DISCARD/ : { *(.comment) }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "boot.ld"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	text := "\t.section .text.stage1, \"ax\"\n\t.fill 300, 1, 0x90\n\t.section .text.stage2, \"ax\"\n\t.long 0\n"
	path, res := assemble(t, src, "boot.S", text)
	got := lintIt(t, prog(path, text, nil), res)
	if summary(got) != "section-overlap@0" || !strings.Contains(got[0].Message, "output section .stage1 at 0x7c00, which holds at least 300 bytes") ||
		!strings.Contains(got[0].Message, ".stage2 at 0x7d00 (boot.ld line 7)") || got[0].Related[0].Range.Start.Line != 6 {
		t.Fatalf("findings = %v", got)
	}
	// Room enough: quiet (the script changed, so the cache rereads it).
	fixed := strings.Replace(script, "0x7D00", "0x7E00", 1) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "boot.ld"), []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lintIt(t, prog(path, text, nil), res); len(got) != 0 {
		t.Fatalf("fixed: %v", got)
	}
	// A file whose sections the script does not name is not linked by it.
	other := "\t.section .text.other, \"ax\"\n\t.fill 300, 1, 0x90\n"
	p2, res2 := assemble(t, src, "other.S", other)
	if got := lintIt(t, prog(p2, other, nil), res2); len(got) != 0 {
		t.Fatalf("untied: %v", got)
	}
}

func TestParseScript(t *testing.T) {
	s := parseScript("x.ld", `SECTIONS {
	. = 0x1000;
	.a : { *(.a) }
	.b (NOLOAD) : ALIGN(16) { foo.o(.b .b2) EXCLUDE_FILE(*crt*.o) *(SORT(.bx*)) }
	. = ALIGN(8);
	.c : { *(.c) }
	.d 0x3000 : { *(.d) } >RAM AT>ROM :hdr =0x90
	PROVIDE(end = .);
	.e 0x4000 : { LONG(0) *(.e) }
}`)
	if len(s.sects) != 5 {
		t.Fatalf("sections = %+v", s.sects)
	}
	want := []struct {
		name  string
		addr  int64
		known bool
	}{{".a", 0x1000, true}, {".b", 0, false}, {".c", 0, false}, {".d", 0x3000, true}, {".e", 0x4000, true}}
	for i, w := range want {
		o := s.sects[i]
		if o.name != w.name || o.addrKnown != w.known || (w.known && o.addr != w.addr) {
			t.Errorf("section %d = %+v, want %+v", i, o, w)
		}
	}
	if b := s.sects[1].inputs; len(b) != 2 || b[0].file != "foo.o" || strings.Join(b[0].secs, ",") != ".b,.b2" || strings.Join(b[1].secs, ",") != ".bx*" {
		t.Fatalf(".b inputs = %+v", b)
	}
	if !s.names[".b2"] || s.names[".bx*"] {
		t.Fatalf("names = %v", s.names)
	}
	if got := s.match("foo.o", ".b2"); got != 1 {
		t.Fatalf("match = %d", got)
	}
	if got := s.match("bar.o", ".b2"); got != -1 {
		t.Fatalf("other object match = %d", got)
	}
	// Unsupported constructs stop the parse; no SECTIONS means nothing.
	for _, src := range []string{"MEMORY { RAM : ORIGIN = 0, LENGTH = 1K }", "SECTIONS { OVERLAY : { .x { *(.x) } } }",
		"SECTIONS { INCLUDE other.ld }", "SECTIONS { .a 0x10 { *(.a) } }", "SECTIONS { .a : { *(.a) ", "SECTIONS { .a : AT(0) ", "SECTIONS { .a",
		"SECTIONS { /* open"} {
		if s := parseScript("y.ld", src); len(s.sects) != 0 {
			t.Errorf("%q: %+v", src, s.sects)
		}
	}
}

func TestHelpers(t *testing.T) {
	look := func(n string) (string, bool) {
		if n == "OFF" {
			return "0x10", true
		}
		return "", false
	}
	refs := []struct {
		in   string
		base string
		off  int64
		ok   bool
	}{
		{"_start + 510", "_start", 510, true}, {"(_start+OFF)", "_start", 16, true}, {"_start - 2", "_start", -2, true},
		{"OFF + 2", "", 18, true}, {"0x1fe", "", 510, true}, {". + 4", "", 0, false}, {"_start * 2", "", 0, false},
		{"_start + x", "", 0, false}, {"tbl", "tbl", 0, true}, {"(1", "", 0, false},
	}
	for _, r := range refs {
		b, o, ok := parseRef(r.in, look, false)
		if b != r.base || o != r.off || ok != r.ok {
			t.Errorf("parseRef(%q) = %q %d %v", r.in, b, o, ok)
		}
	}
	if b, o, ok := parseRef("pd | 7", look, true); b != "pd" || o != 7 || !ok {
		t.Errorf("or form = %q %d %v", b, o, ok)
	}
	for in, want := range map[string]string{" .org _start + 0x1B2": "_start + 0x1B2", ".ORG 5, 0xff # c": "5", ". = 510": "510", "nop": "", ". == 1": "", ".orgx 1": ""} {
		got, _ := moveTargetText(in)
		if got != want {
			t.Errorf("moveTargetText(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]int64{"512": 512, "0x200": 512, "4K": 4096, "4KiB": 4096, "1M": 1 << 20, "2mib": 2 << 20, "0": 0, "x": 0} {
		got, _ := parseBytes(in)
		if got != want {
			t.Errorf("parseBytes(%q) = %d", in, got)
		}
	}
	for in, want := range map[string]string{"%eax": "rax", "ah": "rax", "r9d": "r9", "r12b": "r12", "sil": "rsi", "bp": "rbp", "esp": "rsp",
		"bl": "rbx", "cx": "rcx", "dl": "rdx", "edi": "rdi", "xmm0": "", "cr3": ""} {
		if got := regFamily(in); got != want {
			t.Errorf("regFamily(%q) = %q", in, got)
		}
	}
	if stageName(".text.stage1", "_start") != "stage1" || stageName(".text", "_start") != "_start" || stageName(".text", "") != ".text" {
		t.Error("stageName")
	}
	if got := sectionAt(prog("/x/a.S", "", nil), 0); got != ".text" {
		t.Errorf("empty sectionAt = %q", got)
	}
	if got := sectionAt(prog("/x/a.S", "\tnop\n\t.section .foo, \"a\"\n", nil), 100); got != ".foo" {
		t.Errorf("trailing sectionAt = %q", got)
	}
	if got := sectionAt(prog("/x/a.S", "\t.data\n", nil), 100); got != ".data" {
		t.Errorf("trailing .data = %q", got)
	}
	if _, ok := docPath(&asm.Program{Doc: &docstore.Document{URI: "untitled:1"}}); ok {
		t.Error("untitled has no path")
	}
	b := Budget{Name: "s", Limit: 10, Used: 12, Exact: true}
	if b.Title() != "s: 12 / 10 bytes (2 over)" || overBy(b) != "2 over" || atLeast(b) != "12" {
		t.Errorf("over title %q", b.Title())
	}
	b.Exact = false
	if overBy(b) != "at least 2 over" || atLeast(b) != "at least 12" {
		t.Error("lower bound wording")
	}
	if refText("", 4) != "offset 0x4" {
		t.Error("refText")
	}
}

// Listing-free contexts: every listing rule is skipped, and the unknown
// target branch of the backwards message still reads sensibly.
func TestNoListing(t *testing.T) {
	p := prog("/x/a.S", "\t.org FOO * 2\n", nil)
	if got := lintIt(t, p, nil); len(got) != 0 {
		t.Fatalf("findings = %v", got)
	}
	res := &toolchain.AsmResult{Diagnostics: map[string][]protocol.Diagnostic{
		"/x/a.S": {{Range: protocol.Range{}, Message: "attempt to move .org backwards"}},
	}}
	got := lintIt(t, p, res, "budget-exceeded")
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "the code and data before this line") {
		t.Fatalf("unknown target = %v", got)
	}
}

// TestRealTree checks the lens on airies-boot's boot sector: 70 bytes are
// free in front of the signature. Opt-in, like toolchain's real tree test.
func TestRealTree(t *testing.T) {
	root := os.Getenv("FALCON_ASM_REAL_TREE")
	if root == "" {
		t.Skip("FALCON_ASM_REAL_TREE not set")
	}
	db, err := toolchain.LoadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "falcon-bios/src/start/boot.S")
	f, ok := db.Flags(file)
	if !ok {
		t.Fatalf("no flags for %s", file)
	}
	var r toolchain.Runner
	res, err := r.Assemble(context.Background(), file, f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	macros, err := r.Macros(context.Background(), file, f)
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]string{}
	for k, m := range macros {
		vals[k] = m.Value
	}
	p := prog(file, string(data), vals)
	bs := Budgets(p, res)
	var titles []string
	for _, b := range bs {
		titles = append(titles, b.Title())
		t.Logf("lens line %d: %s", b.Line+1, b.Title())
	}
	if len(bs) != 1 || bs[0].Title() != "stage1: 440 / 510 bytes (70 free)" || p.Stmts[p.Labels["_start"]].Line != bs[0].Line {
		t.Fatalf("budgets = %v", titles)
	}
	for _, fd := range lintIt(t, p, res) {
		t.Errorf("finding: %v", fd)
	}
}
