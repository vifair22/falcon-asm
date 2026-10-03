// Package symbols indexes the symbols of GAS assembly documents: labels,
// local numeric labels, .set/.equ assignments, .macro names and cpp
// #define constants. It answers go-to-definition, find-references and rename
// across open documents, the headers they include and the .S/.s files under
// the workspace roots, and builds the per-document outline and folding
// ranges.
//
// Everything here works on lexer tokens; nothing is assembled or
// preprocessed, so #if branches are all indexed and a symbol built by a
// macro expansion is invisible. Those are the same limits the editor's other
// features have, and keeping them consistent beats a half-right expansion.
package symbols

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// symKind is what defined a name. It decides both the outline kind and the
// scope a reference search covers.
type symKind int

const (
	kindLabel    symKind = iota // foo:
	kindEqu                     // .set/.equ/.equiv/.eqv foo, ... and foo = ...
	kindDefine                  // #define FOO
	kindAsmMacro                // .macro foo
)

// span is a byte range on one line, which is all a token can cover.
type span struct {
	line, col, n int
}

type def struct {
	name   string
	kind   symKind
	at     span
	detail string // the value or parameter list, for the outline
}

// file is an immutable parse of one document. The index replaces a file
// wholesale on change, so readers can hold one without locking.
type file struct {
	uri  string
	key  string // filesystem path for file URIs, the URI otherwise
	text string
	// starts are the byte offsets of the lexer's lines ('\n' only). Kept
	// instead of a []string of lines: a large document has a hundred
	// thousand of them, and string headers are pointers the collector has
	// to scan on every cycle while the file is live.
	starts []int
	toks   []lexer.Token

	defs     []def
	byName   map[string][]int // name -> indexes into defs
	occs     map[string][]span
	globals  map[string]bool
	incNames []string // names written in #include and .include lines, without <> or ""
	// incAngled parallels incNames: true for #include <name>, which
	// searches the include path only, never the includer's directory.
	incAngled []bool
	includes  []string // resolved header paths supplied by the caller
	cHeader   bool     // only #define lines are meaningful
}

// cppOccDirectives are the cpp lines whose argument names macros, so the
// names in them are references worth finding and renaming.
var cppOccDirectives = map[string]bool{
	"define": true, "undef": true, "if": true, "ifdef": true, "ifndef": true,
	"elif": true, "elifdef": true, "elifndef": true,
}

// cppName returns the directive word of a CppDirective token, tolerating
// the "# define" spelling.
func cppName(t lexer.Token) string {
	return strings.TrimSpace(strings.TrimPrefix(t.Text, "#"))
}

// isCHeader decides whether a header is C (only #define lines matter) or
// assembly (indexed in full). Running a C header through the asm lexer would
// turn "default:" and friends into labels.
func isCHeader(key string) bool {
	ext := strings.ToLower(filepath.Ext(key))
	return ext == ".h" || ext == ".hh" || ext == ".hpp"
}

// parse indexes text. uri is kept for locations; the key is derived from it.
func parse(uri, text string, cHeader bool) *file {
	return parseTokens(uri, text, lexer.Tokenize(text), cHeader)
}

// parseTokens is parse with the token stream supplied, so a document the
// engine has already tokenized is not tokenized again.
func parseTokens(uri, text string, toks []lexer.Token, cHeader bool) *file {
	f := &file{
		uri:     uri,
		key:     uriKey(uri),
		text:    text,
		starts:  lexLineStarts(text),
		toks:    toks,
		byName:  make(map[string][]int),
		occs:    make(map[string][]span),
		globals: make(map[string]bool),
		cHeader: cHeader,
	}
	f.scan()
	return f
}

// File is a parsed document, shared between the index and the outline so
// one document version is scanned once.
type File struct {
	f *file
}

// Parse parses an open document whose tokens the caller already has
// (lexer.Tokenize(text); nil means tokenize here). Whether it is a C header
// follows from the URI, as for Update.
func Parse(uri, text string, toks []lexer.Token) *File {
	if toks == nil {
		toks = lexer.Tokenize(text)
	}
	return &File{f: parseTokens(uri, text, toks, isCHeader(uriKey(uri)))}
}

