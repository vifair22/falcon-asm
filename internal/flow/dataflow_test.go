package flow

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// valueAt asserts a register's known bits before the statement holding sub.
func wantValue(t *testing.T, r *Result, sub, reg string, v, mask uint64) {
	t.Helper()
	i := at(t, r.CFG.Prog, sub, 0)
	gv, gm, _ := r.ValueAt(i, reg)
	if gv != v || gm != mask {
		t.Fatalf("before %q: %s = %#x/%#x, want %#x/%#x", sub, reg, gv, gm, v, mask)
	}
}

func wantKnown(t *testing.T, r *Result, sub, reg string, v uint64) {
	t.Helper()
	i := at(t, r.CFG.Prog, sub, 0)
	got, ok := r.KnownAt(i, reg)
	if !ok || got != v {
		gv, gm, _ := r.ValueAt(i, reg)
		t.Fatalf("before %q: %s = %#x/%#x, want known %#x", sub, reg, gv, gm, v)
	}
}

func wantUnknown(t *testing.T, r *Result, sub, reg string) {
	t.Helper()
	i := at(t, r.CFG.Prog, sub, 0)
	if v, m, ok := r.ValueAt(i, reg); ok {
		t.Fatalf("before %q: %s = %#x/%#x, want unknown", sub, reg, v, m)
	}
}

func stateAt(t *testing.T, r *Result, sub string) *State {
	t.Helper()
	return r.Before(at(t, r.CFG.Prog, sub, 0))
}

func TestPmodeFixture(t *testing.T) {
	g, r := fixture(t, "pmode.S")
	wantKnown(t, r, "int     $0x15", "ax", 0x2401)
	wantKnown(t, r, "int     $0x15", "ah", 0x24)
	s := stateAt(t, r, "int     $0x15")
	if s.IF != Clear {
		t.Fatalf("IF after cli: %d", s.IF)
	}
	// The BIOS call's effect is unknown by default: IF and AX lost.
	if s := stateAt(t, r, "inb     $0x92"); s.IF != Clear {
		t.Fatalf("cli after int: IF %d", s.IF)
	}
	if _, _, ok := r.ValueAt(at(t, g.Prog, "cli", 1), "ax"); ok {
		t.Fatal("ax after the BIOS call")
	}
	// CR0.PE is known set at the far jump, whatever else CR0 holds.
	i := at(t, g.Prog, "ljmpl", 0)
	v, m, ok := r.ValueAt(i, "cr0")
	if !ok || m&1 == 0 || v&1 != 1 {
		t.Fatalf("cr0 %#x/%#x", v, m)
	}
	// Bits 9 and 10 of CR4 on the path that set them, but the join with
	// the path that skips the write loses them.
	wantValue(t, r, "movl    %eax, %cr4", "eax", 0x600, 0x600)
	if _, m, _ := r.ValueAt(i, "cr4"); m != 0 {
		t.Fatalf("cr4 after the join: mask %#x", m)
	}
	// The A20 probe pushes and pops DS/ES: balanced.
	if s := stateAt(t, r, "cmpw    $0x1234"); !s.StackKnown || s.Stack != 0 {
		t.Fatalf("stack %v %d", s.StackKnown, s.Stack)
	}
	wantKnown(t, r, "movw    %ax, %ds", "ax", 0)
	wantKnown(t, r, "movw    $0x1234, (0x0500)", "ds", 0)
	wantKnown(t, r, "movw    $0x1234, (0x0500)", "es", 0xFFFF)
	wantKnown(t, r, "cpuid", "eax", 1)
	leaf, lok, _, sok := r.CPUIDLeaf(at(t, g.Prog, "cpuid", 0))
	if !lok || leaf != 1 || sok {
		t.Fatalf("cpuid leaf %d %v %v", leaf, lok, sok)
	}
	if s := stateAt(t, r, "jmp     *pmode_entry"); s.Mode != asm.Mode32 || s.StackKnown {
		t.Fatalf("after mov $0x7C00, %%esp: mode %d stack %v", s.Mode, s.StackKnown)
	}
	wantKnown(t, r, "jmp     *pmode_entry", "esp", 0x7C00)
}

