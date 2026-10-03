package asm

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
)

// evalAtom evaluates a single literal or name without the expression
// machinery: almost every displacement and immediate in real code is one
// number or one macro, and constexpr allocates a token slice per call.
func (p *parser) evalAtom(s string) Value {
	if s == "" {
		return Value{}
	}
	switch c := s[0]; {
	case isDigit(c):
		if v, ok := parseNumber(s); ok {
			return Value{Known: true, V: v}
		}
		return Value{}
	case c == '\'':
		if v, ok := parseChar(s); ok {
			return Value{Known: true, V: v}
		}
		return Value{}
	}
	body, ok := p.lookup(s)
	if !ok {
		return Value{}
	}
	if v, ok := parseNumber(strings.TrimSpace(body)); ok {
		return Value{Known: true, V: v}
	}
	// Bodies naming other macros recurse through here; a cycle
	// (A defined as B, B as A) must end as unknown, as in constexpr.
	if p.evalDepth >= constexpr.MaxDepth {
		return Value{}
	}
	p.evalDepth++
	v := p.evalText(body)
	p.evalDepth--
	return v
}

// evalText evaluates a GAS expression. Symbols other than known macros and
// followed assignments make it unknown, which is the point: a label's
// address is not known until link time.
func (p *parser) evalText(s string) Value {
	// Token bounds already exclude trailing blanks except the one in a
	// space constant ("' "), which trimming would destroy.
	s = strings.TrimLeft(s, " \t")
	if strings.TrimRight(s, " \t") != s && !strings.Contains(s, "'") {
		s = strings.TrimRight(s, " \t")
	}
	if s == "" {
		return Value{}
	}
	if isAtom(s) {
		return p.evalAtom(s)
	}
	if s[0] == '-' && isAtom(s[1:]) {
		v := p.evalAtom(s[1:])
		v.V = -v.V
		return v
	}
	if strings.IndexByte(s, '\'') >= 0 {
		var ok bool
		if s, ok = normalizeChars(s); !ok {
			return Value{}
		}
	}
	if !p.atomsResolve(s) {
		return Value{}
	}
	v, err := constexpr.Eval(s, p.lookupFn)
	if err != nil {
		return Value{}
	}
	return Value{Known: true, V: v}
}

// atomsResolve reports whether every name and number in s can be
// evaluated. Most non-constant operands are label arithmetic
// ("msg - _start"); turning those away here saves constexpr building an
// error for each.
func (p *parser) atomsResolve(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if !isLetter(c) && !isDigit(c) && c != '_' && c != '.' && c != '$' {
			i++
			continue
		}
		j := i
		for j < len(s) && (isLetter(s[j]) || isDigit(s[j]) || s[j] == '_' || s[j] == '.' || s[j] == '$') {
			j++
		}
		w := s[i:j]
		i = j
		if isDigit(c) {
			// C suffixes reach here from macro bodies (0x10UL).
			if _, ok := parseNumber(strings.TrimRight(w, "uUlL")); !ok {
				return false
			}
			continue
		}
		if _, ok := p.lookup(w); !ok {
			return false
		}
	}
	return true
}

// isAtom reports a lone number or identifier, which evalAtom handles.
func isAtom(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isLetter(c) && !isDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

// parseNumber reads a GAS integer: decimal, 0x hex, 0b binary, leading-zero
// octal. Local label references (1f, 2b), floating forms and values that
// overflow 64 bits fail. It is written out rather than calling
// strconv.ParseUint because a failure there allocates an error, and every
// "jmp 1f" asks.
func parseNumber(s string) (int64, bool) {
	base, d := uint64(10), s
	switch {
	case len(s) > 2 && s[0] == '0' && (s[1]|0x20) == 'x':
		base, d = 16, s[2:]
	case len(s) > 2 && s[0] == '0' && (s[1]|0x20) == 'b':
		base, d = 2, s[2:]
	case len(s) > 1 && s[0] == '0':
		base, d = 8, s[1:]
	}
	if d == "" {
		return 0, false
	}
	var u uint64
	for i := 0; i < len(d); i++ {
		c := d[i]
		var v uint64
		switch {
		case isDigit(c):
			v = uint64(c - '0')
		case (c|0x20) >= 'a' && (c|0x20) <= 'f':
			v = uint64(c|0x20-'a') + 10
		default:
			return 0, false
		}
		if v >= base || u > (^uint64(0)-v)/base {
			return 0, false
		}
		u = u*base + v
	}
	return int64(u), true
}

// parseChar reads a GAS character constant: 'c with an optional closing
// quote (GAS needs none; C habit adds one), and the escapes GAS accepts.
func parseChar(s string) (int64, bool) {
	body := s[1:]
	if len(body) >= 2 && body[len(body)-1] == '\'' {
		body = body[:len(body)-1]
	}
	if body == "" {
		return 0, false
	}
	if body[0] != '\\' {
		r, size := utf8.DecodeRuneInString(body)
		if size != len(body) || r == utf8.RuneError {
			return 0, false
		}
		return int64(r), true
	}
	e := body[1:]
	if e == "" {
		return 0, false
	}
	switch e[0] {
	case 'x', 'X':
		if len(e) < 2 {
			return 0, false
		}
		u, err := strconv.ParseUint(e[1:], 16, 64)
		return int64(u), err == nil
	case '0', '1', '2', '3', '4', '5', '6', '7':
		if len(e) > 3 {
			return 0, false
		}
		u, err := strconv.ParseUint(e, 8, 64)
		return int64(u), err == nil
	}
	if len(e) != 1 {
		return 0, false
	}
	switch e[0] {
	case 'b':
		return 8, true
	case 'f':
		return 12, true
	case 'n':
		return 10, true
	case 'r':
		return 13, true
	case 't':
		return 9, true
	case 'v':
		return 11, true
	case 'a':
		return 7, true
	case '\\', '\'', '"':
		return int64(e[0]), true
	}
	return 0, false
}

// normalizeChars rewrites every character constant in s as a decimal
// number, since constexpr wants C's closed 'c' form and GAS code often
// writes 'c. ok is false for a constant that does not parse.
func normalizeChars(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := charEnd(s, i)
		v, ok := parseChar(s[i:j])
		if !ok {
			return "", false
		}
		b.WriteString(strconv.FormatInt(v, 10))
		i = j
	}
	return b.String(), true
}

// charEnd mirrors the lexer's charLit: the end of the constant at from.
func charEnd(s string, from int) int {
	j := from + 1
	if j >= len(s) {
		return j
	}
	if s[j] == '\\' {
		j++
		if j < len(s) {
			j++
			for j < len(s) && j-from < 6 && isHexDigit(s[j]) {
				j++
			}
		}
	} else {
		_, size := utf8.DecodeRuneInString(s[j:])
		j += size
	}
	if j < len(s) && s[j] == '\'' {
		j++
	}
	return j
}

func isHexDigit(c byte) bool { return isDigit(c) || ((c|0x20) >= 'a' && (c|0x20) <= 'f') }

func formatInt(v int64) string { return strconv.FormatInt(v, 10) }
