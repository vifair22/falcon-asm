package lint

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// UnusedSuppressionCode is the rule reporting suppression comments that
// suppressed nothing. The runner implements it; the registered rule exists
// for its code, severity, configuration and documentation.
const UnusedSuppressionCode = "unused-suppression"

// IgnoreWithoutReasonCode is the rule reporting suppressions that do not
// say why. Like unused-suppression, the runner implements it.
const IgnoreWithoutReasonCode = "ignore-without-reason"

// SuppressionSyntax is the one-paragraph reference of the suppression
// comment syntax, shared by docs/RULES.md and the rule documentation so
// the two cannot drift apart.
const SuppressionSyntax = "Inside any comment (`/* */`, `#` or `//` in assembly; `//` or `/* */` in C, " +
	"for inline asm): `falcon-asm: ignore=<code>[,<code>...]: <reason>` silences findings that start " +
	"on the comment's line; `ignore-next=<code>: <reason>` the next line holding code (comment-only " +
	"lines between are skipped); `ignore-file=<code>: <reason>` the whole file; `ignore-begin=<code>: " +
	"<reason>` ... `ignore-end=<code>` every line between the two, inclusive (an unclosed begin runs " +
	"to the end of the file). `all` in place of a code matches every rule. The reason is optional: " +
	"everything after the colon that follows the code list is free text. Enable ignore-without-reason " +
	"to require one."

func init() {
	Register(&Rule{
		Code:    UnusedSuppressionCode,
		Section: 12,
		Default: Info,
		Langs:   LangAsm | LangInline | LangC,
		Doc: Doc{
			Summary: "A falcon-asm: ignore comment that suppresses nothing.",
			Why: "Suppressions outlive the findings they were written for: the code is fixed, " +
				"the rule improves, or the comment drifts to another line in an edit. A stale " +
				"suppression then hides the next real finding on that line without anyone " +
				"deciding it should. A code that names no rule (a typo) is reported the same way, " +
				"because it suppresses nothing either.",
			Bad:  "\t.code64\n\tpushq %rax /* falcon-asm: ignore=mode-register */",
			Good: "\t.code64\n\tpushq %rax",
		},
		Check: func(*Context) {},
	})
	Register(&Rule{
		Code:    IgnoreWithoutReasonCode,
		Section: 12,
		Default: Off,
		Langs:   LangAsm | LangInline | LangC,
		Doc: Doc{
			Summary: "A falcon-asm: ignore comment that does not say why (no `: reason` after its codes).",
			Why: "A suppression records a decision that a finding is wrong about this code, and the next " +
				"reader cannot tell a considered decision from a quick silencing unless the comment says " +
				"which it was. Write the reason after a colon: `falcon-asm: ignore=slow-loop: runs once at " +
				"boot`. ignore-end needs none, since its begin carries it. Off by default; turn it on per " +
				"project with `\"rules\": {\"ignore-without-reason\": \"info\"}` in .falcon-asm.json. " +
				SuppressionSyntax,
			Bad:  "\t.code64\n\tenter $32, $0 /* falcon-asm: ignore=slow-enter */",
			Good: "\t.code64\n\tenter $32, $0 /* falcon-asm: ignore=slow-enter: matches the frame the debugger expects */",
		},
		Check: func(*Context) {},
	})
}

// CommentStyle is how an inserted suppression comment is spelled.
type CommentStyle int

// Comment styles: /* */ for assembly (valid in .S and plain .s), // for C.
const (
	StyleAsm CommentStyle = iota
	StyleC
)

type dirKind int

const (
	dirIgnore dirKind = iota // this line
	dirNext                  // the next code line
	dirFile                  // the whole file
	dirBegin                 // from here to the matching end
	dirEnd
)

var dirKeys = map[string]dirKind{
	"ignore": dirIgnore, "ignore-next": dirNext, "ignore-file": dirFile,
	"ignore-begin": dirBegin, "ignore-end": dirEnd,
}

// directive is one code of one suppression item: "ignore=a,b" is two.
type directive struct {
	kind       dirKind
	code       string
	start, end int // byte offsets of the code in the document
	listEnd    int // end of the item's whole code list (where another code is appended)
	line       int // document line of the item
	target     int // dirNext: the line it applies to, -1 when there is none
	endLine    int // dirBegin: last line of the region (inclusive)
	matched    bool
	used       bool
	reason     bool // a ": text" follows the item's code list
}

// Suppressions are the suppression comments of one document, parsed once.
// Filter marks what it used, so Unused must come after it.
type Suppressions struct {
	doc   *docstore.Document
	style CommentStyle
	dirs  []*directive
}

