package encoding

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// checkAddresses checks every memory operand's base, index and scale:
// registers that cannot address at all (addr-invalid-reg) and the narrow
// set of 16-bit combinations (addr16-invalid). It runs before operand
// preparation, so an address is checked even when another operand of the
// statement is a macro argument.
func (st *stmtCtx) checkAddresses() {
	for j := range st.s.Ops {
		op := &st.s.Ops[j]
		if op.Kind != asm.OpMem || (op.Base == "" && op.Index == "") {
			continue
		}
		if op.Base == "dx" && op.Index == "" && op.Disp == "" && portInstr[st.name] {
			continue // (%dx) is the port operand of in/out, not an address
		}
		var base, index *isa.Register
		ok := true
		if op.Base != "" {
			base, ok = isa.LookupRegister(op.Base)
		}
		if ok && op.Index != "" {
			index, ok = isa.LookupRegister(op.Index)
		}
		if !ok {
			continue // an inline asm operand reference such as (%1)
		}
		if st.addrReg(j, op, base, index) {
			continue
		}
		st.addr16(j, op, base, index)
	}
}

var portInstr = map[string]bool{"in": true, "out": true, "ins": true, "outs": true}

// addrReg reports registers that can never form an address together;
// true when it reported.
func (st *stmtCtx) addrReg(j int, op *asm.Operand, base, index *isa.Register) bool {
	bad := func(msg string, args ...any) bool {
		st.reportOp(CodeAddrReg, j, fmt.Sprintf(msg, args...))
		return true
	}
	if base != nil {
		switch {
		case base.Class == "ip":
			if base.Name == "ip" {
				return bad("%%ip cannot address memory; only %%rip (and %%eip with addr32) in 64-bit code")
			}
			// %rip outside 64-bit code is the mode-register rule's
			// finding already; %eip has no such rule to cover it.
			if st.modeKnown() && st.mode() != asm.Mode64 && base.Name == "eip" {
				return bad("%s-relative addressing exists only in 64-bit code (this is %d-bit code)", regText(base), st.mode())
			}
			if index != nil {
				return bad("%s-relative addressing takes no index register", regText(base))
			}
		case base.Class != "gpr":
			return bad("%s cannot be a base register; a base is a general register", regText(base))
		case base.Width == 8:
			return bad("%s cannot be a base register; byte registers do not address memory", regText(base))
		}
	}
	if index == nil {
		return st.scale(j, op)
	}
	_, vec := vecClass[index.Class]
	switch {
	case index.Class == "ip":
		return bad("%s cannot be an index register", regText(index))
	case vec:
		if !st.hasVSIB() {
			return bad("a vector index (%s) exists only in gather and scatter instructions", regText(index))
		}
		return false
	case index.Class != "gpr":
		return bad("%s cannot be an index register; an index is a general register", regText(index))
	case index.Width == 8:
		return bad("%s cannot be an index register; byte registers do not address memory", regText(index))
	case index.Name == "esp" || index.Name == "rsp":
		msg := fmt.Sprintf("%s cannot be an index register (the SIB encoding means \"no index\")", regText(index))
		if base != nil && base.Class == "gpr" && op.Scale <= 1 && family(base.Name) != "sp" {
			if e, ok := st.swapEdit(op, base.Name, index.Name); ok {
				st.explained = true
				st.a.addFix(CodeAddrReg, op.Start, op.End, msg+"; with scale 1 it can be the base",
					fmt.Sprintf("Swap to (%%%s,%%%s)", index.Name, base.Name), e)
				return true
			}
		}
		return bad("%s", msg)
	}
	if base != nil && base.Class == "gpr" && base.Width != index.Width {
		return bad("base %s (%d-bit) and index %s (%d-bit) differ in width; an address uses one size",
			regText(base), base.Width, regText(index), index.Width)
	}
	return st.scale(j, op)
}

func (st *stmtCtx) scale(j int, op *asm.Operand) bool {
	switch op.Scale {
	case 0, 1, 2, 4, 8:
		return false
	}
	st.reportOp(CodeAddrReg, j, fmt.Sprintf("scale %d is not encodable; the scale is 1, 2, 4 or 8", op.Scale))
	return true
}

