package docstore

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// mixed exercises every width class plus both common line endings:
//
//	a        1 byte,  1 unit
//	e-acute  2 bytes, 1 unit
//	b        1 byte,  1 unit
//	emoji    4 bytes, 2 units (surrogate pair)
//	c        1 byte,  1 unit
//
// Byte layout: a0 e1-2 b3 emoji4-7 c8 CR9 LF10 x11 y12 LF13 LF14 l15 a16 s17 t18.
const mixed = "a\u00e9b\U0001F600c\r\nxy\n\nlast"

func pos(line, char int) protocol.Position {
	return protocol.Position{Line: line, Character: char}
}

func TestOffset(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		p       protocol.Position
		want    int
		wantErr bool
	}{
		{"start", mixed, pos(0, 0), 0, false},
		{"after ascii", mixed, pos(0, 1), 1, false},
		{"after 2-byte", mixed, pos(0, 2), 3, false},
		{"before astral", mixed, pos(0, 3), 4, false},
		{"inside surrogate pair snaps back", mixed, pos(0, 4), 4, false},
		{"after astral", mixed, pos(0, 5), 8, false},
		{"line end before CRLF", mixed, pos(0, 6), 9, false},
		{"past line end clamps before CRLF", mixed, pos(0, 100), 9, false},
		{"second line", mixed, pos(1, 0), 11, false},
		{"second line end", mixed, pos(1, 2), 13, false},
		{"second line past end", mixed, pos(1, 3), 13, false},
		{"empty line", mixed, pos(2, 0), 14, false},
		{"empty line past end", mixed, pos(2, 5), 14, false},
		{"last line", mixed, pos(3, 0), 15, false},
		{"end of text", mixed, pos(3, 4), 19, false},
		{"line past end is end of text", mixed, pos(4, 0), 19, false},
		{"far line past end", mixed, pos(99, 7), 19, false},
		{"lone CR terminates a line", "a\rbc", pos(1, 1), 3, false},
		{"trailing LF makes empty last line", "x\n", pos(1, 0), 2, false},
		{"empty text", "", pos(0, 3), 0, false},
		{"invalid utf8 byte is one unit", "a\xffb", pos(0, 2), 2, false},
		{"negative line", mixed, pos(-1, 0), 0, true},
		{"negative character", mixed, pos(0, -1), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, d := range []*Document{newDocument("u", 1, tt.text), {Text: tt.text}} {
				got, err := d.Offset(tt.p)
				if (err != nil) != tt.wantErr {
					t.Fatalf("Offset(%v) err = %v, wantErr %v", tt.p, err, tt.wantErr)
				}
				if !tt.wantErr && got != tt.want {
					t.Fatalf("Offset(%v) = %d, want %d (indexed=%v)", tt.p, got, tt.want, d.lineStarts != nil)
				}
			}
		})
	}
}

func TestPosition(t *testing.T) {
	tests := []struct {
		name string
		text string
		off  int
		want protocol.Position
	}{
		{"start", mixed, 0, pos(0, 0)},
		{"after ascii", mixed, 1, pos(0, 1)},
		{"inside 2-byte rune", mixed, 2, pos(0, 1)},
		{"after 2-byte rune", mixed, 3, pos(0, 2)},
		{"before astral", mixed, 4, pos(0, 3)},
		{"inside astral", mixed, 6, pos(0, 3)},
		{"after astral", mixed, 8, pos(0, 5)},
		{"at CR", mixed, 9, pos(0, 6)},
		{"between CR and LF", mixed, 10, pos(0, 6)},
		{"second line", mixed, 11, pos(1, 0)},
		{"at LF", mixed, 13, pos(1, 2)},
		{"empty line", mixed, 14, pos(2, 0)},
		{"last line", mixed, 15, pos(3, 0)},
		{"end", mixed, 19, pos(3, 4)},
		{"past end clamps", mixed, 100, pos(3, 4)},
		{"negative clamps", mixed, -5, pos(0, 0)},
		{"lone CR", "a\rbc", 3, pos(1, 1)},
		{"after trailing LF", "x\n", 2, pos(1, 0)},
		{"invalid utf8 byte", "a\xffb", 2, pos(0, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, d := range []*Document{newDocument("u", 1, tt.text), {Text: tt.text}} {
				if got := d.Position(tt.off); got != tt.want {
					t.Fatalf("Position(%d) = %v, want %v", tt.off, got, tt.want)
				}
			}
		})
	}
}

