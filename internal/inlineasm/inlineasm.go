// Package inlineasm finds GNU inline assembly in C and C++ source and turns
// each statement into something the GAS tooling can work on: the template as
// the assembler will see it (escapes decoded, adjacent literals joined, %%
// collapsed), an exact mapping from every template byte back to the C byte it
// came from, and the parsed operand, clobber and label lists.
//
// The scanner is a token-level approximation of C, not a parser. It is built
// to be wrong in the safe direction: anything it cannot read with confidence
// (a macro in the template, a half-typed statement) still yields a statement,
// flagged Partial or truncated, rather than a misaligned map.
package inlineasm

import (
	"sort"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Span is a half-open byte range in the C source.
type Span struct {
	Start, End int
}

// Contains reports whether off lies in the span. The end is included so a
// cursor sitting just after the last character still counts, which is how
// editors report a position at the end of a word.
func (s Span) Contains(off int) bool { return off >= s.Start && off <= s.End }

// Operand is one entry of an output or input list: [name] "constraint" (expr).
type Operand struct {
	Name       string // symbolic name without brackets, "" when absent
	Constraint string // unescaped constraint text, adjacent literals joined
	Expr       string // C expression between the parentheses, trimmed
	Index      int    // the number %N refers to

	NameSpan       Span // the identifier inside [ ]; zero when absent
	ConstraintSpan Span // from the first opening quote to past the last closing quote
	ExprSpan       Span // the trimmed expression text
}

// Label is one entry of an asm goto label list.
type Label struct {
	Name  string
	Index int // the number %lN refers to
	Span  Span
}

// Stmt is one asm statement or file-scope asm.
type Stmt struct {
	// Asm is the template as the assembler sees it. For extended asm, %%,
	// %{, %| and %} are collapsed and {att|intel} dialect groups reduced to
	// the AT&T alternative; operand references like %0 and %[x] are kept as
	// written. For basic asm the decoded text is used verbatim, because GCC
	// copies basic asm through untouched and a single % already means a
	// register there.
	Asm string

	// Map converts a byte offset in Asm (0..len(Asm)) to the C source byte
	// it came from. Offsets out of range are clamped. A byte produced by an
	// escape sequence maps to its backslash; len(Asm) maps to the closing
	// quote of the last template literal. The mapping never decreases.
	Map func(asmOff int) (cOff int)

	Outputs, Inputs []Operand
	Clobbers        []string
	ClobberSpans    []Span // parallel to Clobbers, each covering the literal(s) with quotes
	Labels          []Label

	// Range covers the statement from the asm keyword through its closing
	// parenthesis, in LSP coordinates. Start and End are the same span in
	// bytes. An unterminated statement ends where scanning stopped.
	Range      protocol.Range
	Start, End int

	// Template holds the content span (between the quotes) of each literal
	// that contributed to Asm, in order.
	Template []Span

	Basic    bool // no colon at all: basic asm, no operand substitution
	TopLevel bool // outside any braces: file-scope asm
	Volatile bool
	Inline   bool
	Goto     bool

	// Partial is set when something other than a string literal (a macro,
	// a wide literal) appeared in the template or a constraint/clobber, so
	// Asm holds only the literals before it. The operand lists are still
	// parsed.
	Partial bool

	// Unterminated is set when the input ended (or a literal broke) before
	// the statement's closing parenthesis.
	Unterminated bool

	refs []OperandRef
	offs []int // len(Asm)+1 entries
}

// OperandRefs returns the operand references in an extended asm template,
// with offsets into stmt.Asm. Basic asm has none.
func OperandRefs(stmt Stmt) []OperandRef {
	return append([]OperandRef(nil), stmt.refs...)
}

// AsmOffset maps a C byte offset back into Asm. It succeeds only when cOff
// is inside (or at the closing quote of) a literal that contributed to Asm.
// For a byte that produced nothing (a line splice, a dropped dialect
// alternative) the nearest preceding template byte is returned.
func (s *Stmt) AsmOffset(cOff int) (int, bool) {
	in := false
	for _, sp := range s.Template {
		if sp.Contains(cOff) {
			in = true
			break
		}
	}
	if !in || len(s.offs) == 0 {
		return 0, false
	}
	i := sort.SearchInts(s.offs, cOff+1) - 1
	if i < 0 {
		i = 0
	}
	if i > len(s.Asm) {
		i = len(s.Asm)
	}
	return i, true
}

// operandCount is the number of numbered operands, which is where goto label
// numbering starts.
func (s *Stmt) operandCount() int { return len(s.Outputs) + len(s.Inputs) }

// Operand returns operand number i (outputs first, then inputs).
func (s *Stmt) Operand(i int) (*Operand, bool) {
	switch {
	case i < 0:
		return nil, false
	case i < len(s.Outputs):
		return &s.Outputs[i], true
	case i < s.operandCount():
		return &s.Inputs[i-len(s.Outputs)], true
	}
	return nil, false
}

// Find returns the statement whose span contains cOff, or nil.
func Find(stmts []Stmt, cOff int) *Stmt {
	for k := range stmts {
		if cOff >= stmts[k].Start && cOff <= stmts[k].End {
			return &stmts[k]
		}
	}
	return nil
}

// Extract returns every asm statement in csrc, in source order.
func Extract(csrc string) []Stmt {
	toks := lexC(csrc)
	pos := newPosConv(csrc)
	var out []Stmt
	depth := 0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tPunct {
			switch csrc[t.start] {
			case '{':
				depth++
			case '}':
				if depth > 0 {
					depth--
				}
			}
			continue
		}
		if t.kind != tIdent || !isAsmKeyword(t.text(csrc)) || !statementContext(csrc, toks, i, depth) {
			continue
		}
		p := parser{src: csrc, toks: toks, i: i + 1}
		st, ok := p.stmt(t.start)
		if !ok {
			continue
		}
		st.TopLevel = depth == 0
		st.Range = protocol.Range{Start: pos.position(st.Start), End: pos.position(st.End)}
		out = append(out, st)
		// Resume after the statement; nothing inside it can open a brace
		// that matters at statement level.
		i = p.i - 1
	}
	return out
}

