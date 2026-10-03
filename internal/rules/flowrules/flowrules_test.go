package flowrules

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

var allCodes = []string{
	CodeDFUnknown, CodeSegmentUnset, CodeStackImbalance, CodePushfPopf, CodeCliSti, CodePartialRead,
	CodeUninitRead, CodeIFCritical, CodeFlagsClobbered, CodeBIOSCFUnchecked, CodeBIOSClobber,
	CodeBIOSClobberQuirk, CodeUnreachable, CodeFallIntoData, CodeBusySpin, CodeSelfModifying,
	CodeFeatureUnguarded,
}

func ourRules() []*lint.Rule {
	var out []*lint.Rule
	for _, c := range allCodes {
		r, ok := lint.Lookup(c)
		if !ok {
			panic("rule " + c + " not registered")
		}
		out = append(out, r)
	}
	return out
}

func prog(text string, mode asm.Mode) *asm.Program {
	// Through a store, so the document has its line index, as in the
	// engine: a bare Document rescans the text on every position.
	s := docstore.New()
	s.Open("file:///t.S", 1, text)
	doc, _ := s.Get("file:///t.S")
	toks := lexer.Tokenize(text)
	return asm.Parse(doc, toks, asm.Config{InitialMode: mode})
}

// lintText runs the rules of this package (or only code, when given) over
// text assembled in mode with target t.
func lintText(t *testing.T, text string, mode asm.Mode, tgt lint.Target, code string) []lint.Finding {
	t.Helper()
	rules := ourRules()
	if code != "" {
		r, _ := lint.Lookup(code)
		rules = []*lint.Rule{r}
	}
	if tgt.Mode == 0 {
		tgt.Mode = mode
	}
	return lint.Run(lint.Input{Prog: prog(text, mode), Lang: lint.LangAsm, Target: tgt, Rules: rules})
}

// only filters findings to one code.
func only(fs []lint.Finding, code string) []lint.Finding {
	var out []lint.Finding
	for _, f := range fs {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

// expect checks that code fires exactly n times on text, each finding
// containing want in its message, and returns the findings.
func expect(t *testing.T, text string, mode asm.Mode, code string, n int, want string) []lint.Finding {
	t.Helper()
	got := only(lintText(t, text, mode, lint.Target{}, ""), code)
	if len(got) != n {
		t.Fatalf("%s: want %d findings, got %d: %v\n%s", code, n, len(got), got, text)
	}
	for _, f := range got {
		if !strings.Contains(f.Message, want) {
			t.Fatalf("%s: message %q lacks %q", code, f.Message, want)
		}
	}
	return got
}

// line is the 1-based line a finding starts on.
func line(f lint.Finding) int { return f.Range.Start.Line + 1 }

// apply applies a fix's edits (non-overlapping, any order) to text.
func apply(t *testing.T, text string, fx lint.Fix) string {
	t.Helper()
	doc := &docstore.Document{Text: text}
	type ed struct {
		s, e int
		txt  string
	}
	var eds []ed
	for _, e := range fx.Edits {
		s, err1 := doc.Offset(e.Range.Start)
		en, err2 := doc.Offset(e.Range.End)
		if err1 != nil || err2 != nil {
			t.Fatalf("edit out of range: %+v", e)
		}
		eds = append(eds, ed{s, en, e.NewText})
	}
	for i := 0; i < len(eds); i++ {
		for j := i + 1; j < len(eds); j++ {
			if eds[j].s > eds[i].s {
				eds[i], eds[j] = eds[j], eds[i]
			}
		}
	}
	for _, e := range eds {
		text = text[:e.s] + e.txt + text[e.e:]
	}
	return text
}

// TestDocExamples checks every rule's documented Bad example triggers it
// and its Good example does not, so docs/RULES.md cannot drift from the
// code.
func TestDocExamples(t *testing.T) {
	for _, r := range ourRules() {
		if r.Doc.Bad == "" || r.Doc.Good == "" || r.Doc.Why == "" || !r.NeedsFlow {
			t.Errorf("%s: incomplete doc or not a flow rule", r.Code)
			continue
		}
		mode := asm.Mode16
		if strings.Contains(r.Doc.Bad, ".code32") {
			mode = asm.Mode32
		}
		if got := only(lintText(t, r.Doc.Bad, mode, lint.Target{}, r.Code), r.Code); len(got) == 0 {
			t.Errorf("%s: Bad example reports nothing:\n%s", r.Code, r.Doc.Bad)
		}
		if got := only(lintText(t, r.Doc.Good, mode, lint.Target{}, r.Code), r.Code); len(got) != 0 {
			t.Errorf("%s: Good example reports %v", r.Code, got)
		}
	}
}
