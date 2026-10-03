package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// gasAt is a gas error on line.
func gasAt(line int, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{Range: lineRange(line), Severity: protocol.SeverityError, Source: toolchain.Source, Message: msg}
}

// Roadmap item 78: a gas diagnostic on a line where falcon-asm reports a
// warning or an error is dropped; Information does not shadow.
func TestGasDedupe(t *testing.T) {
	h := newHarness(t, setup{opts: `{"level":"v3"}`})
	text := "\t.code32\n" + // 0
		"\tpushl %rax\n" + // 1: mode-register warning
		"\taesenc %xmm1, %xmm0\n" + // 2: level-outside information
		"\tbogus\n" // 3: nothing of ours
	path, uri := h.file("d.S", text)
	hdr := filepath.Join(h.dir, "inc.h")
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc", Mode: 32}}, nil }
	h.tc.defaultA = &toolchain.AsmResult{Diagnostics: map[string][]protocol.Diagnostic{
		path: {gasAt(1, "gas: operand type mismatch"), gasAt(2, "gas: aesenc"), gasAt(3, "gas: no such instruction")},
		hdr:  {gasAt(0, "gas: header")},
	}}
	h.open(uri, "falcon-asm", 1, text)
	p := h.waitPub(uri, hasMsg("no such instruction"))
	var got []string
	for _, d := range p.Diagnostics {
		got = append(got, d.Source+":"+d.Code+"@"+string(rune('0'+d.Range.Start.Line)))
		if d.Source == lint.Source && (d.CodeDescription == nil || !strings.HasSuffix(d.CodeDescription.Href, "RULES.md#"+d.Code)) {
			t.Errorf("no documentation link: %+v", d)
		}
	}
	want := "falcon-asm:mode-register@1 falcon-asm:feature-unguarded@2 falcon-asm:level-outside@2 gas:@2 gas:@3"
	if strings.Join(got, " ") != want {
		t.Fatalf("published %q\nwant %q", got, want)
	}

	// Gas diagnostics other files' runs put on a file are shadowed too.
	_, hdrURI := h.file("inc.h", "\tpushl %rax\n")
	h.open(hdrURI, "falcon-asm", 1, "\t.code32\n\tpushl %rax\n")
	h.e.mu.Lock()
	h.e.ext[hdrURI] = map[string][]protocol.Diagnostic{uri: {gasAt(1, "gas: shadowed"), gasAt(0, "gas: kept")}}
	h.e.mu.Unlock()
	h.e.publish(hdrURI)
	hp := h.waitPub(hdrURI, hasMsg("gas: kept"))
	for _, d := range hp.Diagnostics {
		if d.Message == "gas: shadowed" {
			t.Fatalf("ext diagnostic not shadowed: %+v", hp.Diagnostics)
		}
	}
}

func titles(as []protocol.CodeAction) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Title)
	}
	return out
}

