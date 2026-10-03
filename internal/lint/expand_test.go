package lint

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func parsed(text string) *asm.Program {
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	return asm.Parse(doc, lexer.Tokenize(text), asm.Config{})
}

// hltRule reports every hlt, with a fix and a related location, so the
// mapping onto the invocation can be checked.
func hltRule(code string) *Rule {
	r := testRule(code)
	r.Check = func(c *Context) {
		for i, s := range c.Prog.Stmts {
			if s.Kind == asm.Instr && s.Mnemonic == "hlt" && !s.InMacroDef {
				c.ReportFinding(Finding{
					Range: c.Prog.Range(s.Start, s.End), Message: "hlt in mode " + string(rune('0'+int(s.Mode)/16)),
					Fixes:   []Fix{NewFix("x", c.InsertBefore(i, "nop; "))},
					Related: []protocol.Location{{URI: c.Prog.Doc.URI}},
				})
			}
		}
	}
	return r
}

const macroText = "\t.macro stop n\n\t.rept \\n\n\thlt\n\t.endr\n\t.endm\n" +
	"\t.macro twice\n\tstop 1\n\tstop 1\n\t.endm\n" +
	"\tstop 1\n\t.code16\n\ttwice /* c */\n\thlt\n"

func TestExpansionFindingsMapToInvocation(t *testing.T) {
	r := hltRule("x-hlt")
	register(t, r)
	got := Run(Input{Prog: parsed(macroText), Lang: LangAsm, Target: Target{Mode: 64}, Rules: []*Rule{r}, ExpandMacros: true})
	var lines []string
	for _, f := range got {
		lines = append(lines, f.String())
	}
	want := []string{
		"9:1-9:7 warning x-hlt: in expansion of stop: hlt in mode 4",
		"11:1-11:6 warning x-hlt: in expansion of twice: hlt in mode 1",
		"12:1-12:4 warning x-hlt: hlt in mode 1",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(lines, "\n"))
	}
	for _, f := range got[:2] {
		if f.Fixes != nil || f.Related != nil {
			t.Fatalf("expansion finding kept fixes or related: %+v", f)
		}
	}
	// Off unless asked for.
	if got := Run(Input{Prog: parsed(macroText), Lang: LangAsm, Rules: []*Rule{r}}); len(got) != 1 {
		t.Fatalf("without ExpandMacros: %v", got)
	}
}

func TestExpansionSkipsWholeDocumentRules(t *testing.T) {
	skip := hltRule("x-skip")
	skip.SkipExpansions = true
	needsFlow := hltRule("x-flow")
	needsFlow.NeedsFlow = true
	flowLazy := testRule("x-lazy")
	var sawNil bool
	flowLazy.Check = func(c *Context) {
		if cfg, res := c.Flow(); cfg == nil && res == nil {
			sawNil = true
		}
	}
	register(t, skip, needsFlow, flowLazy)
	text := "\t.macro m\n\thlt\n\t.endm\n\tm\n"
	got := Run(Input{Prog: parsed(text), Lang: LangAsm, Rules: []*Rule{skip, needsFlow, flowLazy}, ExpandMacros: true})
	for _, f := range got {
		if strings.Contains(f.Message, "in expansion") {
			t.Fatalf("rule ran on an expansion: %v", f)
		}
	}
	if !sawNil {
		t.Fatal("Flow in an expansion should be nil")
	}
}

func TestExpansionCap(t *testing.T) {
	r := hltRule("x-cap")
	register(t, r)
	var b strings.Builder
	b.WriteString("\t.macro m\n\thlt\n\t.endm\n")
	for range MaxExpansions + 50 {
		b.WriteString("\tm\n")
	}
	got := Run(Input{Prog: parsed(b.String()), Lang: LangAsm, Rules: []*Rule{r}, ExpandMacros: true})
	if len(got) != MaxExpansions {
		t.Fatalf("%d findings, want %d", len(got), MaxExpansions)
	}
	// An empty expansion and a recursive macro end.
	text := "\t.macro e\n\t.endm\n\te\n\t.macro r\n\thlt\n\tr\n\t.endm\n\tr\n"
	got = Run(Input{Prog: parsed(text), Lang: LangAsm, Rules: []*Rule{r}, ExpandMacros: true})
	if len(got) != 1 || !strings.Contains(got[0].Message, "in expansion of r") {
		t.Fatalf("recursive: %v", got)
	}
}

func TestWorkspaceAccessor(t *testing.T) {
	var seen Workspace = nopWS{}
	r := testRule("x-ws")
	var got Workspace
	r.Check = func(c *Context) { got = c.Workspace() }
	register(t, r)
	Run(Input{Prog: testProg(), Lang: LangAsm, Rules: []*Rule{r}, Workspace: seen})
	if got != seen {
		t.Fatal("Workspace not passed through")
	}
	if (&Context{}).Workspace() != nil {
		t.Fatal("zero context has a workspace")
	}
}

type nopWS struct{}

func (nopWS) Defined(string) bool                          { return false }
func (nopWS) Referenced(string) bool                       { return false }
func (nopWS) GlobalDefinitions(string) []protocol.Location { return nil }
func (nopWS) Complete() bool                               { return true }
