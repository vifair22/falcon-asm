package toolchain

import (
	"encoding/hex"
	"strconv"
	"strings"
)

// ListingLine is what the assembler produced for one main-file line.
type ListingLine struct {
	Line      int    // 0-based line in the main file
	Offset    uint64 // offset within Section of the first byte; valid when HasOffset
	HasOffset bool   // false for lines that emit nothing (labels, blank, directives)
	Section   string // section in effect when the line was assembled
	// Bytes as listed. gas shows at most lhs-width bytes per row and a
	// limited number of continuation rows, so a long fill or string is
	// truncated here; offsets, not len(Bytes), give a line's true size.
	Bytes  []byte
	Source string // the source text as gas saw it (after cpp)
	// Unknown is set on every line when gas could not finalize addresses
	// (it prints ???? after an error); Bytes are still valid, offsets not.
	Unknown bool
}

// rawListing is one numbered row of a gas listing, continuation rows
// already folded in. Macro and .rept expansion rows (shown with `> ` under
// -alm) are separate rows with the invoking line's number.
type rawListing struct {
	num       int // 1-based line in the assembled file
	addr      uint64
	hasAddr   bool
	unknown   bool // address printed as ????
	bytes     []byte
	source    string
	expansion bool
	// included marks a row from a file gas pulled in with .include: such
	// rows are numbered by that file's own lines, so mapping them through
	// the main file's line map would pin their bytes on unrelated main
	// lines. See markIncluded.
	included bool
}

// parseGasListing reads a listing produced with -al or -alm. The layout is
// `%4d %04x HEXBYTES<tab>source`, continuation rows `%4d      HEXBYTES`
// with no tab, and a `GAS LISTING <file> ... page N` header (preceded by a
// form feed) plus blank lines at every page break.
func parseGasListing(text string) []rawListing {
	var out []rawListing
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(strings.TrimLeft(l, "\f"), "GAS LISTING") {
			continue
		}
		left, src, hasTab := strings.Cut(l, "\t")
		f := strings.Fields(left)
		if len(f) == 0 {
			continue
		}
		num, err := strconv.Atoi(f[0])
		if err != nil {
			// "****  Error: ..." rows and symbol tables.
			continue
		}
		if !hasTab {
			// Continuation of the previous row's bytes.
			if len(out) > 0 && out[len(out)-1].num == num && len(f) >= 2 {
				if b, err := hex.DecodeString(f[1]); err == nil {
					out[len(out)-1].bytes = append(out[len(out)-1].bytes, b...)
				}
			}
			continue
		}
		r := rawListing{num: num, source: src}
		if s, ok := strings.CutPrefix(src, "> "); ok {
			r.source, r.expansion = s, true
		} else if src == ">" {
			r.source, r.expansion = "", true
		}
		if len(f) >= 2 {
			if a, err := strconv.ParseUint(f[1], 16, 64); err == nil {
				r.addr, r.hasAddr = a, true
			} else if strings.Trim(f[1], "?") == "" {
				r.unknown = true
			}
		}
		if len(f) >= 3 {
			if b, err := hex.DecodeString(f[2]); err == nil {
				r.bytes = b
			}
		}
		out = append(out, r)
	}
	return out
}

// sectionTracker follows gas's section state machine closely enough to
// label listing rows: .text/.data/.bss, .section, .previous (swap with the
// prior section) and .pushsection/.popsection (which save and restore both
// the current and the previous section).
type sectionTracker struct {
	cur, prev string
	stack     [][2]string
}

func newSectionTracker() *sectionTracker { return &sectionTracker{cur: ".text"} }

func (t *sectionTracker) set(name string) {
	if name == t.cur {
		return
	}
	t.prev, t.cur = t.cur, name
}

// apply updates the state from one source row's statements.
func (t *sectionTracker) apply(src string) {
	for _, st := range statements(src) {
		dir, arg := st, ""
		if i := strings.IndexAny(st, " \t"); i >= 0 {
			dir, arg = st[:i], strings.TrimSpace(st[i+1:])
		}
		switch strings.ToLower(dir) {
		case ".text", ".data", ".bss":
			t.set(strings.ToLower(dir))
		case ".section":
			if n := sectionName(arg); n != "" {
				t.set(n)
			}
		case ".pushsection":
			t.stack = append(t.stack, [2]string{t.cur, t.prev})
			if n := sectionName(arg); n != "" {
				t.set(n)
			}
		case ".popsection":
			if k := len(t.stack); k > 0 {
				t.cur, t.prev = t.stack[k-1][0], t.stack[k-1][1]
				t.stack = t.stack[:k-1]
			}
		case ".previous":
			if t.prev != "" {
				t.cur, t.prev = t.prev, t.cur
			}
		}
	}
}

// sectionName takes the first operand of .section, which may be quoted.
func sectionName(arg string) string {
	if strings.HasPrefix(arg, "\"") {
		if name, _, ok := cutCString(arg[1:]); ok {
			return name
		}
		return ""
	}
	name, _, _ := strings.Cut(arg, ",")
	return strings.TrimSpace(name)
}

