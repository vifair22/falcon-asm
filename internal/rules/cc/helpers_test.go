package cc

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// run lints text with the given rules (all of this package's when none)
// in the given initial mode and target convention.
func run(t *testing.T, text string, mode asm.Mode, cc string, codes ...string) []lint.Finding {
	t.Helper()
	if len(codes) == 0 {
		codes = allCodes
	}
	var rules []*lint.Rule
	for _, code := range codes {
		r, ok := lint.Lookup(code)
		if !ok {
			t.Fatalf("%s not registered", code)
		}
		rules = append(rules, r)
	}
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	p := asm.Parse(doc, lexer.Tokenize(text), asm.Config{InitialMode: mode})
	return lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: lint.Target{Mode: mode, CC: cc,
		Sources: map[string]string{"cc": "test target"}}, Rules: rules})
}

var allCodes = []string{CodeAnnotation, CodeCalleeSaved, CodeCallerSaved, CodeStackAlign, CodeShadowSpace,
	CodeRedZone, CodeArgUnset, CodeReturnUnset, CodeHandlerRet, CodeHandlerKeep, CodeHandlerErrcode, CodeFrameAlign}

// summary renders findings as "code@line" (one-based), sorted as found.
func summary(fs []lint.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code+"@"+itoa(int(f.Range.Start.Line)+1))
	}
	return strings.Join(out, " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// expect checks the findings' summary and that every message contains
// the given fragments.
func expect(t *testing.T, name string, got []lint.Finding, want string, frags ...string) {
	t.Helper()
	if s := summary(got); s != want {
		t.Errorf("%s: got %q, want %q", name, s, want)
		for _, f := range got {
			t.Logf("  %s: %s", f.Code, f.Message)
		}
		return
	}
	for _, fr := range frags {
		found := false
		for _, f := range got {
			if strings.Contains(f.Message, fr) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no message contains %q: %v", name, fr, got)
		}
	}
}
