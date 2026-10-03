package asm

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// DefaultSection is the section in effect before any section directive,
// which is where GAS puts code that names none.
const DefaultSection = ".text"

// sectionState is one level of GAS's section stack: the current section and
// the one .previous returns to.
type sectionState struct {
	cur, prev string
}

// fixup records where a statement's slices live in the shared arenas. The
// arenas grow while the pass runs, so slices are only cut once it is done;
// cutting early would leave statements pointing at abandoned copies.
type fixup struct {
	labLo, labHi int32
	preLo, preHi int32
	argLo, argHi int32
}

type parser struct {
	src  string
	toks []lexer.Token
	nl   []int // byte offset of each lexer line (lines split at '\n' only)
	prog *Program
	cfg  Config

	cppDefs   map[string]string // in-file object-like #define bodies
	cppNames  map[string]bool   // every in-file #define name, function-like included
	asmNames  map[string]bool   // in-file .macro names, lowercase
	macLocals map[string][]string
	equ       map[string]equVal
	lookupFn  func(string) (string, bool)
	evalDepth int

	mode     Mode
	sec      sectionState
	secStack []sectionState
	macDepth int
	curMac   string // outermost .macro being defined, lowercase
	condLvl  int    // .if nesting: assignments inside are not followed
	loopLvl  int    // .rept/.irp nesting

	ops   []Operand // current operand chunk
	strs  []string
	fix   []fixup
	items []item // operand items of the statement in hand, reused
	buf   []int  // non-comment token indexes of the operand in hand, reused
}

// equVal is a GAS symbol assignment's value as the pass last saw it. A
// value that cannot be pinned down (assigned in a loop or a conditional) is
// kept with ok false so it shadows nothing and never evaluates.
type equVal struct {
	body string
	ok   bool
}

func parse(doc *docstore.Document, toks []lexer.Token, cfg Config) *Program {
	if cfg.InitialMode != Mode16 && cfg.InitialMode != Mode32 && cfg.InitialMode != Mode64 {
		cfg.InitialMode = Mode64
	}
	p := &parser{
		src:  doc.Text,
		toks: toks,
		cfg:  cfg,
		mode: cfg.InitialMode,
		sec:  sectionState{cur: DefaultSection, prev: DefaultSection},
		prog: &Program{Doc: doc, Toks: toks},
	}
	p.lookupFn = p.lookup
	p.lineStarts()
	nStmts, nOps, nLabels := p.collectMacros()
	p.prog.nl = p.nl
	p.prog.macLocals = p.macLocals
	// Both counts are upper bounds from the pre-pass, so neither slice
	// regrows: a Stmt and an Operand are each a few hundred bytes, and
	// regrowth copies were most of the parse's cost.
	p.prog.Stmts = make([]Stmt, 0, nStmts)
	p.fix = make([]fixup, 0, nStmts)
	p.ops = make([]Operand, 0, nOps)
	p.prog.Labels = make(map[string]int, nLabels)
	// Labels, prefixes and directive arguments: about one per statement
	// in the fixtures. Growth past it is harmless; strings are small.
	p.strs = make([]string, 0, nStmts)
	p.items = make([]item, 0, 8)
	p.buf = make([]int, 0, 32)
	p.run()
	p.finish()
	return p.prog
}

