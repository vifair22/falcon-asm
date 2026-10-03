package hw

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func flowIndex(text string, mode asm.Mode) *Index {
	p := prog(text, mode)
	return NewIndexFlow(p, flow.Analyze(flow.Build(p), flow.Config{}))
}

// biosdefs are the airies-boot header values the boot.S copy uses.
var biosdefs = map[string]string{
	"STAGE1_LOAD": "0x7C00", "STAGE2_LOAD": "0x7E00", "STAGE2_MAGIC": "0x32534F42",
	"GATE_MAGIC": "(0x7E00 + 0x04)", "SERIAL_PORT": "0x3F8", "SERIAL_DIVISOR": "1",
	"STAGE1_S2_LBA_OFF": "0x1B2", "STAGE1_S2_SECTORS_OFF": "0x1B6",
}

func flowIndexMacros(text string, macros map[string]string) *Index {
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	toks := lexer.Tokenize(text)
	p := asm.Parse(doc, toks, asm.Config{InitialMode: asm.Mode16, Macros: macros})
	return NewIndexFlow(p, flow.Analyze(flow.Build(p), flow.Config{}))
}

// hoverAt is the hardware hover at the nth occurrence (from 0) of sub,
// delta bytes into it.
func hoverAt(t *testing.T, ix *Index, sub string, nth, delta int) string {
	t.Helper()
	text := ix.Prog.Doc.Text
	off := -1
	for k, from := 0, 0; k <= nth; k++ {
		j := strings.Index(text[from:], sub)
		if j < 0 {
			t.Fatalf("%q #%d not found", sub, nth)
		}
		off = from + j
		from = off + 1
	}
	md, _, _ := ix.Hover(hwdb.Default(), off+delta)
	return md
}

// boot.S's values cross labels: the DAP read's AH is set behind label 2:
// and the int is after label 4:; the serial polling loop sets DX before
// label 1:.
func TestFlowHoverBootS(t *testing.T) {
	ix := flowIndexMacros(fixture(t, "boot_mbr.S"), biosdefs)
	cases := []struct {
		sub        string
		nth, delta int
		want       []string
	}{
		{"int     $0x13", 2, 1, []string{"AH depends on the path here: 02h or 42h", "Extended read", "Read sectors"}},
		{"inb     %dx, %al", 0, 1, []string{"COM1 LSR"}},
		{"inb     %dx, %al", 0, 10, []string{"`%dx` = `0x3fd` (COM1 LSR)"}},
		{"outb    %al, %dx", 0, 15, []string{"`%dx` = `0x3f8` (COM1 THR"}},
		{"movb    $0x42, %ah", 0, 16, []string{"after: `%ah` = `0x42` (INT 13h AH=42h"}},
		{"movb    $0x42, %ah", 0, 9, []string{"Extended read"}},
		{"movw    $0x0201, %ax", 0, 9, []string{"Read sectors"}},
	}
	for _, c := range cases {
		md := hoverAt(t, ix, c.sub, c.nth, c.delta)
		for _, w := range c.want {
			if !strings.Contains(md, w) {
				t.Errorf("%q#%d+%d: %q lacks %q", c.sub, c.nth, c.delta, md, w)
			}
		}
	}
}

