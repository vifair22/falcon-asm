package inlinec

import "strings"

// What an instruction reads and writes. internal/isa describes forms and
// flags but not operand access or implicit registers, so this file carries
// the small part of that knowledge the inline asm rules need. It is
// written for the instructions inline asm actually uses (CPUID, the
// time-stamp and MSR reads, string moves, the system instructions); an
// instruction it does not list falls back to the AT&T convention of a
// written last operand, and an instruction the ISA tables do not know at
// all has no effects, so it can never produce a finding.

// noExplicitWrite lists instructions whose explicit operands are all read:
// comparisons, stores to places no rule tracks (ports, descriptor tables),
// one-operand arithmetic whose result goes to fixed registers, and hints.
var noExplicitWrite = setOf(
	"cmp", "test", "bt", "push", "out", "int", "int1", "int3", "into",
	"lgdt", "lidt", "lldt", "ltr", "lmsw", "invlpg", "invlpga", "invpcid", "invept", "invvpid",
	"clflush", "clflushopt", "clwb", "cldemote",
	"ucomiss", "ucomisd", "comiss", "comisd", "vucomiss", "vucomisd", "vcomiss", "vcomisd",
	"ptest", "vptest", "vtestps", "vtestpd",
	"mul", "div", "idiv", "verr", "verw", "wrfsbase", "wrgsbase", "ldmxcsr", "vldmxcsr",
	"fxrstor", "fxrstor64", "xrstor", "xrstor64", "xrstors", "xrstors64",
	"bound", "monitor", "umonitor", "mwait", "umwait", "tpause", "nop", "ud0", "ud1", "ud2",
	"maskmovdqu", "vmaskmovdqu", "maskmovq",
	"pcmpestri", "pcmpistri", "vpcmpestri", "vpcmpistri", "pcmpestrm", "pcmpistrm", "vpcmpestrm", "vpcmpistrm",
	"ret", "lret", "enter", "vmptrld", "vmclear", "vmwrite", "movdir64b", "clzero",
	"wrmsr", "xsetbv", "wrpkru",
)

// writesAllOperands exchange their operands.
var writesAllOperands = setOf("xchg", "xadd")

// x87Writes are the x87 instructions that store to their operand; every
// other x87 instruction only reads memory, and the x87 stack itself is not
// tracked.
var x87Writes = setOf(
	"fst", "fstp", "fist", "fistp", "fisttp", "fbstp", "fnstsw", "fstsw", "fnstcw", "fstcw",
	"fnstenv", "fstenv", "fnsave", "fsave", "fxsave", "fxsave64",
)

