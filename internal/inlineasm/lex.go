package inlineasm

// A deliberately small C tokenizer. It only has to be good enough to find asm
// statements and walk their operand lists, so it never classifies keywords or
// evaluates anything. What it must get right is what is NOT code: comments,
// string and character literals, and preprocessor lines. A stray quote or
// "asm" inside any of those must never start a statement, and an unterminated
// literal must not swallow the rest of the file (the user is usually typing).

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tChar
	tNumber
	tPunct
)

type token struct {
	kind  tokKind
	start int // byte offset of the first byte (including any encoding prefix)
	end   int // one past the last byte (including the closing quote)

	// String literals only. contentStart..contentEnd is the text between the
	// quotes (or between the raw delimiters); prefix is the encoding prefix
	// ("", "L", "u8", "R", ...). unterminated is set when the literal ran into
	// a newline or the end of the input before its closing quote.
	prefix       string
	contentStart int
	contentEnd   int
	raw          bool
	unterminated bool
}

func (t token) text(src string) string { return src[t.start:t.end] }

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// lexC tokenizes the whole source. Lexing everything up front (rather than
// searching for "asm" and lexing around it) is what makes the context checks
// sound: whether a given "asm" is inside a comment or a string can only be
// known by scanning from the top.
func lexC(src string) []token {
	var toks []token
	lineStart := true // only whitespace and comments since the last newline
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '\n':
			lineStart = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
			continue
		case c == '\\' && i+1 < n && (src[i+1] == '\n' || src[i+1] == '\r'):
			// A line splice outside any literal; treat as whitespace and do
			// not reset lineStart, since the logical line continues.
			i = skipNewline(src, i+1)
			continue
		case c == '/' && i+1 < n && src[i+1] == '/':
			i = skipLineComment(src, i)
			continue
		case c == '/' && i+1 < n && src[i+1] == '*':
			i = skipBlockComment(src, i)
			continue
		case c == '#' && lineStart:
			i = skipDirective(src, i)
			continue
		}
		lineStart = false
		switch {
		case c == '"':
			t := lexString(src, i, i, "")
			toks = append(toks, t)
			i = t.end
		case c == '\'':
			end := lexQuoted(src, i+1, '\'')
			toks = append(toks, token{kind: tChar, start: i, end: end})
			i = end
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(src[i+1])):
			end := lexNumber(src, i)
			toks = append(toks, token{kind: tNumber, start: i, end: end})
			i = end
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentChar(src[j]) {
				j++
			}
			word := src[i:j]
			if j < n && (src[j] == '"' || src[j] == '\'') && isEncodingPrefix(word) {
				if src[j] == '\'' {
					end := lexQuoted(src, j+1, '\'')
					toks = append(toks, token{kind: tChar, start: i, end: end})
					i = end
					continue
				}
				t := lexString(src, i, j, word)
				toks = append(toks, t)
				i = t.end
				continue
			}
			toks = append(toks, token{kind: tIdent, start: i, end: j})
			i = j
		case c == '-' && i+1 < n && src[i+1] == '>':
			toks = append(toks, token{kind: tPunct, start: i, end: i + 2})
			i += 2
		default:
			toks = append(toks, token{kind: tPunct, start: i, end: i + 1})
			i++
		}
	}
	return toks
}

func isEncodingPrefix(w string) bool {
	switch w {
	case "L", "u", "U", "u8", "R", "LR", "uR", "UR", "u8R":
		return true
	}
	return false
}

// skipNewline steps over one line terminator (\n, \r\n or a lone \r) at i.
func skipNewline(src string, i int) int {
	if i < len(src) && src[i] == '\r' {
		i++
	}
	if i < len(src) && src[i] == '\n' {
		i++
	}
	return i
}

// skipLineComment returns the offset of the newline that ends the comment. A
// backslash before the newline continues the comment onto the next line
// (translation phase 2 runs before comments are removed), which is a classic
// way for a "// note \" to eat a following asm statement in real compilers.
func skipLineComment(src string, i int) int {
	for i < len(src) {
		switch src[i] {
		case '\\':
			if i+1 < len(src) && (src[i+1] == '\n' || src[i+1] == '\r') {
				i = skipNewline(src, i+1)
				continue
			}
		case '\n':
			return i
		}
		i++
	}
	return i
}

func skipBlockComment(src string, i int) int {
	for j := i + 2; j+1 < len(src); j++ {
		if src[j] == '*' && src[j+1] == '/' {
			return j + 2
		}
	}
	return len(src)
}

