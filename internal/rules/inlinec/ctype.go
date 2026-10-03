package inlinec

import (
	"strconv"
	"strings"
)

// There is no C type checker here, so operand widths come from the obvious
// cases only: an integer literal, a cast, the address of something, or a
// plain identifier whose declaration in the enclosing function (or its
// parameter list) names a type this file knows. Anything else, including
// an identifier declared twice with different widths, is unknown, and the
// width rules stay quiet about it.

// cType is what a width rule needs from an expression's type.
type cType struct {
	width     int    // bits
	name      string // as written, for messages
	pointer   bool
	heuristic bool // a typedef mapped by convention (UINT32, u8), not a standard type
}

// exactTypes are standard C and <stdint.h> names; 0 means pointer-sized.
var exactTypes = map[string]int{
	"uint8_t": 8, "int8_t": 8, "uint_least8_t": 8, "int_least8_t": 8, "uint_fast8_t": 8, "int_fast8_t": 8,
	"uint16_t": 16, "int16_t": 16, "uint_least16_t": 16, "int_least16_t": 16,
	"uint32_t": 32, "int32_t": 32, "uint_least32_t": 32, "int_least32_t": 32,
	"uint64_t": 64, "int64_t": 64, "uint_least64_t": 64, "int_least64_t": 64, "uint_fast64_t": 64, "int_fast64_t": 64,
	"size_t": 0, "ssize_t": 0, "uintptr_t": 0, "intptr_t": 0, "ptrdiff_t": 0,
	"_Bool": 8, "bool": 8, "char": 8, "short": 16, "int": 32,
}

// conventionTypes are typedefs whose width is a near-universal convention
// (Linux, EDK2, Windows) but which this file cannot see the definition of.
var conventionTypes = map[string]int{
	"u8": 8, "s8": 8, "__u8": 8, "__s8": 8, "UINT8": 8, "INT8": 8, "BYTE": 8, "CHAR8": 8, "UCHAR": 8, "BOOLEAN": 8,
	"u16": 16, "s16": 16, "__u16": 16, "__s16": 16, "UINT16": 16, "INT16": 16, "WORD": 16, "USHORT": 16, "CHAR16": 16,
	"u32": 32, "s32": 32, "__u32": 32, "__s32": 32, "UINT32": 32, "INT32": 32, "DWORD": 32, "UINT": 32,
	"u64": 64, "s64": 64, "__u64": 64, "__s64": 64, "UINT64": 64, "INT64": 64, "QWORD": 64, "ULONGLONG": 64,
	"DWORD64": 64, "ULONG64": 64,
	"UINTN": 0, "INTN": 0, "ULONG_PTR": 0, "UINT_PTR": 0, "DWORD_PTR": 0, "SIZE_T": 0,
}

// qualifiers may appear among the specifiers without changing the width.
var qualifiers = setOf("const", "volatile", "static", "register", "extern", "inline", "restrict",
	"__restrict", "__restrict__", "auto", "__volatile__", "__const")

var intWords = setOf("signed", "unsigned", "short", "long", "int", "char", "_Bool", "bool")

func isTypeWord(w string) bool {
	_, e := exactTypes[w]
	_, h := conventionTypes[w]
	return e || h || qualifiers[w] || intWords[w]
}

// typeOf reads a list of specifier words plus a pointer flag. cc is the
// calling convention, which decides the width of long in 64-bit code (32
// under the Microsoft ABI, 64 under System V).
func typeOf(words []string, pointer bool, mode int, cc string) (cType, bool) {
	t := cType{name: strings.Join(words, " "), pointer: pointer}
	ptr := 32
	if mode == 64 {
		ptr = 64
	}
	if pointer {
		t.width = ptr
		t.name += " *"
		return t, true
	}
	longs, base := 0, ""
	for _, w := range words {
		switch {
		case qualifiers[w]:
		case w == "long":
			longs++
		case w == "signed" || w == "unsigned":
			if base == "" {
				base = "int"
			}
		case w == "int":
			if base == "" {
				base = "int"
			}
		case intWords[w]:
			base = w
		default:
			if base != "" && base != "int" {
				return cType{}, false
			}
			v, exact := exactTypes[w]
			if !exact {
				var ok bool
				if v, ok = conventionTypes[w]; !ok {
					return cType{}, false
				}
				t.heuristic = true
			}
			if v == 0 {
				v = ptr
			}
			t.width = v
			return t, len(words) > 0
		}
	}
	switch {
	case longs >= 2:
		t.width = 64
	case longs == 1:
		switch {
		case mode != 64 || cc == "ms":
			t.width = 32
		case cc == "sysv":
			t.width = 64
		default:
			return cType{}, false
		}
	case base == "":
		return cType{}, false
	default:
		t.width = exactTypes[base]
	}
	return t, true
}

