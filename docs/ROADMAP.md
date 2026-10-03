# falcon-asm roadmap: the linter

The goal: the most capable GAS (AT&T) x86 assembly linter we can build,
aimed first at boot loader and firmware code (falcon-bios real, protected
and long mode code; falcon-core's x86-64-v3 code; inline asm in C).

Items marked **[have]** exist today. Everything else is planned.

Ground rules for every check:

- Unknown state never warns. When the analysis cannot prove something (an
  indirect jump, a computed far jump, a value from memory), it stays quiet.
- Lints default to Warning and can be silenced with
  `falcon-asm: ignore=<code>` (see section 12).
- Every rule has a code, a doc entry in `docs/RULES.md` with a bad and a
  good example, and its own test fixtures.

## 1. Encoding and operand validity (instant, no gcc)

1. Structured AT&T operand parser: register, `seg:disp(base,index,scale)`,
   immediate, label.
2. No valid form for these operands, with what is valid:
   `lgdt %eax` -> "needs m16&32".
3. Suffix vs register width mismatch: `movw %eax, %bx`.
4. Ambiguous size: memory operand with no suffix and no register
   (`inc (%bx)`).
5. REX vs high-byte conflict: `movb %ah, %sil`.
6. 16-bit addressing combinations: `(%si,%di)`, `(%ax)`, any scale without
   `addr32`.
7. Invalid base or index: `%esp` as index, `%rip` outside 64-bit code,
   mixed address widths.
8. Segment register misuse: `mov %ax, %cs`, `pop %cs`, `lds`/`lss` with a
   register source.
9. Accumulator-only and fixed-register forms: `in %dx, %bl`,
   `out %al, %cx`, `shl %bl, %eax`.
10. Immediate range: `out $0x300`, `int $256`, `.byte 300`, `ret $70000`,
    sign-extension surprises (`$0xFFFFFFFF` into a 64-bit operand).
11. Prefix misuse: `lock` without a memory destination or on an unlockable
    instruction; `rep` on a non-string instruction; repeated or conflicting
    prefixes.
12. AVX/AVX-512 rules: `{%k0}` used as a mask, `{z}` without a mask,
    broadcast width mismatch, mixed vector widths in one instruction.
13. Branch reach: `loop`/`jcxz`/`jecxz` targets beyond rel8 once layout is
    known (from the listing).

## 2. Mode, CPU and ISA target

14. **[have]** Instruction and register invalid in the current
    `.code16/32/64` mode.
15. **[have]** x86-64 level target (v1-v4) from comment, setting or
    `-march`.
16. Target CPU profiles, beyond levels: `i386`, `i486`, `pentium`, `p6`,
    `x86-64`, `znver4`, ... falcon-bios runs on old machines, so: `cpuid`
    on a 386, `cmov` before P6, `rdtsc` on a 486.
17. Guarded-feature checks: an instruction needing a feature (AVX2,
    `rdrand`, `xsave`) reached on a path with no matching CPUID check
    (dataflow).
18. Wrong mode at a far jump target: `ljmp $SEL, $label` where the
    selector's descriptor is 32-bit code but `label` is assembled under
    `.code16`, or the reverse.

## 3. Dataflow (CFG with forward state)

19. Direction flag unknown before string ops (no `cld` on some path).
20. Segment register not set before use (`%es:(%di)` stores, `%ds` loads
    in real mode).
21. Stack imbalance at `ret` or where paths merge; `pushf`/`popf` and
    `cli`/`sti` pairing.
22. Partial register reads (`movw` to `%ax`, then `%eax` treated as
    zero-extended).
23. Uninitialised register read (the count in `rep` with `%ecx` unset,
    `%dx` port unset).
24. Interrupts enabled inside a critical section (INT 15h's `sti`, a
    missing `cli` before a mode switch).
25. Flags clobbered between `cmp`/`test` and `jcc`.
26. Unchecked CF error after a BIOS call.
27. Registers the BIOS may clobber used after the call without being
    reloaded.
28. Dead code after `jmp`/`ret`/`hlt` loops; falling through from code into
    data.
29. Infinite loop with no `hlt`/`pause` (busy spin), as Information.
30. Writes to memory inside the code section (self-modifying code), as
    Information.

## 4. Calling conventions

31. SysV AMD64, MS x64, i386 cdecl/stdcall/fastcall, and `custom`
    (explicit clobber/preserve lists for 16-bit helpers:
    `falcon-asm: cc=custom clobbers=ax,cx preserves=bx,si,di`).
32. Inferred from the compile database (mingw -> MS x64, `-m32` -> cdecl,
    x86-64 Linux -> SysV, `.code16` -> none until declared); overridable per
    file or per label with `falcon-asm: cc=<name>`.
33. Callee-saved register modified without being saved and restored.
34. Caller-saved register read after a `call` without being reloaded.
35. Stack alignment at `call` (SysV 16 bytes), MS x64 shadow space,
    red-zone use under MS x64.
36. Argument registers read before being set at a call site; return
    register unset on some paths (Information).
37. Interrupt and exception handlers: `iret` vs `ret`, all registers
    preserved, error-code pop for the vectors that push one.

## 5. Boot and system protocol checks

38. Mode-switch sequence verifier: real -> protected (A20, `lgdt`, CR0.PE,
    far jump, segment reloads) and protected -> long (CR4.PAE, CR3,
    EFER.LME, CR0.PG, far jump into 64-bit code). Steps out of order or
    missing are flagged.
39. GDT and IDT decode and validation: `.quad`/`.word` descriptors decoded
    in hover; limit, granularity, D/L bits, DPL and gate types checked;
    each selector's index must exist and have the right type for its use
    (CS needs code, SS needs writable data).
40. Page table entries decoded; CR3 and table alignment (4 KiB).
41. SMP trampoline rules: SIPI vector alignment, below 1 MiB, 16-bit entry.
42. Boot sector rules: `0x55AA` at offset 510, partition table area
    untouched by code, the MBR/VBR 512-byte budget.
43. Loader cross-check: the sector count passed to INT 13h
    (`movb $N, %al`) vs the actual size of the stage it loads, from the
    listing and symbols. Catches "stage2 grew past what stage1 reads".
44. Real-mode reach: data that must be reachable from real mode located
    above 1 MiB, or code crossing a 64 KiB segment boundary.
45. Disk Address Packet, E820 entry and other BIOS structures validated
    where they are built with `.byte`/`.word`.

## 6. Memory layout and size

46. **[have]** Listing inlay hints: offset and encoding bytes per line;
    padding counts at `.org` and `. =`.
47. Size budget code lens per stage or section, with an error when over
    budget.
48. Alignment checks for GDT/IDT/TSS, page tables, `movdqa`/`vmovdqa`
    operands, and stack frames.
49. Overlapping `.org` targets and sections that collide after layout.

## 7. Hardware knowledge (hover plus checks)

50. Value tracking at `int`, `in`/`out`, `rdmsr`/`wrmsr`, `cpuid` and
    control register writes (from the dataflow).
51. BIOS service hover: INT 10h/12h/13h/15h/16h/1Ah/18h/19h, with inputs,
    outputs, errors and quirks. Legacy BIOS only.
52. I/O port hover: PIC, PIT, keyboard controller, CMOS, port 0x92, POST
    0x80, DMA, COM, IDE, VGA, PCI config, 0xCF9 reset.
53. MSR hover with bit layouts. Write checks: reserved bits set, unknown
    MSR as Information.
54. CPUID leaf hover and output bit names; which bits gate which x86-64
    level.
55. Value decoders for CR0/CR3/CR4/EFER/XCR0, PCI config addresses,
    descriptors and page table entries. Reserved-bit checks on writes.
56. The same hovers on C helper calls: `outb(0x92, ...)`, `inl(PCI_DATA)`,
    `rdmsr(MSR_EFER)`, `cpuid(7, ...)`.
57. Per-project `.falcon-asm.json` to add ports, MSRs, BIOS quirks and
    helper names, merged over the built-in tables.

Tables are hand written from the Intel SDM, the IBM and Phoenix BIOS
references and the PCI specification. No third-party data is copied.

## 8. Performance lints (for x86-64-v3 code)

58. Missing `vzeroupper` before `call`/`ret` after ymm/zmm use (AVX-SSE
    transition penalty).
59. False dependencies: `popcnt`/`lzcnt`/`tzcnt` output not zeroed first;
    partial flag stalls (`inc`/`dec` followed by a CF read).
60. Slow or microcoded forms: `loop`, `enter`, `xchg` with memory
    (implicitly locked), `bt` on memory, 3-component `lea` on older cores,
    division by a constant.
61. Latency, throughput and uop hover per form, if a source with a usable
    licence is available (uops.info). Optional.

## 9. Inline asm in C

62. **[have]** Template hover, constraint hover, mode and level checks.
63. Operand reference out of range (`%2` with only two operands) and
    unknown `%[name]`.
64. Missing clobbers: the template writes a register that is not an output
    or clobber. The most common inline asm bug.
65. Missing `"memory"` clobber when the template writes memory that is not
    an operand.
66. Missing early-clobber `&` when an output is written before all inputs
    are read.
67. C type width vs instruction suffix (`uint8_t` into `movl`); operand
    modifier vs width (`%k0` on an 8-bit variable).
68. Fixed register named in the template that a `"r"` constraint could
    also have chosen.
69. Asm with side effects but outputs only, and no `volatile` (could be
    optimised away).

## 10. Symbols and structure

70. **[have]** Go-to-definition, references, rename, outline, folding
    across files and includes.
71. Undefined labels, unused labels, unused `#define`/`.equ`, duplicate
    definitions across files that get linked together.
72. Local label errors: `1f` with no following `1:`; `1b` reaching past an
    intended boundary.
73. `.globl` naming an undefined symbol; `.extern` hygiene.
74. Macro expansion hover (`.macro`, `.rept`, cpp); diagnostics inside
    expansions mapped back to the call site.
75. **[have]** Inactive `#if` regions faded.
76. gas `.include` followed.

## 11. Toolchain truth

77. **[have]** Real gas errors on save, mapped through includes.
78. **[have]** Our diagnostics win over gas's on the same line, to avoid duplicates.
79. Opt-in on-type gas assembly (default off).

## 12. Lint workflow

80. **[have]** `falcon-asm lint` CLI with no editor: walks a tree using
    the compile database, prints `file:line:col` output, and also JSON and
    SARIF. Exit codes, a `--fix` mode, and a pre-commit friendly fast path.
81. Quick fixes: add a `cld`, `vzeroupper`, missing clobber or missing
    suffix; swap to the valid form; insert an `ignore` comment; convert
    `%rax` to the mode-correct register. **[have]** the code action
    plumbing, the `ignore` comment and the mode-correct register.
82. **[have]** Rule configuration in `.falcon-asm.json`: enable/disable
    and severity per rule, per path glob.
83. **[have]** Suppression: `falcon-asm: ignore=<code>` for a line,
    `ignore-next`, `ignore-file`, and region begin/end.
84. Inlay hints with tracked register state at interesting points (e.g.
    `ah=42h` before `int $0x13`). Off by default.
85. Workspace symbols and call hierarchy (who calls this label).
86. Every rule documented in `docs/RULES.md`, with a bad and good example
    and the reasoning, and its own test fixtures. **[have]** the generator
    (`make rules-doc`) and the test that fails when the file is stale.

## Build order

The foundations come first because every rule plugs into them:

1. Operand parser (section 1, item 1).
2. CFG and forward dataflow framework (section 3).
3. Rule registry with codes, severities, suppression and
   `.falcon-asm.json` config (items 82, 83).
4. `falcon-asm lint` CLI (item 80).

After that the sections are largely independent and can be built in
parallel. Estimated total: about 30-35 days of agent work, most of it in
dataflow, calling conventions, boot protocol checks and inline asm
clobber analysis.
