package symbols

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// def is a name defined or declared by a statement: the name token's byte
// span in the document.
type def struct {
	name       string
	stmt       int
	start, end int
}

// ref is a use of a name in a place where an undefined symbol is a real
// problem: an instruction operand or the value of a data or assignment
// directive.
type ref struct {
	name       string
	stmt       int
	start, end int
}

// localRef is a 1f/1b reference.
type localRef struct {
	text       string // lowercase, "1f"
	stmt       int
	start, end int
}

// frame is one open conditional: which block, and which of its branches
// (#if/#elif/#else, .if/.elseif/.else) the statement is in.
type frame struct {
	id, branch int
}

// table is everything the symbol rules need from one document, built in
// one pass over the statements and shared through the run's memo.
type table struct {
	labels  []def // non-local labels outside macro bodies, in order
	equs    []def // .set/.equ/.equiv/.eqv/= outside macro bodies
	defines []def // #define
	globals []def // names in .globl/.global
	externs []def // names in .extern

	defined  map[string]bool // every name the document defines, macro bodies included
	declared map[string]bool // named by a symbol declaration (.globl, .extern, .weak, ...)
	exported map[string]bool // .globl/.global/.weak: visible to the link
	function map[string]bool // .type name, @function
	uses     map[string]int  // occurrences that use a name (not its definition or declarations)

	refs      []ref
	localRefs []localRef
	cond      map[int][]frame // conditional path of each label-defining statement
}

const tableKey = "symbols/table"

func tableOf(c *lint.Context) *table {
	if c.Prog == nil || c.Prog.Doc == nil {
		return nil
	}
	t, _ := c.Memo(tableKey, func() any { return build(c.Prog) }).(*table)
	return t
}

// declDirectives name symbols without using them: a label only mentioned in
// its own .globl, .type or .size is still unreferenced.
var declDirectives = map[string]bool{
	".globl": true, ".global": true, ".extern": true, ".weak": true, ".hidden": true,
	".internal": true, ".protected": true, ".local": true, ".type": true, ".size": true,
}

// refDirectives take expressions whose symbols must exist: data, location
// moves and alignment fills.
var refDirectives = map[string]bool{
	".byte": true, ".word": true, ".short": true, ".hword": true, ".value": true, ".int": true,
	".long": true, ".quad": true, ".octa": true, ".2byte": true, ".4byte": true, ".8byte": true,
	".dc.b": true, ".dc.w": true, ".dc.l": true, ".dc.a": true, ".dc.q": true, ".rva": true,
	".org": true, ".skip": true, ".space": true, ".fill": true, ".zero": true,
}

func build(p *asm.Program) *table {
	t := &table{
		defined: map[string]bool{}, declared: map[string]bool{}, exported: map[string]bool{},
		function: map[string]bool{}, uses: map[string]int{}, cond: map[int][]frame{},
	}
	var stack []frame
	nextID := 0
	k := 0 // index into p.Toks of the statement's first token
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if len(s.Toks) == 0 {
			continue
		}
		for k < len(p.Toks) && &p.Toks[k] != &s.Toks[0] {
			k++
		}
		off := func(j int) int { return p.TokOffset(k + j) }
		switch s.Kind {
		case asm.Cpp:
			switch s.Name {
			case "#if", "#ifdef", "#ifndef":
				nextID++
				stack = append(stack, frame{id: nextID})
			case "#elif", "#else", "#elifdef", "#elifndef":
				if n := len(stack); n > 0 {
					stack[n-1].branch++
				}
			case "#endif":
				if n := len(stack); n > 0 {
					stack = stack[:n-1]
				}
			}
			t.cppStmt(s, off)
			continue
		case asm.Directive:
			switch {
			case strings.HasPrefix(s.Name, ".if"):
				nextID++
				stack = append(stack, frame{id: nextID})
			case s.Name == ".else" || s.Name == ".elseif":
				if n := len(stack); n > 0 {
					stack[n-1].branch++
				}
			case s.Name == ".endif":
				if n := len(stack); n > 0 {
					stack = stack[:n-1]
				}
			}
		}
		t.stmt(p, i, s, off, stack)
	}
	return t
}

// cppStmt records a cpp line: a #define's name, and the macro names its
// body and the conditionals use.
func (t *table) cppStmt(s *asm.Stmt, off func(int) int) {
	first := s.Name == "#define"
	for j, tok := range s.Toks {
		if tok.Kind != lexer.CppArg {
			continue
		}
		for _, w := range cWords(tok.Text) {
			if first {
				first = false
				start := off(j) + w.off
				t.defines = append(t.defines, def{name: w.word, stmt: -1, start: start, end: start + len(w.word)})
				t.defined[w.word] = true
				continue
			}
			switch s.Name {
			case "#define", "#if", "#ifdef", "#ifndef", "#elif", "#elifdef", "#elifndef":
				if w.word != "defined" {
					t.uses[w.word]++
				}
			}
		}
	}
}

