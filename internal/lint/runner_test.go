package lint

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

const progText = "\tcld\n\tmovl %eax, %ebx\n"

// testProg is progText as asm.Parse would model it, built by hand so the
// runner is tested independently of the parser.
func testProg() *asm.Program {
	doc := &docstore.Document{URI: "file:///t.S", Text: progText}
	return &asm.Program{
		Doc:  doc,
		Toks: lexer.Tokenize(progText),
		Stmts: []asm.Stmt{
			{Kind: asm.Instr, Line: 0, Start: 1, End: 4, Mnemonic: "cld", MnemStart: 1},
			{Kind: asm.Instr, Line: 1, Start: 6, End: 21, Mnemonic: "movl", MnemStart: 6, Ops: []asm.Operand{
				{Kind: asm.OpReg, Text: "%eax", Start: 11, End: 15, RegName: "eax"},
				{Kind: asm.OpReg, Text: "%ebx", Start: 17, End: 21, RegName: "ebx"},
			}},
		},
	}
}

func rng(l1, c1, l2, c2 int) protocol.Range {
	return protocol.Range{Start: protocol.Position{Line: l1, Character: c1}, End: protocol.Position{Line: l2, Character: c2}}
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func TestRunReportsSortsAndDedupes(t *testing.T) {
	report := testRule("x-report")
	report.Check = func(c *Context) {
		c.ReportOp(1, 1, "dst")
		c.ReportOp(1, 0, "src")
		c.ReportStmt(0, "stmt", NewFix("Insert", c.InsertBefore(0, "nop; ")))
		c.ReportRange(6, 10, "mnemonic")
		c.Report(rng(0, 1, 0, 4), "stmt")                                            // duplicate of the ReportStmt
		c.ReportFinding(Finding{Code: "spoofed", Severity: Error, Message: "range"}) // code and severity are forced
		// Out of range: ignored, not trusted.
		c.ReportStmt(9, "x")
		c.ReportOp(1, 5, "x")
		c.ReportOp(-1, 0, "x")
		c.ReportRange(5, 999, "x")
	}
	register(t, report)
	got := Run(Input{Prog: testProg(), Lang: LangAsm, Rules: []*Rule{report}})
	var msgs []string
	for _, f := range got {
		if f.Code != "x-report" || f.Severity != Warning {
			t.Fatalf("finding %v", f)
		}
		msgs = append(msgs, f.Message)
	}
	if strings.Join(msgs, ",") != "range,stmt,mnemonic,src,dst" {
		t.Fatalf("order = %v", msgs)
	}
	if got[1].String() != "0:1-0:4 warning x-report: stmt" || len(got[1].Fixes) != 1 {
		t.Fatalf("stmt finding = %v %+v", got[1], got[1].Fixes)
	}
}

func TestRunSelectsRules(t *testing.T) {
	ran := map[string]int{}
	mk := func(code string, f func(r *Rule)) *Rule {
		r := testRule(code)
		r.Check = func(c *Context) { ran[code]++; c.ReportStmt(0, code) }
		f(r)
		return r
	}
	off := mk("x-off", func(r *Rule) {})
	cOnly := mk("x-conly", func(r *Rule) { r.Langs = LangC })
	listing := mk("x-listing", func(r *Rule) { r.NeedsListing = true })
	flowed := mk("x-flow", func(r *Rule) {
		r.NeedsFlow = true
		r.Check = func(c *Context) {
			ran["x-flow"]++
			if g, res := c.Flow(); g == nil || res == nil || g.Prog != c.Prog {
				t.Errorf("flow = %v %v", g, res)
			}
		}
	})
	lazy := mk("x-lazy", func(r *Rule) {
		r.Check = func(c *Context) {
			ran["x-lazy"]++
			g1, _ := c.Flow()
			g2, _ := c.Flow()
			if g1 == nil || g1 != g2 {
				t.Error("lazy flow not shared")
			}
		}
	})
	defaultOff := mk("x-defoff", func(r *Rule) { r.Default = Off })
	register(t, off, cOnly, listing, flowed, lazy, defaultOff)
	rules := []*Rule{off, cOnly, listing, flowed, lazy, defaultOff}
	cfg := FileConfig{Rules: map[string]Severity{"x-off": Off, "x-defoff": Error}}

	got := Run(Input{Prog: testProg(), Lang: LangAsm, Rules: rules, Config: cfg})
	if len(got) != 1 || got[0].Code != "x-defoff" || got[0].Severity != Error {
		t.Fatalf("findings = %v", got)
	}
	if ran["x-off"] != 0 || ran["x-conly"] != 0 || ran["x-listing"] != 0 || ran["x-flow"] != 1 || ran["x-lazy"] != 1 {
		t.Fatalf("ran = %v", ran)
	}
	got = Run(Input{Prog: testProg(), Lang: LangAsm, Rules: rules, Config: cfg, Listing: &toolchain.AsmResult{}})
	if ran["x-listing"] != 1 || len(got) != 2 {
		t.Fatalf("with a listing: ran %v, findings %v", ran, got)
	}
	// Every registered rule when none are listed, and nothing for no
	// program.
	if Run(Input{Lang: LangAsm}) != nil {
		t.Fatal("findings without a program")
	}
	all := Run(Input{Prog: testProg(), Lang: LangC, Config: cfg})
	if len(all) != 1 || all[0].Code != "x-conly" {
		t.Fatalf("all rules on C = %v", all)
	}
}

func TestRuleListingAccess(t *testing.T) {
	r := testRule("x-seeslisting")
	var seen *toolchain.AsmResult
	r.NeedsListing = true
	r.Check = func(c *Context) { seen = c.Listing }
	plain := testRule("x-nolisting")
	var plainSeen *toolchain.AsmResult
	plain.Check = func(c *Context) { plainSeen = c.Listing }
	register(t, r, plain)
	res := &toolchain.AsmResult{}
	Run(Input{Prog: testProg(), Lang: LangAsm, Rules: []*Rule{r, plain}, Listing: res})
	if seen != res || plainSeen != nil {
		t.Fatalf("listing: %p %p", seen, plainSeen)
	}
}

func TestPanickingRule(t *testing.T) {
	boom := testRule("x-boom")
	boom.Check = func(c *Context) {
		c.ReportStmt(0, "half done")
		panic("rule bug")
	}
	fine := testRule("x-fine")
	fine.Check = func(c *Context) { c.ReportStmt(1, "fine") }
	register(t, boom, fine)
	panicked.Delete("rule x-boom")
	var log logSink
	for range 3 {
		got := Run(Input{Prog: testProg(), Lang: LangAsm, Rules: []*Rule{boom, fine}, Log: log.logf})
		if len(got) != 1 || got[0].Code != "x-fine" {
			t.Fatalf("findings = %v", got)
		}
	}
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "rule x-boom panicked") || !strings.Contains(log.lines[0], "rule bug") {
		t.Fatalf("log = %q", log.lines)
	}
}