func isAsmKeyword(w string) bool {
	return w == "asm" || w == "__asm" || w == "__asm__"
}

// statementContext rejects uses of the keyword that are not asm statements:
// member names (x.asm, p->asm) and asm labels on declarators
// (int x asm("eax"), void f(void) __asm__("g")). An asm label follows an
// identifier, a ']' or, at file scope, a ')'; inside a function a ')' is far
// more likely to be "if (c) asm(...)".
func statementContext(src string, toks []token, i, depth int) bool {
	if i == 0 {
		return true
	}
	prev := toks[i-1]
	switch prev.kind {
	case tPunct:
		switch prev.text(src) {
		case ".", "->", "]":
			return false
		case ")":
			return depth > 0
		}
		return true
	case tIdent:
		switch prev.text(src) {
		case "else", "do", "__extension__", "return", "case", "default":
			return true
		}
		return false
	}
	return false
}

func isQualifier(w string) (vol, inl, gt, ok bool) {
	switch w {
	case "volatile", "__volatile__", "__volatile":
		return true, false, false, true
	case "inline", "__inline__", "__inline":
		return false, true, false, true
	case "goto":
		return false, false, true, true
	}
	return false, false, false, false
}

type parser struct {
	src  string
	toks []token
	i    int
}

func (p *parser) peek() (token, bool) {
	if p.i < len(p.toks) {
		return p.toks[p.i], true
	}
	return token{}, false
}

func (p *parser) isPunct(t token, c byte) bool {
	return t.kind == tPunct && t.end-t.start == 1 && p.src[t.start] == c
}

