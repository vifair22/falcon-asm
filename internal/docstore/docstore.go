// Package docstore holds the text of open documents and is the single place
// where LSP positions (line, UTF-16 code unit) are converted to and from byte
// offsets. Every other package works in bytes.
package docstore

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Store maps URIs to the latest snapshot of each open document.
type Store struct {
	mu   sync.RWMutex
	docs map[string]*Document
}

// New returns an empty store.
func New() *Store {
	return &Store{docs: make(map[string]*Document)}
}

// Open records a document, replacing any previous snapshot under the same
// URI (a client reopening without closing is tolerated rather than rejected).
func (s *Store) Open(uri string, version int, text string) {
	d := newDocument(uri, version, text)
	s.mu.Lock()
	s.docs[uri] = d
	s.mu.Unlock()
}

// Change applies edits in order, each against the result of the previous
// one, as the LSP requires. The update is all or nothing: if any edit fails,
// the stored snapshot is left as it was.
func (s *Store) Change(uri string, version int, changes []protocol.TextDocumentContentChangeEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.docs[uri]
	if !ok {
		return fmt.Errorf("change %s: document not open", uri)
	}
	for i, ch := range changes {
		if ch.Range == nil {
			cur = newDocument(uri, version, ch.Text)
			continue
		}
		start, err := cur.Offset(ch.Range.Start)
		if err != nil {
			return fmt.Errorf("change %s: edit %d start: %w", uri, i, err)
		}
		end, err := cur.Offset(ch.Range.End)
		if err != nil {
			return fmt.Errorf("change %s: edit %d end: %w", uri, i, err)
		}
		if end < start {
			return fmt.Errorf("change %s: edit %d: range end precedes start", uri, i)
		}
		cur = cur.edit(version, start, end, ch.Text)
	}
	if cur.Version != version {
		// An empty change list still advances the version.
		cur = &Document{URI: cur.URI, Version: version, Text: cur.Text, lineStarts: cur.lineStarts}
	}
	s.docs[uri] = cur
	return nil
}

// Close forgets a document.
func (s *Store) Close(uri string) {
	s.mu.Lock()
	delete(s.docs, uri)
	s.mu.Unlock()
}

// Get returns the current snapshot. Snapshots are never mutated, so the
// caller may use one after later edits without locking.
func (s *Store) Get(uri string) (*Document, bool) {
	s.mu.RLock()
	d, ok := s.docs[uri]
	s.mu.RUnlock()
	return d, ok
}

// Document is an immutable snapshot of one document's text.
//
// Documents built by the Store carry a precomputed line index. A Document
// built as a literal (convenient in other packages' tests) still works; its
// index is recomputed on each call instead of cached, which keeps the type
// free of locks and safe to copy.
type Document struct {
	URI     string
	Version int
	Text    string

	lineStarts []int
}

func newDocument(uri string, version int, text string) *Document {
	return &Document{URI: uri, Version: version, Text: text, lineStarts: lineStarts(text)}
}

// lineStarts records the byte offset where each line begins. The LSP counts
// "\n", "\r\n" and a lone "\r" as line terminators, so all three are honoured.
func lineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\n':
			starts = append(starts, i+1)
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			starts = append(starts, i+1)
		}
	}
	return starts
}

// edit returns the document with bytes [start, end) replaced by text. The
// line index is patched rather than rebuilt: lines before the edit keep
// their starts, the edited region is rescanned, and lines after it are
// shifted. Rebuilding meant scanning every byte of the document on every
// keystroke, which on a 100k-line file cost more than the edit itself.
func (d *Document) edit(version, start, end int, text string) *Document {
	newText := d.Text[:start] + text + d.Text[end:]
	old := d.starts()
	delta := len(text) - (end - start)

	// A start at or before the edit's first byte stays valid: the bytes
	// that decided it lie before start. The terminator just before start is
	// rescanned anyway, since a lone "\r" there pairs with a "\n" the edit
	// may insert or remove.
	keep := sort.SearchInts(old, start)
	keep = max(keep, 1)
	starts := make([]int, keep, len(old)+strings.Count(text, "\n")+strings.Count(text, "\r")+1)
	copy(starts, old[:keep])

	// Rescan from the byte before the edit to one byte past the inserted
	// text, so a "\r\n" pair straddling either edge is classified once.
	i := max(start-1, 0)
	stop := min(start+len(text)+1, len(newText))
	for ; i < stop; i++ {
		switch newText[i] {
		case '\n':
			starts = append(starts, i+1)
		case '\r':
			if i+1 < len(newText) && newText[i+1] == '\n' {
				i++
			}
			starts = append(starts, i+1)
		}
	}
	// Everything from i on is unchanged text, so the old starts beyond it
	// only move by the size difference.
	for _, s := range old[sort.SearchInts(old, i-delta+1):] {
		starts = append(starts, s+delta)
	}
	return &Document{URI: d.URI, Version: version, Text: newText, lineStarts: starts}
}

