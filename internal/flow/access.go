package flow

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// The read model: which register parts a statement reads, explicit and
// implicit. The transfer functions track what each statement writes; the
// rules that report a use (a partial read, a caller-saved register read
// after a call, a register the BIOS left undefined) need the reads too,
// and every one of them reports a read, so the model under-approximates:
// an operand counts as read only when the instruction certainly reads it,
// and implicit reads are listed only for the instructions whose operands
// the architecture fixes and that commonly matter (string instructions,
// multiply and divide, loop, the sign extensions, the MSR, XCR and CPUID
// instructions, cmpxchg). Missing a read loses a finding; inventing one
// would report a correct program.
//
// Only families with a tracked value (general, segment and control
// registers) are reported: no rule follows a vector or mask register's
// reads.

// Access is one register part a statement reads: the family, the
// inclusive bit range of the family and the operand that names it (-1 for
// an implicit read).
type Access struct {
	Reg    Reg
	Lo, Hi int
	Op     int
}

// Width is the part's width in bits.
func (a Access) Width() int { return a.Hi - a.Lo + 1 }

// Bits is the part as a mask over the family.
func (a Access) Bits() uint64 { return bitsOf(a.Lo, a.Hi) }

func bitsOf(lo, hi int) uint64 {
	w := hi - lo + 1
	if w >= 64 {
		return ^uint64(0)
	}
	return (uint64(1)<<uint(w) - 1) << uint(lo)
}

// Name is the register the part names: "ah" for bits 8-15 of the four
// legacy families, else the family's name at the part's width.
func (a Access) Name() string {
	if a.Lo == 8 && a.Hi == 15 && a.Reg >= RAX && a.Reg <= RBX {
		return gpr8h[a.Reg]
	}
	return a.Reg.Name(a.Width())
}

// destRMW lists the two-or-more-operand instructions whose destination
// (last) operand is an input as well as an output.
var destRMW = map[string]bool{
	"add": true, "adc": true, "sub": true, "sbb": true, "and": true, "or": true, "xor": true,
	"shl": true, "sal": true, "shr": true, "sar": true, "rol": true, "ror": true, "rcl": true, "rcr": true,
	"shld": true, "shrd": true, "xchg": true, "xadd": true, "adcx": true, "adox": true,
	"bts": true, "btr": true, "btc": true, "cmpxchg": true,
}

// pureWriteOne are the one-operand instructions that only write their
// operand; every other one-operand instruction reads it (inc, not, push,
// an indirect jump, a one-operand multiply, ltr).
var pureWriteOne = map[string]bool{
	"pop": true, "rdrand": true, "rdseed": true, "rdfsbase": true, "rdgsbase": true, "str": true,
	"sldt": true, "smsw": true, "rdpid": true, "fnstsw": true, "fstsw": true, "rdsspd": true, "rdsspq": true,
}

// branchCategories are the isa categories whose operands are all inputs:
// a branch target, a pushed value.
var branchCategories = map[string]bool{
	"COND_BR": true, "UNCOND_BR": true, "CALL": true, "RET": true, "PUSH": true,
}

// lastReadOnly reports an instruction that never writes its last operand
// through the generic path: the comparisons, the stores to places no
// register tracks (ports, descriptor tables), branches and pushes.
func lastReadOnly(inst *isa.Instruction) bool {
	return readOnlyLast[inst.Name] || strings.HasPrefix(inst.Name, "prefetch") || branchCategories[inst.Category]
}

// DestRead reports whether the instruction reads its last explicit
// operand: a one-operand instruction unless it is a pure write (pop,
// setcc, rdrand), a longer one when the destination is also a source
// (arithmetic, exchanges, cmov) or only a source (cmp, test, out).
func DestRead(st *asm.Stmt) bool {
	if st.Kind != asm.Instr || st.Inst == nil || len(st.Ops) == 0 {
		return false
	}
	name := st.Inst.Name
	switch n := len(st.Ops); {
	case n == 1:
		return !pureWriteOne[name] && !strings.HasPrefix(name, "set")
	case name == "imul":
		// The three-operand form writes a product of the other two.
		return n == 2
	}
	return destRMW[name] || readOnlyLast[name] || strings.HasPrefix(name, "cmov")
}

// DestWritten reports whether the instruction writes its last explicit
// operand. The one-operand multiplies and divides write the accumulator,
// not their operand; lmsw writes CR0.
func DestWritten(st *asm.Stmt) bool {
	if st.Kind != asm.Instr || st.Inst == nil || len(st.Ops) == 0 || lastReadOnly(st.Inst) {
		return false
	}
	switch st.Inst.Name {
	case "mul", "div", "imul", "idiv":
		return len(st.Ops) != 1
	case "lmsw", "jmp", "call", "ret", "lret", "int":
		return false
	}
	return true
}

// zeroIdiom is xor/sub of a register with itself: a write of zero that
// reads nothing, whatever the register held.
func zeroIdiom(st *asm.Stmt) bool {
	switch st.Inst.Name {
	case "xor", "sub", "pxor", "xorps", "xorpd", "vpxor", "vxorps", "vxorpd":
	default:
		return false
	}
	return len(st.Ops) == 2 && st.Ops[0].Kind == asm.OpReg && st.Ops[1].Kind == asm.OpReg &&
		strings.EqualFold(st.Ops[0].RegName, st.Ops[1].RegName)
}

// Reads returns the register parts statement i reads, explicit operands
// first in operand order, then the implicit ones.
func Reads(p *asm.Program, i int) []Access { return AppendReads(nil, p, i) }