// writtenOps returns the positions of the explicit operands the
// instruction writes.
func writtenOps(in *insn) []int {
	n := len(in.ops)
	switch {
	case !in.known || n == 0 || in.branch || stringOps[in.name] || noExplicitWrite[in.name]:
		return nil
	case strings.HasPrefix(in.name, "prefetch") || strings.HasPrefix(in.name, "kortest") ||
		strings.HasPrefix(in.name, "ktest"):
		return nil
	case in.name == "imul" && n == 1:
		return nil
	case in.name[0] == 'f' && !x87Writes[in.name]:
		return nil
	case writesAllOperands[in.name]:
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	return []int{n - 1}
}

// pureWrite reports whether the destination's old value is not read: a
// move or a load, a three-operand VEX form, a zeroing idiom. Only the
// early-clobber rule cares, and only to know that an input named as such a
// destination is not being read there.
func pureWrite(in *insn) bool {
	n := len(in.ops)
	if n == 0 {
		return false
	}
	name := in.name
	switch name {
	case "movss", "movsd", "movlps", "movhps", "movlpd", "movhpd", "movs":
		return false
	case "lea", "pop", "rdrand", "rdseed", "rdpid", "rdfsbase", "rdgsbase", "in", "sgdt", "sidt",
		"sldt", "str", "smsw", "popcnt", "lzcnt", "tzcnt", "pdep", "pext", "mulx", "andn", "bextr",
		"bzhi", "sarx", "shlx", "shrx", "rorx", "blsi", "blsr", "blsmsk":
		return true
	case "imul":
		return n == 3
	case "xor", "sub", "pxor", "xorps", "xorpd", "psubb", "psubw", "psubd", "psubq", "sbb":
		return n == 2 && in.ops[0].text == in.ops[1].text && name != "sbb"
	}
	switch {
	case strings.HasPrefix(name, "mov") || strings.HasPrefix(name, "vmov") ||
		strings.HasPrefix(name, "set") || strings.HasPrefix(name, "cvt") || strings.HasPrefix(name, "vcvt"):
		return true
	case name[0] == 'v' && n >= 3 && !strings.Contains(in.ops[n-1].text, "{"):
		return true
	}
	return false
}

// implicitWrites returns the register families the instruction writes
// without naming them. Stack pointer updates (push, pop, call) are left
// out: GCC never allocates %rsp, and a balanced asm leaves it unchanged.
func implicitWrites(in *insn) []string {
	if !in.known {
		return nil
	}
	var out []string
	if in.rep && stringOps[in.name] {
		// rep on anything else is a different instruction (rep nop is
		// pause, rep ret a branch-predictor hint) and leaves %rcx alone.
		out = append(out, "rcx")
	}
	switch in.name {
	case "cpuid":
		out = append(out, "rax", "rbx", "rcx", "rdx")
	case "rdtsc", "rdmsr", "rdpmc", "xgetbv", "rdpkru":
		out = append(out, "rax", "rdx")
	case "rdtscp":
		out = append(out, "rax", "rdx", "rcx")
	case "mul", "imul", "div", "idiv":
		if len(in.ops) != 1 {
			break
		}
		// The 8-bit forms use only %ax; a width the template does not
		// show (an operand reference without a suffix) claims only %rax,
		// since claiming %rdx could be wrong.
		out = append(out, "rax")
		if w := opWidth(in, 0); w > 8 {
			out = append(out, "rdx")
		}
	case "cwtd", "cltd", "cqto", "cwd", "cdq", "cqo":
		out = append(out, "rdx")
	case "cbtw", "cwtl", "cltq", "cbw", "cwde", "cdqe", "xlat", "lahf", "xbegin",
		"aaa", "aas", "daa", "das", "aam", "aad":
		out = append(out, "rax")
	case "in":
		if len(in.ops) < 2 {
			out = append(out, "rax")
		}
	case "lods":
		out = append(out, "rax", "rsi")
	case "stos", "scas", "ins":
		out = append(out, "rdi")
	case "movs", "cmps":
		out = append(out, "rsi", "rdi")
	case "outs":
		out = append(out, "rsi")
	case "loop", "loope", "loopne", "loopz", "loopnz":
		out = append(out, "rcx")
	case "syscall":
		out = append(out, "rcx", "r11")
	case "cmpxchg":
		out = append(out, "rax")
	case "cmpxchg8b", "cmpxchg16b":
		out = append(out, "rax", "rdx")
	case "enter", "leave":
		out = append(out, "rbp")
	case "popa", "popal", "popaw", "popad":
		out = append(out, "rax", "rbx", "rcx", "rdx", "rsi", "rdi", "rbp")
	case "pcmpestri", "pcmpistri", "vpcmpestri", "vpcmpistri":
		out = append(out, "rcx")
	case "pcmpestrm", "pcmpistrm", "vpcmpestrm", "vpcmpistrm":
		out = append(out, "xmm0")
	case "vzeroall", "vzeroupper":
		n := 16
		if in.mode != 64 {
			n = 8
		}
		for i := 0; i < n; i++ {
			out = append(out, "xmm"+itoa(i))
		}
	}
	return out
}

// implicitMemWrite reports an instruction that stores through a register
// it does not name: the string stores and the masked moves through %rdi.
func implicitMemWrite(in *insn) bool {
	switch in.name {
	case "stos", "movs", "ins", "maskmovdqu", "vmaskmovdqu", "maskmovq", "clzero", "movdir64b":
		return in.known
	}
	return false
}

// opWidth is the width of operand i: the suffix's, a literal register's,
// or 0 when the template does not say.
func opWidth(in *insn, i int) int {
	if in.width != 0 {
		return in.width
	}
	if i >= 0 && i < len(in.ops) && in.ops[i].kind == oReg && in.ops[i].fam != "" {
		return in.ops[i].width
	}
	return 0
}

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