func TestMapping(t *testing.T) {
	r := testRule("x-map")
	r.Langs = LangInline
	r.Check = func(c *Context) {
		c.ReportStmt(0, "kept", NewFix("a", c.ReplaceRange(1, 4, "std")), NewFix("b", c.ReplaceRange(1, 1, "x")))
		c.ReportStmt(1, "dropped")
		c.ReportFinding(Finding{Range: rng(0, 1, 0, 2), Message: "related", Related: []protocol.Location{
			{URI: "file:///t.S", Range: rng(0, 0, 0, 1)}, {URI: "file:///other.S", Range: rng(5, 0, 5, 1)}, {Range: rng(1, 0, 1, 1)},
		}})
	}
	register(t, r)
	shift := func(p protocol.Range) (protocol.Range, bool) {
		if p.Start.Line == 1 {
			return protocol.Range{}, false
		}
		p.Start.Line += 10
		p.End.Line += 10
		return p, true
	}
	in := Input{Prog: testProg(), Lang: LangInline, Rules: []*Rule{r}, MapRange: shift}
	// Sorted by position: "related" (0:1-0:2) before "kept" (0:1-0:4).
	got := Run(in)
	if len(got) != 2 || got[1].Message != "kept" || got[1].Range.Start.Line != 10 || got[1].Fixes != nil {
		t.Fatalf("no MapEdit: %v", got)
	}
	// Related locations in this document are mapped (and dropped when they
	// do not map); other documents' are kept as they are.
	rel := got[0].Related
	if len(rel) != 2 || rel[0].Range.Start.Line != 10 || rel[1].URI != "file:///other.S" {
		t.Fatalf("related = %+v", rel)
	}
	in.MapEdit = func(e protocol.TextEdit) (protocol.TextEdit, bool) {
		if e.NewText == "x" {
			return e, false
		}
		e.Range, _ = shift(e.Range)
		return e, true
	}
	got = Run(in)
	if fx := got[1].Fixes; len(fx) != 1 || fx[0].Title != "a" || fx[0].Edits[0].Range.Start.Line != 10 {
		t.Fatalf("fixes = %+v", fx)
	}
}

