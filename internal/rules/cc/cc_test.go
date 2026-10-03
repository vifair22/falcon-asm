package cc

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// lines joins source lines with newlines, so a test's expected line
// numbers can be read off the slice.
func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func TestCalleeSaved(t *testing.T) {
	cases := []struct {
		name string
		mode asm.Mode
		cc   string
		text string
		want string
		frag []string
	}{
		{"sysv rbx written", 64, "sysv", lines(
			"f:",
			"\tmovq $1, %rbx",
			"\tret"), "cc-callee-saved@3", []string{"%rbx is callee-saved under sysv", "written on line 2"}},
		{"sysv saved", 64, "sysv", lines(
			"f:",
			"\tpushq %rbx",
			"\tmovq $1, %rbx",
			"\tpopq %rbx",
			"\tret"), "", nil},
		{"sysv one path", 64, "sysv", lines(
			"f:",
			"\ttestq %rdi, %rdi",
			"\tjz 1f",
			"\tmovq $1, %r12",
			"1:\tret"), "cc-callee-saved@5", []string{"%r12", "line 4"}},
		{"sysv caller-saved is free", 64, "sysv", lines(
			"f:",
			"\tmovq $1, %rcx",
			"\tmovq $1, %rdi",
			"\tret"), "", nil},
		{"cdecl pusha", 32, "cdecl", lines(
			"f:",
			"\tpusha",
			"\tmovl $1, %ebx",
			"\tmovl $1, %esi",
			"\tpopa",
			"\tret"), "", nil},
		{"cdecl frame", 32, "cdecl", lines(
			"f:",
			"\tpushl %ebp",
			"\tmovl %esp, %ebp",
			"\tsubl $8, %esp",
			"\tleave",
			"\tret"), "", nil},
		{"cdecl frame pointer clobbered", 32, "cdecl", lines(
			"f:",
			"\tmovl %esp, %ebp",
			"\tret"), "cc-callee-saved@3", []string{"%ebp"}},
		{"spill slot", 64, "sysv", lines(
			"f:",
			"\tsubq $24, %rsp",
			"\tmovq %rbx, 8(%rsp)",
			"\tmovq $5, %rbx",
			"\tmovq 8(%rsp), %rbx",
			"\taddq $24, %rsp",
			"\tret"), "", nil},
		{"pop on an unknown stack", 32, "cdecl", lines(
			"f:",
			"\tpushl %ebx",
			"\tandl $-16, %esp",
			"\tmovl $1, %ebx",
			"\tpopl %ebx",
			"\tret"), "", nil},
		{"external call preserves", 32, "cdecl", lines(
			"f:",
			"\tcall g",
			"\tret"), "", nil},
		{"callee breaks it, caller clean", 32, "cdecl", lines(
			"f:",
			"\tcall g",
			"\tret",
			"g:",
			"\tmovl $1, %esi",
			"\tret"), "cc-callee-saved@6", []string{"g returns here"}},
		{"none helper clobbers a preserved register", 16, "", lines(
			"# falcon-asm: cc=custom preserves=bx returns=ax",
			"f:",
			"\tcall helper",
			"\tret",
			"helper:",
			"\tmovw $1, %bx",
			"\tret"), "cc-callee-saved@4", []string{"%bx is callee-saved under custom", "line 3"}},
		{"macro makes it unknown", 32, "cdecl", lines(
			"f:",
			"\ttestl %eax, %eax",
			"\tjz 1f",
			"\tSOMETHING",
			"1:\tret"), "", nil},
		{"ms xmm6", 64, "ms", lines(
			"f:",
			"\tpxor %xmm0, %xmm6",
			"\tret"), "cc-callee-saved@2", []string{"%xmm6 is callee-saved under ms"}},
		{"ms xmm6 saved", 64, "ms", lines(
			"f:",
			"\tsubq $24, %rsp",
			"\tmovaps %xmm6, (%rsp)",
			"\tpxor %xmm0, %xmm6",
			"\tmovaps (%rsp), %xmm6",
			"\taddq $24, %rsp",
			"\tret"), "", nil},
		{"ms vzeroall", 64, "ms", lines(
			"f:",
			"\tvzeroall",
			"\tucomiss %xmm0, %xmm7",
			"\tret"), "cc-callee-saved@2 cc-callee-saved@2 cc-callee-saved@2 cc-callee-saved@2 cc-callee-saved@2 " +
			"cc-callee-saved@2 cc-callee-saved@2 cc-callee-saved@2 cc-callee-saved@2 cc-callee-saved@2", nil},
		{"mxcsr", 64, "sysv", lines(
			"f:",
			"\tldmxcsr (%rdi)",
			"\tfldcw (%rsi)",
			"\tret"), "cc-callee-saved@2 cc-callee-saved@3", []string{"MXCSR", "x87 control word"}},
		{"mxcsr saved", 64, "sysv", lines(
			"f:",
			"\tstmxcsr -8(%rsp)",
			"\tldmxcsr (%rdi)",
			"\tfnstcw -16(%rsp)",
			"\tfldcw (%rsi)",
			"\tret"), "", nil},
		{"custom preserves all", 16, "", lines(
			"# falcon-asm: cc=custom clobbers=ax preserves=all",
			"f:",
			"\tmovw $1, %ax",
			"\tmovw $1, %dx",
			"\tret"), "cc-callee-saved@5", []string{"%dx"}},
		{"stdcall", 32, "stdcall", lines(
			"f:",
			"\tmovl $1, %edi",
			"\tret $8"), "cc-callee-saved@3", []string{"stdcall"}},
		{"16-bit is none by default", 16, "cdecl", lines(
			"f:",
			"\tmovw $1, %bx",
			"\tret"), "", nil},
		{"wrong width convention", 32, "sysv", lines(
			"f:",
			"\tmovl $1, %ebx",
			"\tret"), "", nil},
		{"no convention", 32, "", lines(
			"f:",
			"\tmovl $1, %ebx",
			"\tret"), "", nil},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, tc.mode, tc.cc, CodeCalleeSaved), tc.want, tc.frag...)
	}
	got := run(t, lines("f:", "\tmovq $1, %rbx", "\tret"), 64, "sysv", CodeCalleeSaved)
	if len(got) != 1 || len(got[0].Related) != 1 || got[0].Related[0].Range.Start.Line != 1 {
		t.Errorf("related: %+v", got)
	}
}

