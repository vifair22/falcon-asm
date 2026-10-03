package asm

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// item is one comma-separated operand: tokens [lo, hi) of the statement.
type item struct{ lo, hi int }

// operands splits the operand list on top-level commas and parses each.
// Commas inside parentheses (base, index, scale) and braces (decorations)
// do not split.
func (p *parser) operands(st *Stmt, lo, hi int) {
	if lo >= hi {
		return
	}
	p.items = p.items[:0]
	paren, brace := 0, 0
	start := lo
	for i := lo; i < hi; i++ {
		t := &p.toks[i]
		if t.Kind != lexer.Punct {
			continue
		}
		switch t.Text {
		case "(":
			paren++
		case ")":
			if paren > 0 {
				paren--
			}
		case "{":
			brace++
		case "}":
			if brace > 0 {
				brace--
			}
		case ",":
			if paren == 0 && brace == 0 {
				p.items = append(p.items, item{start, i})
				start = i + 1
			}
		}
	}
	p.items = append(p.items, item{start, hi})

	label := st.Kind == Instr && takesLabel(st.Inst)
	macro := st.Kind != Instr
	sufBits := int16(0)
	if st.Kind == Instr && st.Suffix != "" && !strings.HasPrefix(st.Inst.Category, "X87") {
		// x87 suffixes name formats (s, l, t, ll), not operand
		// sizes, so a suffix-derived width would be wrong there.
		sufBits = suffixBits(st.Suffix)
	}
	// Operands are parsed straight into a chunk with room for every
	// item, so none is copied after parsing; an Operand is about 300
	// bytes. A chunk never regrows, so the statement's slice is cut at
	// once.
	if cap(p.ops)-len(p.ops) < len(p.items) {
		p.ops = make([]Operand, 0, max(opChunk, len(p.items)))
	}
	at := len(p.ops)
	var pending Operand // decorations written as their own operand ({sae})
	havePending := false
	for _, it := range p.items {
		p.ops = p.ops[:len(p.ops)+1]
		op := &p.ops[len(p.ops)-1]
		if p.operand(op, it.lo, it.hi, hi, label, macro) {
			// GAS writes {sae} and {rn-sae} as a separate operand,
			// first in AT&T order; it qualifies the operand next to it.
			mergeDeco(&pending, op)
			havePending = true
			*op = Operand{} // the slot is reused; chunks start zeroed
			p.ops = p.ops[:len(p.ops)-1]
			continue
		}
		if op.Kind == OpMem {
			op.sufBits = sufBits
		}
		if havePending {
			mergeDeco(op, &pending)
			pending, havePending = Operand{}, false
		}
	}
	if len(p.ops) == at {
		return
	}
	if havePending {
		mergeDeco(&p.ops[len(p.ops)-1], &pending)
	}
	st.Ops = p.ops[at:len(p.ops):len(p.ops)]
}

// opChunk is the operand chunk size once the first, estimated, chunk is
// used up; the estimate is an upper bound, so this is only a safety net.
const opChunk = 256

func mergeDeco(dst, src *Operand) {
	if src.Mask != "" {
		dst.Mask = src.Mask
	}
	if src.Zeroing {
		dst.Zeroing = true
	}
	if src.Broadcast != "" {
		dst.Broadcast = src.Broadcast
	}
	if src.Rounding != "" {
		dst.Rounding = src.Rounding
	}
}

func suffixBits(s string) int16 {
	switch s {
	case "b":
		return 8
	case "w":
		return 16
	case "l":
		return 32
	case "q":
		return 64
	}
	return 0
}

// takesLabel reports instructions whose bare-symbol operand is a code
// address (a branch target) rather than a memory reference.
func takesLabel(inst *isa.Instruction) bool {
	if inst == nil {
		return false
	}
	switch inst.Category {
	case "COND_BR":
		return inst.Name != "xend"
	case "UNCOND_BR":
		return inst.Name != "xabort"
	case "CALL":
		return true
	}
	return false
}

func (p *parser) punctAt(i int, s string) bool {
	return p.toks[i].Kind == lexer.Punct && p.toks[i].Text == s
}