// exprType infers the type of an operand expression. src is the C text,
// at is the offset of the asm statement (declarations are searched before
// it).
func exprType(src string, at int, expr string, mode int, cc string) (cType, bool) {
	e := strings.TrimSpace(expr)
	for strings.HasPrefix(e, "(") && matchingParen(e, 0) == len(e)-1 {
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	if e == "" {
		return cType{}, false
	}
	if e[0] == '(' {
		// A cast: (uint32_t)v.
		if j := matchingParen(e, 0); j > 0 && j < len(e)-1 {
			words, ptr := splitType(e[1:j])
			if len(words) > 0 && allTypeWords(words) {
				return typeOf(words, ptr, mode, cc)
			}
		}
		return cType{}, false
	}
	if e[0] == '&' && isIdent(strings.TrimSpace(e[1:])) {
		return typeOf([]string{"void"}, true, mode, cc)
	}
	if e[0] >= '0' && e[0] <= '9' {
		return literalType(e)
	}
	if isIdent(e) {
		return identType(src, at, e, mode, cc)
	}
	return cType{}, false
}

// literalType types an integer literal by its suffix. An unsuffixed
// literal is int when it fits, which covers the constants inline asm
// operands are given in practice; a bigger one is unknown.
func literalType(e string) (cType, bool) {
	body := strings.TrimRight(e, "uUlL")
	suffix := strings.ToLower(e[len(body):])
	v, err := strconv.ParseUint(body, 0, 64)
	if err != nil {
		return cType{}, false
	}
	t := cType{name: "integer literal " + e}
	switch strings.Count(suffix, "l") {
	case 0:
		if v > 0x7fffffff && !strings.Contains(suffix, "u") || v > 0xffffffff {
			return cType{}, false
		}
		t.width = 32
	case 2:
		t.width = 64
	default:
		return cType{}, false
	}
	return t, true
}

func splitType(s string) (words []string, pointer bool) {
	return strings.Fields(strings.ReplaceAll(s, "*", " ")), strings.Contains(s, "*")
}

func allTypeWords(words []string) bool {
	for _, w := range words {
		if !isTypeWord(w) {
			return false
		}
	}
	return true
}

func matchingParen(s string, open int) int {
	d := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			d++
		case ')':
			d--
			if d == 0 {
				return i
			}
		}
	}
	return -1
}

// identType finds the declarations of name in the function enclosing at
// and agrees on one type, or gives up.
func identType(src string, at int, name string, mode int, cc string) (cType, bool) {
	code := blankComments(src[:min(at, len(src))])
	from := functionStart(code)
	var found []cType
	for i := from; i < len(code); {
		k := strings.Index(code[i:], name)
		if k < 0 {
			break
		}
		p := i + k
		i = p + len(name)
		if p > 0 && isIdentByte(code[p-1]) || p+len(name) < len(code) && isIdentByte(code[p+len(name)]) {
			continue
		}
		t, decl, ok := declarationAt(code, from, p, name, mode, cc)
		if !decl {
			continue
		}
		if !ok {
			return cType{}, false
		}
		found = append(found, t)
	}
	if len(found) == 0 {
		return cType{}, false
	}
	for _, t := range found[1:] {
		if t.width != found[0].width || t.pointer != found[0].pointer {
			return cType{}, false
		}
	}
	return found[0], true
}

