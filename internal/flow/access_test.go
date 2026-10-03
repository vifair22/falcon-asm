package flow

import (
	"sort"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

func TestReads(t *testing.T) {
	cases := []struct {
		mode asm.Mode
		src  string
		want string // register parts read, sorted
	}{
		{asm.Mode16, "movw %ax, %bx", "ax"},
		{asm.Mode16, "addw %ax, %bx", "ax bx"},
		{asm.Mode16, "xorw %ax, %ax", ""},
		{asm.Mode16, "movw (%bx,%si), %ax", "bx si"},
		{asm.Mode16, "popw %ax", ""},
		{asm.Mode16, "pushw %ax", "ax"},
		{asm.Mode16, "cmpb %ah, %al", "ah al"},
		{asm.Mode16, "rep movsb", "cx di si"},
		{asm.Mode32, "rep stosl", "eax ecx edi"},
		{asm.Mode16, "stosb", "al di"},
		{asm.Mode16, "lodsw", "si"},
		{asm.Mode16, "scasw", "ax di"},
		{asm.Mode16, "insb", "di dx"},
		{asm.Mode16, "outsb", "dx si"},
		{asm.Mode16, "loop 1f\n1:", "cx"},
		{asm.Mode16, "jcxz 1f\n1:", "cx"},
		{asm.Mode32, "jecxz 1f\n1:", "ecx"},
		{asm.Mode64, "jrcxz 1f\n1:", "rcx"},
		{asm.Mode16, "mulb %bl", "al bl"},
		{asm.Mode16, "divb %bl", "ax bl"},
		{asm.Mode16, "divw %bx", "ax bx dx"},
		{asm.Mode32, "imull %ecx", "eax ecx"},
		{asm.Mode32, "imull $3, %ecx, %eax", "ecx"},
		{asm.Mode16, "cbw", "al"},
		{asm.Mode16, "cwd", "ax"},
		{asm.Mode32, "cltd", "eax"},
		{asm.Mode16, "xlat", "al bx"},
		{asm.Mode32, "rdmsr", "ecx"},
		{asm.Mode32, "wrmsr", "eax ecx edx"},
		{asm.Mode16, "sahf", "ah"},
		{asm.Mode16, "leave", "bp"},
		{asm.Mode16, "outb %al, %dx", "al dx"},
		{asm.Mode16, "inb %dx, %al", "dx"},
		{asm.Mode16, "leaw 2(%bx), %ax", "bx"},
		{asm.Mode64, "movq (%rip), %rax", ""},
		{asm.Mode16, "movw %ax, %ds", "ax"},
		{asm.Mode16, "addr32 rep movsb", "ecx edi esi"},
		{asm.Mode32, "addr16 lodsb", "si"},
		{asm.Mode64, "addr32 stosb", "al edi"},
	}
	for _, c := range cases {
		p := parse(t, "\t"+c.src+"\n", c.mode)
		var names []string
		for _, u := range Reads(p, 0) {
			names = append(names, u.Name())
		}
		sort.Strings(names)
		if got := strings.Join(names, " "); got != c.want {
			t.Errorf("%s: reads %q, want %q", c.src, got, c.want)
		}
	}
	// Not an instruction.
	if got := Reads(parse(t, "x:\n", asm.Mode16), 0); len(got) != 0 {
		t.Errorf("label reads %v", got)
	}
	// High-byte names and widths.
	u := Access{Reg: RDX, Lo: 8, Hi: 15}
	if u.Name() != "dh" || u.Width() != 8 || u.Bits() != 0xFF00 {
		t.Errorf("dh: %s %d %#x", u.Name(), u.Width(), u.Bits())
	}
	if u := (Access{Reg: RCX, Lo: 8, Hi: 15}); u.Name() != "ch" {
		t.Errorf("ch: %s", u.Name())
	}
	if u := (Access{Reg: RBX, Lo: 8, Hi: 15}); u.Name() != "bh" {
		t.Errorf("bh: %s", u.Name())
	}
	if u := (Access{Reg: RAX, Lo: 0, Hi: 63}); u.Bits() != ^uint64(0) {
		t.Errorf("rax bits %#x", u.Bits())
	}
}

// TestReadsMerged covers the entries the cc package's model contributed
// when the two read models were merged, and the lone prefix statement.
func TestReadsMerged(t *testing.T) {
	cases := []struct {
		mode asm.Mode
		src  string
		want string
	}{
		{asm.Mode64, "cqto", "rax"},
		{asm.Mode32, "cpuid", "eax"},
		{asm.Mode32, "xgetbv", "ecx"},
		{asm.Mode32, "xsetbv", "eax ecx edx"},
		{asm.Mode32, "cmpxchgl %ebx, (%esi)", "eax ebx esi"},
		{asm.Mode32, "cmpxchg8b (%esi)", "eax ebx ecx edx esi"},
		{asm.Mode64, "cmpxchg16b (%rsi)", "rax rbx rcx rdx rsi"},
		{asm.Mode64, "cmovzq %rcx, %rdx", "rcx rdx"},
		{asm.Mode64, "setz %al", ""},
		{asm.Mode64, "call *%rax", "rax"},
		{asm.Mode64, "xorl %EAX, %eax", ""},
		{asm.Mode64, "xlatb", "al rbx"},
		{asm.Mode16, "mul (%bx)", "bx"},
		{asm.Mode16, "rep\n\tmovsb", "cx di si"},
	}
	for _, c := range cases {
		p := parse(t, "\t"+c.src+"\n", c.mode)
		i := len(p.Stmts) - 1
		for i > 0 && p.Stmts[i].Kind != asm.Instr {
			i--
		}
		var names []string
		for _, u := range Reads(p, i) {
			names = append(names, u.Name())
		}
		sort.Strings(names)
		if got := strings.Join(names, " "); got != c.want {
			t.Errorf("%s: reads %q, want %q", c.src, got, c.want)
		}
	}
}

func TestDestAccess(t *testing.T) {
	cases := []struct {
		src         string
		read, write bool
	}{
		{"movq %rax, %rbx", false, true},
		{"addq %rax, %rbx", true, true},
		{"cmpq %rax, %rbx", true, false},
		{"imulq $3, %rcx, %rdx", false, true},
		{"imulq %rcx, %rdx", true, true},
		{"mulq %rcx", true, false},
		{"jmp *%rax", true, false},
		{"lmsw %ax", true, false},
		{"popq %rax", false, true},
		{"incq %rax", true, true},
		{"nop", false, false},
		{"vpcmpestri $0, %xmm1, %xmm2", true, false},
	}
	for _, c := range cases {
		p := parse(t, "\t"+c.src+"\n", asm.Mode64)
		st := &p.Stmts[0]
		if r, w := DestRead(st), DestWritten(st); r != c.read || w != c.write {
			t.Errorf("%s: read %v written %v, want %v %v", c.src, r, w, c.read, c.write)
		}
	}
	label := &asm.Stmt{Kind: asm.Label}
	if DestRead(label) || DestWritten(label) {
		t.Error("label")
	}
}