func TestThunkFixture(t *testing.T) {
	g, r := fixture(t, "thunk.S")
	ret := atExact(t, g.Prog, "ret", 0)
	s := r.Before(ret)
	if !s.Reachable || !s.StackKnown || s.Stack != 0 {
		t.Fatalf("thunk_bios ret: reachable %v stack %v/%d", s.Reachable, s.StackKnown, s.Stack)
	}
	// The callee-saved registers come back off the stack with the
	// values they had on entry.
	for _, reg := range []Reg{RBX, RSI, RDI, RBP} {
		if s.Get(reg).Def != DefEntry {
			t.Fatalf("%s Def %d at ret, want DefEntry", reg, s.Get(reg).Def)
		}
	}
	if s.Get(RAX).Def == DefEntry {
		t.Fatal("eax was written")
	}
	// Across the real-mode trip the stack depth is unknown...
	if s := stateAt(t, r, "int     $0x00"); s.StackKnown {
		t.Fatal("stack on the real-mode stack")
	}
	// ...and parking %esp in saved_esp brings it back.
	if s := stateAt(t, r, "lidt    idt_desc"); !s.StackKnown || s.Stack != 20 || len(s.Slots()) != 5 {
		t.Fatalf("after the reload: %v %d %d", s.StackKnown, s.Stack, len(s.Slots()))
	}
	// The first popfl is in the MARK32 macro body.
	popf := r.Before(at(t, g.Prog, "popfl", 1))
	if d, ok := popf.Pushed(RBX); !ok || d != 4 {
		t.Fatalf("ebx saved at depth %d %v", d, ok)
	}
	if top, ok := popf.Top(); !ok || !top.Flags || top.Width != 4 {
		t.Fatalf("top slot %+v", top)
	}
	// thunk_16 loads SEL_DATA16 (a macro the test has no value for);
	// thunk_real loads zero.
	if _, _, ok := r.ValueAt(at(t, g.Prog, "movw    %ax, %ds", 0), "ax"); ok {
		t.Fatal("SEL_DATA16 is unknown here")
	}
	if v, ok := r.KnownAt(at(t, g.Prog, "movw    %ax, %ds", 1), "ax"); !ok || v != 0 {
		t.Fatal("xorw %ax, %ax")
	}
	wantKnown(t, r, "lidt    ivt_desc", "sp", 0x7000)
	if s := stateAt(t, r, "real_int_op:"); s.IF != Set {
		t.Fatalf("IF before the BIOS int: %d", s.IF)
	}
	if s := r.Before(at(t, g.Prog, "pushfw", 1)); s.IF != Clear {
		t.Fatalf("IF after the cli following int: %d", s.IF)
	}
}

func TestCldFixture(t *testing.T) {
	_, r := fixture(t, "idt.S")
	if s := stateAt(t, r, "incb    fault_depth"); s.DF != Clear {
		t.Fatalf("DF after cld: %d", s.DF)
	}
	if s := stateAt(t, r, "cld"); s.DF != Unknown {
		t.Fatalf("DF on entry: %d", s.DF)
	}
	// fault_install's loop counts ecx down from 32: the loop head joins
	// 32 with 31, 30, ...
	wantUnknown(t, r, "lidt    idt_desc", "cx")
	s := r.Before(atExact(t, r.CFG.Prog, "ret", 0))
	if !s.StackKnown || s.Stack != 0 || s.Get(RDI).Def != DefEntry {
		t.Fatalf("fault_install ret: stack %v/%d edi def %d", s.StackKnown, s.Stack, s.Get(RDI).Def)
	}
}

func TestEveryFixtureAnalyzes(t *testing.T) {
	for _, name := range []string{"boot.S", "boot_fd.S", "memprobe.S", "natide.S", "nobar.S", "noe820.S",
		"rigboot.S", "rthunk.S", "stage2.S", "stage3.S", "tickstop.S"} {
		g, r := fixture(t, name)
		reached := 0
		for i := range r.In {
			if r.In[i].Reachable {
				reached++
				if g.BlockOf[i] < 0 {
					t.Fatalf("%s: non-code stmt %d has a state", name, i)
				}
			}
		}
		if reached == 0 {
			t.Fatalf("%s: nothing reached", name)
		}
	}
}

func TestRdmsrECX(t *testing.T) {
	_, r := run(t, `
	.code64
	movl $0xC0000080, %ecx
	rdmsr
	orl $0x100, %eax
	wrmsr
`, asm.Mode64, Config{})
	wantKnown(t, r, "rdmsr", "ecx", 0xC0000080)
	wantKnown(t, r, "rdmsr", "rcx", 0xC0000080)
	// rdmsr writes edx:eax as 32-bit registers: the upper halves of rax
	// and rdx are known zero in 64-bit mode.
	wantValue(t, r, "orl", "rax", 0, 0xFFFFFFFF00000000)
	wantValue(t, r, "wrmsr", "eax", 0x100, 0x100)
	wantKnown(t, r, "wrmsr", "ecx", 0xC0000080)
}

func TestPartialRegisters(t *testing.T) {
	_, r := run(t, `
	movb $0x42, %ah
	movb $0x11, %al
	movw $0x1234, %bx
	movl $0x89ABCDEF, %ecx
	movw $0x5555, %cx
	movb $0x66, %ch
	nop
`, asm.Mode32, Config{})
	wantValue(t, r, "movb $0x11", "eax", 0x4200, 0xFF00)
	wantValue(t, r, "movw $0x1234", "eax", 0x4211, 0xFFFF)
	wantKnown(t, r, "movw $0x1234", "ax", 0x4211)
	wantKnown(t, r, "nop", "ecx", 0x89AB6655)
	wantKnown(t, r, "nop", "ch", 0x66)
	wantKnown(t, r, "nop", "cl", 0x55)
	wantValue(t, r, "nop", "rcx", 0x89AB6655, 0xFFFFFFFF)
	wantUnknown(t, r, "nop", "edx")
}

