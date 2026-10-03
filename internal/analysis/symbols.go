package analysis

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// labels records the label and assignment definitions that open a
// statement. Labels written inside a .macro body are left out: they exist
// once per expansion, at the expansion site, and are almost always
// numeric locals that would only clutter an outline.
func (a *analyzer) labels(stmt []lexer.Token) {
	if a.macroDepth > 0 {
		return
	}
	for j, t := range stmt {
		switch t.Kind {
		case lexer.LabelDef:
			if j+1 < len(stmt) && stmt[j+1].Kind == lexer.Punct && stmt[j+1].Text == "=" {
				a.addEqu(stmt, j)
				return
			}
			a.addLabel(Label, t)
		case lexer.LocalLabelDef:
			a.addLabel(LocalLabel, t)
		case lexer.Punct, lexer.Prefix:
		default:
			return
		}
	}
}

func (a *analyzer) addLabel(kind SymbolKind, t lexer.Token) {
	a.syms = append(a.syms, Symbol{
		Name:           t.Text,
		Kind:           kind,
		Range:          protocol.Range{Start: a.doc.Position(a.off(t)), End: a.lineEndPos(t)},
		SelectionRange: a.tokRange(t),
		Detail:         a.secName,
	})
}

// addEqu records "name = expr" or ".set name, expr" (stmt[j] is the name).
// The value is kept verbatim as the detail because it is usually the very
// thing a reader of an outline wants (a port number, a selector).
func (a *analyzer) addEqu(stmt []lexer.Token, j int) {
	// The range opens at the .set directive when there is one, so a
	// label earlier on the line stays a symbol of its own.
	start := j
	if j > 0 && stmt[j-1].Kind == lexer.Directive {
		start = j - 1
	}
	detail := ""
	for k := j + 1; k < len(stmt); k++ {
		if stmt[k].Kind == lexer.Punct && (stmt[k].Text == "," || stmt[k].Text == "=") {
			detail = a.stmtText(stmt, k+1)
			break
		}
	}
	a.syms = append(a.syms, Symbol{
		Name:           stmt[j].Text,
		Kind:           Equ,
		Range:          a.rangeOf(stmt[start], stmt[len(stmt)-1]),
		SelectionRange: a.tokRange(stmt[j]),
		Detail:         detail,
	})
}

// stmtText returns the source text from stmt[from] to the end of the
// statement, or "" when from is past it.
func (a *analyzer) stmtText(stmt []lexer.Token, from int) string {
	if from >= len(stmt) {
		return ""
	}
	last := stmt[len(stmt)-1]
	return strings.TrimSpace(a.doc.Text[a.off(stmt[from]) : a.off(last)+last.Len])
}

func (a *analyzer) defineAsmMacro(stmt []lexer.Token, i int) {
	if i+1 >= len(stmt) || stmt[i+1].Kind != lexer.Identifier {
		a.openMac = append(a.openMac, -1)
		return
	}
	name := stmt[i+1]
	a.syms = append(a.syms, Symbol{
		Name:           name.Text,
		Kind:           AsmMacro,
		Range:          a.rangeOf(stmt[i], stmt[len(stmt)-1]),
		SelectionRange: a.tokRange(name),
		Detail:         a.stmtText(stmt, i+2),
	})
	a.openMac = append(a.openMac, len(a.syms)-1)
}

// endAsmMacro stretches the open .macro symbol to its .endm line.
func (a *analyzer) endAsmMacro(d lexer.Token) {
	if len(a.openMac) == 0 {
		return
	}
	idx := a.openMac[len(a.openMac)-1]
	a.openMac = a.openMac[:len(a.openMac)-1]
	if idx >= 0 {
		a.syms[idx].Range.End = a.lineEndPos(d)
	}
}

// openSection starts a section symbol. Its range runs until the next
// section switch so that an outline can nest the labels under it.
func (a *analyzer) openSection(name, dir lexer.Token, stmt []lexer.Token, argFrom int) {
	a.closeSection(dir)
	detail := a.stmtText(stmt, argFrom)
	detail = strings.TrimSpace(strings.TrimPrefix(detail, ","))
	a.syms = append(a.syms, Symbol{
		Name:           name.Text,
		Kind:           Section,
		Range:          protocol.Range{Start: a.doc.Position(a.off(dir)), End: a.lineEndPos(dir)},
		SelectionRange: a.tokRange(name),
		Detail:         detail,
	})
	a.openSec = len(a.syms) - 1
	a.secName = name.Text
}

// closeSection ends the open section at the end of the line before d, or
// leaves it on its own line when d is on that same line.
func (a *analyzer) closeSection(d lexer.Token) {
	if a.openSec < 0 {
		return
	}
	s := &a.syms[a.openSec]
	a.openSec = -1
	if d.Line == 0 || d.Line-1 < s.Range.Start.Line {
		return
	}
	s.Range.End = a.doc.Position(a.nlStarts[d.Line] - 1)
}

func (a *analyzer) cppStatement(stmt []lexer.Token) {
	if cppWord(stmt[0].Text) != "define" || len(stmt) < 2 || stmt[1].Line != stmt[0].Line {
		return
	}
	arg := stmt[1]
	name := leadingIdent(arg.Text)
	if name == "" {
		return
	}
	parts := []string{strings.TrimSpace(arg.Text[len(name):])}
	for _, t := range stmt[2:] {
		parts = append(parts, t.Text)
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[i]), "\\"))
	}
	start := a.off(arg)
	a.syms = append(a.syms, Symbol{
		Name:  name,
		Kind:  Macro,
		Range: a.rangeOf(stmt[0], stmt[len(stmt)-1]),
		SelectionRange: protocol.Range{
			Start: a.doc.Position(start),
			End:   a.doc.Position(start + len(name)),
		},
		Detail: strings.Join(strings.Fields(strings.Join(parts, " ")), " "),
	})
}

// finishSymbols closes whatever is still open at end of file and applies
// .globl, which may legitimately come after the definition it exports.
func (a *analyzer) finishSymbols() []Symbol {
	end := a.doc.Position(len(a.doc.Text))
	if a.openSec >= 0 {
		a.syms[a.openSec].Range.End = end
	}
	for _, idx := range a.openMac {
		if idx >= 0 {
			a.syms[idx].Range.End = end
		}
	}
	for i := range a.syms {
		if k := a.syms[i].Kind; (k == Label || k == Equ) && a.globals[a.syms[i].Name] {
			a.syms[i].Global = true
		}
	}
	return a.syms
}

// cppWord turns "#define" or "# define" into "define".
func cppWord(text string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(text, "#")))
}

// leadingIdent returns the C identifier at the start of s, or "".
func leadingIdent(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '_' || (c|0x20) >= 'a' && (c|0x20) <= 'z' || (i > 0 && c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	return s[:i]
}
