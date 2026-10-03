// Package constexpr evaluates the integer constant expressions that cpp macro
// bodies in assembly projects are made of: selector values, port numbers,
// bit masks built with shifts and ors, sums of other macros. It exists so a
// hover on SEL_CODE32 can say 0x08 instead of repeating "(GDT_CODE32 * 8)".
//
// The grammar is C's integer subset without assignment, comparison, the
// conditional operator or casts. Arithmetic is 64-bit two's complement, which
// is what gas does with the expanded result and close enough to cpp for every
// value a hover shows. Anything outside the subset is an error rather than a
// guess, so a hover never displays a number the toolchain would not produce.
package constexpr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxDepth bounds macro expansion. Real projects nest a handful of levels;
// a cycle (A defined as B, B as A) would otherwise recurse forever.
const MaxDepth = 32

// Errors returned by Eval. They are wrapped with the offending text.
var (
	ErrSyntax      = errors.New("not a constant expression")
	ErrUnknown     = errors.New("unknown identifier")
	ErrDivZero     = errors.New("division by zero")
	ErrTooDeep     = errors.New("macro expansion too deep")
	ErrUnsupported = errors.New("unsupported construct")
)

// Lookup resolves an identifier to its macro body; ok is false for a name
// that is not an object-like macro.
type Lookup func(name string) (body string, ok bool)

// Eval evaluates expr, expanding identifiers through lookup (which may be
// nil). Each identifier's body is evaluated as its own parenthesized
// expression, which matches cpp for every well-formed constant macro and
// avoids the classic precedence surprise of textual substitution only for
// bodies that were already broken.
func Eval(expr string, lookup Lookup) (int64, error) {
	return eval(expr, lookup, 0)
}

func eval(expr string, lookup Lookup, depth int) (int64, error) {
	if depth > MaxDepth {
		return 0, ErrTooDeep
	}
	toks, err := tokenize(expr)
	if err != nil {
		return 0, err
	}
	if len(toks) == 0 {
		return 0, fmt.Errorf("%w: empty", ErrSyntax)
	}
	p := parser{toks: toks, lookup: lookup, depth: depth}
	v, err := p.expr(0)
	if err != nil {
		return 0, err
	}
	if p.pos != len(p.toks) {
		return 0, fmt.Errorf("%w: unexpected %q", ErrSyntax, p.toks[p.pos].text)
	}
	return v, nil
}

type tokKind int

const (
	tNum tokKind = iota
	tIdent
	tOp
)

type token struct {
	kind tokKind
	text string
	val  int64
}

// twoChar are the operators that must be read before their one-character
// prefixes.
var twoChar = []string{"<<", ">>", "&&", "||", "==", "!=", "<=", ">="}

func tokenize(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\\':
			// A backslash here is a #define line continuation that
			// survived into the body.
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			v, err := parseNumber(s[i:j])
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tNum, text: s[i:j], val: v})
			i = j
		case c == '\'':
			v, n, err := parseChar(s[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tNum, text: s[i : i+n], val: v})
			i += n
		case isIdentByte(c):
			j := i
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			out = append(out, token{kind: tIdent, text: s[i:j]})
			i = j
		default:
			op := string(c)
			for _, two := range twoChar {
				if strings.HasPrefix(s[i:], two) {
					op = two
					break
				}
			}
			if !strings.Contains("+-*/%<>&|^~!()", op[:1]) || isComparison(op) {
				return nil, fmt.Errorf("%w: %q", ErrUnsupported, op)
			}
			out = append(out, token{kind: tOp, text: op})
			i += len(op)
		}
	}
	return out, nil
}

// isComparison flags the operators a constant macro body has no business
// holding; they are rejected rather than half-supported.
func isComparison(op string) bool {
	switch op {
	case "<", ">", "<=", ">=", "==", "!=", "&&", "||":
		return true
	}
	return false
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || (c|0x20) >= 'a' && (c|0x20) <= 'z'
}

