// Package encoding holds the rules of roadmap section 1 (encoding and
// operand validity): statements GAS rejects or silently assembles into
// something other than what was written, found without running the
// assembler.
//
// The rules share one pass over the program (memoized per document run):
// each instruction statement is checked by the specific checks first
// (segment registers, fixed registers, REX conflicts, decorations,
// addressing, prefixes) and then matched against the instruction's forms
// from the isa tables. A statement a specific check already explained is
// not reported again by the generic "no form" rule, so one mistake gives
// one finding with the most useful message.
//
// False positives are the enemy here: a statement is only judged when
// every operand was understood (no macro arguments, no inline asm operand
// references, no unknown registers), instructions whose table forms are
// incomplete or irregular (branches, string and x87 instructions) are left
// to GAS, and anything uncertain stays quiet.
package encoding

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Rule codes.
const (
	CodeNoForm        = "operand-no-form"
	CodeSizeMismatch  = "operand-size-mismatch"
	CodeSizeAmbiguous = "operand-size-ambiguous"
	CodeREXHighByte   = "rex-high-byte"
	CodeAddr16        = "addr16-invalid"
	CodeAddrReg       = "addr-invalid-reg"
	CodeSreg          = "sreg-misuse"
	CodeFixedReg      = "fixed-reg-form"
	CodeImmRange      = "imm-range"
	CodeDataRange     = "data-range"
	CodePrefix        = "prefix-misuse"
	CodeAVXDeco       = "avx-decoration"
	CodeBranchRange   = "branch-out-of-range"
)

// edit is a fix's text edit in byte offsets of the program document.
type edit struct {
	start, end int
	text       string
}

// issue is one finding of the shared pass, reported by the rule owning its
// code.
type issue struct {
	code       string
	start, end int
	msg        string
	fixTitle   string
	edits      []edit
}

const memoKey = "encoding/issues"

// issues runs the shared pass once per document run.
func issues(c *lint.Context) []issue {
	if c.Prog == nil || c.Prog.Doc == nil {
		return nil
	}
	v := c.Memo(memoKey, func() any { return analyze(c.Prog, c.Lang(), c.Target.Sources["mode"] != "") })
	is, _ := v.([]issue)
	return is
}

// report emits the shared pass's findings for the running rule's code.
func report(c *lint.Context) {
	code := c.Rule().Code
	for _, is := range issues(c) {
		if is.code != code {
			continue
		}
		var fixes []lint.Fix
		if len(is.edits) > 0 {
			edits := make([]protocol.TextEdit, 0, len(is.edits))
			for _, e := range is.edits {
				edits = append(edits, c.ReplaceRange(e.start, e.end, e.text))
			}
			fixes = append(fixes, lint.NewFix(is.fixTitle, edits...))
		}
		c.ReportRange(is.start, is.end, is.msg, fixes...)
	}
}

// analyze is the shared pass over every statement of the program.
//
// modeSet says the initial mode came from somewhere (a compile flag, the
// configuration) rather than being the 64-bit default.
func analyze(p *asm.Program, lang lint.Lang, modeSet bool) []issue {
	a := &pass{prog: p, lang: lang, explicitFrom: explicitFrom(p, modeSet)}
	x86 := looksX86(p, lang)
	if x86 == notX86 {
		return nil
	}
	for i := range p.Stmts {
		s := &p.Stmts[i]
		switch s.Kind {
		case asm.Instr:
			if s.Mnemonic != "" && s.Inst != nil && !foreignStmt(s) {
				a.instr(i)
			}
		case asm.Directive:
			if x86 == isX86 {
				a.data(s)
			}
		}
	}
	return a.out
}

// x86ness says which parts of a program the pass judges.
type x86ness int

const (
	notX86   x86ness = iota // another architecture: nothing is judged
	codeOnly                // instructions, but not data (no x86 register seen)
	isX86                   // instructions and data
)

