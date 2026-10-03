// Package lexer tokenizes GNU as (GAS) x86 assembly in AT&T syntax as it
// appears in cpp-preprocessed .S files, before cpp has run: cpp directives
// and C comments are first-class tokens.
//
// The lexer knows syntax, not instructions. The mnemonic is found by
// position (the first word of a statement after label definitions and
// prefix words), so a macro invocation is reported as a Mnemonic too;
// deciding whether a word names a real instruction is internal/isa's job.
//
// Every token lies on one line. Constructs that span lines (block comments,
// #define continuations) are reported one token per line segment, because
// LSP semantic tokens cannot span lines and every consumer would otherwise
// have to split them again.
package lexer

import "strings"

// Kind classifies a token.
type Kind int

// Token kinds. The order is part of the contract with other packages only by
// name; nothing should depend on the numeric values.
const (
	Mnemonic      Kind = iota // first word of an instruction statement (after any labels / prefixes)
	Prefix                    // rep, lock, data16, segment overrides written as words, {vex} pseudo prefixes
	Register                  // %eax, %cr0, %st(1): whole token including '%'
	Immediate                 // '$' plus the expression's first atom when there is one: $0x92, $sym, $'a', $\arg; a bare '$' before '(' or '-'
	Number                    // numeric or character literal not preceded by '$'
	LabelDef                  // foo:, name = expr, .set/.equ name (token excludes the colon)
	LocalLabelDef             // 1:
	LabelRef                  // identifier used as an operand or directive argument
	LocalLabelRef             // 1f, 1b
	Directive                 // .code16, .globl, .section: whole token including '.'
	CppDirective              // #include, #define ...: from '#' through the directive word
	CppArg                    // the rest of a cpp line, one token per contiguous run between comments
	Comment                   // /* */ (one token per line segment), //, and '#' comments
	String                    // "..." with escapes
	Identifier                // \arg, .macro name and parameters, section names, @type, the location counter '.'
	Punct                     // , ( ) : * + - and every other ASCII operator character
)

var kindNames = [...]string{
	Mnemonic:      "Mnemonic",
	Prefix:        "Prefix",
	Register:      "Register",
	Immediate:     "Immediate",
	Number:        "Number",
	LabelDef:      "LabelDef",
	LocalLabelDef: "LocalLabelDef",
	LabelRef:      "LabelRef",
	LocalLabelRef: "LocalLabelRef",
	Directive:     "Directive",
	CppDirective:  "CppDirective",
	CppArg:        "CppArg",
	Comment:       "Comment",
	String:        "String",
	Identifier:    "Identifier",
	Punct:         "Punct",
}

func (k Kind) String() string {
	if k >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Kind(?)"
}

// Token is one lexeme. Line and Col are zero based; Col and Len are bytes.
// Text is always src[lineStart+Col : lineStart+Col+Len].
type Token struct {
	Kind Kind
	Line int
	Col  int
	Len  int
	Text string
}

// cppDirectives is the set of words that make a line-leading '#' a
// preprocessor directive. Anything else after '#' is a GAS comment, which is
// what as would see if the line reached it.
var cppDirectives = map[string]bool{
	"include": true, "include_next": true, "define": true, "undef": true,
	"if": true, "ifdef": true, "ifndef": true, "elif": true, "elifdef": true,
	"elifndef": true, "else": true, "endif": true, "error": true,
	"warning": true, "pragma": true, "line": true,
}

// prefixWords are instruction prefixes GAS accepts written as a separate
// word before the mnemonic.
var prefixWords = map[string]bool{
	"rep": true, "repe": true, "repz": true, "repne": true, "repnz": true,
	"lock": true, "data16": true, "data32": true, "addr16": true,
	"addr32": true, "rex": true, "rex64": true, "notrack": true,
	"xacquire": true, "xrelease": true, "bnd": true,
}

// segmentWords are only prefixes when another word follows; on their own
// they are the (rare) one-byte segment-override instruction form.
var segmentWords = map[string]bool{
	"cs": true, "ds": true, "es": true, "fs": true, "gs": true, "ss": true,
}

