package asm

import (
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// MacroParam is one parameter of a gas .macro definition.
type MacroParam struct {
	Name     string
	Default  string // the value when the invocation gives none
	Required bool   // name:req
	Vararg   bool   // name:vararg, takes the rest of the line
}

// MacroDef is a gas .macro defined in a document. Body is the source text
// between the .macro line and its .endm, as written, so an expansion is the
// text gas would assemble at the call site.
type MacroDef struct {
	Name   string // as written
	Params []MacroParam
	Body   string
	Stmt   int // index of the .macro statement
	End    int // index of the matching .endm statement; -1 when unterminated
}

// Expansion is the text one macro invocation expands to.
type Expansion struct {
	Stmt  int    // index of the invoking statement
	Macro string // the macro's name as written in its definition
	Text  string // expanded body, nested in-file invocations expanded too
	// Err says why the expansion is incomplete or not what gas would
	// produce (a missing required argument, the depth or size cap); "" when
	// it is faithful.
	Err string
}

// Expansion limits. The depth cap is what stops a recursive macro (gas
// allows them, guarded by .if) from expanding forever; the line cap bounds
// a .rept-free but explosive nesting.
const (
	maxExpandDepth = 16
	maxExpandLines = 4000
)

// MacroDefs returns the document's .macro definitions by lowercase name
// (gas macro names are case-insensitive). The first definition of a name
// wins, since gas rejects a second one without .purgem in between.
func (p *Program) MacroDefs() map[string]*MacroDef {
	var out map[string]*MacroDef
	for i := 0; i < len(p.Stmts); i++ {
		s := &p.Stmts[i]
		if s.Kind != Directive || s.Name != ".macro" || s.InMacroDef {
			continue
		}
		d := p.macroDef(i)
		if d == nil {
			continue
		}
		if out == nil {
			out = map[string]*MacroDef{}
		}
		key := strings.ToLower(d.Name)
		if _, dup := out[key]; !dup {
			out[key] = d
		}
		if d.End > i {
			i = d.End
		}
	}
	return out
}

// macroDef reads the definition opened by statement i.
func (p *Program) macroDef(i int) *MacroDef {
	s := &p.Stmts[i]
	src := p.Doc.Text
	// The header is everything after the .macro word; Args would have
	// split "name a, b" at the comma and lost the name/parameter boundary.
	hdrStart := s.Start
	if k := strings.Index(strings.ToLower(src[hdrStart:s.End]), ".macro"); k >= 0 {
		hdrStart += k + len(".macro")
	}
	name, params := parseMacroHeader(src[hdrStart:s.End])
	if name == "" {
		return nil
	}
	d := &MacroDef{Name: name, Params: params, Stmt: i, End: -1}
	depth := 0
	for j := i + 1; j < len(p.Stmts); j++ {
		t := &p.Stmts[j]
		if t.Kind != Directive {
			continue
		}
		switch t.Name {
		case ".macro":
			depth++
		case ".endm", ".endmacro":
			if depth > 0 {
				depth--
				continue
			}
			d.End = j
			d.Body = src[p.lineEnd(s.EndLine):p.lineStart(t.Line)]
			return d
		}
	}
	d.Body = src[p.lineEnd(s.EndLine):]
	return d
}

// lineStart is the byte offset of lexer line n; lineEnd the offset just
// past its line break (the start of line n+1), or the end of the text.
func (p *Program) lineStart(n int) int {
	if n < len(p.nl) {
		return p.nl[n]
	}
	return len(p.Doc.Text)
}

func (p *Program) lineEnd(n int) int { return p.lineStart(n + 1) }

// parseMacroHeader reads ".macro" operands: the name, then parameters
// separated by commas or blanks, each `name`, `name=default`, `name:req` or
// `name:vararg`.
func parseMacroHeader(h string) (string, []MacroParam) {
	h = strings.TrimSpace(stripLineComment(h))
	n := 0
	for n < len(h) && isSymChar(h[n]) {
		n++
	}
	name := h[:n]
	rest := strings.TrimLeft(h[n:], " \t,")
	var params []MacroParam
	for _, piece := range splitMacroArgs(scrub(rest)) {
		mp := MacroParam{Name: piece}
		if k := strings.IndexByte(piece, '='); k >= 0 {
			mp.Name, mp.Default = piece[:k], unquote(piece[k+1:])
		}
		if base, q, ok := strings.Cut(mp.Name, ":"); ok {
			mp.Name = base
			switch strings.ToLower(q) {
			case "req":
				mp.Required = true
			case "vararg":
				mp.Vararg = true
			}
		}
		if mp.Name != "" {
			params = append(params, mp)
		}
	}
	return name, params
}

// stripLineComment drops a trailing # or // comment from a header; a block
// comment is a token the statement's End already excludes.
func stripLineComment(s string) string {
	inStr := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '#', c == '/' && i+1 < len(s) && s[i+1] == '/':
			return s[:i]
		}
	}
	return s
}

