package asm

import "strings"

// Width is the operand's size in bits: the register's width for a
// register, the AT&T suffix's size for a memory operand of a suffixed
// instruction (movl -> 32), 0 when neither says. x87 suffixes name formats
// rather than sizes and give 0. An immediate has no width of its own.
func (op Operand) Width() int {
	switch op.Kind {
	case OpReg:
		if op.Reg != nil {
			return op.Reg.Width
		}
	case OpMem:
		return int(op.sufBits)
	}
	return 0
}

func (s Stmt) instName() string {
	if s.Kind != Instr || s.Inst == nil {
		return ""
	}
	return s.Inst.Name
}

// IsCondJump reports a conditional jump: jcc, jcxz/jecxz/jrcxz and the
// loop family. xbegin is excluded; its target is an abort handler, not a
// successor.
func (s Stmt) IsCondJump() bool {
	return s.Kind == Instr && s.Inst != nil && s.Inst.Category == "COND_BR" &&
		s.Inst.Name != "xbegin" && s.Inst.Name != "xend"
}

// IsUncondJump reports jmp and ljmp in every form (direct, indirect, far).
func (s Stmt) IsUncondJump() bool {
	switch s.instName() {
	case "jmp", "ljmp", "jmpabs":
		return true
	}
	return false
}

// IsCall reports call and lcall.
func (s Stmt) IsCall() bool {
	switch s.instName() {
	case "call", "lcall":
		return true
	}
	return false
}

// IsRet reports near and far returns (ret, lret). Interrupt returns are
// IsIret.
func (s Stmt) IsRet() bool {
	switch s.instName() {
	case "ret", "lret":
		return true
	}
	return false
}

// IsIret reports iret, iretl/iretd and iretq.
func (s Stmt) IsIret() bool {
	switch s.instName() {
	case "iret", "iretq":
		return true
	}
	return false
}

// IsBranch reports any transfer to an operand-named target: conditional
// and unconditional jumps and calls.
func (s Stmt) IsBranch() bool {
	return s.IsCondJump() || s.IsUncondJump() || s.IsCall()
}

// IsFar reports an inter-segment transfer: ljmp, lcall, lret, and jmp or
// call written with a selector and offset ("jmp $SEL, $off"), which GAS
// assembles as the far form.
func (s Stmt) IsFar() bool {
	switch s.instName() {
	case "ljmp", "lcall", "lret":
		return true
	case "jmp", "call":
		return len(s.Ops) == 2 && s.Ops[0].Kind == OpImm && s.Ops[1].Kind == OpImm
	}
	return false
}

// Flags is a set of EFLAGS bits.
type Flags uint32

// EFLAGS bits, in the names the isa tables use.
const (
	FlagCF Flags = 1 << iota
	FlagPF
	FlagAF
	FlagZF
	FlagSF
	FlagTF
	FlagIF
	FlagDF
	FlagOF
	FlagIOPL
	FlagNT
	FlagRF
	FlagVM
	FlagAC
	FlagVIF
	FlagVIP
	FlagID

	// FlagsStatus are the six arithmetic status flags.
	FlagsStatus = FlagCF | FlagPF | FlagAF | FlagZF | FlagSF | FlagOF
	// FlagsAll is every bit above.
	FlagsAll = FlagID<<1 - 1
)

var flagNames = map[string]Flags{
	"CF": FlagCF, "PF": FlagPF, "AF": FlagAF, "ZF": FlagZF, "SF": FlagSF,
	"TF": FlagTF, "IF": FlagIF, "DF": FlagDF, "OF": FlagOF, "IOPL": FlagIOPL,
	"NT": FlagNT, "RF": FlagRF, "VM": FlagVM, "AC": FlagAC, "VIF": FlagVIF,
	"VIP": FlagVIP, "ID": FlagID,
	// "RFLAGS masked by IA32_FMASK" (syscall): every bit may change.
	"RFLAGS": FlagsAll, "EFLAGS": FlagsAll,
}

// ParseFlags reads an isa flags string ("CF ZF SF OF"). Words that are not
// EFLAGS bits (the x87 C0..C3 condition codes, prose) are ignored.
func ParseFlags(s string) Flags {
	var f Flags
	for s != "" {
		s = strings.TrimLeft(s, " \t,;()")
		j := strings.IndexAny(s, " \t,;()")
		if j < 0 {
			j = len(s)
		}
		f |= flagNames[s[:j]]
		s = s[j:]
	}
	return f
}

// FlagsRead is the EFLAGS bits the instruction reads; 0 for anything but a
// resolved instruction.
func (s Stmt) FlagsRead() Flags {
	if s.Kind != Instr || s.Inst == nil {
		return 0
	}
	return ParseFlags(s.Inst.FlagsRead)
}

// FlagsWritten is the EFLAGS bits the instruction changes, including those
// it leaves undefined: either way the old value is gone.
func (s Stmt) FlagsWritten() Flags {
	if s.Kind != Instr || s.Inst == nil {
		return 0
	}
	return ParseFlags(s.Inst.FlagsWritten) | ParseFlags(s.Inst.FlagsUndefined)
}

// ReadsFlags reports whether the instruction reads any EFLAGS bit.
func (s Stmt) ReadsFlags() bool { return s.FlagsRead() != 0 }

// WritesFlags reports whether the instruction changes any EFLAGS bit.
func (s Stmt) WritesFlags() bool { return s.FlagsWritten() != 0 }

// IsGPRMove reports whether the statement is a general-purpose mov. AT&T
// spells the 64-bit form movq, which resolves exactly to the SSE/MMX movq
// instruction, so a check on the name alone misses `movq $tbl, %rax` and
// `movq %rax, %cr3`. movq counts as mov here unless an operand is an MMX
// or XMM register; movabs is the 64-bit immediate form of mov.
func (s Stmt) IsGPRMove() bool {
	if s.Inst == nil {
		return false
	}
	switch s.Inst.Name {
	case "mov", "movabs":
		return true
	case "movq":
		for i := range s.Ops {
			if r := s.Ops[i].Reg; r != nil && (r.Class == "mmx" || r.Class == "xmm") {
				return false
			}
		}
		return true
	}
	return false
}