func TestFlowHoverSystem(t *testing.T) {
	src := "\t.code32\n\tmovl $0xC0000080, %ecx\n1:\trdmsr\n\torl $0x100, %eax\n2:\twrmsr\n" +
		"\tmovl $7, %eax\n\txorl %ecx, %ecx\n3:\tcpuid\n" +
		"\tmovl %cr4, %eax\n\torl $0x20, %eax\n4:\tmovl %eax, %cr4\n" +
		"\tmovl $0x80000011, %eax\n5:\tmovl %eax, %cr0\n" +
		"\txorl %ecx, %ecx\n\txorl %edx, %edx\n\tmovl $7, %eax\n6:\txsetbv\n" +
		"\tmovw $0xCF8, %dx\n7:\tinl %dx, %eax\n"
	ix := flowIndex(src, asm.Mode32)
	cases := []struct {
		sub   string
		delta int
		want  string
	}{
		{"rdmsr", 0, "EFER"},
		{"wrmsr", 0, "`LME`"},
		{"cpuid", 0, "CPUID"},
		{"movl %eax, %cr4", 0, "`PAE`"},
		{"movl %eax, %cr4", 12, "`PAE`"},
		{"movl %eax, %cr0", 0, "value written: 0x80000011"},
		{"xsetbv", 0, "XCR0"},
		{"inl %dx", 0, "CONFIG"},
		{"rdmsr", -6, ""},
	}
	for _, c := range cases {
		md := hoverAt(t, ix, c.sub, 0, c.delta)
		if c.want == "" {
			continue
		}
		if !strings.Contains(md, c.want) {
			t.Errorf("%q+%d: %q lacks %q", c.sub, c.delta, md, c.want)
		}
	}
	// The register operands: ecx before the rdmsr, eax as the leaf.
	if md := hoverAt(t, ix, "movl $7, %eax", 0, 10); !strings.Contains(md, "CPUID leaf 0x7") {
		t.Errorf("leaf hover = %q", md)
	}
	if md := hoverAt(t, ix, "movl $0xC0000080, %ecx", 0, 19); !strings.Contains(md, "IA32_EFER") {
		t.Errorf("ecx hover = %q", md)
	}
	// The immediate that feeds a consumer across a label.
	if md := hoverAt(t, ix, "$0xCF8", 0, 1); !strings.Contains(md, "CONFIG") {
		t.Errorf("port immediate hover = %q", md)
	}
}

func TestCandidatesAndValueText(t *testing.T) {
	if ValueText(0x42, 0xFF00>>8, 16) != "0x??42" {
		t.Fatal(ValueText(0x42, 0xFF, 16))
	}
	if got := ValueText(0x1, 0x1, 8); got != "0x?? (bits 0x1 known: 0x1)" {
		t.Fatal(got)
	}
	if ValueText(5, 0xFF, 8) != "0x5" {
		t.Fatal(ValueText(5, 0xFF, 8))
	}
	if _, ok := Candidates(nil, 0, "ah", 4); ok {
		t.Fatal("nil result")
	}
	ix := flowIndex("\ttestw %bx, %bx\n\tjz 1f\n\tmovb $2, %ah\n\tjmp 2f\n1:\tmovb $3, %ah\n2:\tint $0x13\n", asm.Mode16)
	i := len(ix.Prog.Stmts) - 1
	for ix.Prog.Stmts[i].Kind != asm.Instr {
		i--
	}
	vals, ok := Candidates(ix.Flow(), i, "ah", 4)
	if !ok || len(vals) != 2 {
		t.Fatalf("candidates = %v %v", vals, ok)
	}
	if _, ok := Candidates(ix.Flow(), i, "ah", 1); ok {
		t.Fatal("over the cap")
	}
	if _, ok := Candidates(ix.Flow(), i, "bl", 4); ok {
		t.Fatal("bl is unknown")
	}
	if _, ok := Candidates(ix.Flow(), i, "nosuch", 4); ok {
		t.Fatal("bad register")
	}
}

