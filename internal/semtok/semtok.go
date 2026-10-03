// Package semtok turns a document into the LSP semantic token stream: the
// lexer supplies syntax, internal/isa decides which mnemonics are real and
// privileged, and the result is relative-encoded with UTF-16 positions as the
// protocol requires.
package semtok

import (
	"strings"
	"unicode/utf8"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/server"
)

// Compile-time check that Encode is what the server wires in.
var _ server.SemanticTokensFunc = Encode

// Options carries what the server knows about a document beyond its text.
// The zero value gives exactly Encode's result.
type Options struct {
	// Tokens is lexer.Tokenize(doc.Text) when the caller already has it,
	// so one document version is tokenized once; nil means tokenize here.
	Tokens []lexer.Token
	// IsMacro reports names the preprocessor defines outside the document
	// (the toolchain's macro table: headers, -D, builtins). Without it only
	// names #define'd earlier in the document paint as macros.
	IsMacro func(name string) bool
	// Deprecated holds the start positions (LSP line and UTF-16 column) of
	// tokens to mark with the deprecated modifier: instructions and
	// registers invalid in the mode in effect. Keying on the diagnostic
	// positions keeps the paint and the warning from ever disagreeing.
	Deprecated map[protocol.Position]bool
}

// Encode returns the relative-encoded semantic tokens for the whole
// document: five integers per token (deltaLine, deltaStartChar, length,
// tokenType, tokenModifiers), positions and lengths in UTF-16 code units.
// It never fails; the error is part of the server contract.
func Encode(doc *docstore.Document) ([]uint32, error) {
	return EncodeWith(doc, Options{}), nil
}

// EncodeWith is Encode with outside knowledge applied; see Options.
func EncodeWith(doc *docstore.Document, opts Options) []uint32 {
	toks := opts.Tokens
	if toks == nil {
		toks = lexer.Tokenize(doc.Text)
	}
	e := encoder{
		w:          walker{text: doc.Text},
		out:        make([]uint32, 0, len(toks)*5),
		extMacro:   opts.IsMacro,
		deprecated: opts.Deprecated,
	}
	for i := range toks {
		e.token(&toks[i])
	}
	return e.out
}

type encoder struct {
	w   walker
	out []uint32

	// lineStart holds the byte offset of the lexer's current line. The lexer
	// numbers lines by '\n' only, and tokens arrive in order, so it is
	// advanced incrementally instead of indexing every line up front.
	lexLine   int
	lineStart int

	prevLine, prevCol int

	// cppDir is the directive word of the cpp line whose CppArg tokens are
	// being read; it decides whether an argument is painted at all.
	cppDir string
	// defineName is set between "#define" and its first CppArg, which is
	// where the macro name lives.
	defineName bool
	// macros are the names #define'd so far. Only names defined earlier in
	// the document count, mirroring what cpp itself would expand; names from
	// #included headers are invisible without a preprocessor and stay labels.
	macros map[string]bool

	extMacro   func(string) bool
	deprecated map[protocol.Position]bool
}

// isMacro reports whether name expands as a cpp macro at this point.
func (e *encoder) isMacro(name string) bool {
	return e.macros[name] || e.extMacro != nil && e.extMacro(name)
}

// token maps one lexer token to zero, one or two semantic tokens.
func (e *encoder) token(t *lexer.Token) {
	start := e.offset(t)
	end := start + t.Len

	switch t.Kind {
	case lexer.Mnemonic:
		e.mnemonic(t.Text, start, end)
	case lexer.Prefix:
		e.emit(start, end, server.TokenModifier, 0)
	case lexer.Register:
		// Unknown names stay variables: a typo is still a register by
		// syntax, and the colour change would be noise while typing.
		e.emit(start, end, server.TokenVariable, 0)
	case lexer.Immediate:
		// $NAME where NAME is a cpp constant reads better with the name in
		// macro colour, matching the bare-operand case below.
		if t.Len > 1 && e.isMacro(t.Text[1:]) {
			e.emit(start, start+1, server.TokenParameter, 0)
			e.emit(start+1, end, server.TokenMacro, 0)
			return
		}
		e.emit(start, end, server.TokenParameter, 0)
	case lexer.Number:
		e.emit(start, end, server.TokenNumber, 0)
	case lexer.LabelDef, lexer.LocalLabelDef:
		e.emit(start, end, server.TokenLabel, server.ModDeclaration)
	case lexer.LabelRef:
		// The lexer cannot know an operand word is a #define'd constant;
		// painting it as a label would promise a definition that is not
		// one.
		if e.isMacro(t.Text) {
			e.emit(start, end, server.TokenMacro, 0)
			return
		}
		e.emit(start, end, server.TokenLabel, 0)
	case lexer.LocalLabelRef:
		e.emit(start, end, server.TokenLabel, 0)
	case lexer.Directive:
		e.emit(start, end, server.TokenType, 0)
	case lexer.CppDirective:
		e.cppDir = cppWord(t.Text)
		e.defineName = e.cppDir == "define"
		e.emit(start, end, server.TokenMacro, 0)
	case lexer.CppArg:
		e.cppArg(t.Text, start, end)
	case lexer.Comment:
		e.emit(start, end, server.TokenComment, 0)
	case lexer.String:
		e.emit(start, end, server.TokenString, 0)
	case lexer.Identifier:
		if e.isMacro(t.Text) {
			e.emit(start, end, server.TokenMacro, 0)
		}
	}
}