func TestCallerSavedRead(t *testing.T) {
	cases := []struct {
		name string
		mode asm.Mode
		cc   string
		text string
		want string
		frag []string
	}{
		{"cdecl ecx", 32, "cdecl", lines(
			"f:",
			"\tmovl $10, %ecx",
			"\tcall putc",
			"\tdecl %ecx",
			"\tret"), "cc-caller-saved-read@4", []string{"%ecx is read here", "call to putc on line 3", "cdecl"}},
		{"cdecl result", 32, "cdecl", lines(
			"f:",
			"\tcall getc",
			"\tmovl %eax, x",
			"\tmovl %edx, y",
			"\tret"), "", nil},
		{"reloaded", 32, "cdecl", lines(
			"f:",
			"\tcall putc",
			"\tmovl $3, %ecx",
			"\tdecl %ecx",
			"\tret"), "", nil},
		{"sysv rdi and rbx", 64, "sysv", lines(
			"f:",
			"\tcall g",
			"\tmovq (%rdi), %rax",
			"\tmovq (%rbx), %rax",
			"\tret"), "cc-caller-saved-read@3", []string{"%rdi"}},
		{"ms rdx", 64, "ms", lines(
			"f:",
			"\tcall g",
			"\taddq %rdx, %rax",
			"\tret"), "cc-caller-saved-read@3", []string{"%rdx", "ms"}},
		{"in-document callee leaves it", 32, "cdecl", lines(
			"f:",
			"\tmovl $1, %ecx",
			"\tcall g",
			"\tdecl %ecx",
			"\tret",
			"g:",
			"\tmovl $0, %eax",
			"\tret"), "", nil},
		{"custom callee clobbers", 16, "", lines(
			"f:",
			"\tcall g",
			"\tdecw %cx",
			"\tret",
			"# falcon-asm: cc=custom clobbers=cx returns=ax",
			"g:",
			"\tmovw $0, %cx",
			"\tmovw $0, %ax",
			"\tret"), "cc-caller-saved-read@3", []string{"custom"}},
		{"16-bit external", 16, "cdecl", lines(
			"f:",
			"\tcall g",
			"\tdecw %cx",
			"\tret"), "", nil},
		{"implicit read", 32, "cdecl", lines(
			"f:",
			"\tcall g",
			"\trep movsb",
			"\tret"), "cc-caller-saved-read@3", []string{"%ecx"}},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, tc.mode, tc.cc, CodeCallerSaved), tc.want, tc.frag...)
	}
}