func TestZeroExtension64(t *testing.T) {
	_, r := run(t, `
	movl $1, %eax
	movw $2, %bx
	movl $-1, %r8d
	xorl %ecx, %ecx
	movq $-1, %rdx
	movb $0, %dl
	nop
`, asm.Mode64, Config{})
	wantKnown(t, r, "nop", "rax", 1)
	wantValue(t, r, "nop", "rbx", 2, 0xFFFF)
	wantKnown(t, r, "nop", "r8", 0xFFFFFFFF)
	wantKnown(t, r, "nop", "rcx", 0)
	wantKnown(t, r, "nop", "rdx", 0xFFFFFFFFFFFFFF00)
	// In 32-bit code the same write leaves the upper half alone.
	_, r = run(t, "movl $1, %eax\nnop\n", asm.Mode32, Config{})
	wantValue(t, r, "nop", "rax", 1, 0xFFFFFFFF)
}

func TestArithmetic(t *testing.T) {
	_, r := run(t, `
	movl $0x10, %eax
	addl $0x22, %eax
	subl $2, %eax
	andl $0xF0, %eax
	orl $0x05, %eax
	xorl $0x0F, %eax
	shll $4, %eax
	shrl $1, %eax
	movl $0x80000000, %ebx
	sarl $4, %ebx
	roll $4, %ebx
	rorl $8, %ebx
	movb $3, %cl
	shlb %cl, %dl
	movl $5, %edx
	shll %cl, %edx
	incl %edx
	decl %edx
	decl %edx
	notl %edx
	negl %edx
	leal 4(%edx,%eax,2), %esi
	leal 0x100, %edi
	leal sym, %ebp
	movzbl %cl, %eax
	movsbw %bl, %bx
	movb $0x80, %al
	cbtw
	cwtl
	cltd
	nop
	sbbl %eax, %eax
	nop
`, asm.Mode32, Config{})
	wantKnown(t, r, "subl $2", "eax", 0x32)
	wantKnown(t, r, "andl", "eax", 0x30)
	wantKnown(t, r, "orl", "eax", 0x30)
	wantKnown(t, r, "xorl", "eax", 0x35)
	wantKnown(t, r, "shll $4", "eax", 0x3A)
	wantKnown(t, r, "shrl", "eax", 0x3A0)
	wantKnown(t, r, "movl $0x80000000", "eax", 0x1D0)
	wantKnown(t, r, "roll", "ebx", 0xF8000000)
	wantKnown(t, r, "rorl", "ebx", 0x8000000F)
	wantKnown(t, r, "movb $3", "ebx", 0x0F800000)
	wantValue(t, r, "movl $5, %edx", "dl", 0, 7) // shifted-in zeros
	wantKnown(t, r, "incl", "edx", 40)
	if v, _ := r.KnownAt(at(t, r.CFG.Prog, "decl", 1), "edx"); v != 40 {
		t.Fatalf("decl: %d", v)
	}
	wantKnown(t, r, "notl", "edx", 39)
	wantKnown(t, r, "negl", "edx", ^uint64(39)&0xFFFFFFFF)
	wantKnown(t, r, "leal 4(", "edx", 40)
	wantKnown(t, r, "leal 0x100", "esi", 40+4+0x1D0*2)
	wantKnown(t, r, "leal sym", "edi", 0x100)
	wantUnknown(t, r, "movzbl", "ebp")
	wantKnown(t, r, "movsbw", "eax", 3)
	wantKnown(t, r, "movb $0x80", "bx", 0) // bl of 0x0F800000
	wantKnown(t, r, "cwtl", "ax", 0xFF80)
	wantKnown(t, r, "cltd", "eax", 0xFFFFFF80)
	if v, ok := r.KnownAt(at(t, r.CFG.Prog, "nop", 0), "edx"); !ok || v != 0xFFFFFFFF {
		t.Fatalf("cltd of a negative eax: %#x %v", v, ok)
	}
	// sbb depends on CF: unknown.
	i := at(t, r.CFG.Prog, "sbbl", 0)
	if _, m, _ := r.ValueAt(i+1, "eax"); m != 0 {
		t.Fatalf("sbb result known: %#x", m)
	}
}

func TestAddCarryKnowsLowBits(t *testing.T) {
	_, r := run(t, `
	movb $0x0F, %al
	addl $1, %eax
	nop
`, asm.Mode32, Config{})
	// The carry out of bit 7 lands in unknown bits: only the low byte.
	wantValue(t, r, "nop", "eax", 0x10, 0xFF)
}

func TestPushPop(t *testing.T) {
	_, r := run(t, `
f:	movw $0x1234, %ax
	pushw %ax
	pushw $0x55
	pushw %ds
	popw %es
	popw %bx
	popw %cx
	movw $0x10, %ax
	movw %ax, %ds
	movw %ds, %dx
	ret
`, asm.Mode16, Config{})
	wantUnknown(t, r, "popw %bx", "es")
	if d := stateAt(t, r, "popw %bx").Get(ES).Def; d != DefEntry {
		t.Fatalf("es holds ds's entry value: def %d", d)
	}
	wantKnown(t, r, "popw %cx", "bx", 0x55)
	wantKnown(t, r, "movw $0x10", "cx", 0x1234)
	s := stateAt(t, r, "movw $0x10")
	if !s.StackKnown || s.Stack != 0 {
		t.Fatalf("stack %v %d", s.StackKnown, s.Stack)
	}
	wantKnown(t, r, "ret", "dx", 0x10)
	if s := stateAt(t, r, "pushw %ds"); s.Stack != 4 || len(s.Slots()) != 2 {
		t.Fatalf("depth %d slots %d", s.Stack, len(s.Slots()))
	}
	if d, ok := stateAt(t, r, "pushw %ds").Pushed(RAX); !ok || d != 2 {
		t.Fatalf("Pushed(ax) %d %v", d, ok)
	}
	if _, ok := stateAt(t, r, "pushw %ds").Pushed(RBX); ok {
		t.Fatal("bx was not pushed")
	}
}

