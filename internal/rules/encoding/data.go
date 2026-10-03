package encoding

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/constexpr"
)

// dataWidths are the fixed-size data directives and their widths in bits
// (on x86, .word and .short are 16 bits and .int is 32).
var dataWidths = map[string]int{
	".byte": 8, ".1byte": 8,
	".word": 16, ".short": 16, ".hword": 16, ".value": 16, ".2byte": 16,
	".long": 32, ".int": 32, ".4byte": 32,
}

// data reports data directive values that do not fit their width. GAS
// truncates them with a warning ("value 0x12c truncated to 0x2c") or, for
// small negative values, silently: either way the bytes are not the value
// written. Only values that evaluate to a constant are judged; labels and
// .set symbols stay unknown.
func (a *pass) data(s *asm.Stmt) {
	w, ok := dataWidths[s.Name]
	if !ok || len(s.Args) == 0 {
		return
	}
	src := a.prog.Doc.Text
	if s.Start < 0 || s.End > len(src) {
		return
	}
	lo, hi := -(int64(1) << (w - 1)), int64(1)<<w-1
	text := src[s.Start:s.End]
	// Arguments are searched for in order after the directive name, which
	// places each one even when the same text repeats.
	cur := strings.Index(strings.ToLower(text), s.Name)
	if cur < 0 {
		return
	}
	cur += len(s.Name)
	for _, arg := range s.Args {
		at := strings.Index(text[cur:], arg)
		if at < 0 {
			return
		}
		start := s.Start + cur + at
		cur += at + len(arg)
		v, ok := a.constValue(arg)
		if !ok || (v >= lo && v <= hi) {
			continue
		}
		trunc := uint64(v) & (uint64(1)<<w - 1)
		how := "GAS truncates it"
		if v < 0 {
			how = "GAS stores it without a warning"
		}
		a.add(CodeDataRange, start, start+len(arg), fmt.Sprintf("%s value %s does not fit in %d bits (%s-%s); %s to 0x%x",
			s.Name, hex(v), w, hex(lo), hex(hi), how, trunc))
	}
}

// constValue evaluates a constant expression the way cpp and GAS would:
// cpp macros are substituted as text (no parentheses added), and the result
// is evaluated only when GAS's operator precedence and C's agree on it.
// Anything else, including character constants and symbols, is unknown.
func (a *pass) constValue(expr string) (int64, bool) {
	if expr == "" || strings.ContainsAny(expr, "'\"\\") {
		return 0, false
	}
	x, ok := a.expand(expr, 0)
	if !ok || !precedenceSafe(x) {
		return 0, false
	}
	v, err := constexpr.Eval(x, nil)
	return v, err == nil
}

// expand substitutes cpp object-like macros in expr, recursively.
func (a *pass) expand(expr string, depth int) (string, bool) {
	if depth > 8 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(expr); {
		c := expr[i]
		switch {
		case c >= '0' && c <= '9':
			j := i
			for j < len(expr) && isWordByte(expr[j]) {
				j++
			}
			b.WriteString(expr[i:j])
			i = j
		case isWordByte(c):
			j := i
			for j < len(expr) && isWordByte(expr[j]) {
				j++
			}
			word := expr[i:j]
			if body, ok := a.prog.Macros[word]; ok {
				x, ok := a.expand(body, depth+1)
				if !ok {
					return "", false
				}
				b.WriteString(" " + x + " ")
			} else {
				b.WriteString(word)
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), true
}

func isWordByte(c byte) bool {
	return c == '_' || c == '.' || c == '$' || (c|0x20 >= 'a' && c|0x20 <= 'z') || (c >= '0' && c <= '9')
}

// opClass groups binary operators by how GAS and C order them.
type opClass uint8

const (
	opAdd   opClass = 1 << iota // + -
	opMul                       // * / %
	opShift                     // << >>
	opAnd                       // &
	opOr                        // |
	opXor                       // ^
)

// precedenceSafe reports whether an expression evaluates the same under
// GAS's operator precedence as under C's, which is what constexpr
// implements. GAS ranks * / % << >> together above | & ^, and those above
// + -; C ranks * / % above + -, above << >>, above & ^ |. Mixing operators
// whose order differs ("1 + 2 << 3", "a | b & c", "a << b * c") gives a
// different number, and a range check on the C number would be a false
// report, so such expressions are not judged.
func precedenceSafe(expr string) bool {
	var seen opClass
	prevOperand := false
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		var cl opClass
		switch c {
		case ' ', '\t':
			continue
		case '+', '-':
			if !prevOperand {
				continue // unary
			}
			cl = opAdd
		case '*', '/', '%':
			cl = opMul
		case '<', '>':
			if i+1 < len(expr) && expr[i+1] == c {
				i++
				cl = opShift
			} else {
				return false // a comparison: leave it alone
			}
		case '&':
			cl = opAnd
		case '|':
			cl = opOr
		case '^':
			cl = opXor
		case '!', '=':
			return false
		case '(':
			prevOperand = false
			continue
		default:
			prevOperand = true
			continue
		}
		seen |= cl
		prevOperand = false
	}
	bit := func(m opClass) bool { return seen&m != 0 }
	switch {
	case bit(opAdd) && bit(opShift|opAnd|opOr|opXor):
		return false
	case bit(opMul) && bit(opShift):
		return false
	case bits(seen&(opAnd|opOr|opXor)) > 1:
		return false
	}
	return true
}

func bits(c opClass) int {
	n := 0
	for ; c != 0; c &= c - 1 {
		n++
	}
	return n
}
