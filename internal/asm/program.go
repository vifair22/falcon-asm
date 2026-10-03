package asm

import (
	"sort"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Range converts byte offsets into Doc.Text to an LSP range (UTF-16
// columns), the form diagnostics are published in.
func (p *Program) Range(start, end int) protocol.Range {
	return protocol.Range{Start: p.Doc.Position(start), End: p.Doc.Position(end)}
}

// TokOffset is the byte offset in Doc.Text of Toks[i].
func (p *Program) TokOffset(i int) int {
	t := &p.Toks[i]
	if t.Line < len(p.nl) {
		return p.nl[t.Line] + t.Col
	}
	return len(p.Doc.Text)
}

// StmtAt returns the indexes of the statements that touch a zero-based
// line: several when ';' separates them, one spanning several lines for a
// cpp directive with continuations.
func (p *Program) StmtAt(line int) []int {
	// Statements are in source order and never overlap, so the ones
	// touching line are a contiguous run ending before the first one
	// that starts below it.
	hi := sort.Search(len(p.Stmts), func(i int) bool { return p.Stmts[i].Line > line })
	lo := hi
	for lo > 0 && p.Stmts[lo-1].EndLine >= line {
		lo--
	}
	if lo == hi {
		return nil
	}
	out := make([]int, 0, hi-lo)
	for i := lo; i < hi; i++ {
		out = append(out, i)
	}
	return out
}

// LocalTarget resolves a local label reference ("1f", "9903b") made by
// statement stmtIdx to the statement defining that label: the nearest
// "N:" after the reference for f, the nearest at or before it for b (a
// label opening the statement precedes its operands). This is the token
// order rule of symbols.ResolveLocal, with two refinements that keep a
// control-flow graph from inventing edges:
//
//   - a .macro body is not code where it is written: a reference outside
//     one never lands inside one, and a reference inside one never leaves
//     it (where it lands depends on the call site);
//   - a call to an in-file .macro whose body defines N: defines it again at
//     the call site, so a search that would step over such a call gives up
//     rather than skip the closer definition.
func (p *Program) LocalTarget(stmtIdx int, ref string) (int, bool) {
	i, st := p.ResolveLocal(stmtIdx, ref)
	return i, st == LocalFound
}

// LocalStatus is how a local label reference resolved.
type LocalStatus int

// Local label resolution outcomes. LocalUnknown is not an error: the search
// left a .macro body or stepped over a call to a macro that defines the
// label, so where the reference lands depends on an expansion.
const (
	LocalFound   LocalStatus = iota
	LocalMissing             // there is no such label in the search direction
	LocalUnknown             // the answer depends on a macro expansion
	LocalInvalid             // ref is not a local label reference
)

// ResolveLocal is LocalTarget that also says why a reference did not
// resolve, so a rule can report a label that is truly missing and stay
// quiet about one that only an expansion could define.
func (p *Program) ResolveLocal(stmtIdx int, ref string) (int, LocalStatus) {
	if stmtIdx < 0 || stmtIdx >= len(p.Stmts) || len(ref) < 2 {
		return -1, LocalInvalid
	}
	name, dir := ref[:len(ref)-1], ref[len(ref)-1]
	for i := 0; i < len(name); i++ {
		if !isDigit(name[i]) {
			return -1, LocalInvalid
		}
	}
	inMac := p.Stmts[stmtIdx].InMacroDef
	step, i := 1, stmtIdx+1
	switch dir {
	case 'f':
	case 'b':
		step, i = -1, stmtIdx
	default:
		return -1, LocalInvalid
	}
	for ; i >= 0 && i < len(p.Stmts); i += step {
		s := &p.Stmts[i]
		if s.InMacroDef != inMac {
			if inMac {
				return -1, LocalUnknown
			}
			continue
		}
		if s.Kind == MacroCall && p.macroDefines(s.Mnemonic, name) {
			// Backward from a call site, the call itself comes after
			// the reference's own statement only when i != stmtIdx.
			if i != stmtIdx || step > 0 {
				return -1, LocalUnknown
			}
		}
		for _, l := range s.Labels {
			if l == name {
				return i, LocalFound
			}
		}
	}
	if inMac {
		// A body that does not define the label refers to one at or
		// around its call sites.
		return -1, LocalUnknown
	}
	return -1, LocalMissing
}

func (p *Program) macroDefines(macro, label string) bool {
	for _, l := range p.macLocals[macro] {
		if l == label {
			return true
		}
	}
	return false
}

// LabelTarget resolves an OpLabel operand of statement stmtIdx to the
// statement defining it in this document: local references through
// LocalTarget, anything else through Labels. ok is false for a symbol
// defined elsewhere or an expression.
func (p *Program) LabelTarget(stmtIdx int, op *Operand) (int, bool) {
	if op.Kind != OpLabel || op.Symbol == "" {
		return -1, false
	}
	if isLocalRef(op.Symbol) {
		return p.LocalTarget(stmtIdx, op.Symbol)
	}
	i, ok := p.Labels[op.Symbol]
	if !ok {
		return -1, false
	}
	return i, true
}

func isLocalRef(s string) bool {
	if len(s) < 2 || (s[len(s)-1] != 'f' && s[len(s)-1] != 'b') {
		return false
	}
	for i := 0; i < len(s)-1; i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}
