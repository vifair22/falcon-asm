package hw

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// eventsOf runs the tracker over text and returns its events by statement
// line.
func eventsOf(t *testing.T, text string, mode asm.Mode) (*Index, map[int]event) {
	t.Helper()
	ix := NewIndex(prog(text, mode))
	out := map[int]event{}
	for _, e := range ix.trk.events {
		out[ix.Prog.Stmts[e.stmt].Line] = e
	}
	return ix, out
}

func TestTrackerArithmetic(t *testing.T) {
	text := "\tmovl $0x10, %eax\n" + // 0
		"\taddl $2, %eax\n\tsubl $1, %eax\n\tshll $4, %eax\n\tshrl $1, %eax\n\tincl %eax\n\tdecl %eax\n" + // 1-6
		"\txorl $0xF, %eax\n\tshll %eax\n\tmovl %eax, %ecx\n\twrmsr\n" + // 7-10
		"\tmovw $0x1234, %ax\n\tmovb $0x56, %ah\n\tmovb $0x78, %al\n\tint $0x21\n" + // 11-14
		"\tmovl $5, %ebx\n\txchgl %ebx, %eax\n\tmovl %eax, %ecx\n\tcpuid\n" + // 15-18
		"\tmovl $3, %eax\n\tsetz %al\n\tmovl %eax, %ecx\n\tcpuid\n" + // 19-22
		"\tmovl $3, %eax\n\tleal (%ebx), %ecx\n\tmovl %ecx, %eax\n\tcpuid\n" + // 23-26
		"\tmovl $3, %eax\n\tlock incl (%ebx)\n\trep movsb\n\tcpuid\n" + // 27-30
		"\tmovl $3, %eax\n\tmull %ebx\n\tcpuid\n" + // 31-33
		"\tmovl $3, %eax\n\taddl %ebx, %eax\n\tcpuid\n" + // 34-36
		"\tmovl $3, %eax\n\tandl %ebx, %eax\n\torl $0x80, %eax\n\tcpuid\n" + // 37-40
		"\tmovl $1, %ecx\n\tmovl (%ebx), %eax\n\tcmpl $1, %eax\n\tjne 1f\n\tmovl %ecx, %eax\n\tcpuid\n" + // 41-46
		"1:\tmovl $1, %eax\n\t.code32\n\tcpuid\n" + // 47-49
		"\tmovl $1, %eax\n\tcall f\n\tcpuid\n" // 50-52
	ix, ev := eventsOf(t, text, asm.Mode32)
	// ((0x10+2-1)<<4>>1 +1 -1) ^ 0xF, then << 1
	want := uint64((((0x10+2-1)<<4)>>1)^0xF) << 1
	if e := ev[10]; !e.c.full(32) || e.c.val != want {
		t.Fatalf("wrmsr ecx = %+v, want %#x", e.c, want)
	}
	if e := ev[14]; e.a.val&0xFFFF != 0x5678 || e.a.known&0xFFFF != 0xFFFF {
		t.Fatalf("int ax = %+v", e.a)
	}
	if e := ev[18]; e.a.known != 0 && e.a.full(32) || e.c.full(32) {
		t.Fatalf("after xchg = %+v", e)
	}
	if e := ev[22]; e.c.full(32) {
		t.Fatalf("setz leaves eax unknown in al: %+v", e.c)
	}
	if e := ev[26]; e.a.full(32) {
		t.Fatalf("lea: %+v", e.a)
	}
	if e := ev[30]; e.a.full(32) {
		t.Fatalf("rep movsb forgets: %+v", e.a)
	}
	if e := ev[33]; e.a.full(32) {
		t.Fatalf("mul forgets: %+v", e.a)
	}
	if e := ev[36]; e.a.full(32) {
		t.Fatalf("add of unknown: %+v", e.a)
	}
	if e := ev[40]; e.a.full(32) || e.a.set() != 0x80 || e.a.known != 0x80 {
		t.Fatalf("or on unknown: %+v", e.a)
	}
	if e := ev[46]; !e.a.full(32) || e.a.val != 1 {
		t.Fatalf("jne falls through with values: %+v", e.a)
	}
	// A mode directive emits nothing; registers survive it.
	if e := ev[49]; !e.a.full(32) {
		t.Fatalf(".code32 directive: %+v", e.a)
	}
	if e := ev[52]; e.a.full(32) {
		t.Fatalf("call resets: %+v", e.a)
	}
	_ = ix
}