func TestBatchFinish(t *testing.T) {
	r := testRule("x-batch")
	r.Check = func(c *Context) { c.ReportStmt(0, "a") }
	register(t, r)
	prog := testProg()
	sup := AsmSuppressions(&docstore.Document{Text: "\tcld\n\tnop /* falcon-asm: ignore=x-batch */\n"}, nil)
	var b Batch
	b.Add(Input{Prog: prog, Lang: LangAsm, Rules: []*Rule{r}})
	b.Add(Input{Prog: prog, Lang: LangAsm, Rules: []*Rule{r}})
	got := b.Finish(sup, FileConfig{})
	if len(got) != 2 || got[0].Code != "x-batch" || got[1].Code != UnusedSuppressionCode {
		t.Fatalf("finish = %v", got)
	}
	// unused-suppression configured off.
	var b2 Batch
	b2.Add(Input{Prog: prog, Lang: LangAsm, Rules: []*Rule{r}})
	sup = AsmSuppressions(&docstore.Document{Text: "\tcld\n\tnop /* falcon-asm: ignore=x-batch */\n"}, nil)
	if got := b2.Finish(sup, FileConfig{Rules: map[string]Severity{UnusedSuppressionCode: Off}}); len(got) != 1 {
		t.Fatalf("finish with unused off = %v", got)
	}
	var empty Batch
	if got := empty.Finish(nil, FileConfig{}); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}
}

func TestMemo(t *testing.T) {
	m := NewMemo()
	calls := 0
	f := func() any { calls++; return calls }
	first := m.Get("k", f)
	second := m.Get("k", f)
	if first != 1 || second != 1 || calls != 1 {
		t.Fatal("memo recomputed")
	}
	m.Set("k", 99)
	if m.Get("k", f) != 1 {
		t.Fatal("Set replaced a computed value")
	}
	m.Set("s", "seed")
	if m.Get("s", f) != "seed" {
		t.Fatal("seed ignored")
	}
	var zero Memo
	if zero.Get("z", func() any { return 3 }) != 3 {
		t.Fatal("zero memo")
	}
	// The rules of one run share it; a context without a run computes.
	r := testRule("x-memo")
	var seen []any
	r.Check = func(c *Context) { seen = append(seen, c.Memo("key", func() any { return len(seen) })) }
	register(t, r)
	mm := NewMemo()
	mm.Set("key", "seeded")
	Run(Input{Prog: testProg(), Lang: LangAsm, Rules: []*Rule{r}, Memo: mm})
	if len(seen) != 1 || seen[0] != "seeded" {
		t.Fatalf("seen = %v", seen)
	}
	if (&Context{}).Memo("k", func() any { return 5 }) != 5 {
		t.Fatal("context without a run")
	}
}

func TestContextAccessors(t *testing.T) {
	var c Context
	if c.Lang() != LangAsm || c.Inline() != nil || c.Config().Rules != nil || c.Rule() != nil || c.Severity() != Off {
		t.Fatal("zero context")
	}
	if g, r := c.Flow(); g != nil || r != nil {
		t.Fatal("flow without a run")
	}
	c.Report(rng(0, 0, 0, 1), "nowhere") // no sink: dropped, no panic
	r := testRule("x-acc")
	r.Langs = LangC
	r.Check = func(c *Context) {
		if c.Lang() != LangC || c.Rule().Code != "x-acc" || c.Severity() != Hint || c.Config().Rules["x-acc"] != Hint {
			t.Errorf("accessors: %v %v %v", c.Lang(), c.Rule(), c.Severity())
		}
		if len(c.Inline()) != 0 {
			t.Error("inline")
		}
	}
	register(t, r)
	Run(Input{Prog: testProg(), Lang: LangC, Rules: []*Rule{r}, Config: FileConfig{Rules: map[string]Severity{"x-acc": Hint}}})
}

func TestFixHelpers(t *testing.T) {
	c := &Context{Prog: testProg()}
	apply := func(e protocol.TextEdit) string { return applyEdit(t, c.Prog.Doc, e) }
	if got := apply(c.InsertLineAbove(1, "cld")); got != "\tcld\n\tcld\n\tmovl %eax, %ebx\n" {
		t.Fatalf("InsertLineAbove = %q", got)
	}
	if got := apply(c.InsertBefore(1, "rep; ")); got != "\tcld\n\trep; movl %eax, %ebx\n" {
		t.Fatalf("InsertBefore = %q", got)
	}
	if got := apply(c.ReplaceRange(12, 15, "ecx")); got != "\tcld\n\tmovl %ecx, %ebx\n" {
		t.Fatalf("ReplaceRange = %q", got)
	}
	if got := apply(c.ReplaceRange(-5, 1, "")); got != progText[1:] {
		t.Fatalf("clamped ReplaceRange = %q", got)
	}
	if got := apply(c.ReplaceRange(20, 999, "")); got != "\tcld\n\tmovl %eax, %eb" {
		t.Fatalf("clamped end = %q", got)
	}
	if got := apply(c.InsertBefore(7, "x")); got != "x"+progText {
		t.Fatalf("InsertBefore out of range = %q", got)
	}
	if got := apply(c.InsertLineAbove(7, "x")); got != "x\n"+progText {
		t.Fatalf("InsertLineAbove out of range = %q", got)
	}
	crlf := &asm.Program{Doc: &docstore.Document{Text: "  a:\r\n    hlt\r\n"}, Stmts: []asm.Stmt{{Start: 2}, {Start: 10}}}
	c = &Context{Prog: crlf}
	if got := applyEdit(t, crlf.Doc, c.InsertLineAbove(1, "cli")); got != "  a:\r\n    cli\r\n    hlt\r\n" {
		t.Fatalf("CRLF = %q", got)
	}
	if fx := NewFix("t", c.InsertBefore(0, "x")); fx.Title != "t" || len(fx.Edits) != 1 {
		t.Fatalf("NewFix = %+v", fx)
	}
	empty := &Context{}
	if e := empty.ReplaceRange(0, 3, "x"); e.NewText != "x" || e.Range != (protocol.Range{}) {
		t.Fatalf("no program = %+v", e)
	}
}