func (p *parser) lineStarts() {
	p.nl = make([]int, 1, strings.Count(p.src, "\n")+1)
	for i := 0; ; {
		j := strings.IndexByte(p.src[i:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
		p.nl = append(p.nl, i)
	}
}

func (p *parser) off(i int) int {
	t := &p.toks[i]
	if t.Line < len(p.nl) {
		return p.nl[t.Line] + t.Col
	}
	return len(p.src)
}

func (p *parser) end(i int) int { return p.off(i) + p.toks[i].Len }

// cppDirectiveNames maps a cpp directive word to its canonical spelling, so
// "#  define" names the statement "#define" without building a string.
var cppDirectiveNames = map[string]string{
	"include": "#include", "include_next": "#include_next", "define": "#define",
	"undef": "#undef", "if": "#if", "ifdef": "#ifdef", "ifndef": "#ifndef",
	"elif": "#elif", "elifdef": "#elifdef", "elifndef": "#elifndef",
	"else": "#else", "endif": "#endif", "error": "#error",
	"warning": "#warning", "pragma": "#pragma", "line": "#line",
}

func cppWord(text string) string {
	return strings.TrimLeft(strings.TrimPrefix(text, "#"), " \t")
}

// leadingIdent returns the C identifier at the start of s.
func leadingIdent(s string) string {
	j := 0
	for j < len(s) && (s[j] == '_' || isLetter(s[j]) || (j > 0 && isDigit(s[j]))) {
		j++
	}
	return s[:j]
}

func isLetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

// collectMacros finds every name that turns a statement into a macro call,
// and the values of in-file object-like #defines, before the main pass:
// a .macro may be invoked above its definition when a file is included in
// pieces, and the analysis package decides the same way, so the two cannot
// disagree about which statements are instructions.
//
// The same walk bounds the statement and operand counts: a statement
// starts on a new line or after ';', and an instruction has at most one
// operand more than its top-level commas.
func (p *parser) collectMacros() (nStmts, nOps, nLabels int) {
	depth := 0
	line := -1
	inInsn := false
	nest := 0 // parentheses and braces inside an instruction's operands
	for i := 0; i < len(p.toks); i++ {
		t := &p.toks[i]
		if t.Kind != lexer.Comment && t.Line != line {
			nStmts++
			line = t.Line
			inInsn = false
		}
		switch t.Kind {
		case lexer.LabelDef:
			nLabels++
		case lexer.Mnemonic, lexer.Identifier:
			if !inInsn {
				inInsn, nest = true, 0
				nOps++
			}
		case lexer.Punct:
			switch t.Text {
			case ";":
				nStmts++
				inInsn = false
			case "(", "{":
				nest++
			case ")", "}":
				nest--
			case ",":
				if inInsn && nest == 0 {
					nOps++
				}
			}
		case lexer.CppDirective:
			if cppWord(t.Text) != "define" || i+1 >= len(p.toks) {
				continue
			}
			a := &p.toks[i+1]
			if a.Kind != lexer.CppArg || a.Line != t.Line {
				continue
			}
			name := leadingIdent(a.Text)
			if name == "" {
				continue
			}
			if p.cppNames == nil {
				p.cppNames = map[string]bool{}
				p.cppDefs = map[string]string{}
			}
			p.cppNames[name] = true
			rest := a.Text[len(name):]
			if strings.HasPrefix(rest, "(") {
				continue // function-like: a call, never a value
			}
			if _, seen := p.cppDefs[name]; !seen {
				p.cppDefs[name] = p.defineBody(i+1, rest)
			}
		case lexer.Directive:
			switch {
			case strings.EqualFold(t.Text, ".macro"):
				depth++
				if i+1 < len(p.toks) && p.toks[i+1].Line == t.Line && p.toks[i+1].Kind == lexer.Identifier {
					if p.asmNames == nil {
						p.asmNames = map[string]bool{}
					}
					name := strings.ToLower(p.toks[i+1].Text)
					p.asmNames[name] = true
					if depth == 1 {
						p.curMac = name
					}
				}
			case strings.EqualFold(t.Text, ".endm") || strings.EqualFold(t.Text, ".endmacro"):
				if depth > 0 {
					depth--
				}
			}
		case lexer.LocalLabelDef:
			// A macro whose body defines N: also defines it at every
			// call site, which LocalTarget must not jump over.
			if depth > 0 && p.curMac != "" {
				if p.macLocals == nil {
					p.macLocals = map[string][]string{}
				}
				p.macLocals[p.curMac] = append(p.macLocals[p.curMac], t.Text)
			}
		}
	}
	p.curMac = ""
	return nStmts, nOps, nLabels
}

// defineBody returns a #define's value: every CppArg segment of the
// logical line after the name (comments split a line into segments, and
// continuations add lines), joined by single blanks, with the
// continuation backslashes gone. One segment, the common case, is returned
// as a slice of the source.
func (p *parser) defineBody(i int, rest string) string {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "\\"))
	j := i + 1
	for j < len(p.toks) && p.toks[j].Kind == lexer.Comment {
		j++
	}
	if j >= len(p.toks) || p.toks[j].Kind != lexer.CppArg {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	for ; j < len(p.toks); j++ {
		t := &p.toks[j]
		if t.Kind == lexer.Comment {
			continue
		}
		if t.Kind != lexer.CppArg {
			break
		}
		if seg := strings.TrimSpace(strings.TrimSuffix(t.Text, "\\")); seg != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(seg)
		}
	}
	return b.String()
}

