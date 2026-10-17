package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/server"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// fakeTC is a scripted toolchain. Each call takes the next scripted result
// for its kind; a result with a gate blocks until the gate is closed or the
// context ends, which is how tests order concurrent runs without sleeping.
type fakeTC struct {
	mu       sync.Mutex
	macros   []macroResult
	asm      []asmResult
	calls    []string
	started  chan string // receives "macros"/"assemble" as each call begins; may be nil
	defaultM map[string]toolchain.Macro
	defaultA *toolchain.AsmResult
}

type macroResult struct {
	m    map[string]toolchain.Macro
	err  error
	gate chan struct{}
}

type asmResult struct {
	r    *toolchain.AsmResult
	err  error
	gate chan struct{}
	// ignoreCtx makes the call wait on its gate even after cancellation,
	// like a compiler that does not die promptly.
	ignoreCtx bool
}

func wait(ctx context.Context, gate chan struct{}, ignoreCtx bool) error {
	if gate == nil {
		return nil
	}
	if ignoreCtx {
		<-gate
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeTC) Macros(ctx context.Context, file string, _ toolchain.Flags) (map[string]toolchain.Macro, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "macros "+filepath.Base(file))
	r := macroResult{m: f.defaultM}
	if len(f.macros) > 0 {
		r, f.macros = f.macros[0], f.macros[1:]
	}
	started := f.started
	f.mu.Unlock()
	if started != nil {
		started <- "macros"
	}
	if err := wait(ctx, r.gate, false); err != nil {
		return nil, err
	}
	return r.m, r.err
}

func (f *fakeTC) Assemble(ctx context.Context, file string, _ toolchain.Flags) (*toolchain.AsmResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "assemble "+filepath.Base(file))
	r := asmResult{r: f.defaultA}
	if len(f.asm) > 0 {
		r, f.asm = f.asm[0], f.asm[1:]
	}
	started := f.started
	f.mu.Unlock()
	if started != nil {
		started <- "assemble"
	}
	if err := wait(ctx, r.gate, r.ignoreCtx); err != nil {
		return nil, err
	}
	return r.r, r.err
}

func (f *fakeTC) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeDB answers flags for listed paths.
type fakeDB map[string]toolchain.Flags

func (d fakeDB) Flags(file string) (toolchain.Flags, bool) {
	f, ok := d[file]
	return f, ok
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness is an initialized engine whose publishes land in a channel.
type harness struct {
	t    *testing.T
	e    *Engine
	tc   *fakeTC
	pubs chan protocol.PublishDiagnosticsParams
	log  *syncBuf
	dir  string
}

type setup struct {
	cfg  Config
	opts string // initializationOptions JSON
	root string // workspace root path; "" for none
	db   fakeDB // returned by LoadDB for root when non-nil
}

func newHarness(t *testing.T, s setup) *harness {
	t.Helper()
	h := &harness{t: t, tc: &fakeTC{}, pubs: make(chan protocol.PublishDiagnosticsParams, 256), log: &syncBuf{}, dir: t.TempDir()}
	cfg := s.cfg
	if cfg.Toolchain == nil {
		cfg.Toolchain = h.tc
	}
	if cfg.LoadDB == nil {
		cfg.LoadDB = func(string) (FlagDB, error) {
			if s.db == nil {
				return nil, fs.ErrNotExist
			}
			return s.db, nil
		}
	}
	if cfg.FindDB == nil {
		cfg.FindDB = func(string) (FlagDB, error) { return nil, fs.ErrNotExist }
	}
	cfg.Debounce = time.Nanosecond
	cfg.Log = h.log
	h.e = New(cfg)
	root := ""
	if s.root != "" {
		root = toolchain.PathToURI(s.root)
	}
	h.e.Initialize(server.Session{
		RootURI:     root,
		InitOptions: json.RawMessage(s.opts),
		Publish:     func(p protocol.PublishDiagnosticsParams) { h.pubs <- p },
	})
	t.Cleanup(h.e.Shutdown)
	return h
}

// file writes name under the harness dir and returns its path and URI.
func (h *harness) file(name, text string) (string, string) {
	h.t.Helper()
	p := filepath.Join(h.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return p, toolchain.PathToURI(p)
}

func (h *harness) open(uri, lang string, version int, text string) *docstore.Document {
	d := &docstore.Document{URI: uri, Version: version, Text: text}
	h.e.Open(d, lang)
	return d
}

func (h *harness) change(uri string, version int, text string) *docstore.Document {
	d := &docstore.Document{URI: uri, Version: version, Text: text}
	h.e.Change(d)
	return d
}

// waitPub returns the first publish for uri that satisfies ok, failing
// after a generous timeout. Publishes for other URIs or not matching are
// skipped; versions must never go backwards for one URI.
func (h *harness) waitPub(uri string, ok func(protocol.PublishDiagnosticsParams) bool) protocol.PublishDiagnosticsParams {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case p := <-h.pubs:
			if p.URI == uri && ok(p) {
				return p
			}
		case <-deadline:
			h.t.Fatalf("no matching publish for %s", uri)
		}
	}
}

func hasMsg(sub string) func(protocol.PublishDiagnosticsParams) bool {
	return func(p protocol.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if strings.Contains(d.Message, sub) {
				return true
			}
		}
		return false
	}
}

func version(v int) func(protocol.PublishDiagnosticsParams) bool {
	return func(p protocol.PublishDiagnosticsParams) bool { return p.Version != nil && *p.Version == v }
}