func TestTrackerSymbols(t *testing.T) {
	text := "\tleaw buf, %di\n\tmovl $0xE820, %eax\n\tint $0x15\n" +
		"\tmovw $buf, %si\n\tmovw %si, %di\n\tmovl $0xE820, %eax\n\tint $0x15\n" +
		"buf:\t.space 20\n"
	if got := run(t, text, opts{}, "bios-struct"); len(got) != 0 {
		t.Fatal(got)
	}
	short := strings.Replace(text, ".space 20", ".space 12", 1)
	if got := run(t, short, opts{}, "bios-struct"); len(got) != 1 {
		t.Fatalf("dedup of the same buffer: %v", got)
	}
}

// 64-bit IDT gates and TSS descriptors take 16 bytes.
func TestLongModeTables(t *testing.T) {
	text := "\t.code64\n\tlgdt gdtr\n\tlidt idtr\n" +
		"gdt:\t.quad 0\n\t.quad 0x00AF9A000000FFFF\n\t.quad 0x00008B0000000067\n\t.quad 0\n\t.quad 0x00CF92000000FFFF\ngdt_end:\n" +
		"gdtr:\t.word gdt_end - gdt - 1\n\t.quad gdt\n" +
		"idt:\t.quad 0x00008E0000081000\n\t.quad 0\nidt_end:\n" +
		"idtr:\t.word idt_end - idt - 1\n\t.quad idt\n" +
		"\tljmp *(%rax)\n\tmovw $0x20, %ax\n\tmovw %ax, %ss\n"
	ix := NewIndex(prog(text, asm.Mode64))
	g := ix.GDT()
	if g == nil || len(g.Entries) != 4 || g.Entries[2].Size != 16 || g.Entries[3].Index != 4 {
		t.Fatalf("gdt = %+v", g)
	}
	md, _, _ := ix.Hover(hwdb.Default(), strings.Index(text, "0x00008B"))
	if !strings.Contains(md, "GDT entry 2") {
		t.Fatalf("tss hover = %q", md)
	}
	md, _, _ = ix.Hover(hwdb.Default(), strings.Index(text, "0x00008E"))
	if !strings.Contains(md, "IDT vector 0") {
		t.Fatalf("gate hover = %q", md)
	}
	md, _, _ = ix.Hover(hwdb.Default(), strings.Index(text, "$0x20"))
	if !strings.Contains(md, "GDT entry 4") {
		t.Fatalf("selector hover = %q", md)
	}
	if got := run(t, text, opts{mode: asm.Mode64}, gdtRules...); len(got) != 0 {
		t.Fatal(got)
	}
	bad := strings.Replace(text, "idt_end - idt - 1", "idt_end - idt - 9", 1)
	mustHave(t, run(t, bad, opts{mode: asm.Mode64}, "gdt-limit"), "gdt-limit", "multiple of 16")
}

func TestSeedAndMemo(t *testing.T) {
	p := prog(fixture(t, "pmode.S"), asm.Mode16)
	ix := NewIndex(p)
	m := lint.NewMemo()
	Seed(m, ix)
	r, _ := lint.Lookup("far-jump-mode")
	if got := lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Memo: m, Rules: []*lint.Rule{r}}); len(got) != 0 {
		t.Fatal(got)
	}
	// A program with no document gives no index.
	if got := lint.Run(lint.Input{Prog: &asm.Program{}, Lang: lint.LangAsm, Rules: []*lint.Rule{r}}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSelectorHoverVariants(t *testing.T) {
	text := fixture(t, "pmode.S") + "\t.code32\nx:\n\tmovw $0x0C, %ax\n\tmovw %ax, %fs\n\tmovw $0x80, %ax\n\tmovw %ax, %gs\n"
	ix := NewIndex(prog(text, asm.Mode16))
	md, _, _ := ix.Hover(hwdb.Default(), strings.Index(text, "$0x0C"))
	if !strings.Contains(md, "LDT") {
		t.Fatalf("ldt = %q", md)
	}
	md, _, _ = ix.Hover(hwdb.Default(), strings.Index(text, "$0x80"))
	if !strings.Contains(md, "beyond") {
		t.Fatalf("beyond = %q", md)
	}
	mustHave(t, run(t, text, opts{}, "selector-type"), "selector-type", "0x80")
}