// stmt parses from just after the asm keyword. ok is false when what follows
// is not an asm statement at all (no parenthesis after the qualifiers), so a
// plain identifier named asm is ignored.
func (p *parser) stmt(start int) (Stmt, bool) {
	st := Stmt{Start: start}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tIdent {
			break
		}
		v, in, g, q := isQualifier(t.text(p.src))
		if !q {
			break
		}
		st.Volatile = st.Volatile || v
		st.Inline = st.Inline || in
		st.Goto = st.Goto || g
		p.i++
	}
	t, ok := p.peek()
	if !ok || !p.isPunct(t, '(') {
		return Stmt{}, false
	}
	p.i++
	st.End = t.end

	var tmpl strBuf
	tmplEnd := t.end
	stopped := false
	section := 0
	for section == 0 {
		t, ok := p.peek()
		if !ok {
			st.Unterminated = true
			break
		}
		switch {
		case p.isPunct(t, ':'):
			section = 1
			p.i++
		case p.isPunct(t, ')'):
			section = -1
			p.i++
			st.End = t.end
		case t.kind == tString && literalOK(t) && !stopped:
			tmpl.addLiteral(p.src, t)
			st.Template = append(st.Template, Span{t.contentStart, t.contentEnd})
			tmplEnd = t.contentEnd
			st.End = t.end
			p.i++
			if t.unterminated {
				st.Unterminated = true
				section = -1
			}
		case p.isPunct(t, ';') || p.isPunct(t, '{') || p.isPunct(t, '}'):
			// Statement boundary before the closing parenthesis: the user
			// is mid-edit. Stop here so the following code is scanned
			// normally.
			st.Unterminated = true
			section = -1
		default:
			stopped = true
			st.Partial = true
			st.End = p.skipItemToken()
		}
	}
	tmpl.offs = append(tmpl.offs, tmplEnd)

	st.Basic = section != 1
	if st.Basic {
		st.Asm, st.offs = string(tmpl.b), tmpl.offs
	} else {
		ex, refs := expand(tmpl)
		st.Asm, st.offs, st.refs = string(ex.b), ex.offs, refs
		p.lists(&st)
	}
	offs := st.offs
	st.Map = func(a int) int {
		if a < 0 {
			a = 0
		}
		if a >= len(offs) {
			a = len(offs) - 1
		}
		return offs[a]
	}
	st.resolveRefs()
	return st, true
}

// literalOK reports whether a string token can be part of a template:
// ordinary and raw literals can, wide and UTF-16/32 ones cannot (GCC rejects
// them), so they are treated like a macro.
func literalOK(t token) bool { return t.prefix == "" || t.prefix == "R" || t.prefix == "u8" }

// skipItemToken steps over one token, or a whole balanced (...) / [...]
// group, and returns the end offset of what it consumed.
func (p *parser) skipItemToken() int {
	t := p.toks[p.i]
	p.i++
	if !p.isPunct(t, '(') && !p.isPunct(t, '[') {
		return t.end
	}
	d := 1
	end := t.end
	for p.i < len(p.toks) && d > 0 {
		u := p.toks[p.i]
		p.i++
		end = u.end
		if p.isPunct(u, '(') || p.isPunct(u, '[') {
			d++
		} else if p.isPunct(u, ')') || p.isPunct(u, ']') {
			d--
		} else if p.isPunct(u, ';') || p.isPunct(u, '{') || p.isPunct(u, '}') {
			// A statement boundary inside an "argument" means the user is
			// mid-edit; stop before it so the next statement is still found.
			p.i--
			return u.start
		}
	}
	return end
}

// item is the token range of one comma-separated list entry.
type item struct{ a, b int }

// lists parses the colon-separated sections after the template.
func (p *parser) lists(st *Stmt) {
	section := 1
	for {
		items, term, end := p.section()
		st.End = end
		for _, it := range items {
			switch section {
			case 1:
				st.Outputs = append(st.Outputs, p.operand(st, it))
			case 2:
				st.Inputs = append(st.Inputs, p.operand(st, it))
			case 3:
				p.clobber(st, it)
			default:
				p.label(st, it)
			}
		}
		switch term {
		case ':':
			section++
			continue
		case ')':
			return
		default:
			st.Unterminated = true
			return
		}
	}
}

// section collects items up to the next top-level ':' or ')', consuming the
// terminator. term is 0 when the input ended or a statement boundary was hit.
func (p *parser) section() (items []item, term byte, end int) {
	a := p.i
	if p.i > 0 {
		end = p.toks[p.i-1].end
	}
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		switch {
		case p.isPunct(t, ',') || p.isPunct(t, ':') || p.isPunct(t, ')'):
			if p.i > a {
				items = append(items, item{a, p.i})
			}
			p.i++
			end = t.end
			if c := p.src[t.start]; c != ',' {
				return items, c, end
			}
			a = p.i
		case p.isPunct(t, ';') || p.isPunct(t, '{') || p.isPunct(t, '}'):
			if p.i > a {
				items = append(items, item{a, p.i})
			}
			return items, 0, end
		default:
			end = p.skipItemToken()
		}
	}
	if p.i > a {
		items = append(items, item{a, p.i})
	}
	return items, 0, end
}