// Every offset that is a code point boundary outside a line terminator must
// survive a round trip; that is the property the server relies on.
func TestRoundTrip(t *testing.T) {
	texts := []string{
		mixed,
		"mov %eax, %ebx\r\n\t# \u00e9t\u00e9\r\n\U0001F680\U0001F680x\n",
		"\r\r\n\n",
		"",
	}
	for _, text := range texts {
		d := newDocument("u", 1, text)
		for off := 0; off <= len(text); off++ {
			if off < len(text) && off > 0 && !isBoundary(text, off) {
				continue
			}
			if off > 0 && off <= len(text) && text[off-1] == '\r' && off < len(text) && text[off] == '\n' {
				continue
			}
			p := d.Position(off)
			back, err := d.Offset(p)
			if err != nil {
				t.Fatal(err)
			}
			if back != off {
				t.Errorf("text %q: offset %d -> %v -> %d", text, off, p, back)
			}
		}
	}
}

func isBoundary(s string, i int) bool {
	return s[i]&0xC0 != 0x80
}

func TestLines(t *testing.T) {
	d := newDocument("u", 1, mixed)
	if n := d.LineCount(); n != 4 {
		t.Fatalf("LineCount = %d, want 4", n)
	}
	want := []string{"a\u00e9b\U0001F600c", "xy", "", "last"}
	for i, w := range want {
		got, ok := d.Line(i)
		if !ok || got != w {
			t.Errorf("Line(%d) = %q, %v; want %q", i, got, ok, w)
		}
	}
	for _, n := range []int{-1, 4} {
		if _, ok := d.Line(n); ok {
			t.Errorf("Line(%d) ok, want false", n)
		}
	}
	lit := &Document{Text: "a\nb"}
	if lit.LineCount() != 2 {
		t.Fatalf("literal LineCount = %d", lit.LineCount())
	}
}

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"movl", 4},
		{"\u00e9t\u00e9", 3},
		{"\U0001F600", 2},
		{"a\U0001F600b", 4},
		{"\xff\xfe", 2},
		{"\xe2\x82", 2}, // truncated code point: each stray byte is one unit
	}
	for _, tt := range tests {
		if got := UTF16Len(tt.in); got != tt.want {
			t.Errorf("UTF16Len(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func rng(sl, sc, el, ec int) *protocol.Range {
	return &protocol.Range{Start: pos(sl, sc), End: pos(el, ec)}
}

func TestChange(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		changes []protocol.TextDocumentContentChangeEvent
		want    string
		wantErr bool
	}{
		{
			name:    "insert",
			initial: "mov %eax\n",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 3, 0, 3), Text: "l"}},
			want:    "movl %eax\n",
		},
		{
			name:    "delete",
			initial: "movl %eax\n",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 3, 0, 4), Text: ""}},
			want:    "mov %eax\n",
		},
		{
			name:    "replace across lines with CRLF",
			initial: "a\r\nb\r\nc",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 1, 2, 0), Text: "-"}},
			want:    "a-c",
		},
		{
			name:    "edits apply in order against the evolving text",
			initial: "abc",
			changes: []protocol.TextDocumentContentChangeEvent{
				{Range: rng(0, 0, 0, 0), Text: "\n"}, // "\nabc": abc moves to line 1
				{Range: rng(1, 1, 1, 2), Text: "B"},  // only correct if applied after the first
			},
			want: "\naBc",
		},
		{
			name:    "edit after astral char uses utf16 columns",
			initial: "# \U0001F600 x",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 5, 0, 6), Text: "y"}},
			want:    "# \U0001F600 y",
		},
		{
			name:    "replace astral char",
			initial: "\U0001F600!",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 0, 0, 2), Text: "\u00e9"}},
			want:    "\u00e9!",
		},
		{
			name:    "range-less change replaces everything",
			initial: "old text\n",
			changes: []protocol.TextDocumentContentChangeEvent{{Text: "new"}},
			want:    "new",
		},
		{
			name:    "full replace then incremental",
			initial: "zzz",
			changes: []protocol.TextDocumentContentChangeEvent{
				{Text: "abc"},
				{Range: rng(0, 3, 0, 3), Text: "d"},
			},
			want: "abcd",
		},
		{
			name:    "end of document expressed as next line",
			initial: "ab\n",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 2, 5, 0), Text: "!"}},
			want:    "ab!",
		},
		{
			name:    "reversed range rejected",
			initial: "abc",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 2, 0, 1), Text: "x"}},
			wantErr: true,
		},
		{
			name:    "bad start rejected",
			initial: "abc",
			changes: []protocol.TextDocumentContentChangeEvent{{Range: rng(-1, 0, 0, 1), Text: "x"}},
			wantErr: true,
		},
		{
			name:    "bad end rejected and earlier edits discarded",
			initial: "abc",
			changes: []protocol.TextDocumentContentChangeEvent{
				{Range: rng(0, 0, 0, 1), Text: "X"},
				{Range: rng(0, 0, 0, -1), Text: "x"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New()
			s.Open("file:///a.S", 1, tt.initial)
			err := s.Change("file:///a.S", 2, tt.changes)
			d, ok := s.Get("file:///a.S")
			if !ok {
				t.Fatal("document vanished")
			}
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if d.Text != tt.initial || d.Version != 1 {
					t.Fatalf("failed change mutated document: %q v%d", d.Text, d.Version)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Text != tt.want || d.Version != 2 {
				t.Fatalf("got %q v%d, want %q v2", d.Text, d.Version, tt.want)
			}
			// The cached line index must describe the new text.
			if fresh := lineStarts(d.Text); fmt.Sprint(fresh) != fmt.Sprint(d.lineStarts) {
				t.Fatalf("stale line index %v, want %v", d.lineStarts, fresh)
			}
		})
	}
}