// statements splits a source row into statements: `;` separates them, `#`
// starts a comment, labels (`name:`) are dropped, and both are ignored
// inside string literals.
func statements(src string) []string {
	var out []string
	var b strings.Builder
	inStr := false
	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		for {
			// Strip leading labels so `buf: .bss` still sees the directive.
			i := strings.IndexByte(s, ':')
			if i <= 0 || strings.ContainsAny(s[:i], " \t\"") {
				break
			}
			s = strings.TrimSpace(s[i+1:])
		}
		if s != "" {
			out = append(out, s)
		}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inStr:
			if c == '\\' && i+1 < len(src) {
				b.WriteByte(c)
				i++
				c = src[i]
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == ';':
			flush()
			continue
		case c == '#':
			flush()
			return out
		}
		b.WriteByte(c)
	}
	flush()
	return out
}

// buildListing folds raw rows into one ListingLine per main-file line.
// Section tracking runs over every row, including rows from included files
// and macro expansions, because a section switch there still applies to the
// main-file lines that follow. A line whose expansion emits into several
// sections keeps the first emitting row's section and offset, and only the
// bytes emitted there.
func buildListing(rows []rawListing, lm *LineMap) []ListingLine {
	var out []ListingLine
	idx := map[int]int{} // main line -> index in out
	t := newSectionTracker()
	unknown := false
	for _, r := range rows {
		unknown = unknown || r.unknown
		sec := t.cur
		t.apply(r.source)
		if r.hasAddr && t.cur != sec {
			// A row such as `.section .x; .byte 1` emits into the new section.
			sec = t.cur
		}
		if r.included || lm == nil || r.num-1 < 0 || r.num-1 >= len(lm.Lines) {
			continue
		}
		o := lm.Lines[r.num-1]
		if o.File == "" || o.File != lm.Main {
			continue
		}
		i, ok := idx[o.Line]
		if !ok && !r.hasAddr && len(r.bytes) == 0 && strings.TrimSpace(r.source) == "" {
			// Blank rows are cpp's stand-ins for removed lines and comments.
			continue
		}
		if !ok {
			idx[o.Line] = len(out)
			ll := ListingLine{Line: o.Line, Section: sec}
			if !r.expansion {
				ll.Source = r.source
			}
			out = append(out, ll)
			i = len(out) - 1
		}
		ll := &out[i]
		if r.unknown && !ll.HasOffset && (len(ll.Bytes) == 0 || sec == ll.Section) {
			ll.Section = sec
			ll.Bytes = append(ll.Bytes, r.bytes...)
			continue
		}
		if !r.hasAddr {
			continue
		}
		if !ll.HasOffset {
			ll.HasOffset, ll.Offset, ll.Section = true, r.addr, sec
			ll.Bytes = append(ll.Bytes, r.bytes...)
			continue
		}
		if sec == ll.Section {
			ll.Bytes = append(ll.Bytes, r.bytes...)
		}
	}
	if unknown {
		for i := range out {
			out[i].Unknown, out[i].HasOffset, out[i].Offset = true, false, 0
		}
	}
	return out
}

// isLocationMove reports whether a statement moves the location counter
// forward with fill: `.org expr` or `. = expr`.
func isLocationMove(src string) bool {
	for _, st := range statements(src) {
		low := strings.ToLower(st)
		if strings.HasPrefix(low, ".org") && (len(low) == 4 || low[4] == ' ' || low[4] == '\t') {
			return true
		}
		if rest, ok := strings.CutPrefix(st, "."); ok {
			rest = strings.TrimLeft(rest, " \t")
			if strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") {
				return true
			}
		}
	}
	return false
}

// PadBytes returns how many fill bytes a `.org` or `. = expr` line inserts:
// the distance from where the line starts emitting to the next listed
// offset in the same section. gas prints an address on such a line only
// when it fills, so a line without one pads nothing. ok is false when line
// is not a location move, is not in the listing, has Unknown offsets
// (the file had assembly errors), or is the last emitting
// line of its section (the end is then unknown from the listing alone; see
// AsmResult.PadBytes, which also knows section sizes).
func PadBytes(lines []ListingLine, line int) (int, bool) {
	return padBytes(lines, line, nil)
}

func padBytes(lines []ListingLine, line int, sizes map[string]uint64) (int, bool) {
	at := -1
	for i, l := range lines {
		if l.Line == line {
			at = i
			break
		}
	}
	if at < 0 || lines[at].Unknown || !isLocationMove(lines[at].Source) {
		return 0, false
	}
	l := lines[at]
	if !l.HasOffset {
		return 0, true
	}
	for _, n := range lines[at+1:] {
		if n.HasOffset && n.Section == l.Section && n.Offset >= l.Offset {
			return int(n.Offset - l.Offset), true
		}
	}
	if sz, ok := sizes[l.Section]; ok && sz >= l.Offset {
		return int(sz - l.Offset), true
	}
	return 0, false
}