// literals joins the string literals among toks[a:b] that appear before the
// first non-literal, returning the text, the quoted span and whether every
// token was a usable literal.
func (p *parser) literals(a, b int) (string, Span, int, bool) {
	var buf strBuf
	sp := Span{-1, -1}
	k := a
	for ; k < b; k++ {
		t := p.toks[k]
		if t.kind != tString || !literalOK(t) {
			break
		}
		buf.addLiteral(p.src, t)
		if sp.Start < 0 {
			sp.Start = t.start
		}
		sp.End = t.end
	}
	if sp.Start < 0 {
		sp = Span{}
	}
	return string(buf.b), sp, k, k > a
}

func (p *parser) operand(st *Stmt, it item) Operand {
	op := Operand{}
	k := it.a
	if k < it.b && p.isPunct(p.toks[k], '[') {
		// skipItemToken consumed the bracket group as one unit, so look
		// inside for the identifier.
		if k+1 < it.b && p.toks[k+1].kind == tIdent {
			op.Name = p.toks[k+1].text(p.src)
			op.NameSpan = Span{p.toks[k+1].start, p.toks[k+1].end}
		}
		for k < it.b && !p.isPunct(p.toks[k], ']') {
			k++
		}
		k++
	}
	c, sp, next, ok := p.literals(k, it.b)
	op.Constraint, op.ConstraintSpan = c, sp
	if !ok {
		st.Partial = true
	}
	k = next
	for k < it.b && !p.isPunct(p.toks[k], '(') {
		st.Partial = true
		k++
	}
	if k < it.b {
		open := p.toks[k]
		closeTok := k
		d := 0
		for j := k; j < it.b; j++ {
			if p.isPunct(p.toks[j], '(') {
				d++
			} else if p.isPunct(p.toks[j], ')') {
				d--
				if d == 0 {
					closeTok = j
					break
				}
			}
		}
		end := p.toks[it.b-1].end
		if closeTok > k {
			end = p.toks[closeTok].start
		}
		a, z := open.end, end
		for a < z && isSpace(p.src[a]) {
			a++
		}
		for z > a && isSpace(p.src[z-1]) {
			z--
		}
		op.Expr, op.ExprSpan = p.src[a:z], Span{a, z}
	}
	return op
}

func (p *parser) clobber(st *Stmt, it item) {
	c, sp, next, ok := p.literals(it.a, it.b)
	if !ok || next < it.b {
		st.Partial = true
	}
	if ok {
		st.Clobbers = append(st.Clobbers, c)
		st.ClobberSpans = append(st.ClobberSpans, sp)
	}
}

func (p *parser) label(st *Stmt, it item) {
	t := p.toks[it.a]
	if t.kind != tIdent {
		return
	}
	st.Labels = append(st.Labels, Label{Name: t.text(p.src), Span: Span{t.start, t.end}})
}

// resolveRefs numbers operands and labels and resolves %[name] references.
func (s *Stmt) resolveRefs() {
	for k := range s.Outputs {
		s.Outputs[k].Index = k
	}
	for k := range s.Inputs {
		s.Inputs[k].Index = len(s.Outputs) + k
	}
	for k := range s.Labels {
		s.Labels[k].Index = s.operandCount() + k
	}
	for k := range s.refs {
		r := &s.refs[k]
		if r.Name == "" {
			continue
		}
		r.Index = s.lookupName(r.Name)
	}
}

func (s *Stmt) lookupName(name string) int {
	for _, op := range s.Outputs {
		if op.Name == name {
			return op.Index
		}
	}
	for _, op := range s.Inputs {
		if op.Name == name {
			return op.Index
		}
	}
	// asm goto labels are referenced by their C label name: %l[done].
	for _, l := range s.Labels {
		if l.Name == name {
			return l.Index
		}
	}
	return -1
}