func TestStackAlignShadowRedZone(t *testing.T) {
	cases := []struct {
		name string
		cc   string
		text string
		want string
		frag []string
	}{
		{"sysv misaligned", "sysv", lines(
			"\t.globl f",
			"f:",
			"\tcall g",
			"\tret"), "cc-stack-align@3", []string{"8 bytes off", "sysv"}},
		{"sysv push aligns", "sysv", lines(
			"\t.globl f",
			"f:",
			"\tpushq %rbp",
			"\tcall g",
			"\tpopq %rbp",
			"\tret"), "", nil},
		{"called function", "sysv", lines(
			"main:",
			"\tsubq $8, %rsp",
			"\tcall f",
			"\taddq $8, %rsp",
			"\tret",
			"f:",
			"\tsubq $16, %rsp",
			"\tcall g",
			"\taddq $16, %rsp",
			"\tret"), "cc-stack-align@8", nil},
		{"realigned", "sysv", lines(
			"\t.globl f",
			"f:",
			"\tandq $-16, %rsp",
			"\tcall g",
			"\tret"), "", nil},
		{"not entered", "sysv", lines(
			"f:",
			"\tcall g",
			"\tret"), "", nil},
		{"program entry", "sysv", lines(
			"\t.globl _start",
			"_start:",
			"\tcall main",
			"\thlt"), "", nil},
		{"callee without convention", "sysv", lines(
			"\t.globl f",
			"f:",
			"\tcall g",
			"\tret",
			"# falcon-asm: cc=none",
			"g:",
			"\tret"), "", nil},
		{"ms shadow", "ms", lines(
			"\t.globl f",
			"f:",
			"\tsubq $40, %rsp",
			"\tcall g",
			"\taddq $40, %rsp",
			"\tret"), "", nil},
		{"ms shadow missing", "ms", lines(
			"\t.globl f",
			"f:",
			"\tsubq $8, %rsp",
			"\tcall g",
			"\taddq $8, %rsp",
			"\tret"), "cc-shadow-space@4", []string{"only 8 bytes", "32 bytes of shadow space"}},
		{"ms arg pushed after shadow", "ms", lines(
			"\t.globl f",
			"f:",
			"\tsubq $16, %rsp",
			"\tpushq $5",
			"\tsubq $16, %rsp",
			"\tcall g",
			"\taddq $40, %rsp",
			"\tret"), "cc-shadow-space@6", nil},
		{"sysv red zone", "sysv", lines(
			"f:",
			"\tmovq %rdi, -8(%rsp)",
			"\tmovq %rdi, -136(%rsp)",
			"\tleaq -200(%rsp), %rax",
			"\tmovq 8(%rsp), %rax",
			"\tret"), "cc-red-zone@3", []string{"136 bytes below rsp", "128-byte red zone"}},
		{"ms red zone", "ms", lines(
			"f:",
			"\tmovq %rcx, -8(%rsp)",
			"\tret"), "cc-red-zone@2", []string{"ms has no red zone"}},
		{"frame align rbp", "sysv", lines(
			"\t.globl f",
			"f:",
			"\tpushq %rbp",
			"\tmovq %rsp, %rbp",
			"\tsubq $32, %rsp",
			"\tmovaps %xmm0, -16(%rbp)",
			"\tmovaps %xmm0, -8(%rbp)",
			"\tmovdqa %xmm0, 8(%rsp)",
			"\tmovdqa %xmm0, (%rsp)",
			"\tleave",
			"\tret"), "stack-frame-align@7 stack-frame-align@8", []string{"rbp is 8 bytes below", "movdqa"}},
		{"frame align unknown", "sysv", lines(
			"\t.globl f",
			"f:",
			"\tandq $-16, %rsp",
			"\tmovaps %xmm0, 8(%rsp)",
			"\tret"), "", nil},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, 64, tc.cc, CodeStackAlign, CodeShadowSpace, CodeRedZone, CodeFrameAlign),
			tc.want, tc.frag...)
	}
	// The i386 conventions promise no alignment the rules check.
	got := run(t, lines("\t.globl f", "f:", "\tcall g", "\tmovaps %xmm0, 4(%esp)", "\tret"), 32, "cdecl")
	expect(t, "cdecl", got, "")
}