// offset converts the token's (line, byte column) to an absolute byte
// offset, advancing the line cursor past the '\n's between the previous
// token and this one.
func (e *encoder) offset(t *lexer.Token) int {
	text := e.w.text
	for e.lexLine < t.Line {
		nl := strings.IndexByte(text[e.lineStart:], '\n')
		if nl < 0 {
			break
		}
		e.lineStart += nl + 1
		e.lexLine++
	}
	return e.lineStart + t.Col
}

func (e *encoder) mnemonic(word string, start, end int) {
	inst, suffix, ok := isa.Lookup(word)
	if !ok {
		// A statement that invokes a cpp macro (a function-like macro
		// expanding to instructions) reads as one, not as a typo.
		if e.isMacro(word) {
			e.emit(start, end, server.TokenMacro, 0)
			return
		}
		e.emit(start, end, server.TokenKeyword, 0)
		return
	}
	var mods uint32
	if inst.CPL == 0 {
		mods = server.ModDefaultLibrary
	}
	// Lookup lowercases, so compare the suffix case-insensitively against
	// the word's last byte before trusting the split position.
	if n := len(word); len(suffix) == 1 && n > 1 && strings.EqualFold(word[n-1:], suffix) {
		e.emit(start, end-1, server.TokenKeyword, mods)
		e.emit(end-1, end, server.TokenOperator, 0)
		return
	}
	e.emit(start, end, server.TokenKeyword, mods)
}

func (e *encoder) cppArg(text string, start, end int) {
	if e.defineName {
		e.defineName = false
		if name := leadingIdent(text); name != "" {
			if e.macros == nil {
				e.macros = make(map[string]bool)
			}
			e.macros[name] = true
		}
	}
	switch e.cppDir {
	case "include", "include_next":
		e.emit(start, end, server.TokenString, 0)
	case "undef":
		delete(e.macros, leadingIdent(text))
	}
	// Other directives' arguments (macro bodies, #if expressions) are left
	// unpainted so the client's base grammar colours them.
}

// cppWord returns the directive word of a CppDirective token, which may have
// blanks between '#' and the word.
func cppWord(s string) string {
	return strings.TrimLeft(strings.TrimPrefix(s, "#"), " \t")
}

func leadingIdent(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '_' || (c|0x20) >= 'a' && (c|0x20) <= 'z' || i > 0 && c >= '0' && c <= '9' {
			i++
			continue
		}
		break
	}
	return s[:i]
}

// emit appends the token covering bytes [start, end). The walker splits it
// at any line terminator inside it (a lone '\r' is one to the client but
// not to the lexer) and drops empty pieces, since LSP tokens may neither
// span lines nor be empty.
func (e *encoder) emit(start, end int, typ, mods uint32) {
	e.w.skipTo(start)
	for e.w.off < end {
		line, col := e.w.line, e.w.col
		crossed := e.w.walkLine(end)
		if n := e.w.col - col; n > 0 {
			e.put(line, col, n, typ, mods)
		}
		if !crossed {
			return
		}
		e.w.terminator()
	}
}

func (e *encoder) put(line, col, n int, typ, mods uint32) {
	if e.deprecated[protocol.Position{Line: line, Character: col}] {
		mods |= server.ModDeprecated
	}
	dl := line - e.prevLine
	dc := col
	if dl == 0 {
		dc = col - e.prevCol
	}
	e.out = append(e.out, uint32(dl), uint32(dc), uint32(n), typ, mods)
	e.prevLine, e.prevCol = line, col
}

// walker tracks a byte offset together with its LSP (line, UTF-16 column).
// Tokens arrive in order, so the walker only ever moves forward and the
// whole conversion costs one pass over the text, however many tokens a long
// line holds.
type walker struct {
	text      string
	off       int
	line, col int
}

// skipTo moves forward to target. If target falls inside a multibyte code
// point the walker stops at that code point's start, reporting the column
// just before it, the same answer docstore.Position gives.
func (w *walker) skipTo(target int) {
	for w.off < target {
		if w.terminator() {
			continue
		}
		if !w.step(target) {
			return
		}
	}
}

// walkLine advances toward end without crossing a line terminator. It
// reports true when it stopped on a terminator before end, leaving it for
// the caller to consume once the piece before it has been measured.
func (w *walker) walkLine(end int) bool {
	for w.off < end {
		if c := w.text[w.off]; c == '\n' || c == '\r' {
			return true
		}
		if !w.step(end) {
			return false
		}
	}
	return false
}

// terminator consumes a "\n", "\r\n" or lone "\r" at the cursor, the three
// line endings LSP recognises.
func (w *walker) terminator() bool {
	switch w.text[w.off] {
	case '\n':
		w.off++
	case '\r':
		w.off++
		if w.off < len(w.text) && w.text[w.off] == '\n' {
			w.off++
		}
	default:
		return false
	}
	w.line++
	w.col = 0
	return true
}

// step consumes one code point if it ends at or before limit. Invalid UTF-8
// bytes count as one unit each, matching docstore.UTF16Len.
func (w *walker) step(limit int) bool {
	if c := w.text[w.off]; c < utf8.RuneSelf {
		w.off++
		w.col++
		return true
	}
	r, size := utf8.DecodeRuneInString(w.text[w.off:])
	if w.off+size > limit {
		return false
	}
	w.off += size
	if r >= 0x10000 {
		w.col += 2
	} else {
		w.col++
	}
	return true
}