func TestPushfPopfAndFlags(t *testing.T) {
	_, r := run(t, `
f:	cli
	cld
	pushfl
	sti
	std
	popfl
	nop
	pushl %eax
	popfl
	nop
	cmpl $1, %eax
	movl $2, %ebx
	je 1f
	addl $1, %ecx
1:	ret
`, asm.Mode32, Config{})
	s := stateAt(t, r, "nop")
	if s.IF != Clear || s.DF != Clear {
		t.Fatalf("popf restores pushf's IF/DF: IF %d DF %d", s.IF, s.DF)
	}
	if s := stateAt(t, r, "popfl"); s.IF != Set || s.DF != Set {
		t.Fatal("sti/std")
	}
	i := at(t, r.CFG.Prog, "nop", 1)
	if s := r.Before(i); s.IF != Unknown || s.DF != Unknown {
		t.Fatal("popf of an unknown value")
	}
	cmp := at(t, r.CFG.Prog, "cmpl", 0)
	je := r.Before(at(t, r.CFG.Prog, "je 1f", 0))
	if je.FlagDef[FlagZF] != cmp || je.FlagDef[FlagCF] != cmp {
		t.Fatalf("FlagDef at je %v want %d", je.FlagDef, cmp)
	}
	ret := r.Before(at(t, r.CFG.Prog, "ret", 0))
	if ret.FlagDef[FlagZF] != DefMerged {
		t.Fatalf("flags at the join %v", ret.FlagDef)
	}
	if v := r.Before(at(t, r.CFG.Prog, "cli", 0)).FlagDef[FlagCF]; v != DefEntry {
		t.Fatalf("entry FlagDef %d", v)
	}
}

func TestPushaPopa(t *testing.T) {
	_, r := run(t, `
f:	movw $1, %ax
	movw $2, %si
	pushaw
	xorw %ax, %ax
	movw $9, %si
	popaw
	ret
`, asm.Mode16, Config{})
	if s := stateAt(t, r, "xorw"); s.Stack != 16 {
		t.Fatalf("pusha depth %d", s.Stack)
	}
	wantKnown(t, r, "ret", "ax", 1)
	wantKnown(t, r, "ret", "si", 2)
	if s := stateAt(t, r, "ret"); !s.StackKnown || s.Stack != 0 {
		t.Fatal("popa balance")
	}
	_, r = run(t, "pushaq\nnop\n", asm.Mode64, Config{})
	if s := stateAt(t, r, "nop"); s.StackKnown {
		t.Fatal("pusha in 64-bit mode is opaque")
	}
}

func TestFrames(t *testing.T) {
	_, r := run(t, `
f:	pushl %ebp
	movl %esp, %ebp
	andl $-16, %esp
	subl $32, %esp
	movl %ebp, %esp
	popl %ebp
	ret
g:	enter $16, $0
	pushl %ebx
	leave
	ret
h:	pushl %ebp
	movl %esp, %ebp
	subl $8, %esp
	leave
	ret
k:	enter $8, $1
	leave
	ret
`, asm.Mode32, Config{})
	if s := stateAt(t, r, "subl $32"); s.StackKnown || !s.FrameKnown || s.FrameDepth != 4 {
		t.Fatalf("after and: %+v", s)
	}
	s := stateAt(t, r, "popl %ebp")
	if !s.StackKnown || s.Stack != 4 {
		t.Fatalf("mov %%ebp, %%esp restores the depth: %v %d", s.StackKnown, s.Stack)
	}
	s = r.Before(at(t, r.CFG.Prog, "ret", 0))
	if !s.StackKnown || s.Stack != 0 || s.Get(RBP).Def != DefEntry {
		t.Fatalf("f ret: %v %d ebp def %d", s.StackKnown, s.Stack, s.Get(RBP).Def)
	}
	if s := stateAt(t, r, "pushl %ebx"); s.Stack != 20 || !s.FrameKnown || s.FrameDepth != 4 {
		t.Fatalf("enter: %d %v %d", s.Stack, s.FrameKnown, s.FrameDepth)
	}
	s = r.Before(at(t, r.CFG.Prog, "ret", 1))
	if !s.StackKnown || s.Stack != 0 || s.Get(RBP).Def != DefEntry {
		t.Fatalf("leave after enter: %v %d %d", s.StackKnown, s.Stack, s.Get(RBP).Def)
	}
	s = r.Before(at(t, r.CFG.Prog, "ret", 2))
	if !s.StackKnown || s.Stack != 0 {
		t.Fatalf("leave after push/mov: %v %d", s.StackKnown, s.Stack)
	}
	// enter with a nesting level: the depth inside is lost, but rbp still
	// points just below the saved rbp, so leave restores it.
	if s := r.Before(at(t, r.CFG.Prog, "leave", 2)); s.StackKnown {
		t.Fatal("depth inside enter $8, $1")
	}
	s = r.Before(at(t, r.CFG.Prog, "ret", 3))
	if !s.StackKnown || s.Stack != 0 {
		t.Fatal("leave after enter with a level")
	}
}