const tag = "falcon-asm:"

// AsmSuppressions parses the suppression comments of an assembly document.
// toks must be lexer.Tokenize(doc.Text); nil tokenizes here.
func AsmSuppressions(doc *docstore.Document, toks []lexer.Token) *Suppressions {
	if toks == nil {
		toks = lexer.Tokenize(doc.Text)
	}
	s := &Suppressions{doc: doc, style: StyleAsm}
	var starts []int // lexer line starts, built only if a directive exists
	for i, t := range toks {
		if t.Kind != lexer.Comment || !containsFold(t.Text, tag) {
			continue
		}
		if starts == nil {
			starts = newlineStarts(doc.Text)
		}
		if t.Line >= len(starts) {
			continue
		}
		off := starts[t.Line] + t.Col
		first := len(s.dirs)
		s.parseComment(doc.Text[off:off+t.Len], off)
		// ignore-next: the first token on a later lexer line that is not a
		// comment is the code it guards.
		for _, d := range s.dirs[first:] {
			if d.kind != dirNext {
				continue
			}
			d.target = -1
			for _, u := range toks[i+1:] {
				if u.Kind != lexer.Comment && u.Line > t.Line && u.Line < len(starts) {
					d.target = doc.Position(starts[u.Line] + u.Col).Line
					break
				}
			}
		}
	}
	s.pairRegions()
	return s
}

// CSuppressions parses the suppression comments of a C document, so
// findings in its inline asm can be silenced from C comments.
func CSuppressions(doc *docstore.Document) *Suppressions {
	s := &Suppressions{doc: doc, style: StyleC}
	if !containsFold(doc.Text, tag) {
		return s
	}
	comments, code := scanC(doc.Text)
	for _, c := range comments {
		if !containsFold(doc.Text[c.start:c.end], tag) {
			continue
		}
		first := len(s.dirs)
		s.parseComment(doc.Text[c.start:c.end], c.start)
		for _, d := range s.dirs[first:] {
			if d.kind != dirNext {
				continue
			}
			d.target = -1
			// The next line holding code outside comments, after the line
			// the directive itself is on.
			for _, off := range code {
				if off > d.end {
					if l := doc.Position(off).Line; l > d.line {
						d.target = l
						break
					}
				}
			}
		}
	}
	s.pairRegions()
	return s
}

// parseComment reads every "falcon-asm: key=value ..." group in a comment
// starting at byte offset base. Items other than the ignore family
// (level=, cc=, ...) belong to other features and are skipped; the first
// word that is not key=value ends the group, so free text may follow.
func (s *Suppressions) parseComment(text string, base int) {
	for i := 0; i+len(tag) <= len(text); i++ {
		if !strings.EqualFold(text[i:i+len(tag)], tag) {
			continue
		}
		j := i + len(tag)
		for {
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			k := j
			for k < len(text) && (isLower(text[k]|0x20) || text[k] == '-') {
				k++
			}
			if k == j || k >= len(text) || text[k] != '=' {
				break
			}
			key := strings.ToLower(text[j:k])
			j = k + 1
			kind, isIgnore := dirKeys[key]
			var item []*directive
			for first := true; ; first = false {
				// "ignore=a, b": a space is allowed after a comma only, so
				// "ignore=a because" still ends the list at "a".
				for !first && j < len(text) && (text[j] == ' ' || text[j] == '\t') {
					j++
				}
				c := j
				for c < len(text) && isCodeChar(text[c]) {
					c++
				}
				if c > j && isIgnore {
					item = append(item, &directive{
						kind: kind, code: strings.ToLower(text[j:c]),
						start: base + j, end: base + c, line: s.doc.Position(base + j).Line, target: -1,
					})
				}
				j = c
				if j < len(text) && text[j] == ',' {
					j++
					continue
				}
				break
			}
			reason := hasReason(text[j:])
			for _, d := range item {
				d.listEnd = base + j
				d.reason = reason
			}
			s.dirs = append(s.dirs, item...)
		}
		i = j - 1
	}
}

// hasReason reports whether rest (the comment after an item's code list)
// opens with ": " and some text. The comment's closing delimiter is not
// text, and neither is a colon followed only by spaces.
func hasReason(rest string) bool {
	if rest == "" || rest[0] != ':' {
		return false
	}
	r := strings.TrimSpace(rest[1:])
	r = strings.TrimSpace(strings.TrimSuffix(r, "*/"))
	return r != ""
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }

func isCodeChar(c byte) bool { return isIdentByte(c) || c == '-' }