// isMacroCall reports whether a word in mnemonic position names a macro.
// Macros win over instructions, as they do in the assembler: cpp rewrites
// the word before as sees it, and a .macro shadows a mnemonic.
func (p *parser) isMacroCall(word string) bool {
	if p.cppNames[word] {
		return true
	}
	if _, ok := p.cfg.Macros[word]; ok {
		return true
	}
	return p.asmNames[strings.ToLower(word)]
}

// lookup resolves an identifier for constexpr: cpp macros first, since cpp
// has replaced them before as runs, then GAS symbol assignments.
func (p *parser) lookup(name string) (string, bool) {
	if b, ok := p.cppDefs[name]; ok {
		return b, true
	}
	if b, ok := p.cfg.Macros[name]; ok {
		return b, true
	}
	if v, ok := p.equ[name]; ok && v.ok {
		return v.body, true
	}
	return "", false
}

// run splits the token stream into statements. A statement ends at ';' or
// at a line break that is not inside a block comment: cpp turns a comment
// into one space, so a statement that straddles a multi-line comment is one
// statement to the assembler. A cpp directive is one statement through its
// continuation lines.
func (p *parser) run() {
	toks := p.toks
	lo, last := -1, -1
	cur := -1 // the line up to which line breaks are known to be inside comments
	inBlock, blockCovers := false, false
	inCpp := false
	flush := func() {
		if lo >= 0 {
			p.statement(lo, last+1, inCpp)
		}
		lo, last, inCpp = -1, -1, false
	}
	for i := range toks {
		t := &toks[i]
		if t.Kind == lexer.Comment {
			closes := len(t.Text) >= 2 && strings.HasSuffix(t.Text, "*/")
			switch {
			case inBlock:
				if closes {
					inBlock = false
					if blockCovers {
						cur = t.Line
					}
				}
			case strings.HasPrefix(t.Text, "/*") && !(len(t.Text) >= 4 && closes):
				inBlock = true
				blockCovers = t.Line == cur
			}
			continue
		}
		if inCpp {
			if t.Kind == lexer.CppArg {
				last = i
				continue
			}
			flush()
		}
		if lo >= 0 && t.Line != cur {
			flush()
		}
		switch {
		case t.Kind == lexer.Punct && t.Text == ";":
			flush()
			cur = t.Line
			continue
		case t.Kind == lexer.CppDirective:
			flush()
			inCpp = true
		}
		if lo < 0 {
			lo = i
		}
		last = i
		cur = t.Line
	}
	flush()
}

// next returns the first non-comment token index at or after i, or hi.
func (p *parser) next(i, hi int) int {
	for i < hi && p.toks[i].Kind == lexer.Comment {
		i++
	}
	return i
}

func (p *parser) isPunct(i, hi int, s string) bool {
	return i < hi && p.toks[i].Kind == lexer.Punct && p.toks[i].Text == s
}