// hasVSIB reports an instruction with a vector-index memory form.
func (st *stmtCtx) hasVSIB() bool {
	for _, f := range formsOf(st.inst, st.apx()).forms {
		for _, s := range f.slots {
			for _, a := range s.alts {
				if a.kind == akVSIB {
					return true
				}
			}
		}
	}
	return false
}

// addr16 checks a memory operand addressed through 16-bit registers: the
// base is %bx or %bp, the index %si or %di, there is no scale, and there is
// no index without a base. GAS accepts 16-bit addressing in 32-bit code
// (it adds the 0x67 prefix) but not in 64-bit code.
func (st *stmtCtx) addr16(j int, op *asm.Operand, base, index *isa.Register) {
	if (base == nil || base.Width != 16) && (index == nil || index.Width != 16) {
		return
	}
	bad := func(msg string, args ...any) {
		st.reportOp(CodeAddr16, j, fmt.Sprintf(msg, args...))
	}
	if st.modeKnown() && st.mode() == asm.Mode64 {
		bad("16-bit addressing is not encodable in 64-bit code")
		return
	}
	for _, p := range st.prefixes {
		if p.word == "addr32" {
			bad("addr32 selects 32-bit addressing, which cannot use the 16-bit registers of %s", op.Text)
			return
		}
	}
	isBase := func(r *isa.Register) bool { return r != nil && (r.Name == "bx" || r.Name == "bp") }
	isIndex := func(r *isa.Register) bool { return r != nil && (r.Name == "si" || r.Name == "di") }
	const pairs = "16-bit addressing combines at most a base (%bx or %bp) and an index (%si or %di)"
	switch {
	case index == nil:
		if !isBase(base) && !isIndex(base) {
			bad("%s cannot address memory in 16-bit addressing; use %%bx, %%bp, %%si or %%di", regText(base))
		}
	case op.Scale > 1:
		bad("16-bit addressing has no scaled index; scale %d needs 32-bit registers", op.Scale)
	case base == nil:
		if isBase(index) || isIndex(index) {
			if e, ok := st.indexOnlyEdit(op, index.Name); ok {
				st.explained = true
				st.a.addFix(CodeAddr16, op.Start, op.End,
					"16-bit addressing has no index without a base; a lone register is written as the base",
					fmt.Sprintf("Use (%%%s)", index.Name), e)
				return
			}
		}
		bad("%s", pairs)
	case isBase(base) && isIndex(index):
	case isIndex(base) && isBase(index):
		msg := fmt.Sprintf("%s: the base comes first; %s", strings.TrimSpace(op.Text), pairs)
		if e, ok := st.swapEdit(op, base.Name, index.Name); ok {
			st.explained = true
			st.a.addFix(CodeAddr16, op.Start, op.End, msg, fmt.Sprintf("Swap to (%%%s,%%%s)", index.Name, base.Name), e)
			return
		}
		bad("%s", msg)
	default:
		bad("%s", pairs)
	}
}

// group locates the "(base,index,scale)" text of a memory operand: the
// offsets of its parentheses in the document.
func (st *stmtCtx) group(op *asm.Operand) (open, closing int, ok bool) {
	src := st.a.prog.Doc.Text
	if op.Start < 0 || op.End > len(src) || op.End <= op.Start || src[op.End-1] != ')' {
		return 0, 0, false
	}
	text := src[op.Start:op.End]
	i := strings.LastIndexByte(text, '(')
	if i < 0 {
		return 0, 0, false
	}
	return op.Start + i, op.End - 1, true
}

// swapEdit rewrites the group as (%index,%base), keeping the scale out:
// both callers only swap with scale 1.
func (st *stmtCtx) swapEdit(op *asm.Operand, base, index string) (edit, bool) {
	open, closing, ok := st.group(op)
	if !ok {
		return edit{}, false
	}
	return edit{start: open, end: closing + 1, text: "(%" + index + ",%" + base + ")"}, true
}

// indexOnlyEdit rewrites "(,%si)" or "(,%si,1)" as "(%si)".
func (st *stmtCtx) indexOnlyEdit(op *asm.Operand, index string) (edit, bool) {
	open, closing, ok := st.group(op)
	if !ok {
		return edit{}, false
	}
	return edit{start: open, end: closing + 1, text: "(%" + index + ")"}, true
}