// lexLineStarts records where each '\n'-terminated line begins.
func lexLineStarts(text string) []int {
	starts := make([]int, 1, strings.Count(text, "\n")+1)
	for i := 0; ; {
		j := strings.IndexByte(text[i:], '\n')
		if j < 0 {
			return starts
		}
		i += j + 1
		starts = append(starts, i)
	}
}

// lineCount is the number of lexer lines, as strings.Split would count them.
func (f *file) lineCount() int { return len(f.starts) }

// line returns lexer line n without its '\n'; "" when out of range.
func (f *file) line(n int) string {
	if n < 0 || n >= len(f.starts) {
		return ""
	}
	if n+1 < len(f.starts) {
		return f.text[f.starts[n] : f.starts[n+1]-1]
	}
	return f.text[f.starts[n]:]
}

func (f *file) addDef(d def) {
	f.byName[d.name] = append(f.byName[d.name], len(f.defs))
	f.defs = append(f.defs, d)
}

func (f *file) addOcc(name string, at span) {
	f.occs[name] = append(f.occs[name], at)
}

func tokSpan(t lexer.Token) span { return span{t.Line, t.Col, t.Len} }

// scan walks the tokens once, tracking which directive owns the current
// statement, because the same token kind means different things in
// different directives (.macro parameters, section names, .globl operands).
func (f *file) scan() {
	toks := f.toks
	stmtDir := ""  // lowercased directive of the current statement
	argIdx := 0    // non-punct words seen after that directive
	cppDir := ""   // cpp directive whose CppArg tokens follow
	cppFirst := "" // set until the first CppArg of a #define is consumed
	line := -1
	for i, t := range toks {
		if t.Line != line {
			line = t.Line
			stmtDir, argIdx = "", 0
		}
		if t.Kind != lexer.CppArg && t.Kind != lexer.Comment {
			cppDir, cppFirst = "", ""
		}
		switch t.Kind {
		case lexer.CppDirective:
			cppDir = cppName(t)
			cppFirst = cppDir
			continue
		case lexer.CppArg:
			f.cppArg(t, cppDir, cppFirst == "define")
			cppFirst = ""
			continue
		case lexer.Comment:
			continue
		}
		if f.cHeader {
			continue
		}
		switch t.Kind {
		case lexer.Punct:
			if t.Text == ";" {
				stmtDir, argIdx = "", 0
			}
			continue
		case lexer.Directive:
			stmtDir, argIdx = strings.ToLower(t.Text), 0
			continue
		}
		first := argIdx == 0
		if stmtDir != "" {
			argIdx++
		}
		switch t.Kind {
		case lexer.LabelDef:
			kind := kindEqu
			if stmtDir == "" && i+1 < len(toks) && toks[i+1].Line == t.Line && toks[i+1].Text == ":" {
				kind = kindLabel
			}
			d := def{name: t.Text, kind: kind, at: tokSpan(t)}
			if kind == kindEqu {
				d.detail = f.restOfStatement(i + 1)
			}
			f.addDef(d)
			f.addOcc(t.Text, tokSpan(t))
		case lexer.LabelRef:
			if stmtDir == ".globl" || stmtDir == ".global" {
				f.globals[t.Text] = true
			}
			f.addOcc(t.Text, tokSpan(t))
		case lexer.Mnemonic:
			// A macro invocation sits in mnemonic position. Real
			// instructions are filtered when a query starts, not here, so
			// the occurrence table stays a plain name index.
			f.addOcc(t.Text, tokSpan(t))
		case lexer.Identifier:
			f.identifier(i, stmtDir, first)
		case lexer.Immediate:
			if name := t.Text[1:]; isSymbolName(name) {
				f.addOcc(name, span{t.Line, t.Col + 1, t.Len - 1})
			}
		case lexer.String:
			// gas .include pulls a file in textually, like #include, so
			// it takes part in the include heuristics the same way.
			if stmtDir == ".include" && first {
				if name := strings.Trim(t.Text, "\""); name != "" {
					f.incNames = append(f.incNames, name)
					f.incAngled = append(f.incAngled, false)
				}
			}
		}
	}
}