// skipDirective skips a preprocessor line including its continuations and any
// block comment that starts on it and spills onto later lines. asm written
// inside a #define body is deliberately not extracted: its text is only a
// fragment until the macro is expanded somewhere, and offsets inside it would
// not correspond to any one use.
func skipDirective(src string, i int) int {
	n := len(src)
	for i < n {
		switch c := src[i]; {
		case c == '\n':
			return i
		case c == '\\' && i+1 < n && (src[i+1] == '\n' || src[i+1] == '\r'):
			i = skipNewline(src, i+1)
		case c == '/' && i+1 < n && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '/' && i+1 < n && src[i+1] == '/':
			return skipLineComment(src, i)
		case c == '"' || c == '\'':
			// Quotes in directives (#include "x.h", #error don't) are not
			// always balanced, so a literal here may only run to end of line.
			i = lexQuoted(src, i+1, c)
		default:
			i++
		}
	}
	return i
}

// lexQuoted scans a quoted literal body starting just after the opening quote
// and returns the offset one past the closing quote, or the offset of the
// newline / end of input when unterminated. Backslash-newline splices keep
// the literal going.
func lexQuoted(src string, i int, q byte) int {
	n := len(src)
	for i < n {
		switch src[i] {
		case '\\':
			if i+1 < n && (src[i+1] == '\n' || src[i+1] == '\r') {
				i = skipNewline(src, i+1)
				continue
			}
			i += 2
			continue
		case '\n':
			return i
		case q:
			return i + 1
		}
		i++
	}
	return n
}

// lexString lexes a string literal whose encoding prefix starts at start and
// whose opening quote is at q.
func lexString(src string, start, q int, prefix string) token {
	t := token{kind: tString, start: start, prefix: prefix}
	if len(prefix) > 0 && prefix[len(prefix)-1] == 'R' {
		return lexRawString(src, t, q)
	}
	end := lexQuoted(src, q+1, '"')
	t.end = end
	t.contentStart = q + 1
	if end > q+1 && end <= len(src) && src[end-1] == '"' && !endsInEscape(src, q+1, end-1) {
		t.contentEnd = end - 1
	} else {
		t.contentEnd = end
		t.unterminated = true
	}
	return t
}

// endsInEscape reports whether the quote at q is escaped. lexQuoted already
// steps over escapes, so a quote it stops after is never escaped; this only
// guards the case where it stopped at end of input right after a backslash.
func endsInEscape(src string, from, q int) bool {
	bs := 0
	for k := q - 1; k >= from && src[k] == '\\'; k-- {
		bs++
	}
	return bs%2 == 1
}

// lexRawString handles C++ R"delim( ... )delim". Raw strings can legitimately
// span lines, so they run to the closing delimiter or end of input.
func lexRawString(src string, t token, q int) token {
	t.raw = true
	j := q + 1
	for j < len(src) && j-q-1 <= 16 && src[j] != '(' && src[j] != '"' && src[j] != '\n' && src[j] != ' ' {
		j++
	}
	if j >= len(src) || src[j] != '(' {
		// Malformed delimiter; fall back to an ordinary literal so the rest
		// of the file still lexes sensibly.
		t.raw = false
		end := lexQuoted(src, q+1, '"')
		t.end, t.contentStart, t.contentEnd, t.unterminated = end, q+1, end, true
		return t
	}
	closing := ")" + src[q+1:j] + "\""
	t.contentStart = j + 1
	for k := j + 1; k+len(closing) <= len(src); k++ {
		if src[k:k+len(closing)] == closing {
			t.contentEnd = k
			t.end = k + len(closing)
			return t
		}
	}
	t.contentEnd, t.end, t.unterminated = len(src), len(src), true
	return t
}

// lexNumber consumes a preprocessing number: digits, letters, dots, and a
// sign directly after an exponent letter. Getting this right matters only so
// that "1e+asm" style junk never produces a bogus identifier token.
func lexNumber(src string, i int) int {
	j := i + 1
	for j < len(src) {
		c := src[j]
		if isIdentChar(c) || c == '.' || c == '\'' {
			j++
			continue
		}
		if (c == '+' || c == '-') && strchr("eEpP", src[j-1]) {
			j++
			continue
		}
		break
	}
	return j
}

func strchr(set string, c byte) bool {
	for k := 0; k < len(set); k++ {
		if set[k] == c {
			return true
		}
	}
	return false
}
