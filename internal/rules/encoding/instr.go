package encoding

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// stmtCtx is one instruction statement under analysis.
type stmtCtx struct {
	a        *pass
	idx      int
	s        *asm.Stmt
	inst     *isa.Instruction
	name     string // canonical instruction name
	prefixes []prefixTok
	// suffix is the size suffix in effect: the parsed one, or the size
	// letter of an alias such as inb.
	suffix string
	// explained is set once a specific check reported the statement, so
	// the generic form rules do not report it a second time.
	explained bool
	m         matchCtx
}

// prefixTok is one prefix word applying to the statement, with its place
// in the document.
type prefixTok struct {
	word       string
	start, end int
}

func (a *pass) instr(i int) {
	s := &a.prog.Stmts[i]
	st := &stmtCtx{a: a, idx: i, s: s, inst: s.Inst, name: s.Inst.Name, suffix: s.Suffix}
	st.resolveMovq()
	if st.suffix == "" {
		// An alias that is the name plus a size letter (inb, outw)
		// carries the size the way a suffix does.
		if m := s.Mnemonic; len(m) == len(st.name)+1 && strings.HasPrefix(m, st.name) && suffixWidth(m[len(m)-1:]) != 0 {
			st.suffix = m[len(m)-1:]
		}
	}
	st.prefixes = a.prefixesOf(i)
	st.checkAddresses()
	ok := st.prepare()
	st.checkPrefixes()
	if !ok {
		return
	}
	st.checkSreg()
	st.checkFixed()
	st.checkREX()
	st.checkDeco()
	st.checkForms()
}

// resolveMovq picks between the two instructions GAS spells movq: the
// SSE/MMX quadword move (which the tables list under that name) and mov
// with a q suffix. It is the SSE one only when an operand is an xmm or mm
// register, which is how GAS tells them apart.
func (st *stmtCtx) resolveMovq() {
	if st.name != "movq" || st.suffix != "" {
		return
	}
	for j := range st.s.Ops {
		if r := st.s.Ops[j].Reg; st.s.Ops[j].Kind == asm.OpReg && r != nil && (r.Class == "xmm" || r.Class == "mmx") {
			return
		}
	}
	if mov, _, ok := isa.Lookup("mov"); ok {
		st.inst, st.name, st.suffix = mov, "mov", "q"
	}
}

// prefixesOf collects the prefixes of statement i: its own and those of the
// prefix-only statements right before it ("rep; movsb", or "lock" alone on
// the line above), which GAS applies to the next instruction. A label in
// between ends the chain: a jump to it skips the prefix bytes.
func (a *pass) prefixesOf(i int) []prefixTok {
	var out []prefixTok
	s := &a.prog.Stmts[i]
	out = a.prefixTokens(s, out)
	if len(s.Labels) > 0 {
		return out
	}
	for j := i - 1; j >= 0; j-- {
		p := &a.prog.Stmts[j]
		if p.Kind != asm.Instr || p.Mnemonic != "" || len(p.Prefixes) == 0 || p.InMacroDef != s.InMacroDef {
			break
		}
		// Earlier statements' prefixes come first.
		out = append(a.prefixTokens(p, nil), out...)
		if len(p.Labels) > 0 {
			break
		}
	}
	return out
}

// prefixTokens appends the statement's prefix words with their offsets.
func (a *pass) prefixTokens(s *asm.Stmt, out []prefixTok) []prefixTok {
	k := 0
	for t := range s.Toks {
		if k >= len(s.Prefixes) {
			break
		}
		tok := &s.Toks[t]
		if tok.Kind == lexer.Comment || !strings.EqualFold(tok.Text, s.Prefixes[k]) {
			continue
		}
		start := a.tokOffset(s, t)
		out = append(out, prefixTok{word: s.Prefixes[k], start: start, end: start + len(tok.Text)})
		k++
	}
	return out
}

// maybeMacro reports a bare operand that may be a cpp macro standing for
// a register or a whole operand: a name the program defines as a macro, or
// any all-caps name (the macro convention), since a macro from an included
// header is not visible here.
func (a *pass) maybeMacro(disp string) bool {
	if disp == "" {
		return false
	}
	if _, ok := a.prog.Macros[disp]; ok {
		return true
	}
	upper := false
	for i := 0; i < len(disp); i++ {
		c := disp[i]
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c == '_' || (c >= '0' && c <= '9'):
		default:
			return false
		}
	}
	return upper
}

// tokOffset is the byte offset of s.Toks[t]. A statement's tokens are a
// sub-slice of the program's, so the index in the program follows from
// the capacities; the pointer comparison guards a program built some
// other way, which falls back to the statement's start.
func (a *pass) tokOffset(s *asm.Stmt, t int) int {
	i := cap(a.prog.Toks) - cap(s.Toks) + t
	if i < 0 || i >= len(a.prog.Toks) || &a.prog.Toks[i] != &s.Toks[t] {
		return s.Start
	}
	return a.prog.TokOffset(i)
}