// operand parses tokens [lo, hi) into one operand. stmtHi is the
// statement's end, which places an empty operand. decoOnly reports an item
// holding nothing but decorations.
func (p *parser) operand(op *Operand, lo, hi, stmtHi int, label, macro bool) (decoOnly bool) {
	b := p.buf[:0]
	for i := lo; i < hi; i++ {
		if p.toks[i].Kind != lexer.Comment {
			b = append(b, i)
		}
	}
	p.buf = b
	if len(b) == 0 {
		// "movl %eax," or ",%eax": keep the slot so operand counts stay
		// honest, as an expression nothing will check.
		at := p.end(lo - 1)
		if hi < stmtHi {
			at = p.off(hi)
		}
		*op = Operand{Kind: OpExpr, Start: at, End: at}
		return false
	}
	op.Start = p.off(b[0])
	op.End = p.end(b[len(b)-1])
	op.Text = p.src[op.Start:op.End]
	op.Kind = OpExpr
	if macro {
		return false
	}

	n := len(b)
	for n > 1 && p.punctAt(b[n-1], "}") {
		k := n - 2
		for k >= 0 && !p.punctAt(b[k], "{") && !p.punctAt(b[k], "}") {
			k--
		}
		if k < 0 || !p.punctAt(b[k], "{") || !p.decoration(op, b[k+1:n-1]) {
			break
		}
		n = k
	}
	b = b[:n]
	if len(b) == 0 {
		return true
	}
	if p.punctAt(b[0], "*") {
		op.Indirect = true
		b = b[1:]
		if len(b) == 0 {
			return false
		}
	}
	for _, i := range b {
		t := &p.toks[i]
		switch t.Kind {
		case lexer.String:
			return false
		case lexer.Identifier:
			// A macro parameter (\arg) or a .irp variable: the
			// operand is only known once expanded.
			if strings.HasPrefix(t.Text, "\\") {
				return false
			}
		case lexer.Immediate:
			if i != b[0] || strings.HasPrefix(t.Text, "$\\") {
				return false
			}
		case lexer.Punct:
			if t.Text == "\\" || t.Text == "{" || t.Text == "}" {
				return false
			}
		}
	}

	t0 := &p.toks[b[0]]
	switch {
	case t0.Kind == lexer.Immediate:
		p.immediate(op, b)
	case t0.Kind == lexer.Register && len(b) == 1:
		op.Kind = OpReg
		op.RegName = strings.ToLower(t0.Text[1:])
		op.Reg, _ = isa.LookupRegister(op.RegName)
	case t0.Kind == lexer.Register && len(b) == 4 && strings.EqualFold(t0.Text, "%st") &&
		p.punctAt(b[1], "(") && p.toks[b[2]].Kind == lexer.Number && p.punctAt(b[3], ")"):
		// "%st (1)": the lexer joins the index only when no blank
		// separates it.
		op.Kind = OpReg
		op.RegName = "st(" + p.toks[b[2]].Text + ")"
		op.Reg, _ = isa.LookupRegister(op.RegName)
	default:
		p.memory(op, b, label)
		if op.Kind == OpExpr {
			// Half-parsed fields would read as facts to a rule that
			// forgets to check Kind; an expression carries none.
			*op = Operand{
				Kind: OpExpr, Text: op.Text, Start: op.Start, End: op.End, Indirect: op.Indirect,
				Mask: op.Mask, Zeroing: op.Zeroing, Broadcast: op.Broadcast, Rounding: op.Rounding,
			}
		}
	}
	return false
}

// decoration applies one {...} decoration; false when it is not one.
func (p *parser) decoration(op *Operand, in []int) bool {
	if len(in) == 0 {
		return false
	}
	text := p.src[p.off(in[0]):p.end(in[len(in)-1])]
	if len(in) == 1 && p.toks[in[0]].Kind == lexer.Register {
		op.Mask = strings.ToLower(text[1:])
		return true
	}
	low := strings.ToLower(text)
	switch {
	case low == "z":
		op.Zeroing = true
	case strings.HasPrefix(low, "1to"):
		op.Broadcast = low
	case low == "sae" || strings.HasSuffix(low, "-sae"):
		op.Rounding = low
	default:
		return false
	}
	return true
}

func (p *parser) immediate(op *Operand, b []int) {
	op.Kind = OpImm
	start := p.off(b[0]) + 1
	end := p.end(b[len(b)-1])
	// Left only: a trailing blank can only be the space constant "' ".
	op.Imm = strings.TrimLeft(p.src[start:end], " \t")
	if len(b) == 1 {
		op.ImmVal = p.evalAtom(op.Imm)
		return
	}
	op.ImmVal = p.evalText(op.Imm)
}

