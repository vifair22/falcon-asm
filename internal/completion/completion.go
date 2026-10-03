// Package completion answers textDocument/completion for GAS x86 assembly.
//
// The context is read from the text left of the cursor only: the lexer runs
// over the document up to the cursor (so an unterminated comment or string
// in that prefix means the cursor is inside one), and the statement's
// tokens before the word being typed decide what kind of word it is:
//
//   - after '%': registers valid in the current mode
//   - a word starting with '.' at statement start: directives
//   - the first word of a statement (after labels and prefixes): mnemonics,
//     their aliases, and the prefix words
//   - any other operand word, or after '$': labels and macros from Context
//
// Every item carries a TextEdit replacing the partial word, sigil included
// for '%' and '.', so a client's own word boundaries (VS Code does not
// treat '%' as part of a word) cannot leave a doubled sigil behind.
package completion

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// MaxItems caps a reply. With the cap hit the list is marked incomplete so
// the client asks again as the prefix grows instead of filtering a
// truncated list down to nothing.
const MaxItems = 200

// Context is what completion needs from outside the document text.
type Context struct {
	// Mode is the code size in effect at the cursor line: 16, 32 or 64. 0
	// means 64, the assembler's default for x86-64 targets.
	Mode int
	// Labels and Macros supply operand candidates (the symbol index and the
	// toolchain's macro table). Either may be nil.
	Labels func() []string
	Macros func() []string
}

// Complete returns the completion list at pos, or nil where completion makes
// no sense: inside a comment, a string or a cpp directive line, on a number,
// or at an invalid position.
func Complete(doc *docstore.Document, pos protocol.Position, ctx Context) *protocol.CompletionList {
	if doc == nil {
		return nil
	}
	off, err := doc.Offset(pos)
	if err != nil {
		return nil
	}
	mode := ctx.Mode
	if mode == 0 {
		mode = 64
	}

	text := doc.Text
	lineStart := strings.LastIndexByte(text[:off], '\n') + 1
	toks, lineToks := tokensBefore(text, lineStart, off)
	if insideCommentOrString(toks, lineToks) {
		return nil
	}
	for _, t := range lineToks {
		if t.Kind == lexer.CppDirective || t.Kind == lexer.CppArg {
			return nil
		}
	}

	start := partialStart(text, lineStart, off)
	partial := text[start:off]
	var sigil byte
	if start > lineStart {
		sigil = text[start-1]
	}
	if partial != "" && isDigit(partial[0]) {
		// A number, or a local label being defined or referenced (1:, 1f):
		// nothing worth offering.
		return nil
	}

	editStart := start
	var items []protocol.CompletionItem
	switch {
	case sigil == '%':
		editStart = start - 1
		items = registerItems(strings.ToLower(partial), mode)
	case sigil == '\'' || sigil == '\\':
		// A character constant or a macro parameter reference.
		return nil
	case sigil != '$' && atStatementStart(lineToks, start-lineStart):
		if strings.HasPrefix(partial, ".") {
			items = directiveItems(strings.ToLower(partial))
		} else {
			items = mnemonicItems(strings.ToLower(partial), mode)
		}
	default:
		items = symbolItems(partial, ctx)
	}

	list := &protocol.CompletionList{Items: items}
	if len(list.Items) > MaxItems {
		list.Items = list.Items[:MaxItems]
		list.IsIncomplete = true
	}
	rng := protocol.Range{Start: doc.Position(editStart), End: doc.Position(off)}
	for i := range list.Items {
		it := &list.Items[i]
		it.SortText = fmt.Sprintf("%04d", i)
		it.FilterText = it.Label
		it.TextEdit = &protocol.TextEdit{Range: rng, NewText: it.Label}
	}
	if list.Items == nil {
		// An empty list rather than null: the client then knows the server
		// answered and does not fall back to word-based suggestions.
		list.Items = []protocol.CompletionItem{}
	}
	return list
}

// tokensBefore lexes the text up to the cursor and returns all tokens plus
// the ones on the cursor's line. Only the cursor line is lexed when nothing
// before it can change how that line lexes: no block comment opener at all
// and no backslash continuation into the line. Otherwise the whole prefix is
// lexed, which costs about a millisecond per 10k lines.
func tokensBefore(text string, lineStart, off int) (all, line []lexer.Token) {
	before := text[:lineStart]
	if !strings.Contains(before, "/*") && !continues(before) {
		all = lexer.Tokenize(text[lineStart:off])
		return all, all
	}
	all = lexer.Tokenize(text[:off])
	last := strings.Count(before, "\n")
	i := len(all)
	for i > 0 && all[i-1].Line == last {
		i--
	}
	return all, all[i:]
}

// continues reports whether the text, which ends just after a newline,
// ends with a backslash-continued line.
func continues(before string) bool {
	s := strings.TrimSuffix(before, "\n")
	s = strings.TrimSuffix(s, "\r")
	return strings.HasSuffix(s, "\\")
}

