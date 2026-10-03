package cc

import (
	"sort"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

func TestReads(t *testing.T) {
	cases := []struct {
		mode asm.Mode
		text string
		want string
	}{
		{64, "movq %rax, %rbx", "rax"},
		{64, "movq (%rsi,%rdi,8), %rbx", "rsi rdi"},
		{64, "movq %rax, 8(%rbx)", "rax rbx"},
		{64, "addq %rax, %rbx", "rax rbx"},
		{64, "xorl %eax, %eax", ""},
		{64, "subq %rcx, %rcx", ""},
		{64, "xorl %ecx, %eax", "rcx rax"},
		{64, "cmpq $1, %rdx", "rdx"},
		{64, "cmovzq %rcx, %rdx", "rcx rdx"},
		{64, "imulq $3, %rcx, %rdx", "rcx"},
		{64, "incq %r8", "r8"},
		{64, "popq %rbx", ""},
		{64, "setz %al", ""},
		{64, "pushq %rbx", "rbx"},
		{64, "call *%rax", "rax"},
		{64, "leaq 8(%rsp), %rax", ""},
		{64, "mulq %rcx", "rcx rax"},
		{64, "divl %ecx", "rcx rax rdx"},
		{64, "cqto", "rax"},
		{64, "rep movsb", "rsi rdi rcx"},
		{64, "movsb", "rsi rdi"},
		{64, "rep stosb", "rdi rax rcx"},
		{64, "lodsb", "rsi"},
		{64, "outsb", "rsi rdx"},
		{32, "insb", "rdi rdx"},
		{32, "loop .", "rcx"},
		{64, "wrmsr", "rcx rax rdx"},
		{32, "pusha", ""},
		{32, "outb %al, %dx", "rax rdx"},
		{32, "inb %dx, %al", "rdx"},
		{64, "nop", ""},
	}
	for _, tc := range cases {
		doc := &docstore.Document{URI: "file:///r.S", Text: "\t" + tc.text + "\n"}
		p := asm.Parse(doc, lexer.Tokenize(doc.Text), asm.Config{InitialMode: tc.mode})
		at := -1
		for i := range p.Stmts {
			if p.Stmts[i].Kind == asm.Instr {
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("%q: no instruction", tc.text)
		}
		var got []string
		for _, u := range reads(p, at) {
			got = append(got, u.r.String())
		}
		want := strings.Fields(tc.want)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%q: %v, want %v", tc.text, got, want)
		}
	}
	if reads(&asm.Program{Stmts: []asm.Stmt{{Kind: asm.Label}}}, 0) != nil {
		t.Error("label")
	}
}

func TestHeuristics(t *testing.T) {
	cases := []struct {
		name string
		mode asm.Mode
		cc   string
		text string
		want string
	}{
		{"stack spill at a moved offset", 64, "sysv", lines(
			"f:",
			"\tmovq %rbx, -8(%rsp)",
			"\tsubq $16, %rsp",
			"\tmovq $1, %rbx",
			"\tmovq 8(%rsp), %rbx",
			"\taddq $16, %rsp",
			"\tret"), ""},
		{"store after the load is not a save", 32, "cdecl", lines(
			"f:",
			"\tmovl 4(%esi), %ebx",
			"\tmovl %ebx, 4(%esi)",
			"\tret"), "cc-callee-saved@4"},
		{"lret as a jump", 32, "cdecl", lines(
			"f:",
			"\tmovl $1, %ebx",
			"\tpushl $8",
			"\tpushl $g",
			"\tlret",
			"g:",
			"\thlt"), ""},
		{"push and ret as a jump", 32, "cdecl", lines(
			"f:",
			"\tmovl $1, %ebx",
			"\tpushl $g",
			"\tret",
			"g:",
			"\thlt"), ""},
		{"write before a bios call", 16, "", lines(
			"# falcon-asm: cc=custom preserves=bx",
			"f:",
			"\tmovw $7, %bx",
			"\tint $0x10",
			"\tret"), "cc-callee-saved@5"},
		{"bios call alone", 16, "", lines(
			"# falcon-asm: cc=custom preserves=bx",
			"f:",
			"\tint $0x10",
			"\tret"), ""},
		{"intra-function call", 64, "sysv", lines(
			"\t.globl f",
			"f:",
			"\tsubq $8, %rsp",
			"\tcall 1f",
			"\taddq $8, %rsp",
			"\tret",
			"1:\tcall g",
			"\tret"), ""},
		{"lea into an idt", 32, "", lines(
			"setup:",
			"\tleal isr, %eax",
			"\tlidt idtr",
			"\tret",
			"isr:",
			"\tret",
			"idtr:",
			"\t.word 0"), "handler-ret@6"},
		{"shared epilogue spill", 64, "sysv", lines(
			"\t.globl f",
			"f:",
			"\tsubq $24, %rsp",
			"\tmovq %rbx, (%rsp)",
			"\tmovq $1, %rbx",
			"\tjmp .Lout",
			"fix:",
			"\tmovq $2, %rax",
			".Lout:",
			"\tmovq (%rsp), %rbx",
			"\taddq $24, %rsp",
			"\tret",
			"\t.quad fix"), ""},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, tc.mode, tc.cc), tc.want)
	}
}