// labelDefDirectives define the symbol named by their first argument, so
// that argument is reported as a definition for go-to-definition.
var labelDefDirectives = map[string]bool{
	".set": true, ".equ": true, ".equiv": true, ".eqv": true,
}

// identArgDirectives take a section name, not a symbol, as their first
// argument; reporting it as a LabelRef would offer a definition that does
// not exist.
var identArgDirectives = map[string]bool{
	".section": true, ".pushsection": true,
}

// twoCharOps are GAS expression operators that read as one symbol; keeping
// them whole lets a consumer treat "<<" as an operator, not two less-thans.
var twoCharOps = map[string]bool{
	"<<": true, ">>": true, "==": true, "!=": true, "<>": true, "<=": true,
	">=": true, "&&": true, "||": true,
}

type mode int

const (
	modeStart     mode = iota // expecting labels, prefixes or the statement's first word
	modeOperands              // after a mnemonic or an assignment's '='
	modeDirective             // after a directive word
)

type lexer struct {
	src       string
	i         int
	line      int
	lineStart int
	toks      []Token

	mode    mode
	dirName string // directive of the current statement, lowercased because GAS ignores case there
	argPos  int    // words seen in the current directive's first argument; -1 after a comma
}

// Tokenize splits src into tokens. It never fails: unrecognised bytes are
// emitted as Punct (ASCII) or skipped (non-ASCII), so an editor always gets
// a best-effort result for a half-typed line.
func Tokenize(src string) []Token {
	l := &lexer{src: src, toks: make([]Token, 0, len(src)/4)}
	l.run()
	return l.toks
}

func (l *lexer) emit(k Kind, start, end int) {
	if end <= start {
		return
	}
	l.toks = append(l.toks, Token{
		Kind: k,
		Line: l.line,
		Col:  start - l.lineStart,
		Len:  end - start,
		Text: l.src[start:end],
	})
}

// newline consumes the '\n' at l.i.
func (l *lexer) newline() {
	l.i++
	l.line++
	l.lineStart = l.i
}

func (l *lexer) resetStmt() {
	l.mode = modeStart
	l.dirName = ""
	l.argPos = 0
}

func (l *lexer) peek(off int) byte {
	if l.i+off < len(l.src) {
		return l.src[l.i+off]
	}
	return 0
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return (c|0x20) >= 'a' && (c|0x20) <= 'z' }

func isIdentStart(c byte) bool { return isLetter(c) || c == '_' || c == '.' }

func isIdentChar(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '.' || c == '$'
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v' }

// lineEnd returns the offset of the next '\n' at or after from, or len(src).
func (l *lexer) lineEnd(from int) int {
	for j := from; j < len(l.src); j++ {
		if l.src[j] == '\n' {
			return j
		}
	}
	return len(l.src)
}

// trimRight drops trailing whitespace (including the '\r' of CRLF) so no
// token carries it.
func (l *lexer) trimRight(start, end int) int {
	for end > start && isSpace(l.src[end-1]) {
		end--
	}
	return end
}

// skipWord returns the end of the identifier run starting at from.
func (l *lexer) skipWord(from int) int {
	j := from
	for j < len(l.src) && isIdentChar(l.src[j]) {
		j++
	}
	return j
}

// nextNonSpace returns the first byte at or after from that is not blank on
// the same line, or 0 at end of line or input.
func (l *lexer) nextNonSpace(from int) (byte, int) {
	j := from
	for j < len(l.src) && (l.src[j] == ' ' || l.src[j] == '\t') {
		j++
	}
	if j < len(l.src) {
		return l.src[j], j
	}
	return 0, j
}

func (l *lexer) run() {
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '\n':
			l.newline()
			l.resetStmt()
		case isSpace(c):
			l.i++
		case c == '#':
			if l.atLineStart() && l.cppLine() {
				continue
			}
			l.lineComment()
		case c == '/' && l.peek(1) == '*':
			l.blockComment()
		case c == '/' && l.peek(1) == '/':
			l.lineComment()
		case c == ';':
			l.emit(Punct, l.i, l.i+1)
			l.i++
			l.resetStmt()
		case c == '"':
			l.str()
		case c == '\'':
			l.operand(Number, l.charLit(l.i))
		case c == '$':
			l.immediate()
		case c == '%':
			l.register()
		case isDigit(c):
			l.number()
		case isIdentStart(c) || c == '\\':
			l.word()
		case c == '@':
			l.atWord()
		case c == '{' && l.mode == modeStart:
			l.pseudoPrefix()
		case c == ',':
			if l.mode == modeDirective {
				l.argPos = -1
			}
			l.emit(Punct, l.i, l.i+1)
			l.i++
		case c < 0x80:
			n := 1
			if twoCharOps[l.src[l.i:min(l.i+2, len(l.src))]] {
				n = 2
			}
			l.operand(Punct, l.i+n)
		default:
			// Non-ASCII outside comments and strings is not GAS syntax;
			// dropping it keeps a stray byte from splitting a UTF-8
			// sequence into tokens that cannot be mapped to UTF-16.
			l.i++
		}
	}
}