// looksX86 decides whether the program reads as AT&T x86. Projects keep
// ARM, MIPS, PowerPC and RISC-V sources next to x86 ones under the same .S
// suffix, and their mnemonics (mov, add, and, str, ret) resolve to x86
// instructions. One operand naming an x86 register ('%eax', '(%rbx)')
// settles it. Without one, a statement that reads as another
// architecture's (foreignStmt) means the file is one; anything else is
// taken as x86 code, but its data directives are judged only in assembly
// files, since .word is 32 bits on ARM and inline asm with no register
// says nothing about the target.
func looksX86(p *asm.Program, lang lint.Lang) x86ness {
	foreign := false
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if s.Kind != asm.Instr {
			continue
		}
		if s.Inst != nil && foreignStmt(s) {
			foreign = true
		}
		for j := range s.Ops {
			switch op := &s.Ops[j]; op.Kind {
			case asm.OpReg:
				if op.Reg != nil {
					return isX86
				}
			case asm.OpMem:
				if _, ok := isa.LookupRegister(op.Base); ok {
					return isX86
				}
				if _, ok := isa.LookupRegister(op.Index); ok {
					return isX86
				}
			}
		}
	}
	switch {
	case foreign:
		return notX86
	case lang == lint.LangAsm:
		return isX86
	}
	return codeOnly
}

// foreignStmt reports a statement that reads as another architecture's
// even in a file with x86 code: files such as libunwind's keep every
// architecture in one source behind #if, and the parse sees them all. An
// operand named like a foreign register (x16, sp, r0) gives it away, and
// so do two or more operands with no x86 register among them, all of them
// immediates or bare symbols ("or $2, $0, $0", "mov x0, sp"), which x86
// has only in enter and far branches.
func foreignStmt(s *asm.Stmt) bool {
	bare := 0
	for j := range s.Ops {
		op := &s.Ops[j]
		switch op.Kind {
		case asm.OpMem:
			if op.Base == "" && op.Index == "" && op.Seg == "" {
				if foreignReg(op.Disp) {
					return true
				}
				bare++
			}
		case asm.OpLabel:
			if foreignReg(op.Symbol) {
				return true
			}
		case asm.OpImm:
			bare++
		}
	}
	return len(s.Ops) >= 2 && bare == len(s.Ops) && s.Inst.Name != "enter"
}

// foreignReg reports a register name of ARM, AArch64 or RISC-V, or of
// Go's Plan 9 assembler (AX, R8, X0), whose .s files are not GAS either.
func foreignReg(s string) bool {
	switch s {
	case "AX", "BX", "CX", "DX", "SI", "DI", "BP", "SP", "SB", "FP", "PC":
		return true
	case "sp", "lr", "pc", "fp", "ip", "ra", "zero", "gp", "tp":
		return true
	}
	if len(s) < 2 || len(s) > 3 {
		return false
	}
	switch s[0] {
	case 'r', 'x', 'w', 'a', 't', 's', 'R', 'X', 'Y', 'Z', 'K':
		for i := 1; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// pass carries the shared pass's state.
type pass struct {
	prog *asm.Program
	lang lint.Lang
	out  []issue
	// explicitFrom is the first statement whose mode is known rather than
	// defaulted: 0 when the target names a mode, else the statement after
	// the first .codeNN directive; len(Stmts) when there is neither.
	explicitFrom int
	opsBuf       []operand // reused by stmtCtx.prepare
}

// explicitFrom finds where the program's mode stops being a default. A
// .S file with no .codeNN directive and no compile flags saying -m32 is
// assembled as 64-bit by default, but is as likely 32-bit code built with
// -m32; checks that depend on the mode stay quiet there.
func explicitFrom(p *asm.Program, modeSet bool) int {
	if modeSet {
		return 0
	}
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if s.Kind == asm.Directive && !s.InMacroDef && strings.HasPrefix(s.Name, ".code") {
			return i + 1
		}
	}
	return len(p.Stmts)
}

func (a *pass) add(code string, start, end int, msg string) *issue {
	a.out = append(a.out, issue{code: code, start: start, end: end, msg: msg})
	return &a.out[len(a.out)-1]
}

func (a *pass) addFix(code string, start, end int, msg, title string, edits ...edit) {
	is := a.add(code, start, end, msg)
	is.fixTitle = title
	is.edits = edits
}
