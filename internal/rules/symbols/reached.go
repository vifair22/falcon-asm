package symbols

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// Some labels are reached without any symbol reference: code placed with
// .org at an offset that a vector, a header or a far pointer built
// elsewhere names by number; an option ROM's entry at offset 3; strings
// a loop walks one after another from the first. Nothing in the sources
// names them, so unused-label would report every one. reachedLabels finds
// them, so the rule can stay quiet without a suppression:
//
//   - a label directly after `.org <constant>` or `. = <constant>` is
//     presumed reachable: placing code at a fixed offset is how such
//     entries are made, and a label there names the offset rather than a
//     jump target;
//   - `falcon-asm: reached=offset|external|sequence` on the label's line,
//     or in the comment lines directly above it, says so explicitly.
//     sequence also covers the labels of the data that follows the
//     annotated label line by line, up to the first line that is not a
//     label or a data directive (a blank line, a comment line, code).

// Reach kinds of the reached= annotation.
const (
	reachedOffset   = "offset"
	reachedExternal = "external"
	reachedSequence = "sequence"
)

// placementSkip are directives that emit nothing and do not move the
// location counter, so a label after them still sits where the .org put
// it.
var placementSkip = map[string]bool{
	".code16": true, ".code32": true, ".code64": true, ".code16gcc": true,
	".globl": true, ".global": true, ".type": true, ".size": true, ".hidden": true,
	".local": true, ".weak": true, ".func": true, ".endfunc": true,
}

// dataDirectives emit bytes without being code; a sequence runs through
// them.
var dataDirectives = map[string]bool{
	".byte": true, ".word": true, ".short": true, ".hword": true, ".value": true, ".int": true,
	".long": true, ".quad": true, ".octa": true, ".2byte": true, ".4byte": true, ".8byte": true,
	".dc.b": true, ".dc.w": true, ".dc.l": true, ".dc.a": true, ".dc.q": true,
	".ascii": true, ".asciz": true, ".string": true, ".string8": true, ".string16": true,
	".fill": true, ".skip": true, ".space": true, ".zero": true,
	".float": true, ".single": true, ".double": true,
}

// reachInfo is what reachedLabels found: the names presumed or declared
// reachable, and the reached= values it could not understand, by label,
// so the report can say why the annotation did not help.
type reachInfo struct {
	reached map[string]bool
	bad     map[string]string
}

func reachedLabels(p *asm.Program, t *table) reachInfo {
	ri := reachInfo{reached: map[string]bool{}, bad: map[string]string{}}
	labelSet := make(map[string]bool, len(t.labels))
	for _, d := range t.labels {
		labelSet[d.name] = true
	}
	for _, d := range t.labels {
		if afterConstOrg(p, d.stmt, labelSet) {
			ri.reached[d.name] = true
		}
	}
	anns := reachedComments(p)
	if len(anns) == 0 {
		return ri
	}
	lineCode, lineExists := lineInfo(p)
	for _, d := range t.labels {
		s := &p.Stmts[d.stmt]
		val, ok := anns[s.Line]
		for l := s.Line - 1; !ok && l >= 0 && l < len(lineExists) && lineExists[l] && !lineCode[l]; l-- {
			val, ok = anns[l]
		}
		if !ok {
			continue
		}
		switch val {
		case reachedOffset, reachedExternal:
			ri.reached[d.name] = true
		case reachedSequence:
			ri.reached[d.name] = true
			for _, name := range sequenceLabels(p, d.stmt) {
				ri.reached[name] = true
			}
		default:
			ri.bad[d.name] = val
		}
	}
	return ri
}

// afterConstOrg reports whether the statement at i (a label's) directly
// follows a location move to a constant: only pure label lines and
// directives that emit nothing may sit between.
func afterConstOrg(p *asm.Program, i int, labels map[string]bool) bool {
	for j := i - 1; j >= 0; j-- {
		s := &p.Stmts[j]
		switch {
		case s.InMacroDef:
			return false
		case s.Kind == asm.Empty || s.Kind == asm.Label:
			continue
		case s.Kind != asm.Directive:
			return false
		case s.Name == ".org" && len(s.Args) > 0:
			return len(s.Labels) == 0 && constantExpr(s.Args[0], labels)
		case s.Name == ".set" && len(s.Args) == 2 && s.Args[0] == ".":
			return len(s.Labels) == 0 && constantExpr(s.Args[1], labels)
		case placementSkip[s.Name] && len(s.Labels) == 0:
			continue
		default:
			return false
		}
	}
	return false
}

