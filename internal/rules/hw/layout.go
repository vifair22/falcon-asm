package hw

import (
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
)

// The layout pass answers "where does this byte live relative to that
// label" without the assembler: statements of known size (data directives,
// .space, .fill, strings, labels and directives that emit nothing) chain
// into runs, and two labels in one run are a known distance apart. An
// instruction, an alignment, an .org or a section switch ends the run,
// since its size or its effect on the location counter is not known here.
// That is enough for every structure this package reads (descriptor tables,
// page tables, BIOS packets), which are data and laid out by hand.

// dataSizes are the element sizes of the integer data directives. .word is
// two bytes on x86, whatever the mode.
var dataSizes = map[string]int{
	".byte": 1, ".2byte": 2, ".hword": 2, ".short": 2, ".word": 2, ".value": 2,
	".4byte": 4, ".long": 4, ".int": 4, ".8byte": 8, ".quad": 8,
}

// silent are directives that emit nothing and leave the location counter
// alone, so a run continues across them.
var silent = map[string]bool{
	".globl": true, ".global": true, ".extern": true, ".type": true, ".size": true,
	".local": true, ".hidden": true, ".weak": true, ".file": true, ".loc": true,
	".ident": true, ".set": true, ".equ": true, ".equiv": true, ".eqv": true,
	".code16": true, ".code32": true, ".code64": true, ".code16gcc": true,
}

// pos is a location: an offset within a run. run -1 is unknown.
type pos struct {
	run int
	off int64
}

// Elem is one value a data directive emits.
type Elem struct {
	Stmt  int   // statement emitting it
	Arg   int   // argument index within the statement
	Off   int64 // offset within the run
	Size  int
	Known bool   // Val holds the value (truncated to Size bytes)
	Val   uint64 // the value when Known
	// Sym is set for a relocatable value Sym + Addend ("pdpt + 3").
	Sym    string
	Addend int64
}

// layout is the result of the pass over one program.
type layout struct {
	prog   *asm.Program
	stmt   []pos            // start of each statement
	size   []int64          // bytes each statement emits; -1 unknown
	labels map[string]pos   // label positions (global and the last of each local name)
	elems  [][]Elem         // per run, in order
	byStmt map[int][2]int   // statement -> [first, last+1) index into its run's elems
	sets   map[string]int64 // symbols assigned exactly once with a constant
}

func newLayout(p *asm.Program) *layout {
	l := &layout{
		prog: p, stmt: make([]pos, len(p.Stmts)), size: make([]int64, len(p.Stmts)),
		labels: map[string]pos{}, byStmt: map[int][2]int{}, sets: map[string]int64{},
	}
	l.collectSets()
	run, off := -1, int64(0)
	section := ""
	newRun := func() {
		l.elems = append(l.elems, nil)
		run, off = len(l.elems)-1, 0
	}
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if s.Section != section || run < 0 {
			section = s.Section
			newRun()
		}
		if s.InMacroDef {
			l.stmt[i], l.size[i] = pos{-1, 0}, -1
			newRun()
			continue
		}
		l.stmt[i] = pos{run, off}
		for _, lb := range s.Labels {
			l.labels[lb] = pos{run, off}
		}
		n := l.stmtSize(s)
		l.size[i] = n
		if n < 0 {
			newRun()
			continue
		}
		off += n
	}
	l.collectElems()
	return l
}

// collectSets records `name = expr` and `.set name, expr` assignments made
// once with a constant value; a symbol assigned twice (a .rept counter)
// has no single value and stays unknown.
func (l *layout) collectSets() {
	seen := map[string]int{}
	for i := range l.prog.Stmts {
		s := &l.prog.Stmts[i]
		if s.Kind != asm.Directive || s.Name != ".set" || len(s.Args) != 2 || s.Args[0] == "." {
			continue
		}
		seen[s.Args[0]]++
		if v, ok := l.evalConst(s.Args[1]); ok {
			l.sets[s.Args[0]] = v
		}
	}
	for name, n := range seen {
		if n > 1 {
			delete(l.sets, name)
		}
	}
}