func TestStoreLifecycle(t *testing.T) {
	s := New()
	const uri = "file:///boot.S"
	if _, ok := s.Get(uri); ok {
		t.Fatal("Get on empty store succeeded")
	}
	if err := s.Change(uri, 2, nil); err == nil {
		t.Fatal("Change on unopened document succeeded")
	}
	s.Open(uri, 1, "cli\n")
	old, _ := s.Get(uri)

	if err := s.Change(uri, 2, []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 0, 0, 3), Text: "sti"}}); err != nil {
		t.Fatal(err)
	}
	if old.Text != "cli\n" || old.Version != 1 {
		t.Fatalf("snapshot mutated: %q v%d", old.Text, old.Version)
	}

	// An empty change list still records the new version.
	if err := s.Change(uri, 3, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Get(uri)
	if d.Text != "sti\n" || d.Version != 3 || d.URI != uri {
		t.Fatalf("got %+v", d)
	}
	if p := d.Position(len(d.Text)); p != pos(1, 0) {
		t.Fatalf("version-only update lost line index: %v", p)
	}

	s.Open(uri, 9, "reopened")
	if d, _ := s.Get(uri); d.Text != "reopened" || d.Version != 9 {
		t.Fatalf("reopen did not replace: %+v", d)
	}

	s.Close(uri)
	if _, ok := s.Get(uri); ok {
		t.Fatal("document still present after Close")
	}
	s.Close(uri) // closing twice is harmless
}

func TestStoreConcurrent(t *testing.T) {
	s := New()
	const uri = "file:///c.S"
	s.Open(uri, 0, "")
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 100 {
				ch := []protocol.TextDocumentContentChangeEvent{{Range: rng(0, 0, 0, 0), Text: fmt.Sprintf("%d.%d\n", w, i)}}
				if err := s.Change(uri, i, ch); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				if d, ok := s.Get(uri); ok {
					_ = d.Position(len(d.Text))
				}
			}
		}()
	}
	wg.Wait()
	d, _ := s.Get(uri)
	if got := strings.Count(d.Text, "\n"); got != 400 {
		t.Fatalf("lost edits: %d lines, want 400", got)
	}
}

// TestEditLineIndex checks the patched line index against a full rebuild
// over many random edits, with every terminator kind and edits that split
// and join "\r\n" pairs.
func TestEditLineIndex(t *testing.T) {
	alphabet := []string{"a", "b", "\n", "\r", "\r\n", "xy", "", "\n\n", "\r\r"}
	seed := uint64(1)
	rnd := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	for trial := range 300 {
		var b strings.Builder
		for range rnd(40) {
			b.WriteString(alphabet[rnd(len(alphabet))])
		}
		d := newDocument("u", 1, b.String())
		for step := range 30 {
			start := rnd(len(d.Text) + 1)
			end := start + rnd(len(d.Text)-start+1)
			var ins strings.Builder
			for range rnd(4) {
				ins.WriteString(alphabet[rnd(len(alphabet))])
			}
			d = d.edit(step+2, start, end, ins.String())
			want := lineStarts(d.Text)
			if !slices.Equal(d.lineStarts, want) {
				t.Fatalf("trial %d step %d: text %q\ngot  %v\nwant %v", trial, step, d.Text, d.lineStarts, want)
			}
		}
	}
}
