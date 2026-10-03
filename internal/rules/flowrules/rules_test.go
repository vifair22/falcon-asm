package flowrules

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// bootSector wraps code as a 16-bit boot sector entered at _start.
func bootSector(body string) string {
	return "\t.code16\n\t.globl _start\n_start:\n" + body + "\n\t. = _start + 510\n\t.word 0xAA55\n"
}

func TestDFUnknown(t *testing.T) {
	// Boot entry, no cld: reported once, at the first string instruction.
	text := bootSector("\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw %ax, %ds\n\tmovw $1, %cx\n\tmovw $0, %di\n\tmovw $0, %si\n\trep\n\tstosb\n\tmovsb")
	got := expect(t, text, asm.Mode16, CodeDFUnknown, 1, "the BIOS left")
	if line(got[0]) != 11 {
		t.Fatalf("reported at line %d", line(got[0]))
	}
	// The fix goes above the lone rep prefix, not between it and stosb.
	fixed := apply(t, text, got[0].Fixes[0])
	if !strings.Contains(fixed, "\tcld\n\trep\n\tstosb") {
		t.Fatalf("fix:\n%s", fixed)
	}
	expect(t, fixed, asm.Mode16, CodeDFUnknown, 0, "")

	// cld on one path, the caller's DF on the other.
	expect(t, "\t.code16\nmain:\n\tcall f\n\tret\nf:\n\ttestw %ax, %ax\n\tjz 1f\n\tcld\n1:\tstosb\n\tret",
		asm.Mode16, CodeDFUnknown, 1, "cld runs on some paths")
	// std on one path, cld on the other.
	expect(t, "\t.code16\nf:\n\tcld\n\ttestw %ax, %ax\n\tjz 1f\n\tstd\n1:\tmovsb\n\tret",
		asm.Mode16, CodeDFUnknown, 1, "the std at line 6")
	// Deliberately backwards: DF is Set on every path.
	expect(t, "\t.code16\nf:\n\tstd\n\trep movsb\n\tcld\n\tret", asm.Mode16, CodeDFUnknown, 0, "")
	// An ordinary function relies on its caller's DF: not reported.
	expect(t, "\t.code16\nf:\n\tstosb\n\tret", asm.Mode16, CodeDFUnknown, 0, "")
	// A call the analysis cannot see into makes DF unknown, not wrong.
	expect(t, "\t.code16\nf:\n\tcld\n\ttestw %ax, %ax\n\tjz 1f\n\tcall ext\n1:\tstosb\n\tret",
		asm.Mode16, CodeDFUnknown, 0, "")
	// An interrupt handler runs with the interrupted code's DF.
	expect(t, "\t.code16\nsetup:\n\tmovw $isr, (0x20)\n\tret\nisr:\n\tstosb\n\tiret",
		asm.Mode16, CodeDFUnknown, 1, "interrupted code")
	// Data in the middle of the path: whatever it does is unknown.
	expect(t, "\t.code16\nf:\n\tcld\n\t.byte 0x90\n\tstosb\n\tret", asm.Mode16, CodeDFUnknown, 0, "")
	// popf restoring a known DF.
	expect(t, "\t.code16\nf:\n\tcld\n\tpushf\n\tstd\n\tpopf\n\tstosb\n\tret", asm.Mode16, CodeDFUnknown, 0, "")
}

func TestSegmentUnset(t *testing.T) {
	got := expect(t, bootSector("\tmovw $0x500, %di\n\tstosw\n\tstosw"), asm.Mode16, CodeSegmentUnset, 1, "stosw stores through %es")
	if line(got[0]) != 5 {
		t.Fatalf("line %d", line(got[0]))
	}
	expect(t, bootSector("\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x500, %di\n\tstosw"), asm.Mode16, CodeSegmentUnset, 0, "")
	// A DS load through a variable, and an explicit override.
	expect(t, bootSector("\tmovb drive, %dl\n\tmovw %fs:(%bx), %ax\n\tmovw (%bp), %cx\ndrive:\n\t.byte 0"),
		asm.Mode16, CodeSegmentUnset, 2, "since the BIOS entered")
	// DS:SI as a chain loader hands it over.
	expect(t, bootSector("\tmovw (%si), %ax\n\tlodsb"), asm.Mode16, CodeSegmentUnset, 0, "")
	// ...but not once SI was reloaded.
	expect(t, bootSector("\tmovw $0x600, %si\n\tlodsb"), asm.Mode16, CodeSegmentUnset, 1, "lodsb reads through %ds")
	// movs: ES destination and the overridden source.
	expect(t, bootSector("\tmovsb %es:(%si), (%di)"), asm.Mode16, CodeSegmentUnset, 1, "stores through %es")
	// Not a boot sector: the caller may have set ES.
	expect(t, "\t.code16\nf:\n\tstosw\n\tret", asm.Mode16, CodeSegmentUnset, 0, "")
	// lea and nop address nothing.
	expect(t, bootSector("\tleaw 4(%bx), %ax\n\tnopw (%bx)"), asm.Mode16, CodeSegmentUnset, 0, "")
	// 32-bit code is not real mode.
	expect(t, "\t.code32\n_start:\n\tstosl\n\tret", asm.Mode32, CodeSegmentUnset, 0, "")
	// _start alone marks the boot entry.
	expect(t, "\t.code16\n_start:\n\tstosb\n\tjmp .", asm.Mode16, CodeSegmentUnset, 1, "%es")
}