// stmtSize is the number of bytes s emits, or -1 when it is not known or s
// moves the location counter in a way the pass cannot follow.
func (l *layout) stmtSize(s *asm.Stmt) int64 {
	switch s.Kind {
	case asm.Empty, asm.Label:
		return 0
	case asm.Cpp:
		// #define and #include emit nothing at this point; a conditional
		// means one of two texts is assembled, so the run cannot span it.
		t := ""
		if len(s.Toks) > 0 {
			t = strings.TrimLeft(strings.TrimPrefix(s.Toks[0].Text, "#"), " \t")
		}
		if strings.HasPrefix(t, "if") || strings.HasPrefix(t, "el") || strings.HasPrefix(t, "endif") {
			return -1
		}
		return 0
	case asm.Directive:
	default:
		return -1
	}
	if sz, ok := dataSizes[s.Name]; ok {
		return int64(sz * len(s.Args))
	}
	if s.Name == ".set" && len(s.Args) > 0 && s.Args[0] == "." {
		return -1 // `. = expr` moves the location counter
	}
	if silent[s.Name] {
		return 0
	}
	switch s.Name {
	case ".space", ".skip", ".zero":
		if len(s.Args) >= 1 {
			if n, ok := l.evalConst(s.Args[0]); ok && n >= 0 {
				return n
			}
		}
	case ".fill":
		if len(s.Args) >= 1 {
			rep, ok := l.evalConst(s.Args[0])
			size := int64(1)
			if len(s.Args) >= 2 {
				var ok2 bool
				size, ok2 = l.evalConst(s.Args[1])
				ok = ok && ok2
			}
			if ok && rep >= 0 && size >= 0 && size <= 8 {
				return rep * size
			}
		}
	case ".ascii", ".asciz", ".string":
		var n int64
		for _, a := range s.Args {
			b, ok := unquote(a)
			if !ok {
				return -1
			}
			n += int64(len(b))
			if s.Name != ".ascii" {
				n++
			}
		}
		return n
	}
	return -1
}

// collectElems evaluates every data element now that label positions are
// known, so `gdt_end - gdt - 1` resolves even though gdt_end comes later.
func (l *layout) collectElems() {
	for i := range l.prog.Stmts {
		s := &l.prog.Stmts[i]
		p := l.stmt[i]
		if p.run < 0 || l.size[i] <= 0 || s.Kind != asm.Directive {
			continue
		}
		first := len(l.elems[p.run])
		off := p.off
		emit := func(e Elem) {
			e.Stmt, e.Off = i, off
			off += int64(e.Size)
			l.elems[p.run] = append(l.elems[p.run], e)
		}
		if sz, ok := dataSizes[s.Name]; ok {
			for a, arg := range s.Args {
				e := l.value(arg, sz)
				e.Arg, e.Size = a, sz
				emit(e)
			}
		} else {
			switch s.Name {
			case ".space", ".skip", ".zero", ".fill":
				l.fillElems(s, emit)
			case ".ascii", ".asciz", ".string":
				for a, arg := range s.Args {
					b, _ := unquote(arg)
					if s.Name != ".ascii" {
						b = append(b, 0)
					}
					for _, c := range b {
						emit(Elem{Arg: a, Size: 1, Known: true, Val: uint64(c)})
					}
				}
			}
		}
		l.byStmt[i] = [2]int{first, len(l.elems[p.run])}
	}
}

