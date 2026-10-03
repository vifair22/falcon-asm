package target

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

var codes = []string{analysis.CodeModeInstruction, analysis.CodeModeRegister, analysis.CodeLevel, analysis.CodeLevelOutside}

func rules(t *testing.T) []*lint.Rule {
	t.Helper()
	var out []*lint.Rule
	for _, c := range codes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		out = append(out, r)
	}
	return out
}

func prog(text string) *asm.Program {
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	toks := lexer.Tokenize(text)
	p := asm.Parse(doc, toks, asm.Config{})
	p.Doc, p.Toks = doc, toks
	return p
}

func run(t *testing.T, text string, tgt lint.Target) []lint.Finding {
	t.Helper()
	return lint.Run(lint.Input{Prog: prog(text), Lang: lint.LangAsm, Target: tgt, Rules: rules(t)})
}

func apply(t *testing.T, text string, fx lint.Fix) string {
	t.Helper()
	doc := &docstore.Document{Text: text}
	e := fx.Edits[0]
	s, _ := doc.Offset(e.Range.Start)
	en, _ := doc.Offset(e.Range.End)
	return text[:s] + e.NewText + text[en:]
}

func TestModeRules(t *testing.T) {
	text := "\t.code32\n\tmovl $1, %rax\n\tmovl %r8d, %eax\n\t.code16\n\tpush %rsp\n\t.code64\n\tpusha\n"
	got := run(t, text, lint.Target{Mode: 64})
	var summary []string
	for _, f := range got {
		summary = append(summary, f.Code+"@"+string(rune('0'+f.Range.Start.Line)))
	}
	if strings.Join(summary, " ") != "mode-register@1 mode-register@2 mode-register@4 mode-instruction@6" {
		t.Fatalf("findings = %v", got)
	}
	if got[0].Severity != lint.Warning || !strings.Contains(got[0].Message, "set by .code32 at line 1") {
		t.Fatalf("finding 0 = %v", got[0])
	}
	if fx := got[0].Fixes; len(fx) != 1 || fx[0].Title != "Use %eax (the 32-bit register)" ||
		apply(t, text, fx[0]) != strings.Replace(text, "%rax", "%eax", 1) {
		t.Fatalf("32-bit fix = %+v", fx)
	}
	if len(got[1].Fixes) != 0 {
		t.Fatalf("%%r8d has no legacy name, fix = %+v", got[1].Fixes)
	}
	if fx := got[2].Fixes; len(fx) != 1 || apply(t, text, fx[0]) != strings.Replace(text, "%rsp", "%sp", 1) {
		t.Fatalf("16-bit fix = %+v", fx)
	}
}

func TestLevelRules(t *testing.T) {
	text := "\tandn %eax, %ebx, %ecx\n\taesenc %xmm1, %xmm0\n"
	if got := run(t, text, lint.Target{Mode: 64}); len(got) != 0 {
		t.Fatalf("no level target: %v", got)
	}
	got := run(t, text, lint.Target{Mode: 64, Level: 2, Sources: map[string]string{"level": "-march=x86-64-v2"}})
	if len(got) != 2 || got[0].Code != analysis.CodeLevel || got[1].Code != analysis.CodeLevelOutside ||
		got[1].Severity != lint.Info || !strings.Contains(got[0].Message, "-march=x86-64-v2") {
		t.Fatalf("findings = %v", got)
	}
	// The in-file comment wins, as in the analysis itself.
	if got := run(t, "# falcon-asm: level=v3\n"+text, lint.Target{Mode: 64, Level: 1}); len(got) != 1 || got[0].Code != analysis.CodeLevelOutside {
		t.Fatalf("with a v3 comment: %v", got)
	}
}

// A caller that already analyzed the document seeds the result, and the
// rules report from it instead of analyzing again.
func TestSeed(t *testing.T) {
	m := lint.NewMemo()
	seeded := analysis.Result{Diagnostics: []protocol.Diagnostic{{
		Range: protocol.Range{End: protocol.Position{Character: 3}}, Code: analysis.CodeModeInstruction, Message: "seeded",
	}}}
	Seed(m, seeded)
	got := lint.Run(lint.Input{Prog: prog("\tnop\n"), Lang: lint.LangAsm, Rules: rules(t), Memo: m})
	if len(got) != 1 || got[0].Message != "seeded" {
		t.Fatalf("findings = %v", got)
	}
	// A program without a document reports nothing.
	if got := lint.Run(lint.Input{Prog: &asm.Program{}, Lang: lint.LangAsm, Rules: rules(t)}); len(got) != 0 {
		t.Fatalf("no document: %v", got)
	}
}

func TestAnalysisConfig(t *testing.T) {
	cfg := AnalysisConfig(lint.Target{Mode: 16, Level: 3, Sources: map[string]string{"level": "x"}}, map[string]string{"A": "1"})
	if cfg.InitialMode != 0 || cfg.Level != 3 || cfg.LevelSource != "x" || cfg.Macros["A"] != "1" {
		t.Fatalf("unsourced mode = %+v", cfg)
	}
	cfg = AnalysisConfig(lint.Target{Mode: 16, Sources: map[string]string{"mode": "compile flags"}}, nil)
	if cfg.InitialMode != analysis.Mode16 {
		t.Fatalf("sourced mode = %+v", cfg)
	}
}