func TestArgAndReturnUnset(t *testing.T) {
	cases := []struct {
		name string
		mode asm.Mode
		text string
		want string
		frag []string
	}{
		{"some path", 32, lines(
			"# falcon-asm: cc=cdecl",
			"",
			"# falcon-asm: cc=fastcall",
			"add2:",
			"\tleal (%ecx,%edx), %eax",
			"\tret",
			"main:",
			"\tmovl $1, %ecx",
			"\ttestl %eax, %eax",
			"\tjz 1f",
			"\tmovl $2, %edx",
			"1:\tcall add2",
			"\tret"), "cc-arg-unset@12", []string{"add2 reads %edx as an argument (line 5)"}},
		{"caller passes its own", 64, lines(
			"# falcon-asm: cc=sysv",
			"",
			"f:",
			"\tmovq (%rdi), %rax",
			"\tret",
			"g:",
			"\tcall f",
			"\tret"), "", nil},
		{"push is not a use", 16, lines(
			"# falcon-asm: cc=custom args=ax,bx preserves=bx returns=ax",
			"",
			"f:",
			"\tpushw %bx",
			"\tincw %ax",
			"\tpopw %bx",
			"\tret",
			"g:",
			"\tmovw $1, %ax",
			"\tcall f",
			"\tret"), "", nil},
		{"none caller", 16, lines(
			"# falcon-asm: cc=custom args=si returns=ax",
			"puts:",
			"\tlodsb",
			"\tret",
			"main:",
			"\tcall puts",
			"\thlt"), "", nil},
		{"return evidence", 32, lines(
			"# falcon-asm: cc=cdecl",
			"",
			"get:",
			"\ttestl %ecx, %ecx",
			"\tjz 1f",
			"\tmovl $1, %eax",
			"1:\tret",
			"main:",
			"\tcall get",
			"\tmovl %eax, result",
			"\tret"), "cc-return-unset@7", []string{"get can return here without setting %eax", "line 10"}},
		{"no caller reads", 32, lines(
			"# falcon-asm: cc=cdecl",
			"",
			"get:",
			"\tret",
			"main:",
			"\tcall get",
			"\tmovl $0, %eax",
			"\tret"), "", nil},
		{"set through a call", 32, lines(
			"# falcon-asm: cc=cdecl",
			"",
			"get:",
			"\tcall inner",
			"\tret",
			"inner:",
			"\tmovl $1, %eax",
			"\tret",
			"main:",
			"\tcall get",
			"\tmovl %eax, result",
			"\tret"), "", nil},
		{"evidence past a branch", 32, lines(
			"# falcon-asm: cc=cdecl",
			"",
			"get:",
			"\tret",
			"main:",
			"\tcall get",
			"\tjmp 1f",
			"1:\tpushl %eax",
			"\tret"), "cc-return-unset@4", nil},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, tc.mode, "", CodeArgUnset, CodeReturnUnset), tc.want, tc.frag...)
	}
}