func (d *Document) starts() []int {
	if d.lineStarts != nil {
		return d.lineStarts
	}
	return lineStarts(d.Text)
}

// lineBounds returns the byte range of line n's content, excluding its
// terminator.
func (d *Document) lineBounds(starts []int, n int) (start, end int) {
	start = starts[n]
	if n+1 >= len(starts) {
		return start, len(d.Text)
	}
	end = starts[n+1]
	if end > start && d.Text[end-1] == '\n' {
		end--
	}
	if end > start && d.Text[end-1] == '\r' {
		end--
	}
	return start, end
}

// LineCount returns the number of lines. Text ending in a terminator has a
// final empty line, matching how editors number lines.
func (d *Document) LineCount() int {
	return len(d.starts())
}

// Line returns the content of line n without its terminator.
func (d *Document) Line(n int) (string, bool) {
	starts := d.starts()
	if n < 0 || n >= len(starts) {
		return "", false
	}
	start, end := d.lineBounds(starts, n)
	return d.Text[start:end], true
}

// Offset converts an LSP position to a byte offset.
//
// Out-of-range positions are clamped rather than rejected, because clients
// legitimately send them: a character past the end of a line means the line
// end (LSP spec), and a line past the last one is how some clients express
// "end of document" in a range. A character that lands inside a surrogate
// pair snaps to the start of that code point so a slice never splits it.
// Only negative coordinates are an error.
func (d *Document) Offset(p protocol.Position) (int, error) {
	if p.Line < 0 || p.Character < 0 {
		return 0, fmt.Errorf("invalid position %d:%d", p.Line, p.Character)
	}
	starts := d.starts()
	if p.Line >= len(starts) {
		return len(d.Text), nil
	}
	start, end := d.lineBounds(starts, p.Line)
	units := 0
	i := start
	for i < end && units < p.Character {
		r, size := utf8.DecodeRuneInString(d.Text[i:end])
		w := utf16Width(r)
		if units+w > p.Character {
			break
		}
		units += w
		i += size
	}
	return i, nil
}

// Position converts a byte offset to an LSP position. Offsets outside the
// text clamp to its ends; an offset inside a line terminator or a multibyte
// code point reports the position just before it.
func (d *Document) Position(off int) protocol.Position {
	if off < 0 {
		off = 0
	}
	if off > len(d.Text) {
		off = len(d.Text)
	}
	starts := d.starts()
	line := sort.Search(len(starts), func(i int) bool { return starts[i] > off }) - 1
	start, end := d.lineBounds(starts, line)
	if off > end {
		off = end
	}
	return protocol.Position{Line: line, Character: utf16Len(d.Text[start:off], true)}
}

// UTF16Len returns the length of s in UTF-16 code units, the unit LSP
// columns and lengths are measured in. Invalid UTF-8 bytes count as one unit
// each, matching how Offset and Position walk them, so semantic token
// columns computed from a line prefix agree with hover positions.
func UTF16Len(s string) int {
	return utf16Len(s, false)
}

// utf16Len with truncate set ignores a trailing incomplete code point, which
// is what a byte offset pointing into the middle of a character needs.
func utf16Len(s string, truncate bool) int {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if truncate && r == utf8.RuneError && size == 1 && !utf8.FullRuneInString(s[i:]) {
			break
		}
		n += utf16Width(r)
		i += size
	}
	return n
}

func utf16Width(r rune) int {
	if r >= 0x10000 && r <= utf8.MaxRune {
		return 2
	}
	return 1
}
