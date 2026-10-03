package hw

import (
	"os"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

func prog(text string, mode asm.Mode) *asm.Program {
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	toks := lexer.Tokenize(text)
	p := asm.Parse(doc, toks, asm.Config{InitialMode: mode})
	p.Doc, p.Toks = doc, toks
	return p
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type opts struct {
	mode    asm.Mode
	listing *toolchain.AsmResult
	tables  lint.Tables
	cpu     string
}

// run lints text with the named rules only.
func run(t *testing.T, text string, o opts, codes ...string) []lint.Finding {
	t.Helper()
	var rules []*lint.Rule
	for _, c := range codes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		rules = append(rules, r)
	}
	if o.mode == 0 {
		o.mode = asm.Mode16
	}
	if o.tables == nil {
		o.tables = hwdb.Default()
	}
	return lint.Run(lint.Input{Prog: prog(text, o.mode), Lang: lint.LangAsm, Target: lint.Target{Mode: o.mode, CPU: o.cpu},
		Listing: o.listing, Tables: o.tables, Rules: rules})
}

func codesOf(fs []lint.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code+"@"+itoaLine(f.Range.Start.Line))
	}
	return strings.Join(out, " ")
}

func itoaLine(n int) string {
	if n < 0 {
		return "-"
	}
	s := ""
	for {
		s = string(rune('0'+n%10)) + s
		n /= 10
		if n == 0 {
			return s
		}
	}
}

func mustHave(t *testing.T, fs []lint.Finding, code, sub string) lint.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Code == code && strings.Contains(f.Message, sub) {
			return f
		}
	}
	t.Fatalf("no %s finding containing %q in %v", code, sub, fs)
	return lint.Finding{}
}

func lineOf(text, sub string) int {
	i := strings.Index(text, sub)
	if i < 0 {
		return -1
	}
	return strings.Count(text[:i], "\n")
}

// Every rule's documented Bad example triggers it and the Good one does
// not: the documentation cannot drift from the behaviour.
func TestDocExamples(t *testing.T) {
	for _, code := range []string{"far-jump-mode", "selector-type", "descriptor-invalid", "gdt-limit", "pte-reserved",
		"sipi-trampoline", "sipi-unproven", "bios-struct", "msr-reserved-write", "cr-reserved-write"} {
		r, _ := lint.Lookup(code)
		mode := asm.Mode16
		if strings.HasPrefix(r.Doc.Bad, "\t.code32") || code == "msr-reserved-write" || code == "cr-reserved-write" {
			mode = asm.Mode32
		}
		if got := run(t, r.Doc.Bad, opts{mode: mode}, code); len(got) == 0 {
			t.Errorf("%s: Bad example not reported", code)
		}
		if got := run(t, r.Doc.Good, opts{mode: mode}, code); len(got) != 0 {
			t.Errorf("%s: Good example reported: %v", code, got)
		}
	}
}