func TestRegisterHints(t *testing.T) {
	ix := flowIndexMacros(fixture(t, "boot_mbr.S"), biosdefs)
	db := hwdb.Default()
	hints := ix.RegisterHints(db)
	got := map[string]bool{}
	for _, h := range hints {
		got[h.Label] = true
	}
	for _, want := range []string{"ah=41h bx=55AAh", "dx=3FDh", "ah=2h|42h", "ah=0h"} {
		found := false
		for l := range got {
			found = found || strings.HasPrefix(l, want)
		}
		if !found {
			t.Errorf("no hint %q in %v", want, got)
		}
	}
	src := "\t.code32\n\tmovl $0xC0000080, %ecx\n1:\trdmsr\n\torl $0x100, %eax\n\twrmsr\n" +
		"\tmovl $1, %eax\n\tcpuid\n\tmovl $0x11, %eax\n\tmovl %eax, %cr0\n\tljmp $8, $2f\n2:\tmovw $0x3F8, %dx\n\tmovb $0x41, %al\n\toutb %al, %dx\n" +
		"\t.code16\n\tmovw $0, %ax\n\tmovw %ax, %ds\n\tlgdt gdt\ngdt:\t.quad 0\n"
	six := flowIndex(src, asm.Mode32)
	var labels []string
	for _, h := range six.RegisterHints(db) {
		labels = append(labels, h.Label+" | "+h.Tooltip)
	}
	all := strings.Join(labels, "\n")
	for _, want := range []string{"ecx=C0000080h (EFER) | MSR 0xc0000080", "eax=1h", "eax=11h", "PE=1", "dx=3F8h al=41h | port 0x3f8", "ds=0h"} {
		if !strings.Contains(all, want) {
			t.Errorf("hints lack %q:\n%s", want, all)
		}
	}
	if NewIndex(ix.Prog).RegisterHints(db) != nil {
		t.Fatal("block-local index gives hints")
	}
}

// The bios-struct rule finds the packet through flow's last writer of SI
// when the call is after a label.
func TestFlowBIOSStruct(t *testing.T) {
	src := "\tmovw $dap, %si\n1:\tmovb $0x42, %ah\n\tint $0x13\n\tret\ndap:\t.byte 0x18, 0\n\t.word 1, 0x7E00, 0\n\t.quad 1\n"
	mustHave(t, run(t, src, opts{}, "bios-struct"), "bios-struct", "dap")
	lea := strings.Replace(src, "movw $dap, %si", "leaw dap, %si", 1)
	mustHave(t, run(t, lea, opts{}, "bios-struct"), "bios-struct", "dap")
	// SI from something that is not a label: nothing to check.
	other := strings.Replace(src, "movw $dap, %si", "movw %bx, %si", 1)
	if got := run(t, other, opts{}, "bios-struct"); len(got) != 0 {
		t.Fatal(got)
	}
	ix := flowIndex(src, asm.Mode16)
	if ix.flowSym(0, "si") != "" || ix.flowSym(2, "nosuch") != "" {
		t.Fatal("flowSym on the writer itself or a bad register")
	}
	if NewIndex(ix.Prog).flowSym(2, "si") != "" {
		t.Fatal("block-local index")
	}
}

// 16-bit code that flow proves runs with PE set is protected mode: its
// selector loads are checked even several labels after the CR0 write.
func TestFlowSelectorPE(t *testing.T) {
	src := "\t.code16\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n1:\tnop\n2:\tmovw $0x08, %ax\n\tmovw %ax, %ss\n\thlt\n" +
		"gdt:\t.quad 0\n\t.quad 0x00CF9A000000FFFF\ngdt_end:\ngdt_desc: .word gdt_end - gdt - 1\n\t.long gdt\n"
	mustHave(t, run(t, src, opts{}, "selector-type"), "selector-type", "SS needs a writable data")
	// Known clear: a real-mode segment, not a selector.
	real := strings.Replace(src, "orl $1, %eax", "andl $0xFFFFFFFE, %eax", 1)
	if got := run(t, real, opts{}, "selector-type"); len(got) != 0 {
		t.Fatal(got)
	}
	ix := flowIndex(src, asm.Mode16)
	if d, stmt, ok := ix.Descriptor(8); !ok || !d.Code || stmt < 0 {
		t.Fatalf("descriptor 8 = %+v %d %v", d, stmt, ok)
	}
	for _, sel := range []int64{0, 4, 0x10, 0x30} {
		if _, _, ok := ix.Descriptor(sel); ok {
			t.Errorf("selector %#x resolved", sel)
		}
	}
	if _, _, ok := NewIndex(prog("\tnop\n", asm.Mode16)).Descriptor(8); ok {
		t.Fatal("no GDT")
	}
}

