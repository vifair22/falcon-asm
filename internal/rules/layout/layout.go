// Package layout holds the rules of roadmap section 6 (memory layout and
// size): stage and section size budgets (item 47), alignment of descriptor
// tables, page tables and aligned SIMD operands (item 48), and location
// moves or linked sections that overlap (item 49). It also computes the
// budgets the editor shows as code lenses, so the lens and the
// budget-exceeded rule cannot disagree.
//
// Most of these checks need the assembler's listing: offsets are only known
// once gas has laid the file out. Where the listing cannot settle a
// question (a section's final address, alignment that depends on where the
// linker places a section) the rules stay quiet or say so at Information,
// in keeping with the ground rule that unknown state never warns.
package layout

import (
	"path/filepath"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// move is a statement that sets the location counter: `.org expr` or
// `. = expr`. Targets are section-relative in gas, either a constant
// offset or a symbol plus a constant.
type move struct {
	stmt    int
	line    int
	section string
	base    string // symbol the target is relative to; "" = section start
	off     int64  // target relative to base
	known   bool   // base and off were evaluated
	text    string // the statement as written, for messages
}

// moves finds the location moves of p in statement order. A target that
// does not evaluate from the source alone (a macro the parse does not
// know) is read from the listing's preprocessed text of the same line when
// a listing is at hand.
func moves(p *asm.Program, res *toolchain.AsmResult) []move {
	if p == nil || p.Doc == nil {
		return nil
	}
	var src map[int]string
	var out []move
	for i := range p.Stmts {
		st := &p.Stmts[i]
		expr, ok := moveTarget(st)
		if !ok {
			continue
		}
		m := move{stmt: i, line: st.Line, section: st.Section, text: stmtText(p, i)}
		m.base, m.off, m.known = parseRef(expr, macroLookup(p), false)
		if !m.known && res != nil {
			if src == nil {
				src = listingSources(res)
			}
			if e, ok := moveTargetText(src[st.Line]); ok {
				m.base, m.off, m.known = parseRef(e, macroLookup(p), false)
			}
		}
		out = append(out, m)
	}
	return out
}

// moveTarget is the target expression of a location move statement.
func moveTarget(st *asm.Stmt) (string, bool) {
	if st.Kind != asm.Directive || st.InMacroDef {
		return "", false
	}
	switch {
	case st.Name == ".org" && len(st.Args) >= 1:
		return st.Args[0], true
	case st.Name == ".set" && len(st.Args) == 2 && st.Args[0] == ".":
		return st.Args[1], true
	}
	return "", false
}

// moveTargetText extracts the target from a listing source line that holds
// exactly one location move (gas lists the line after cpp, so macros are
// already numbers there).
func moveTargetText(src string) (string, bool) {
	s := strings.TrimSpace(src)
	if i := strings.IndexAny(s, "#;"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	low := strings.ToLower(s)
	if rest, ok := strings.CutPrefix(low, ".org"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		arg := strings.TrimSpace(s[4:])
		if j := topLevelComma(arg); j >= 0 {
			arg = arg[:j]
		}
		return strings.TrimSpace(arg), arg != ""
	}
	if rest, ok := strings.CutPrefix(s, "."); ok {
		rest = strings.TrimLeft(rest, " \t")
		if strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") {
			return strings.TrimSpace(rest[1:]), true
		}
	}
	return "", false
}

func topLevelComma(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func listingSources(res *toolchain.AsmResult) map[int]string {
	m := make(map[int]string, len(res.Listing))
	for _, l := range res.Listing {
		m[l.Line] = l.Source
	}
	return m
}

// stmtText is the statement's source text, trimmed, for messages.
func stmtText(p *asm.Program, i int) string {
	st := &p.Stmts[i]
	if st.Start < 0 || st.End > len(p.Doc.Text) || st.Start > st.End {
		return ""
	}
	s := p.Doc.Text[st.Start:st.End]
	// A label opening the statement is not part of what it does.
	for _, l := range st.Labels {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(s), l); ok {
			s = strings.TrimPrefix(strings.TrimSpace(rest), ":")
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func macroLookup(p *asm.Program) constexpr.Lookup {
	return func(name string) (string, bool) {
		b, ok := p.Macros[name]
		return b, ok
	}
}

// parseRef reads `sym`, `sym + expr`, `sym - expr` or a constant
// expression; with orOK also `sym | expr`, the way page table entries add
// flag bits to a table's address. A leading name that is a known macro is
// part of a constant expression, not a symbol; `.` (the location counter)
// makes the reference relative and therefore unknown here.
func parseRef(expr string, lookup constexpr.Lookup, orOK bool) (base string, off int64, ok bool) {
	s := strings.TrimSpace(expr)
	for len(s) >= 2 && s[0] == '(' && matchingParen(s) == len(s)-1 {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	id := leadingIdent(s)
	if id == "." {
		return "", 0, false
	}
	if id != "" {
		if _, isMacro := lookup(id); !isMacro {
			rest := strings.TrimSpace(s[len(id):])
			if rest == "" {
				return id, 0, true
			}
			op := rest[0]
			if op != '+' && op != '-' && !(orOK && op == '|') {
				return "", 0, false
			}
			v, err := constexpr.Eval(rest[1:], lookup)
			if err != nil {
				return "", 0, false
			}
			if op == '-' {
				v = -v
			}
			return id, v, true
		}
	}
	v, err := constexpr.Eval(s, lookup)
	if err != nil {
		return "", 0, false
	}
	return "", v, true
}

func matchingParen(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// leadingIdent is the symbol name s starts with ("" when it starts with a
// digit or punctuation). GAS names may contain '.', '$' and '_'.
func leadingIdent(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		ok := c == '_' || c == '.' || c == '$' || (c|0x20) >= 'a' && (c|0x20) <= 'z' || i > 0 && c >= '0' && c <= '9'
		if !ok {
			break
		}
		i++
	}
	return s[:i]
}

// labelOffset is where label name lies in its section, from the listing:
// the offset of the first line at or after the label that emits into the
// same section (lines between emit nothing, so the label shares that
// offset), else the section's size when the label ends the section.
func labelOffset(p *asm.Program, res *toolchain.AsmResult, name string) (sec string, off uint64, line int, ok bool) {
	if res == nil {
		return "", 0, 0, false
	}
	idx, found := p.Labels[name]
	if !found {
		return "", 0, 0, false
	}
	st := &p.Stmts[idx]
	sec, line = st.Section, st.Line
	// The listing is in source order in practice, but nothing promises
	// it, so the nearest emitting line is searched for rather than
	// assumed to be the next one.
	best := -1
	for i := range res.Listing {
		l := &res.Listing[i]
		if l.Unknown {
			return "", 0, 0, false
		}
		if l.Section == sec && l.HasOffset && l.Line >= line && (best < 0 || l.Line < res.Listing[best].Line) {
			best = i
		}
	}
	if best >= 0 {
		return sec, res.Listing[best].Offset, line, true
	}
	if sz, ok := res.SectionSizes[sec]; ok {
		return sec, sz, line, true
	}
	return "", 0, 0, false
}

// sectionAligns is the largest alignment each section asks for in this
// file (.align, .balign, .p2align), which gas makes the section's own
// alignment. Directives in included files are not seen, so this is a
// lower bound: it can only make a check quieter.
func sectionAligns(p *asm.Program) map[string]int64 {
	out := map[string]int64{}
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Directive || st.InMacroDef || len(st.Args) == 0 {
			continue
		}
		var a int64
		switch st.Name {
		case ".align", ".balign", ".balignw", ".balignl":
			v, err := constexpr.Eval(st.Args[0], macroLookup(p))
			if err != nil || v <= 0 {
				continue
			}
			a = v
		case ".p2align", ".p2alignw", ".p2alignl":
			v, err := constexpr.Eval(st.Args[0], macroLookup(p))
			if err != nil || v < 0 || v > 30 {
				continue
			}
			a = 1 << v
		default:
			continue
		}
		if a > out[st.Section] {
			out[st.Section] = a
		}
	}
	return out
}

func sectionAlign(c *lint.Context, sec string) int64 {
	m, _ := c.Memo("layout/aligns", func() any { return sectionAligns(c.Prog) }).(map[string]int64)
	if a := m[sec]; a > 0 {
		return a
	}
	return 1
}

// docPath is the file the program came from, for looking up gas
// diagnostics (keyed by path) and linker scripts beside it.
func docPath(p *asm.Program) (string, bool) {
	if p == nil || p.Doc == nil {
		return "", false
	}
	path, ok := toolchain.URIToPath(p.Doc.URI)
	if !ok {
		return "", false
	}
	return filepath.Clean(path), true
}

// listedBytes is a lower bound on what lines (from, to) of sec emit: the
// bytes gas listed for them. Long fills are truncated in a listing, so it
// can only undercount, which is the safe direction for "at least N bytes".
func listedBytes(res *toolchain.AsmResult, sec string, from, to int) int64 {
	var n int64
	for _, l := range res.Listing {
		if l.Line > from && l.Line < to && l.Section == sec {
			n += int64(len(l.Bytes))
		}
	}
	return n
}

// emitting reports whether a statement places bytes: an instruction or a
// data directive. Macro calls and unknown words are not counted, since
// what they emit is unknown.
func emitting(st *asm.Stmt) bool {
	switch st.Kind {
	case asm.Instr:
		return true
	case asm.Directive:
		return dataSizes[st.Name] != 0 || fillDirectives[st.Name]
	}
	return false
}

// dataSizes are the data directives and the size of one item.
var dataSizes = map[string]int{
	".byte": 1, ".word": 2, ".short": 2, ".hword": 2, ".value": 2, ".2byte": 2,
	".long": 4, ".int": 4, ".4byte": 4, ".quad": 8, ".8byte": 8, ".octa": 16,
}

var fillDirectives = map[string]bool{
	".ascii": true, ".asciz": true, ".string": true, ".fill": true, ".skip": true,
	".space": true, ".zero": true, ".incbin": true, ".float": true, ".single": true, ".double": true,
}
