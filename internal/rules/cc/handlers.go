package cc

import (
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// Handler discovery (roadmap item 37). A handler is never called, so the
// document shows it only by how its address is used:
//
//   - stored into the real-mode interrupt vector table: a move of $label
//     to an absolute address below 0x400 ("movw $isr, 0x24" installs
//     vector 9);
//   - taken in a function that loads an IDT (lidt): "movl $stubs, %eax"
//     before the gate-building loop, or a table of handler addresses
//     ($table or lea table) whose index is the vector;
//   - declared: `falcon-asm: handler` on the label, with "=errcode",
//     "=noerrcode" or "=service" and "vector=N" to say more.
//
// A push of a label is not counted: pushing a code address and returning
// to it is how mode-switch code far-jumps, and such code loads IDTs too.

// hardwareIVT are the real-mode vectors the processor or an IRQ raises
// (and 1Ch, which the timer interrupt calls): their handlers interrupt
// code that does not expect any register to change. Every other vector
// is a software service whose results come back in registers.
func hardwareIVT(v int) bool {
	return v <= 0x0F || v == 0x1C || v >= 0x70 && v <= 0x77
}

// handlers finds the handlers installed by code; declared ones were
// applied with the conventions.
func (m *model) handlers() {
	p := m.prog
	mark := func(fi int, h handler) {
		fn := &m.funcs[fi]
		if fn.handler != nil {
			if fn.handler.vector < 0 && h.vector >= 0 {
				fn.handler.vector = h.vector
			}
			return
		}
		if fn.explicit {
			// A convention declared on the label says what it is.
			return
		}
		fn.handler = &h
		fn.conv = nil
	}
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.Inst.Name != "mov" || len(st.Ops) != 2 {
			continue
		}
		src, dst := &st.Ops[0], &st.Ops[1]
		if src.Kind != asm.OpImm || dst.Kind != asm.OpMem || dst.Base != "" || dst.Index != "" ||
			!dst.DispVal.Known || dst.DispVal.V < 0 || dst.DispVal.V >= 0x400 || dst.DispVal.V%4 != 0 {
			continue
		}
		if fi, ok := m.funcIn(src.Imm); ok {
			v := int(dst.DispVal.V / 4)
			kind := ""
			if !hardwareIVT(v) {
				kind = "service"
			}
			mark(fi, handler{vector: v, kind: kind, ivt: true, how: "installed in the interrupt vector table"})
		}
	}
	for fi := range m.funcs {
		if !m.loadsIDT(fi) {
			continue
		}
		m.eachStmt(fi, func(i int) bool {
			st := &p.Stmts[i]
			if st.Kind != asm.Instr || st.Inst == nil || len(st.Ops) != 2 {
				return true
			}
			name := ""
			switch {
			case st.Inst.Name == "mov" && st.Ops[0].Kind == asm.OpImm:
				if h, ok := m.funcIn(st.Ops[0].Imm); ok {
					if h != fi {
						mark(h, handler{vector: -1, how: "its address is installed in an IDT"})
					}
					return true
				}
				name = symbolOf(st.Ops[0].Imm)
			case st.Inst.Name == "lea" && st.Ops[0].Kind == asm.OpMem && st.Ops[0].Base == "" && st.Ops[0].Index == "":
				name = symbolOf(st.Ops[0].Disp)
			}
			if name == "" {
				return true
			}
			if h, ok := m.funcAtLabel(name); ok {
				if h != fi {
					mark(h, handler{vector: -1, how: "its address is installed in an IDT"})
				}
				return true
			}
			for v, target := range m.table(name) {
				if h, ok := m.funcAtLabel(target); ok && h != fi {
					mark(h, handler{vector: v, how: "entry " + strconv.Itoa(v) + " of the handler table " + name})
				}
			}
			return true
		})
	}
}

// loadsIDT reports a function that executes lidt.
func (m *model) loadsIDT(fi int) bool {
	found := false
	m.eachStmt(fi, func(i int) bool {
		st := &m.prog.Stmts[i]
		if st.Kind == asm.Instr && st.Inst != nil && st.Inst.Name == "lidt" {
			found = true
			return false
		}
		return true
	})
	return found
}

// table reads a table of addresses at a data label: the symbols of the
// .long/.quad/.word/.int entries from the label on, in order, until the
// next label or anything that is not such a directive. A number in an
// entry keeps its index (a vector with no handler) so the indexes stay
// the vectors.
func (m *model) table(name string) []string {
	p := m.prog
	si, ok := p.Labels[name]
	if !ok {
		return nil
	}
	var out []string
	for i := si; i < len(p.Stmts); i++ {
		st := &p.Stmts[i]
		if i > si && len(st.Labels) > 0 {
			break
		}
		switch {
		case st.Kind == asm.Label || st.Kind == asm.Empty:
			continue
		case st.Kind != asm.Directive:
			return out
		}
		switch st.Name {
		case ".long", ".quad", ".word", ".int", ".4byte", ".8byte", ".2byte":
		default:
			return out
		}
		for _, a := range st.Args {
			out = append(out, symbolOf(a))
		}
	}
	return out
}

// funcIn finds the function an address expression names: a lone label,
// or the one label added rather than subtracted in a relocation
// expression such as RELOC + (filter - _start), which is how code copied
// to another address computes where its handler will be.
func (m *model) funcIn(expr string) (int, bool) {
	found, n := -1, 0
	neg := false
	for i := 0; i < len(expr); {
		c := expr[i]
		if !symStart(c) {
			if c == '-' {
				neg = true
			} else if c != ' ' && c != '\t' && c != '(' {
				neg = false
			}
			i++
			continue
		}
		j := i + 1
		for j < len(expr) && (symStart(expr[j]) || expr[j] >= '0' && expr[j] <= '9') {
			j++
		}
		if fi, ok := m.funcAtLabel(expr[i:j]); ok && !neg {
			found = fi
			n++
		}
		neg = false
		i = j
	}
	return found, n == 1
}

func symStart(c byte) bool { return c == '_' || c == '.' || c|0x20 >= 'a' && c|0x20 <= 'z' }

// symbolOf is the expression text when it is a lone symbol, else "".
func symbolOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && c != '.' && c != '$' && (c < '0' || c > '9') && (c|0x20 < 'a' || c|0x20 > 'z') {
			return ""
		}
	}
	return s
}