// declarationAt decides whether the identifier at p is being declared. decl
// reports a declaration (or something that may be one); ok is false when
// it is one whose type is unknown, which makes the whole lookup unknown.
func declarationAt(code string, from, p int, name string, mode int, cc string) (t cType, decl, ok bool) {
	after := skipSpace(code, p+len(name))
	if after >= len(code) || !strings.ContainsRune(";,=)[", rune(code[after])) {
		return cType{}, false, false
	}
	if code[after] == '[' {
		// An array decays to a pointer as an operand; its element type
		// says nothing useful. Treat it as a declaration of unknown type.
		return cType{}, true, false
	}
	q := p
	stars := 0
	for q > from && (code[q-1] == '*' || isSpace(code[q-1])) {
		if code[q-1] == '*' {
			stars++
		}
		q--
	}
	if q <= from {
		return cType{}, false, false
	}
	switch c := code[q-1]; {
	case isIdentByte(c):
		ws := q
		for ws > from && isIdentByte(code[ws-1]) {
			ws--
		}
		w := code[ws:q]
		if !isTypeWord(w) {
			// "MYTYPE x;" at a declaration start is a declaration of a
			// type this file does not know; anything else ("return x;")
			// is an expression.
			b := skipSpaceBack(code, from, ws)
			if b <= from || strings.ContainsRune(";{}(,", rune(code[b-1])) {
				return cType{}, true, false
			}
			return cType{}, false, false
		}
		words := []string{w}
		for {
			e := skipSpaceBack(code, from, ws)
			s := e
			for s > from && isIdentByte(code[s-1]) {
				s--
			}
			if s == e || !isTypeWord(code[s:e]) {
				break
			}
			words = append([]string{code[s:e]}, words...)
			ws = s
		}
		t, ok := typeOf(words, stars > 0, mode, cc)
		return t, true, ok
	case c == ',':
		return listDeclaration(code, from, q-1, name, stars, mode, cc)
	}
	return cType{}, false, false
}

// listDeclaration handles "unsigned int a = 0, b = 0;": the type is at the
// start of the declaration, before the first declarator.
func listDeclaration(code string, from, comma int, name string, stars int, mode int, cc string) (cType, bool, bool) {
	depth := 0
	s := comma
	for s > from {
		c := code[s-1]
		if c == ')' {
			depth++
		} else if c == '(' {
			if depth == 0 {
				break
			}
			depth--
		} else if depth == 0 && (c == ';' || c == '{' || c == '}') {
			break
		}
		s--
	}
	if s > from && code[s-1] == '(' {
		// A parameter list or a call: each parameter carries its own type.
		return cType{}, false, false
	}
	var words []string
	i := skipSpace(code, s)
	for i < comma {
		j := i
		for j < comma && isIdentByte(code[j]) {
			j++
		}
		if j == i || !isTypeWord(code[i:j]) {
			break
		}
		words = append(words, code[i:j])
		i = skipSpace(code, j)
	}
	if len(words) == 0 || i >= comma || !(isIdentByte(code[i]) || code[i] == '*') {
		return cType{}, false, false
	}
	t, ok := typeOf(words, stars > 0, mode, cc)
	return t, true, ok
}

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

func skipSpaceBack(s string, from, i int) int {
	for i > from && isSpace(s[i-1]) {
		i--
	}
	return i
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// functionStart returns the offset where the search for declarations
// begins: the parameter list of the function whose body encloses the end
// of code, or 0 when the end is at file scope.
func functionStart(code string) int {
	depth, open := 0, -1
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '{':
			if depth == 0 {
				open = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	if depth == 0 || open < 0 {
		return 0
	}
	// Step back over the parameter list.
	p := skipSpaceBack(code, 0, open)
	if p > 0 && code[p-1] == ')' {
		d := 0
		for q := p - 1; q >= 0; q-- {
			switch code[q] {
			case ')':
				d++
			case '(':
				d--
				if d == 0 {
					return q
				}
			}
		}
	}
	return open
}

// blankComments replaces comments, string and character literals and
// preprocessor lines with spaces, keeping every offset, so the
// declaration search never matches text that is not code.
func blankComments(src string) string {
	b := []byte(src)
	blank := func(from, to int) {
		for k := from; k < to && k < len(b); k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	lineStart := true
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '\n':
			lineStart = true
			i++
			continue
		case lineStart && c == '#':
			j := i
			for j < len(b) && !(b[j] == '\n' && (j == 0 || b[j-1] != '\\')) {
				j++
			}
			blank(i, j)
			i = j
			continue
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
			continue
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			end := len(b)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			blank(i, end)
			i = end
			continue
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(b) && b[j] != c && b[j] != '\n' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			blank(i, min(j+1, len(b)))
			i = j + 1
			continue
		}
		if !isSpace(c) {
			lineStart = false
		}
		i++
	}
	return string(b)
}