func TestStackImbalance(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tpushw %bx\n\tret", asm.Mode16, CodeStackImbalance, 1, "2 bytes still pushed (%bx on top)")
	if line(got[0]) != 4 {
		t.Fatalf("line %d", line(got[0]))
	}
	expect(t, "\t.code16\nmain:\n\tcall f\n\tret\nf:\n\tpopw %bx\n\tret", asm.Mode16, CodeStackImbalance, 1, "2 bytes popped beyond")
	// Continuation of code entered by a far jump: it pops what was
	// pushed before the jump.
	expect(t, "\t.code16\nf:\n\tpopw %bx\n\tiret", asm.Mode16, CodeStackImbalance, 0, "")
	expect(t, "\t.code16\nf:\n\tsubw $8, %sp\n\tret", asm.Mode16, CodeStackImbalance, 1, "8 bytes still pushed:")
	// push $target; ret and push %es; push $off; lret are jumps.
	expect(t, "\t.code16\nf:\n\tpushw $g\n\tret\ng:\n\tpushw %es\n\tpushw $0x100\n\tlret", asm.Mode16, CodeStackImbalance, 0, "")
	// pushf left on the stack is not a jump.
	expect(t, "\t.code16\nf:\n\tpushfw\n\tret", asm.Mode16, CodeStackImbalance, 1, "still pushed")
	// A handler that pops what it pushed.
	expect(t, "\t.code16\nisr:\n\tpushw %ax\n\tpopw %ax\n\tiret", asm.Mode16, CodeStackImbalance, 0, "")
	// Join of two forward paths with different depths.
	got = expect(t, "\t.code16\nf:\n\tpushw %bx\n\ttestw %ax, %ax\n\tjz 1f\n\tpopw %bx\n1:\tret", asm.Mode16, CodeStackImbalance, 1, "different stack depths")
	if !strings.Contains(got[0].Message, "2 bytes pushed coming from line 5, 0 from line 6") {
		t.Fatalf("message %q", got[0].Message)
	}
	// A loop that pushes per iteration (number printing) is not a join.
	expect(t, "\t.code16\nf:\n\txorw %cx, %cx\n1:\txorw %dx, %dx\n\tdivw %bx\n\tpushw %dx\n\tincw %cx\n\ttestw %ax, %ax\n\tjnz 1b\n2:\tpopw %ax\n\tloop 2b\n\tret",
		asm.Mode16, CodeStackImbalance, 0, "")
	// Only one arm of .if/.else is assembled.
	expect(t, "\t.code16\nf:\n.if DEBUG\n\tpushw %ax\n.else\n\tnop\n.endif\n\tnop\n.if DEBUG\n\tpopw %ax\n.endif\n\tret",
		asm.Mode16, CodeStackImbalance, 0, "")
	// An untracked stack pointer write: unknown, quiet.
	expect(t, "\t.code16\nf:\n\tpushw %bx\n\tmovw %ax, %sp\n\tret", asm.Mode16, CodeStackImbalance, 0, "")
}

func TestPushfPopf(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tpushfw\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tpopfw\n\tret\n1:\taddw $2, %sp\n\tret",
		asm.Mode16, CodePushfPopf, 1, "saved at line 3")
	if line(got[0]) != 10 {
		t.Fatalf("line %d", line(got[0]))
	}
	// pushf; pop %ax reads the flags; there is no restore to miss.
	expect(t, "\t.code16\nf:\n\tpushfw\n\tpopw %ax\n\tret", asm.Mode16, CodePushfPopf, 0, "")
	// Stack-imbalance reports the path that leaves the flags pushed.
	fs := lintText(t, "\t.code16\nf:\n\tpushfw\n\ttestw %ax, %ax\n\tjz 1f\n\tpopfw\n1:\tret", asm.Mode16, lint.Target{}, "")
	if len(only(fs, CodePushfPopf)) != 0 {
		t.Fatalf("double report: %v", fs)
	}
}