// identifier handles the lexer's catch-all kind. Only a .macro name and
// plain words outside .macro/.section statements can name a symbol;
// parameters, section names, @types and \args never do.
func (f *file) identifier(i int, stmtDir string, first bool) {
	t := f.toks[i]
	if !isSymbolName(t.Text) || t.Text == "." {
		return
	}
	switch stmtDir {
	case ".macro":
		if first {
			f.addDef(def{name: t.Text, kind: kindAsmMacro, at: tokSpan(t), detail: f.restOfStatement(i + 1)})
			f.addOcc(t.Text, tokSpan(t))
		}
	case ".section", ".pushsection":
	default:
		f.addOcc(t.Text, tokSpan(t))
	}
}

// cppArg indexes the words of a cpp argument: the #define name as a
// definition, every other identifier as an occurrence, and #include names
// for the include heuristic.
func (f *file) cppArg(t lexer.Token, dir string, defineName bool) {
	if dir == "include" || dir == "include_next" {
		arg := strings.TrimSpace(t.Text)
		name := strings.Trim(arg, "<>\"")
		if name != "" {
			f.incNames = append(f.incNames, name)
			f.incAngled = append(f.incAngled, strings.HasPrefix(arg, "<"))
		}
		return
	}
	if !cppOccDirectives[dir] {
		return
	}
	for k, w := range cWords(t.Text) {
		at := span{t.Line, t.Col + w.off, len(w.word)}
		if k == 0 && defineName {
			body := strings.TrimSpace(t.Text[w.off+len(w.word):])
			f.addDef(def{name: w.word, kind: kindDefine, at: at, detail: body})
		}
		if w.word == "defined" {
			continue
		}
		f.addOcc(w.word, at)
	}
}

// restOfStatement returns the source text after token i up to the end of
// its statement, used as a one-line detail for .set values and .macro
// parameters.
func (f *file) restOfStatement(i int) string {
	if i >= len(f.toks) {
		return ""
	}
	line := f.toks[i-1].Line
	start, end := -1, -1
	for j := i; j < len(f.toks) && f.toks[j].Line == line; j++ {
		t := f.toks[j]
		if t.Kind == lexer.Comment || (t.Kind == lexer.Punct && t.Text == ";") {
			break
		}
		if start < 0 {
			start = t.Col
		}
		end = t.Col + t.Len
	}
	if start < 0 {
		return ""
	}
	s := strings.TrimSpace(f.line(line)[start:end])
	s = strings.TrimSpace(strings.TrimPrefix(s, ","))
	return strings.TrimSpace(strings.TrimPrefix(s, "="))
}

type cWord struct {
	off  int
	word string
}

// cWords splits a cpp argument into C identifiers, skipping numbers and
// quoted text so "0x10" and an #error message do not produce names.
func cWords(s string) []cWord {
	var out []cWord
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		case isCIdentStart(c):
			j := i + 1
			for j < len(s) && isCIdentChar(s[j]) {
				j++
			}
			out = append(out, cWord{i, s[i:j]})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(s) && isCIdentChar(s[j]) {
				j++
			}
			i = j
		default:
			i++
		}
	}
	return out
}

func isCIdentStart(c byte) bool {
	return c == '_' || (c|0x20) >= 'a' && (c|0x20) <= 'z'
}

func isCIdentChar(c byte) bool { return isCIdentStart(c) || (c >= '0' && c <= '9') }

// isSymbolName reports whether s is a GAS symbol: letters, digits, '_', '.'
// and '$', not starting with a digit.
func isSymbolName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isCIdentChar(c) || c == '.' || c == '$') {
			return false
		}
	}
	return true
}

// isCIdent reports whether s can be a cpp macro name.
func isCIdent(s string) bool {
	if s == "" || !isCIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isCIdentChar(s[i]) {
			return false
		}
	}
	return true
}