// insideCommentOrString decides from the last token whether the cursor sits
// in a comment or string. Because the lexer only saw text up to the cursor,
// anything still open there is open at the cursor.
func insideCommentOrString(all, line []lexer.Token) bool {
	if len(all) == 0 {
		return false
	}
	last := len(all) - 1
	t := all[last]
	onLine := len(line) > 0 && line[len(line)-1] == t
	switch t.Kind {
	case lexer.Comment:
		if isBlockSegment(all, last) {
			return !closesBlock(t.Text)
		}
		return onLine
	case lexer.String:
		return onLine && !closedString(t.Text)
	}
	return false
}

// isBlockSegment reports whether comment token i is part of a /* */
// comment. A continuation line's segment does not start with "/*" and may
// start with '#' or "//", so it is recognised by following an open block
// segment on an earlier line instead.
func isBlockSegment(toks []lexer.Token, i int) bool {
	// Walk back to the segment that opened the comment, so a long comment
	// costs one pass rather than recursion per line.
	j := i
	for !strings.HasPrefix(toks[j].Text, "/*") {
		if j == 0 {
			return false
		}
		prev := toks[j-1]
		if prev.Kind != lexer.Comment || prev.Line == toks[j].Line {
			return false
		}
		if strings.HasPrefix(prev.Text, "/*") {
			return !closesBlock(prev.Text)
		}
		if closesBlock(prev.Text) {
			return false
		}
		j--
	}
	return true
}

// closesBlock reports whether a block comment segment ends the comment.
// "/*/" opens a comment without closing it.
func closesBlock(s string) bool {
	if strings.HasPrefix(s, "/*") {
		return len(s) >= 4 && strings.HasSuffix(s, "*/")
	}
	return strings.HasSuffix(s, "*/")
}

// closedString mirrors the lexer's string scan: a backslash escapes the
// next byte, and the first unescaped quote ends the literal.
func closedString(s string) bool {
	for j := 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return true
		}
	}
	return false
}

// partialStart returns where the word ending at the cursor begins. '$' is
// left out of the word even though GAS allows it in symbol names, because
// at the start of an operand it is the immediate sigil.
func partialStart(text string, lineStart, off int) int {
	i := off
	for i > lineStart && isWordChar(text[i-1]) {
		i--
	}
	return i
}

func isWordChar(c byte) bool {
	return (c|0x20) >= 'a' && (c|0x20) <= 'z' || isDigit(c) || c == '_' || c == '.'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// segmentWords stand alone as mnemonics when nothing follows them yet, which
// is exactly the state while the real mnemonic is still being typed.
var segmentWords = map[string]bool{"cs": true, "ds": true, "es": true, "fs": true, "gs": true, "ss": true}

// atStatementStart reports whether the word at byte column col is in
// mnemonic position: everything of its statement before it is labels and
// prefixes.
func atStatementStart(line []lexer.Token, col int) bool {
	var stmt []lexer.Token
	for _, t := range line {
		if t.Col >= col {
			break
		}
		switch {
		case t.Kind == lexer.Punct && t.Text == ";":
			stmt = stmt[:0]
		case t.Kind != lexer.Comment:
			stmt = append(stmt, t)
		}
	}
	for _, t := range stmt {
		switch {
		case t.Kind == lexer.LabelDef, t.Kind == lexer.LocalLabelDef, t.Kind == lexer.Prefix:
		case t.Kind == lexer.Punct && t.Text == ":":
		case t.Kind == lexer.Mnemonic && segmentWords[strings.ToLower(t.Text)]:
		default:
			return false
		}
	}
	return true
}

// validInMode reports whether a Modes field ("16,32", "64", "" for all)
// admits mode.
func validInMode(modes string, mode int) bool {
	if modes == "" {
		return true
	}
	want := fmt.Sprint(mode)
	for _, f := range strings.FieldsFunc(modes, func(r rune) bool { return r == ',' || r == ' ' }) {
		if f == want {
			return true
		}
	}
	return false
}

// tables holds the candidate lists, built once: the ISA data is static, and
// rebuilding about 2,000 mnemonic entries per keystroke would dominate the
// cost of a request.
type tables struct {
	mnemonics  []mnemonic
	registers  []*isa.Register // completion order
	directives []*isa.Directive
}

type mnemonic struct {
	label  string
	detail string
	kind   protocol.CompletionItemKind
	modes  string
	common int    // rank in commonMnemonics, or -1
	base   string // for a generated suffixed form, the base it extends
}

var (
	tablesOnce sync.Once
	tbl        tables
)

func loadTables() *tables {
	tablesOnce.Do(func() {
		tbl.mnemonics = buildMnemonics()
		tbl.registers = isa.Registers()
		sort.SliceStable(tbl.registers, func(i, j int) bool {
			a, b := tbl.registers[i], tbl.registers[j]
			if ca, cb := classRank(a.Class), classRank(b.Class); ca != cb {
				return ca < cb
			}
			return naturalLess(a.Name, b.Name)
		})
		tbl.directives = isa.Directives()
		sort.Slice(tbl.directives, func(i, j int) bool { return tbl.directives[i].Name < tbl.directives[j].Name })
	})
	return &tbl
}