// constantExpr reports an expression that names no location: no label of
// the document, no "." and no 1b/1f local reference. Other identifiers
// are taken for constants, since an offset is usually a #define or an
// .equ from a header the linter may not have read.
func constantExpr(expr string, labels map[string]bool) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	for i := 0; i < len(expr); {
		c := expr[i]
		switch {
		case c >= '0' && c <= '9':
			j := i
			for j < len(expr) && symChar(expr[j]) {
				j++
			}
			// 1b / 1f: a local label; 0x1f, 10b are numbers only with a
			// prefix gas reads them by (0x, 0b) or no suffix letter.
			if n := expr[i:j]; len(n) > 1 && (n[len(n)-1] == 'b' || n[len(n)-1] == 'f') && allDigits(n[:len(n)-1]) {
				return false
			}
			i = j
		case symStart(c):
			j := i
			for j < len(expr) && symChar(expr[j]) {
				j++
			}
			if name := expr[i:j]; name == "." || labels[name] {
				return false
			}
			i = j
		default:
			i++
		}
	}
	return true
}

// symStart and symChar are isIdentStart and isIdentChar widened by the
// '.' and '$' GAS allows in symbols, so ".Lend" and "." are one name each.
func symStart(c byte) bool { return isIdentStart(c) || c == '.' || c == '$' }

func symChar(c byte) bool { return symStart(c) || c >= '0' && c <= '9' }

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// reachedComments maps the line of every comment holding a
// `falcon-asm: ... reached=<value>` item to its value (lowercase, letters
// only, so "reached=sequence: walked from m_head" reads as "sequence").
func reachedComments(p *asm.Program) map[int]string {
	var out map[int]string
	for i := range p.Toks {
		tk := &p.Toks[i]
		if tk.Kind != lexer.Comment {
			continue
		}
		lower := strings.ToLower(tk.Text)
		m := strings.Index(lower, "falcon-asm:")
		if m < 0 {
			continue
		}
		k := strings.Index(lower[m:], "reached=")
		if k < 0 {
			continue
		}
		v := lower[m+k+len("reached="):]
		e := 0
		for e < len(v) && v[e] >= 'a' && v[e] <= 'z' {
			e++
		}
		if out == nil {
			out = map[int]string{}
		}
		// The lexer cuts a block comment at each newline, so the marker
		// and the item must share a line, as they must for ignore=.
		out[tk.Line] = v[:e]
	}
	return out
}

// lineInfo says, per line, whether it holds any token and whether it holds
// one that is not a comment.
func lineInfo(p *asm.Program) (code, exists []bool) {
	n := 0
	for i := range p.Toks {
		n = max(n, p.Toks[i].Line+1)
	}
	code = make([]bool, n)
	exists = make([]bool, n)
	for i := range p.Toks {
		tk := &p.Toks[i]
		exists[tk.Line] = true
		if tk.Kind != lexer.Comment {
			code[tk.Line] = true
		}
	}
	return code, exists
}

// sequenceLabels are the labels on the data lines that directly follow
// the statement at i, line after line, until a line that is neither a
// label nor data.
func sequenceLabels(p *asm.Program, i int) []string {
	var out []string
	prevEnd := p.Stmts[i].EndLine
	for k := i + 1; k < len(p.Stmts); k++ {
		s := &p.Stmts[k]
		if s.Line > prevEnd+1 || s.InMacroDef {
			break
		}
		if s.Kind != asm.Label && (s.Kind != asm.Directive || !dataDirectives[s.Name]) {
			break
		}
		out = append(out, s.Labels...)
		prevEnd = s.EndLine
	}
	return out
}