func TestStackPointerArithmetic(t *testing.T) {
	_, r := run(t, `
f:	subq $24, %rsp
	leaq -8(%rsp), %rsp
	addq $32, %rsp
	movq $0x1000, %rsp
	pushq %rax
	popq %rsp
	nop
`, asm.Mode64, Config{})
	if s := stateAt(t, r, "leaq"); s.Stack != 24 {
		t.Fatalf("sub: %d", s.Stack)
	}
	if s := stateAt(t, r, "addq"); s.Stack != 32 {
		t.Fatalf("lea: %d", s.Stack)
	}
	if s := stateAt(t, r, "movq $0x1000"); !s.StackKnown || s.Stack != 0 {
		t.Fatalf("add: %d", s.Stack)
	}
	wantKnown(t, r, "popq", "rsp", 0xFF8)
	if s := stateAt(t, r, "nop"); s.StackKnown {
		t.Fatal("pop %rsp")
	}
}

func TestCallEffects(t *testing.T) {
	src := `
	.globl main
main:	movw $1, %ax
	movw $2, %bx
	movw $3, %si
	movw $0x10, %dx
	movw %dx, %es
	cld
	call keeps
	nop
	call clobbers
	nop
	call external
	nop
	call stdcall
	nop
	ret
keeps:	pushw %bx
	movw $7, %bx
	popw %bx
	ret
clobbers:
	movw $9, %si
	std
	ret
stdcall:
	ret $4
`
	_, r := run(t, src, asm.Mode16, Config{})
	// keeps writes bx only to restore it: everything survives.
	wantKnown(t, r, "nop", "ax", 1)
	wantKnown(t, r, "nop", "bx", 2)
	wantKnown(t, r, "nop", "si", 3)
	if s := r.Before(at(t, r.CFG.Prog, "nop", 0)); s.DF != Clear || s.FlagDef[FlagZF] != at(t, r.CFG.Prog, "call keeps", 0) {
		t.Fatalf("after keeps: DF %d flags %v", s.DF, s.FlagDef)
	}
	// clobbers writes si and DF.
	n1 := at(t, r.CFG.Prog, "nop", 1)
	if _, ok := r.KnownAt(n1, "si"); ok {
		t.Fatal("si clobbered")
	}
	if s := r.Before(n1); s.DF != Unknown || s.Get(RSI).Def != at(t, r.CFG.Prog, "call clobbers", 0) {
		t.Fatalf("after clobbers: DF %d si def %d", s.DF, s.Get(RSI).Def)
	}
	if v, ok := r.KnownAt(n1, "ax"); !ok || v != 1 {
		t.Fatal("ax survives clobbers")
	}
	// An unknown callee clobbers every general register and the data
	// segments, keeps the depth.
	n2 := at(t, r.CFG.Prog, "nop", 2)
	for _, reg := range []string{"ax", "bx", "es"} {
		if _, _, ok := r.ValueAt(n2, reg); ok {
			t.Fatalf("%s survives an unknown call", reg)
		}
	}
	if s := r.Before(n2); !s.StackKnown || s.Stack != 0 {
		t.Fatal("unknown call keeps the depth")
	}
	n3 := at(t, r.CFG.Prog, "nop", 3)
	if s := r.Before(n3); !s.StackKnown || s.Stack != -4 {
		t.Fatalf("ret $4 callee pops: %v %d", s.StackKnown, s.Stack)
	}

	// With a declared convention only the listed registers go.
	_, r = run(t, src, asm.Mode16, Config{CallClobbers: []Reg{RAX, RDX}})
	n2 = at(t, r.CFG.Prog, "nop", 2)
	if v, ok := r.KnownAt(n2, "si"); ok {
		t.Fatalf("si clobbered by clobbers(): %d", v)
	}
	if _, ok := r.KnownAt(n2, "ax"); ok {
		t.Fatal("ax is listed")
	}
	if v, ok := r.KnownAt(n2, "es"); !ok || v != 0x10 {
		t.Fatal("es survives")
	}
}

func TestCallSummaryEdgeCases(t *testing.T) {
	_, r := run(t, `
	.globl main
main:	movw $1, %cx
	call opaque
	nop
	call tail
	nop
	call rec
	nop
	call twopops
	nop
	ret
opaque:	MACRO
	ret
tail:	jmp elsewhere
rec:	movw $5, %di
	call rec2
	ret
rec2:	call rec
	movw $6, %bp
	ret
twopops:
	je 1f
	ret $2
1:	ret $4
`, asm.Mode16, Config{})
	if _, ok := r.KnownAt(at(t, r.CFG.Prog, "nop", 0), "cx"); ok {
		t.Fatal("a callee with a macro call is opaque")
	}
	if s := r.Before(at(t, r.CFG.Prog, "nop", 1)); s.Get(RCX).Mask != 0 {
		t.Fatal("tail call is opaque")
	}
	s := r.Before(at(t, r.CFG.Prog, "nop", 2))
	if s.Get(RDI).Def != at(t, r.CFG.Prog, "call rec", 0) || s.Get(RBP).Def != at(t, r.CFG.Prog, "call rec", 0) {
		t.Fatalf("recursive summary: di %d bp %d", s.Get(RDI).Def, s.Get(RBP).Def)
	}
	if s := r.Before(at(t, r.CFG.Prog, "nop", 3)); s.StackKnown {
		t.Fatal("rets that pop different amounts")
	}
}