// ResolveLocal finds the definition a local numeric label reference points
// at, the way GAS does: "Nf" is the nearest "N:" after the reference, "Nb"
// the nearest before it. Token order rather than line order decides, which
// gets "2: jmp 2b" and "jmp 1f; 1:" on one line right. A LocalLabelDef
// resolves to itself. ok is false for any other token or a dangling
// reference.
func ResolveLocal(toks []lexer.Token, idx int) (defIdx int, ok bool) {
	if idx < 0 || idx >= len(toks) {
		return -1, false
	}
	t := toks[idx]
	switch t.Kind {
	case lexer.LocalLabelDef:
		return idx, true
	case lexer.LocalLabelRef:
	default:
		return -1, false
	}
	name, dir := t.Text[:len(t.Text)-1], t.Text[len(t.Text)-1]
	if dir == 'f' {
		for i := idx + 1; i < len(toks); i++ {
			if toks[i].Kind == lexer.LocalLabelDef && toks[i].Text == name {
				return i, true
			}
		}
		return -1, false
	}
	for i := idx - 1; i >= 0; i-- {
		if toks[i].Kind == lexer.LocalLabelDef && toks[i].Text == name {
			return i, true
		}
	}
	return -1, false
}

// tokenAt finds the token under a byte column. A caret just after a word
// (where it sits after typing) still selects the word, unless another
// non-punctuation token starts there.
func tokenAt(toks []lexer.Token, line, col int) int {
	ending := -1
	for i, t := range toks {
		if t.Line < line {
			continue
		}
		if t.Line > line {
			break
		}
		if t.Col <= col && col < t.Col+t.Len {
			if t.Kind == lexer.Punct && ending >= 0 {
				return ending
			}
			return i
		}
		if t.Col+t.Len == col && t.Kind != lexer.Punct {
			ending = i
		}
	}
	return ending
}

// byteCol converts an LSP UTF-16 column to a byte column on a lexer line.
// Lexer lines split at '\n' only; a lone '\r' line break (classic Mac) is
// the documented lexer limit and shifts lines the same way everywhere.
func (f *file) byteCol(pos protocol.Position) (line, col int, ok bool) {
	if pos.Line < 0 || pos.Line >= f.lineCount() || pos.Character < 0 {
		return 0, 0, false
	}
	s := f.line(pos.Line)
	units, i := 0, 0
	for i < len(s) && units < pos.Character {
		r, size := utf8.DecodeRuneInString(s[i:])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > pos.Character {
			break
		}
		units += w
		i += size
	}
	return pos.Line, i, true
}

// rng converts a byte span to an LSP range, measuring the line prefix so
// multi-byte text earlier on the line keeps columns right.
func (f *file) rng(s span) protocol.Range {
	text := f.line(s.line)
	end := min(s.col+s.n, len(text))
	start := min(s.col, end)
	sc := docstore.UTF16Len(text[:start])
	return protocol.Range{
		Start: protocol.Position{Line: s.line, Character: sc},
		End:   protocol.Position{Line: s.line, Character: sc + docstore.UTF16Len(text[start:end])},
	}
}

func (f *file) loc(s span) protocol.Location {
	return protocol.Location{URI: f.uri, Range: f.rng(s)}
}

// lineEnd is the UTF-16 length of a line, for ranges that run to its end.
func (f *file) lineEnd(line int) int {
	if line < 0 || line >= f.lineCount() {
		return 0
	}
	return docstore.UTF16Len(strings.TrimRight(f.line(line), "\r"))
}

// uriKey maps a file URI to a clean filesystem path so an open document and
// the same file found on disk or passed as an include compare equal. Other
// schemes (untitled:) keep the URI itself.
func uriKey(uri string) string {
	if !strings.HasPrefix(uri, "file:") {
		return uri
	}
	u, err := url.Parse(uri)
	if err != nil || u.Path == "" {
		return uri
	}
	return filepath.Clean(filepath.FromSlash(u.Path))
}

// pathURI builds a file URI from an absolute path, percent-encoding what
// needs it.
func pathURI(p string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(p)}
	return u.String()
}