func isSymChar(c byte) bool {
	return c == '_' || c == '.' || c == '$' || isDigit(c) || isLetter(c)
}

// scrub squeezes blanks the way gas's input scrubber does before a macro
// sees its operands: runs of blanks become one, and blanks next to an
// operator or a comma go, so "1 + 2" is one argument and "a , b" two.
// Quoted text is left alone.
func scrub(s string) string {
	var b strings.Builder
	inStr := false
	pendingSpace := false
	var prev byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == '"' {
				inStr = false
			}
			prev = c
			continue
		}
		if c == ' ' || c == '\t' {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace && !isScrubOp(prev) && !isScrubOp(c) {
			b.WriteByte(' ')
		}
		pendingSpace = false
		if c == '"' {
			inStr = true
		}
		b.WriteByte(c)
		prev = c
	}
	return b.String()
}

func isScrubOp(c byte) bool {
	return strings.IndexByte("+-*/%&|^<>=!~,()", c) >= 0
}

// splitMacroArgs splits scrubbed operands at top-level commas and blanks.
func splitMacroArgs(s string) []string {
	var out []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case (c == ',' || c == ' ') && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) || len(out) > 0 {
		out = append(out, s[start:])
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// bind matches invocation operands to parameters: keyword arguments
// (name=value) by name, the rest by position, a vararg parameter taking the
// remainder as written. err names a missing required argument.
func (d *MacroDef) bind(operands string) (map[string]string, string) {
	vals := make(map[string]string, len(d.Params))
	set := make(map[string]bool, len(d.Params))
	sc := scrub(strings.TrimSpace(operands))
	args := splitMacroArgs(sc)
	pos := 0
	off := 0 // byte offset of args[k] in sc
	for k, a := range args {
		if k > 0 {
			off += len(args[k-1]) + 1
		}
		if name, v, ok := strings.Cut(a, "="); ok && d.param(name) >= 0 {
			vals[d.Params[d.param(name)].Name] = unquote(v)
			set[name] = true
			continue
		}
		for pos < len(d.Params) && set[d.Params[pos].Name] {
			pos++
		}
		if pos >= len(d.Params) {
			break
		}
		mp := d.Params[pos]
		if mp.Vararg {
			vals[mp.Name] = sc[off:]
			set[mp.Name] = true
			break
		}
		vals[mp.Name] = unquote(a)
		set[mp.Name] = true
		pos++
	}
	err := ""
	for _, mp := range d.Params {
		if set[mp.Name] && vals[mp.Name] != "" {
			continue
		}
		if mp.Required && err == "" {
			err = "missing required argument " + mp.Name
		}
		if !set[mp.Name] || vals[mp.Name] == "" {
			vals[mp.Name] = mp.Default
		}
	}
	return vals, err
}

func (d *MacroDef) param(name string) int {
	for i, mp := range d.Params {
		if mp.Name == name {
			return i
		}
	}
	return -1
}

// substitute replaces \param, \@ and \() in the body. Unknown \x sequences
// are left for gas (or a nested macro) to interpret.
func substitute(body string, vals map[string]string, counter int) string {
	if !strings.Contains(body, "\\") {
		return body
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		switch n := body[i+1]; {
		case n == '@':
			b.WriteString(strconv.Itoa(counter))
			i++
		case n == '(' && i+2 < len(body) && body[i+2] == ')':
			i += 2
		case isSymChar(n):
			j := i + 1
			for j < len(body) && isSymChar(body[j]) {
				j++
			}
			if v, ok := vals[body[i+1:j]]; ok {
				b.WriteString(v)
				i = j - 1
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Expander expands invocations of a document's gas macros, keeping gas's
// \@ counter across them: gas numbers every macro execution in assembly
// order, so the value an invocation sees depends on everything expanded
// before it.
type Expander struct {
	Defs    map[string]*MacroDef // by lowercase name
	Counter int                  // the next \@ value
}

// NewExpander returns an expander over the program's own definitions.
func (p *Program) NewExpander() *Expander {
	return &Expander{Defs: p.MacroDefs()}
}

// ExpandMacros expands every invocation of an in-file macro outside macro
// bodies, in order, at most limit of them (limit <= 0 means no limit).
// Invocations inside a .macro body are expanded where that macro is.
func (p *Program) ExpandMacros(limit int) []Expansion {
	x := p.NewExpander()
	if len(x.Defs) == 0 {
		return nil
	}
	var out []Expansion
	for i := range p.Stmts {
		if limit > 0 && len(out) >= limit {
			break
		}
		if e, ok := x.ExpandStmt(p, i); ok {
			out = append(out, e)
		}
	}
	return out
}

// ExpandStmt expands statement i when it invokes one of the expander's
// macros (outside a macro body), advancing the counter.
func (x *Expander) ExpandStmt(p *Program, i int) (Expansion, bool) {
	if i < 0 || i >= len(p.Stmts) {
		return Expansion{}, false
	}
	s := &p.Stmts[i]
	if s.Kind != MacroCall || s.InMacroDef || s.MnemStart < 0 {
		return Expansion{}, false
	}
	d, ok := x.Defs[s.Mnemonic]
	if !ok {
		return Expansion{}, false
	}
	ops := p.operandText(i)
	lines := 0
	text, err := x.expand(d, ops, 0, &lines)
	return Expansion{Stmt: i, Macro: d.Name, Text: text, Err: err}, true
}

// operandText is the source of statement i after its mnemonic, with
// comments dropped (a block comment is one blank to gas).
func (p *Program) operandText(i int) string {
	s := &p.Stmts[i]
	src := p.Doc.Text
	from := s.MnemStart + len(s.Mnemonic)
	var b strings.Builder
	at := from
	for _, t := range s.Toks {
		off := p.nlOff(t.Line) + t.Col
		if off < from {
			continue
		}
		if t.Kind == lexer.Comment {
			b.WriteString(src[at:off])
			b.WriteByte(' ')
			at = off + t.Len
			continue
		}
	}
	if at < s.End {
		b.WriteString(src[at:s.End])
	}
	return b.String()
}

func (p *Program) nlOff(line int) int {
	if line < len(p.nl) {
		return p.nl[line]
	}
	return len(p.Doc.Text)
}

// Expand expands one invocation of macro name (any case) with the given
// operand text, as if written at the current point of the assembly.
func (x *Expander) Expand(name, operands string) (Expansion, bool) {
	d, ok := x.Defs[strings.ToLower(name)]
	if !ok {
		return Expansion{}, false
	}
	lines := 0
	text, err := x.expand(d, operands, 0, &lines)
	return Expansion{Stmt: -1, Macro: d.Name, Text: text, Err: err}, true
}

func (x *Expander) expand(d *MacroDef, operands string, depth int, lines *int) (string, string) {
	vals, err := d.bind(operands)
	body := substitute(d.Body, vals, x.Counter)
	x.Counter++
	var b strings.Builder
	for _, line := range splitKeep(body) {
		*lines++
		if *lines > maxExpandLines {
			return b.String(), "expansion truncated at " + strconv.Itoa(maxExpandLines) + " lines"
		}
		word, labels, rest := lineHead(line)
		if strings.EqualFold(word, ".exitm") {
			break
		}
		nd, nested := x.Defs[strings.ToLower(word)]
		if !nested {
			b.WriteString(line)
			continue
		}
		if depth+1 >= maxExpandDepth {
			if err == "" {
				err = "macro nesting deeper than " + strconv.Itoa(maxExpandDepth)
			}
			b.WriteString(line)
			continue
		}
		if labels != "" {
			b.WriteString(labels)
			b.WriteByte('\n')
		}
		inner, ierr := x.expand(nd, rest, depth+1, lines)
		if err == "" {
			err = ierr
		}
		b.WriteString(inner)
		if !strings.HasSuffix(inner, "\n") && strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), err
}

// splitKeep splits text into lines that keep their line breaks.
func splitKeep(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// lineHead finds the first word of a line after any "label:" prefixes, so
// a nested invocation can be recognized. labels is the label text to keep;
// rest the operands after the word.
func lineHead(line string) (word, labels, rest string) {
	s := strings.TrimRight(line, "\r\n")
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		j := i
		for j < len(s) && isSymChar(s[j]) {
			j++
		}
		if j < len(s) && s[j] == ':' && j > i {
			i = j + 1
			labels = s[:i]
			continue
		}
		return s[i:j], labels, s[j:]
	}
}