func TestIntHooks(t *testing.T) {
	src := `
	movw $0x2401, %ax
	movw $5, %bx
	movw $0x10, %es
	cli
	int $0x15
	nop
	int3
	nop
	int %al
	nop
`
	_, r := run(t, src, asm.Mode16, Config{})
	n0 := at(t, r.CFG.Prog, "nop", 0)
	for _, reg := range []string{"ax", "bx", "es"} {
		if _, _, ok := r.ValueAt(n0, reg); ok {
			t.Fatalf("%s survives an int by default", reg)
		}
	}
	if s := r.Before(n0); s.IF != Unknown || s.FlagDef[FlagCF] != at(t, r.CFG.Prog, "int $0x15", 0) {
		t.Fatalf("int: IF %d CF def %d", s.IF, s.FlagDef[FlagCF])
	}
	called := []uint64{}
	cfg := Config{
		IntClobbers: func(v uint64) ([]Reg, bool) {
			if v == 0x15 {
				return []Reg{RAX}, true
			}
			return nil, false
		},
		IntEffect: func(v uint64, st *State) {
			called = append(called, v)
			if v == 0x15 {
				st.IF = Set
				st.SetReg(RAX, Value{V: 0, Mask: 0xFF00, Def: 99})
			}
		},
	}
	_, r = run(t, src, asm.Mode16, cfg)
	n0 = at(t, r.CFG.Prog, "nop", 0)
	wantKnown(t, r, "nop", "bx", 5)
	wantKnown(t, r, "nop", "es", 0x10)
	wantKnown(t, r, "nop", "ah", 0)
	if s := r.Before(n0); s.IF != Set || s.Get(RAX).Def != 99 {
		t.Fatalf("hook: IF %d", s.IF)
	}
	if len(called) == 0 || called[0] != 0x15 {
		t.Fatalf("IntEffect calls %v", called)
	}
	// int3 returns through iret: IF stays.
	if s := r.Before(at(t, r.CFG.Prog, "nop", 1)); s.IF != Set {
		t.Fatalf("int3 IF %d", s.IF)
	}
	if _, ok := r.KnownAt(at(t, r.CFG.Prog, "nop", 2), "bx"); ok {
		t.Fatal("unknown vector uses the default clobbers")
	}
}

func TestStringOps(t *testing.T) {
	_, r := run(t, `
	movw $0x100, %si
	movw $0x200, %di
	movw $4, %cx
	cld
	rep movsb
	nop
	movw $2, %cx
	std
	rep; movsw
	nop
	lodsb
	stosl
	nop
	repe cmpsb
	nop
	movw $8, %cx
	addr32 movsb
	nop
	movl $3, %ecx
	addr32 rep stosb
	nop
	movw $1, %cx
	loop 1f
1:	nop
`, asm.Mode16, Config{})
	wantKnown(t, r, "nop", "si", 0x104)
	wantKnown(t, r, "nop", "di", 0x204)
	wantKnown(t, r, "nop", "cx", 0)
	n1 := at(t, r.CFG.Prog, "nop", 1)
	if v, _ := r.KnownAt(n1, "si"); v != 0x100 {
		t.Fatalf("std: si %#x", v)
	}
	if v, _ := r.KnownAt(n1, "di"); v != 0x200 {
		t.Fatalf("std: di %#x", v)
	}
	n2 := at(t, r.CFG.Prog, "nop", 2)
	if v, _ := r.KnownAt(n2, "si"); v != 0xFF {
		t.Fatalf("lodsb: si %#x", v)
	}
	if v, _ := r.KnownAt(n2, "di"); v != 0x1FC {
		t.Fatalf("stosl: di %#x", v)
	}
	if _, ok := r.KnownAt(n2, "al"); ok {
		t.Fatal("lodsb loads al")
	}
	n3 := at(t, r.CFG.Prog, "nop", 3)
	for _, reg := range []string{"si", "di", "cx"} {
		if _, _, ok := r.ValueAt(n3, reg); ok {
			t.Fatalf("repe cmps leaves %s unknown", reg)
		}
	}
	n4 := at(t, r.CFG.Prog, "nop", 4)
	if v, ok := r.KnownAt(n4, "cx"); !ok || v != 8 {
		t.Fatal("no rep: cx unchanged")
	}
	if _, ok := r.KnownAt(n4, "esi"); ok {
		t.Fatal("addr32 steps esi, which was unknown above 16 bits")
	}
	n5 := at(t, r.CFG.Prog, "nop", 5)
	if v, ok := r.KnownAt(n5, "ecx"); !ok || v != 0 {
		t.Fatal("addr32 rep zeroes ecx")
	}
	n6 := at(t, r.CFG.Prog, "nop", 6)
	if v, ok := r.KnownAt(n6, "cx"); !ok || v != 0 {
		t.Fatalf("loop decrements cx: %d %v", v, ok)
	}
	// DF unknown: the index registers are unknown afterwards.
	_, r = run(t, "movw $1, %si\nlodsb\nnop\n", asm.Mode16, Config{})
	wantUnknown(t, r, "nop", "si")
	_, r = run(t, "movw $1, %si\ncld\nlodsb\nnop\n", asm.Mode16, Config{EntryDF: Set})
	wantKnown(t, r, "nop", "si", 2)
}

