package inlineasm

import "unicode/utf8"

// The template goes through the same two stages the compiler applies:
// translation phases (escape sequences, then concatenation of adjacent
// literals) produce one byte string, and for extended asm GCC's
// output_asm_insn then rewrites %-sequences and dialect braces. Each stage
// carries a parallel slice of C source offsets, one per output byte plus a
// final entry for the end, so any template offset maps back to the exact
// source byte that produced it.

// OperandRef is a reference to an operand inside an extended asm template:
// %0, %k1, %[name], %l[label], %c[name] and so on.
type OperandRef struct {
	Offset   int    // byte offset of the '%' in Stmt.Asm
	Len      int    // bytes in Stmt.Asm, e.g. 3 for "%k0", 7 for "%[dst]"
	Index    int    // operand number (outputs, then inputs, then goto labels); -1 when a name does not resolve
	Name     string // symbolic name for %[name] forms, "" for numeric ones
	Modifier byte   // operand modifier letter ('k', 'b', 'l', ...), 0 when none
}

// strBuf accumulates bytes with the C offset each one came from.
type strBuf struct {
	b    []byte
	offs []int
}

func (s *strBuf) add(c byte, off int) {
	s.b = append(s.b, c)
	s.offs = append(s.offs, off)
}

// addLiteral appends the unescaped contents of one string literal token.
// Every byte produced by an escape sequence maps to the backslash, which is
// where a cursor inside "\n" most usefully lands.
func (s *strBuf) addLiteral(src string, t token) {
	if t.raw {
		for i := t.contentStart; i < t.contentEnd; i++ {
			s.add(src[i], i)
		}
		return
	}
	i, end := t.contentStart, t.contentEnd
	for i < end {
		c := src[i]
		if c != '\\' || i+1 >= end {
			s.add(c, i)
			i++
			continue
		}
		bs := i
		e := src[i+1]
		i += 2
		switch e {
		case '\n', '\r':
			// Line splice inside a literal produces nothing.
			i = skipNewline(src, i-1)
		case 'n':
			s.add('\n', bs)
		case 't':
			s.add('\t', bs)
		case 'r':
			s.add('\r', bs)
		case 'a':
			s.add(7, bs)
		case 'b':
			s.add(8, bs)
		case 'f':
			s.add(12, bs)
		case 'v':
			s.add(11, bs)
		case 'e', 'E':
			s.add(27, bs) // GNU extension
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := int(e - '0')
			for k := 0; k < 2 && i < end && src[i] >= '0' && src[i] <= '7'; k++ {
				v = v*8 + int(src[i]-'0')
				i++
			}
			s.add(byte(v), bs)
		case 'x':
			v, any := 0, false
			for i < end && hexVal(src[i]) >= 0 {
				v = (v*16 + hexVal(src[i])) & 0xff
				i++
				any = true
			}
			if !any {
				s.add('x', bs)
			} else {
				s.add(byte(v), bs)
			}
		case 'u', 'U':
			want := 4
			if e == 'U' {
				want = 8
			}
			v, got := 0, 0
			for got < want && i < end && hexVal(src[i]) >= 0 {
				v = v*16 + hexVal(src[i])
				i++
				got++
			}
			if got == 0 || v > utf8.MaxRune {
				s.add(e, bs)
				break
			}
			var tmp [utf8.UTFMax]byte
			for _, b := range tmp[:utf8.EncodeRune(tmp[:], rune(v))] {
				s.add(b, bs)
			}
		default:
			// \\ \" \' \? and unknown escapes, which GCC accepts with a
			// warning and replaces by the character itself.
			s.add(e, bs)
		}
	}
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// expand applies output_asm_insn's rewriting for extended asm: %% %{ %| %}
// become the literal character, {att|intel} keeps the first (AT&T)
// alternative, and operand references are kept verbatim but recorded so
// hover can find them. Doing this before the reference scan matters:
// "%%k0" is the AVX-512 mask register k0, not operand 0 with modifier k, and
// the two are indistinguishable once %% has collapsed.
func expand(in strBuf) (strBuf, []OperandRef) {
	var out strBuf
	var refs []OperandRef
	b := in.b
	dialect := false
	i := 0
	for i < len(b) {
		c := b[i]
		switch {
		case c == '%' && i+1 < len(b) && strchr("%{|}", b[i+1]):
			out.add(b[i+1], in.offs[i])
			i += 2
		case c == '%':
			if r, n := scanRef(b[i:]); n > 0 {
				r.Offset = len(out.b)
				refs = append(refs, r)
				for k := 0; k < n; k++ {
					out.add(b[i+k], in.offs[i+k])
				}
				i += n
				continue
			}
			out.add(c, in.offs[i])
			i++
		case c == '{' && !dialect:
			dialect = true
			i++
		case c == '|' && dialect:
			// Skip the other dialects up to the closing brace, stepping over
			// any character after '%' as GCC does.
			i++
			for i < len(b) {
				if b[i] == '%' && i+1 < len(b) {
					i += 2
					continue
				}
				i++
				if b[i-1] == '}' {
					break
				}
			}
			dialect = false
		case c == '}' && dialect:
			dialect = false
			i++
		default:
			out.add(c, in.offs[i])
			i++
		}
	}
	out.offs = append(out.offs, in.offs[len(in.b)])
	return out, refs
}

// scanRef parses an operand reference at the start of b (b[0] == '%') and
// returns it with its length, or n == 0 when this '%' is not one.
func scanRef(b []byte) (r OperandRef, n int) {
	j := 1
	if j+1 < len(b) && isLetter(b[j]) && (isDigit(b[j+1]) || b[j+1] == '[') {
		r.Modifier = b[j]
		j++
	}
	if j >= len(b) {
		return r, 0
	}
	switch {
	case isDigit(b[j]):
		v := 0
		for j < len(b) && isDigit(b[j]) {
			if v < 1<<20 {
				v = v*10 + int(b[j]-'0')
			}
			j++
		}
		r.Index = v
	case b[j] == '[':
		k := j + 1
		for k < len(b) && b[k] != ']' && b[k] != '\n' {
			k++
		}
		if k >= len(b) || b[k] != ']' {
			return r, 0
		}
		r.Name = trimSpace(string(b[j+1 : k]))
		r.Index = -1
		j = k + 1
	default:
		return r, 0
	}
	r.Len = j
	return r, j
}

func trimSpace(s string) string {
	a, z := 0, len(s)
	for a < z && isSpace(s[a]) {
		a++
	}
	for z > a && isSpace(s[z-1]) {
		z--
	}
	return s[a:z]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}