// fillElems emits the elements of .space/.skip/.fill. A fill of a constant
// byte is that many known bytes; a long fill is capped, since nothing here
// looks further than a 4 KiB page table.
func (l *layout) fillElems(s *asm.Stmt, emit func(Elem)) {
	rep, size, val := int64(0), int64(1), int64(0)
	switch s.Name {
	case ".fill":
		rep, _ = l.evalConst(s.Args[0])
		if len(s.Args) >= 2 {
			size, _ = l.evalConst(s.Args[1])
		}
		if len(s.Args) >= 3 {
			var ok bool
			if val, ok = l.evalConst(s.Args[2]); !ok {
				val = -1
			}
		}
	default:
		rep, _ = l.evalConst(s.Args[0])
		if len(s.Args) >= 2 {
			var ok bool
			if val, ok = l.evalConst(s.Args[1]); !ok {
				val = -1
			}
		}
	}
	if size <= 0 || size > 8 {
		return
	}
	// .fill repeats a value of `size` bytes, of which gas writes the low
	// four and zeroes the rest.
	v := uint64(val)
	if s.Name == ".fill" && size > 4 {
		v &= 0xFFFFFFFF
	}
	const maxElems = 8192
	for k := int64(0); k < rep && k < maxElems; k++ {
		emit(Elem{Size: int(size), Known: val >= 0, Val: v & sizeMask(int(size))})
	}
}

func sizeMask(n int) uint64 {
	if n >= 8 {
		return ^uint64(0)
	}
	return 1<<(8*uint(n)) - 1
}

// value evaluates one data argument: a constant, or a relocatable symbol
// plus a constant (`gdt`, `pdpt + 3`, `3 + pdpt`, `pd | 0x83`).
func (l *layout) value(arg string, size int) Elem {
	if v, ok := l.eval(arg); ok {
		return Elem{Known: true, Val: uint64(v) & sizeMask(size)}
	}
	if sym, add, ok := l.symbolic(arg); ok {
		return Elem{Sym: sym, Addend: add}
	}
	return Elem{}
}

// symbolic splits "sym", "sym + c", "c + sym", "sym - c" and "sym | c" with
// c constant. A label is page aligned in every table this is used for, so
// or-ing flags into it is the same as adding them.
func (l *layout) symbolic(arg string) (string, int64, bool) {
	a := strings.TrimSpace(arg)
	for len(a) > 1 && a[0] == '(' && a[len(a)-1] == ')' && matchedParens(a[1:len(a)-1]) {
		a = strings.TrimSpace(a[1 : len(a)-1])
	}
	if isSymbol(a) {
		if _, isMacro := l.lookup(a); !isMacro {
			return a, 0, true
		}
		return "", 0, false
	}
	depth := 0
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '+', '-', '|':
			if depth != 0 || i == 0 {
				continue
			}
			left, right := strings.TrimSpace(a[:i]), strings.TrimSpace(a[i+1:])
			if isSymbol(left) {
				if _, isMacro := l.lookup(left); !isMacro {
					if c, ok := l.evalConst(right); ok {
						if a[i] == '-' {
							c = -c
						}
						return left, c, true
					}
				}
			}
			if a[i] == '+' && isSymbol(right) {
				if _, isMacro := l.lookup(right); !isMacro {
					if c, ok := l.evalConst(left); ok {
						return right, c, true
					}
				}
			}
			return "", 0, false
		}
	}
	return "", 0, false
}

func matchedParens(s string) bool {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			d++
		case ')':
			d--
			if d < 0 {
				return false
			}
		}
	}
	return d == 0
}