func (p *parser) statement(lo, hi int, cpp bool) {
	// Built in place: the slice is presized to an upper bound and zeroed,
	// so growing it by one and filling the fields avoids building a
	// 256-byte Stmt and copying it in. Nothing else appends to Stmts
	// while st is held, so st cannot go stale even past the bound.
	idx := len(p.prog.Stmts)
	if idx < cap(p.prog.Stmts) {
		p.prog.Stmts = p.prog.Stmts[:idx+1]
	} else {
		p.prog.Stmts = append(p.prog.Stmts, Stmt{})
	}
	st := &p.prog.Stmts[idx]
	st.Line = p.toks[lo].Line
	st.EndLine = p.toks[hi-1].Line
	st.Start = p.off(lo)
	st.End = p.end(hi - 1)
	st.Toks = p.toks[lo:hi]
	st.Mode = p.mode
	st.Section = p.sec.cur
	st.InMacroDef = p.macDepth > 0
	st.MnemStart = -1
	fx := fixup{labLo: int32(len(p.strs))}
	if cpp {
		st.Kind = Cpp
		w := cppWord(p.toks[lo].Text)
		if n, ok := cppDirectiveNames[w]; ok {
			st.Name = n
		} else {
			st.Name = p.toks[lo].Text
		}
		fx.labHi, fx.preLo, fx.preHi, fx.argLo = fx.labLo, fx.labLo, fx.labLo, fx.labLo
		for i := lo + 1; i < hi; i++ {
			if p.toks[i].Kind == lexer.CppArg {
				p.strs = append(p.strs, p.toks[i].Text)
			}
		}
		fx.argHi = int32(len(p.strs))
		p.fix = append(p.fix, fx)
		return
	}

	i := p.next(lo, hi)
	for i < hi {
		t := &p.toks[i]
		isName := t.Kind == lexer.LabelDef || t.Kind == lexer.LocalLabelDef ||
			(t.Kind == lexer.Identifier && strings.HasPrefix(t.Text, "\\"))
		c := p.next(i+1, hi)
		if !isName || !p.isPunct(c, hi, ":") {
			break
		}
		p.strs = append(p.strs, t.Text)
		if !st.InMacroDef && t.Kind == lexer.LabelDef {
			if _, dup := p.prog.Labels[t.Text]; !dup {
				p.prog.Labels[t.Text] = idx
			}
		}
		i = p.next(c+1, hi)
	}
	fx.labHi = int32(len(p.strs))
	fx.preLo = fx.labHi
	for i < hi && p.toks[i].Kind == lexer.Prefix {
		if st.MnemStart < 0 {
			st.MnemStart = p.off(i)
		}
		p.strs = append(p.strs, strings.ToLower(p.toks[i].Text))
		i = p.next(i+1, hi)
	}
	fx.preHi = int32(len(p.strs))
	fx.argLo, fx.argHi = fx.preHi, fx.preHi

	switch {
	case i >= hi:
		switch {
		case fx.preHi > fx.preLo:
			// A prefix on its own line ("rep; movsb") assembles to the
			// prefix byte alone; it is an instruction with no mnemonic.
			st.Kind = Instr
		case fx.labHi > fx.labLo:
			st.Kind = Label
		default:
			st.Kind = Empty
		}
	case p.isAssignment(i, hi):
		st.Kind = Directive
		st.Name = ".set"
		p.strs = append(p.strs, p.toks[i].Text)
		eq := p.next(i+1, hi)
		p.strs = append(p.strs, p.textOf(eq+1, hi))
		fx.argHi = int32(len(p.strs))
		p.assign(p.toks[i].Text, p.strs[len(p.strs)-1], ".set")
	case p.toks[i].Kind == lexer.Directive:
		st.Kind = Directive
		st.Name = strings.ToLower(p.toks[i].Text)
		p.args(i+1, hi)
		fx.argHi = int32(len(p.strs))
		if st.Name == ".equ" {
			st.Name = ".set"
		}
		p.directive(st, p.strs[fx.argLo:fx.argHi], hi)
	case p.toks[i].Kind == lexer.Mnemonic || (p.toks[i].Kind == lexer.Identifier && strings.HasPrefix(p.toks[i].Text, "\\")):
		st.MnemStart = p.off(i)
		st.Mnemonic = strings.ToLower(p.toks[i].Text)
		p.instruction(st, i, hi, &fx)
	default:
		st.Kind = Empty
	}
	p.fix = append(p.fix, fx)
}

