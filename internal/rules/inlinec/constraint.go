package inlinec

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
)

// opInfo is what the rules need to know about one operand's constraint:
// whether it is an output, where the compiler may put it, and whether it is
// tied to another operand. It is deliberately coarse: GCC's constraint
// language is large, and anything not modelled here makes the operand
// "anything" (anyReg), which keeps every rule quiet about it.
type opInfo struct {
	idx    int
	out    bool // output list
	rw     bool // '+': read and written
	early  bool // '&'
	flag   bool // =@cc flag output
	tiedTo int  // input matching an output ("0", "[name]"), -1 otherwise

	fixed  regSet // registers the constraint names outright (a b c d S D A Yz)
	gpr    bool   // the compiler may choose any general register (r R l U g ...)
	byteQ  bool   // ... one with a low byte: a b c d outside 64-bit code, any GPR in it (q)
	highQ  bool   // ... one with a high byte: a b c d (Q)
	vec    bool   // ... an SSE register (x v)
	mask   bool   // ... a mask register (k Yk)
	mem    bool   // ... a memory operand (m o V g X ...)
	imm    bool   // ... an immediate
	anyReg bool   // a constraint this model does not understand: assume any register
}

// regOK reports whether the compiler could place the operand in fam. It is
// what makes a hard-coded register in the template a conflict: a free "r"
// operand may well land in %eax.
func (o *opInfo) regOK(fam string, mode int) bool {
	switch {
	case o.anyReg:
		return true
	case o.fixed[fam]:
		return true
	case !isGPR(fam) || fam == "rsp":
	case o.gpr || o.byteQ && mode == 64:
		return mode == 64 || !isREXOnly(fam)
	case o.byteQ || o.highQ:
		return fam == "rax" || fam == "rbx" || fam == "rcx" || fam == "rdx"
	}
	switch {
	case isXMM(fam):
		return o.vec
	case isMask(fam):
		return o.mask
	}
	return false
}

// regLike reports whether the operand may be a register at all.
func (o *opInfo) regLike() bool {
	return o.anyReg || o.gpr || o.byteQ || o.highQ || o.vec || o.mask || len(o.fixed) > 0
}

// memOnly reports an operand that is always a memory reference.
func (o *opInfo) memOnly() bool { return o.mem && !o.regLike() && !o.imm }

// immOnly reports an operand that is always an immediate.
func (o *opInfo) immOnly() bool { return o.imm && !o.mem && !o.regLike() }

// analyzeOperands derives opInfo for every numbered operand of st.
func analyzeOperands(st *inlineasm.Stmt) []opInfo {
	n := len(st.Outputs) + len(st.Inputs)
	out := make([]opInfo, 0, n)
	for i := 0; i < n; i++ {
		op, _ := st.Operand(i)
		info := parseConstraint(op.Constraint)
		info.idx = i
		info.out = i < len(st.Outputs)
		info.tiedTo = -1
		if !info.out {
			info.tiedTo = tiedIndex(st, op.Constraint)
		}
		out = append(out, info)
	}
	// A tied input lives wherever its output does, and makes that output
	// read-write: "=r"(x) with "0"(y) is "+r" with two names.
	for i := range out {
		if t := out[i].tiedTo; t >= 0 && t < len(st.Outputs) {
			out[t].rw = true
			o := out[t]
			out[i].fixed, out[i].gpr, out[i].vec, out[i].mask = o.fixed, o.gpr, o.vec, o.mask
			out[i].byteQ, out[i].highQ = o.byteQ, o.highQ
			out[i].mem, out[i].imm, out[i].anyReg = o.mem, o.imm, o.anyReg
		}
	}
	return out
}

// tiedIndex returns the output an input constraint matches ("0", "[res]"),
// -1 when it matches none. Only the first alternative is read, which is
// where a matching constraint sits in practice.
func tiedIndex(st *inlineasm.Stmt, c string) int {
	alt, _, _ := strings.Cut(c, ",")
	for i := 0; i < len(alt); i++ {
		ch := alt[i]
		switch {
		case ch >= '0' && ch <= '9':
			v := 0
			for i < len(alt) && alt[i] >= '0' && alt[i] <= '9' {
				v = v*10 + int(alt[i]-'0')
				i++
			}
			return v
		case ch == '[':
			j := strings.IndexByte(alt[i:], ']')
			if j < 0 {
				return -1
			}
			name := strings.TrimSpace(alt[i+1 : i+j])
			for _, o := range st.Outputs {
				if o.Name == name {
					return o.Index
				}
			}
			return -1
		}
	}
	return -1
}

// parseConstraint reads the modifier characters and every alternative.
func parseConstraint(c string) opInfo {
	info := opInfo{fixed: regSet{}}
	if strings.HasPrefix(strings.TrimLeft(c, "=+&"), "@cc") {
		info.flag = true
		info.out = true
		return info
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch ch {
		case '=', '%', '*', '?', '!', ' ', '\t', ',':
		case '+':
			info.rw = true
		case '&':
			info.early = true
		case '#':
			// The rest of the alternative only steers register
			// preferencing.
			for i+1 < len(c) && c[i+1] != ',' {
				i++
			}
		case '[':
			j := strings.IndexByte(c[i:], ']')
			if j < 0 {
				i = len(c)
				continue
			}
			i += j
		case 'Y', 'B', 'W', 'j':
			if i+1 < len(c) {
				code := c[i : i+2]
				i++
				switch code {
				case "Yz":
					info.fixed.add("xmm0")
				case "Yk":
					info.mask = true
				default:
					info.anyReg = true
				}
			}
		default:
			if ch >= '0' && ch <= '9' {
				continue
			}
			letter(&info, ch)
		}
	}
	return info
}

func letter(info *opInfo, ch byte) {
	switch ch {
	case 'a':
		info.fixed.add("rax")
	case 'b':
		info.fixed.add("rbx")
	case 'c':
		info.fixed.add("rcx")
	case 'd':
		info.fixed.add("rdx")
	case 'S':
		info.fixed.add("rsi")
	case 'D':
		info.fixed.add("rdi")
	case 'A':
		info.fixed.add("rax", "rdx")
	case 'r', 'R', 'l', 'U':
		info.gpr = true
	case 'q':
		info.byteQ = true
	case 'Q':
		info.highQ = true
	case 'p':
		// An address: its registers are general registers.
		info.gpr = true
	case 'x', 'v':
		info.vec = true
	case 'k':
		info.mask = true
	case 'm', 'o', 'V', '<', '>':
		info.mem = true
	case 'g':
		info.gpr, info.mem, info.imm = true, true, true
	case 'X':
		info.anyReg, info.mem, info.imm = true, true, true
	case 'i', 'n', 's', 'E', 'F', 'G', 'C', 'I', 'J', 'K', 'L', 'M', 'N', 'O', 'e', 'Z':
		info.imm = true
	case 'f', 't', 'u', 'y':
		// x87 and MMX registers: not tracked by these rules.
	default:
		info.anyReg = true
	}
}