// operand emits a token that ends any label/prefix phase of the statement.
func (l *lexer) operand(k Kind, end int) {
	l.emit(k, l.i, end)
	l.i = end
	if l.mode == modeStart {
		l.mode = modeOperands
	}
}

func (l *lexer) atLineStart() bool {
	for j := l.lineStart; j < l.i; j++ {
		if l.src[j] != ' ' && l.src[j] != '\t' {
			return false
		}
	}
	return true
}

func (l *lexer) lineComment() {
	end := l.trimRight(l.i, l.lineEnd(l.i))
	l.emit(Comment, l.i, end)
	l.i = l.lineEnd(l.i)
}

// blockComment emits one Comment per line the comment touches. Statement
// state is left alone: cpp replaces the comment with a single space, so a
// statement that straddles one continues after it.
func (l *lexer) blockComment() {
	start := l.i
	l.i += 2
	for l.i < len(l.src) {
		c := l.src[l.i]
		if c == '*' && l.peek(1) == '/' {
			l.i += 2
			l.emit(Comment, start, l.i)
			return
		}
		if c == '\n' {
			l.emit(Comment, start, l.trimRight(start, l.i))
			l.newline()
			// Leading indentation of a continuation line is not comment
			// text worth painting.
			for l.i < len(l.src) && (l.src[l.i] == ' ' || l.src[l.i] == '\t') {
				l.i++
			}
			start = l.i
			continue
		}
		l.i++
	}
	l.emit(Comment, start, l.trimRight(start, l.i))
}

// cppLine handles a line-leading '#'. It returns false, consuming nothing,
// when the word after '#' is not a cpp directive so the caller can treat
// the line as a GAS comment.
func (l *lexer) cppLine() bool {
	j := l.i + 1
	for j < len(l.src) && (l.src[j] == ' ' || l.src[j] == '\t') {
		j++
	}
	ws := j
	for j < len(l.src) && (isLetter(l.src[j]) || l.src[j] == '_') {
		j++
	}
	if j == ws || !cppDirectives[l.src[ws:j]] || (j < len(l.src) && isIdentChar(l.src[j])) {
		return false
	}
	l.emit(CppDirective, l.i, j)
	l.i = j
	l.cppArgs()
	return true
}

// cppArgs scans the rest of a directive, following backslash continuations
// and splitting around comments so the comments keep their own colour.
func (l *lexer) cppArgs() {
	seg := -1
	flush := func() {
		if seg >= 0 {
			l.emit(CppArg, seg, l.trimRight(seg, l.i))
			seg = -1
		}
	}
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '\n':
			flush()
			if l.trimCR(l.lineStart, l.i) == '\\' {
				l.newline()
				continue
			}
			return
		case c == '/' && l.peek(1) == '*':
			flush()
			l.blockComment()
		case c == '/' && l.peek(1) == '/':
			flush()
			l.lineComment()
		case c == ' ' || c == '\t' || c == '\r':
			l.i++
		case c == '"' || c == '\'':
			if seg < 0 {
				seg = l.i
			}
			l.i = l.skipQuoted(l.i)
		default:
			if seg < 0 {
				seg = l.i
			}
			l.i++
		}
	}
	flush()
}