func empty(p protocol.PublishDiagnosticsParams) bool { return len(p.Diagnostics) == 0 }

func TestLanguageOf(t *testing.T) {
	tests := []struct {
		uri, id string
		want    lang
	}{
		{"file:///a.S", "falcon-asm", langAsm},
		{"file:///a.c", "falcon-asm", langAsm},
		{"file:///a.S", "c", langC},
		{"file:///a.S", "cpp", langC},
		{"file:///a.S", "", langAsm},
		{"file:///a.s", "asm", langAsm},
		{"file:///a.asm", "plaintext", langAsm},
		{"file:///a.c", "", langC},
		{"file:///a.H", "unknown", langC},
	}
	for _, tt := range tests {
		if got := languageOf(tt.uri, tt.id); got != tt.want {
			t.Errorf("languageOf(%q, %q) = %v, want %v", tt.uri, tt.id, got, tt.want)
		}
	}
}

func TestLevelSetting(t *testing.T) {
	for in, want := range map[string]int{"": 0, "v1": 1, "V3 ": 3, "v4": 4, "v5": 0, "x86-64-v2": 0} {
		if got, _ := levelSetting(in); int(got) != want {
			t.Errorf("levelSetting(%q) = %d, want %d", in, got, want)
		}
	}
}

// Analysis diagnostics publish on open and on change; a burst of changes
// ends with a publish for the newest version, and versions never go back.
func TestAnalysisPublishing(t *testing.T) {
	h := newHarness(t, setup{})
	_, uri := h.file("a.S", "")
	h.open(uri, "falcon-asm", 1, "\t.code16\n\tpushq %rax\n")
	p := h.waitPub(uri, version(1))
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Source != "falcon-asm" {
		t.Fatalf("open diagnostics = %+v", p.Diagnostics)
	}
	for v := 2; v <= 20; v++ {
		h.change(uri, v, fmt.Sprintf("\t.code64\n\tpushq %%rax\n# %d\n", v))
	}
	last := 0
	for last != 20 {
		p := h.waitPub(uri, func(protocol.PublishDiagnosticsParams) bool { return true })
		if *p.Version < last {
			t.Fatalf("version went back from %d to %d", last, *p.Version)
		}
		last = *p.Version
		if last >= 2 && len(p.Diagnostics) != 0 {
			t.Fatalf("v%d diagnostics = %+v", last, p.Diagnostics)
		}
	}
	h.e.Close(uri)
	h.waitPub(uri, func(p protocol.PublishDiagnosticsParams) bool { return p.Version == nil && empty(p) })
	// Changes and saves for a closed document are ignored.
	h.e.Change(&docstore.Document{URI: uri, Version: 30})
	h.e.Save(uri)
	h.e.Close(uri)
}

// asmFixture is the toolchain world for one assembly file: flags, a macro
// table and an assembly result with errors in the file and in a header,
// an inactive region and a listing.
func asmFixture(h *harness) (path, uri, hdrPath, text string) {
	text = "#include \"defs.h\"\n" + // 0
		"_start:\n" + // 1
		"\tmovw $SEL, %ax\t/* selector */\n" + // 2
		"#if 0\n" + // 3
		"\tdead\n" + // 4
		"#endif\n" + // 5
		"\tbogus\n" + // 6
		"\t. = _start + 16\n" + // 7
		"\t.byte 0x55, 0xAA\n" // 8
	path, uri = h.file("src/boot.S", text)
	hdrPath, _ = h.file("src/defs.h", "#define SEL 0x08\n")
	defLoc := &protocol.Location{URI: toolchain.PathToURI(hdrPath), Range: protocol.Range{Start: protocol.Position{Character: 8}, End: protocol.Position{Character: 11}}}
	h.tc.defaultM = map[string]toolchain.Macro{
		"SEL":      {Value: "0x08", Def: defLoc},
		"__i386__": {Value: "1"},
	}
	h.tc.defaultA = &toolchain.AsmResult{
		Diagnostics: map[string][]protocol.Diagnostic{
			path:    {{Range: lineRange(6), Severity: protocol.SeverityError, Source: toolchain.Source, Message: "no such instruction: `bogus'"}},
			hdrPath: {{Range: lineRange(0), Severity: protocol.SeverityError, Source: toolchain.Source, Message: "header trouble"}},
		},
		Inactive: []protocol.Range{lineRange(4)},
		Listing: []toolchain.ListingLine{
			{Line: 2, Offset: 0, HasOffset: true, Section: ".text", Bytes: []byte{0xb8, 0x08, 0x00}, Source: "movw $0x08, %ax"},
			{Line: 7, Offset: 3, HasOffset: true, Section: ".text", Bytes: []byte{0, 0, 0}, Source: ". = _start + 16"},
			{Line: 8, Offset: 16, HasOffset: true, Section: ".text", Bytes: []byte{0x55, 0xaa}, Source: ".byte 0x55, 0xAA"},
		},
		SectionSizes: map[string]uint64{".text": 18},
	}
	return path, uri, hdrPath, text
}

func lineRange(l int) protocol.Range {
	return protocol.Range{Start: protocol.Position{Line: l}, End: protocol.Position{Line: l, Character: 3}}
}