func TestImplicitWrites(t *testing.T) {
	_, r := run(t, `
	movl $1, %eax
	movl $2, %ebx
	movl $3, %ecx
	movl $4, %edx
	rdtsc
	nop
	movl $3, %ecx
	mull %ecx
	nop
	movl $3, %ecx
	mulb %cl
	nop
	lahf
	nop
	movb $1, %al
	xchgb %al, %ah
	nop
	xchgl %eax, (%ebx)
	cmpxchgl %ecx, (%ebx)
	nop
	inb $0x60, %al
	outb %al, $0x80
	nop
	movl $7, %ebx
	cpuid
	nop
	movl %cr0, %eax
	movl %eax, %cr3
	clts
	lmsw %ax
	nop
	lss stk, %esp
	nop
	syscall
	nop
	cmovzl %ebx, %ecx
	setz %al
	nop
	MYMACRO 1, 2
	nop
`, asm.Mode32, Config{})
	n := func(k int) int { return at(t, r.CFG.Prog, "nop", k) }
	if _, ok := r.KnownAt(n(0), "eax"); ok {
		t.Fatal("rdtsc writes eax")
	}
	if v, ok := r.KnownAt(n(0), "ecx"); !ok || v != 3 {
		t.Fatal("rdtsc keeps ecx")
	}
	if _, ok := r.KnownAt(n(1), "edx"); ok {
		t.Fatal("mul writes edx")
	}
	if v, ok := r.KnownAt(n(2), "ecx"); !ok || v != 3 {
		t.Fatal("mulb keeps ecx")
	}
	if _, ok := r.KnownAt(n(2), "ax"); ok {
		t.Fatal("mulb writes ax")
	}
	if _, ok := r.KnownAt(n(3), "ah"); ok {
		t.Fatal("lahf writes ah")
	}
	if v, ok := r.KnownAt(n(4), "ah"); !ok || v != 1 {
		t.Fatal("xchg moves al into ah")
	}
	if _, ok := r.KnownAt(n(5), "eax"); ok {
		t.Fatal("xchg/cmpxchg with memory write eax")
	}
	if _, ok := r.KnownAt(n(6), "al"); ok {
		t.Fatal("in writes al")
	}
	if _, ok := r.KnownAt(n(7), "ebx"); ok {
		t.Fatal("cpuid writes ebx")
	}
	if s := r.Before(n(8)); s.Get(CR0).Mask&0x8 != 0 {
		t.Fatal("lmsw loads TS")
	}
	s := stateAt(t, r, "lmsw")
	if s.Get(CR0).Mask&0x8 == 0 || s.Get(CR0).V&0x8 != 0 {
		t.Fatalf("clts clears TS (mask %#x)", s.Get(CR0).Mask)
	}
	if s.Get(CR3).Def != at(t, r.CFG.Prog, "%cr3", 0) {
		t.Fatal("mov to cr3")
	}
	if s := r.Before(n(9)); s.StackKnown || s.Get(SS).Def < 0 {
		t.Fatal("lss loads ss:esp")
	}
	if _, ok := r.KnownAt(n(11), "ecx"); ok {
		t.Fatal("cmov writes")
	}
	if s := r.Before(n(12)); s.Get(RBX).Mask != 0 || s.Get(RBX).Def != DefMerged || s.DF != Unknown {
		t.Fatal("a macro call is opaque")
	}
}

func TestLmsw(t *testing.T) {
	_, r := run(t, `
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	xorw %ax, %ax
	lmsw %ax
	nop
`, asm.Mode16, Config{})
	v, m, _ := r.ValueAt(at(t, r.CFG.Prog, "nop", 0), "cr0")
	if m&0xF != 0xF || v&0xF != 1 {
		t.Fatalf("lmsw cannot clear PE: %#x/%#x", v, m)
	}
}

func TestJoinAndWidening(t *testing.T) {
	_, r := run(t, `
f:	movw $0x1234, %ax
	movw $5, %bx
	je 1f
	movw $0x1237, %ax
	pushw %bx
1:	nop
	movl $0, %ecx
2:	incl %ecx
	shll $1, %edx
	cmpl $100, %ecx
	jne 2b
	ret
`, asm.Mode16, Config{})
	wantValue(t, r, "1:\tnop", "ax", 0x1234, 0xFFFF&^3)
	wantKnown(t, r, "1:\tnop", "bx", 5)
	if s := stateAt(t, r, "1:\tnop"); s.StackKnown || s.Get(RAX).Def != DefMerged || s.Get(RBX).Def == DefMerged {
		t.Fatalf("join: stack %v ax def %d bx def %d", s.StackKnown, s.Get(RAX).Def, s.Get(RBX).Def)
	}
	// The counter loop terminates and knows nothing of ecx at its head.
	wantUnknown(t, r, "incl", "ecx")
	wantUnknown(t, r, "ret", "ecx")
	// 64 iterations of shifting a known-zero bit in: widening stops it.
	_, r = run(t, `
	movl $0, %edx
1:	shll $1, %edx
	incl %edx
	jmp 1b
`, asm.Mode32, Config{})
	if s := stateAt(t, r, "shll"); !s.Reachable {
		t.Fatal("loop head reached")
	}
}