// stmt records an assembly statement's labels, definitions, declarations
// and uses.
func (t *table) stmt(p *asm.Program, i int, s *asm.Stmt, off func(int) int, stack []frame) {
	toks := s.Toks
	j := 0
	// Labels open the statement: a name and its colon, possibly several.
	for j < len(toks) {
		tk := &toks[j]
		if tk.Kind == lexer.Comment {
			j++
			continue
		}
		if (tk.Kind != lexer.LabelDef && tk.Kind != lexer.LocalLabelDef) || j+1 >= len(toks) ||
			toks[j+1].Kind != lexer.Punct || toks[j+1].Text != ":" {
			break
		}
		if tk.Kind == lexer.LabelDef {
			t.defined[tk.Text] = true
			if !s.InMacroDef {
				t.labels = append(t.labels, def{name: tk.Text, stmt: i, start: off(j), end: off(j) + tk.Len})
				if _, ok := t.cond[i]; !ok {
					t.cond[i] = append([]frame(nil), stack...)
				}
			}
		}
		j += 2
	}
	dir := ""
	if s.Kind == asm.Directive {
		dir = s.Name
	}
	argN := 0 // symbol-like operands seen after the directive word
	for ; j < len(toks); j++ {
		tk := &toks[j]
		switch tk.Kind {
		case lexer.Directive:
			continue
		case lexer.LocalLabelRef:
			if !s.InMacroDef && s.Kind != asm.Cpp {
				t.localRefs = append(t.localRefs, localRef{text: strings.ToLower(tk.Text), stmt: i, start: off(j), end: off(j) + tk.Len})
			}
			continue
		case lexer.Mnemonic:
			if s.Kind == asm.MacroCall {
				t.uses[tk.Text]++
				if lw := strings.ToLower(tk.Text); lw != tk.Text {
					t.uses[lw]++
				}
			}
			continue
		case lexer.Identifier:
			if dir == ".macro" && argN == 0 {
				t.defined[tk.Text] = true
				t.defined[strings.ToLower(tk.Text)] = true
				argN++
				continue
			}
			if dir == ".section" || dir == ".pushsection" || dir == ".macro" || declDirectives[dir] ||
				!isSymbolName(tk.Text) || tk.Text == "." {
				continue
			}
			t.uses[tk.Text]++
			continue
		case lexer.LabelDef:
			// The name of .set/.equ/.equiv/.eqv or "name = value".
			t.defined[tk.Text] = true
			if !s.InMacroDef && tk.Text != "." {
				t.equs = append(t.equs, def{name: tk.Text, stmt: i, start: off(j), end: off(j) + tk.Len})
			}
			argN++
			continue
		}
		name, start := "", 0
		switch tk.Kind {
		case lexer.LabelRef:
			name, start = tk.Text, off(j)
		case lexer.Immediate:
			if a := tk.Text[1:]; isSymbolName(a) {
				name, start = a, off(j)+1
			}
		}
		if name == "" {
			continue
		}
		first := argN == 0
		argN++
		d := def{name: name, stmt: i, start: start, end: start + len(name)}
		switch dir {
		case ".globl", ".global":
			t.declared[name], t.exported[name] = true, true
			if !s.InMacroDef {
				t.globals = append(t.globals, d)
			}
			continue
		case ".extern":
			t.declared[name] = true
			if !s.InMacroDef {
				t.externs = append(t.externs, d)
			}
			continue
		case ".weak":
			t.declared[name], t.exported[name] = true, true
			continue
		case ".type":
			if first {
				t.declared[name] = true
				t.function[name] = t.function[name] || strings.Contains(stmtText(p, s), "function")
			}
			continue
		case ".hidden", ".internal", ".protected", ".local", ".size":
			t.declared[name] = true
			continue
		case ".comm", ".lcomm":
			if first {
				t.defined[name] = true
				continue
			}
		case ".weakref":
			if first {
				t.defined[name] = true
				continue
			}
		}
		t.uses[name]++
		if s.InMacroDef {
			continue
		}
		if s.Kind == asm.Instr || refDirectives[dir] || dir == ".set" || dir == ".equiv" || dir == ".eqv" {
			t.refs = append(t.refs, ref{name: name, stmt: i, start: d.start, end: d.end})
		}
	}
}

func stmtText(p *asm.Program, s *asm.Stmt) string {
	return p.Doc.Text[s.Start:s.End]
}

type cWord struct {
	off  int
	word string
}

// cWords splits a cpp argument into C identifiers, skipping numbers and
// quoted text.
func cWords(s string) []cWord {
	var out []cWord
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			out = append(out, cWord{i, s[i:j]})
			i = j
		case c >= '0' && c <= '9':
			for i < len(s) && isIdentChar(s[i]) {
				i++
			}
		default:
			i++
		}
	}
	return out
}

func isIdentStart(c byte) bool { return c == '_' || (c|0x20) >= 'a' && (c|0x20) <= 'z' }
func isIdentChar(c byte) bool  { return isIdentStart(c) || c >= '0' && c <= '9' }

// isSymbolName reports a GAS symbol: letters, digits, '_', '.' and '$', not
// starting with a digit.
func isSymbolName(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !isIdentChar(c) && c != '.' && c != '$' {
			return false
		}
	}
	return true
}

// exclusive reports whether two conditional paths are in different
// branches of the same #if or .if, so at most one of them is assembled.
func exclusive(a, b []frame) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i].id != b[i].id {
			return false
		}
		if a[i].branch != b[i].branch {
			return true
		}
	}
	return false
}