// isAssignment reports "name = expr" (also ". = expr" and "\x = 1").
func (p *parser) isAssignment(i, hi int) bool {
	k := p.toks[i].Kind
	if k != lexer.LabelDef && k != lexer.Identifier {
		return false
	}
	return p.isPunct(p.next(i+1, hi), hi, "=")
}

// textOf is the source text from token lo up to the last non-comment token
// before hi; "" when there is none.
func (p *parser) textOf(lo, hi int) string {
	lo = p.next(lo, hi)
	if lo >= hi {
		return ""
	}
	last := hi - 1
	for last > lo && p.toks[last].Kind == lexer.Comment {
		last--
	}
	return p.src[p.off(lo):p.end(last)]
}

// args appends a directive's comma-separated arguments, splitting only at
// top level so ".word (a, b)" style expressions stay whole.
func (p *parser) args(lo, hi int) {
	if p.next(lo, hi) >= hi {
		return
	}
	depth := 0
	start := lo
	for i := lo; i < hi; i++ {
		t := &p.toks[i]
		if t.Kind != lexer.Punct {
			continue
		}
		switch t.Text {
		case "(":
			depth++
		case ")":
			if depth > 0 {
				depth--
			}
		case ",":
			if depth == 0 {
				p.strs = append(p.strs, p.textOf(start, i))
				start = i + 1
			}
		}
	}
	p.strs = append(p.strs, p.textOf(start, hi))
}

// directive applies the directives that change parser state. Inside a
// .macro body only nesting is tracked: what a body does to the mode, the
// section or a symbol happens where it is expanded, not where it is
// written.
func (p *parser) directive(st *Stmt, args []string, hi int) {
	switch st.Name {
	case ".macro":
		p.macDepth++
		return
	case ".endm", ".endmacro":
		if p.macDepth > 0 {
			p.macDepth--
		}
		return
	}
	if p.macDepth > 0 {
		return
	}
	switch st.Name {
	case ".code16", ".code16gcc":
		p.mode = Mode16
	case ".code32":
		p.mode = Mode32
	case ".code64":
		p.mode = Mode64
	case ".text", ".data", ".bss":
		p.setSection(st.Name)
	case ".section":
		if name := firstSection(args); name != "" {
			p.setSection(name)
		}
	case ".pushsection":
		if name := firstSection(args); name != "" {
			p.secStack = append(p.secStack, p.sec)
			p.setSection(name)
		}
	case ".popsection":
		if n := len(p.secStack); n > 0 {
			p.sec = p.secStack[n-1]
			p.secStack = p.secStack[:n-1]
		}
	case ".previous":
		p.sec.cur, p.sec.prev = p.sec.prev, p.sec.cur
	case ".set", ".equiv", ".eqv":
		if len(args) >= 2 {
			p.assign(args[0], args[1], st.Name)
		}
	case ".if", ".ifdef", ".ifndef", ".ifb", ".ifnb", ".ifc", ".ifnc", ".ifeq",
		".ifeqs", ".ifge", ".ifgt", ".ifle", ".iflt", ".ifne", ".ifnes", ".ifnotdef":
		p.condLvl++
	case ".endif":
		if p.condLvl > 0 {
			p.condLvl--
		}
	case ".rept", ".irp", ".irpc":
		if p.loopLvl == 0 {
			p.poisonLoop(hi)
		}
		p.loopLvl++
	case ".endr":
		if p.loopLvl > 0 {
			p.loopLvl--
		}
	}
}

// firstSection is the section a .section or .pushsection names, "" when
// it names none (a missing or empty-quoted name changes nothing).
func firstSection(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return sectionName(args[0])
}

// sectionName strips the quotes GAS allows around a section name.
func sectionName(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	if j := strings.IndexAny(s, " \t"); j > 0 {
		return s[:j] // ".text 1": a subsection of .text
	}
	return s
}