func TestHandlers(t *testing.T) {
	cases := []struct {
		name string
		mode asm.Mode
		cc   string
		text string
		want string
		frag []string
	}{
		{"ivt ret", 16, "", lines(
			"\tmovw $tick, 0x20",
			"\tret",
			"tick:",
			"\tincw ticks",
			"\tret"), "handler-ret@5", []string{"interrupt vector table", "flags and code segment"}},
		{"ivt service lret $2", 16, "", lines(
			"\tmovw $(RELOC + (int13 - start)), (0x13 * 4)",
			"\tret",
			"start:",
			"int13:",
			"\tmovw $0, %ax",
			"\tlret $2"), "", nil},
		{"ivt service plain lret", 16, "", lines(
			"\tmovw $int13, 0x4c",
			"\tret",
			"int13:",
			"\tmovw $0, %ax",
			"\tlret"), "handler-ret@5", []string{"leaves the interrupted code's flags on"}},
		{"irq handler preserves", 16, "", lines(
			"\tmovw $tick, 0x20",
			"\tret",
			"tick:",
			"\tpusha",
			"\tmovw $1, %ax",
			"\tmovw $2, %dx",
			"\tpopa",
			"\tiret"), "", nil},
		{"irq handler clobbers", 16, "", lines(
			"\tmovw $tick, 0x20",
			"\tret",
			"tick:",
			"\tmovw $1, %ax",
			"\tiret"), "handler-preserve@5", []string{"%ax changed", "line 4"}},
		{"handler calls C", 32, "cdecl", lines(
			"# falcon-asm: handler vector=33",
			"kbd:",
			"\tpushl %ebx",
			"\tcall kbd_isr",
			"\tpopl %ebx",
			"\tiret"), "handler-preserve@6 handler-preserve@6 handler-preserve@6", []string{"%eax", "%ecx", "%edx"}},
		{"declared service", 32, "", lines(
			"# falcon-asm: handler=service",
			"svc:",
			"\tmovl $1, %eax",
			"\tiret"), "", nil},
		{"idt table", 32, "", lines(
			"setup:",
			"\tmovl $isr_table, %esi",
			"\tlidt idtr",
			"\tret",
			"isr_table:",
			"\t.long 0, 0, 0, 0, 0, 0, 0, 0",
			"\t.long df, nm",
			"df:",
			"\tiret",
			"nm:",
			"\taddl $4, %esp",
			"\tiret",
			"idtr:",
			"\t.word 0"), "handler-errcode@9 handler-errcode@12", []string{"vector 8", "vector 9", "discards 4 bytes"}},
		{"idt stubs", 32, "", lines(
			"install:",
			"\tmovl $stubs, %eax",
			"\tlidt idtr",
			"\tret",
			"stubs:",
			"\tmovl $1, %ecx",
			"\tret",
			"idtr:",
			"\t.word 0"), "handler-preserve@7 handler-ret@7", []string{"installed in an IDT"}},
		{"64-bit errcode", 64, "", lines(
			"# falcon-asm: handler vector=14",
			"pf:",
			"\tiretq",
			"# falcon-asm: handler=errcode",
			"gp:",
			"\taddq $8, %rsp",
			"\tiretq"), "handler-errcode@3", []string{"addq $8, %rsp"}},
		{"unknown vector", 32, "", lines(
			"# falcon-asm: handler",
			"h:",
			"\tiret"), "", nil},
		{"push of a label is not an install", 32, "", lines(
			"switch:",
			"\tpushl $back",
			"\tlidt idtr",
			"\tlret",
			"back:",
			"\tret",
			"idtr:",
			"\t.word 0"), "", nil},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, tc.mode, tc.cc, CodeHandlerRet, CodeHandlerKeep, CodeHandlerErrcode,
			CodeCalleeSaved), tc.want, tc.frag...)
	}
	// The fixes: iret (iretq in 64-bit code) and the error code discard.
	got := run(t, lines("# falcon-asm: handler", "h:", "\tret"), 64, "", CodeHandlerRet)
	if len(got) != 1 || len(got[0].Fixes) != 1 || got[0].Fixes[0].Edits[0].NewText != "iretq" {
		t.Errorf("iretq fix: %+v", got)
	}
	got = run(t, lines("# falcon-asm: handler vector=13", "h:", "\tiret"), 32, "", CodeHandlerErrcode)
	if len(got) != 1 || len(got[0].Fixes) != 1 || got[0].Fixes[0].Edits[0].NewText != "\taddl $4, %esp\n" {
		t.Errorf("discard fix: %+v", got)
	}
}

func TestAnnotations(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
		frag string
	}{
		{"unknown cc", "# falcon-asm: cc=pascal\nf:\n\tret\n", "cc-annotation@1", "unknown calling convention \"pascal\""},
		{"unknown register", "# falcon-asm: cc=custom clobbers=ax,qq\nf:\n\tret\n", "cc-annotation@1", "unknown register \"qq\""},
		{"lists need custom", "# falcon-asm: clobbers=ax\nf:\n\tret\n", "cc-annotation@1", "need cc=custom"},
		{"lists on sysv", "# falcon-asm: cc=sysv clobbers=ax\nf:\n\tret\n", "cc-annotation@1", "apply only to cc=custom"},
		{"empty custom", "# falcon-asm: cc=custom\nf:\n\tret\n", "cc-annotation@1", "declares nothing"},
		{"both", "# falcon-asm: cc=custom clobbers=ax preserves=ax\nf:\n\tret\n", "cc-annotation@1", "both clobbered and preserved"},
		{"bad vector", "# falcon-asm: handler vector=300\nf:\n\tiret\n", "cc-annotation@1", "invalid vector"},
		{"bad kind", "# falcon-asm: handler=maybe\nf:\n\tiret\n", "cc-annotation@1", "unknown handler kind"},
		{"not a function", "f:\n\tjmp 1f\n\tnop\n# falcon-asm: cc=cdecl\n1:\tret\n", "cc-annotation@4", "not a function start"},
		{"fine", "/* falcon-asm: cc=custom clobbers=ax preserves=bx args=si returns=ax */\nf:\n\tret\n", "", ""},
		{"other items ignored", "# falcon-asm: ignore=cc-red-zone some words\nf:\n\tret\n", "", ""},
	}
	for _, tc := range cases {
		var frags []string
		if tc.frag != "" {
			frags = []string{tc.frag}
		}
		expect(t, tc.name, run(t, tc.text, 16, "", CodeAnnotation), tc.want, frags...)
	}
}