// pairRegions matches each ignore-end with the nearest open ignore-begin
// of the same code. A begin left open runs to the end of the file.
func (s *Suppressions) pairRegions() {
	open := map[string][]*directive{}
	for _, d := range s.dirs {
		switch d.kind {
		case dirBegin:
			d.endLine = -1
			open[d.code] = append(open[d.code], d)
		case dirEnd:
			stack := open[d.code]
			if len(stack) == 0 {
				continue
			}
			b := stack[len(stack)-1]
			open[d.code] = stack[:len(stack)-1]
			b.endLine = d.line
			b.matched, d.matched = true, true
		}
	}
}

// covers reports whether d suppresses code at line.
func (d *directive) covers(code string, line int) bool {
	if d.code != code && d.code != CodeAll {
		return false
	}
	switch d.kind {
	case dirIgnore:
		return d.line == line
	case dirNext:
		return d.target >= 0 && d.target == line
	case dirFile:
		return true
	case dirBegin:
		return line >= d.line && (d.endLine < 0 || line <= d.endLine)
	}
	return false
}

// Filter drops the findings a suppression covers and marks those
// suppressions used. Every covering suppression is marked, so a line
// ignore under a file ignore of the same code is not reported unused.
func (s *Suppressions) Filter(fs []Finding) []Finding {
	return s.filter(fs, nil)
}

func (s *Suppressions) filter(fs []Finding, self []*directive) []Finding {
	if s == nil || len(s.dirs) == 0 {
		return fs
	}
	out := fs[:0]
	for i, f := range fs {
		hit := false
		for _, d := range s.dirs {
			if self != nil && self[i] == d {
				continue
			}
			if d.covers(f.Code, f.Range.Start.Line) {
				d.used = true
				hit = true
			}
		}
		if !hit {
			out = append(out, f)
		}
	}
	return out
}

// Unused reports the suppressions that suppressed nothing, at severity
// sev. A code is only judged when its rule ran on the document (ran), so
// disabling a rule in the configuration does not turn every suppression of
// it into noise; "all" and codes no rule has are always judged. The
// findings are themselves subject to the document's suppressions, except
// that a suppression can never hide its own report.
func (s *Suppressions) Unused(sev Severity, ran map[string]bool) []Finding {
	if s == nil {
		return nil
	}
	var out []Finding
	var self []*directive
	for _, d := range s.dirs {
		var msg string
		_, known := Lookup(d.code)
		switch {
		case d.kind == dirEnd:
			if !d.matched {
				msg = fmt.Sprintf("ignore-end=%s has no matching ignore-begin", d.code)
			}
		case d.used:
		case d.code != CodeAll && !known:
			msg = fmt.Sprintf("unknown rule code %q in suppression", d.code)
		case d.code == CodeAll || ran[d.code]:
			msg = fmt.Sprintf("suppression of %s matched no finding", d.code)
		}
		if msg == "" {
			continue
		}
		out = append(out, Finding{
			Code:     UnusedSuppressionCode,
			Severity: sev,
			Range:    protocol.Range{Start: s.doc.Position(d.start), End: s.doc.Position(d.end)},
			Message:  msg,
		})
		self = append(self, d)
	}
	return s.filter(out, self)
}

// Unreasoned reports, at severity sev, every suppression item that gives
// no reason. An item is one key with its code list, so "ignore=a,b" is
// one finding over both codes. ignore-end is never reported: its begin
// carries the reason. As with Unused, a suppression cannot hide its own
// report, but others (an ignore-file of this code) can.
func (s *Suppressions) Unreasoned(sev Severity) []Finding {
	if s == nil {
		return nil
	}
	var out []Finding
	var self []*directive
	for k, d := range s.dirs {
		if d.kind == dirEnd || d.reason {
			continue
		}
		// The first directive of an item stands for the whole item.
		if k > 0 && s.dirs[k-1].listEnd == d.listEnd && s.dirs[k-1].kind == d.kind {
			continue
		}
		key := "ignore"
		for name, kind := range dirKeys {
			if kind == d.kind {
				key = name
			}
		}
		out = append(out, Finding{
			Code:     IgnoreWithoutReasonCode,
			Severity: sev,
			Range:    protocol.Range{Start: s.doc.Position(d.start), End: s.doc.Position(d.listEnd)},
			Message:  fmt.Sprintf("%s=%s gives no reason: say why after a colon, as in %s=<code>: <reason>", key, s.doc.Text[d.start:d.listEnd], key),
		})
		self = append(self, d)
	}
	return s.filter(out, self)
}