// trimCR returns the last byte of src[start:end] ignoring a CRLF's '\r', or
// 0 when there is none.
func (l *lexer) trimCR(start, end int) byte {
	if end > start && l.src[end-1] == '\r' {
		end--
	}
	if end > start {
		return l.src[end-1]
	}
	return 0
}

// skipQuoted returns the offset just past a quoted run starting at from.
// An unclosed quote counts as one ordinary character, because an apostrophe
// in "#error don't" must not hide a comment that follows it.
func (l *lexer) skipQuoted(from int) int {
	q := l.src[from]
	for j := from + 1; j < len(l.src) && l.src[j] != '\n'; j++ {
		switch l.src[j] {
		case '\\':
			if j+1 < len(l.src) && l.src[j+1] != '\n' {
				j++
			}
		case q:
			return j + 1
		}
	}
	return from + 1
}

// str scans a string literal. An unterminated string stops at end of line so
// a half-typed one does not swallow the rest of the file.
func (l *lexer) str() {
	j := l.i + 1
	closed := false
	for j < len(l.src) && l.src[j] != '\n' {
		if l.src[j] == '\\' && j+1 < len(l.src) && l.src[j+1] != '\n' {
			j += 2
			continue
		}
		j++
		if l.src[j-1] == '"' {
			closed = true
			break
		}
	}
	if !closed {
		j = l.trimRight(l.i+1, j)
	}
	l.operand(String, j)
}

// charLit returns the end of a character constant at from. GAS spells it
// 'c with no closing quote; C habit adds one, and GAS tolerates that, so an
// optional closing quote is taken too.
func (l *lexer) charLit(from int) int {
	j := from + 1
	if j >= len(l.src) || l.src[j] == '\n' {
		return j
	}
	switch {
	case l.src[j] == '\\':
		j++
		if j < len(l.src) && l.src[j] != '\n' {
			j++
			for j < len(l.src) && j-from < 6 && isHex(l.src[j]) {
				j++
			}
		}
	case l.src[j] == '\r' && (j+1 == len(l.src) || l.src[j+1] == '\n'):
		return j
	case l.src[j] >= 0x80:
		// Keep a multi-byte character whole.
		j++
		for j < len(l.src) && l.src[j]&0xC0 == 0x80 {
			j++
		}
	default:
		j++
	}
	if j < len(l.src) && l.src[j] == '\'' {
		j++
	}
	return j
}

func isHex(c byte) bool { return isDigit(c) || ((c|0x20) >= 'a' && (c|0x20) <= 'f') }

// immediate takes '$' and the atom right after it, so a semantic token can
// paint "$0x92" or "$sym" as one immediate. A parenthesised or negated
// expression leaves the '$' alone and lexes the expression normally.
func (l *lexer) immediate() {
	j := l.i + 1
	if j < len(l.src) {
		c := l.src[j]
		switch {
		case isDigit(c) || isIdentStart(c):
			j = l.skipWord(j)
		case c == '\'':
			j = l.charLit(j)
		case c == '\\':
			j = l.skipWord(j + 1)
		}
	}
	l.operand(Immediate, j)
}

func (l *lexer) register() {
	j := l.i + 1
	if j >= len(l.src) || !isLetter(l.src[j]) {
		l.operand(Punct, j)
		return
	}
	for j < len(l.src) && (isLetter(l.src[j]) || isDigit(l.src[j]) || l.src[j] == '_') {
		j++
	}
	// %st(1) names one register, so the index belongs to the token.
	if l.src[l.i+1:j] == "st" && j+2 < len(l.src) && l.src[j] == '(' && isDigit(l.src[j+1]) && l.src[j+2] == ')' {
		j += 3
	}
	l.operand(Register, j)
}