func (p *parser) setSection(name string) {
	p.sec.prev = p.sec.cur
	p.sec.cur = name
}

// assign follows a symbol assignment so later expressions can use the
// value. Only straight-line assignments are followed: one in a conditional
// may not happen, one in a loop happens many times, and .eqv re-evaluates
// at every use; each of those makes the symbol unknown instead.
func (p *parser) assign(name, expr, dir string) {
	if p.macDepth > 0 || name == "." || strings.HasPrefix(name, "\\") {
		return
	}
	if p.equ == nil {
		p.equ = map[string]equVal{}
	}
	if p.condLvl > 0 || p.loopLvl > 0 || dir == ".eqv" {
		p.equ[name] = equVal{}
		return
	}
	v := p.evalText(expr)
	if !v.Known {
		p.equ[name] = equVal{}
		return
	}
	p.equ[name] = equVal{body: formatInt(v.V), ok: true}
}

// poisonLoop makes every symbol assigned inside a .rept/.irp body unknown
// from the loop's start: a use before the assignment in the body sees a
// different value on every iteration but the first.
func (p *parser) poisonLoop(hi int) {
	depth := 0
	for j := hi; j < len(p.toks); j++ {
		t := &p.toks[j]
		if t.Kind == lexer.Directive {
			switch strings.ToLower(t.Text) {
			case ".rept", ".irp", ".irpc":
				depth++
			case ".endr":
				if depth == 0 {
					return
				}
				depth--
			}
			continue
		}
		if t.Kind == lexer.LabelDef && !(j+1 < len(p.toks) && p.toks[j+1].Kind == lexer.Punct && p.toks[j+1].Text == ":") {
			if p.equ == nil {
				p.equ = map[string]equVal{}
			}
			p.equ[t.Text] = equVal{}
		}
	}
}

// instruction classifies the statement and parses its operands.
func (p *parser) instruction(st *Stmt, mi, hi int, fx *fixup) {
	word := p.toks[mi].Text
	rest := p.next(mi+1, hi)
	switch {
	case p.toks[mi].Kind == lexer.Identifier || p.isMacroCall(word):
		st.Kind = MacroCall
	case isa.IsPrefix(st.Mnemonic) && rest >= hi:
		// "cs; movsb": a segment word alone is the override byte.
		st.Kind = Instr
		p.strs = append(p.strs, st.Mnemonic)
		fx.preHi = int32(len(p.strs))
		fx.argLo, fx.argHi = fx.preHi, fx.preHi
		st.Mnemonic = ""
		return
	default:
		if inst, suf, ok := isa.Lookup(st.Mnemonic); ok {
			st.Kind = Instr
			st.Inst = inst
			st.Suffix = suf
		} else {
			st.Kind = MacroCall
		}
	}
	p.operands(st, rest, hi)
}

// finish cuts every statement's slices out of the arenas.
func (p *parser) finish() {
	for k := range p.prog.Stmts {
		st := &p.prog.Stmts[k]
		f := &p.fix[k]
		if f.labHi > f.labLo {
			st.Labels = p.strs[f.labLo:f.labHi:f.labHi]
		}
		if f.preHi > f.preLo {
			st.Prefixes = p.strs[f.preLo:f.preHi:f.preHi]
		}
		if f.argHi > f.argLo {
			st.Args = p.strs[f.argLo:f.argHi:f.argHi]
		}
	}
	if len(p.cppDefs) == 0 {
		p.prog.Macros = p.cfg.Macros
		return
	}
	if len(p.cfg.Macros) == 0 {
		p.prog.Macros = p.cppDefs
		return
	}
	m := make(map[string]string, len(p.cfg.Macros)+len(p.cppDefs))
	for k, v := range p.cfg.Macros {
		m[k] = v
	}
	// In-file definitions describe the text being edited, so they win,
	// as they do in hover.
	for k, v := range p.cppDefs {
		m[k] = v
	}
	p.prog.Macros = m
}
