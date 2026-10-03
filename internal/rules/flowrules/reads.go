package flowrules

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

// regRead is one register part an instruction reads.
type regRead struct {
	r      flow.Reg
	lo, hi int
	op     int // operand index, -1 for an implicit read
}

func (u regRead) width() int { return u.hi - u.lo + 1 }

// name is the register the read names: "ah" for bits 8-15, else the
// family's name at the read's width.
func (u regRead) name() string {
	if u.lo == 8 && u.hi == 15 {
		switch u.r {
		case flow.RAX:
			return "ah"
		case flow.RCX:
			return "ch"
		case flow.RDX:
			return "dh"
		case flow.RBX:
			return "bh"
		}
	}
	return u.r.Name(u.width())
}

// bits is the read part as a mask over the family.
func (u regRead) bits() uint64 {
	w := u.width()
	if w >= 64 {
		return ^uint64(0)
	}
	return (uint64(1)<<uint(w) - 1) << uint(u.lo)
}

// rmwDest lists instructions whose destination (last) operand is also an
// input. Anything not listed here and not only reading its last operand is
// taken to overwrite it: a rule built on reads would rather miss a read
// than invent one, because an invented read is a false report.
var rmwDest = map[string]bool{
	"add": true, "adc": true, "sub": true, "sbb": true, "and": true, "or": true, "xor": true,
	"inc": true, "dec": true, "neg": true, "not": true,
	"shl": true, "sal": true, "shr": true, "sar": true, "rol": true, "ror": true, "rcl": true, "rcr": true,
	"shld": true, "shrd": true, "xchg": true, "xadd": true, "bswap": true, "adcx": true, "adox": true,
	"bts": true, "btr": true, "btc": true, "cmpxchg": true,
}

// readOnly lists instructions that only read every operand.
var readOnly = map[string]bool{
	"cmp": true, "test": true, "bt": true, "push": true, "out": true, "outs": true,
	"mul": true, "div": true, "imul": true, "idiv": true, "jmp": true, "call": true,
	"lgdt": true, "lidt": true, "lldt": true, "ltr": true, "lmsw": true, "verr": true, "verw": true,
	"wrmsr": true, "invlpg": true, "clflush": true, "int": true, "ret": true, "lret": true,
}

// readsOf returns the general, segment and control register parts statement
// i reads, explicit and implicit. Vector and mask registers are left out:
// no rule here tracks them.
func readsOf(p *asm.Program, i int) []regRead {
	return appendReads(nil, p, i)
}

// appendReads is readsOf appending to out, so a loop over every statement
// can reuse one buffer.
func appendReads(out []regRead, p *asm.Program, i int) []regRead {
	st := &p.Stmts[i]
	name := instName(st)
	if name == "" {
		return out
	}
	add := func(regName string, op int) {
		if r, lo, hi, ok := flow.RegOf(regName); ok && r.HasValue() {
			out = append(out, regRead{r: r, lo: lo, hi: hi, op: op})
		}
	}
	n := len(st.Ops)
	// xor %ax, %ax and sub %ax, %ax read nothing: the result is zero
	// whatever the register held.
	if (name == "xor" || name == "sub") && n == 2 && st.Ops[0].Kind == asm.OpReg &&
		st.Ops[0].RegName == st.Ops[1].RegName {
		return out
	}
	imul3 := name == "imul" && n == 3
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
			last := k == n-1
			if !last || readOnly[name] && !imul3 || rmwDest[name] {
				add(op.RegName, k)
			}
		}
	}
	return appendImplicit(out, p, i, name, st)
}

// appendImplicit appends the registers an instruction reads without naming
// them: the string instructions' index and count registers, the
// accumulator of mul/div and the sign extensions, the loop counter.
func appendImplicit(out []regRead, p *asm.Program, i int, name string, st *asm.Stmt) []regRead {
	aw := addrWidth(p, i)
	part := func(r flow.Reg, w int) {
		out = append(out, regRead{r: r, lo: 0, hi: w - 1, op: -1})
	}
	acc := func() int {
		for k := range st.Ops {
			if st.Ops[k].Kind == asm.OpReg && st.Ops[k].Reg != nil && strings.HasPrefix(st.Ops[k].Reg.Class, "gpr") {
				return st.Ops[k].Reg.Width
			}
		}
		if w := suffixWidth(st); w != 0 {
			return w
		}
		return 8
	}
	switch name {
	case "movs", "cmps":
		part(flow.RSI, aw)
		part(flow.RDI, aw)
	case "lods", "outs":
		part(flow.RSI, aw)
	case "stos", "ins", "scas":
		part(flow.RDI, aw)
	}
	switch name {
	case "stos", "scas":
		part(flow.RAX, acc())
	case "ins", "outs":
		part(flow.RDX, 16)
	}
	if isStringOp(st) && hasRep(p, i) {
		part(flow.RCX, aw)
	}
	switch name {
	case "loop", "loope", "loopne", "loopz", "loopnz":
		part(flow.RCX, aw)
	case "jcxz":
		part(flow.RCX, 16)
	case "jecxz":
		part(flow.RCX, 32)
	case "jrcxz":
		part(flow.RCX, 64)
	case "mul", "imul", "div", "idiv":
		if len(st.Ops) != 1 {
			break
		}
		w := st.Ops[0].Width()
		if w == 0 {
			w = suffixWidth(st)
		}
		switch {
		case w == 8 && (name == "div" || name == "idiv"):
			part(flow.RAX, 16)
		case w == 8:
			part(flow.RAX, 8)
		case w > 8:
			part(flow.RAX, w)
			if name == "div" || name == "idiv" {
				part(flow.RDX, w)
			}
		}
	case "cbw", "cbtw":
		part(flow.RAX, 8)
	case "cwde", "cwtl", "cwd", "cwtd":
		part(flow.RAX, 16)
	case "cdqe", "cltq", "cdq", "cltd":
		part(flow.RAX, 32)
	case "xlat":
		part(flow.RBX, aw)
		part(flow.RAX, 8)
	case "rdmsr":
		part(flow.RCX, 32)
	case "wrmsr":
		part(flow.RCX, 32)
		part(flow.RAX, 32)
		part(flow.RDX, 32)
	case "sahf":
		out = append(out, regRead{r: flow.RAX, lo: 8, hi: 15, op: -1})
	case "leave":
		part(flow.RBP, aw)
	}
	return out
}

// suffixWidth is the operand size the AT&T suffix spells, 0 for none.
func suffixWidth(st *asm.Stmt) int {
	s := st.Suffix
	if s == "" && st.Inst != nil && len(st.Mnemonic) > len(st.Inst.Name) {
		s = st.Mnemonic[len(st.Mnemonic)-1:]
	}
	switch s {
	case "b":
		return 8
	case "w":
		return 16
	case "l", "d":
		return 32
	case "q":
		return 64
	}
	return 0
}

// addrWidth is the address size of statement i: the index and count
// registers the string instructions and loop use.
func addrWidth(p *asm.Program, i int) int {
	st := &p.Stmts[i]
	has := func(name string) bool {
		for _, x := range st.Prefixes {
			if x == name {
				return true
			}
		}
		if i > 0 && prefixStmt(&p.Stmts[i-1]) {
			for _, x := range p.Stmts[i-1].Prefixes {
				if x == name {
					return true
				}
			}
		}
		return false
	}
	switch st.Mode {
	case asm.Mode16:
		if has("addr32") {
			return 32
		}
		return 16
	case asm.Mode32:
		if has("addr16") {
			return 16
		}
		return 32
	}
	if has("addr32") {
		return 32
	}
	return 64
}