// parseNumber reads a C integer literal: decimal, 0x hex, 0b binary,
// leading-zero octal, with any u/l suffix combination.
func parseNumber(lit string) (int64, error) {
	body := strings.TrimRight(lit, "uUlL")
	base := 10
	switch {
	case len(body) > 2 && (body[1] == 'x' || body[1] == 'X') && body[0] == '0':
		base, body = 16, body[2:]
	case len(body) > 2 && (body[1] == 'b' || body[1] == 'B') && body[0] == '0':
		base, body = 2, body[2:]
	case len(body) > 1 && body[0] == '0':
		base, body = 8, body[1:]
	}
	u, err := strconv.ParseUint(body, base, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: number %q", ErrSyntax, lit)
	}
	return int64(u), nil
}

// parseChar reads a character constant such as 'A' or '\n' and returns its
// value and the bytes consumed.
func parseChar(s string) (int64, int, error) {
	end := 1
	for end < len(s) && s[end] != '\'' {
		if s[end] == '\\' {
			end++
		}
		end++
	}
	if end >= len(s) {
		return 0, 0, fmt.Errorf("%w: unterminated character constant", ErrSyntax)
	}
	v, _, tail, err := strconv.UnquoteChar(s[1:end], '\'')
	if err != nil || tail != "" {
		return 0, 0, fmt.Errorf("%w: character constant %q", ErrSyntax, s[:end+1])
	}
	return int64(v), end + 1, nil
}

type parser struct {
	toks   []token
	pos    int
	lookup Lookup
	depth  int
}

// binPrec is C's precedence for the supported binary operators; higher
// binds tighter.
var binPrec = map[string]int{
	"|": 1, "^": 2, "&": 3,
	"<<": 4, ">>": 4,
	"+": 5, "-": 5,
	"*": 6, "/": 6, "%": 6,
}

// expr is precedence climbing over binPrec; every operator is left
// associative.
func (p *parser) expr(minPrec int) (int64, error) {
	lhs, err := p.unary()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.toks) {
		t := p.toks[p.pos]
		prec, ok := binPrec[t.text]
		if t.kind != tOp || !ok || prec < minPrec {
			break
		}
		p.pos++
		rhs, err := p.expr(prec + 1)
		if err != nil {
			return 0, err
		}
		if lhs, err = apply(t.text, lhs, rhs); err != nil {
			return 0, err
		}
	}
	return lhs, nil
}

func apply(op string, a, b int64) (int64, error) {
	switch op {
	case "|":
		return a | b, nil
	case "^":
		return a ^ b, nil
	case "&":
		return a & b, nil
	case "<<":
		return a << (uint64(b) & 63), nil
	case ">>":
		return a >> (uint64(b) & 63), nil
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "/":
		if b == 0 {
			return 0, ErrDivZero
		}
		return a / b, nil
	default: // "%"
		if b == 0 {
			return 0, ErrDivZero
		}
		return a % b, nil
	}
}

func (p *parser) unary() (int64, error) {
	if p.pos >= len(p.toks) {
		return 0, fmt.Errorf("%w: unexpected end", ErrSyntax)
	}
	t := p.toks[p.pos]
	p.pos++
	switch t.kind {
	case tNum:
		return t.val, nil
	case tIdent:
		if p.lookup == nil {
			return 0, fmt.Errorf("%w: %s", ErrUnknown, t.text)
		}
		body, ok := p.lookup(t.text)
		if !ok {
			return 0, fmt.Errorf("%w: %s", ErrUnknown, t.text)
		}
		return eval(body, p.lookup, p.depth+1)
	}
	switch t.text {
	case "(":
		v, err := p.expr(0)
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.toks) || p.toks[p.pos].text != ")" {
			return 0, fmt.Errorf("%w: missing )", ErrSyntax)
		}
		p.pos++
		return v, nil
	case "-", "+", "~", "!":
		v, err := p.unary()
		if err != nil {
			return 0, err
		}
		switch t.text {
		case "-":
			return -v, nil
		case "~":
			return ^v, nil
		case "!":
			if v == 0 {
				return 1, nil
			}
			return 0, nil
		}
		return v, nil
	}
	return 0, fmt.Errorf("%w: unexpected %q", ErrSyntax, t.text)
}