// memory parses seg:disp(base,index,scale) with every part optional, and a
// bare symbol in a branch, which is a code address rather than memory.
func (p *parser) memory(op *Operand, b []int, label bool) {
	if len(b) >= 2 && p.toks[b[0]].Kind == lexer.Register && p.punctAt(b[1], ":") {
		op.Seg = strings.ToLower(p.toks[b[0]].Text[1:])
		b = b[2:]
		if len(b) == 0 {
			return
		}
	}
	disp := b
	var group []int
	if p.punctAt(b[len(b)-1], ")") {
		depth := 0
		for k := len(b) - 1; k >= 0; k-- {
			switch {
			case p.punctAt(b[k], ")"):
				depth++
			case p.punctAt(b[k], "("):
				depth--
			}
			if depth != 0 {
				continue
			}
			// Parentheses that open on a register or a comma hold
			// base, index and scale; any others are part of the
			// displacement expression, "(RELOC + 4)".
			if k+1 < len(b)-1 && (p.toks[b[k+1]].Kind == lexer.Register || p.punctAt(b[k+1], ",")) {
				disp, group = b[:k], b[k+1:len(b)-1]
			}
			break
		}
	}
	for _, i := range disp {
		if p.toks[i].Kind == lexer.Register {
			return
		}
	}
	if len(disp) > 0 {
		op.Disp = p.src[p.off(disp[0]):p.end(disp[len(disp)-1])]
		if len(disp) == 1 {
			op.DispVal = p.evalAtom(op.Disp)
		} else {
			op.DispVal = p.evalText(op.Disp)
		}
	}
	switch {
	case group != nil:
		if !p.baseIndex(op, group) {
			return
		}
		op.RegName = op.Base
	case op.Seg == "" && label && !op.Indirect:
		op.Kind = OpLabel
		op.Symbol = p.symbolName(disp)
		return
	}
	op.Kind = OpMem
}

// baseIndex parses the inside of "(base,index,scale)".
func (p *parser) baseIndex(op *Operand, in []int) bool {
	var parts [3][]int
	n, s := 0, 0
	for k, i := range in {
		t := &p.toks[i]
		if t.Kind == lexer.Punct && t.Text == "," {
			if n == 2 {
				return false
			}
			parts[n] = in[s:k]
			n++
			s = k + 1
			continue
		}
		if t.Kind == lexer.Punct && (t.Text == "(" || t.Text == ")") {
			return false
		}
	}
	parts[n] = in[s:]
	reg := func(part []int) (string, bool) {
		switch {
		case len(part) == 0:
			return "", true
		case len(part) == 1 && p.toks[part[0]].Kind == lexer.Register:
			return strings.ToLower(p.toks[part[0]].Text[1:]), true
		}
		return "", false
	}
	var ok bool
	if op.Base, ok = reg(parts[0]); !ok {
		return false
	}
	if op.Index, ok = reg(parts[1]); !ok {
		return false
	}
	op.RIPRel = op.Base == "rip" || op.Base == "eip"
	if op.Index == "" {
		return n < 2 || len(parts[2]) > 0 || n == 0
	}
	if n < 2 || len(parts[2]) == 0 {
		op.Scale = 1
		return true
	}
	sc := parts[2]
	text := p.src[p.off(sc[0]):p.end(sc[len(sc)-1])]
	if v := p.evalText(text); v.Known {
		op.Scale = int(v.V)
	}
	return true
}

// symbolName is a branch target's symbol: the name alone when the target
// is one symbol (with an optional @PLT-style suffix), else the expression,
// which resolves to no label and so stays unknown downstream.
func (p *parser) symbolName(disp []int) string {
	if len(disp) == 0 {
		return ""
	}
	t := &p.toks[disp[0]]
	single := len(disp) == 1 ||
		(len(disp) == 2 && p.toks[disp[1]].Kind == lexer.Identifier && strings.HasPrefix(p.toks[disp[1]].Text, "@"))
	if single && (t.Kind == lexer.LabelRef || t.Kind == lexer.LocalLabelRef || t.Text == ".") {
		return t.Text
	}
	return p.src[p.off(disp[0]):p.end(disp[len(disp)-1])]
}