func isLocalRef(w string) bool {
	if len(w) < 2 {
		return false
	}
	last := w[len(w)-1]
	if last != 'f' && last != 'b' {
		return false
	}
	for i := 0; i < len(w)-1; i++ {
		if !isDigit(w[i]) {
			return false
		}
	}
	return true
}

func (l *lexer) number() {
	j := l.i
	for j < len(l.src) && (isIdentChar(l.src[j]) && l.src[j] != '$') {
		j++
	}
	w := l.src[l.i:j]
	if l.mode == modeStart {
		if c, k := l.nextNonSpace(j); c == ':' && isAllDigits(w) {
			l.emit(LocalLabelDef, l.i, j)
			l.emit(Punct, k, k+1)
			l.i = k + 1
			return
		}
	}
	if isLocalRef(w) {
		l.operand(LocalLabelRef, j)
		return
	}
	l.operand(Number, j)
}

func isAllDigits(w string) bool {
	for i := 0; i < len(w); i++ {
		if !isDigit(w[i]) {
			return false
		}
	}
	return w != ""
}

func (l *lexer) atWord() {
	j := l.skipWord(l.i + 1)
	if j == l.i+1 {
		l.operand(Punct, j)
		return
	}
	l.operand(Identifier, j)
}

// pseudoPrefix recognises {vex}, {evex}, {disp32} and friends, which GAS
// accepts before a mnemonic to steer encoding.
func (l *lexer) pseudoPrefix() {
	j := l.i + 1
	for j < len(l.src) && (isLetter(l.src[j]) || isDigit(l.src[j])) {
		j++
	}
	if j > l.i+1 && j < len(l.src) && l.src[j] == '}' {
		l.emit(Prefix, l.i, j+1)
		l.i = j + 1
		return
	}
	l.operand(Punct, l.i+1)
}

func (l *lexer) word() {
	start := l.i
	var end int
	backslash := l.src[start] == '\\'
	if backslash {
		end = l.skipWord(start + 1)
		if end == start+1 {
			l.operand(Punct, end)
			return
		}
	} else {
		end = l.skipWord(start)
	}
	w := l.src[start:end]

	switch l.mode {
	case modeStart:
		l.statementWord(w, start, end, backslash)
	case modeDirective:
		l.directiveArg(w, start, end, backslash)
	default:
		l.emit(operandKind(w, backslash), start, end)
		l.i = end
	}
}

func operandKind(w string, backslash bool) Kind {
	if backslash || w == "." {
		return Identifier
	}
	return LabelRef
}

func (l *lexer) statementWord(w string, start, end int, backslash bool) {
	c, k := l.nextNonSpace(end)
	switch {
	case c == ':':
		kind := LabelDef
		if backslash {
			kind = Identifier
		}
		l.emit(kind, start, end)
		l.emit(Punct, k, k+1)
		l.i = k + 1
		return
	case c == '=' && (k+1 >= len(l.src) || l.src[k+1] != '='):
		kind := LabelDef
		if backslash || w == "." {
			kind = Identifier
		}
		l.emit(kind, start, end)
		l.i = end
		l.mode = modeOperands
		return
	case backslash:
		l.emit(Identifier, start, end)
		l.mode = modeOperands
	case w[0] == '.':
		l.emit(Directive, start, end)
		l.mode = modeDirective
		l.dirName = strings.ToLower(w)
		l.argPos = 0
	case prefixWords[w]:
		l.emit(Prefix, start, end)
	case segmentWords[w] && (isIdentStart(c) || c == '{'):
		l.emit(Prefix, start, end)
	default:
		l.emit(Mnemonic, start, end)
		l.mode = modeOperands
	}
	l.i = end
}

func (l *lexer) directiveArg(w string, start, end int, backslash bool) {
	first := l.argPos == 0
	if l.argPos >= 0 {
		l.argPos++
	}
	kind := operandKind(w, backslash)
	switch {
	case l.dirName == ".macro":
		kind = Identifier
	case first && labelDefDirectives[l.dirName] && !backslash:
		kind = LabelDef
	case first && identArgDirectives[l.dirName]:
		kind = Identifier
	}
	l.emit(kind, start, end)
	l.i = end
}