func TestCodeActions(t *testing.T) {
	h := newHarness(t, setup{opts: `{"toolchain":false}`})
	text := "\t.code32\n\tpushl %rax /* falcon-asm: ignore=level */\n\tnop\n"
	_, uri := h.file("ca.S", text)
	doc := h.open(uri, "falcon-asm", 1, text)
	line1 := protocol.Range{Start: protocol.Position{Line: 1, Character: 9}, End: protocol.Position{Line: 1, Character: 9}}
	acts := h.e.CodeActions(doc, line1, protocol.CodeActionContext{})
	want := "Use %eax (the 32-bit register)|Ignore mode-register on this line"
	if strings.Join(titles(acts), "|") != want {
		t.Fatalf("actions = %q", titles(acts))
	}
	fix, ignore := acts[0], acts[1]
	if !fix.IsPreferred || fix.Kind != protocol.CodeActionKindQuickFix || len(fix.Diagnostics) != 1 || fix.Diagnostics[0].Code != "mode-register" {
		t.Fatalf("fix = %+v", fix)
	}
	if got := applyEdits(t, doc, ignore.Edit.Changes[uri]); !strings.Contains(got, "ignore=level,mode-register */") {
		t.Fatalf("ignore edit gives %q", got)
	}
	if got := applyEdits(t, doc, fix.Edit.Changes[uri]); !strings.Contains(got, "pushl %eax") {
		t.Fatalf("fix edit gives %q", got)
	}
	// The unused level suppression is reported but offers no ignore.
	all := h.e.CodeActions(doc, protocol.Range{End: protocol.Position{Line: 9}}, protocol.CodeActionContext{Only: []string{"quickfix"}})
	if len(all) != 2 {
		t.Fatalf("whole-document actions = %q", titles(all))
	}
	for _, only := range [][]string{{"refactor"}, {"source.fixAll"}} {
		if got := h.e.CodeActions(doc, line1, protocol.CodeActionContext{Only: only}); got != nil {
			t.Fatalf("only %v = %q", only, titles(got))
		}
	}
	if got := h.e.CodeActions(doc, protocol.Range{Start: protocol.Position{Line: 2}, End: protocol.Position{Line: 2, Character: 2}}, protocol.CodeActionContext{}); got != nil {
		t.Fatalf("clean line = %q", titles(got))
	}

	// C: the ignore comment is a // comment on the C line.
	src := "void f(void) { __asm__(\"pushl %rax\"); }\n"
	// Its own directory: FindDB results are cached per directory.
	_, curi := h.file("c/ca.c", src)
	h.e.cfg.FindDB = func(string) (FlagDB, error) {
		return fakeDB{filepath.Join(h.dir, "c", "ca.c"): {Compiler: "gcc", Mode: 32}}, nil
	}
	cdoc := h.open(curi, "c", 1, src)
	acts = h.e.CodeActions(cdoc, protocol.Range{End: protocol.Position{Line: 1}}, protocol.CodeActionContext{Only: []string{""}})
	if len(acts) != 2 {
		t.Fatalf("C actions = %q", titles(acts))
	}
	if got := applyEdits(t, cdoc, acts[1].Edit.Changes[curi]); got != "void f(void) { __asm__(\"pushl %rax\"); } // falcon-asm: ignore=mode-register\n" {
		t.Fatalf("C ignore gives %q", got)
	}
	if got := applyEdits(t, cdoc, acts[0].Edit.Changes[curi]); got != strings.Replace(src, "%rax", "%eax", 1) {
		t.Fatalf("C fix gives %q", got)
	}
}

func applyEdits(t *testing.T, doc *docstore.Document, edits []protocol.TextEdit) string {
	t.Helper()
	text := doc.Text
	for i := len(edits) - 1; i >= 0; i-- {
		d := &docstore.Document{Text: text}
		s, err1 := d.Offset(edits[i].Range.Start)
		e, err2 := d.Offset(edits[i].Range.End)
		if err1 != nil || err2 != nil {
			t.Fatalf("edit %+v", edits[i])
		}
		text = text[:s] + edits[i].NewText + text[e:]
	}
	return text
}