// prepare reduces the operands for matching. false means some operand is
// not understood well enough to judge: a macro argument, an inline asm
// operand reference, an unknown register, an indirect branch target.
func (st *stmtCtx) prepare() bool {
	s := st.s
	// The buffer is reused across statements: the operands are only
	// needed while this statement is checked.
	if cap(st.a.opsBuf) < len(s.Ops) {
		st.a.opsBuf = make([]operand, len(s.Ops)+4)
	}
	ops := st.a.opsBuf[:len(s.Ops)]
	for j := range s.Ops {
		op := &s.Ops[j]
		o := operand{op: op, kind: op.Kind}
		if op.Indirect {
			return false
		}
		switch op.Kind {
		case asm.OpReg:
			if op.Reg == nil {
				return false
			}
			// In an inline asm template %k0 is either the mask
			// register or operand 0 with the k modifier; the two
			// cannot be told apart once %% has collapsed.
			if st.a.lang == lint.LangInline && op.Reg.Class == "mask" {
				return false
			}
			if op.Reg.Modes == "64" && st.mode() != asm.Mode64 {
				// The mode-register rule reports %rax in 32-bit
				// code; a size finding on top would be the same
				// mistake twice, with a conflicting fix.
				return false
			}
			o.reg = op.Reg
		case asm.OpMem:
			if (op.Seg != "" && !isSegName(op.Seg)) || strings.Contains(op.Disp, "%") {
				// "%1" in an inline asm template parses as a
				// memory operand with that displacement.
				return false
			}
			if op.Base == "" && op.Index == "" && st.a.maybeMacro(op.Disp) {
				// "TMP" may be a cpp macro naming a register,
				// defined here or in a header the parse never saw.
				return false
			}
			if op.Base != "" {
				if _, ok := isa.LookupRegister(op.Base); !ok {
					return false
				}
			}
			if op.Index != "" {
				r, ok := isa.LookupRegister(op.Index)
				if !ok {
					return false
				}
				if w, vec := vecClass[r.Class]; vec {
					o.vsib = w
				}
			}
			o.dxMem = op.Base == "dx" && op.Index == "" && op.Disp == "" && op.Seg == ""
		case asm.OpImm:
		default:
			return false
		}
		if op.Mask != "" && !isMaskReg(op.Mask) {
			return false
		}
		ops[j] = o
	}
	st.m = matchCtx{ops: ops}
	if !isX87(st.inst) {
		st.m.suffix = suffixWidth(st.suffix)
	}
	return true
}

func isX87(inst *isa.Instruction) bool {
	return strings.HasPrefix(inst.Category, "X87") || inst.Category == "FCMOV"
}

// skipForms reports instructions the form rules leave to GAS: branches
// (labels, far pointers and the optional '*' do not fit the patterns),
// string instructions (their operands are documentation) and x87 (the
// suffixes name formats and the register forms are irregular).
func skipForms(inst *isa.Instruction) bool {
	switch inst.Category {
	case "COND_BR", "UNCOND_BR", "CALL", "STRINGOP", "IOSTRINGOP":
		return true
	}
	return isX87(inst)
}

// opRange is the byte range of operand j.
func (st *stmtCtx) opRange(j int) (int, int) {
	op := &st.s.Ops[j]
	return op.Start, op.End
}

// mnemRange is the byte range of the mnemonic.
func (st *stmtCtx) mnemRange() (int, int) {
	if st.s.MnemStart < 0 {
		return st.s.Start, st.s.End
	}
	return st.s.MnemStart, st.s.MnemStart + len(st.s.Mnemonic)
}

func (st *stmtCtx) reportOp(code string, j int, msg string) *issue {
	start, end := st.opRange(j)
	st.explained = true
	return st.a.add(code, start, end, msg)
}

func (st *stmtCtx) reportMnem(code string, msg string) *issue {
	start, end := st.mnemRange()
	st.explained = true
	return st.a.add(code, start, end, msg)
}

// suffixEdit is an edit changing the mnemonic's suffix to suf.
func (st *stmtCtx) suffixEdit(suf string) edit {
	_, end := st.mnemRange()
	return edit{start: end - 1, end: end, text: suf}
}

// nameEdit replaces a register name inside operand j (after its '%'), the
// only kind of edit that survives into the C source of inline asm.
func (st *stmtCtx) nameEdit(j int, name string) (edit, bool) {
	op := &st.s.Ops[j]
	src := st.a.prog.Doc.Text
	if op.Start < 0 || op.End > len(src) {
		return edit{}, false
	}
	text := src[op.Start:op.End]
	at := strings.Index(strings.ToLower(text), "%"+op.RegName)
	if op.Kind != asm.OpReg || at < 0 {
		return edit{}, false
	}
	start := op.Start + at + 1
	return edit{start: start, end: start + len(op.RegName), text: name}, true
}

func (st *stmtCtx) mode() asm.Mode { return st.s.Mode }

// apx reports whether the APX forms apply: in 64-bit code, or in a .macro
// body, which may be expanded in 64-bit code.
func (st *stmtCtx) apx() bool { return st.mode() == asm.Mode64 || !st.modeKnown() }

// modeKnown reports whether the statement's mode is the one it assembles
// in: not a .macro body (assembled wherever it is invoked), and not the
// 64-bit default of a file nothing has given a mode.
func (st *stmtCtx) modeKnown() bool { return !st.s.InMacroDef && st.idx >= st.a.explicitFrom }

func regText(r *isa.Register) string { return "%" + r.Name }

func (st *stmtCtx) mnemonic() string { return st.s.Mnemonic }

// hex formats a value the way the messages quote immediates.
func hex(v int64) string {
	if v < 0 {
		return fmt.Sprintf("-0x%x", uint64(-v))
	}
	return fmt.Sprintf("0x%x", v)
}
