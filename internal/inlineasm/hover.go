package inlineasm

import (
	"strconv"
	"strings"
)

// HoverAt explains what is under cOff inside s when it is something only
// this package understands: an operand reference in the template, a
// constraint, an operand name, a clobber or a goto label. It returns false
// for everything else (instructions and registers in the template, C
// expressions), which the caller handles with the GAS hover on Asm or leaves
// to the C language server.
func (s *Stmt) HoverAt(cOff int) (string, bool) {
	if a, ok := s.AsmOffset(cOff); ok {
		for _, r := range s.refs {
			if a >= r.Offset && a < r.Offset+r.Len {
				return s.RefHover(r), true
			}
		}
		return "", false
	}
	for _, list := range [][]Operand{s.Outputs, s.Inputs} {
		for k := range list {
			op := &list[k]
			if op.ConstraintSpan != (Span{}) && op.ConstraintSpan.Contains(cOff) {
				return "`\"" + op.Constraint + "\"` " + ConstraintHover(op.Constraint), true
			}
			if op.Name != "" && op.NameSpan.Contains(cOff) {
				return s.operandLine(op), true
			}
		}
	}
	for k, sp := range s.ClobberSpans {
		if sp.Contains(cOff) {
			return ClobberHover(s.Clobbers[k]), true
		}
	}
	for _, l := range s.Labels {
		if l.Span.Contains(cOff) {
			return "goto label " + l.Name + ", referenced as %l" + strconv.Itoa(l.Index) + " or %l[" + l.Name + "]", true
		}
	}
	return "", false
}

// operandLine renders an operand as "operand 1 [x]: input "r" (v) - meaning".
func (s *Stmt) operandLine(op *Operand) string {
	var b strings.Builder
	b.WriteString("operand " + strconv.Itoa(op.Index))
	if op.Name != "" {
		b.WriteString(" [" + op.Name + "]")
	}
	b.WriteString(": `\"" + op.Constraint + "\" (" + op.Expr + ")`")
	if h := ConstraintHover(op.Constraint); h != "" {
		b.WriteString(" - " + h)
	}
	return b.String()
}

// RefHover describes the operand an OperandRef from s points at, with the
// effect of its modifier.
func (s *Stmt) RefHover(r OperandRef) string {
	var line string
	switch op, ok := s.Operand(r.Index); {
	case ok:
		line = s.operandLine(op)
	case r.Index >= s.operandCount() && r.Index-s.operandCount() < len(s.Labels):
		l := s.Labels[r.Index-s.operandCount()]
		line = "goto label " + l.Name + " (operand " + strconv.Itoa(r.Index) + ")"
	case r.Name != "":
		line = "no operand named [" + r.Name + "]"
	default:
		line = "operand " + strconv.Itoa(r.Index) + " does not exist (" + strconv.Itoa(s.operandCount()) + " operands)"
	}
	if r.Modifier != 0 {
		m := ModifierHover(r.Modifier)
		if m == "" {
			m = "unknown modifier"
		}
		line += "\n\nmodifier " + string(r.Modifier) + ": " + m
	}
	return line
}
