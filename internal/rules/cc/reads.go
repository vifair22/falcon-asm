package cc

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

// The read model. The flow package tracks what each statement writes but
// not what it reads, and the convention rules need reads: a caller-saved
// register read after a call, an argument register a callee reads, a
// result register a caller reads. Every rule that uses it reports a
// read, so the model under-approximates: an operand counts as read only
// when the instruction certainly reads it, and implicit reads are listed
// only for the instructions whose operands are fixed by the architecture
// and commonly matter (string instructions, multiply and divide, loop,
// the sign extensions, the MSR instructions). Missing a read loses a
// finding; inventing one would report a correct program.

// use is one register read: the family and the operand that names it
// (-1 for an implicit read).
type use struct {
	r  flow.Reg
	op int
}

// rmwLast are the two-or-more-operand instructions that read their last
// operand as well as (or instead of) writing it: the arithmetic, the
// compares, the exchanges. Any instruction not listed is treated as only
// writing it, which is the under-approximation.
var rmwLast = map[string]bool{
	"add": true, "adc": true, "sub": true, "sbb": true, "and": true, "or": true, "xor": true,
	"cmp": true, "test": true, "bt": true, "bts": true, "btr": true, "btc": true,
	"xchg": true, "xadd": true, "cmpxchg": true, "imul": true,
	"shl": true, "sal": true, "shr": true, "sar": true, "rol": true, "ror": true, "rcl": true, "rcr": true,
	"shld": true, "shrd": true, "adcx": true, "adox": true, "out": true,
}

// pureWriteOne are one-operand instructions that only write their
// operand; every other one-operand instruction reads it.
var pureWriteOne = map[string]bool{
	"pop": true, "rdrand": true, "rdseed": true, "rdfsbase": true, "rdgsbase": true, "str": true,
	"sldt": true, "smsw": true, "rdpid": true, "fnstsw": true, "fstsw": true, "rdsspd": true, "rdsspq": true,
}

// implicitReads are the fixed registers instructions read without naming
// them (beyond the string instructions, handled with their prefixes).
var implicitReads = map[string][]flow.Reg{
	"cbw": {flow.RAX}, "cbtw": {flow.RAX}, "cwde": {flow.RAX}, "cwtl": {flow.RAX},
	"cdqe": {flow.RAX}, "cltq": {flow.RAX}, "cwd": {flow.RAX}, "cwtd": {flow.RAX},
	"cdq": {flow.RAX}, "cltd": {flow.RAX}, "cqo": {flow.RAX}, "cqto": {flow.RAX},
	"loop": {flow.RCX}, "loope": {flow.RCX}, "loopne": {flow.RCX}, "loopz": {flow.RCX}, "loopnz": {flow.RCX},
	"jcxz": {flow.RCX}, "jecxz": {flow.RCX}, "jrcxz": {flow.RCX},
	"rdmsr": {flow.RCX}, "wrmsr": {flow.RCX, flow.RAX, flow.RDX}, "xgetbv": {flow.RCX},
	"xsetbv": {flow.RCX, flow.RAX, flow.RDX}, "cpuid": {flow.RAX},
	"cmpxchg8b": {flow.RAX, flow.RDX, flow.RBX, flow.RCX}, "cmpxchg16b": {flow.RAX, flow.RDX, flow.RBX, flow.RCX},
	"xlat": {flow.RBX, flow.RAX}, "xlatb": {flow.RBX, flow.RAX}, "sahf": {flow.RAX},
	"cmpxchg": {flow.RAX}, "leave": {flow.RBP},
}

// reads returns the general registers statement st reads, the stack
// pointer excluded (the stack is tracked separately and every push reads
// it).
func reads(st *asm.Stmt) []use {
	if st.Kind != asm.Instr || st.Inst == nil {
		return nil
	}
	name := st.Inst.Name
	ops := st.Ops
	n := len(ops)
	var out []use
	add := func(r flow.Reg, op int) {
		if !r.IsGPR() || r == flow.RSP {
			return
		}
		for _, u := range out {
			if u.r == r {
				return
			}
		}
		out = append(out, use{r, op})
	}
	if zeroIdiom(st) {
		return nil
	}
	if name == "pusha" || name == "pushad" {
		// Saving every register reads them all, but saving a dead value
		// is harmless; counting it would make every handler prologue
		// after a call look like a bug.
		return nil
	}
	for k := range ops {
		op := &ops[k]
		switch op.Kind {
		case asm.OpMem:
			for _, b := range []string{op.Base, op.Index} {
				if b == "" {
					continue
				}
				if r, _, _, ok := flow.RegOf(b); ok {
					add(r, k)
				}
			}
		case asm.OpReg:
			r, _, _, ok := flow.RegOf(op.RegName)
			if !ok {
				continue
			}
			last := k == n-1
			switch {
			case !last:
				add(r, k)
			case n == 1 && !pureWriteOne[name] && !strings.HasPrefix(name, "set"):
				add(r, k)
			case n > 1 && (rmwLast[name] || strings.HasPrefix(name, "cmov")):
				if name == "imul" && n == 3 {
					continue
				}
				add(r, k)
			}
		}
	}
	for _, r := range implicitReads[name] {
		add(r, -1)
	}
	switch name {
	case "mul", "div", "imul", "idiv":
		if n == 1 {
			add(flow.RAX, -1)
			if name == "div" || name == "idiv" {
				// The dividend's upper half; for a byte divide it is AH,
				// part of the same family.
				add(flow.RDX, -1)
			}
		}
	case "movs", "cmps":
		add(flow.RSI, -1)
		add(flow.RDI, -1)
	case "lods", "outs":
		add(flow.RSI, -1)
	case "stos", "scas", "ins":
		add(flow.RDI, -1)
	}
	switch name {
	case "stos", "scas":
		add(flow.RAX, -1)
	case "ins", "outs":
		add(flow.RDX, -1)
	case "movs", "cmps", "lods":
	default:
		return out
	}
	if hasRep(st) {
		add(flow.RCX, -1)
	}
	return out
}

func hasRep(st *asm.Stmt) bool {
	for _, p := range st.Prefixes {
		switch strings.ToLower(p) {
		case "rep", "repe", "repz", "repne", "repnz":
			return true
		}
	}
	return false
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

// readsReg reports whether st reads family r, and which operand names it.
func readsReg(st *asm.Stmt, r flow.Reg) (int, bool) {
	for _, u := range reads(st) {
		if u.r == r {
			return u.op, true
		}
	}
	return 0, false
}
