package encoding

import "git.airies.net/vifair22/falcon-asm/internal/lint"

func init() {
	langs := lint.LangAsm | lint.LangInline
	reg := func(code string, def lint.Severity, doc lint.Doc) {
		lint.Register(&lint.Rule{Code: code, Section: 1, Default: def, Langs: langs, Doc: doc, Check: report})
	}
	reg(CodeNoForm, lint.Warning, lint.Doc{
		Summary: "No form of the instruction takes these operands (lgdt %eax: lgdt needs memory).",
		Why: "Every instruction has a fixed set of operand forms: lgdt and lidt read a 6- or 10-byte " +
			"pseudo-descriptor from memory, lea needs a memory operand, an immediate is never a " +
			"destination. The operands are matched against the instruction's forms from the ISA " +
			"tables and the message lists the valid ones. When the problem is more specific (a size " +
			"mismatch, a segment register, a fixed register, a decoration) that rule reports instead. " +
			"Branches, string and x87 instructions are left to the assembler, whose operand rules " +
			"there do not fit the tables.",
		Bad:  "\tlgdt %eax",
		Good: "\tlgdt gdt_descriptor",
	})
	reg(CodeSizeMismatch, lint.Warning, lint.Doc{
		Summary: "Operand sizes disagree with the suffix or with each other (movw %eax, %bx).",
		Why: "The AT&T suffix names the operand size, and the register operands must agree with it " +
			"and with each other. GAS rejects the mismatch, but only after the edit-assemble cycle; " +
			"the usual cause is a register renamed for a new mode while the suffix stayed. Forms " +
			"whose operands legitimately differ in size (movzbl, the %cl count of a shift, the %dx " +
			"port of in and out) are matched from the tables and not reported. A quick fix changes " +
			"the suffix when the registers agree on one size.",
		Bad:  "\tmovw %eax, %ebx",
		Good: "\tmovl %eax, %ebx",
	})
	reg(CodeSizeAmbiguous, lint.Warning, lint.Doc{
		Summary: "Memory operand with no suffix and no register to give its size (inc (%bx)).",
		Why: "Without a suffix or a register operand nothing says whether inc (%bx) increments a " +
			"byte, a word or a dword. GAS picks a default and warns, or for movzx and movsx from " +
			"memory silently assumes a byte; either way the size was never written down. push and " +
			"pop are not reported: they default to the stack width by design.",
		Bad:  "\tincw (%bx)\n\tinc (%bx)",
		Good: "\tincw (%bx)\n\tincb (%bx)",
	})
	reg(CodeREXHighByte, lint.Warning, lint.Doc{
		Summary: "%ah/%bh/%ch/%dh in an instruction that needs a REX prefix (movb %ah, %sil).",
		Why: "With a REX prefix the encodings of %ah, %ch, %dh and %bh name %spl, %bpl, %sil and " +
			"%dil instead, so a high-byte register cannot appear in any instruction that needs REX: " +
			"one using r8-r15, the uniform byte registers, a 64-bit operand or xmm8-15. GAS rejects " +
			"it; the fix is to move the byte through %al or another low register.",
		Bad:  "\t.code64\n\tmovb %ah, %sil",
		Good: "\t.code64\n\tmovb %ah, %al\n\tmovb %al, %sil",
	})
	reg(CodeAddr16, lint.Warning, lint.Doc{
		Summary: "Invalid 16-bit addressing: (%si,%di), (%ax), a scaled index, or 16-bit registers in 64-bit code.",
		Why: "16-bit addressing has only eight forms: a base of %bx or %bp, an index of %si or %di, " +
			"either or both, with no scale. (%ax), (%si,%di), (%bx,%bp) and (%bx,%si,2) do not exist, " +
			"addr32 cannot use them, and 64-bit code has no 16-bit addressing at all. Real-mode code " +
			"written with 32-bit habits trips on these. A quick fix swaps a reversed pair such as " +
			"(%si,%bx).",
		Bad:  "\t.code16\n\tmovw (%si,%di), %ax",
		Good: "\t.code16\n\tmovw (%bx,%di), %ax",
	})
	reg(CodeAddrReg, lint.Warning, lint.Doc{
		Summary: "Register that cannot address memory: %esp as an index, %rip outside 64-bit code, mixed base/index widths.",
		Why: "%esp and %rsp cannot be an index (that SIB encoding means \"no index\"), %rip-relative " +
			"addressing exists only in 64-bit code and takes no index, the base and index of one " +
			"address have one width, segment and vector registers are not general registers (a " +
			"vector index is only for gather and scatter), and the scale is 1, 2, 4 or 8. A quick fix " +
			"swaps base and index when %esp is the index with scale 1.",
		Bad:  "\tmovl (%eax,%esp), %ebx",
		Good: "\tmovl (%esp,%eax), %ebx",
	})
	reg(CodeSreg, lint.Warning, lint.Doc{
		Summary: "Segment register misuse: mov to %cs, pop %cs, lds with a register, push %ds in 64-bit code.",
		Why: "Segment registers have few and irregular forms. mov into %cs assembles but raises #UD; " +
			"%cs is loaded by a far jmp, call or ret. pop %cs does not exist. lds/les/lss/lfs/lgs load " +
			"a far pointer from memory. push and pop of %es, %cs, %ss and %ds are invalid in 64-bit " +
			"code. There is no immediate or segment-to-segment mov, byte registers cannot be used, " +
			"and a store to memory is always 16 bits (a quick fix changes movl to movw).",
		Bad:  "\tmovw %ax, %cs",
		Good: "\tljmp $0x08, $1f\n1:",
	})
	reg(CodeFixedReg, lint.Warning, lint.Doc{
		Summary: "Operand that must be a fixed register: in/out data and port, shift count (in %dx, %bl).",
		Why: "in and out move data only through %al, %ax or %eax and take the port as an imm8 or " +
			"%dx; a shift or rotate count is an immediate or %cl. Any other register is rejected by " +
			"GAS. A quick fix renames a register of the right family (%ecx to %cl as a count, %edx " +
			"to %dx as a port).",
		Bad:  "\tinb %dx, %bl\n\tshll %ecx, %eax",
		Good: "\tinb %dx, %al\n\tshll %cl, %eax",
	})
	reg(CodeImmRange, lint.Warning, lint.Doc{
		Summary: "Immediate out of range for the instruction (out $0x300, int $256, ret $70000, movb $300).",
		Why: "An immediate field has a fixed width: GAS truncates an oversized value with a warning, " +
			"rejects it, or for small negative values wraps it silently; in each case the code does " +
			"not do what it says. Ports above 0xff need %dx, interrupt vectors are 0-0xff, ret pops " +
			"at most 0xffff bytes. A 64-bit operation sign-extends its imm32, so $0xffffffff is not " +
			"0x00000000ffffffff there and GAS rejects it (mov to a register is the exception: it " +
			"becomes movabs). Only constants that evaluate the same under GAS and C precedence are judged.",
		Bad:  "\toutb %al, $0x300\n\taddq $0xffffffff, %rax",
		Good: "\tmovw $0x300, %dx\n\toutb %al, %dx\n\tmovl $0xffffffff, %ecx\n\taddq %rcx, %rax",
	})
	reg(CodeDataRange, lint.Warning, lint.Doc{
		Summary: "Data directive value that does not fit its width (.byte 300, .word 0x10000).",
		Why: ".byte stores 8 bits, .word/.short/.value 16 and .long/.int 32. GAS truncates a larger " +
			"value with only a warning, and wraps a small negative one (.byte -129) without any; " +
			"in a descriptor table or a boot sector signature that is a silent corruption. Values " +
			"built from labels are not judged.",
		Bad:  "\t.byte 0x155",
		Good: "\t.word 0x155",
	})
	reg(CodePrefix, lint.Warning, lint.Doc{
		Summary: "Prefix misuse: lock without a memory destination, rep on a non-string instruction, repeated or conflicting prefixes.",
		Why: "lock applies only to read-modify-write instructions with a memory destination (add, " +
			"xchg, cmpxchg, inc ...). rep repeats string instructions (rep ret, rep nop and rep " +
			"bsf/bsr are accepted idioms). Two prefixes of one group (lock and rep, two segment " +
			"overrides, data16 twice) are rejected or one silently wins, as does a segment prefix " +
			"against a different override in the operand. addr32 in 32-bit code and data16 on a " +
			"w-suffixed instruction outside 16-bit code are redundant, and GAS rejects them.",
		Bad:  "\tlock addl %eax, %ebx\n\trep addl %eax, %ebx",
		Good: "\tlock addl %eax, (%ebx)\n\trep movsb",
	})
	reg(CodeAVXDeco, lint.Warning, lint.Doc{
		Summary: "AVX-512 decoration errors: {%k0} as a mask, {z} without a mask, broadcast width mismatch, mixed vector widths.",
		Why: "%k0 as a write mask encodes \"no masking\"; {z} needs a mask and cannot apply to a " +
			"memory destination; the mask goes on the destination; a broadcast {1toN} must fill the " +
			"vector with elements of the instruction's size (a zmm of dwords is {1to16}); EVEX " +
			"gathers and scatters need a completion mask; and an instruction whose operands share " +
			"one width cannot mix xmm, ymm and zmm. Conversions, extracts, inserts and broadcasts " +
			"whose forms legitimately mix widths are matched from the tables and not reported.",
		Bad:  "\tvaddps (%rax){1to8}, %zmm1, %zmm2{%k0}",
		Good: "\tvaddps (%rax){1to16}, %zmm1, %zmm2{%k1}",
	})
	lint.Register(&lint.Rule{
		Code:         CodeBranchRange,
		Section:      1,
		Default:      lint.Error,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "loop/jcxz/jecxz target beyond the rel8 range, from the assembler listing.",
			Why: "loop, loope, loopne, jcxz, jecxz and jrcxz exist only with an 8-bit displacement: " +
				"the target must lie within -128..+127 bytes of the end of the instruction. Unlike " +
				"jmp and jcc, GAS cannot relax them to a longer form, so a loop body that grows past " +
				"the limit stops assembling. The distance is measured from the listing of the last " +
				"assembly, when both addresses are known and in one section.",
			Bad:  "1:\n\t.fill 200, 1, 0x90\n\tloop 1b",
			Good: "1:\n\t.fill 200, 1, 0x90\n\tdecl %ecx\n\tjnz 1b",
		},
		Check: checkBranchRange,
	})
}