func TestIndexOf(t *testing.T) {
	var got *Index
	lint.Register(&lint.Rule{Code: "zz-hw-indexof-probe", Section: 7, Default: lint.Warning, Langs: lint.LangAsm,
		Doc: lint.Doc{Summary: "test"}, Check: func(c *lint.Context) { got = IndexOf(c) }})
	r, _ := lint.Lookup("zz-hw-indexof-probe")
	lint.Run(lint.Input{Prog: prog("\tnop\n", asm.Mode16), Lang: lint.LangAsm, Rules: []*lint.Rule{r}, Tables: hwdb.Default()})
	if got == nil || got.Flow() == nil {
		t.Fatalf("IndexOf = %+v", got)
	}
}

func TestPartialValueHovers(t *testing.T) {
	src := "\t.code32\n\tmovl %cr0, %eax\n\tandl $0x9FFFFFFF, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n" +
		"\tmovl $0x1B, %ecx\n\trdmsr\n\torl $0x800, %eax\n\twrmsr\n" +
		"\tmovl $0, %ecx\n\txgetbv\n\torl $3, %eax\n\txsetbv\n" +
		"\tmovl $7, %eax\n\tmovl $1, %ecx\n\tcpuid\n" +
		"\ttestl %ebx, %ebx\n\tjz 1f\n\tmovl $5, %edx\n\tjmp 2f\n1:\tmovl $6, %edx\n2:\tmovl %edx, %esi\n"
	ix := flowIndex(src, asm.Mode32)
	cases := []struct {
		sub   string
		delta int
		want  string
	}{
		{"movl %eax, %cr0", 0, "bits 0x1 known set (`PE`)"},
		{"movl %eax, %cr0", 0, "known clear"},
		{"wrmsr", 0, "bits 0x800 known set"},
		{"xsetbv", 0, "XCR0"},
		{"cpuid", 0, "CPUID"},
		{"movl %edx, %esi", 6, "`0x5` or `0x6`, depending on the path"},
		{"movl %eax, %cr0", 6, "`%eax` = `0x"},
	}
	for _, c := range cases {
		if md := hoverAt(t, ix, c.sub, 0, c.delta); !strings.Contains(md, c.want) {
			t.Errorf("%q+%d: %q lacks %q", c.sub, c.delta, md, c.want)
		}
	}
	if md := hoverAt(t, ix, "movl %eax, %cr0", 0, 12); !strings.Contains(md, "after: `%cr0`") || !strings.Contains(md, "`PE` set") {
		t.Errorf("cr0 operand = %q", md)
	}
	// AH from a word move.
	s := &ix.Prog.Stmts[0]
	if _, ok := immToAH(s, nil); ok {
		t.Fatal("immToAH on a non-mov")
	}
	p := prog("\tmovw $0x0201, %ax\n\tmovb $0x42, %ah\n\tmovb $1, %al\n", asm.Mode16)
	for k, want := range []uint64{2, 0x42} {
		st := &p.Stmts[k]
		if v, ok := immToAH(st, &st.Ops[0]); !ok || v != want {
			t.Errorf("stmt %d: %#x %v", k, v, ok)
		}
	}
	if _, ok := immToAH(&p.Stmts[2], &p.Stmts[2].Ops[0]); ok {
		t.Fatal("al is not ah")
	}
	if crWriteHover(event{kind: evCR, cr: "cr8"}, asm.Mode64) != "" {
		t.Fatal("cr8")
	}
	if crWriteHover(event{kind: evCR, cr: "cr4"}, asm.Mode64) != "" {
		t.Fatal("nothing known")
	}
	if msrEventHover(hwdb.Default(), event{kind: evMSR}) != "" {
		t.Fatal("index unknown")
	}
}
