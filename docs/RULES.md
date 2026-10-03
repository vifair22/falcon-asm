# falcon-asm rules

Generated from the rule registry by `make rules-doc`; do not edit by hand.
A test fails when this file and the registry disagree.

Every rule can be configured per project and per path in
`.falcon-asm.json` (`"rules": {"<code>": "off"}`) and suppressed in
the source with a comment: `falcon-asm: ignore=<code>` on the line,
`ignore-next=<code>` for the next code line, `ignore-file=<code>`, or an
`ignore-begin=<code>` ... `ignore-end=<code>` region. `all` matches every
rule. See docs/DESIGN.md (Lint engine) for the full reference.

| Code | Section | Default | Summary |
| --- | --- | --- | --- |
| [addr-invalid-reg](#addr-invalid-reg) | 1 | warning | Register that cannot address memory: %esp as an index, %rip outside 64-bit code, mixed base/index widths. |
| [addr16-invalid](#addr16-invalid) | 1 | warning | Invalid 16-bit addressing: (%si,%di), (%ax), a scaled index, or 16-bit registers in 64-bit code. |
| [avx-decoration](#avx-decoration) | 1 | warning | AVX-512 decoration errors: {%k0} as a mask, {z} without a mask, broadcast width mismatch, mixed vector widths. |
| [branch-out-of-range](#branch-out-of-range) | 1 | error | loop/jcxz/jecxz target beyond the rel8 range, from the assembler listing. |
| [data-range](#data-range) | 1 | warning | Data directive value that does not fit its width (.byte 300, .word 0x10000). |
| [fixed-reg-form](#fixed-reg-form) | 1 | warning | Operand that must be a fixed register: in/out data and port, shift count (in %dx, %bl). |
| [imm-range](#imm-range) | 1 | warning | Immediate out of range for the instruction (out $0x300, int $256, ret $70000, movb $300). |
| [operand-no-form](#operand-no-form) | 1 | warning | No form of the instruction takes these operands (lgdt %eax: lgdt needs memory). |
| [operand-size-ambiguous](#operand-size-ambiguous) | 1 | warning | Memory operand with no suffix and no register to give its size (inc (%bx)). |
| [operand-size-mismatch](#operand-size-mismatch) | 1 | warning | Operand sizes disagree with the suffix or with each other (movw %eax, %bx). |
| [prefix-misuse](#prefix-misuse) | 1 | warning | Prefix misuse: lock without a memory destination, rep on a non-string instruction, repeated or conflicting prefixes. |
| [rex-high-byte](#rex-high-byte) | 1 | warning | %ah/%bh/%ch/%dh in an instruction that needs a REX prefix (movb %ah, %sil). |
| [sreg-misuse](#sreg-misuse) | 1 | warning | Segment register misuse: mov to %cs, pop %cs, lds with a register, push %ds in 64-bit code. |
| [cpu-unsupported](#cpu-unsupported) | 2 | warning | Instruction the target CPU profile does not implement (cmov on a Pentium, cpuid on a 386). |
| [far-jump-mode](#far-jump-mode) | 2 | warning | Far jump into code assembled for a different mode than the selector's descriptor runs it in. |
| [feature-unguarded](#feature-unguarded) | 2 | info | Instruction needing a CPUID feature (rdtsc, cmov, AVX2, rdrand) with no CPUID test of it on every path, beyond what the target guarantees. |
| [level](#level) | 2 | warning | Instruction form above the x86-64 level the file targets (v1-v4). |
| [level-outside](#level-outside) | 2 | info | Instruction outside the x86-64 level scheme (AES, SHA, VMX ...) in a file with a level target. |
| [mode-instruction](#mode-instruction) | 2 | warning | Instruction not valid in the .code16/.code32/.code64 mode in effect. |
| [mode-register](#mode-register) | 2 | warning | Register not encodable in the mode in effect (%rax, %r8d, %sil, %xmm8 outside 64-bit code). |
| [bios-cf-unchecked](#bios-cf-unchecked) | 3 | warning | BIOS call that reports errors in CF whose CF is overwritten before anything tests it. |
| [bios-clobber](#bios-clobber) | 3 | warning | Register a BIOS service is documented to destroy, read after the int without being reloaded. |
| [bios-clobber-quirk](#bios-clobber-quirk) | 3 | info | Register some BIOSes destroy across a call although no reference says so (BP over INT 10h), read after the int. |
| [busy-spin](#busy-spin) | 3 | info | Infinite loop with no hlt or pause: a jmp to itself, or a loop whose exit test nothing in it can change. |
| [cli-sti-pair](#cli-sti-pair) | 3 | info | Function that disables interrupts with cli and returns on some path without sti or popf. |
| [df-unknown](#df-unknown) | 3 | warning | String instruction reached with the direction flag not provably clear (no cld on some path). |
| [fallthrough-into-data](#fallthrough-into-data) | 3 | warning | Code that falls through into data (a string, a table, padding) instead of jumping or returning. |
| [flags-clobbered](#flags-clobbered) | 3 | warning | Flags of a cmp/test overwritten before the conditional instruction that was meant to read them. |
| [if-enabled-critical](#if-enabled-critical) | 3 | warning | BIOS call inside a cli section that returns with interrupts enabled before a mode switch (INT 15h ends with sti). |
| [partial-register-read](#partial-register-read) | 3 | warning | 32/64-bit read of a register whose last write set only its low 8 or 16 bits (movw $x, %ax ... %eax). |
| [pushf-popf-pair](#pushf-popf-pair) | 3 | info | Function that saves the flags with pushf but returns on some path without popf. |
| [segment-unset](#segment-unset) | 3 | warning | Memory access through a segment register the boot entry never set (%es:(%di) stores, %ds loads in real mode). |
| [self-modifying-code](#self-modifying-code) | 3 | info | Store into an instruction in an executable section (self-modifying code). |
| [stack-imbalance](#stack-imbalance) | 3 | warning | ret/iret with pushed bytes still on the stack, or paths that join with different stack depths. |
| [uninit-read](#uninit-read) | 3 | info | Boot entry code reading a register nothing has set since the BIOS jumped to it (a rep count, an I/O port in %dx). |
| [unreachable-code](#unreachable-code) | 3 | info | Code after a jmp, ret or halt that nothing jumps to. |
| [cc-annotation](#cc-annotation) | 4 | warning | A falcon-asm calling convention or handler annotation that cannot be understood. |
| [cc-arg-unset](#cc-arg-unset) | 4 | info | A call to a function in the document that reads an argument register the caller never set. |
| [cc-callee-saved](#cc-callee-saved) | 4 | warning | A callee-saved register is changed and not restored before the function returns. |
| [cc-caller-saved-read](#cc-caller-saved-read) | 4 | warning | A caller-saved register is read after a call that may have changed it. |
| [cc-red-zone](#cc-red-zone) | 4 | warning | A memory access below rsp that the convention does not protect. |
| [cc-return-unset](#cc-return-unset) | 4 | info | A function whose callers read its result returns on some path without setting it. |
| [cc-shadow-space](#cc-shadow-space) | 4 | warning | An MS x64 call without the 32 bytes of shadow space the callee may write. |
| [cc-stack-align](#cc-stack-align) | 4 | warning | The stack is not 16-byte aligned at a call (SysV and MS x64). |
| [handler-errcode](#handler-errcode) | 4 | warning | An exception handler returns without discarding the error code its vector pushes (or discards one that is not there). |
| [handler-preserve](#handler-preserve) | 4 | warning | An interrupt or exception handler changes a register it does not restore. |
| [handler-ret](#handler-ret) | 4 | error | An interrupt or exception handler returns with ret or lret instead of iret. |
| [bios-struct](#bios-struct) | 5 | warning | BIOS structure (disk address packet, E820 buffer ...) built with a wrong size or member. |
| [boot-signature](#boot-signature) | 5 | warning | Boot sector whose bytes 510-511 are not 55h AAh. |
| [descriptor-invalid](#descriptor-invalid) | 5 | warning | GDT descriptor that cannot be loaded: L and D both set, a reserved system type, a short TSS, or not present but used. |
| [gdt-limit](#gdt-limit) | 5 | warning | GDT or IDT pseudo-descriptor limit is not size - 1. |
| [loader-sector-count](#loader-sector-count) | 5 | warning | INT 13h read of fewer sectors than the stage it loads. |
| [loader-sector-count-unproven](#loader-sector-count-unproven) | 5 | info | Boot sector or annotated loader whose INT 13h read cannot be shown to cover the stage it loads. |
| [mbr-partition-overlap](#mbr-partition-overlap) | 5 | warning | Code or data of an MBR emitted into the partition table (bytes 446-509). |
| [mode-switch-a20](#mode-switch-a20) | 5 | info | Switch to protected mode in a file that never enables A20. |
| [mode-switch-order](#mode-switch-order) | 5 | warning | Mode switch with a step missing or out of order: lgdt, cli, the far jump and segment reloads around CR0.PE; PAE, CR3, EFER.LME and PG for long mode. |
| [pte-reserved](#pte-reserved) | 5 | warning | Page table entry built with reserved bits set. |
| [real-mode-layout](#real-mode-layout) | 5 | warning | Real-mode code that reaches for a label linked above 1 MiB, or runs past the end of its 64 KiB code segment. |
| [real-mode-reach](#real-mode-reach) | 5 | warning | Real-mode memory access past what segment:offset can reach, or a 16-bit offset that wraps. |
| [selector-type](#selector-type) | 5 | error | Selector names a GDT entry beyond the limit or of the wrong type for the segment register it is loaded into. |
| [sipi-alignment](#sipi-alignment) | 5 | warning | SIPI trampoline at a section offset that is not a multiple of 4 KiB. |
| [sipi-trampoline](#sipi-trampoline) | 5 | warning | SIPI trampoline not assembled as 16-bit code. |
| [sipi-unproven](#sipi-unproven) | 5 | info | SIPI trampoline whose 4 KiB alignment cannot be shown. |
| [budget-exceeded](#budget-exceeded) | 6 | error | A stage or section is larger than its size budget. |
| [org-overlap](#org-overlap) | 6 | error | A .org or `. =` target is behind, or the same as, an earlier one in the section. |
| [page-table-alignment](#page-table-alignment) | 6 | error | A page table is not 4 KiB aligned. |
| [section-overlap](#section-overlap) | 6 | error | Sections a linker script places at fixed addresses collide. |
| [simd-alignment](#simd-alignment) | 6 | error | An aligned SIMD move (movdqa, movaps, vmovdqa ...) names a label that is not aligned to the vector width. |
| [stack-frame-align](#stack-frame-align) | 6 | warning | An aligned SSE access (movaps, movdqa, ...) to a stack slot that is not 16-byte aligned. |
| [table-alignment](#table-alignment) | 6 | info | GDT or IDT base not 8-byte aligned (16 for a long mode IDT), or a table whose alignment depends on section placement. |
| [cr-reserved-write](#cr-reserved-write) | 7 | warning | Constant written to CR0, CR4, XCR0 or EFER that sets reserved bits or an invalid combination. |
| [msr-reserved-write](#msr-reserved-write) | 7 | warning | wrmsr with a constant value that sets reserved bits, writes a read-only MSR or a reserved memory type. |
| [bt-mem](#bt-mem) | 8 | info | bt/bts/btr/btc with a register bit offset into memory: bit-string addressing, microcoded and slow. |
| [div-by-constant](#div-by-constant) | 8 | hint | div/idiv by a register holding a known constant: a shift or a multiply by the reciprocal is faster. |
| [false-dependency](#false-dependency) | 8 | info | popcnt/lzcnt/tzcnt into a register that was not zeroed first: a false dependency on its old value. |
| [inc-no-carry](#inc-no-carry) | 8 | warning | Branch, setcc or cmovcc on CF right after inc/dec, which do not set CF: it tests a stale carry. |
| [lea-three-component](#lea-three-component) | 8 | info | lea with base, index and displacement: 3-cycle latency on Sandy Bridge through Ice Lake. |
| [missing-vzeroupper](#missing-vzeroupper) | 8 | warning | call, ret or jump out of the document while ymm/zmm upper halves are dirty: add vzeroupper first. |
| [partial-flags-stall](#partial-flags-stall) | 8 | info | CF read while the latest flag writer is inc/dec: the flags must be merged from two instructions. |
| [slow-enter](#slow-enter) | 8 | info | enter with a nesting level, or any enter in 64-bit code: microcoded; push/mov/lea is faster. |
| [slow-loop](#slow-loop) | 8 | info | loop/loope/loopne in 32- or 64-bit code: microcoded on Intel cores; dec + jnz is faster. |
| [xchg-mem-lock](#xchg-mem-lock) | 8 | info | xchg with a memory operand is implicitly locked: atomic, and as slow as any locked instruction. |
| [asm-early-clobber](#asm-early-clobber) | 9 | warning | An output is written before an input is read, but its constraint has no early-clobber '&'. |
| [asm-hardcoded-reg](#asm-hardcoded-reg) | 9 | warning | The template reads a fixed register that an "r"-class operand could also be allocated to. |
| [asm-missing-clobber](#asm-missing-clobber) | 9 | warning | The template writes a register that is neither an output operand nor in the clobber list. |
| [asm-missing-memory-clobber](#asm-missing-memory-clobber) | 9 | warning | The template stores to memory that is not a memory operand, and "memory" is not clobbered. |
| [asm-missing-volatile](#asm-missing-volatile) | 9 | warning | Asm with outputs and side effects is not volatile, so GCC may delete, merge or hoist it. |
| [asm-operand-ref](#asm-operand-ref) | 9 | warning | Operand reference out of range, unknown %[name], %l outside asm goto, or an unknown operand modifier. |
| [asm-operand-width](#asm-operand-width) | 9 | warning | C operand width disagrees with the instruction's size suffix or the operand modifier. |
| [asm-operand-width-typedef](#asm-operand-width-typedef) | 9 | info | As asm-operand-width, for typedefs whose width is assumed by convention (UINT32, u8, DWORD, UINTN). |
| [duplicate-definition](#duplicate-definition) | 10 | warning | A global symbol also defined by a file assembled into the same program. |
| [duplicate-label](#duplicate-label) | 10 | error | The same label defined twice in one file (outside alternative #if/.if branches). |
| [extern-unused](#extern-unused) | 10 | info | An .extern declaration whose symbol the file never uses. |
| [globl-undefined](#globl-undefined) | 10 | warning | .globl/.global naming a symbol this file does not define and nothing in the workspace defines. |
| [local-label-far](#local-label-far) | 10 | info | A 1b/1f that resolves across a global or @function label, into another function. |
| [local-label-unresolved](#local-label-unresolved) | 10 | error | A 1f with no following 1:, or a 1b with no preceding one. |
| [undefined-label](#undefined-label) | 10 | warning | A branch, call, $symbol or data reference to a symbol defined nowhere in the workspace. |
| [unused-define](#unused-define) | 10 | info | A #define, .set or .equ symbol that neither the file nor the files including it use. |
| [unused-label](#unused-label) | 10 | info | A label that is not global and that nothing references, in the file or the workspace. |
| [unused-suppression](#unused-suppression) | 12 | info | A falcon-asm: ignore comment that suppresses nothing. |

## addr-invalid-reg

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Register that cannot address memory: %esp as an index, %rip outside 64-bit code, mixed base/index widths.

%esp and %rsp cannot be an index (that SIB encoding means "no index"), %rip-relative addressing exists only in 64-bit code and takes no index, the base and index of one address have one width, segment and vector registers are not general registers (a vector index is only for gather and scatter), and the scale is 1, 2, 4 or 8. A quick fix swaps base and index when %esp is the index with scale 1.

Bad:

```asm
	movl (%eax,%esp), %ebx
```

Good:

```asm
	movl (%esp,%eax), %ebx
```

## addr16-invalid

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Invalid 16-bit addressing: (%si,%di), (%ax), a scaled index, or 16-bit registers in 64-bit code.

16-bit addressing has only eight forms: a base of %bx or %bp, an index of %si or %di, either or both, with no scale. (%ax), (%si,%di), (%bx,%bp) and (%bx,%si,2) do not exist, addr32 cannot use them, and 64-bit code has no 16-bit addressing at all. Real-mode code written with 32-bit habits trips on these. A quick fix swaps a reversed pair such as (%si,%bx).

Bad:

```asm
	.code16
	movw (%si,%di), %ax
```

Good:

```asm
	.code16
	movw (%bx,%di), %ax
```

## avx-decoration

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

AVX-512 decoration errors: {%k0} as a mask, {z} without a mask, broadcast width mismatch, mixed vector widths.

%k0 as a write mask encodes "no masking"; {z} needs a mask and cannot apply to a memory destination; the mask goes on the destination; a broadcast {1toN} must fill the vector with elements of the instruction's size (a zmm of dwords is {1to16}); EVEX gathers and scatters need a completion mask; and an instruction whose operands share one width cannot mix xmm, ymm and zmm. Conversions, extracts, inserts and broadcasts whose forms legitimately mix widths are matched from the tables and not reported.

Bad:

```asm
	vaddps (%rax){1to8}, %zmm1, %zmm2{%k0}
```

Good:

```asm
	vaddps (%rax){1to16}, %zmm1, %zmm2{%k1}
```

## branch-out-of-range

- Section: 1 (Encoding and operand validity)
- Default severity: error
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

loop/jcxz/jecxz target beyond the rel8 range, from the assembler listing.

loop, loope, loopne, jcxz, jecxz and jrcxz exist only with an 8-bit displacement: the target must lie within -128..+127 bytes of the end of the instruction. Unlike jmp and jcc, GAS cannot relax them to a longer form, so a loop body that grows past the limit stops assembling. The distance is measured from the listing of the last assembly, when both addresses are known and in one section.

Bad:

```asm
1:
	.fill 200, 1, 0x90
	loop 1b
```

Good:

```asm
1:
	.fill 200, 1, 0x90
	decl %ecx
	jnz 1b
```

## data-range

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Data directive value that does not fit its width (.byte 300, .word 0x10000).

.byte stores 8 bits, .word/.short/.value 16 and .long/.int 32. GAS truncates a larger value with only a warning, and wraps a small negative one (.byte -129) without any; in a descriptor table or a boot sector signature that is a silent corruption. Values built from labels are not judged.

Bad:

```asm
	.byte 0x155
```

Good:

```asm
	.word 0x155
```

## fixed-reg-form

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Operand that must be a fixed register: in/out data and port, shift count (in %dx, %bl).

in and out move data only through %al, %ax or %eax and take the port as an imm8 or %dx; a shift or rotate count is an immediate or %cl. Any other register is rejected by GAS. A quick fix renames a register of the right family (%ecx to %cl as a count, %edx to %dx as a port).

Bad:

```asm
	inb %dx, %bl
	shll %ecx, %eax
```

Good:

```asm
	inb %dx, %al
	shll %cl, %eax
```

## imm-range

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Immediate out of range for the instruction (out $0x300, int $256, ret $70000, movb $300).

An immediate field has a fixed width: GAS truncates an oversized value with a warning, rejects it, or for small negative values wraps it silently; in each case the code does not do what it says. Ports above 0xff need %dx, interrupt vectors are 0-0xff, ret pops at most 0xffff bytes. A 64-bit operation sign-extends its imm32, so $0xffffffff is not 0x00000000ffffffff there and GAS rejects it (mov to a register is the exception: it becomes movabs). Only constants that evaluate the same under GAS and C precedence are judged.

Bad:

```asm
	outb %al, $0x300
	addq $0xffffffff, %rax
```

Good:

```asm
	movw $0x300, %dx
	outb %al, %dx
	movl $0xffffffff, %ecx
	addq %rcx, %rax
```

## operand-no-form

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

No form of the instruction takes these operands (lgdt %eax: lgdt needs memory).

Every instruction has a fixed set of operand forms: lgdt and lidt read a 6- or 10-byte pseudo-descriptor from memory, lea needs a memory operand, an immediate is never a destination. The operands are matched against the instruction's forms from the ISA tables and the message lists the valid ones. When the problem is more specific (a size mismatch, a segment register, a fixed register, a decoration) that rule reports instead. Branches, string and x87 instructions are left to the assembler, whose operand rules there do not fit the tables.

Bad:

```asm
	lgdt %eax
```

Good:

```asm
	lgdt gdt_descriptor
```

## operand-size-ambiguous

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Memory operand with no suffix and no register to give its size (inc (%bx)).

Without a suffix or a register operand nothing says whether inc (%bx) increments a byte, a word or a dword. GAS picks a default and warns, or for movzx and movsx from memory silently assumes a byte; either way the size was never written down. push and pop are not reported: they default to the stack width by design.

Bad:

```asm
	incw (%bx)
	inc (%bx)
```

Good:

```asm
	incw (%bx)
	incb (%bx)
```

## operand-size-mismatch

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Operand sizes disagree with the suffix or with each other (movw %eax, %bx).

The AT&T suffix names the operand size, and the register operands must agree with it and with each other. GAS rejects the mismatch, but only after the edit-assemble cycle; the usual cause is a register renamed for a new mode while the suffix stayed. Forms whose operands legitimately differ in size (movzbl, the %cl count of a shift, the %dx port of in and out) are matched from the tables and not reported. A quick fix changes the suffix when the registers agree on one size.

Bad:

```asm
	movw %eax, %ebx
```

Good:

```asm
	movl %eax, %ebx
```

## prefix-misuse

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Prefix misuse: lock without a memory destination, rep on a non-string instruction, repeated or conflicting prefixes.

lock applies only to read-modify-write instructions with a memory destination (add, xchg, cmpxchg, inc ...). rep repeats string instructions (rep ret, rep nop and rep bsf/bsr are accepted idioms). Two prefixes of one group (lock and rep, two segment overrides, data16 twice) are rejected or one silently wins, as does a segment prefix against a different override in the operand. addr32 in 32-bit code and data16 on a w-suffixed instruction outside 16-bit code are redundant, and GAS rejects them.

Bad:

```asm
	lock addl %eax, %ebx
	rep addl %eax, %ebx
```

Good:

```asm
	lock addl %eax, (%ebx)
	rep movsb
```

## rex-high-byte

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

%ah/%bh/%ch/%dh in an instruction that needs a REX prefix (movb %ah, %sil).

With a REX prefix the encodings of %ah, %ch, %dh and %bh name %spl, %bpl, %sil and %dil instead, so a high-byte register cannot appear in any instruction that needs REX: one using r8-r15, the uniform byte registers, a 64-bit operand or xmm8-15. GAS rejects it; the fix is to move the byte through %al or another low register.

Bad:

```asm
	.code64
	movb %ah, %sil
```

Good:

```asm
	.code64
	movb %ah, %al
	movb %al, %sil
```

## sreg-misuse

- Section: 1 (Encoding and operand validity)
- Default severity: warning
- Runs on: assembly, inline asm

Segment register misuse: mov to %cs, pop %cs, lds with a register, push %ds in 64-bit code.

Segment registers have few and irregular forms. mov into %cs assembles but raises #UD; %cs is loaded by a far jmp, call or ret. pop %cs does not exist. lds/les/lss/lfs/lgs load a far pointer from memory. push and pop of %es, %cs, %ss and %ds are invalid in 64-bit code. There is no immediate or segment-to-segment mov, byte registers cannot be used, and a store to memory is always 16 bits (a quick fix changes movl to movw).

Bad:

```asm
	movw %ax, %cs
```

Good:

```asm
	ljmp $0x08, $1f
1:
```

## cpu-unsupported

- Section: 2 (Mode, CPU and ISA target)
- Default severity: warning
- Runs on: assembly, inline asm

Instruction the target CPU profile does not implement (cmov on a Pentium, cpuid on a 386).

A CPU target (`target.cpu` in .falcon-asm.json, or a -march that names a processor such as i586 or pentium3) promises the code runs on that processor. An instruction from a later generation assembles without complaint and raises #UD on the old machine, typically the one nobody tests on. The message names the ISA extension and the first profile that has it. Profiles list only what every part of a generation implements, so the rule errs toward reporting; an instruction guarded by a CPUID check can be silenced on its line. With only an x86-64 level target the level rules apply instead.

Bad:

```asm
# .falcon-asm.json: {"target": {"cpu": "pentium"}}
	cmovel %ebx, %eax
```

Good:

```asm
# .falcon-asm.json: {"target": {"cpu": "pentiumpro"}}
	cmovel %ebx, %eax
```

## far-jump-mode

- Section: 2 (Mode, CPU and ISA target)
- Default severity: warning
- Runs on: assembly

Far jump into code assembled for a different mode than the selector's descriptor runs it in.

`ljmp $SEL, $label` loads CS from the GDT entry SEL names, and the descriptor's D and L bits, not the .code16/.code32/.code64 directive, decide how the processor decodes what it finds at label. A 32-bit code selector in front of a .code16 label (or the reverse) runs the bytes with the wrong operand and address size, which usually triple faults a few instructions later with nothing on the screen. The rule finds the GDT through the lgdt in the same file, decodes the entry and compares; the finding links to the entry. Jumps in 16-bit code count only right after a CR0 write that sets PE, so a real-mode `ljmp $0x07C0, $start` is never read as a selector.

Bad:

```asm
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	ljmpl $0x08, $pm
	.code16
pm:
	hlt
gdt:	.quad 0
	.quad 0x00CF9A000000FFFF
gdt_end:
gdt_desc: .word gdt_end - gdt - 1
	.long gdt
```

Good:

```asm
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	ljmpl $0x08, $pm
	.code32
pm:
	hlt
gdt:	.quad 0
	.quad 0x00CF9A000000FFFF
gdt_end:
gdt_desc: .word gdt_end - gdt - 1
	.long gdt
```

## feature-unguarded

- Section: 2 (Mode, CPU and ISA target)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Instruction needing a CPUID feature (rdtsc, cmov, AVX2, rdrand) with no CPUID test of it on every path, beyond what the target guarantees.

An instruction from an extension the processor lacks raises #UD. Code that must run on older machines tests the CPUID bit first and takes another path when it is clear. The rule looks, in the instruction's function, for a cpuid whose leaf the dataflow knows followed by a test, bt or and of the output register covering the feature's bit, and reports the instruction when no such test dominates it (every path from the function's entry passes one). Nothing is reported when the target guarantees the feature: an x86-64 level or CPU profile that includes it, or 64-bit code for the x86-64 baseline (SSE2, CMOV, TSC ...). Information by default, because the test often lives in another function (a startup check that refuses to boot without the feature); silence it per line or per file there.

Bad:

```asm
	.code32
f:
	rdrand %eax
	ret
```

Good:

```asm
	.code32
f:
	movl $1, %eax
	cpuid
	btl $30, %ecx
	jnc 1f
	rdrand %eax
1:	ret
```

## level

- Section: 2 (Mode, CPU and ISA target)
- Default severity: warning
- Runs on: assembly, inline asm

Instruction form above the x86-64 level the file targets (v1-v4).

A target level (from -march=x86-64-vN, the falcon-asm.level setting, .falcon-asm.json or a `falcon-asm: level=vN` comment) promises the code runs on every CPU of that level. An AVX2 or BMI2 instruction in a v1 file assembles fine and then faults with #UD on the first machine without the extension. The operand registers select the form family, so `vpaddd %ymm...` is judged as AVX2 even though the xmm form is AVX.

Bad:

```asm
# falcon-asm: level=v2
	andn %eax, %ebx, %ecx
```

Good:

```asm
# falcon-asm: level=v3
	andn %eax, %ebx, %ecx
```

## level-outside

- Section: 2 (Mode, CPU and ISA target)
- Default severity: info
- Runs on: assembly, inline asm

Instruction outside the x86-64 level scheme (AES, SHA, VMX ...) in a file with a level target.

The psABI levels do not cover every extension: AES-NI, PCLMULQDQ, SHA, VMX and the newer AVX-512 subsets are separate CPUID bits on top of any level. A level target therefore says nothing about them, and code using one needs its own CPUID check. Reported as information, since the instruction may well be guarded.

Bad:

```asm
# falcon-asm: level=v3
	aesenc %xmm1, %xmm0
```

Good:

```asm
# falcon-asm: level=v3
	# CPUID.01H:ECX.AES checked by the caller
	aesenc %xmm1, %xmm0 # falcon-asm: ignore=level-outside
```

## mode-instruction

- Section: 2 (Mode, CPU and ISA target)
- Default severity: warning
- Runs on: assembly, inline asm

Instruction not valid in the .code16/.code32/.code64 mode in effect.

gas assembles for the mode the last .codeNN selected (or the -m flag), and some instructions only exist in some modes: pusha and the BCD instructions are gone in 64-bit code, swapgs and iretq exist only there. Boot code switches modes several times in one file, and an instruction moved across a .code32 or .code64 line is either rejected or, worse, encodes as something else. The message names the directive that set the mode.

Bad:

```asm
	.code64
	pusha
```

Good:

```asm
	.code32
	pusha
```

## mode-register

- Section: 2 (Mode, CPU and ISA target)
- Default severity: warning
- Runs on: assembly, inline asm

Register not encodable in the mode in effect (%rax, %r8d, %sil, %xmm8 outside 64-bit code).

The 64-bit registers, r8-r15, the uniform byte registers and xmm8 and up need a REX prefix, which only exists in 64-bit mode. In 16- or 32-bit code gas rejects them, and a copy-paste from long-mode code into the real-mode entry is the usual way they get there. A quick fix offers the register's legacy name where one exists.

Bad:

```asm
	.code32
	movl $1, %rax
```

Good:

```asm
	.code32
	movl $1, %eax
```

## bios-cf-unchecked

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

BIOS call that reports errors in CF whose CF is overwritten before anything tests it.

INT 13h, most of INT 15h and the PCI BIOS report failure by returning with CF set (and a status in AH). Ignoring it turns a failed disk read into a jump into whatever the buffer held. The rule finds the service from AH (and AL) at the int, and follows every path after it: a jc/jnc/jb/jae, adc, sbb, setc, pushf or anything else reading CF checks it; an instruction that writes CF first (cmp, add, clc, another int) loses it. A path that returns hands CF to the caller, so the call sites in the document are checked the same way. A call on the path, or a service the dataflow cannot name, stops the check.

Bad:

```asm
	movw $0x0201, %ax
	int $0x13
	cmpw $0xAA55, 0x7DFE
	jne bad
```

Good:

```asm
	movw $0x0201, %ax
	int $0x13
	jc disk_error
	cmpw $0xAA55, 0x7DFE
	jne bad
```

## bios-clobber

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

Register a BIOS service is documented to destroy, read after the int without being reloaded.

Some services change registers beyond their outputs: INT 10h AH=00h returns the mode byte in AL and leaves AH undefined, INT 13h AH=08h loads ES:DI with a table pointer and clobbers AL, INT 15h AH=87h destroys AX. The rule names the service from AH (and AL) at the int and reports a read of a documented clobber that every path reaches without a write in between. Outputs are not clobbers: reading CX after INT 13h AH=08h is the point of the call. A push is not a use, and a register saved before the int and popped after it is its old self again.

Bad:

```asm
	movw $0x0003, %ax
	int $0x10
	movb %ah, %bl
```

Good:

```asm
	movw $0x0003, %ax
	int $0x10
	movb $0x0F, %ah
	int $0x10
	movb %ah, %bl
```

## bios-clobber-quirk

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Register some BIOSes destroy across a call although no reference says so (BP over INT 10h), read after the int.

Old video BIOSes destroy BP during scroll and teletype output, some E820 implementations change EDX. No specification allows it, but the machines exist and boot code written for them saves the register or reloads it. Reported like bios-clobber, at Information, because QEMU and modern firmware preserve the register and the code works there.

Bad:

```asm
	movw $0x0E41, %ax
	int $0x10
	movw (%bp), %si
```

Good:

```asm
	pushw %bp
	movw $0x0E41, %ax
	int $0x10
	popw %bp
	movw (%bp), %si
```

## busy-spin

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Infinite loop with no hlt or pause: a jmp to itself, or a loop whose exit test nothing in it can change.

`jmp .` stops a program but keeps the core running flat out, which on a laptop or a VM host is heat and a stolen CPU; `cli; 1: hlt; jmp 1b` stops it for real (hlt alone, with interrupts on, to idle). A loop whose exit condition depends only on registers it never writes, and that reads no memory or I/O port, either exits at once or never: usually a wrong register in the test or a missing decrement. Loops that read memory or ports, call anything, or contain hlt or pause are polling and are not reported.

Bad:

```asm
fatal:
	cli
	jmp .
```

Good:

```asm
fatal:
	cli
1:	hlt
	jmp 1b
```

## cli-sti-pair

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Function that disables interrupts with cli and returns on some path without sti or popf.

A cli that is undone with sti (or by popf restoring saved flags) on the normal path but not on an early return leaves interrupts disabled in the caller, which shows up much later as a lost timer tick or a keyboard that stops responding. Only functions that re-enable interrupts on some path after the cli are checked; mode-switch code (CR0 writes, lgdt, far jumps), boot entries and interrupt handlers keep interrupts off on purpose.

Bad:

```asm
f:
	cli
	testw %ax, %ax
	jz 1f
	sti
1:	ret
```

Good:

```asm
f:
	cli
	testw %ax, %ax
	jz 1f
	nop
1:	sti
	ret
```

## df-unknown

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

String instruction reached with the direction flag not provably clear (no cld on some path).

movs, stos, lods, scas, cmps, ins and outs step their index registers up when DF is 0 and down when it is 1. Code written for one direction that runs with the other copies backwards over the wrong memory. The BIOS makes no promise about DF at the boot sector's entry, and an interrupt handler runs with whatever the interrupted code had, so both must execute cld before their first string instruction. The rule reports a string instruction when a cld runs on some paths to it but not on others (a std or the entry state reaches it), and, once per function, the first string instruction of a boot entry or interrupt handler that no cld precedes. A DF that is unknown because of a call, an int or a popf is not reported: the analysis cannot see what the callee left. The fix inserts cld before the instruction.

Bad:

```asm
_start:
	xorw %ax, %ax
	movw %ax, %es
	movw $0x500, %di
	movw $64, %cx
	rep stosw
```

Good:

```asm
_start:
	cld
	xorw %ax, %ax
	movw %ax, %es
	movw $0x500, %di
	movw $64, %cx
	rep stosw
```

## fallthrough-into-data

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

Code that falls through into data (a string, a table, padding) instead of jumping or returning.

When the last instruction of a path neither jumps nor returns, the processor executes whatever bytes follow: the message string or the GDT after the function decodes as instructions. The rule reports a reachable fallthrough into a labeled data directive, a string or a fill. Unlabeled .byte/.word/.long right after code are taken to be hand-encoded instructions (a far jump or a prefix gas would not emit), and a call or hlt before data (a call that never returns, a halt) is not a fallthrough.

Bad:

```asm
f:
	movw $msg, %si
	call puts
	movw $1, %ax
msg:
	.asciz "hi"
```

Good:

```asm
f:
	movw $msg, %si
	call puts
	movw $1, %ax
	ret
msg:
	.asciz "hi"
```

## flags-clobbered

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

Flags of a cmp/test overwritten before the conditional instruction that was meant to read them.

A jcc, setcc, cmovcc, adc or sbb reads the flags of the last instruction that wrote them. An inc, add, shift or call slipped in between a cmp and its jump (a pointer bump moved up, a debug print added) silently changes what the jump tests. The rule reports a flag reader whose flags come from such an instruction when, in the same block, a cmp or test before it wrote those flags and nothing read them in between: the cmp's result is dead, which is what makes the clobber a mistake rather than a deliberate test of the inc. Carry chains (add then adc), flags restored with popf or sahf, and BIOS calls (which return status in the flags) are not reported.

Bad:

```asm
	cmpb $0, (%si)
	incw %si
	jne next
```

Good:

```asm
	incw %si
	cmpb $0, -1(%si)
	jne next
```

## if-enabled-critical

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

BIOS call inside a cli section that returns with interrupts enabled before a mode switch (INT 15h ends with sti).

A BIOS handler is free to enable interrupts, and several do whatever flags the caller had: SeaBIOS ends its INT 15h handler with sti, and INT 13h and INT 16h enable interrupts while they wait for the hardware. Code that executes cli, calls one of them and then goes on to lgdt, a CR0 write or a far jump into protected mode runs that sequence with interrupts on: a timer tick in the middle lands in a real-mode handler with half-switched state and the machine triple-faults or wanders into the ROM. The rule reports the int when interrupts were disabled before it (on every path) and a mode-switch step is reached after it with no cli in between. The fix repeats cli after the int.

Bad:

```asm
	cli
	movw $0x2401, %ax
	int $0x15
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
```

Good:

```asm
	cli
	movw $0x2401, %ax
	int $0x15
	cli
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
```

## partial-register-read

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

32/64-bit read of a register whose last write set only its low 8 or 16 bits (movw $x, %ax ... %eax).

Writing %ax or %al leaves the rest of %eax as it was; only a 32-bit write in 64-bit mode zero-extends. Real-mode code that loads a value with movw and then uses %eax (an address for addr32, a 32-bit compare, a CR or MSR write) uses whatever the upper half held before, which on real hardware is rarely zero. The rule reports a 32- or 64-bit read when the last write on every path was narrower and the upper bits are not known on some path. When the upper half was zeroed or written separately (xorl %eax, %eax first) the bits are known and nothing is reported. A push is not a use: saving a register keeps whatever it holds. The fix widens a movw of an immediate to movl.

Bad:

```asm
	.code16
f:
	movw $0x1000, %ax
	movl %eax, %ebx
	ret
```

Good:

```asm
	.code16
f:
	movl $0x1000, %eax
	movl %eax, %ebx
	ret
```

## pushf-popf-pair

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Function that saves the flags with pushf but returns on some path without popf.

A function that begins with pushf and restores the flags with popf before returning promises its caller the interrupt and direction flags come back as they went in. A path to ret that skips the popf (an error exit added later) breaks that promise silently: interrupts stay off after the call, or a std leaks out. Reported at the ret, only for functions that do restore with popf somewhere, so the save/restore intent is clear.

Bad:

```asm
f:
	pushfw
	cli
	testw %ax, %ax
	jz 1f
	popfw
	ret
1:	addw $2, %sp
	ret
```

Good:

```asm
f:
	pushfw
	cli
	testw %ax, %ax
	jz 1f
	popfw
	ret
1:	popfw
	ret
```

## segment-unset

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

Memory access through a segment register the boot entry never set (%es:(%di) stores, %ds loads in real mode).

In real mode every memory access is segment * 16 + offset, and the BIOS promises nothing about DS, ES, FS or GS when it jumps to a boot sector: QEMU happens to leave zeros, many real machines do not. A boot sector that stores through ES:DI (stos, movs, ins, or an %es: override) or reads a variable through DS before loading the register writes or reads some other 64 KiB of memory. The rule runs only on the function the BIOS enters (the first code of a 16-bit file that lays out a boot sector, or _start), and reports the first use of each segment register that no path from the entry has written. Code reached by a call or a far jump is not checked: its caller may well have set the register.

Bad:

```asm
_start:
	movw $0x500, %di
	movw $0, %ax
	stosw
	. = _start + 510
	.word 0xAA55
```

Good:

```asm
_start:
	xorw %ax, %ax
	movw %ax, %es
	movw $0x500, %di
	stosw
	. = _start + 510
	.word 0xAA55
```

## self-modifying-code

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Store into an instruction in an executable section (self-modifying code).

Patching an instruction (the immediate of a far jump, an opcode) works in real mode and is an old boot loader trick, but it is invisible in the source, it breaks the moment the code is placed in ROM or a write-protected page, and on processors since the i486 a write close ahead of the executing instruction may or may not be seen without a jump in between. The rule reports a store whose memory operand names a label of code (an instruction, not a data directive) in .text or a section with the x flag, as Information: it is sometimes the point. Variables kept in .text (a data label) are not code and are not reported.

Bad:

```asm
	movw %ax, patch+1
patch:
	movw $0, %bx
```

Good:

```asm
	movw %ax, value
	movw value, %bx
	.section .data
value:
	.word 0
```

## stack-imbalance

- Section: 3 (Dataflow)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

ret/iret with pushed bytes still on the stack, or paths that join with different stack depths.

ret pops the return address from wherever the stack pointer is. A push without its pop on some path (an early exit taken between the two, typically) makes ret jump to the pushed value. The rule tracks the stack depth relative to the function's entry and reports a ret, lret or iret reached with a known non-zero depth, and a block that two forward paths reach with different known depths, which is where the imbalance starts. A depth that is unknown (an untracked stack pointer write) is never reported, a loop that pushes once per iteration is not a join of two paths, and `push $target; ret` (a jump through ret, the top of the stack an immediate or a memory value) is taken as deliberate.

Bad:

```asm
f:
	pushw %bx
	testw %ax, %ax
	jz 1f
	popw %bx
1:	ret
```

Good:

```asm
f:
	pushw %bx
	testw %ax, %ax
	jz 1f
	nop
1:	popw %bx
	ret
```

## uninit-read

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Boot entry code reading a register nothing has set since the BIOS jumped to it (a rep count, an I/O port in %dx).

When the BIOS jumps to a boot sector the only register with a defined meaning is DL (the boot drive); AX, BX, CX, BP and the high byte of DX hold whatever the BIOS last used. A rep whose count was never loaded copies a random number of bytes, an in or out whose port was never loaded talks to a random device. The rule only looks at the function the BIOS enters (see segment-unset) and reports the first read of each register that no path from the entry has written. SI and DI are left out because chain loaders pass a partition entry in DS:SI and PnP BIOSes a pointer in ES:DI.

Bad:

```asm
_start:
	cld
	xorw %ax, %ax
	movw %ax, %es
	movw $0x500, %di
	rep stosb
	. = _start + 510
	.word 0xAA55
```

Good:

```asm
_start:
	cld
	xorw %ax, %ax
	movw %ax, %es
	movw $0x500, %di
	movw $16, %cx
	rep stosb
	. = _start + 510
	.word 0xAA55
```

## unreachable-code

- Section: 3 (Dataflow)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Code after a jmp, ret or halt that nothing jumps to.

An instruction with no label after an unconditional transfer can only run if something jumps into the middle of it, which nothing in the document does. It is usually the leftover of an edit (a jmp added above the old path) or a missing label. Labels whose address escapes (global, $label, data) are entries already and are never reported, and padding is exempt: a run of nop, int3, hlt or ud2, which is how the BPB jump-over (jmp start; nop) and alignment fill look.

Bad:

```asm
f:
	ret
	movw $1, %ax
	ret
```

Good:

```asm
f:
	ret
g:
	movw $1, %ax
	ret
```

## cc-annotation

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

A falcon-asm calling convention or handler annotation that cannot be understood.

Annotations select the calling convention of a file or a function (`falcon-asm: cc=sysv`, `cc=custom clobbers=ax,cx preserves=bx,si,di args=ax returns=ax`) and mark interrupt handlers (`falcon-asm: handler=errcode vector=14`). A misspelled convention or register, lists without cc=custom, or an annotation on a label that is not a function start would otherwise be ignored silently, and the checks the author asked for would quietly not run. Known conventions: sysv, ms, cdecl, stdcall, fastcall, regparm(N), custom and none. An annotation on the label's line, or above it with only comments and declaration directives (.globl, .type, .align) between, applies to that function; anywhere else it is the file's default.

Bad:

```asm
/* falcon-asm: cc=custom clobbers=ax,cx preserves=bx,sx */
helper:
	ret
```

Good:

```asm
/* falcon-asm: cc=custom clobbers=ax,cx preserves=bx,si */
helper:
	ret
```

## cc-arg-unset

- Section: 4 (Calling conventions)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

A call to a function in the document that reads an argument register the caller never set.

The callee's own dataflow says which argument registers it reads before writing (rdi and rsi under SysV, args= of a custom convention, ecx and edx under fastcall). If the caller has not written one of them on any path to the call, and does not receive it as an argument itself, the callee works on whatever the register happened to hold. Reported as Information because a register can be set deliberately far away (a global register convention).

Bad:

```asm
# falcon-asm: cc=cdecl

# falcon-asm: cc=fastcall
add2:
	leal (%ecx,%edx), %eax
	ret
main:
	movl $1, %ecx
	call add2
	ret
```

Good:

```asm
# falcon-asm: cc=cdecl

# falcon-asm: cc=fastcall
add2:
	leal (%ecx,%edx), %eax
	ret
main:
	movl $1, %ecx
	movl $2, %edx
	call add2
	ret
```

## cc-callee-saved

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

A callee-saved register is changed and not restored before the function returns.

Every convention names registers a function must hand back as it found them: rbx, rbp and r12-r15 under SysV; those plus rdi, rsi and xmm6-xmm15 under MS x64; ebx, esi, edi and ebp under the i386 conventions; whatever a custom convention lists in preserves=. A caller keeps live values there across the call, and the compiler that generated it will not reload them, so the damage surfaces far from the function that did it. The dataflow follows push/pop pairs (and pusha/popa, leave, spill slots), so a saved and restored register is quiet; the report is on each ret a changed value can reach, with the write as the related location. For SysV and MS x64, loading the MXCSR or the x87 control word without saving it first is reported too.

Bad:

```asm
# falcon-asm: cc=cdecl

count:
	movl 4(%esp), %ebx
	movl (%ebx), %eax
	ret
```

Good:

```asm
# falcon-asm: cc=cdecl

count:
	pushl %ebx
	movl 8(%esp), %ebx
	movl (%ebx), %eax
	popl %ebx
	ret
```

## cc-caller-saved-read

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

A caller-saved register is read after a call that may have changed it.

A call to a function with a known convention (one in the document with its own, or any other under the file's) may change every register the convention does not make callee-saved: rcx, rsi, rdi and r8-r11 under SysV, ecx under cdecl, and so on. Reading one after the call, before writing it, reads whatever the callee left there; the code works until the callee changes. Result registers (rax, rdx under SysV; eax, edx under the i386 conventions) are not reported, since reading them is the point of the call. For a callee in the document only the registers its code actually changes are reported.

Bad:

```asm
# falcon-asm: cc=cdecl

	movl $10, %ecx
	call putc
	decl %ecx
```

Good:

```asm
# falcon-asm: cc=cdecl

	movl $10, %ebx
	call putc
	decl %ebx
```

## cc-red-zone

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

A memory access below rsp that the convention does not protect.

SysV guarantees 128 bytes below rsp (the red zone) that signal and interrupt delivery will not touch; MS x64 guarantees none, so anything stored below rsp there can be overwritten at any moment by an interrupt, an exception dispatch or a debugger. Accesses through rsp with a negative displacement are reported under MS x64, and those beyond -128 under SysV. (Code built with -mno-red-zone, kernels and firmware, has no red zone even under SysV; declare it with a custom convention or silence the rule.)

Bad:

```asm
# falcon-asm: cc=ms

f:
	movq %rcx, -8(%rsp)
	ret
```

Good:

```asm
# falcon-asm: cc=ms

f:
	subq $8, %rsp
	movq %rcx, (%rsp)
	addq $8, %rsp
	ret
```

## cc-return-unset

- Section: 4 (Calling conventions)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

A function whose callers read its result returns on some path without setting it.

When a caller reads the result register (rax/eax, rdx/edx for a pair, or a custom returns=) right after calling a function in the document, the function evidently returns a value; a ret reached without writing that register returns the caller's own leftover value instead. Usually an early-exit path that forgot to set the error code. Information, since the evidence is the callers' use rather than a declaration.

Bad:

```asm
# falcon-asm: cc=cdecl

get:
	testl %ecx, %ecx
	jz 1f
	movl $1, %eax
1:	ret
main:
	call get
	movl %eax, result
	ret
```

Good:

```asm
# falcon-asm: cc=cdecl

get:
	xorl %eax, %eax
	testl %ecx, %ecx
	jz 1f
	movl $1, %eax
1:	ret
main:
	call get
	movl %eax, result
	ret
```

## cc-shadow-space

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

An MS x64 call without the 32 bytes of shadow space the callee may write.

Under the MS x64 convention the caller reserves 32 bytes directly above the return address, where the callee may spill its four register arguments. A callee that does (any compiled one built without optimization, and many with) overwrites whatever the caller keeps there: a saved register, a local, its own return address. The space counted is what the function allocated since its last push, when the stack depth is known.

Bad:

```asm
# falcon-asm: cc=ms

f:
	pushq %rbx
	call g
	popq %rbx
	ret
```

Good:

```asm
# falcon-asm: cc=ms

f:
	pushq %rbx
	subq $32, %rsp
	call g
	addq $32, %rsp
	popq %rbx
	ret
```

## cc-stack-align

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

The stack is not 16-byte aligned at a call (SysV and MS x64).

Both 64-bit conventions require rsp to be a multiple of 16 at the call instruction, so a function starts with rsp 8 bytes past a 16-byte boundary (the return address). Compiled callees rely on it: movaps to a stack slot faults when it is wrong, usually deep inside a library function. The depth at each call is computed from the function's entry through pushes, sub/add and lea on rsp and the frame pointer; after `and $-16, %rsp` the depth is unknown and nothing is reported. Only functions something calls or that are global are checked, since a program entry point starts with a different stack.

Bad:

```asm
# falcon-asm: cc=sysv

	.globl f
f:
	call g
	ret
```

Good:

```asm
# falcon-asm: cc=sysv

	.globl f
f:
	subq $8, %rsp
	call g
	addq $8, %rsp
	ret
```

## handler-errcode

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

An exception handler returns without discarding the error code its vector pushes (or discards one that is not there).

In protected and long mode the processor pushes an error code for vectors 8, 10-14, 17, 21, 29 and 30, and not for the others. iret expects the return address on top of the stack, so a handler for one of those vectors must remove the error code first (add $4, %esp; add $8, %rsp in 64-bit code), and a handler for any other vector must not. Getting it wrong returns to the error code as an address. The vector is known from a handler table's index or from `falcon-asm: handler vector=N` (or handler=errcode).

Bad:

```asm
# falcon-asm: handler vector=13
gp_fault:
	incl faults
	iret
```

Good:

```asm
# falcon-asm: handler vector=13
gp_fault:
	incl faults
	addl $4, %esp
	iret
```

## handler-preserve

- Section: 4 (Calling conventions)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

An interrupt or exception handler changes a register it does not restore.

A hardware interrupt or an exception can arrive between any two instructions, so the code it interrupts has no chance to save anything: for a handler every general register is callee-saved. The dataflow follows push/pop and pusha/popa, and a call from the handler to code with a known convention changes that convention's caller-saved registers. Software service handlers (an IVT vector other than the exceptions, IRQs and 1Ch, or `falcon-asm: handler=service`) return results in registers and are not checked.

Bad:

```asm
# falcon-asm: handler vector=32
irq0:
	incl ticks
	movb $0x20, %al
	outb %al, $0x20
	iret
```

Good:

```asm
# falcon-asm: handler vector=32
irq0:
	pushl %eax
	incl ticks
	movb $0x20, %al
	outb %al, $0x20
	popl %eax
	iret
```

## handler-ret

- Section: 4 (Calling conventions)
- Default severity: error
- Runs on: assembly
- Needs: dataflow

An interrupt or exception handler returns with ret or lret instead of iret.

The processor enters a handler with the flags, the code segment and the return address on the stack. ret pops only the address, leaving the rest for the interrupted code to trip over; lret pops the segment too but not the flags, so interrupts stay as the handler left them. A handler is a label installed in the interrupt vector table or taken by IDT setup code (a function that runs lidt), or one marked `falcon-asm: handler`. `lret $2` (or $4), the INT 13h hook idiom that discards the saved flags to return the carry flag, is not reported.

Bad:

```asm
	.code16
	movw $tick, 0x20
	ret
tick:
	incw ticks
	ret
```

Good:

```asm
	.code16
	movw $tick, 0x20
	ret
tick:
	incw ticks
	iret
```

## bios-struct

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly

BIOS structure (disk address packet, E820 buffer ...) built with a wrong size or member.

INT 13h AH=42h reads a disk address packet whose first byte must be 10h or 18h and whose second must be zero; E820h writes 20 or 24 bytes into ES:DI. A packet with the wrong size byte fails on some BIOSes and silently works on others, which is the worst way to find out. The structure is found where the pointer register is loaded with its label before the call (`movw $dap, %si; movb $0x42, %ah; int $0x13`, the move possibly before a label the dataflow follows), or marked with `falcon-asm: struct=dap`.

Bad:

```asm
	movw $dap, %si
	movb $0x42, %ah
	int $0x13
dap:	.byte 0x18, 0
	.word 1, 0x7E00, 0
	.quad 1
```

Good:

```asm
	movw $dap, %si
	movb $0x42, %ah
	int $0x13
dap:	.byte 0x10, 0
	.word 1, 0x7E00, 0
	.quad 1
```

## boot-signature

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

Boot sector whose bytes 510-511 are not 55h AAh.

The BIOS loads a boot sector and jumps to it only when its last two bytes are 55h AAh. The usual mistake is `.word 0x55AA`, which x86 stores little-endian as AA 55, and the sector is silently skipped. A file is taken as a boot sector when it moves to offset 510 (`.org 510`, `. = _start + 510`); the signature must follow, and with the listing its offset from the start label is checked to be 510.

Bad:

```asm
_start:
	cli
	. = _start + 510
	.word 0x55AA
```

Good:

```asm
_start:
	cli
	. = _start + 510
	.word 0xAA55
```

## descriptor-invalid

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly

GDT descriptor that cannot be loaded: L and D both set, a reserved system type, a short TSS, or not present but used.

A code descriptor with both L and D set is reserved and faults when loaded into CS in long mode; a reserved system type or a TSS shorter than 0x67 bytes faults at ltr; a descriptor with P clear raises #NP the moment a selector naming it is loaded. The entries are decoded from the .quad (or .word/.byte) data of a GDT the file loads with lgdt or names gdt. Hover on an entry shows the decoded fields.

Bad:

```asm
gdt:	.quad 0
	.quad 0x00EF9A000000FFFF   # L and D
gdt_end:
gdt_desc: .word gdt_end - gdt - 1
	.long gdt
	lgdt gdt_desc
```

Good:

```asm
gdt:	.quad 0
	.quad 0x00AF9A000000FFFF   # L only: 64-bit code
gdt_end:
gdt_desc: .word gdt_end - gdt - 1
	.long gdt
	lgdt gdt_desc
```

## gdt-limit

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly

GDT or IDT pseudo-descriptor limit is not size - 1.

The limit lgdt and lidt take is the offset of the table's last valid byte, so a table of n eight-byte entries has limit 8n - 1. Writing the size instead (`gdt_end - gdt`) makes one byte past the table addressable, which a selector can then reach; any value that is not 7 modulo 8 cuts the last descriptor short or exposes part of the next one.

Bad:

```asm
gdt_desc:
	.word gdt_end - gdt
	.long gdt
	lgdt gdt_desc
gdt:	.quad 0
gdt_end:
```

Good:

```asm
gdt_desc:
	.word gdt_end - gdt - 1
	.long gdt
	lgdt gdt_desc
gdt:	.quad 0
gdt_end:
```

## loader-sector-count

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow, the assembler listing (runs only after a toolchain run)

INT 13h read of fewer sectors than the stage it loads.

A boot sector reads the next stage with INT 13h: AL sectors for AH=02h, the disk address packet's count for AH=42h. When the stage grows past that many sectors nothing fails at build time; the loader jumps into a stage whose tail was never read. The count is taken from the dataflow (AL, or a packet laid out as constants that nothing writes), and the stage from `falcon-asm: loads=<label or file>` (a label of this file measured to its _end label in the listing, a binary by its size, an assembly source assembled with its own flags) or, in a boot sector, from a read into the rest of its own section. A count read from memory is replaced by `falcon-asm: sectors=<n>` or a <STAGE>_SECTORS macro when there is one.

Bad:

```asm
	.code16
_start:
	xorw %ax, %ax
	movw %ax, %es
	movw $0x0202, %ax
	movw $0x0002, %cx
	xorw %dx, %dx
	movw $0x7E00, %bx
	int $0x13
	ljmp $0, $0x7E00
	. = _start + 510
	.byte 0x55, 0xAA
stage2:
	.fill 1500, 1, 0x90
```

Good:

```asm
	.code16
_start:
	xorw %ax, %ax
	movw %ax, %es
	movw $0x0203, %ax
	movw $0x0002, %cx
	xorw %dx, %dx
	movw $0x7E00, %bx
	int $0x13
	ljmp $0, $0x7E00
	. = _start + 510
	.byte 0x55, 0xAA
stage2:
	.fill 1500, 1, 0x90
```

## loader-sector-count-unproven

- Section: 5 (Boot and system protocol checks)
- Default severity: info
- Runs on: assembly
- Needs: dataflow, the assembler listing (runs only after a toolchain run)

Boot sector or annotated loader whose INT 13h read cannot be shown to cover the stage it loads.

loader-sector-count needs a constant sector count and a known stage size. When a boot sector reads with a count from memory (patched in by an installer), inside a loop, or without anything naming the stage, the fit cannot be proven and this says which part is missing. Information only: annotate with `falcon-asm: loads=<label or file>` and, for a count from memory, `falcon-asm: sectors=<n>` to have it checked.

Bad:

```asm
	.code16
_start:
	movb count, %al
	movb $0x02, %ah
	int $0x13
	ljmp $0, $0x7E00
count:	.byte 0
	. = _start + 510
	.byte 0x55, 0xAA
```

Good:

```asm
	.code16
_start:
	xorw %ax, %ax
	movw %ax, %es
	movw $0x0201, %ax
	movw $0x7E00, %bx
	int $0x13
	ljmp $0, $0x7E00
	. = _start + 510
	.byte 0x55, 0xAA
stage2:
	.fill 500, 1, 0x90
```

## mbr-partition-overlap

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

Code or data of an MBR emitted into the partition table (bytes 446-509).

A master boot record shares its sector with the partition table: bytes 446-509 are the disk's, written by fdisk and every installer, and 440-445 the disk signature. Boot code that grows into them is overwritten when the disk is partitioned, or corrupts the table when the sector is written. A boot sector is taken as an MBR when it places something at 440 or 446 relative to its start, or carries a `falcon-asm: mbr` comment; data after an explicit move to 446 is the partition table and is allowed. Offsets come from the assembler listing.

Bad:

```asm
# falcon-asm: mbr
_start:
	.fill 450, 1, 0x90
	. = _start + 510
	.byte 0x55, 0xAA
```

Good:

```asm
# falcon-asm: mbr
_start:
	.fill 400, 1, 0x90
	. = _start + 510
	.byte 0x55, 0xAA
```

## mode-switch-a20

- Section: 5 (Boot and system protocol checks)
- Default severity: info
- Runs on: assembly
- Needs: dataflow

Switch to protected mode in a file that never enables A20.

With the A20 line masked every odd megabyte of physical memory aliases the one below it, so anything placed at 1 MiB lands on the interrupt vector table. The file sets CR0.PE but nothing in it opens A20 (the fast gate, bit 1 of port 0x92; the keyboard controller's write-output-port command 0xD1; INT 15h AX=2401h). Information only: an earlier stage may have done it, which a single file cannot show. A return to protected mode after a trip to real mode is not reported.

Bad:

```asm
	.code16
	cli
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	ljmpl $8, $pm
	.code32
pm:
	hlt
gdt_desc:	.word 0
	.long 0
```

Good:

```asm
	.code16
	cli
	inb $0x92, %al
	orb $2, %al
	outb %al, $0x92
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	ljmpl $8, $pm
	.code32
pm:
	hlt
gdt_desc:	.word 0
	.long 0
```

## mode-switch-order

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

Mode switch with a step missing or out of order: lgdt, cli, the far jump and segment reloads around CR0.PE; PAE, CR3, EFER.LME and PG for long mode.

Real to protected mode is: interrupts off, lgdt, set CR0.PE, a far jump that loads CS with a code selector as the very next control transfer, then DS, ES and SS reloaded before anything uses them. Protected to long mode is CR4.PAE, CR3, EFER.LME, then CR0.PG, then a far jump into a 64-bit code segment. Back to real mode is a 16-bit code segment, PE cleared, a far jump to real-mode code and real-mode segment loads. None of these mistakes is an error the assembler can see, and each one is a triple fault: an instant reset with nothing on the screen. The transitions are found from values the dataflow proves (the PE, PG, PAE and LME bits written); a step is out of order when this file performs it after the transition on some path and on no path before it, and steps that follow a transition are checked on the code right after it. Unknown values are silent.

Bad:

```asm
	.code16
	cli
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	lgdt gdt_desc
	ljmpl $8, $pm
	.code32
pm:
	hlt
gdt_desc:	.word 0
	.long 0
```

Good:

```asm
	.code16
	cli
	lgdt gdt_desc
	movl %cr0, %eax
	orl $1, %eax
	movl %eax, %cr0
	ljmpl $8, $pm
	.code32
pm:
	hlt
gdt_desc:	.word 0
	.long 0
```

## pte-reserved

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly

Page table entry built with reserved bits set.

A present paging-structure entry with a reserved bit set makes every walk through it raise #PF with the RSVD error code, which before an IDT exists is a triple fault the moment paging is enabled. Tables are found from the label moved into CR3 and followed through entries written as `.quad next_table + flags`; for those the flags are checked. With 8-byte entries and no 64-bit code in the file the paging mode could be PAE or 4-level, and only bits reserved under both are reported (PAE's PDPTEs reserve RW and US, which 4-level paging uses).

Bad:

```asm
	.code32
	movl $pml4, %eax
	movl %eax, %cr3
	.code64
	nop
	.balign 4096
pml4:	.quad pdpt + 0x83
	.balign 4096
pdpt:	.quad 0
```

Good:

```asm
	.code32
	movl $pml4, %eax
	movl %eax, %cr3
	.code64
	nop
	.balign 4096
pml4:	.quad pdpt + 0x3
	.balign 4096
pdpt:	.quad 0
```

## real-mode-layout

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow, the assembler listing (runs only after a toolchain run)

Real-mode code that reaches for a label linked above 1 MiB, or runs past the end of its 64 KiB code segment.

Data that real-mode code must read has to be below the HMA, and real-mode code has to fit in the 64 KiB of the segment it runs in: IP wraps to 0 past offset 0xFFFF. Link addresses come from the linker script beside the file (an output section with an address, or after `. = N;`, whose first input is this file's section) plus the listing's offsets. Real-mode code uses its labels' link addresses as IP values, so code linked past 0xFFFF is code IP cannot reach; a real-mode region longer than 64 KiB is reported whatever the addresses.

Bad:

```asm
# link.ld: SECTIONS { .lo 0x7C00 : { *(.text.lo) } .hi 0x200000 : { *(.data.hi) } }
	.code16
	.section .text.lo, "ax"
	movl hi_data, %eax
	.section .data.hi, "aw"
hi_data:	.long 0
```

Good:

```asm
# link.ld: SECTIONS { .lo 0x7C00 : { *(.text.lo) } .hi 0x200000 : { *(.data.hi) } }
	.code16
	.section .text.lo, "ax"
	movl lo_data, %eax
	.section .text.lo, "ax"
lo_data:	.long 0
```

## real-mode-reach

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

Real-mode memory access past what segment:offset can reach, or a 16-bit offset that wraps.

A real-mode address is segment * 16 plus a 16-bit offset: nothing above 0x10FFEF (FFFF:FFFF, the top of the HMA) is reachable, an offset past 0xFFFF with a 32-bit address faults on real mode's 64 KiB limit, and a 16-bit offset computed past 0xFFFF silently wraps to the bottom of the segment. When the dataflow knows the segment register, the base and index registers and the displacement of a memory operand in real-mode code, the address is checked. Files that leave protected mode (and so may set up unreal mode on purpose) are not judged on 32-bit offsets.

Bad:

```asm
	.code16
	movw $0xF000, %ax
	movw %ax, %ds
	movl $0x20000, %esi
	movl (%esi), %eax
```

Good:

```asm
	.code16
	movw $0xF000, %ax
	movw %ax, %ds
	movl $0xFFF0, %esi
	movl (%esi), %eax
```

## selector-type

- Section: 5 (Boot and system protocol checks)
- Default severity: error
- Runs on: assembly

Selector names a GDT entry beyond the limit or of the wrong type for the segment register it is loaded into.

CS must be loaded with a code descriptor, SS with a writable data descriptor, and DS, ES, FS and GS with data or readable code; a selector past the GDT limit or naming a zero entry faults too. Every one of these is a #GP at the load, which in a boot path without an IDT is a reset. Checked for constant selectors: far jumps (`ljmp $SEL, $label`) and the two-step `movw $SEL, %ax; movw %ax, %ds` (across labels as far as the dataflow proves the value), in protected-mode code only (16-bit code counts when the dataflow proves CR0.PE set, or when a protected-mode far jump leads to it), against the single GDT the file loads.

Bad:

```asm
	.code32
	movw $0x08, %ax
	movw %ax, %ss
	lgdt gdt_desc
gdt:	.quad 0
	.quad 0x00CF9A000000FFFF
gdt_end:
gdt_desc: .word gdt_end - gdt - 1
	.long gdt
```

Good:

```asm
	.code32
	movw $0x10, %ax
	movw %ax, %ss
	lgdt gdt_desc
gdt:	.quad 0
	.quad 0x00CF9A000000FFFF
	.quad 0x00CF92000000FFFF
gdt_end:
gdt_desc: .word gdt_end - gdt - 1
	.long gdt
```

## sipi-alignment

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

SIPI trampoline at a section offset that is not a multiple of 4 KiB.

The startup IPI's vector is a page number: the processor starts at offset 0 of that page. A trampoline the listing places at, say, offset 0x120 of its section cannot be the first byte of a page however the section is aligned. A trampoline that is copied to its page before the IPIs are sent is not this rule's business; mark only the in-place one.

Bad:

```asm
	.code16
	nop
	# falcon-asm: trampoline
ap_start:
	cli
```

Good:

```asm
	.code16
	# falcon-asm: trampoline
ap_start:
	cli
```

## sipi-trampoline

- Section: 5 (Boot and system protocol checks)
- Default severity: warning
- Runs on: assembly

SIPI trampoline not assembled as 16-bit code.

A startup IPI starts an application processor in real mode at the first byte of the page its vector names (CS = vector << 8, IP = 0). The trampoline therefore has to be .code16 and page aligned. A label is taken as the trampoline when the vector field of an ICR value is built from it with delivery mode 110b (`$(0x4600 | (ap_start >> 12))`), or when a `falcon-asm: trampoline` comment marks it. sipi-alignment checks the page alignment against the listing.

Bad:

```asm
	# falcon-asm: trampoline
	.code32
ap_start:
	cli
```

Good:

```asm
	.balign 4096
	# falcon-asm: trampoline
	.code16
ap_start:
	cli
```

## sipi-unproven

- Section: 5 (Boot and system protocol checks)
- Default severity: info
- Runs on: assembly

SIPI trampoline whose 4 KiB alignment cannot be shown.

When there is no listing and no `.balign 4096` (or `.p2align 12`) right before the trampoline label, nothing in the file proves the page alignment a startup IPI needs. Reported as information: the linker script may well place it, which is outside what the linter sees.

Bad:

```asm
	# falcon-asm: trampoline
	.code16
ap_start:
	cli
```

Good:

```asm
	.balign 4096
	# falcon-asm: trampoline
	.code16
ap_start:
	cli
```

## budget-exceeded

- Section: 6 (Memory layout and size)
- Default severity: error
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

A stage or section is larger than its size budget.

Boot code lives in fixed-size slots: a boot sector has 510 bytes before 0x55AA, a stage has the sectors its loader reads. The usual guard is a location move such as `. = _start + 510`, which gas refuses once the code in front of it has grown past the target, with the unhelpful "attempt to move .org backwards". This rule replaces that message with one naming the stage, its budget and, from the listing, how far over it is at least. A section can also be given a budget with a `falcon-asm: budget=<bytes>` comment inside it (or `budget=<section>:<bytes>` anywhere); its size comes from the assembled object. The editor shows every budget as a code lens on the stage's start label, e.g. "stage1: 440 / 510 bytes (70 free)". Needs the toolchain.

Bad:

```asm
	.section .text.stage1, "ax"
_start:
	.fill 520, 1, 0x90
	. = _start + 510
	.byte 0x55, 0xAA
```

Good:

```asm
	.section .text.stage1, "ax"
_start:
	.fill 440, 1, 0x90
	. = _start + 510
	.byte 0x55, 0xAA
```

## org-overlap

- Section: 6 (Memory layout and size)
- Default severity: error
- Runs on: assembly

A .org or `. =` target is behind, or the same as, an earlier one in the section.

Fixed layouts are written as a series of location moves: the boot code, then `.org _start + 0x1B2` for a slot the installer patches, then the partition table at 446. If a later target is lower than an earlier one (a typo, or a constant changed in one header but not another), the regions overlap; gas stops with "attempt to move .org backwards" and no hint of which earlier line it means. Two moves to the same target with data between them overlap the same way. Both are found from the source alone, with macro values from the toolchain when it has run; targets relative to different symbols are not compared.

Bad:

```asm
	.org 0x1BE
part_table:
	.fill 64, 1, 0
	.org 0x1B8
disk_sig:
	.long 0
```

Good:

```asm
	.org 0x1B8
disk_sig:
	.long 0
	.org 0x1BE
part_table:
	.fill 64, 1, 0
```

## page-table-alignment

- Section: 6 (Memory layout and size)
- Default severity: error
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

A page table is not 4 KiB aligned.

CR3 and every paging-structure entry hold a 4 KiB aligned physical address with flags in the low 12 bits, so a table that is not aligned is not where the CPU looks: the walk reads the aligned address below it and the first memory access after enabling paging faults or maps garbage. Page tables are found from the register moved into %cr3 (traced back to `mov $table, %reg` or `lea table, %reg` in straight-line code) and then through entries of known tables that name another label (`.quad pdpt + 3`). An error is reported only when the offset is misaligned within a section that is itself aligned to 4 KiB, which makes the result certain; otherwise table-alignment says it depends on placement. Needs the toolchain.

Bad:

```asm
	.section .data
	.balign 4096
	.long 0
pml4:
	.quad pdpt + 3
	.balign 4096
pdpt:
	.quad 0x83
	.text
	movq $pml4, %rax
	movq %rax, %cr3
```

Good:

```asm
	.section .data
	.balign 4096
pml4:
	.quad pdpt + 3
	.balign 4096
pdpt:
	.quad 0x83
	.text
	movq $pml4, %rax
	movq %rax, %cr3
```

## section-overlap

- Section: 6 (Memory layout and size)
- Default severity: error
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

Sections a linker script places at fixed addresses collide.

Boot images place each stage at the address it is loaded to (`.stage1 0x7C00 : { ... }`, `.stage2 0x7E00 : { ... }`). When stage 1 grows past 512 bytes it runs into stage 2, and depending on the script ld either stops with a terse VMA overlap error at link time or the image is simply wrong. This rule reads the *.ld and *.lds scripts beside the file and in the directories above it (up to the project root), places the file's sections the way ld does, and compares the address ranges using the section sizes from the assembled object. It is conservative: only literal addresses (`. = 0x7C00;` or an address on the output section) are used, sizes are this file's contribution only (a lower bound), and a script is only considered when it names one of the file's own non-standard sections literally. Needs the toolchain.

Bad:

```asm
/* boot.ld: SECTIONS { .stage1 0x7C00 : { *(.text.stage1) }  .stage2 0x7D00 : { *(.text.stage2) } } */
	.section .text.stage1, "ax"
	.fill 300, 1, 0x90
```

Good:

```asm
/* boot.ld: SECTIONS { .stage1 0x7C00 : { *(.text.stage1) }  .stage2 0x7E00 : { *(.text.stage2) } } */
	.section .text.stage1, "ax"
	.fill 300, 1, 0x90
```

## simd-alignment

- Section: 6 (Memory layout and size)
- Default severity: error
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

An aligned SIMD move (movdqa, movaps, vmovdqa ...) names a label that is not aligned to the vector width.

movdqa, movaps, movapd, the non-temporal stores and their VEX and EVEX forms raise #GP when the memory operand is not aligned to the vector width (16, 32 or 64 bytes). When the operand is a label in this file and the listing puts it at a misaligned offset of a section aligned at least that much, the fault is certain. Operands through registers, and labels in sections with weaker alignment, are left alone. Needs the toolchain.

Bad:

```asm
	.data
	.balign 16
	.long 0
mask:
	.quad 0, 0
	.text
	movdqa mask(%rip), %xmm0
```

Good:

```asm
	.data
	.balign 16
mask:
	.quad 0, 0
	.text
	movdqa mask(%rip), %xmm0
```

## stack-frame-align

- Section: 6 (Memory layout and size)
- Default severity: warning
- Runs on: assembly
- Needs: dataflow

An aligned SSE access (movaps, movdqa, ...) to a stack slot that is not 16-byte aligned.

movaps, movdqa, their VEX forms and the non-temporal moves fault (#GP) on an address that is not 16-byte aligned. A 64-bit SysV or MS x64 function starts with rsp 8 past a 16-byte boundary, so after an odd number of pushes, or a sub that is not a multiple of 16 from there, N(%rsp) is misaligned for every N that looks right. The address is computed from the function's entry through the tracked stack depth (and the frame pointer when rbp holds a captured depth); a realigned stack (`and $-16, %rsp`) has an unknown depth and is never reported.

Bad:

```asm
# falcon-asm: cc=sysv

	.globl f
f:
	subq $16, %rsp
	movaps %xmm0, (%rsp)
	addq $16, %rsp
	ret
```

Good:

```asm
# falcon-asm: cc=sysv

	.globl f
f:
	subq $24, %rsp
	movaps %xmm0, (%rsp)
	addq $24, %rsp
	ret
```

## table-alignment

- Section: 6 (Memory layout and size)
- Default severity: info
- Runs on: assembly
- Needs: the assembler listing (runs only after a toolchain run)

GDT or IDT base not 8-byte aligned (16 for a long mode IDT), or a table whose alignment depends on section placement.

The SDM recommends an 8-byte aligned base for the GDT (vol. 3, 3.5.1) and the IDT (6.10) so descriptor loads do not straddle cache lines; long mode gates are 16 bytes. The tables are found from `lgdt`/`lidt` and their pseudo-descriptor (`.word limit` then `.long base`), and their offsets come from the listing. When the offset is misaligned but the section itself asks for less alignment than needed, the final address depends on where the linker puts the section; that case is reported here for page tables too (see page-table-alignment), as information. A quick fix inserts `.balign` before the table. Needs the toolchain.

Bad:

```asm
	.balign 8
	.byte 0
gdt:
	.quad 0, 0x00cf9a000000ffff
gdt_desc:
	.word . - gdt - 1
	.long gdt
	lgdt gdt_desc
```

Good:

```asm
	.balign 8
gdt:
	.quad 0, 0x00cf9a000000ffff
gdt_desc:
	.word . - gdt - 1
	.long gdt
	lgdt gdt_desc
```

## cr-reserved-write

- Section: 7 (Hardware knowledge)
- Default severity: warning
- Runs on: assembly, inline asm

Constant written to CR0, CR4, XCR0 or EFER that sets reserved bits or an invalid combination.

A mov to CR0 or CR4 with a reserved bit set raises #GP, as does CR0.PG without PE or NW without CD, and xsetbv and the EFER wrmsr have their own invalid combinations. The value is followed from constants (`movl $X, %eax; movl %eax, %cr0`), across labels as far as the dataflow proves it; after a read-modify-write (`movl %cr4, %eax; orl $X, %eax`) only the bits known to be set are checked, against the reserved mask.

Bad:

```asm
	movl %cr4, %eax
	orl $0x8000, %eax
	movl %eax, %cr4
```

Good:

```asm
	movl %cr4, %eax
	orl $0x20, %eax
	movl %eax, %cr4
```

## msr-reserved-write

- Section: 7 (Hardware knowledge)
- Default severity: warning
- Runs on: assembly, inline asm

wrmsr with a constant value that sets reserved bits, writes a read-only MSR or a reserved memory type.

wrmsr raises #GP for a reserved bit set, a write to a read-only register or an undefined PAT or MTRR memory type, and in early boot code nothing catches it. When ECX, EAX and EDX are set from constants before the wrmsr (`movl $lo, %eax; movl $hi, %edx; movl $msr, %ecx; wrmsr`, across labels as far as the dataflow proves them), or EAX is a read-modify-write of the register (`rdmsr; orl $bit, %eax; wrmsr`), the value is checked against the MSR's layout. EFER is checked by cr-reserved-write.

Bad:

```asm
	movl $0x277, %ecx
	movl $0x02020202, %eax
	movl $0x02020202, %edx
	wrmsr
```

Good:

```asm
	movl $0x277, %ecx
	movl $0x00070406, %eax
	movl $0x00070406, %edx
	wrmsr
```

## bt-mem

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm

bt/bts/btr/btc with a register bit offset into memory: bit-string addressing, microcoded and slow.

With a register offset and a memory operand these instructions address a bit string: the offset may reach far outside the named operand (Intel SDM Vol. 2, BT, "bit base"), so the core computes the address in microcode (Agner Fog's tables list around 10 uops for bt m,r and more for bts/btr/btc). Loading the word and testing the register form is a handful of fast uops; an immediate offset is fine as it is. Make sure the bit-string semantics (an offset beyond the operand's width) are not relied on before rewriting.

Bad:

```asm
	btl %ecx, (%rdi)
```

Good:

```asm
	movl (%rdi), %eax
	btl %ecx, %eax
```

## div-by-constant

- Section: 8 (Performance lints)
- Default severity: hint
- Runs on: assembly, inline asm
- Needs: dataflow

div/idiv by a register holding a known constant: a shift or a multiply by the reciprocal is faster.

Integer division is the slowest common ALU instruction: tens of cycles of latency for 32 bits and more for 64 bits on all but the newest cores (Agner Fog's tables; Ice Lake made 64-bit division much faster). When the dataflow proves the divisor constant, an unsigned power of two is a shift (and a mask for the remainder), a signed one a shift with a rounding bias, and any other constant a multiply by its fixed-point reciprocal (Granlund and Montgomery, "Division by invariant integers using multiplication", 1994), which is what compilers emit. Hint severity: in cold code the division is often clearer. In 16-bit code only powers of two are reported, since the reciprocal trick needs a wider multiply than the dividend.

Bad:

```asm
	movl $16, %ecx
	xorl %edx, %edx
	divl %ecx
```

Good:

```asm
	movl %eax, %edx
	andl $15, %edx
	shrl $4, %eax
```

## false-dependency

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm
- Needs: dataflow

popcnt/lzcnt/tzcnt into a register that was not zeroed first: a false dependency on its old value.

These instructions write their whole destination, but on affected Intel cores the hardware still waits for the destination's previous value: lzcnt and tzcnt on Haswell through Skylake (fixed in Cannon Lake), popcnt from Sandy Bridge through Skylake as well (fixed in Ice Lake). In a loop the result then chains through iterations that are really independent; the effect is why GCC (its avoid_false_dep_for_bmi tuning) and Clang emit a zeroing xor before them when tuning for these cores. The xor idiom is recognised at rename and costs no execution unit. The rule applies when no CPU is targeted, for an x86-64-vN level target, and for the affected profiles; it stays quiet when the destination is also a source (the dependency is real), was last written by a zeroing idiom or an immediate move, or is 16 bits wide (a 32-bit xor would change the bits a 16-bit result keeps).

Bad:

```asm
	popcntq (%rdi), %rax
	addq %rax, %rcx
```

Good:

```asm
	xorl %eax, %eax
	popcntq (%rdi), %rax
	addq %rax, %rcx
```

## inc-no-carry

- Section: 8 (Performance lints)
- Default severity: warning
- Runs on: assembly, inline asm
- Needs: dataflow

Branch, setcc or cmovcc on CF right after inc/dec, which do not set CF: it tests a stale carry.

inc and dec leave CF as it was (Intel SDM Vol. 2, INC/DEC: "The CF flag is not affected"), so `incl %eax; jc overflow` never sees the increment's carry; it tests whatever an older instruction left there. This is a correctness bug far more often than an idiom, and it is also a partial flags merge. The rule fires when a jcc, setcc or cmovcc reading CF immediately follows the inc/dec. It stays quiet when the instruction just before the inc/dec wrote CF (`cmp; inc; jb` deliberately carries the compare's CF across the inc) and for adc/sbb/rcl/rcr, which consume a carry chain on purpose (see partial-flags-stall). Use add/sub $1 to get a carry.

Bad:

```asm
	incl %eax
	jc overflow
```

Good:

```asm
	addl $1, %eax
	jc overflow
```

## lea-three-component

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm

lea with base, index and displacement: 3-cycle latency on Sandy Bridge through Ice Lake.

On Intel cores from Sandy Bridge to Ice Lake a lea with all three components runs on a single port with 3-cycle latency, where a two-component lea is one cycle on two ports (Intel optimization manual, "LEA"; Agner Fog's tables). Splitting it into two leas, or an lea and an add, is faster on a critical path. The rule runs only when the target CPU is one of those profiles; other cores (Alder Lake and later, AMD Zen) handle it well enough not to bother.

Bad:

```asm
# .falcon-asm.json: {"target": {"cpu": "skylake"}}
	leaq 8(%rdi,%rsi,4), %rax
```

Good:

```asm
# .falcon-asm.json: {"target": {"cpu": "skylake"}}
	leaq (%rdi,%rsi,4), %rax
	addq $8, %rax
```

## missing-vzeroupper

- Section: 8 (Performance lints)
- Default severity: warning
- Runs on: assembly, inline asm
- Needs: dataflow

call, ret or jump out of the document while ymm/zmm upper halves are dirty: add vzeroupper first.

A VEX.256 or EVEX.512 write leaves the upper bits of a vector register non-zero. When legacy (non-VEX) SSE code runs in that state, Intel cores pay for it: Sandy Bridge through Broadwell save and restore the upper halves on each transition (on the order of 70 cycles each way, per Agner Fog's microarchitecture guide; Intel's optimization manual says to execute vzeroupper before any transition to legacy SSE code), and Skylake and later instead make every SSE instruction depend on the full register and merge it, which serializes otherwise independent code. The callee of an external call, or the caller a ret returns to, may well be SSE code (libc, older compilers), which is why the System V psABI convention and every compiler emit vzeroupper before leaving AVX code. The rule fires only where the dataflow proves some upper half dirty on every path, at a call (unless the callee is in the document and itself uses ymm/zmm or runs no legacy SSE), a ret, or a jmp to a label outside the document. It stays quiet when the block passes ymm/zmm arguments in ymm0-ymm7 (vzeroupper would destroy them), at a ret where only ymm0 is dirty (a __m256 return value), and for AMD CPU targets, whose cores track the upper state without a transition penalty.

Bad:

```asm
	vaddps %ymm1, %ymm2, %ymm3
	vmovups %ymm3, (%rdi)
	ret
```

Good:

```asm
	vaddps %ymm1, %ymm2, %ymm3
	vmovups %ymm3, (%rdi)
	vzeroupper
	ret
```

## partial-flags-stall

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm
- Needs: dataflow

CF read while the latest flag writer is inc/dec: the flags must be merged from two instructions.

inc and dec write every status flag except CF, so a later instruction reading CF needs flags from two different producers. P6-family cores (Pentium Pro through Core 2) stall until the inc/dec retires; Sandy Bridge and later insert a flag-merging uop (Agner Fog, "The microarchitecture of Intel, AMD and VIA CPUs", partial flags stall sections). The pattern is intentional in carry-chain loops (adc with an inc'd index) and cheap on modern cores, hence Info. Using add/sub $1 writes every flag and avoids the merge, where the old CF is not needed.

Bad:

```asm
	clc
	decl %ecx
	adcl $0, %eax
```

Good:

```asm
	clc
	subl $1, %ecx
	adcl $0, %eax
```

## slow-enter

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm

enter with a nesting level, or any enter in 64-bit code: microcoded; push/mov/lea is faster.

enter is microcoded on every modern core (Agner Fog's tables list a dozen or more uops for level 0 and far more per nesting level), where the push/mov/sub sequence it replaces is three simple instructions; leave, by contrast, is cheap and fine to keep. Compilers stopped emitting enter decades ago. A nesting level is reported in every mode; level 0 only in 64-bit code, where it is most likely hot code rather than size-optimised boot code. The fix uses lea rather than sub to set the frame size because enter does not touch the flags.

Bad:

```asm
	.code64
	enter $32, $0
```

Good:

```asm
	.code64
	pushq %rbp; movq %rsp, %rbp; leaq -32(%rsp), %rsp
```

## slow-loop

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm

loop/loope/loopne in 32- or 64-bit code: microcoded on Intel cores; dec + jnz is faster.

Intel cores decode loop into several uops and run it far slower than the dec/jnz pair, which macro-fuses into one uop (Agner Fog's instruction tables list loop at about 7 uops on Skylake and loope/loopne at more; the Intel optimization manual advises against it). Compilers never emit it. In 16-bit code the rule is quiet: there it is usually boot code optimised for size, where loop's two bytes matter more than its speed. dec does not preserve the flags that loop preserves, so the rewrite is not offered as an automatic fix.

Bad:

```asm
1:	addl (%esi), %eax
	addl $4, %esi
	loop 1b
```

Good:

```asm
1:	addl (%esi), %eax
	addl $4, %esi
	decl %ecx
	jnz 1b
```

## xchg-mem-lock

- Section: 8 (Performance lints)
- Default severity: info
- Runs on: assembly, inline asm

xchg with a memory operand is implicitly locked: atomic, and as slow as any locked instruction.

xchg with memory asserts LOCK whether or not the prefix is written (Intel SDM Vol. 2, XCHG), so it is a full memory barrier costing roughly 20 cycles or more on current cores. That is exactly right for a spinlock or a semaphore, and wasteful for a plain swap of a register with a variable no other agent touches, which two or three movs do far faster. The rule is a note, not a fix: only the author knows whether the atomicity is needed.

Bad:

```asm
	xchgl %eax, saved_value
```

Good:

```asm
	movl saved_value, %edx
	movl %eax, saved_value
	movl %edx, %eax
```

## asm-early-clobber

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

An output is written before an input is read, but its constraint has no early-clobber '&'.

Without '&' GCC may give an output the same register as an input, on the assumption that every input is consumed before any output is produced. A template that writes the output and then reads another input reads its own result instead. The bug only appears when the register allocator happens to share the register, so it comes and goes with unrelated changes. Outputs tied to the input ("0") and read-write outputs ("+r") are not affected. The quick fix inserts '&' after '='.

Bad:

```asm
asm("movl %1, %0\n\taddl %2, %0" : "=r"(sum) : "r"(a), "r"(b));
```

Good:

```asm
asm("movl %1, %0\n\taddl %2, %0" : "=&r"(sum) : "r"(a), "r"(b));
```

## asm-hardcoded-reg

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

The template reads a fixed register that an "r"-class operand could also be allocated to.

A register named in the template (%%eax) that no constraint binds and no clobber reserves is one GCC is free to use for an operand. Reading it gets whatever the compiler left there, which may be one of the operands. Bind the value with a constraint ("a") or, when the asm sets the register itself, clobber it. Writes are asm-missing-clobber's; %rsp and %rbp are not reported.

Bad:

```asm
asm("movl %%ecx, %0" : "=r"(v));
```

Good:

```asm
asm("movl %1, %0" : "=r"(v) : "c"(x));
```

## asm-missing-clobber

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

The template writes a register that is neither an output operand nor in the clobber list.

GCC assumes every register not named as an output or a clobber holds the same value after the asm as before it, and keeps live values there. A template that writes %ebx (explicitly, or implicitly through cpuid, rdtsc, rep movs, mul, syscall ...) silently corrupts whatever the compiler had in it; the failure moves with optimisation level and inlining. Registers saved and restored inside the template (push/pop, the PIC-safe xchg %ebx sequence) are not reported. A register that is an input operand cannot be clobbered; the message says to make it an in/out operand ("+c") instead. The quick fix adds the missing clobbers.

Bad:

```asm
uint32_t lo, hi;
asm volatile("cpuid" : "=a"(lo), "=d"(hi) : "a"(0));
```

Good:

```asm
uint32_t lo, hi;
asm volatile("cpuid" : "=a"(lo), "=d"(hi) : "a"(0) : "rbx", "rcx");
```

## asm-missing-memory-clobber

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

The template stores to memory that is not a memory operand, and "memory" is not clobbered.

Without a "memory" clobber or an "=m" output naming the object, GCC assumes the asm leaves memory alone: it keeps values cached in registers across it and may move loads and stores past it. A store through a pointer operand ((%0) with an "r" constraint), a rep stos/movs, or an xsave into a buffer then goes unseen. Statements with a memory output operand are not reported, since that operand usually names the object written. The quick fix adds "memory".

Bad:

```asm
asm volatile("rep stosb" : "+D"(p), "+c"(n) : "a"(0));
```

Good:

```asm
asm volatile("rep stosb" : "+D"(p), "+c"(n) : "a"(0) : "memory");
```

## asm-missing-volatile

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

Asm with outputs and side effects is not volatile, so GCC may delete, merge or hoist it.

An extended asm with outputs and no volatile is treated as a pure function of its inputs: if the outputs are unused it is deleted, two identical ones are merged, and it may be moved out of a loop. That is wrong for port I/O, MSR and control register writes, cli/sti, invlpg, stores to memory that is not an output, and instructions whose result differs per execution (rdtsc, rdrand). Asm without outputs and basic asm are implicitly volatile and not reported. The quick fix adds volatile.

Bad:

```asm
uint8_t v;
asm("inb %1, %0" : "=a"(v) : "Nd"(port));
```

Good:

```asm
uint8_t v;
asm volatile("inb %1, %0" : "=a"(v) : "Nd"(port));
```

## asm-operand-ref

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

Operand reference out of range, unknown %[name], %l outside asm goto, or an unknown operand modifier.

GCC numbers the operands of an extended asm outputs first, then inputs, then (for asm goto) labels. A %N past the end, a %[name] that names no operand, or a modifier letter the x86 back end does not define is a compile error at best; at worst it is a register name written with a single % (%r8 reads as operand 8 with modifier r), which the quick fix turns into %%r8.

Bad:

```asm
asm("movl %2, %0" : "=r"(out) : "r"(in));
```

Good:

```asm
asm("movl %1, %0" : "=r"(out) : "r"(in));
```

## asm-operand-width

- Section: 9 (Inline asm in C)
- Default severity: warning
- Runs on: C

C operand width disagrees with the instruction's size suffix or the operand modifier.

GCC prints a register operand in the width of its C type: a uint8_t in "r" becomes %al, a pointer %rax. A movl on it is then an assembler error, or worse, with a memory operand a 4-byte access to a 1-byte object. A modifier that widens an input (%k0 on a uint8_t) reads upper bits the compiler never defined, and one that narrows a pointer (%b0, %k0 in 64-bit code) truncates it. Types come from a cast, a literal or the declaration of a plain identifier in the enclosing function; anything else is not checked. This rule covers standard C and <stdint.h> types; see asm-operand-width-typedef for typedefs.

Bad:

```asm
uint8_t v = 1;
asm("movl %0, %%eax" : : "r"(v) : "eax");
```

Good:

```asm
uint32_t v = 1;
asm("movl %0, %%eax" : : "r"(v) : "eax");
```

## asm-operand-width-typedef

- Section: 9 (Inline asm in C)
- Default severity: info
- Runs on: C

As asm-operand-width, for typedefs whose width is assumed by convention (UINT32, u8, DWORD, UINTN).

GCC prints a register operand in the width of its C type: a uint8_t in "r" becomes %al, a pointer %rax. A movl on it is then an assembler error, or worse, with a memory operand a 4-byte access to a 1-byte object. A modifier that widens an input (%k0 on a uint8_t) reads upper bits the compiler never defined, and one that narrows a pointer (%b0, %k0 in 64-bit code) truncates it. Types come from a cast, a literal or the declaration of a plain identifier in the enclosing function; anything else is not checked. The width of these typedefs is a convention (Linux, EDK2, Windows) rather than something visible here, so the finding is information only.

Bad:

```asm
UINT8 v = 1;
asm("movl %0, %%eax" : : "r"(v) : "eax");
```

Good:

```asm
UINT32 v = 1;
asm("movl %0, %%eax" : : "r"(v) : "eax");
```

## duplicate-definition

- Section: 10 (Symbols and structure)
- Default severity: warning
- Runs on: assembly

A global symbol also defined by a file assembled into the same program.

Two definitions of one global symbol fail at link time with "multiple definition", or with -z muldefs silently keep the first. The rule reports a label defined both here and in a file this one includes or is included by, and a global label also defined by another source that is linked with this one; linked means one of the two references a global only the other defines, so standalone programs that each have a _start are not reported. The other definitions are listed as related locations.

Bad:

```asm
# a.S
	.globl init
init:
	call setup
	ret
# b.S
	.globl init, setup
init:
setup:
	ret
```

Good:

```asm
# a.S
	.globl init
init:
	call setup
	ret
# b.S
	.globl setup
setup:
	ret
```

## duplicate-label

- Section: 10 (Symbols and structure)
- Default severity: error
- Runs on: assembly

The same label defined twice in one file (outside alternative #if/.if branches).

gas rejects the second definition with "symbol already defined" but points only at the second one; in a long boot file the first can be hundreds of lines away. The finding names it and links to it. Definitions in different branches of one #if or .if chain are not duplicates, since only one is assembled.

Bad:

```asm
loop:
	jmp loop
loop:
	hlt
```

Good:

```asm
spin:
	jmp spin
halt:
	hlt
```

## extern-unused

- Section: 10 (Symbols and structure)
- Default severity: info
- Runs on: assembly

An .extern declaration whose symbol the file never uses.

gas treats every undefined symbol as external, so .extern is documentation; one that names a symbol nothing uses documents a dependency that no longer exists.

Bad:

```asm
	.extern panic
	.extern puts
	call puts
```

Good:

```asm
	.extern puts
	call puts
```

## globl-undefined

- Section: 10 (Symbols and structure)
- Default severity: warning
- Runs on: assembly

.globl/.global naming a symbol this file does not define and nothing in the workspace defines.

gas accepts .globl for a symbol the file never defines and turns it into an undefined external reference, so a typo in the export list silently exports nothing and the link fails somewhere else. A name another source or a C file defines is an import written as .globl, which is legal and not reported.

Bad:

```asm
	.globl stage2_entyr
stage2_entry:
	ret
```

Good:

```asm
	.globl stage2_entry
stage2_entry:
	ret
```

## local-label-far

- Section: 10 (Symbols and structure)
- Default severity: info
- Runs on: assembly

A 1b/1f that resolves across a global or @function label, into another function.

Local labels are reused in every function, so a 1b meant for this function's loop silently binds to the previous function's 1: when this one's was deleted or renamed. A reference that crosses a .globl or .type @function label is almost always that mistake.

Bad:

```asm
1:
	ret
	.globl g
g:
	jmp 1b
```

Good:

```asm
	.globl g
g:
1:
	jmp 1b
```

## local-label-unresolved

- Section: 10 (Symbols and structure)
- Default severity: error
- Runs on: assembly

A 1f with no following 1:, or a 1b with no preceding one.

A local label reference that has no target is an assembler error, but gas only says "undefined local label" once the whole file has been read. A reference inside a .macro body, or one whose search crosses a call to a macro that defines the label, is resolved by the expansion and is not reported.

Bad:

```asm
	jz 1f
	nop
2:
	ret
```

Good:

```asm
	jz 1f
	nop
1:
	ret
```

## undefined-label

- Section: 10 (Symbols and structure)
- Default severity: warning
- Runs on: assembly

A branch, call, $symbol or data reference to a symbol defined nowhere in the workspace.

gas does not complain about an undefined symbol: it emits a relocation and leaves the problem to the linker, which reports it far from the typo, or, for a flat binary linked with --noinhibit-exec or a forgiving script, resolves it to 0. The rule looks in the file, the files it includes and that include it, every source's global labels, the C sources and the linker scripts of the workspace, and the macro table. It stays quiet when an include could not be found, and, for a .S file without the toolchain's macro table, about ALL_CAPS names that a -D flag may define.

Bad:

```asm
	call print_strng
print_string:
	ret
```

Good:

```asm
	call print_string
print_string:
	ret
```

## unused-define

- Section: 10 (Symbols and structure)
- Default severity: info
- Runs on: assembly

A #define, .set or .equ symbol that neither the file nor the files including it use.

Constants outlive the code that used them; an unused one misleads the next reader into thinking some hardware value matters here. Exported (.globl) symbols are skipped, and the rule is quiet when an include could not be found, since a header may be the user.

Bad:

```asm
#define KBD_PORT 0x60
#define PIT_PORT 0x40
	inb $KBD_PORT, %al
```

Good:

```asm
#define KBD_PORT 0x60
	inb $KBD_PORT, %al
```

## unused-label

- Section: 10 (Symbols and structure)
- Default severity: info
- Runs on: assembly

A label that is not global and that nothing references, in the file or the workspace.

A label nobody jumps to, calls or takes the address of is either dead code left over from a refactor or a reference that was meant to use it and spells it differently. Global labels, entry points (_start and friends), labels inside .macro bodies and labels a linker script names are never reported.

Bad:

```asm
	jmp done
retry:
	nop
done:
	ret
```

Good:

```asm
	jmp done
done:
	ret
```

## unused-suppression

- Section: 12 (Lint workflow)
- Default severity: info
- Runs on: assembly, inline asm, C

A falcon-asm: ignore comment that suppresses nothing.

Suppressions outlive the findings they were written for: the code is fixed, the rule improves, or the comment drifts to another line in an edit. A stale suppression then hides the next real finding on that line without anyone deciding it should. A code that names no rule (a typo) is reported the same way, because it suppresses nothing either.

Bad:

```asm
	.code64
	pushq %rax /* falcon-asm: ignore=mode-register */
```

Good:

```asm
	.code64
	pushq %rax
```
