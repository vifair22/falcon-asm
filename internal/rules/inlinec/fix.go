package inlinec

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
)

// Fixes edit the C source directly (the LangC run's document), using the
// spans inlineasm recorded: the clobber literals, the section colons, the
// constraint literals and the statement's own extent. Each returns a byte
// range and replacement inside the statement, or ok == false when the
// statement's shape is not one it can edit safely.

// cEdit is a replacement of src[start:end].
type cEdit struct {
	start, end int
	text       string
}

// clobberEdit adds the quoted names to the statement's clobber list,
// creating the clobber section (and an empty input section before it)
// when the statement has none yet.
func clobberEdit(src string, st *inlineasm.Stmt, names []string) (cEdit, bool) {
	e, ok := clobberEditRaw(src, st, names)
	if !ok || !verifyClobbers(src, st, e, names) {
		return cEdit{}, false
	}
	return e, true
}

// verifyClobbers re-reads the edited statement on its own and checks it
// now lists exactly the old clobbers plus the new ones. The scanner is an
// approximation of C, and a statement it misread (an unterminated literal
// in an operand, say) would otherwise get an edit that breaks the build.
func verifyClobbers(src string, st *inlineasm.Stmt, e cEdit, names []string) bool {
	if e.start < st.Start || e.end > st.End || e.start > e.end {
		return false
	}
	edited := src[st.Start:e.start] + e.text + src[e.end:st.End]
	got := inlineasm.Extract(edited)
	if len(got) == 0 || got[0].Start != 0 || got[0].End != len(edited) || got[0].Partial || got[0].Unterminated {
		return false
	}
	want := append(append([]string(nil), st.Clobbers...), names...)
	if len(got[0].Clobbers) != len(want) {
		return false
	}
	for i := range want {
		if got[0].Clobbers[i] != want[i] {
			return false
		}
	}
	return true
}

func clobberEditRaw(src string, st *inlineasm.Stmt, names []string) (cEdit, bool) {
	if len(names) == 0 || st.Basic || st.Unterminated || st.Partial || st.End < 1 || st.End > len(src) ||
		src[st.End-1] != ')' || len(st.Colons) == 0 {
		return cEdit{}, false
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + n + `"`
	}
	list := strings.Join(quoted, ", ")
	if len(st.Colons) >= 3 {
		if n := len(st.ClobberSpans); n > 0 && n == len(st.Clobbers) {
			at := st.ClobberSpans[n-1].End
			if at <= st.Start || at >= st.End {
				return cEdit{}, false
			}
			return cEdit{at, at, ", " + list}, true
		}
		at := st.Colons[2] + 1
		if at <= st.Start || at >= st.End || src[at-1] != ':' {
			return cEdit{}, false
		}
		return cEdit{at, at, " " + list}, true
	}
	prefix := " : "
	if len(st.Colons) == 1 {
		prefix = " : : "
	}
	return cEdit{insertBeforeClose(src, st), insertBeforeClose(src, st), prefix + list}, true
}

// insertBeforeClose is where a new section goes: right after the last
// token before the closing parenthesis, so "(x)\n\t);" becomes
// "(x) : \"rbx\"\n\t);". When that token ends a // comment the insertion
// would land inside it, so it goes directly before the parenthesis.
func insertBeforeClose(src string, st *inlineasm.Stmt) int {
	closeAt := st.End - 1
	p := closeAt
	for p > st.Start && isSpace(src[p-1]) {
		p--
	}
	lineStart := strings.LastIndexByte(src[:p], '\n') + 1
	if strings.Contains(src[lineStart:p], "//") {
		return closeAt
	}
	return p
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// earlyClobberEdit inserts '&' after the '=' of an output constraint.
func earlyClobberEdit(src string, op *inlineasm.Operand) (cEdit, bool) {
	sp := op.ConstraintSpan
	if sp.Start < 0 || sp.End > len(src) || sp.End-sp.Start < 2 {
		return cEdit{}, false
	}
	i := strings.IndexByte(src[sp.Start:sp.End], '=')
	if i < 0 {
		return cEdit{}, false
	}
	at := sp.Start + i + 1
	return cEdit{at, at, "&"}, true
}

// volatileEdit adds the volatile qualifier after the asm keyword, in the
// keyword's own spelling family (asm volatile, __asm__ __volatile__).
func volatileEdit(src string, st *inlineasm.Stmt) (cEdit, bool) {
	if st.Start < 0 || st.Start >= len(src) {
		return cEdit{}, false
	}
	end := st.Start
	for end < len(src) && (src[end] == '_' || src[end] >= 'a' && src[end] <= 'z') {
		end++
	}
	switch src[st.Start:end] {
	case "asm":
		return cEdit{end, end, " volatile"}, true
	case "__asm__", "__asm":
		return cEdit{end, end, " __volatile__"}, true
	}
	return cEdit{}, false
}