func TestDiagnosticAndOverlaps(t *testing.T) {
	f := Finding{Code: "x-d", Severity: Info, Range: rng(2, 4, 2, 8), Message: "m",
		Related: []protocol.Location{{URI: "file:///g.S", Range: rng(9, 0, 9, 1)}}}
	d := f.Diagnostic()
	if d.Code != "x-d" || d.Source != Source || d.Severity != protocol.SeverityInformation ||
		d.CodeDescription == nil || d.CodeDescription.Href != DocsBase+"#x-d" || len(d.RelatedInformation) != 1 {
		t.Fatalf("diagnostic = %+v", d)
	}
	for _, tt := range []struct {
		r    protocol.Range
		want bool
	}{
		{rng(2, 4, 2, 4), true}, {rng(2, 8, 2, 8), true}, {rng(2, 9, 2, 9), false}, {rng(0, 0, 2, 3), false},
		{rng(0, 0, 5, 0), true}, {rng(2, 5, 2, 6), true}, {rng(3, 0, 3, 0), false},
	} {
		if got := f.Overlaps(tt.r); got != tt.want {
			t.Errorf("Overlaps(%+v) = %v", tt.r, got)
		}
	}
}

func TestMarkdownAndText(t *testing.T) {
	r := testRule("x-doc")
	r.NeedsFlow, r.NeedsListing = true, true
	r.Doc.Summary = "Summary with a | pipe."
	r.Doc.Bad = "\tbad\n"
	register(t, r)
	md := Markdown()
	for _, want := range []string{
		"| [x-doc](#x-doc) | 3 | warning | Summary with a \\| pipe. |",
		"\n## x-doc\n", "- Section: 3 (Dataflow)", "- Needs: dataflow, the assembler listing",
		"Bad:\n\n```asm\n\tbad\n```", "Good:\n\n```asm\ngood\n```",
		"\n## unused-suppression\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	txt := r.Text()
	if !strings.Contains(txt, "x-doc (section 3, Dataflow; default warning; runs on assembly)") ||
		!strings.Contains(txt, "Bad:\n    \tbad\n") || !strings.Contains(txt, DocURL("x-doc")) {
		t.Fatalf("text = %q", txt)
	}
}

type fakeTables struct{ tag string }

func (fakeTables) Port(uint64) (string, string, bool)         { return "", "", false }
func (fakeTables) MSR(uint64) (string, string, bool)          { return "", "", false }
func (fakeTables) BIOS(uint64, uint64) (string, string, bool) { return "", "", false }

func TestTables(t *testing.T) {
	t.Cleanup(func() { RegisterTables(nil) })
	RegisterTables(nil)
	if tb, err := LoadTables(nil); tb != nil || err != nil {
		t.Fatal("tables without a loader")
	}
	calls := 0
	RegisterTables(func(raw json.RawMessage) (Tables, error) {
		calls++
		if string(raw) == "bad" {
			return nil, errors.New("bad tables")
		}
		return fakeTables{tag: string(raw)}, nil
	})
	a, _ := LoadTables(json.RawMessage(`{"ports":[]}`))
	b, _ := LoadTables(json.RawMessage(`{"ports":[]}`))
	if a.(fakeTables).tag != `{"ports":[]}` || a != b || calls != 1 {
		t.Fatalf("cached tables: %v %v calls %d", a, b, calls)
	}
	if _, err := LoadTables(json.RawMessage("bad")); err == nil {
		t.Fatal("loader error lost")
	}
	r := testRule("x-tables")
	var seen Tables
	r.Check = func(c *Context) { seen = c.Tables }
	register(t, r)
	Run(Input{Prog: testProg(), Lang: LangAsm, Rules: []*Rule{r}, Tables: a})
	if seen != a {
		t.Fatal("tables not passed to the rule")
	}
}