func TestToolchainRun(t *testing.T) {
	h := newHarness(t, setup{})
	path, _, _, _ := asmFixture(h)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc", Mode: 16}}, nil }
	_, uri, hdrPath, text := asmFixture(h)
	hdrURI := toolchain.PathToURI(hdrPath)

	doc := h.open(uri, "falcon-asm", 1, text)
	p := h.waitPub(uri, hasMsg("bogus"))
	var gas, inactive int
	for _, d := range p.Diagnostics {
		switch {
		case d.Source == toolchain.Source:
			gas++
		case d.Message == inactiveMessage:
			inactive++
			if d.Severity != protocol.SeverityHint || len(d.Tags) != 1 || d.Tags[0] != protocol.TagUnnecessary {
				t.Errorf("inactive diagnostic = %+v", d)
			}
		}
	}
	if gas != 1 || inactive != 1 {
		t.Fatalf("published %+v", p.Diagnostics)
	}
	hp := h.waitPub(hdrURI, hasMsg("header trouble"))
	if hp.Version != nil {
		t.Fatalf("closed header published with a version: %+v", hp)
	}

	// The macro table reaches hover, tokens, definition and completion.
	hv, err := h.e.Hover(doc, protocol.Position{Line: 2, Character: 8})
	if err != nil || hv == nil || !strings.Contains(hv.Contents.Value, "#define SEL 0x08") || !strings.Contains(hv.Contents.Value, "dec `8`") {
		t.Fatalf("hover = %+v %v", hv, err)
	}
	toks := h.e.SemanticTokens(doc)
	if !paints(toks, 2, 7, server.TokenMacro) {
		t.Fatalf("SEL not painted as macro: %v", toks)
	}
	if locs := h.e.Definition(doc, protocol.Position{Line: 2, Character: 8}); len(locs) != 1 || locs[0].URI != toolchain.PathToURI(hdrPath) {
		t.Fatalf("definition = %+v", locs)
	}
	cl := h.e.Completion(doc, protocol.Position{Line: 2, Character: 7})
	if cl == nil || !hasItem(cl, "SEL") || hasItem(cl, "__i386__") {
		t.Fatalf("completion = %+v", cl)
	}

	// Listing hints, inside the requested range only.
	all := protocol.Range{End: protocol.Position{Line: 100}}
	hints := h.e.InlayHints(doc, all)
	labels := hintLabels(hints)
	want := []string{"2:+0x0  b8 08 00", "7:pads 13 bytes", "8:+0x10  55 aa"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("hints = %q", labels)
	}
	if hints[0].Position.Character != len("\tmovw $SEL, %ax") || !hints[0].PaddingLeft || hints[0].Tooltip != "section .text, offset 0x0, 3 bytes" {
		t.Fatalf("hint 0 = %+v", hints[0])
	}
	if got := hintLabels(h.e.InlayHints(doc, protocol.Range{Start: protocol.Position{Line: 7}, End: protocol.Position{Line: 7}})); len(got) != 1 {
		t.Fatalf("ranged hints = %q", got)
	}

	// An edit hides the hints until the next save's run lands.
	doc = h.change(uri, 2, text+"\n")
	if hints := h.e.InlayHints(doc, all); hints != nil {
		t.Fatalf("hints while dirty = %+v", hints)
	}
	// The save's run reads the file from disk: make it match version 2,
	// and drop the header error so the header's diagnostics clear.
	if err := os.WriteFile(path, []byte(text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.tc.mu.Lock()
	h.tc.asm = []asmResult{{r: &toolchain.AsmResult{Listing: h.tc.defaultA.Listing}}}
	h.tc.mu.Unlock()
	h.e.Save(uri)
	h.waitPub(hdrURI, empty)
	h.e.wg.Wait()
	if hints := h.e.InlayHints(doc, all); len(hints) != 3 {
		t.Fatalf("hints after save = %+v", hints)
	}

	// Closing the file clears it.
	h.e.Close(uri)
	h.waitPub(uri, func(p protocol.PublishDiagnosticsParams) bool { return p.Version == nil && empty(p) })
	if calls := h.tc.Calls(); len(calls) != 4 {
		t.Fatalf("toolchain calls = %q", calls)
	}
}

func paints(data []uint32, line, col int, typ uint32) bool {
	l, c := 0, 0
	for i := 0; i+4 < len(data); i += 5 {
		if data[i] != 0 {
			l += int(data[i])
			c = 0
		}
		c += int(data[i+1])
		if l == line && c == col && data[i+3] == typ {
			return true
		}
	}
	return false
}

func hasItem(l *protocol.CompletionList, label string) bool {
	for _, it := range l.Items {
		if it.Label == label {
			return true
		}
	}
	return false
}

func hintLabels(hints []protocol.InlayHint) []string {
	var out []string
	for _, h := range hints {
		out = append(out, fmt.Sprintf("%d:%s", h.Position.Line, h.Label))
	}
	return out
}

// A buffer that differs from the file on disk at open (a restored unsaved
// buffer) gets diagnostics but no listing hints.
func TestDirtyAtOpenAndHintsOff(t *testing.T) {
	for _, tc := range []struct {
		name, opts string
		disk       string
	}{
		{name: "dirty at open", opts: `{}`, disk: "changed on disk\n"},
		{name: "hints off", opts: `{"listingHints":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, setup{opts: tc.opts})
			path, uri, _, text := asmFixture(h)
			h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc"}}, nil }
			if tc.disk != "" {
				h.file("src/boot.S", tc.disk)
			}
			doc := h.open(uri, "falcon-asm", 1, text)
			h.waitPub(uri, hasMsg("bogus"))
			h.e.wg.Wait()
			if hints := h.e.InlayHints(doc, protocol.Range{End: protocol.Position{Line: 99}}); hints != nil {
				t.Fatalf("hints = %+v", hints)
			}
		})
	}
}

func TestToolchainOffAndNoFlags(t *testing.T) {
	h := newHarness(t, setup{opts: `{"toolchain":false}`})
	path, uri, _, text := asmFixture(h)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc"}}, nil }
	h.open(uri, "falcon-asm", 1, text)
	h.e.Save(uri)
	h.waitPub(uri, version(1))
	h.e.wg.Wait()

	// No database at all: logged once, no runs.
	h2 := newHarness(t, setup{})
	_, uri2 := h2.file("x/a.S", "nop\n")
	_, uri3 := h2.file("x/b.S", "nop\n")
	h2.open(uri2, "falcon-asm", 1, "nop\n")
	h2.open(uri3, "falcon-asm", 1, "nop\n")
	h2.e.wg.Wait()
	if n := strings.Count(h2.log.String(), "no compile_commands.json"); n != 1 {
		t.Fatalf("log = %q", h2.log.String())
	}
	// Untitled buffers have no file: no flags lookup, no run.
	h2.open("untitled:Untitled-1", "falcon-asm", 1, "#include \"x.h\"\n")
	h2.waitPub("untitled:Untitled-1", version(1))
	h2.e.wg.Wait()
	if calls := append(h.tc.Calls(), h2.tc.Calls()...); len(calls) != 0 {
		t.Fatalf("toolchain ran: %q", calls)
	}
}

// Errors: a missing compiler is logged once and drops the run; other
// failures are logged and the rest of the run still applies.
func TestToolchainErrors(t *testing.T) {
	h := newHarness(t, setup{})
	path, uri, _, text := asmFixture(h)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc"}}, nil }
	noTC := fmt.Errorf("%w: gcc", toolchain.ErrNoToolchain)
	h.tc.macros = []macroResult{{err: noTC}, {err: noTC}, {err: errors.New("cpp exploded")}, {err: errors.New("cpp exploded")}}
	h.tc.asm = []asmResult{{r: h.tc.defaultA}, {err: errors.New("as exploded")}}
	h.open(uri, "falcon-asm", 1, text)
	h.e.wg.Wait()
	h.e.Save(uri)
	h.e.wg.Wait()
	if n := strings.Count(h.log.String(), "toolchain unavailable"); n != 1 {
		t.Fatalf("log = %q", h.log.String())
	}
	// cpp fails but assembly still yields diagnostics.
	h.e.Save(uri)
	h.waitPub(uri, hasMsg("bogus"))
	h.e.wg.Wait()
	// Both steps fail: logged once per message, nothing published from it.
	h.e.Save(uri)
	h.e.wg.Wait()
	log := h.log.String()
	if strings.Count(log, "cpp exploded") != 1 || strings.Count(log, "as exploded") != 1 {
		t.Fatalf("log = %q", log)
	}

	// A header gets its macros even though it cannot be assembled, and
	// that refusal is not logged.
	h.tc.asm = []asmResult{{err: fmt.Errorf("assemble: %w", toolchain.ErrNotAssembly)}}
	h.tc.macros = []macroResult{{m: map[string]toolchain.Macro{"HDR": {Value: "1"}}}}
	inc, incURI := h.file("src/x.inc", "\tmov $HDR, %ax\n")
	h.e.mu.Lock()
	h.e.found[filepath.Dir(inc)] = fakeDB{path: {Compiler: "gcc"}, inc: {Compiler: "gcc"}}
	h.e.mu.Unlock()
	d := h.open(incURI, "falcon-asm", 1, "\tmov $HDR, %ax\n")
	h.e.wg.Wait()
	if hv, _ := h.e.Hover(d, protocol.Position{Line: 0, Character: 7}); hv == nil || !strings.Contains(hv.Contents.Value, "#define HDR 1") {
		t.Fatalf("header macro hover = %+v", hv)
	}
	if strings.Contains(h.log.String(), "not an assembly source") {
		t.Fatalf("log = %q", h.log.String())
	}
}

// A run that finishes after a newer one started is dropped, so a slow old
// result never replaces a fresh one.
func TestSupersededRunDropped(t *testing.T) {
	h := newHarness(t, setup{})
	path, uri, _, text := asmFixture(h)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc"}}, nil }
	gate1, gate2 := make(chan struct{}), make(chan struct{})
	old := &toolchain.AsmResult{Diagnostics: map[string][]protocol.Diagnostic{path: {{Message: "old result"}}}}
	fresh := &toolchain.AsmResult{Diagnostics: map[string][]protocol.Diagnostic{path: {{Message: "fresh result"}}}}
	h.tc.started = make(chan string, 8)
	h.tc.asm = []asmResult{{r: old, gate: gate1, ignoreCtx: true}, {r: fresh, gate: gate2, ignoreCtx: true}}
	h.open(uri, "falcon-asm", 1, text)
	<-h.tc.started // macros of run 1
	<-h.tc.started // assemble of run 1, now blocked
	h.e.Save(uri)
	<-h.tc.started
	<-h.tc.started
	close(gate2)
	h.waitPub(uri, hasMsg("fresh result"))
	close(gate1)
	h.e.wg.Wait()
	h.e.mu.Lock()
	got := h.e.docs[uri].asm
	h.e.mu.Unlock()
	if got != fresh {
		t.Fatalf("stored result = %+v", got)
	}
}

// Shutdown cancels runs and waits for them; a run that ignores cancellation
// is waited for only up to ShutdownWait.
func TestShutdown(t *testing.T) {
	h := newHarness(t, setup{})
	path, uri, _, text := asmFixture(h)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc"}}, nil }
	h.tc.started = make(chan string, 8)
	h.tc.asm = []asmResult{{gate: make(chan struct{})}}
	h.open(uri, "falcon-asm", 1, text)
	<-h.tc.started
	<-h.tc.started
	h.e.Shutdown()
	h.e.wg.Wait()
	// After shutdown nothing starts and nothing publishes.
	h.e.Save(uri)
	h.e.change(uri, 2, "x")
	if calls := h.tc.Calls(); len(calls) != 2 {
		t.Fatalf("calls after shutdown = %q", calls)
	}

	h2 := newHarness(t, setup{cfg: Config{ShutdownWait: time.Millisecond}})
	path2, uri2, _, text2 := asmFixture(h2)
	h2.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path2: {Compiler: "gcc"}}, nil }
	gate := make(chan struct{})
	h2.tc.started = make(chan string, 8)
	h2.tc.asm = []asmResult{{gate: gate, ignoreCtx: true}}
	h2.open(uri2, "falcon-asm", 1, text2)
	<-h2.tc.started
	<-h2.tc.started
	h2.e.Shutdown()
	if !strings.Contains(h2.log.String(), "still in flight") {
		t.Fatalf("log = %q", h2.log.String())
	}
	close(gate)
	h2.e.wg.Wait()
}

// change is a test helper on Engine to keep call sites short.
func (e *Engine) change(uri string, v int, text string) {
	e.Change(&docstore.Document{URI: uri, Version: v, Text: text})
}

func TestInitializeConfig(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.S")
	var loadedFile, loadedRoot string
	cfg := Config{
		LoadDBFile: func(p string) (FlagDB, error) {
			loadedFile = p
			if strings.HasSuffix(p, "bad.json") {
				return nil, fs.ErrNotExist
			}
			return fakeDB{file: {Compiler: "gcc", Mode: 32}}, nil
		},
		LoadDB: func(r string) (FlagDB, error) {
			loadedRoot = r
			return fakeDB{file: {Compiler: "gcc", Mode: 16}}, nil
		},
	}
	h := newHarness(t, setup{cfg: cfg, root: root, opts: `{"compileCommands":"build/cc.json","level":"v2"}`})
	if loadedFile != filepath.Join(root, "build/cc.json") || loadedRoot != "" {
		t.Fatalf("loaded %q %q", loadedFile, loadedRoot)
	}
	s := h.e.snapshotFor(&docstore.Document{URI: toolchain.PathToURI(file), Text: "nop\n"})
	if s.cfg.Level != 2 || !strings.Contains(s.cfg.LevelSource, "setting") {
		t.Fatalf("level = %d %q", s.cfg.Level, s.cfg.LevelSource)
	}

	h = newHarness(t, setup{cfg: cfg, root: root, opts: `{"compileCommands":"bad.json","level":"v9"}`})
	if loadedRoot != root || !strings.Contains(h.log.String(), "falling back") || !strings.Contains(h.log.String(), `level "v9" ignored`) {
		t.Fatalf("fallback: root %q log %q", loadedRoot, h.log.String())
	}
	d := h.open(toolchain.PathToURI(file), "falcon-asm", 1, "nop\n")
	if s := h.e.snapshotFor(d); s.cfg.InitialMode != 16 || s.cfg.Level != 0 {
		t.Fatalf("cfg = %+v", s.cfg)
	}

	h = newHarness(t, setup{opts: `not json`})
	if !strings.Contains(h.log.String(), "initializationOptions ignored") || !h.e.toolchainOn || !h.e.listingHints {
		t.Fatalf("bad options: %q", h.log.String())
	}
	_ = newHarness(t, setup{opts: `null`, root: root})
}

// Level precedence below the in-file comment: setting, then -march.
func TestFlagsLevelAndFindDBCache(t *testing.T) {
	finds := 0
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.S"), filepath.Join(dir, "b.S")
	db := fakeDB{
		a: {Compiler: "gcc", Mode: 32, Level: 3, LevelSource: "-march=x86-64-v3"},
		b: {Compiler: "gcc", Mode: 64},
	}
	h := newHarness(t, setup{cfg: Config{FindDB: func(string) (FlagDB, error) { finds++; return db, nil }}, opts: `{"toolchain":false}`})
	da := h.open(toolchain.PathToURI(a), "falcon-asm", 1, "\tvpermd %ymm0, %ymm1, %ymm2\n")
	h.open(toolchain.PathToURI(b), "falcon-asm", 1, "nop\n")
	if finds != 1 {
		t.Fatalf("FindDB called %d times", finds)
	}
	s := h.e.snapshotFor(da)
	if s.cfg.InitialMode != 32 || s.cfg.Level != 3 || s.cfg.LevelSource != "-march=x86-64-v3" {
		t.Fatalf("cfg = %+v", s.cfg)
	}
}

func TestCDocuments(t *testing.T) {
	h := newHarness(t, setup{})
	src := "static inline unsigned long rd(void) {\n" + // 0
		"\tunsigned long v;\n" + // 1
		"\t__asm__ volatile (\"mov %%cr4, %0\" : \"=r\"(v));\n" + // 2
		"\t__asm__ (\"pushq %rax\\n\\t\" \"popq %rax\");\n" + // 3
		"\t__asm__ (FOO \"pushq %rax\");\n" + // 4
		"\treturn v;\n" + // 5
		"}\n"
	path, uri := h.file("x.c", src)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc", Mode: 32}}, nil }
	doc := h.open(uri, "c", 1, src)

	// %%cr4: the GAS hover, range mapped back over the escaped spelling.
	hv, err := h.e.Hover(doc, protocol.Position{Line: 2, Character: 24})
	if err != nil || hv == nil || !strings.Contains(hv.Contents.Value, "Control register 4") {
		t.Fatalf("cr4 hover = %+v %v", hv, err)
	}
	col := strings.Index(strings.Split(src, "\n")[2], "%%cr4")
	if hv.Range == nil || hv.Range.Start != (protocol.Position{Line: 2, Character: col}) || hv.Range.End.Character != col+5 {
		t.Fatalf("cr4 range = %+v (want from %d)", hv.Range, col)
	}
	// "=r": inlineasm's own explanation.
	hv, _ = h.e.Hover(doc, protocol.Position{Line: 2, Character: 40})
	if hv == nil || !strings.Contains(hv.Contents.Value, "output") {
		t.Fatalf("constraint hover = %+v", hv)
	}
	// Outside asm, and on the C side of a statement, nothing.
	for _, p := range []protocol.Position{{Line: 1, Character: 3}, {Line: 2, Character: 3}, {Line: 2, Character: 43}} {
		if hv, err := h.e.Hover(doc, p); hv != nil || err != nil {
			t.Fatalf("hover at %+v = %+v %v", p, hv, err)
		}
	}
	if hv, _ := h.e.Hover(doc, protocol.Position{Line: -1}); hv != nil {
		t.Fatal("negative position answered")
	}

	// Mode diagnostics from the C file's -m32, mapped into C; the partial
	// statement on line 4 is skipped.
	p := h.waitPub(uri, version(1))
	if len(p.Diagnostics) != 2 {
		t.Fatalf("C diagnostics = %+v", p.Diagnostics)
	}
	for _, d := range p.Diagnostics {
		if d.Range.Start.Line != 3 {
			t.Errorf("diagnostic on line %d: %+v", d.Range.Start.Line, d)
		}
	}
	first := p.Diagnostics[0].Range
	if line := strings.Split(src, "\n")[3]; line[first.Start.Character:first.End.Character] != "%rax" {
		t.Errorf("mapped range %+v covers %q", first, line[first.Start.Character:first.End.Character])
	}

	// Everything else is clangd's.
	pos := protocol.Position{Line: 2, Character: 24}
	if h.e.SemanticTokens(doc) != nil || h.e.Definition(doc, pos) != nil || h.e.References(doc, pos, true) != nil ||
		h.e.DocumentSymbols(doc) != nil || h.e.FoldingRanges(doc) != nil || h.e.Completion(doc, pos) != nil ||
		h.e.InlayHints(doc, protocol.Range{End: protocol.Position{Line: 9}}) != nil {
		t.Fatal("C document answered an assembly-only feature")
	}
	if r, err := h.e.PrepareRename(doc, pos); r != nil || err != nil {
		t.Fatal("prepareRename in C")
	}
	if r, err := h.e.Rename(doc, pos, "x"); r != nil || err != nil {
		t.Fatal("rename in C")
	}
	h.e.wg.Wait()
	// A C file is preprocessed for its macros (helper hover), never
	// assembled.
	for _, c := range h.tc.Calls() {
		if !strings.HasPrefix(c, "macros ") {
			t.Fatalf("toolchain assembled C: %q", h.tc.Calls())
		}
	}
}

// The level comment in a C file applies to its inline asm.
func TestCLevelComment(t *testing.T) {
	h := newHarness(t, setup{})
	src := "// falcon-asm: level=v1\nvoid f(void) { __asm__(\"andn %eax, %ebx, %ecx\"); }\n"
	_, uri := h.file("y.c", src)
	h.open(uri, "c", 1, src)
	p := h.waitPub(uri, version(1))
	if len(p.Diagnostics) != 1 || !strings.Contains(p.Diagnostics[0].Message, "v3") {
		t.Fatalf("diagnostics = %+v", p.Diagnostics)
	}
}

func TestNavigation(t *testing.T) {
	h := newHarness(t, setup{opts: `{"toolchain":false}`})
	defs := "#define PORT 0x92\n#define TWICE(x) ((x)*2)\n"
	h.file("inc/defs.h", defs)
	aText := "#include \"inc/defs.h\"\n\t.globl entry\nentry:\n\t.code16\n\tinb $PORT, %al\n1:\tjmp 1b\n\tmov %\n"
	_, aURI := h.file("a.S", aText)
	bText := "\tcall entry\n\tcall entry\n"
	_, bURI := h.file("b.S", bText)
	a := h.open(aURI, "falcon-asm", 1, aText)
	b := h.open(bURI, "falcon-asm", 1, bText)

	locs := h.e.Definition(b, protocol.Position{Line: 0, Character: 7})
	if len(locs) != 1 || locs[0].URI != aURI || locs[0].Range.Start.Line != 2 {
		t.Fatalf("definition = %+v", locs)
	}
	if refs := h.e.References(b, protocol.Position{Line: 0, Character: 7}, true); len(refs) != 4 {
		t.Fatalf("references = %+v", refs)
	}
	if locs := h.e.Definition(a, protocol.Position{Line: 4, Character: 7}); len(locs) != 1 || !strings.HasSuffix(locs[0].URI, "inc/defs.h") {
		t.Fatalf("macro definition through include = %+v", locs)
	}
	if locs := h.e.Definition(a, protocol.Position{Line: 5, Character: 8}); len(locs) != 1 || locs[0].Range.Start.Line != 5 {
		t.Fatalf("local label definition = %+v", locs)
	}
	if locs := h.e.Definition(a, protocol.Position{Line: 4, Character: 2}); locs != nil {
		t.Fatalf("definition of a mnemonic = %+v", locs)
	}

	r, err := h.e.PrepareRename(b, protocol.Position{Line: 0, Character: 7})
	if err != nil || r.Placeholder != "entry" {
		t.Fatalf("prepareRename = %+v %v", r, err)
	}
	edit, err := h.e.Rename(b, protocol.Position{Line: 0, Character: 7}, "start")
	if err != nil || len(edit.Changes[aURI]) != 2 || len(edit.Changes[bURI]) != 2 {
		t.Fatalf("rename = %+v %v", edit, err)
	}
	_, err = h.e.Rename(b, protocol.Position{Line: 0, Character: 7}, "eax")
	var rf *server.RequestFailedError
	if !errors.As(err, &rf) {
		t.Fatalf("rename to a register: %v", err)
	}
	if _, err := h.e.PrepareRename(a, protocol.Position{Line: 5, Character: 0}); !errors.As(err, &rf) {
		t.Fatalf("prepareRename of a local label: %v", err)
	}

	if syms := h.e.DocumentSymbols(a); len(syms) == 0 {
		t.Fatal("no document symbols")
	}
	if folds := h.e.FoldingRanges(&docstore.Document{URI: aURI, Text: "/* a\n b\n c */\n"}); len(folds) != 1 {
		t.Fatalf("folds = %+v", folds)
	}

	// Register completion follows the mode at the cursor (16-bit after
	// .code16), and operand completion offers labels and macros.
	cl := h.e.Completion(a, protocol.Position{Line: 6, Character: 6})
	if cl == nil || !hasItem(cl, "%ax") || hasItem(cl, "%rax") {
		t.Fatalf("register completion = %+v", cl)
	}
	cl = h.e.Completion(a, protocol.Position{Line: 4, Character: 6})
	if cl == nil || !hasItem(cl, "PORT") || !hasItem(cl, "entry") {
		t.Fatalf("operand completion = %+v", cl)
	}

	// In-file #define hover, function-like macros included.
	fText := "#define BASE 0x10\n#define F(a, b) a\n#define G (1)\n\tmov $BASE, %ax\n\tF(1, 2)\n\tmov $G, %ax\n"
	_, fURI := h.file("f.S", fText)
	f := h.open(fURI, "falcon-asm", 1, fText)
	hv, _ := h.e.Hover(f, protocol.Position{Line: 3, Character: 7})
	if hv == nil || !strings.Contains(hv.Contents.Value, "dec `16`") || !strings.Contains(hv.Contents.Value, "f.S") {
		t.Fatalf("in-file macro hover = %+v", hv)
	}
	hv, _ = h.e.Hover(f, protocol.Position{Line: 4, Character: 1})
	if hv == nil || !strings.Contains(hv.Contents.Value, "#define F(a,b) a") {
		t.Fatalf("function-like hover = %+v", hv)
	}
	hv, _ = h.e.Hover(f, protocol.Position{Line: 5, Character: 7})
	if hv == nil || !strings.Contains(hv.Contents.Value, "#define G (1)") || !strings.Contains(hv.Contents.Value, "dec `1`") {
		t.Fatalf("object-like with parens = %+v", hv)
	}

	// A closed document leaves the index, so queries against a stale
	// snapshot of it find nothing rather than something outdated.
	h.e.Close(bURI)
	if locs := h.e.Definition(b, protocol.Position{Line: 0, Character: 7}); len(locs) != 0 {
		t.Fatalf("definition from a closed document = %+v", locs)
	}
}

// Deprecated painting follows the mode diagnostics.
func TestSemanticTokensDeprecated(t *testing.T) {
	h := newHarness(t, setup{})
	_, uri := h.file("m.S", "")
	doc := h.open(uri, "falcon-asm", 1, "\t.code32\n\tpushq %rax\n\tswapgs\n")
	toks := h.e.SemanticTokens(doc)
	var deprecated int
	for i := 4; i < len(toks); i += 5 {
		if toks[i]&server.ModDeprecated != 0 {
			deprecated++
		}
	}
	if deprecated != 2 {
		t.Fatalf("deprecated tokens = %d in %v", deprecated, toks)
	}
}

// Snapshots are shared per version: a second query reuses the tokens.
func TestSnapshotReuse(t *testing.T) {
	h := newHarness(t, setup{})
	_, uri := h.file("s.S", "")
	doc := h.open(uri, "falcon-asm", 1, "\tnop\n")
	s1 := h.e.snapshotFor(doc)
	s2 := h.e.snapshotFor(doc)
	if s1 != s2 || &s1.tokens()[0] != &s2.tokens()[0] {
		t.Fatal("snapshot not shared")
	}
	// A query for a version the engine has not been told about gets its
	// own snapshot rather than a stale one.
	other := &docstore.Document{URI: uri, Version: 9, Text: "\thlt\n"}
	if s3 := h.e.snapshotFor(other); s3 == s1 || s3.doc != other {
		t.Fatal("stale snapshot for a newer document")
	}
	if s := h.e.snapshotFor(&docstore.Document{URI: "file:///never/opened.c", Text: ""}); s.lang != langC || s.path != "/never/opened.c" {
		t.Fatalf("unopened snapshot = %+v", s)
	}
	if got := s1.modeAt(99); got != 64 {
		t.Fatalf("modeAt out of range = %d", got)
	}
}

func TestIncludes(t *testing.T) {
	h := newHarness(t, setup{})
	hdr, _ := h.file("inc/a.h", "")
	inc, _ := h.file("x.inc", "")
	text := "#include \"inc/a.h\"\n#include <inc/a.h>\n# include \"missing.h\"\n#include\n#include junk\n\t.include \"x.inc\"\n#include \"" + hdr + "\"\n"
	path, uri := h.file("main.S", text)
	s := (&Engine{}).newSnapshotLocked(&docState{uri: uri, path: path}, &docstore.Document{URI: uri, Text: text})
	got := s.includes()
	want := []string{hdr, hdr, inc, hdr}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("includes = %q, want %q", got, want)
	}
	// With flags, cpp's search rules apply: <> does not search the file's
	// own directory.
	s.hasFlags = true
	s.flags = toolchain.Flags{File: path, Dir: h.dir}
	if got := s.includes(); len(got) != 3 {
		t.Fatalf("includes with flags = %q", got)
	}
	s.path = ""
	if s.includes() != nil {
		t.Fatal("includes for an untitled buffer")
	}
}

func TestListingHintsFormatting(t *testing.T) {
	doc := &docstore.Document{Text: "a\nb\nc\nd\ne\n"}
	res := &toolchain.AsmResult{
		Listing: []toolchain.ListingLine{
			{Line: 0, Offset: 0x1a3, HasOffset: true, Section: ".text", Bytes: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
			{Line: 1, Offset: 0x1b0, HasOffset: true, Section: ".text", Bytes: []byte{0x90}},
			{Line: 2, Section: ".text"},
			{Line: 3, Section: ".text", Source: ".org 0x1b1"},
			{Line: 4, Offset: 0, HasOffset: true, Section: ".data", Bytes: []byte{1, 2}},
			{Line: 9, Offset: 2, HasOffset: true, Section: ".data", Bytes: []byte{3}},
		},
	}
	hints := listingHints(doc, lexer.Tokenize(doc.Text), res, protocol.Range{End: protocol.Position{Line: 99}})
	got := hintLabels(hints)
	want := []string{"0:+0x1a3  01 02 03 04 05 06 07 08 ...", "1:+0x1b0  90", "3:pads 0 bytes", "4:+0x0  01 02"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("labels = %q", got)
	}
	if hints[0].Tooltip != "section .text, offset 0x1a3, 13 bytes" || hints[1].Tooltip != "section .text, offset 0x1b0, 1 byte" ||
		hints[2].Tooltip != "section .text: nothing to fill" {
		t.Fatalf("tooltips = %q / %q / %q", hints[0].Tooltip, hints[1].Tooltip, hints[2].Tooltip)
	}
	res.SectionSizes = map[string]uint64{".text": 0x1b4}
	if n := lineSize(res, 1); n != 4 {
		t.Fatalf("size to section end = %d", n)
	}
}

// Hints sit after the statement's code, not after a trailing comment, so
// the offsets line up with the code whatever the comments look like.
func TestHintColumn(t *testing.T) {
	cases := []struct {
		name, line string
		want       int
	}{
		{"no comment", "\tmovl %eax, %cr0", len("\tmovl %eax, %cr0")},
		{"trailing spaces", "\tcli   ", len("\tcli")},
		{"block comment", "\torl $1, %eax\t\t/* CR0.PE */", len("\torl $1, %eax")},
		{"hash comment", "\t.byte 1 # one", len("\t.byte 1")},
		{"slash comment", "\tnop // pad", len("\tnop")},
		{"comment inside", "\tmovl /* x */ %eax, %ebx /* y */", len("\tmovl /* x */ %eax, %ebx")},
		{"comment only", "\t/* falcon-asm: a20=x */", len("\t/* falcon-asm: a20=x */")},
		{"utf-16", "\t.ascii \"\u00e9\" /* e */", len("\t.ascii \"e\"")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "x:\n" + c.line + "\n"
			if got := hintColumn(lexer.Tokenize(src), 1, c.line); got != c.want {
				t.Fatalf("hintColumn(%q) = %d, want %d", c.line, got, c.want)
			}
		})
	}
}

// A label hover inside an asm template must name the C line, not the line
// within the template: the template is hovered as a document of its own.
func TestCInlineLabelLines(t *testing.T) {
	h := newHarness(t, setup{})
	src := "/* switch */\n" + // 0
		"#include <x.h>\n" + // 1
		"\n" + // 2
		"__asm__(\n" + // 3
		"\t\".globl task_switch\\n\"\n" + // 4
		"\t\"task_switch:\\n\"\n" + // 5
		"\t\"1:\\n\"\n" + // 6
		"\t\"    jmp 1b\\n\"\n" + // 7
		"\t\"    jmp task_switch\\n\"\n" + // 8
		");\n"
	path, uri := h.file("sw.c", src)
	h.e.cfg.FindDB = func(string) (FlagDB, error) { return fakeDB{path: {Compiler: "gcc", Mode: 32}}, nil }
	doc := h.open(uri, "c", 1, src)

	lines := strings.Split(src, "\n")
	cases := []struct {
		line int
		word string
		want string
	}{
		{8, "task_switch", "Label defined at line 6"},
		{7, "1b", "-> line 7"},
	}
	for _, c := range cases {
		col := strings.Index(lines[c.line], c.word)
		hv, err := h.e.Hover(doc, protocol.Position{Line: c.line, Character: col})
		if err != nil || hv == nil || !strings.Contains(hv.Contents.Value, c.want) {
			t.Errorf("hover on %s = %+v %v, want %q", c.word, hv, err, c.want)
		}
	}
	h.e.wg.Wait()
}
