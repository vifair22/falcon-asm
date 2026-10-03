package inlinec

import (
	"strconv"
	"strings"
)

// A C file often holds one asm statement per architecture, picked by
// "#if defined(__x86_64__)". The extractor sees all of them, and judging
// the 32-bit variant against the 64-bit target (or the reverse) produces
// findings about code that is never compiled. This is a tiny conditional
// evaluator that knows only the architecture macros the target mode
// decides; every other condition is unknown and keeps its branches active,
// so it can only ever make the rules quieter.

type tri int

const (
	triUnknown tri = iota
	triFalse
	triTrue
)

func (a tri) not() tri {
	switch a {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	}
	return triUnknown
}

func (a tri) and(b tri) tri {
	switch {
	case a == triFalse || b == triFalse:
		return triFalse
	case a == triTrue && b == triTrue:
		return triTrue
	}
	return triUnknown
}

func (a tri) or(b tri) tri {
	switch {
	case a == triTrue || b == triTrue:
		return triTrue
	case a == triFalse && b == triFalse:
		return triFalse
	}
	return triUnknown
}

// archDefined says whether the compiler defines an architecture macro for
// the mode: triUnknown for anything it cannot be sure of (MSVC's _M_X64,
// the plain "i386" that only GNU dialects define, every user macro).
func archDefined(name string, mode int) tri {
	switch name {
	case "__x86_64__", "__x86_64", "__amd64__", "__amd64":
		if mode == 64 {
			return triTrue
		}
		return triFalse
	case "__i386__", "__i386":
		if mode == 64 {
			return triFalse
		}
		return triTrue
	}
	if otherArch[name] {
		// Code for another architecture shares the file in portable
		// projects (an ARM "str" is not x86's store task register).
		return triFalse
	}
	return triUnknown
}

// otherArch are the predefined macros of non-x86 targets; none of them is
// defined when compiling for x86.
var otherArch = setOf(
	"__arm__", "__aarch64__", "__arm64__", "__thumb__", "__ARM_ARCH", "_M_ARM", "_M_ARM64",
	"__riscv", "__mips__", "__mips", "__powerpc__", "__powerpc64__", "__ppc__", "__PPC__", "__ppc64__",
	"__s390__", "__s390x__", "__sparc__", "__sparc", "__ia64__", "__alpha__", "__hppa__", "__m68k__",
	"__loongarch__", "__xtensa__", "__sh__", "__arc__", "__or1k__", "__nios2__", "__microblaze__",
	"__AVR__", "__MSP430__", "__wasm__", "__wasm32__", "__wasm64__",
)

type condFrame struct {
	taken tri // some earlier branch of the group was taken
	cur   tri // the branch being read
}

// inactiveRanges returns the byte ranges of src inside a conditional branch
// known not to be compiled for the mode.
func inactiveRanges(src string, mode int) [][2]int {
	var out [][2]int
	var stack []condFrame
	inactiveFrom := -1
	inComment := false
	for off := 0; off < len(src); {
		lineStart := off
		line, next := logicalLine(src, off)
		off = next
		wasComment := inComment
		code := stripComments(line, &inComment)
		if wasComment {
			continue
		}
		name, rest, ok := directive(code)
		if !ok {
			continue
		}
		before := frameInactive(stack)
		switch name {
		case "if":
			c := evalCond(rest, mode)
			stack = append(stack, condFrame{taken: c, cur: c})
		case "ifdef", "ifndef":
			c := archDefined(firstWord(rest), mode)
			if name == "ifndef" {
				c = c.not()
			}
			stack = append(stack, condFrame{taken: c, cur: c})
		case "elif", "elifdef", "elifndef", "else":
			if len(stack) == 0 {
				continue
			}
			f := &stack[len(stack)-1]
			var c tri
			switch name {
			case "else":
				c = triTrue
			case "elif":
				c = evalCond(rest, mode)
			default:
				c = archDefined(firstWord(rest), mode)
				if name == "elifndef" {
					c = c.not()
				}
			}
			f.cur = f.taken.not().and(c)
			f.taken = f.taken.or(c)
		case "endif":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			continue
		}
		after := frameInactive(stack)
		switch {
		case !before && after:
			inactiveFrom = lineStart
		case before && !after && inactiveFrom >= 0:
			out = append(out, [2]int{inactiveFrom, lineStart})
			inactiveFrom = -1
		}
	}
	if inactiveFrom >= 0 {
		out = append(out, [2]int{inactiveFrom, len(src)})
	}
	return out
}