// AppendReads is Reads appending to out, so a loop over every statement
// can reuse one buffer.
func AppendReads(out []Access, p *asm.Program, i int) []Access {
	st := &p.Stmts[i]
	if st.Kind != asm.Instr || st.Inst == nil || zeroIdiom(st) {
		return out
	}
	add := func(regName string, op int) {
		if r, lo, hi, ok := RegOf(regName); ok && r.HasValue() {
			out = append(out, Access{Reg: r, Lo: lo, Hi: hi, Op: op})
		}
	}
	n := len(st.Ops)
	for k := range st.Ops {
		op := &st.Ops[k]
		switch op.Kind {
		case asm.OpMem:
			if op.Base != "" && !op.RIPRel {
				add(op.Base, k)
			}
			if op.Index != "" {
				add(op.Index, k)
			}
		case asm.OpReg:
			if k != n-1 || DestRead(st) {
				add(op.RegName, k)
			}
		}
	}
	return appendImplicit(out, p, i, st)
}

// appendImplicit appends the registers an instruction reads without naming
// them, at the width it reads them.
func appendImplicit(out []Access, p *asm.Program, i int, st *asm.Stmt) []Access {
	name := st.Inst.Name
	aw := addrBitsAt(p, i, st.Mode)
	part := func(r Reg, w int) {
		out = append(out, Access{Reg: r, Lo: 0, Hi: w - 1, Op: -1})
	}
	// acc is the accumulator width of stos, scas and cmpxchg: the
	// register operand's, else the suffix's.
	acc := func() int {
		for k := range st.Ops {
			if st.Ops[k].Kind == asm.OpReg && st.Ops[k].Reg != nil && strings.HasPrefix(st.Ops[k].Reg.Class, "gpr") {
				return st.Ops[k].Reg.Width
			}
		}
		if w := suffixBits(st); w != 0 {
			return w
		}
		return 8
	}
	switch name {
	case "movs", "cmps":
		part(RSI, aw)
		part(RDI, aw)
	case "lods", "outs":
		part(RSI, aw)
	case "stos", "ins", "scas":
		part(RDI, aw)
	}
	switch name {
	case "stos", "scas":
		part(RAX, acc())
	case "ins", "outs":
		part(RDX, 16)
	}
	switch name {
	case "movs", "stos", "lods", "scas", "cmps", "ins", "outs":
		if hasPrefixAt(p, i, "rep", "repe", "repz", "repne", "repnz") {
			part(RCX, aw)
		}
	case "loop", "loope", "loopne", "loopz", "loopnz":
		part(RCX, aw)
	case "jcxz":
		part(RCX, 16)
	case "jecxz":
		part(RCX, 32)
	case "jrcxz":
		part(RCX, 64)
	case "mul", "imul", "div", "idiv":
		if len(st.Ops) != 1 {
			break
		}
		w := st.Ops[0].Width()
		if w == 0 {
			w = suffixBits(st)
		}
		div := name == "div" || name == "idiv"
		switch {
		case w == 8 && div:
			// The dividend is AX; the upper half is AH, not DX.
			part(RAX, 16)
		case w == 8:
			part(RAX, 8)
		case w > 8:
			part(RAX, w)
			if div {
				part(RDX, w)
			}
		}
	case "cbw", "cbtw":
		part(RAX, 8)
	case "cwde", "cwtl", "cwd", "cwtd":
		part(RAX, 16)
	case "cdqe", "cltq", "cdq", "cltd":
		part(RAX, 32)
	case "cqo", "cqto":
		part(RAX, 64)
	case "xlat", "xlatb":
		part(RBX, aw)
		part(RAX, 8)
	case "rdmsr", "xgetbv":
		part(RCX, 32)
	case "wrmsr", "xsetbv":
		part(RCX, 32)
		part(RAX, 32)
		part(RDX, 32)
	case "cpuid":
		part(RAX, 32)
	case "cmpxchg":
		part(RAX, acc())
	case "cmpxchg8b":
		part(RAX, 32)
		part(RDX, 32)
		part(RBX, 32)
		part(RCX, 32)
	case "cmpxchg16b":
		part(RAX, 64)
		part(RDX, 64)
		part(RBX, 64)
		part(RCX, 64)
	case "sahf":
		out = append(out, Access{Reg: RAX, Lo: 8, Hi: 15, Op: -1})
	case "leave":
		part(RBP, aw)
	}
	return out
}

// hasPrefixAt reports a prefix on statement i or on a lone prefix
// statement ("rep;") right before it.
func hasPrefixAt(p *asm.Program, i int, names ...string) bool {
	check := func(ps []string) bool {
		for _, x := range ps {
			for _, n := range names {
				if x == n {
					return true
				}
			}
		}
		return false
	}
	if check(p.Stmts[i].Prefixes) {
		return true
	}
	if i > 0 {
		prev := &p.Stmts[i-1]
		if prev.Kind == asm.Instr && prev.Mnemonic == "" && !prev.InMacroDef {
			return check(prev.Prefixes)
		}
	}
	return false
}

// addrBitsAt is statement i's address size in mode (the width of the
// count and index registers string instructions and loop use).
func addrBitsAt(p *asm.Program, i int, mode asm.Mode) int {
	switch mode {
	case asm.Mode16:
		if hasPrefixAt(p, i, "addr32") {
			return 32
		}
		return 16
	case asm.Mode32:
		if hasPrefixAt(p, i, "addr16") {
			return 16
		}
		return 32
	}
	if hasPrefixAt(p, i, "addr32") {
		return 32
	}
	return 64
}