// IgnoreFix is the quick fix that silences code on line: it appends the
// code to an ignore= already on the line, or adds a suppression comment at
// the end of the line in the document's style.
func (s *Suppressions) IgnoreFix(line int, code string) Fix {
	title := fmt.Sprintf("Ignore %s on this line", code)
	var last *directive
	for _, d := range s.dirs {
		if d.kind == dirIgnore && d.line == line && (last == nil || d.listEnd > last.listEnd) {
			last = d
		}
	}
	if last != nil {
		pos := s.doc.Position(last.listEnd)
		return NewFix(title, protocol.TextEdit{Range: protocol.Range{Start: pos, End: pos}, NewText: "," + code})
	}
	text, _ := s.doc.Line(line)
	lineStart, _ := s.doc.Offset(protocol.Position{Line: line})
	content := strings.TrimRight(text, " \t\r\n")
	at := lineStart + len(content)
	comment := " // " + tag + " ignore=" + code
	if s.style == StyleAsm {
		comment = " /* " + tag + " ignore=" + code + " */"
	}
	// A continued line (a #define body, a macro) must keep its backslash
	// last; a block comment before it is the one form that survives there.
	if strings.HasSuffix(content, "\\") {
		at--
		comment = "/* " + tag + " ignore=" + code + " */ "
		if at > lineStart && s.doc.Text[at-1] != ' ' && s.doc.Text[at-1] != '\t' {
			comment = " " + comment
		}
	}
	pos := s.doc.Position(at)
	return NewFix(title, protocol.TextEdit{Range: protocol.Range{Start: pos, End: pos}, NewText: comment})
}

// newlineStarts is the byte offset of each lexer line: the lexer splits at
// '\n' only, unlike docstore, which also honours a lone '\r'.
func newlineStarts(text string) []int {
	starts := make([]int, 1, strings.Count(text, "\n")+1)
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// containsFold reports whether s contains the lowercase ASCII tag ignoring
// case, without allocating: every comment of a document is checked and
// almost none hold a tag.
func containsFold(s, tag string) bool {
	for i := 0; i+len(tag) <= len(s); i++ {
		if s[i]|0x20 == tag[0] && strings.EqualFold(s[i:i+len(tag)], tag) {
			return true
		}
	}
	return false
}

// span of a C comment, in bytes.
type cSpan struct{ start, end int }

// scanC finds the comments of C source, stepping over string and character
// literals (raw strings included) so a "//" inside a string is not taken
// for one. It also returns, in order, the offset of the first code byte
// of every line that has code outside comments, which is what ignore-next
// needs.
func scanC(src string) (comments []cSpan, code []int) {
	lineHasCode := false
	mark := func(i int) {
		if !lineHasCode {
			lineHasCode = true
			code = append(code, i)
		}
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			lineHasCode = false
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			j := i + 2
			for j < len(src) && src[j] != '\n' {
				// A backslash-newline continues the comment onto the next line.
				if src[j] == '\\' && (j+1 < len(src) && src[j+1] == '\n' || j+2 < len(src) && src[j+1] == '\r' && src[j+2] == '\n') {
					j += 2
					continue
				}
				j++
			}
			comments = append(comments, cSpan{i, j})
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			j := len(src)
			if end >= 0 {
				j = i + 2 + end + 2
			}
			comments = append(comments, cSpan{i, j})
			i = j
		case c == '"' || c == '\'' && !(i > 0 && isIdentByte(src[i-1])):
			mark(i)
			if c == '"' && rawPrefix(src, i) {
				i = skipRaw(src, i)
				continue
			}
			j := i + 1
			for j < len(src) && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			i = min(j+1, len(src))
		default:
			mark(i)
			i++
		}
	}
	return comments, code
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// rawPrefix reports whether the quote at i opens a C++ raw string: R, LR,
// uR, UR or u8R directly before it, not part of a longer identifier.
func rawPrefix(src string, i int) bool {
	if i == 0 || src[i-1] != 'R' {
		return false
	}
	j := i - 1
	for _, p := range []string{"u8", "L", "u", "U"} {
		if j >= len(p) && src[j-len(p):j] == p {
			j -= len(p)
			break
		}
	}
	return j == 0 || !isIdentByte(src[j-1])
}

// skipRaw steps over R"delim( ... )delim" starting at the quote.
func skipRaw(src string, i int) int {
	open := strings.IndexByte(src[i+1:], '(')
	if open < 0 {
		return len(src)
	}
	delim := src[i+1 : i+1+open]
	end := strings.Index(src[i+2+open:], ")"+delim+"\"")
	if end < 0 {
		return len(src)
	}
	return i + 2 + open + end + len(delim) + 2
}