func frameInactive(stack []condFrame) bool {
	for _, f := range stack {
		if f.cur == triFalse {
			return true
		}
	}
	return false
}

func inRanges(rs [][2]int, off int) bool {
	for _, r := range rs {
		if off >= r[0] && off < r[1] {
			return true
		}
	}
	return false
}

// logicalLine returns the line starting at off with backslash-newline
// continuations joined, and the offset of the next line.
func logicalLine(src string, off int) (string, int) {
	var b strings.Builder
	for off < len(src) {
		nl := strings.IndexByte(src[off:], '\n')
		if nl < 0 {
			b.WriteString(src[off:])
			return b.String(), len(src)
		}
		seg := strings.TrimSuffix(src[off:off+nl], "\r")
		off += nl + 1
		if strings.HasSuffix(seg, "\\") {
			b.WriteString(seg[:len(seg)-1])
			continue
		}
		b.WriteString(seg)
		break
	}
	return b.String(), off
}

// stripComments removes /* */ and // comments from one line, carrying an
// open block comment to the next line through inComment. String literals
// are not tracked: a directive line rarely holds one, and a misread there
// can only leave a branch active.
func stripComments(line string, inComment *bool) string {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if *inComment {
			if strings.HasPrefix(line[i:], "*/") {
				*inComment = false
				i++
			}
			continue
		}
		if strings.HasPrefix(line[i:], "/*") {
			*inComment = true
			i++
			b.WriteByte(' ')
			continue
		}
		if strings.HasPrefix(line[i:], "//") {
			break
		}
		b.WriteByte(line[i])
	}
	return b.String()
}

func directive(code string) (name, rest string, ok bool) {
	s := strings.TrimLeft(code, " \t")
	if !strings.HasPrefix(s, "#") {
		return "", "", false
	}
	s = strings.TrimLeft(s[1:], " \t")
	n := 0
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z') {
		n++
	}
	return s[:n], s[n:], true
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// evalCond evaluates a #if expression made of defined(), !, &&, || and
// parentheses over the architecture macros. Anything else (a comparison,
// arithmetic, a user macro) is unknown.
func evalCond(expr string, mode int) tri {
	p := condParser{toks: condTokens(expr), mode: mode}
	v := p.or()
	if p.bad || p.i != len(p.toks) {
		return triUnknown
	}
	return v
}

type condParser struct {
	toks []string
	i    int
	mode int
	bad  bool
}

func (p *condParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *condParser) or() tri {
	v := p.and()
	for p.peek() == "||" {
		p.i++
		v = v.or(p.and())
	}
	return v
}

func (p *condParser) and() tri {
	v := p.unary()
	for p.peek() == "&&" {
		p.i++
		v = v.and(p.unary())
	}
	return v
}

func (p *condParser) unary() tri {
	if p.peek() == "!" {
		p.i++
		return p.unary().not()
	}
	return p.primary()
}

func (p *condParser) primary() tri {
	t := p.peek()
	p.i++
	switch {
	case t == "(":
		v := p.or()
		if p.peek() != ")" {
			p.bad = true
		}
		p.i++
		return v
	case t == "defined":
		paren := p.peek() == "("
		if paren {
			p.i++
		}
		name := p.peek()
		p.i++
		if !isIdent(name) {
			p.bad = true
		}
		if paren {
			if p.peek() != ")" {
				p.bad = true
			}
			p.i++
		}
		return archDefined(name, p.mode)
	case t != "" && t[0] >= '0' && t[0] <= '9':
		v, err := strconv.ParseInt(strings.TrimRight(t, "uUlL"), 0, 64)
		if err != nil {
			return triUnknown
		}
		if v != 0 {
			return triTrue
		}
		return triFalse
	case isIdent(t):
		// A macro used as a value: the architecture macros expand to 1.
		return archDefined(t, p.mode)
	}
	p.bad = true
	return triUnknown
}

func isIdent(s string) bool {
	if s == "" || !(s[0] == '_' || s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// condTokens splits an expression into identifiers, numbers and the
// operators; any other character becomes a token of its own, which the
// parser rejects.
func condTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case strings.HasPrefix(s[i:], "&&") || strings.HasPrefix(s[i:], "||"):
			out = append(out, s[i:i+2])
			i += 2
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			j := i
			for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			out = append(out, s[i:j])
			i = j
		default:
			out = append(out, s[i:i+1])
			i++
		}
	}
	return out
}
