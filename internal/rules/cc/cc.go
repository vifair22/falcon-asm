// Package cc holds the calling convention rules of roadmap section 4
// (items 31-37) and the stack frame alignment check of item 48: callee-
// and caller-saved registers, stack alignment, shadow space and the red
// zone at calls, argument and result registers, and interrupt handlers.
//
// The convention model itself is internal/callconv. Which convention a
// function follows is resolved here, per function: an annotation on its
// label (`falcon-asm: cc=custom clobbers=ax preserves=bx returns=ax`),
// else a file-level annotation, else the target's (inferred from the
// compile flags: mingw -> ms, -m32 -> cdecl, x86-64 -> sysv,
// -mregparm=N -> regparm(N)). A convention for another width does not
// apply, and 16-bit code follows none unless a comment says otherwise,
// because real-mode code has no standard convention: falcon-bios's
// helpers each have their own.
//
// Every rule reads the dataflow, and keeps its ground rule: what the
// analysis cannot prove (a merged definition with something opaque in
// the function, an unknown stack depth, a callee with no convention) is
// never reported.
package cc

import (
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Rule codes.
const (
	CodeAnnotation     = "cc-annotation"
	CodeCalleeSaved    = "cc-callee-saved"
	CodeCallerSaved    = "cc-caller-saved-read"
	CodeStackAlign     = "cc-stack-align"
	CodeShadowSpace    = "cc-shadow-space"
	CodeRedZone        = "cc-red-zone"
	CodeArgUnset       = "cc-arg-unset"
	CodeReturnUnset    = "cc-return-unset"
	CodeHandlerRet     = "handler-ret"
	CodeHandlerKeep    = "handler-preserve"
	CodeHandlerErrcode = "handler-errcode"
	CodeFrameAlign     = "stack-frame-align"
)

func register(code string, section int, sev lint.Severity, doc lint.Doc, check func(*lint.Context, *model)) {
	lint.Register(&lint.Rule{
		Code: code, Section: section, Default: sev, Langs: lint.LangAsm, Doc: doc, NeedsFlow: true,
		Check: func(c *lint.Context) {
			if m := modelOf(c); m != nil {
				check(c, m)
			}
		},
	})
}

func init() {
	register(CodeAnnotation, 4, lint.Warning, lint.Doc{
		Summary: "A falcon-asm calling convention or handler annotation that cannot be understood.",
		Why: "Annotations select the calling convention of a file or a function (`falcon-asm: cc=sysv`, " +
			"`cc=custom clobbers=ax,cx preserves=bx,si,di args=ax returns=ax`) and mark interrupt handlers " +
			"(`falcon-asm: handler=errcode vector=14`). The lists (args=, returns=, clobbers=, preserves=) " +
			"belong to a custom convention, so cc=custom may be left out when they are given: lists alone mean " +
			"cc=custom. Lists next to cc= naming any other convention are reported, because one of the two is " +
			"wrong. A misspelled convention or register, or an annotation on a label that is not a function " +
			"start would otherwise be ignored silently, and the checks the author asked for would quietly not " +
			"run. Known conventions: sysv, ms, " +
			"cdecl, stdcall, fastcall, regparm(N), custom and none. An annotation on the label's line, or above " +
			"it with only comments and declaration directives (.globl, .type, .align) between, applies to that " +
			"function; anywhere else it is the file's default.",
		Bad:  "/* falcon-asm: cc=custom clobbers=ax,cx preserves=bx,sx */\nhelper:\n\tret",
		Good: "/* falcon-asm: cc=custom clobbers=ax,cx preserves=bx,si */\nhelper:\n\tret",
	}, checkAnnotation)

	register(CodeCalleeSaved, 4, lint.Warning, lint.Doc{
		Summary: "A callee-saved register is changed and not restored before the function returns.",
		Why: "Every convention names registers a function must hand back as it found them: rbx, rbp and " +
			"r12-r15 under SysV; those plus rdi, rsi and xmm6-xmm15 under MS x64; ebx, esi, edi and ebp under " +
			"the i386 conventions; whatever a custom convention lists in preserves=. A caller keeps live values " +
			"there across the call, and the compiler that generated it will not reload them, so the damage " +
			"surfaces far from the function that did it. The dataflow follows push/pop pairs (and pusha/popa, " +
			"leave, spill slots), so a saved and restored register is quiet; the report is on each ret a " +
			"changed value can reach, with the write as the related location. For SysV and MS x64, loading " +
			"the MXCSR or the x87 control word without saving it first is reported too.",
		Bad:  "# falcon-asm: cc=cdecl\n\ncount:\n\tmovl 4(%esp), %ebx\n\tmovl (%ebx), %eax\n\tret",
		Good: "# falcon-asm: cc=cdecl\n\ncount:\n\tpushl %ebx\n\tmovl 8(%esp), %ebx\n\tmovl (%ebx), %eax\n\tpopl %ebx\n\tret",
	}, checkCalleeSaved)

	register(CodeCallerSaved, 4, lint.Warning, lint.Doc{
		Summary: "A caller-saved register is read after a call that may have changed it.",
		Why: "A call to a function with a known convention (one in the document with its own, or any other " +
			"under the file's) may change every register the convention does not make callee-saved: rcx, rsi, " +
			"rdi and r8-r11 under SysV, ecx under cdecl, and so on. Reading one after the call, before writing " +
			"it, reads whatever the callee left there; the code works until the callee changes. Result " +
			"registers (rax, rdx under SysV; eax, edx under the i386 conventions) are not reported, since " +
			"reading them is the point of the call. For a callee in the document only the registers its code " +
			"actually changes are reported.",
		Bad:  "# falcon-asm: cc=cdecl\n\n\tmovl $10, %ecx\n\tcall putc\n\tdecl %ecx",
		Good: "# falcon-asm: cc=cdecl\n\n\tmovl $10, %ebx\n\tcall putc\n\tdecl %ebx",
	}, checkCallerSaved)

	register(CodeStackAlign, 4, lint.Warning, lint.Doc{
		Summary: "The stack is not 16-byte aligned at a call (SysV and MS x64).",
		Why: "Both 64-bit conventions require rsp to be a multiple of 16 at the call instruction, so a " +
			"function starts with rsp 8 bytes past a 16-byte boundary (the return address). Compiled callees " +
			"rely on it: movaps to a stack slot faults when it is wrong, usually deep inside a library " +
			"function. The depth at each call is computed from the function's entry through pushes, sub/add " +
			"and lea on rsp and the frame pointer; after `and $-16, %rsp` the depth is unknown and nothing is " +
			"reported. Only functions something calls or that are global are checked, since a program entry " +
			"point starts with a different stack.",
		Bad:  "# falcon-asm: cc=sysv\n\n\t.globl f\nf:\n\tcall g\n\tret",
		Good: "# falcon-asm: cc=sysv\n\n\t.globl f\nf:\n\tsubq $8, %rsp\n\tcall g\n\taddq $8, %rsp\n\tret",
	}, checkStackAlign)

	register(CodeShadowSpace, 4, lint.Warning, lint.Doc{
		Summary: "An MS x64 call without the 32 bytes of shadow space the callee may write.",
		Why: "Under the MS x64 convention the caller reserves 32 bytes directly above the return address, " +
			"where the callee may spill its four register arguments. A callee that does (any compiled one " +
			"built without optimization, and many with) overwrites whatever the caller keeps there: a saved " +
			"register, a local, its own return address. The space counted is what the function allocated " +
			"since its last push, when the stack depth is known.",
		Bad:  "# falcon-asm: cc=ms\n\nf:\n\tpushq %rbx\n\tcall g\n\tpopq %rbx\n\tret",
		Good: "# falcon-asm: cc=ms\n\nf:\n\tpushq %rbx\n\tsubq $32, %rsp\n\tcall g\n\taddq $32, %rsp\n\tpopq %rbx\n\tret",
	}, checkShadowSpace)

	register(CodeRedZone, 4, lint.Warning, lint.Doc{
		Summary: "A memory access below rsp that the convention does not protect.",
		Why: "SysV guarantees 128 bytes below rsp (the red zone) that signal and interrupt delivery will not " +
			"touch; MS x64 guarantees none, so anything stored below rsp there can be overwritten at any " +
			"moment by an interrupt, an exception dispatch or a debugger. Accesses through rsp with a negative " +
			"displacement are reported under MS x64, and those beyond -128 under SysV. (Code built with " +
			"-mno-red-zone, kernels and firmware, has no red zone even under SysV; declare it with a custom " +
			"convention or silence the rule.)",
		Bad:  "# falcon-asm: cc=ms\n\nf:\n\tmovq %rcx, -8(%rsp)\n\tret",
		Good: "# falcon-asm: cc=ms\n\nf:\n\tsubq $8, %rsp\n\tmovq %rcx, (%rsp)\n\taddq $8, %rsp\n\tret",
	}, checkRedZone)

	register(CodeArgUnset, 4, lint.Info, lint.Doc{
		Summary: "A call to a function in the document that reads an argument register the caller never set.",
		Why: "The callee's own dataflow says which argument registers it reads before writing (rdi and rsi " +
			"under SysV, args= of a custom convention, ecx and edx under fastcall). If the caller has not " +
			"written one of them on any path to the call, and does not receive it as an argument itself, the " +
			"callee works on whatever the register happened to hold. Reported as Information because a " +
			"register can be set deliberately far away (a global register convention).",
		Bad: "# falcon-asm: cc=cdecl\n\n# falcon-asm: cc=fastcall\nadd2:\n\tleal (%ecx,%edx), %eax\n\tret\n" +
			"main:\n\tmovl $1, %ecx\n\tcall add2\n\tret",
		Good: "# falcon-asm: cc=cdecl\n\n# falcon-asm: cc=fastcall\nadd2:\n\tleal (%ecx,%edx), %eax\n\tret\n" +
			"main:\n\tmovl $1, %ecx\n\tmovl $2, %edx\n\tcall add2\n\tret",
	}, checkArgUnset)

	register(CodeReturnUnset, 4, lint.Info, lint.Doc{
		Summary: "A function whose callers read its result returns on some path without setting it.",
		Why: "When a caller reads the result register (rax/eax, rdx/edx for a pair, or a custom returns=) " +
			"right after calling a function in the document, the function evidently returns a value; a ret " +
			"reached without writing that register returns the caller's own leftover value instead. Usually " +
			"an early-exit path that forgot to set the error code. Information, since the evidence is the " +
			"callers' use rather than a declaration.",
		Bad: "# falcon-asm: cc=cdecl\n\nget:\n\ttestl %ecx, %ecx\n\tjz 1f\n\tmovl $1, %eax\n1:\tret\n" +
			"main:\n\tcall get\n\tmovl %eax, result\n\tret",
		Good: "# falcon-asm: cc=cdecl\n\nget:\n\txorl %eax, %eax\n\ttestl %ecx, %ecx\n\tjz 1f\n\tmovl $1, %eax\n1:\tret\n" +
			"main:\n\tcall get\n\tmovl %eax, result\n\tret",
	}, checkReturnUnset)

	register(CodeHandlerRet, 4, lint.Error, lint.Doc{
		Summary: "An interrupt or exception handler returns with ret or lret instead of iret.",
		Why: "The processor enters a handler with the flags, the code segment and the return address on the " +
			"stack. ret pops only the address, leaving the rest for the interrupted code to trip over; lret " +
			"pops the segment too but not the flags, so interrupts stay as the handler left them. A handler is " +
			"a label installed in the interrupt vector table or taken by IDT setup code (a function that runs " +
			"lidt), or one marked `falcon-asm: handler`. `lret $2` (or $4), the INT 13h hook idiom that " +
			"discards the saved flags to return the carry flag, is not reported.",
		Bad:  "\t.code16\n\tmovw $tick, 0x20\n\tret\ntick:\n\tincw ticks\n\tret",
		Good: "\t.code16\n\tmovw $tick, 0x20\n\tret\ntick:\n\tincw ticks\n\tiret",
	}, checkHandlerRet)

	register(CodeHandlerKeep, 4, lint.Warning, lint.Doc{
		Summary: "An interrupt or exception handler changes a register it does not restore.",
		Why: "A hardware interrupt or an exception can arrive between any two instructions, so the code it " +
			"interrupts has no chance to save anything: for a handler every general register is callee-saved. " +
			"The dataflow follows push/pop and pusha/popa, and a call from the handler to code with a known " +
			"convention changes that convention's caller-saved registers. Software service handlers (an IVT " +
			"vector other than the exceptions, IRQs and 1Ch, or `falcon-asm: handler=service`) return results " +
			"in registers and are not checked.",
		Bad: "# falcon-asm: handler vector=32\nirq0:\n\tincl ticks\n\tmovb $0x20, %al\n\toutb %al, $0x20\n\tiret",
		Good: "# falcon-asm: handler vector=32\nirq0:\n\tpushl %eax\n\tincl ticks\n\tmovb $0x20, %al\n\toutb %al, $0x20\n" +
			"\tpopl %eax\n\tiret",
	}, checkHandlerPreserve)

	register(CodeHandlerErrcode, 4, lint.Warning, lint.Doc{
		Summary: "An exception handler returns without discarding the error code its vector pushes (or discards one that is not there).",
		Why: "In protected and long mode the processor pushes an error code for vectors 8, 10-14, 17, 21, 29 " +
			"and 30, and not for the others. iret expects the return address on top of the stack, so a " +
			"handler for one of those vectors must remove the error code first (add $4, %esp; add $8, %rsp " +
			"in 64-bit code), and a handler for any other vector must not. Getting it wrong returns to the " +
			"error code as an address. The vector is known from a handler table's index or from " +
			"`falcon-asm: handler vector=N` (or handler=errcode).",
		Bad:  "# falcon-asm: handler vector=13\ngp_fault:\n\tincl faults\n\tiret",
		Good: "# falcon-asm: handler vector=13\ngp_fault:\n\tincl faults\n\taddl $4, %esp\n\tiret",
	}, checkHandlerErrcode)

	register(CodeFrameAlign, 6, lint.Warning, lint.Doc{
		Summary: "An aligned SSE access (movaps, movdqa, ...) to a stack slot that is not 16-byte aligned.",
		Why: "movaps, movdqa, their VEX forms and the non-temporal moves fault (#GP) on an address that is " +
			"not 16-byte aligned. A 64-bit SysV or MS x64 function starts with rsp 8 past a 16-byte boundary, " +
			"so after an odd number of pushes, or a sub that is not a multiple of 16 from there, N(%rsp) is " +
			"misaligned for every N that looks right. The address is computed from the function's entry " +
			"through the tracked stack depth (and the frame pointer when rbp holds a captured depth); a " +
			"realigned stack (`and $-16, %rsp`) has an unknown depth and is never reported.",
		Bad:  "# falcon-asm: cc=sysv\n\n\t.globl f\nf:\n\tsubq $16, %rsp\n\tmovaps %xmm0, (%rsp)\n\taddq $16, %rsp\n\tret",
		Good: "# falcon-asm: cc=sysv\n\n\t.globl f\nf:\n\tsubq $24, %rsp\n\tmovaps %xmm0, (%rsp)\n\taddq $24, %rsp\n\tret",
	}, checkFrameAlign)
}