// TestResolution covers which convention a function gets: a comment on
// its label line, above it across declarations, a file-level comment, the
// target, and the 16-bit and width rules.
func TestResolution(t *testing.T) {
	bx := func(head string) string {
		return head + "\tmovw $1, %bx\n\tret\n"
	}
	cases := []struct {
		name string
		text string
		mode asm.Mode
		cc   string
		want string
	}{
		{"label line", bx("f: # falcon-asm: cc=custom preserves=bx\n"), 16, "", "cc-callee-saved@3"},
		{"above with declarations", bx("# falcon-asm: cc=custom preserves=bx\n\t.globl f\n\t.type f, @function\nf:\n"),
			16, "", "cc-callee-saved@6"},
		{"file level, blank line", bx("# falcon-asm: cc=custom preserves=bx\n\nf:\n"), 16, "", "cc-callee-saved@5"},
		{"code between: file level", bx("# falcon-asm: cc=custom preserves=bx\n\tnop\nf:\n"), 16, "", "cc-callee-saved@5"},
		{"trailing comment on code is file level", "\tnop # falcon-asm: cc=custom preserves=bx\nf:\n\tmovw $1, %bx\n\tret\n",
			16, "", "cc-callee-saved@4"},
		{"explicit none", "# falcon-asm: cc=none\nf:\n\tmovl $1, %ebx\n\tret\n", 32, "cdecl", ""},
		{"file none", "# falcon-asm: cc=none\n\nf:\n\tmovl $1, %ebx\n\tret\n", 32, "cdecl", ""},
		{"file cdecl skips 16-bit", "# falcon-asm: cc=cdecl\n\nf:\n\tmovw $1, %bx\n\tret\n", 16, "", ""},
		{"explicit sysv on 32-bit", "# falcon-asm: cc=sysv\nf:\n\tmovl $1, %ebx\n\tret\n", 32, "", "cc-callee-saved@4"},
		{"regparm target", "f:\n\tmovl $1, %esi\n\tret\n", 32, "regparm(2)", "cc-callee-saved@3"},
		{"custom target declares nothing", "f:\n\tmovl $1, %esi\n\tret\n", 32, "custom", ""},
		{"unknown target", "f:\n\tmovl $1, %esi\n\tret\n", 32, "pascal", ""},
		{"code32 under a 64-bit target", "\t.code32\nf:\n\tmovl $1, %ebx\n\tret\n", 64, "sysv", ""},
	}
	for _, tc := range cases {
		expect(t, tc.name, run(t, tc.text, tc.mode, tc.cc, CodeCalleeSaved), tc.want)
	}
	got := run(t, "f:\n\tmovl $1, %esi\n\tret\n", 32, "cdecl", CodeCalleeSaved)
	if len(got) != 1 || !strings.Contains(got[0].Message, "cdecl") {
		t.Errorf("source: %v", got)
	}
}

// TestQuiet: inputs where nothing is known stay quiet, and the rules do
// not run without the flow (inline asm, macro expansions).
func TestQuiet(t *testing.T) {
	for _, text := range []string{
		"",
		"\t.data\nx:\t.long 0\n",
		"f:\n\tjmp *%eax\n",
		"\tcall *%eax\n\tmovl %ecx, %ebx\n\tret\n",
	} {
		if got := run(t, text, 32, ""); len(got) != 0 {
			t.Errorf("%q: %v", text, got)
		}
	}
	r, _ := lint.Lookup(CodeCalleeSaved)
	if r.Langs != lint.LangAsm || !r.NeedsFlow {
		t.Errorf("rule metadata: %+v", r)
	}
}

func TestHasMarker(t *testing.T) {
	for text, want := range map[string]bool{
		"# falcon-asm: cc=sysv":       true,
		"/* FALCON-ASM: cc=none */":   true,
		"// Falcon-Asm:handler":       true,
		"# a: b: c: no marker":        false,
		"# falcon-asm cc=sysv":        false,
		":falcon-asm:":                true,
		"":                            false,
		"# ends with falcon-as":       false,
		"# x:falcon-asm: after colon": true,
	} {
		if got := hasMarker(text); got != want {
			t.Errorf("hasMarker(%q) = %v", text, got)
		}
	}
}