// .falcon-asm.json: found above the file, its problems published on its
// own URI, and an edited buffer of it applied live.
func TestConfigFile(t *testing.T) {
	h := newHarness(t, setup{opts: `{"toolchain":false}`})
	if _, found := lint.FindConfig(h.dir); found {
		t.Skip("a .falcon-asm.json exists above the temp dir")
	}
	cfgPath, cfgURI := h.file(lint.ConfigFile, `{"rules": {"mode-register": "off", "no-such": "error"}}`)
	text := "\t.code32\n\tpushl %rax\n\tpusha\n"
	_, uri := h.file("sub/c.S", text)
	doc := h.open(uri, "falcon-asm", 1, text)

	cp := h.waitPub(cfgURI, hasMsg(`unknown rule code "no-such"`))
	if cp.Diagnostics[0].Severity != protocol.SeverityWarning || cp.Diagnostics[0].Code != "config" {
		t.Fatalf("config problems = %+v", cp.Diagnostics)
	}
	if p := h.waitPub(uri, version(1)); len(p.Diagnostics) != 0 {
		t.Fatalf("mode-register is off, got %+v", p.Diagnostics)
	}

	// The editor opens the configuration and turns the rule into an error.
	cdoc := h.open(cfgURI, "json", 1, `{"rules": {"mode-register": "error"}}`)
	h.waitAll(map[string]func(protocol.PublishDiagnosticsParams) bool{
		cfgURI: func(p protocol.PublishDiagnosticsParams) bool { return p.Version != nil && empty(p) },
		uri: func(p protocol.PublishDiagnosticsParams) bool {
			return len(p.Diagnostics) == 1 && p.Diagnostics[0].Severity == protocol.SeverityError
		},
	})
	// Nothing but configuration is served for the file.
	if hv, _ := h.e.Hover(cdoc, protocol.Position{}); hv != nil || h.e.CodeActions(cdoc, protocol.Range{}, protocol.CodeActionContext{}) != nil ||
		h.e.SemanticTokens(cdoc) != nil {
		t.Fatal("assembly features served for the configuration")
	}
	// A syntax error in the buffer leaves the defaults in force.
	h.change(cfgURI, 2, `{"rules": `)
	h.waitAll(map[string]func(protocol.PublishDiagnosticsParams) bool{
		cfgURI: hasMsg("invalid JSON"),
		uri: func(p protocol.PublishDiagnosticsParams) bool {
			return len(p.Diagnostics) == 1 && p.Diagnostics[0].Severity == protocol.SeverityWarning
		},
	})

	// Closing the buffer goes back to the file on disk; deleting the file
	// drops the configuration.
	h.e.Close(cfgURI)
	h.waitAll(map[string]func(protocol.PublishDiagnosticsParams) bool{uri: empty, cfgURI: hasMsg("no-such")})
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	h.open(cfgURI, "json", 1, `{}`)
	h.e.Close(cfgURI)
	h.waitAll(map[string]func(protocol.PublishDiagnosticsParams) bool{
		uri:    func(p protocol.PublishDiagnosticsParams) bool { return len(p.Diagnostics) == 1 },
		cfgURI: func(p protocol.PublishDiagnosticsParams) bool { return p.Version == nil && empty(p) },
	})
	if s := h.e.snapshotFor(doc); s.fileCfg.Rules != nil {
		t.Fatalf("configuration left after the file went: %+v", s.fileCfg)
	}
}

// waitAll waits until every URI has had a publish satisfying its
// condition, in whatever order they arrive: a configuration change
// publishes the file synchronously and its documents from timers.
func (h *harness) waitAll(conds map[string]func(protocol.PublishDiagnosticsParams) bool) {
	h.t.Helper()
	left := len(conds)
	done := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for left > 0 {
		select {
		case p := <-h.pubs:
			if ok := conds[p.URI]; ok != nil && !done[p.URI] && ok(p) {
				done[p.URI] = true
				left--
			}
		case <-deadline:
			h.t.Fatalf("publishes still missing: have %v of %d", done, len(conds))
		}
	}
}

// Hardware tables come from the loader hwdb registers, with the
// configuration's "tables" merged in.
func TestTablesFromConfig(t *testing.T) {
	h := newHarness(t, setup{opts: `{"toolchain":false}`})
	if _, found := lint.FindConfig(h.dir); found {
		t.Skip("a .falcon-asm.json exists above the temp dir")
	}
	h.file(lint.ConfigFile, `{"tables": {"ports": [1]}}`)
	var raws []string
	lint.RegisterTables(func(raw json.RawMessage) (lint.Tables, error) {
		raws = append(raws, string(raw))
		if string(raw) != "" {
			return nil, os.ErrInvalid
		}
		return nil, nil
	})
	t.Cleanup(func() { lint.RegisterTables(nil) })
	_, uri := h.file("t.S", "\tnop\n")
	h.open(uri, "falcon-asm", 1, "\tnop\n")
	if len(raws) != 2 || raws[0] != `{"ports": [1]}` || !strings.Contains(h.log.String(), "hardware tables") {
		t.Fatalf("loader saw %q, log %q", raws, h.log.String())
	}
}