func TestCliSti(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tsti\n1:\tret", asm.Mode16, CodeCliSti, 1, "the cli at line 3")
	if line(got[0]) != 7 {
		t.Fatalf("line %d", line(got[0]))
	}
	// Never re-enabled: the function means to return with IF clear.
	expect(t, "\t.code16\nf:\n\tcli\n\tret", asm.Mode16, CodeCliSti, 0, "")
	// Mode switch code keeps interrupts off on purpose.
	expect(t, "\t.code16\nf:\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tsti\n\tret\n1:\tlgdt gdt\n\tret", asm.Mode16, CodeCliSti, 0, "")
	expect(t, "\t.code16\nf:\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tsti\n\tret\n1:\tljmp $0, $0x7E00\n\tret", asm.Mode16, CodeCliSti, 0, "")
	// popf restores.
	expect(t, "\t.code16\nf:\n\tpushfw\n\tcli\n\tpopfw\n\tret", asm.Mode16, CodeCliSti, 0, "")
	// Handlers are exempt.
	expect(t, "\t.code16\nisr:\n\tcli\n\tjz 1f\n\tsti\n1:\tiret\ng:\n\tret", asm.Mode16, CodeCliSti, 0, "")
}

func TestPartialRead(t *testing.T) {
	text := "\t.code16\nf:\n\tmovw $0x1000, %ax\n\tmovl %eax, %ebx\n\tret"
	got := expect(t, text, asm.Mode16, CodePartialRead, 1, "set only %ax: bits 16-31")
	if len(got[0].Fixes) != 1 || apply(t, text, got[0].Fixes[0]) != strings.Replace(text, "movw $0x1000, %ax", "movl $0x1000, %eax", 1) {
		t.Fatalf("fix %+v", got[0].Fixes)
	}
	// Upper half zeroed first.
	expect(t, "\t.code16\nf:\n\txorl %eax, %eax\n\tmovw $0x1000, %ax\n\tmovl %eax, %ebx\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// RMW of a full value read from CR0.
	expect(t, "\t.code16\nf:\n\tmovl %cr0, %eax\n\torb $1, %al\n\tmovl %eax, %cr0\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// Narrow write over a full load: the merge is the point.
	expect(t, "\t.code16\nf:\n\tmovl (0x500), %eax\n\tmovw $1, %ax\n\tmovl %eax, (0x500)\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// A byte write, a 64-bit read, an implicit read.
	expect(t, "\t.code64\nf:\n\tmovb $1, %cl\n\taddq %rcx, %rax\n\tret", asm.Mode64, CodePartialRead, 1, "set only %cl")
	expect(t, "\t.code32\nf:\n\tmovw $8, %cx\n\trep stosb\n\tret", asm.Mode32, CodePartialRead, 1, "%ecx is read as 32 bits")
	// A push saves whatever is there.
	expect(t, "\t.code16\nf:\n\tmovw $1, %ax\n\tpushl %eax\n\tpopl %eax\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// Narrow write of an earlier narrow write: still unknown upper bits.
	expect(t, "\t.code16\nf:\n\tmovb $1, %al\n\tmovw $1, %ax\n\tmovl %eax, %ebx\n\tret", asm.Mode16, CodePartialRead, 1, "")
	// High-byte writes are not low-part writes.
	expect(t, "\t.code16\nf:\n\tmovb $1, %ah\n\tmovl %eax, %ebx\n\tret", asm.Mode16, CodePartialRead, 0, "")
	// No fix for a register source.
	got = expect(t, "\t.code16\nf:\n\tmovw %bx, %ax\n\tmovl %eax, %ecx\n\tret", asm.Mode16, CodePartialRead, 1, "")
	if len(got[0].Fixes) != 0 {
		t.Fatalf("fix %+v", got[0].Fixes)
	}
}

func TestUninitRead(t *testing.T) {
	expect(t, bootSector("\tmovw $0x500, %di\n\trep stosb"), asm.Mode16, CodeUninitRead, 2, "")
	got := only(lintText(t, bootSector("\tmovw $0x500, %di\n\txorw %ax, %ax\n\trep stosb"), asm.Mode16, lint.Target{}, CodeUninitRead), CodeUninitRead)
	if len(got) != 1 || !strings.Contains(got[0].Message, "the count of this rep stosb is %cx") {
		t.Fatalf("%v", got)
	}
	// DL is the drive; the port in DX is not.
	expect(t, bootSector("\tmovb %dl, (0x500)\n\tinb %dx, %al"), asm.Mode16, CodeUninitRead, 1, "I/O port of this inb")
	// push saves; SI and DI are handover pointers.
	expect(t, bootSector("\tpushw %ax\n\tmovw %si, %bx"), asm.Mode16, CodeUninitRead, 0, "")
	// Only the boot entry.
	expect(t, "\t.code16\nf:\n\tmovw %ax, %bx\n\tret", asm.Mode16, CodeUninitRead, 0, "")
	// Once per register.
	expect(t, bootSector("\taddw %bx, %bx\n\taddw %bx, %bx"), asm.Mode16, CodeUninitRead, 1, "%bx")
}

func TestFlagsClobbered(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tcmpb $0, (%si)\n\tincw %si\n\tjne 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 1,
		"jne reads ZF from incw %si at line 4, which overwrote the flags of cmpb $0, (%si) at line 3")
	if line(got[0]) != 5 {
		t.Fatalf("line %d", line(got[0]))
	}
	// A call between cmp and jcc (across the call's block boundary).
	expect(t, "\t.code16\nf:\n\tcmpb $5, %al\n\tcall g\n\tje 1f\n1:\tret\ng:\n\tret", asm.Mode16, CodeFlagsClobbered, 1, "call g")
	// Carry chains.
	expect(t, "\t.code16\nf:\n\tcmpw %ax, %bx\n\taddw %cx, %ax\n\tadcw $0, %dx\n\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	// The cmp was consumed in between.
	expect(t, "\t.code16\nf:\n\ttestb %al, %al\n\tsetz %bl\n\tdecw %cx\n\tjnz f\n\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	// inc then jz with no cmp: a counted loop on purpose.
	expect(t, "\t.code16\nf:\n\tincw %cx\n\tjz 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	// Restored and BIOS-produced flags are meant.
	expect(t, "\t.code16\nf:\n\tcmpw %ax, %bx\n\tpushf\n\tincw %si\n\tpopf\n\tje 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	expect(t, "\t.code16\nf:\n\tcmpw %ax, %bx\n\tint $0x13\n\tjc 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	// The writer does not touch the read flag: inc leaves CF.
	expect(t, "\t.code16\nf:\n\tcmpw %ax, %bx\n\tincw %si\n\tjc 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	// Plain cmp; jcc.
	expect(t, "\t.code16\nf:\n\tcmpw %ax, %bx\n\tmovw $1, %si\n\tje 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
	// A macro in between is opaque.
	expect(t, "\t.code16\n.macro M\n\tincw %si\n.endm\nf:\n\tcmpw %ax, %bx\n\tM\n\tje 1f\n1:\tret", asm.Mode16, CodeFlagsClobbered, 0, "")
}

func TestUnreachable(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tret\n\tmovw $1, %ax\n\tret", asm.Mode16, CodeUnreachable, 1, "ret at line 3")
	if line(got[0]) != 4 || got[0].Severity != lint.Info {
		t.Fatalf("%v", got[0])
	}
	// The BPB jump-over and padding.
	expect(t, "\t.code16\n_start:\n\tjmp start\n\tnop\n\t.ascii \"FALCON\"\nstart:\n\tret\n\tnop\n\tint3\n\tud2", asm.Mode16, CodeUnreachable, 0, "")
	// A macro may define the labels that reach it.
	expect(t, "\t.code16\n.macro STUB\nx\\@: ret\n.endm\nf:\n\tret\n\tSTUB", asm.Mode16, CodeUnreachable, 0, "")
}

func TestFallIntoData(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tmovw $1, %ax\nmsg:\n\t.asciz \"hi\"", asm.Mode16, CodeFallIntoData, 1, "into data (msg at line 4)")
	if line(got[0]) != 3 {
		t.Fatalf("line %d", line(got[0]))
	}
	expect(t, "\t.code16\nf:\n\ttestw %ax, %ax\n\tjz f\n\t.fill 4, 1, 0", asm.Mode16, CodeFallIntoData, 1, ".fill")
	// Hand-encoded instructions.
	expect(t, "\t.code16\nf:\n\tmovw $1, %ax\n\t.byte 0xEA\n\t.word 0, 0x1000", asm.Mode16, CodeFallIntoData, 0, "")
	// A call that never returns, a halt, ret.
	expect(t, "\t.code16\nf:\n\tcall reboot\ngdt:\n\t.quad 0\ng:\n\thlt\nm:\n\t.ascii \"x\"", asm.Mode16, CodeFallIntoData, 0, "")
	// Unreachable code falling into data is unreachable-code's business.
	expect(t, "\t.code16\nf:\n\tret\n\tnop\nm:\n\t.ascii \"x\"", asm.Mode16, CodeFallIntoData, 0, "")
}

func TestBusySpin(t *testing.T) {
	expect(t, "\t.code16\nf:\n\tcli\n\tjmp .", asm.Mode16, CodeBusySpin, 1, "hlt")
	expect(t, "\t.code16\nf:\n\tcli\n1:\tjmp 1b", asm.Mode16, CodeBusySpin, 1, "hlt")
	expect(t, "\t.code16\nf:\n1:\tnop\n\tincw %ax\n\tjmp 1b", asm.Mode16, CodeBusySpin, 1, "infinite loop with no hlt or pause (from line 3)")
	expect(t, "\t.code16\nf:\n\tcli\n1:\thlt\n\tjmp 1b", asm.Mode16, CodeBusySpin, 0, "")
	// Exit test on a register the loop never changes.
	got := expect(t, "\t.code16\nf:\n1:\tincw %bx\n\ttestb %al, %al\n\tjz 1b\n\tret", asm.Mode16, CodeBusySpin, 1, "jz at line 5")
	if line(got[0]) != 5 {
		t.Fatalf("line %d", line(got[0]))
	}
	// Flags set before the loop and never rewritten in it.
	expect(t, "\t.code16\nf:\n\ttestb %al, %al\n1:\tmovw $1, %bx\n\tjz 1b\n\tret", asm.Mode16, CodeBusySpin, 1, "busy loop")
	// Loops that can end or that poll.
	expect(t, "\t.code16\nf:\n1:\tdecw %cx\n\tjnz 1b\n\tret", asm.Mode16, CodeBusySpin, 0, "")
	expect(t, "\t.code16\nf:\n1:\tinb $0x64, %al\n\ttestb $2, %al\n\tjnz 1b\n\tret", asm.Mode16, CodeBusySpin, 0, "")
	expect(t, "\t.code16\nf:\n1:\tcmpb $0, (0x500)\n\tje 1b\n\tret", asm.Mode16, CodeBusySpin, 0, "")
	expect(t, "\t.code16\nf:\n1:\tpause\n\tjmp 1b", asm.Mode16, CodeBusySpin, 0, "")
	expect(t, "\t.code16\nf:\n1:\tloop 1b\n\tret", asm.Mode16, CodeBusySpin, 0, "")
	// Entered with AX zero it never leaves, whatever path the exit takes.
	expect(t, "\t.code16\nf:\n1:\ttestw %ax, %ax\n\tjz 2f\n\tret\n2:\tjmp 1b", asm.Mode16, CodeBusySpin, 1, "jz at line 4")
	expect(t, "\t.code16\nf:\n1:\tcall g\n\tjmp 1b\ng:\n\tret", asm.Mode16, CodeBusySpin, 0, "")
}

func TestSelfModifying(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tmovb %al, patch+1\npatch:\n\tint $0\n\tret", asm.Mode16, CodeSelfModifying, 1, "into the instruction at patch (line 4) in executable section .text")
	if got[0].Severity != lint.Info {
		t.Fatalf("%v", got[0])
	}
	// A variable in .text is data, and a store to .data is not code.
	expect(t, "\t.code16\nf:\n\tmovb %al, var\n\tmovb %al, d\n\tmovb (var), %al\n\tret\nvar:\n\t.byte 0\n\t.section .data\nd:\n\tnop", asm.Mode16, CodeSelfModifying, 0, "")
	// A section without the x flag.
	expect(t, "\t.code16\n\t.section .rw, \"aw\"\nf:\n\tmovb %al, f\n\tret", asm.Mode16, CodeSelfModifying, 0, "")
	expect(t, "\t.code16\n\t.section .boot, \"ax\"\nf:\n\tmovb %al, 1f\n1:\tret", asm.Mode16, CodeSelfModifying, 1, "section .boot")
}

func TestFeatureUnguarded(t *testing.T) {
	expect(t, "\t.code32\nf:\n\trdtsc\n\trdtsc\n\tret", asm.Mode32, CodeFeatureUnguarded, 1, "rdtsc needs TSC (CPUID.01h:EDX[4])")
	// The target guarantees it.
	if got := only(lintText(t, "\t.code32\nf:\n\trdtsc\n\tret", asm.Mode32, lint.Target{CPU: "pentium"}, CodeFeatureUnguarded), CodeFeatureUnguarded); len(got) != 0 {
		t.Fatalf("pentium has rdtsc: %v", got)
	}
	if got := only(lintText(t, "\t.code64\nf:\n\tvpaddd %ymm0, %ymm1, %ymm2\n\tret", asm.Mode64, lint.Target{Level: 3}, CodeFeatureUnguarded), CodeFeatureUnguarded); len(got) != 0 {
		t.Fatalf("v3 has AVX2: %v", got)
	}
	expect(t, "\t.code64\nf:\n\trdtsc\n\tcmovzq %rax, %rbx\n\tret", asm.Mode64, CodeFeatureUnguarded, 0, "")
	expect(t, "\t.code64\nf:\n\tvpaddd %ymm0, %ymm1, %ymm2\n\tret", asm.Mode64, CodeFeatureUnguarded, 1, "AVX2")
	// The pmode.S shape: and of the output, cmp, jne past the instruction.
	expect(t, "\t.code32\nf:\n\tmovl $1, %eax\n\tcpuid\n\tandl $(1 << 15), %edx\n\tcmpl $(1 << 15), %edx\n\tjne 1f\n\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 0, "")
	// A guard for another bit, or another leaf, does not count.
	expect(t, "\t.code32\nf:\n\tmovl $1, %eax\n\tcpuid\n\ttestl $(1 << 4), %edx\n\tjz 1f\n\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 1, "CMOV")
	expect(t, "\t.code32\nf:\n\tmovl $7, %eax\n\txorl %ecx, %ecx\n\tcpuid\n\tbtl $15, %edx\n\tjnc 1f\n\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 1, "CMOV")
	// A guard that does not dominate: one path skips it.
	expect(t, "\t.code32\nf:\n\ttestl %esi, %esi\n\tjz 2f\n\tmovl $1, %eax\n\tcpuid\n\tbtl $15, %edx\n\tjnc 1f\n2:\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 1, "CMOV")
	// The register moved and shifted before the test: any bit counts.
	expect(t, "\t.code32\nf:\n\tmovl $1, %eax\n\tcpuid\n\tmovl %edx, %esi\n\tshrl $15, %esi\n\ttestl $1, %esi\n\tjz 1f\n\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 0, "")
	// Subleaf 0 of leaf 7 with an unknown ECX still counts.
	expect(t, "\t.code64\nf:\n\tmovl $7, %eax\n\tcpuid\n\tbtl $5, %ebx\n\tjnc 1f\n\tvpaddd %ymm0, %ymm1, %ymm2\n1:\tret",
		asm.Mode64, CodeFeatureUnguarded, 0, "")
	// Instructions every form of which needs nothing.
	expect(t, "\t.code16\nf:\n\tmovw %ax, %bx\n\tcpuid\n\tret", asm.Mode16, CodeFeatureUnguarded, 0, "")
}

func TestIFCritical(t *testing.T) {
	text := "\t.code16\nf:\n\tcli\n\tmovw $0x2401, %ax\n1:\tint $0x15\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n\tljmp $8, $f"
	got := expect(t, text, asm.Mode16, CodeIFCritical, 1, "INT 15h AX=2401h returns with interrupts enabled (SeaBIOS ends its INT 15h handler with sti); repeat cli after it: lgdt gdt_desc at line 6")
	fixed := apply(t, text, got[0].Fixes[0])
	if !strings.Contains(fixed, "int $0x15\n\tcli\n\tlgdt") {
		t.Fatalf("fix:\n%s", fixed)
	}
	expect(t, fixed, asm.Mode16, CodeIFCritical, 0, "")
	// INT 13h enables interrupts while it waits; the vector's quirk says so.
	expect(t, "\t.code16\nf:\n\tcli\n\tmovb $0x02, %ah\n\tint $0x13\n\tmovl %cr0, %eax\n\tmovl %eax, %cr0", asm.Mode16, CodeIFCritical, 1, "enables interrupts while waiting for the drive")
	// INT 16h AH=00h: the service's own IF note.
	expect(t, "\t.code16\nf:\n\tcli\n\tmovb $0x00, %ah\n\tint $0x16\n\tlidt idt", asm.Mode16, CodeIFCritical, 1, "it enables interrupts while waiting for a key")
	// Interrupts were on anyway; no mode switch follows; a call stops it.
	expect(t, "\t.code16\nf:\n\tmovw $0x2401, %ax\n\tint $0x15\n\tlgdt gdt", asm.Mode16, CodeIFCritical, 0, "")
	expect(t, "\t.code16\nf:\n\tcli\n\tmovw $0x2401, %ax\n\tint $0x15\n\tret", asm.Mode16, CodeIFCritical, 0, "")
	// A real-mode far jump to the next stage is not a mode switch.
	expect(t, "\t.code16\nf:\n\tcli\n\tmovb $0x42, %ah\n\tint $0x13\n\tljmp $0, $0x7E00", asm.Mode16, CodeIFCritical, 0, "")
	expect(t, "\t.code16\nf:\n\tcli\n\tmovw $0x2401, %ax\n\tint $0x15\n\tcall g\n\tlgdt gdt\ng:\n\tret", asm.Mode16, CodeIFCritical, 0, "")
	// INT 10h is not known to enable interrupts.
	expect(t, "\t.code16\nf:\n\tcli\n\tmovb $0x0E, %ah\n\tint $0x10\n\tlgdt gdt", asm.Mode16, CodeIFCritical, 0, "")
	// Another statement on the int's line: no fix, still reported.
	got = expect(t, "\t.code16\nf:\n\tcli\n\tmovw $0x2401, %ax\n\tint $0x15; nop\n\tlgdt gdt", asm.Mode16, CodeIFCritical, 1, "")
	if len(got[0].Fixes) != 0 {
		t.Fatalf("fix %+v", got[0].Fixes)
	}
}

func TestCFUnchecked(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tcmpw $0xAA55, 0x7DFE\n\tjne f\n\tret",
		asm.Mode16, CodeBIOSCFUnchecked, 1, "INT 13h AH=02h reports errors in CF, but cmpw $0xAA55, 0x7DFE at line 5 overwrites CF")
	if line(got[0]) != 4 {
		t.Fatalf("line %d", line(got[0]))
	}
	// Checked directly, through pushf, or via adc.
	expect(t, "\t.code16\nf:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tjc f\n\tcmpw %ax, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	expect(t, "\t.code16\nf:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tmovw $1, %bx\n\tadcw $0, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	// Returned to the caller, which checks it.
	expect(t, "\t.code16\nmain:\n\tcall rd\n\tjc 1f\n1:\tret\nrd:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	// ...or does not.
	expect(t, "\t.code16\nmain:\n\tcall rd\n\tcmpw %ax, %bx\n\tret\nrd:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 1, "the call at line 3 does not test it before cmpw at line 4")
	// No caller in the document: the convention is the caller's business.
	expect(t, "\t.code16\nrd:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	// A second int before the check.
	expect(t, "\t.code16\nf:\n\tmovb $0x88, %ah\n\tint $0x15\n\tint $0x10\n\tjc f\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 1, "int $0x10")
	// Reset before a retry, A20 enable, unknown AH, no error convention.
	expect(t, "\t.code16\nf:\n\txorb %ah, %ah\n\tint $0x13\n\tcmpw %ax, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	expect(t, "\t.code16\nf:\n\tmovw $0x2401, %ax\n\tint $0x15\n\tcmpw %ax, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	expect(t, "\t.code16\nf:\n\tint $0x13\n\tcmpw %ax, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	expect(t, "\t.code16\nf:\n\tmovb $0x0E, %ah\n\tint $0x10\n\tcmpw %ax, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	// A call or a macro on the path stops the check.
	expect(t, "\t.code16\nf:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tcall g\n\tcmpw %ax, %bx\ng:\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	expect(t, "\t.code16\n.macro M\n\tjc 1f\n1:\n.endm\nf:\n\tmovw $0x0201, %ax\n\tint $0x13\n\tM\n\tcmpw %ax, %bx\n\tret", asm.Mode16, CodeBIOSCFUnchecked, 0, "")
	// E820 named by EAX.
	expect(t, "\t.code32\nf:\n\tmovl $0xE820, %eax\n\tint $0x15\n\ttestl %ebx, %ebx\n\tret", asm.Mode32, CodeBIOSCFUnchecked, 1, "INT 15h AX=E820h")
}

func TestBIOSClobber(t *testing.T) {
	got := expect(t, "\t.code16\nf:\n\tmovw $0x0003, %ax\n\tint $0x10\n\tmovb %ah, %bl\n\tret", asm.Mode16, CodeBIOSClobber, 1,
		"%ah is read after INT 10h AH=00h (line 4) without being reloaded, but the service destroys it")
	if line(got[0]) != 5 {
		t.Fatalf("line %d", line(got[0]))
	}
	// The output is the point of the call.
	expect(t, "\t.code16\nf:\n\tmovw $0x0003, %ax\n\tint $0x10\n\tmovb %al, %bl\n\tret", asm.Mode16, CodeBIOSClobber, 0, "")
	expect(t, "\t.code16\nf:\n\tmovb $0x08, %ah\n\tint $0x13\n\tmovw %cx, %bx\n\tmovw %di, %si\n\tmovb %al, %bl\n\tret", asm.Mode16, CodeBIOSClobber, 1, "%al is read after INT 13h AH=08h")
	// Reloaded, or saved and restored.
	expect(t, "\t.code16\nf:\n\tmovw $0x0003, %ax\n\tint $0x10\n\tmovb $1, %ah\n\tmovb %ah, %bl\n\tret", asm.Mode16, CodeBIOSClobber, 0, "")
	expect(t, "\t.code16\nf:\n\tmovw $0x0003, %ax\n\tpushw %ax\n\tint $0x10\n\tpopw %ax\n\tmovb %ah, %bl\n\tret", asm.Mode16, CodeBIOSClobber, 0, "")
	// The quirks are their own code, at Information.
	got = expect(t, "\t.code16\nf:\n\tmovw $0x0E41, %ax\n\tint $0x10\n\tmovw (%bp), %si\n\tmovw %bp, %di\n\tret", asm.Mode16, CodeBIOSClobberQuirk, 1, "%bp is read after INT 10h AH=0Eh (line 4) without being reloaded; some BIOSes destroy it")
	if got[0].Severity != lint.Info {
		t.Fatalf("%v", got[0])
	}
	expect(t, "\t.code16\nf:\n\tmovw $0x0E41, %ax\n\tint $0x10\n\tmovw (%bp), %si\n\tret", asm.Mode16, CodeBIOSClobber, 0, "")
	expect(t, "\t.code32\nf:\n\tmovl $0xE820, %eax\n\tint $0x15\n\tjc f\n\tcmpl %edx, %ebx\n\tret", asm.Mode32, CodeBIOSClobberQuirk, 1, "%edx")
	// Unknown service: nothing to compare with.
	expect(t, "\t.code16\nf:\n\tint $0x10\n\tmovb %ah, %bl\n\tret", asm.Mode16, CodeBIOSClobber, 0, "")
}

func TestControlTransfersThroughReturn(t *testing.T) {
	// iretq through a frame built with pushfq: a far jump, and the code
	// after it is where the pushed address points.
	text := "\t.code64\nf:\n\tmovw %ss, %ax\n\tpush %rax\n\tpush %rsp\n\tpushfq\n\tpush $8\n\tlea 3(%rip), %rax\n\tpush %rax\n\tiretq\n\tmovl $1, %eax\n\tret"
	fs := lintText(t, text, asm.Mode64, lint.Target{}, "")
	if len(only(fs, CodeStackImbalance)) != 0 || len(only(fs, CodeUnreachable)) != 0 {
		t.Fatalf("%v", fs)
	}
	// A mode switch continuing in the next instruction.
	expect(t, "\t.code64\nf:\n\tpush $8\n\tpush $1f\n\tlretq\n\t.code32\n\tmovl $1, %eax\n1:\tret", asm.Mode64, CodeUnreachable, 0, "")
	// 64-bit-only instructions are the mode rules' business.
	expect(t, "\t.code32\nf:\n\tpushfq\n\tret", asm.Mode32, CodeFeatureUnguarded, 0, "")
}

func TestVectorOnlyService(t *testing.T) {
	// AH unknown: the vector's own quirk still applies, named by vector.
	expect(t, "\t.code16\nf:\n\tcli\n\tint $0x15\n\tlgdt gdt", asm.Mode16, CodeIFCritical, 1, "INT 15h returns with interrupts enabled")
	// A vector without function numbers resolves without AH.
	expect(t, "\t.code16\nf:\n\tint $0x12\n\tmovw %ax, %bx\n\tret", asm.Mode16, CodeBIOSClobber, 0, "")
	// The project's tables are used when they are hwdb's.
	if dbOf(nil) == nil {
		t.Fatal("no default db")
	}
}

func TestFeatureMessages(t *testing.T) {
	fs := only(lintText(t, "\t.code32\nf:\n\trdrand %eax\n\tret", asm.Mode32, lint.Target{CPU: "pentium", Level: 1}, CodeFeatureUnguarded), CodeFeatureUnguarded)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "the target (cpu pentium, x86-64-v1)") {
		t.Fatalf("%v", fs)
	}
	// A -march alias names the profile.
	if fs := only(lintText(t, "\t.code32\nf:\n\trdtsc\n\tret", asm.Mode32, lint.Target{CPU: "i686"}, CodeFeatureUnguarded), CodeFeatureUnguarded); len(fs) != 0 {
		t.Fatalf("i686 has rdtsc: %v", fs)
	}
	if fs := only(lintText(t, "\t.code64\nf:\n\tvpaddd %ymm0, %ymm1, %ymm2\n\tret", asm.Mode64, lint.Target{Level: 2}, CodeFeatureUnguarded), CodeFeatureUnguarded); len(fs) != 1 {
		t.Fatalf("v2 lacks AVX2: %v", fs)
	}
	// EVEX forms need AVX-512.
	expect(t, "\t.code64\nf:\n\tvpaddd %zmm0, %zmm1, %zmm2\n\tret", asm.Mode64, CodeFeatureUnguarded, 1, "AVX512F")
	// A guard through a chain longer than the search gives up.
	expect(t, "\t.code32\nf:\n\tmovl $1, %eax\n\tcpuid\n\tmovl %edx, %esi\n\tmovl %esi, %edi\n\tmovl %edi, %ebp\n\tmovl %ebp, %ebx\n\ttestl $(1 << 15), %ebx\n\tjz 1f\n\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 1, "CMOV")
	// A value not from cpuid.
	expect(t, "\t.code32\nf:\n\tmovl (0x500), %edx\n\ttestl $(1 << 15), %edx\n\tjz 1f\n\tcmovzl %eax, %ebx\n1:\tret",
		asm.Mode32, CodeFeatureUnguarded, 1, "CMOV")
}