func isSymbol(s string) bool {
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c == '.' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// lookup resolves a name to a constant macro or assignment body.
func (l *layout) lookup(name string) (string, bool) {
	if b, ok := l.prog.Macros[name]; ok {
		return b, true
	}
	if v, ok := l.sets[name]; ok {
		return strconv.FormatInt(v, 10), true
	}
	return "", false
}

// evalConst evaluates an expression of numbers and macros only.
func (l *layout) evalConst(expr string) (int64, bool) {
	v, err := constexpr.Eval(normalize(expr), l.lookup)
	return v, err == nil
}

// eval evaluates an expression that may also name labels, as long as the
// result does not depend on where the run is placed: `gdt_end - gdt - 1` is
// a constant, `gdt` alone is not. The expression is evaluated twice with the
// run at two different bases; equal results prove the labels cancel.
func (l *layout) eval(expr string) (int64, bool) {
	e := normalize(expr)
	if v, err := constexpr.Eval(e, l.lookup); err == nil {
		return v, true
	}
	run := -2
	at := func(base int64) func(string) (string, bool) {
		return func(name string) (string, bool) {
			if b, ok := l.lookup(name); ok {
				return b, true
			}
			p, ok := l.labels[name]
			if !ok || p.run < 0 || run != -2 && p.run != run {
				return "", false
			}
			run = p.run
			return strconv.FormatInt(base+p.off, 10), true
		}
	}
	a, err1 := constexpr.Eval(e, at(0))
	b, err2 := constexpr.Eval(e, at(0x100000))
	if err1 != nil || err2 != nil || a != b {
		return 0, false
	}
	return a, true
}

// normalize rewrites what constexpr does not read: GAS character constants
// and the '.' and '$' that may appear in symbol names are left alone (they
// fail evaluation, which is the safe outcome).
func normalize(s string) string { return strings.TrimSpace(s) }

// unquote decodes a GAS string literal with the common escapes.
func unquote(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return nil, false
	}
	s = s[1 : len(s)-1]
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(s) {
			return nil, false
		}
		switch s[i] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case '\\', '"', '\'':
			out = append(out, s[i])
		case 'x', 'X':
			j := i + 1
			for j < len(s) && j < i+3 && isHex(s[j]) {
				j++
			}
			v, err := strconv.ParseUint(s[i+1:j], 16, 8)
			if err != nil {
				return nil, false
			}
			out = append(out, byte(v))
			i = j - 1
		default:
			if s[i] < '0' || s[i] > '7' {
				return nil, false
			}
			j := i
			for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			v, _ := strconv.ParseUint(s[i:j], 8, 8)
			out = append(out, byte(v))
			i = j - 1
		}
	}
	return out, true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// elemsFrom returns the elements of the run holding label, starting at the
// label, at most maxBytes bytes' worth. Labels inside the region do not end
// it: a GDT names its entries.
func (l *layout) elemsFrom(label string, maxBytes int64) []Elem {
	p, ok := l.labels[label]
	if !ok || p.run < 0 {
		return nil
	}
	return l.elemsAt(p, maxBytes)
}

func (l *layout) elemsAt(p pos, maxBytes int64) []Elem {
	es := l.elems[p.run]
	var out []Elem
	for _, e := range es {
		if e.Off < p.off {
			continue
		}
		if e.Off+int64(e.Size) > p.off+maxBytes {
			break
		}
		out = append(out, e)
	}
	return out
}

// bytesOf flattens elements starting at base into bytes, with a known flag
// per byte (a relocatable element's bytes are unknown).
func bytesOf(es []Elem, base int64) ([]byte, []bool) {
	if len(es) == 0 {
		return nil, nil
	}
	last := es[len(es)-1]
	n := last.Off + int64(last.Size) - base
	if n <= 0 || n > 1<<16 {
		return nil, nil
	}
	data, known := make([]byte, n), make([]bool, n)
	for _, e := range es {
		for k := 0; k < e.Size; k++ {
			at := e.Off - base + int64(k)
			if at < 0 || at >= n {
				continue
			}
			data[at] = byte(e.Val >> (8 * uint(k)))
			known[at] = e.Known
		}
	}
	return data, known
}

// elemStmtAt returns the statement emitting byte off of run, or -1.
func (l *layout) elemStmtAt(run int, off int64) int {
	if run < 0 || run >= len(l.elems) {
		return -1
	}
	for _, e := range l.elems[run] {
		if off >= e.Off && off < e.Off+int64(e.Size) {
			return e.Stmt
		}
	}
	return -1
}

// stmtElems is the elements statement i emits.
func (l *layout) stmtElems(i int) []Elem {
	r, ok := l.byStmt[i]
	if !ok {
		return nil
	}
	return l.elems[l.stmt[i].run][r[0]:r[1]]
}