func TestVectorUpperState(t *testing.T) {
	_, r := run(t, `
f:	nop
	vmovdqa %ymm0, %ymm1
	nop
	addps %xmm2, %xmm1
	nop
	vaddps %xmm2, %xmm3, %xmm1
	nop
	vmovdqa64 %zmm4, %zmm5
	vzeroupper
	nop
	call ext
	nop
	vmcall
	nop
`, asm.Mode64, Config{})
	n := func(k int) *State { return r.Before(at(t, r.CFG.Prog, "nop", k)) }
	if n(0).UpperDirty(Vec(1)) != Unknown || n(0).AnyUpperDirty() != Unknown {
		t.Fatal("entry unknown")
	}
	if n(1).UpperDirty(Vec(1)) != Set || n(1).AnyUpperDirty() != Set {
		t.Fatal("VEX.256 write dirties")
	}
	if n(2).UpperDirty(Vec(1)) != Set {
		t.Fatal("legacy SSE keeps the upper state")
	}
	if n(3).UpperDirty(Vec(1)) != Clear {
		t.Fatal("VEX.128 zeroes the upper bits")
	}
	if n(4).UpperDirty(Vec(5)) != Clear || n(4).UpperDirty(Vec(1)) != Clear {
		t.Fatal("vzeroupper")
	}
	if n(4).UpperDirty(RAX) != Unknown {
		t.Fatal("not a vector")
	}
	if n(5).UpperDirty(Vec(1)) != Unknown {
		t.Fatal("unknown call")
	}
	all := State{vecKnown: ^uint32(0)}
	if all.AnyUpperDirty() != Clear {
		t.Fatal("all clean")
	}
	if s := n(6); s.Get(RAX).Def != DefMerged {
		t.Fatal("vmcall is opaque")
	}
}

func TestThroughDataHavoc(t *testing.T) {
	_, r := run(t, `
f:	movw $1, %ax
	.byte 0x90
	nop
`, asm.Mode16, Config{})
	if s := stateAt(t, r, "nop"); !s.Reachable || s.Get(RAX).Mask != 0 || s.StackKnown {
		t.Fatalf("through data: %+v", s.Get(RAX))
	}
}

func TestResultAPI(t *testing.T) {
	_, r := run(t, `
	movb $0x42, %ah
	.long 0
x:	nop
`, asm.Mode32, Config{EntryDF: Clear, EntryIF: Set})
	i := at(t, r.CFG.Prog, "movb", 0)
	if r.Before(-1) != nil || r.After(len(r.In)) != nil || r.Reachable(-1) {
		t.Fatal("out of range")
	}
	s := r.Before(i)
	if s.DF != Clear || s.IF != Set || !r.Reachable(i) {
		t.Fatal("entry config")
	}
	a := r.After(i)
	if v, m := a.Part(RAX, 8, 15); v != 0x42 || m != 0xFF {
		t.Fatalf("After: %#x/%#x", v, m)
	}
	if r.In[i].Get(RAX).Mask != 0 {
		t.Fatal("After must not change In")
	}
	if _, _, ok := r.ValueAt(i, "nosuch"); ok {
		t.Fatal("bad name")
	}
	if _, _, ok := r.ValueAt(i, "xmm0"); ok {
		t.Fatal("vector has no value")
	}
	d := at(t, r.CFG.Prog, ".long", 0)
	if r.Reachable(d) || r.After(d).Reachable {
		t.Fatal("data has no state")
	}
	if _, _, ok := r.ValueAt(d, "eax"); ok {
		t.Fatal("data ValueAt")
	}
	if v, ok := s.Reg(RAX); ok || v.Def != DefEntry {
		t.Fatal("Reg unknown")
	}
	if _, ok := s.Reg(Vec(0)); ok {
		t.Fatal("Reg vector")
	}
	var st State
	st.SetReg(RBX, Value{V: 0xFF, Mask: 0x0F, Def: 3})
	if v, ok := st.Reg(RBX); !ok || v.V != 0x0F {
		t.Fatalf("SetReg masks V: %+v", v)
	}
	st.SetReg(Vec(1), Value{Mask: 1})
	st.Clobber(RBX, 4)
	if v, _ := st.Reg(RBX); v.Mask != 0 || v.Def != 4 {
		t.Fatal("Clobber")
	}
	st.setUpper(Vec(2), Set)
	st.Clobber(Vec(2), 4)
	if st.UpperDirty(Vec(2)) != Unknown {
		t.Fatal("Clobber vector")
	}
	st.StackKnown, st.FrameKnown = true, true
	st.Clobber(RBP, 1)
	st.Clobber(RSP, 1)
	if st.FrameKnown || st.StackKnown {
		t.Fatal("Clobber rbp/rsp")
	}
	if v, m := st.Part(Vec(0), 0, 7); v != 0 || m != 0 {
		t.Fatal("Part vector")
	}
	if _, ok := st.Top(); ok {
		t.Fatal("empty Top")
	}
}
