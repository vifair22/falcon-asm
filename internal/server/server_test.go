package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

const testURI = "file:///work/pmode.S"

// client speaks real framed JSON-RPC to a Server running on io.Pipe pairs.
// A reader goroutine splits the server's output into responses and
// notifications, since the engine may publish at any time.
type client struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	resp   chan *jsonrpc.Message
	notes  chan *jsonrpc.Message
	nextID int
	done   chan int
	log    *syncBuffer
}

// syncBuffer is a log sink the server goroutine writes while the test
// reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func start(t *testing.T, opts Options) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	logBuf := &syncBuffer{}
	if opts.Log == nil {
		opts.Log = logBuf
	}
	c := &client{
		t: t, in: inW, out: bufio.NewReader(outR), done: make(chan int, 1), log: logBuf,
		resp: make(chan *jsonrpc.Message, 16), notes: make(chan *jsonrpc.Message, 64),
	}
	srv := New(opts)
	go func() {
		code := srv.Run(inR, outW)
		_ = outW.Close()
		_ = inR.Close()
		c.done <- code
	}()
	go func() {
		defer close(c.resp)
		defer close(c.notes)
		for {
			m, err := jsonrpc.Read(c.out)
			if err != nil {
				return
			}
			if m.IsNotification() {
				c.notes <- m
			} else {
				c.resp <- m
			}
		}
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

func (c *client) send(m *jsonrpc.Message) {
	c.t.Helper()
	if err := jsonrpc.Write(c.in, m); err != nil {
		c.t.Fatalf("send %s: %v", m.Method, err)
	}
}

func (c *client) sendRaw(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, s); err != nil {
		c.t.Fatalf("send raw: %v", err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	c.send(&jsonrpc.Message{Method: method, Params: mustJSON(c.t, params)})
}

// next returns the next response frame. The server answers requests in
// order, so it is the answer to the last request sent.
func (c *client) next() *jsonrpc.Message {
	c.t.Helper()
	select {
	case m, ok := <-c.resp:
		if !ok {
			c.t.Fatal("server closed the stream")
		}
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("no response")
	}
	return nil
}

// call sends a request and reads its response.
func (c *client) call(method string, params any) *jsonrpc.Message {
	c.t.Helper()
	c.nextID++
	id := json.RawMessage(mustJSON(c.t, c.nextID))
	c.send(&jsonrpc.Message{ID: &id, Method: method, Params: mustJSON(c.t, params)})
	m := c.next()
	if m.ID == nil || string(*m.ID) != string(id) {
		c.t.Fatalf("response id mismatch for %s: %+v", method, m)
	}
	return m
}

// note waits for the next notification.
func (c *client) note() *jsonrpc.Message {
	c.t.Helper()
	select {
	case m, ok := <-c.notes:
		if !ok {
			c.t.Fatal("server closed the stream")
		}
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("no notification")
	}
	return nil
}

func (c *client) wait() int {
	c.t.Helper()
	select {
	case code := <-c.done:
		return code
	case <-time.After(5 * time.Second):
		c.t.Fatal("server did not exit")
		return -1
	}
}

func wantResult(t *testing.T, m *jsonrpc.Message, want string) {
	t.Helper()
	if m.Error != nil {
		t.Fatalf("unexpected error: %+v", m.Error)
	}
	if string(m.Result) != want {
		t.Fatalf("result = %s, want %s", m.Result, want)
	}
}

func wantError(t *testing.T, m *jsonrpc.Message, code int) {
	t.Helper()
	if m.Error == nil || m.Error.Code != code {
		t.Fatalf("got %+v (error %+v), want error code %d", m, m.Error, code)
	}
}

func defaultOpts() Options {
	return Options{TokenTypes: TokenTypes, TokenModifiers: TokenModifiers, Name: "falcon-asm", Version: "test"}
}

func initializeWith(t *testing.T, c *client, params map[string]any) protocol.InitializeResult {
	t.Helper()
	m := c.call(protocol.MethodInitialize, params)
	if m.Error != nil {
		t.Fatalf("initialize: %+v", m.Error)
	}
	var res protocol.InitializeResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	c.notify(protocol.MethodInitialized, map[string]any{})
	return res
}

func initialize(t *testing.T, c *client) protocol.InitializeResult {
	t.Helper()
	return initializeWith(t, c, map[string]any{
		"processId":    nil,
		"rootUri":      nil,
		"clientInfo":   map[string]string{"name": "test-client", "version": "1"},
		"capabilities": map[string]any{},
	})
}

func openDoc(c *client, text string) {
	c.notify(protocol.MethodDidOpen, protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: testURI, LanguageID: "gas", Version: 1, Text: text},
	})
}

func docPos(line, char int) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: testURI},
		Position:     protocol.Position{Line: line, Character: char},
	}
}

func hoverAt(c *client, line, char int) *jsonrpc.Message {
	return c.call(protocol.MethodHover, protocol.HoverParams{TextDocumentPositionParams: docPos(line, char)})
}

func semtok(c *client) *jsonrpc.Message {
	return c.call(protocol.MethodSemanticTokensFull, protocol.SemanticTokensParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: testURI},
	})
}

// everyQuery sends one request per document feature and returns the raw
// results keyed by method.
func everyQuery(c *client) map[string]*jsonrpc.Message {
	td := protocol.TextDocumentIdentifier{URI: testURI}
	refs := protocol.ReferenceParams{TextDocumentPositionParams: docPos(0, 1)}
	refs.Context.IncludeDeclaration = true
	return map[string]*jsonrpc.Message{
		protocol.MethodHover:              hoverAt(c, 0, 1),
		protocol.MethodSemanticTokensFull: semtok(c),
		protocol.MethodDefinition:         c.call(protocol.MethodDefinition, docPos(0, 1)),
		protocol.MethodReferences:         c.call(protocol.MethodReferences, refs),
		protocol.MethodPrepareRename:      c.call(protocol.MethodPrepareRename, docPos(0, 1)),
		protocol.MethodRename:             c.call(protocol.MethodRename, protocol.RenameParams{TextDocumentPositionParams: docPos(0, 1), NewName: "x"}),
		protocol.MethodDocumentSymbol:     c.call(protocol.MethodDocumentSymbol, protocol.DocumentSymbolParams{TextDocument: td}),
		protocol.MethodFoldingRange:       c.call(protocol.MethodFoldingRange, protocol.FoldingRangeParams{TextDocument: td}),
		protocol.MethodInlayHint:          c.call(protocol.MethodInlayHint, protocol.InlayHintParams{TextDocument: td}),
		protocol.MethodCompletion:         c.call(protocol.MethodCompletion, protocol.CompletionParams{TextDocumentPositionParams: docPos(0, 1)}),
		protocol.MethodCodeAction: c.call(protocol.MethodCodeAction, protocol.CodeActionParams{
			TextDocument: td, Range: protocol.Range{End: protocol.Position{Character: 3}},
		}),
	}
}

// TestEndToEnd walks a complete session with no engine: every feature is
// advertised and answers empty.
func TestEndToEnd(t *testing.T) {
	c := start(t, defaultOpts())

	m := c.call(protocol.MethodInitialize, map[string]any{"processId": nil, "rootUri": nil, "capabilities": map[string]any{}})
	var raw struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(m.Result, &raw); err != nil {
		t.Fatal(err)
	}
	wantCaps := `{"textDocumentSync":{"openClose":true,"change":2,"save":{"includeText":false}},` +
		`"hoverProvider":true,` +
		`"semanticTokensProvider":{"documentSelector":[{"language":"falcon-asm"}],"legend":{"tokenTypes":["keyword","modifier","variable","number","label","macro","comment","string","operator","type","parameter"],"tokenModifiers":["declaration","defaultLibrary","deprecated"]},"full":{"delta":true},"range":false},` +
		`"definitionProvider":true,"referencesProvider":true,"renameProvider":{"prepareProvider":true},` +
		`"documentSymbolProvider":true,"foldingRangeProvider":true,"inlayHintProvider":true,` +
		`"completionProvider":{"triggerCharacters":["%",".","$"]},` +
		`"codeActionProvider":{"codeActionKinds":["quickfix"]}}`
	if string(raw.Capabilities) != wantCaps {
		t.Fatalf("capabilities =\n%s\nwant\n%s", raw.Capabilities, wantCaps)
	}
	var res protocol.InitializeResult
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.ServerInfo == nil || res.ServerInfo.Name != "falcon-asm" || res.ServerInfo.Version != "test" {
		t.Fatalf("serverInfo = %+v", res.ServerInfo)
	}
	c.notify(protocol.MethodInitialized, map[string]any{})

	// Queries for a document that is not open answer empty.
	for method, m := range everyQuery(c) {
		want := "null"
		if method == protocol.MethodSemanticTokensFull {
			want = `{"data":[]}`
		}
		if m.Error != nil || string(m.Result) != want {
			t.Errorf("%s on unopened doc = %s %+v", method, m.Result, m.Error)
		}
	}

	openDoc(c, "\tcli\n\tlgdt gdt_desc\n")
	c.notify(protocol.MethodDidChange, protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{URI: testURI, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Range: &protocol.Range{Start: protocol.Position{Line: 0, Character: 1}, End: protocol.Position{Line: 0, Character: 4}}, Text: "sti"},
		},
	})
	c.notify(protocol.MethodDidSave, protocol.DidSaveTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: testURI}})
	c.notify(protocol.MethodCancelRequest, map[string]int{"id": 99})

	for method, m := range everyQuery(c) {
		want := "null"
		if method == protocol.MethodSemanticTokensFull {
			want = `{"resultId":"1","data":[]}`
		}
		if m.Error != nil || string(m.Result) != want {
			t.Errorf("%s with nop engine = %s %+v", method, m.Result, m.Error)
		}
	}
	wantError(t, c.call("textDocument/codeLens", map[string]any{}), jsonrpc.MethodNotFound)
	c.notify("workspace/didChangeWatchedFiles", map[string]any{})

	wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
	wantError(t, hoverAt(c, 0, 0), jsonrpc.InvalidRequest)
	c.notify(protocol.MethodExit, nil)
	if code := c.wait(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// fakeEngine records what the server hands it and answers from the
// document, so the test can see that snapshots and positions arrive intact.
type fakeEngine struct {
	mu       sync.Mutex
	calls    []string
	session  Session
	fail     bool
	refuse   bool
	shutdown int
}

func (f *fakeEngine) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeEngine) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeEngine) err() error {
	switch {
	case f.refuse:
		return fmt.Errorf("wrapped: %w", &RequestFailedError{Err: errors.New("cannot rename a register")})
	case f.fail:
		return errors.New("broke")
	}
	return nil
}

func (f *fakeEngine) Initialize(s Session) {
	f.mu.Lock()
	f.session = s
	f.mu.Unlock()
	f.record("init %s %s", s.RootURI, s.InitOptions)
}
func (f *fakeEngine) Open(d *docstore.Document, lang string) {
	f.record("open %s v%d %s", d.URI, d.Version, lang)
}
func (f *fakeEngine) Change(d *docstore.Document) { f.record("change v%d %q", d.Version, d.Text) }
func (f *fakeEngine) Save(uri string)             { f.record("save %s", uri) }
func (f *fakeEngine) Close(uri string)            { f.record("close %s", uri) }
func (f *fakeEngine) Shutdown() {
	f.mu.Lock()
	f.shutdown++
	f.mu.Unlock()
}

func (f *fakeEngine) word(d *docstore.Document, p protocol.Position) string {
	off, err := d.Offset(p)
	if err != nil || off+3 > len(d.Text) {
		return ""
	}
	return d.Text[off : off+3]
}

func (f *fakeEngine) Hover(d *docstore.Document, p protocol.Position) (*protocol.Hover, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	w := f.word(d, p)
	if w == "" {
		return nil, nil
	}
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: "**" + w + "**"}}, nil
}

func (f *fakeEngine) SemanticTokens(d *docstore.Document) []uint32 {
	return []uint32{0, 1, uint32(len(d.Text)), TokenKeyword, 0}
}

func (f *fakeEngine) loc(d *docstore.Document, p protocol.Position) []protocol.Location {
	return []protocol.Location{{URI: d.URI, Range: protocol.Range{Start: p, End: p}}}
}

func (f *fakeEngine) Definition(d *docstore.Document, p protocol.Position) []protocol.Location {
	return f.loc(d, p)
}

func (f *fakeEngine) References(d *docstore.Document, p protocol.Position, decl bool) []protocol.Location {
	if !decl {
		return nil
	}
	return f.loc(d, p)
}

func (f *fakeEngine) PrepareRename(d *docstore.Document, p protocol.Position) (*protocol.PrepareRenameResult, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	return &protocol.PrepareRenameResult{Range: protocol.Range{Start: p, End: p}, Placeholder: f.word(d, p)}, nil
}

func (f *fakeEngine) Rename(d *docstore.Document, p protocol.Position, n string) (*protocol.WorkspaceEdit, error) {
	if err := f.err(); err != nil {
		return nil, err
	}
	return &protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{d.URI: {{Range: protocol.Range{Start: p, End: p}, NewText: n}}}}, nil
}

func (f *fakeEngine) DocumentSymbols(d *docstore.Document) []protocol.DocumentSymbol {
	return []protocol.DocumentSymbol{{Name: f.word(d, protocol.Position{}), Kind: protocol.SymbolKindFunction}}
}

func (f *fakeEngine) FoldingRanges(*docstore.Document) []protocol.FoldingRange {
	return []protocol.FoldingRange{{StartLine: 0, EndLine: 1}}
}

func (f *fakeEngine) InlayHints(_ *docstore.Document, r protocol.Range) []protocol.InlayHint {
	return []protocol.InlayHint{{Position: r.End, Label: "+0x0"}}
}

func (f *fakeEngine) CodeActions(d *docstore.Document, r protocol.Range, ctx protocol.CodeActionContext) []protocol.CodeAction {
	if len(ctx.Only) > 0 {
		return nil
	}
	return []protocol.CodeAction{{
		Title: "fix " + f.word(d, r.Start), Kind: protocol.CodeActionKindQuickFix,
		Edit: &protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{d.URI: {{Range: r, NewText: "x"}}}},
	}}
}

func (f *fakeEngine) Completion(d *docstore.Document, p protocol.Position) *protocol.CompletionList {
	if p.Line > 0 {
		return nil
	}
	return &protocol.CompletionList{Items: []protocol.CompletionItem{{Label: f.word(d, p)}}}
}

// TestEngineWiring checks every feature reaches the engine with the synced
// document and that results, refusals and failures reach the wire intact.
func TestEngineWiring(t *testing.T) {
	eng := &fakeEngine{}
	opts := defaultOpts()
	opts.Engine = eng
	c := start(t, opts)
	initializeWith(t, c, map[string]any{
		"rootUri":               "file:///work",
		"initializationOptions": map[string]any{"toolchain": false},
	})
	if eng.session.RootURI != "file:///work" || string(eng.session.InitOptions) != `{"toolchain":false}` {
		t.Fatalf("session = %+v", eng.session)
	}

	// The emoji is two UTF-16 units, so character 5 is the 'm' of mov; this
	// proves the engine receives the wire position and converts it itself.
	openDoc(c, "# \U0001F600 mov\n")
	wantResult(t, hoverAt(c, 0, 5), `{"contents":{"kind":"markdown","value":"**mov**"}}`)
	c.notify(protocol.MethodDidChange, protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{URI: testURI, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: "    mov %ax, %bx\n"}},
	})
	c.notify(protocol.MethodDidSave, protocol.DidSaveTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: testURI}})
	// A save for a document that is not open never reaches the engine.
	c.notify(protocol.MethodDidSave, protocol.DidSaveTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: "file:///other.S"}})

	got := everyQuery(c)
	want := map[string]string{
		protocol.MethodHover:              `null`,
		protocol.MethodSemanticTokensFull: `{"resultId":"1","data":[0,1,17,0,0]}`,
		protocol.MethodDefinition:         `[{"uri":"file:///work/pmode.S","range":{"start":{"line":0,"character":1},"end":{"line":0,"character":1}}}]`,
		protocol.MethodReferences:         `[{"uri":"file:///work/pmode.S","range":{"start":{"line":0,"character":1},"end":{"line":0,"character":1}}}]`,
		protocol.MethodPrepareRename:      `{"range":{"start":{"line":0,"character":1},"end":{"line":0,"character":1}},"placeholder":"   "}`,
		protocol.MethodRename:             `{"changes":{"file:///work/pmode.S":[{"range":{"start":{"line":0,"character":1},"end":{"line":0,"character":1}},"newText":"x"}]}}`,
		protocol.MethodDocumentSymbol:     `[{"name":"   ","kind":12,"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"selectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}}]`,
		protocol.MethodFoldingRange:       `[{"startLine":0,"endLine":1}]`,
		protocol.MethodInlayHint:          `[{"position":{"line":0,"character":0},"label":"+0x0"}]`,
		protocol.MethodCompletion:         `{"isIncomplete":false,"items":[{"label":"   "}]}`,
		protocol.MethodCodeAction:         `[{"title":"fix    ","kind":"quickfix","edit":{"changes":{"file:///work/pmode.S":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"x"}]}}}]`,
	}
	// Hover at 0,1 lands on spaces: the fake answers "   ", so check it
	// separately at the mnemonic.
	got[protocol.MethodHover] = hoverAt(c, 0, 99)
	for method, w := range want {
		wantResult(t, got[method], w)
	}
	wantResult(t, hoverAt(c, 0, 4), `{"contents":{"kind":"markdown","value":"**mov**"}}`)
	wantResult(t, c.call(protocol.MethodCompletion, protocol.CompletionParams{TextDocumentPositionParams: docPos(1, 0)}), "null")
	refs := protocol.ReferenceParams{TextDocumentPositionParams: docPos(0, 1)}
	wantResult(t, c.call(protocol.MethodReferences, refs), "null")
	// An empty answer (here: the client asked only for kinds the engine
	// does not offer) is null.
	only := protocol.CodeActionParams{TextDocument: protocol.TextDocumentIdentifier{URI: testURI}}
	only.Context.Only = []string{"refactor"}
	wantResult(t, c.call(protocol.MethodCodeAction, only), "null")

	eng.refuse = true
	m := c.call(protocol.MethodRename, protocol.RenameParams{TextDocumentPositionParams: docPos(0, 4), NewName: "eax"})
	wantError(t, m, jsonrpc.RequestFailed)
	if m.Error.Message != "cannot rename a register" {
		t.Fatalf("refusal message = %q", m.Error.Message)
	}
	wantError(t, c.call(protocol.MethodPrepareRename, docPos(0, 4)), jsonrpc.RequestFailed)
	eng.refuse, eng.fail = false, true
	wantError(t, hoverAt(c, 0, 4), jsonrpc.InternalError)
	wantError(t, c.call(protocol.MethodRename, protocol.RenameParams{TextDocumentPositionParams: docPos(0, 4), NewName: "y"}), jsonrpc.InternalError)
	eng.fail = false

	// The engine publishes from its own goroutines; nil diagnostics still
	// clear rather than sending null.
	go eng.session.Publish(protocol.PublishDiagnosticsParams{URI: testURI})
	n := c.note()
	if n.Method != protocol.MethodPublishDiagnostics || string(n.Params) != `{"uri":"file:///work/pmode.S","diagnostics":[]}` {
		t.Fatalf("notification = %s %s", n.Method, n.Params)
	}

	c.notify(protocol.MethodDidClose, protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: testURI}})
	wantResult(t, hoverAt(c, 0, 4), "null")

	wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
	c.notify(protocol.MethodExit, nil)
	if code := c.wait(); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	wantCalls := []string{
		`init file:///work {"toolchain":false}`,
		"open file:///work/pmode.S v1 gas",
		`change v2 "    mov %ax, %bx\n"`,
		"save file:///work/pmode.S",
		"close file:///work/pmode.S",
	}
	if got := eng.Calls(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %q\nwant %q", got, wantCalls)
	}
	if eng.shutdown != 1 {
		t.Fatalf("engine shut down %d times", eng.shutdown)
	}
}

// The workspace root falls back to the first workspace folder, and an
// engine is shut down even when the client vanishes without shutdown.
func TestWorkspaceFolderRootAndAbruptEnd(t *testing.T) {
	eng := &fakeEngine{}
	opts := defaultOpts()
	opts.Engine = eng
	c := start(t, opts)
	initializeWith(t, c, map[string]any{
		"workspaceFolders": []map[string]string{{"uri": "file:///ws", "name": "ws"}},
	})
	if eng.session.RootURI != "file:///ws" {
		t.Fatalf("root = %q", eng.session.RootURI)
	}
	_ = c.in.Close()
	if code := c.wait(); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if eng.shutdown != 1 {
		t.Fatalf("engine shut down %d times", eng.shutdown)
	}
}

// A publish that cannot be written is logged.
func TestPublishFailureLogged(t *testing.T) {
	eng := &fakeEngine{}
	logBuf := &syncBuffer{}
	srv := New(Options{Engine: eng, Log: logBuf})
	in := frameOf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	srv.Run(strings.NewReader(in), failWriter{})
	eng.session.Publish(protocol.PublishDiagnosticsParams{URI: "file:///x.S"})
	if !strings.Contains(logBuf.String(), "publish diagnostics for file:///x.S") {
		t.Fatalf("log = %q", logBuf.String())
	}
}

func TestLifecycleErrors(t *testing.T) {
	tests := []struct {
		name     string
		script   func(t *testing.T, c *client)
		wantCode int
		wantLog  string
	}{
		{
			name: "request before initialize",
			script: func(t *testing.T, c *client) {
				wantError(t, hoverAt(c, 0, 0), jsonrpc.ServerNotInitialized)
				wantError(t, c.call(protocol.MethodShutdown, nil), jsonrpc.ServerNotInitialized)
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 1,
		},
		{
			name: "notifications before initialize are dropped",
			script: func(t *testing.T, c *client) {
				openDoc(c, "cli\n")
				initialize(t, c)
				// If didOpen had been applied the change would succeed and
				// nothing would be logged.
				c.notify(protocol.MethodDidChange, protocol.DidChangeTextDocumentParams{
					TextDocument:   protocol.VersionedTextDocumentIdentifier{URI: testURI, Version: 2},
					ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: "x"}},
				})
				wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 0,
			wantLog:  "document not open",
		},
		{
			name: "initialize twice",
			script: func(t *testing.T, c *client) {
				initialize(t, c)
				wantError(t, c.call(protocol.MethodInitialize, map[string]any{}), jsonrpc.InvalidRequest)
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 1,
		},
		{
			name: "exit without shutdown",
			script: func(t *testing.T, c *client) {
				initialize(t, c)
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 1,
		},
		{
			name: "stream closed without exit",
			script: func(t *testing.T, c *client) {
				initialize(t, c)
				_ = c.in.Close()
			},
			wantCode: 1,
			wantLog:  "without exit",
		},
		{
			name: "broken framing",
			script: func(t *testing.T, c *client) {
				c.sendRaw("not a header\r\n\r\n")
			},
			wantCode: 1,
			wantLog:  "connection failed",
		},
		{
			name: "invalid params",
			script: func(t *testing.T, c *client) {
				wantError(t, c.call(protocol.MethodInitialize, []int{1}), jsonrpc.InvalidParams)
				initialize(t, c)
				wantError(t, c.call(protocol.MethodHover, "nope"), jsonrpc.InvalidParams)
				wantError(t, c.call(protocol.MethodSemanticTokensFull, 7), jsonrpc.InvalidParams)
				for _, method := range []string{protocol.MethodDefinition, protocol.MethodReferences,
					protocol.MethodPrepareRename, protocol.MethodRename, protocol.MethodDocumentSymbol,
					protocol.MethodFoldingRange, protocol.MethodInlayHint, protocol.MethodCompletion} {
					wantError(t, c.call(method, 7), jsonrpc.InvalidParams)
				}
				c.notify(protocol.MethodDidSave, "bad")
				c.notify(protocol.MethodDidOpen, "bad")
				c.notify(protocol.MethodDidChange, "bad")
				c.notify(protocol.MethodDidClose, "bad")
				wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 0,
			wantLog:  "textDocument/didClose",
		},
		{
			name: "unparseable body, stray response and empty message are survived",
			script: func(t *testing.T, c *client) {
				initialize(t, c)
				body := `{"jsonrpc":`
				c.sendRaw("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
				wantError(t, c.next(), jsonrpc.ParseError)
				c.sendRaw(frameOf(`{"jsonrpc":"2.0","id":5,"result":null}`))
				c.sendRaw(frameOf(`{"jsonrpc":"2.0"}`))
				wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
				c.notify(protocol.MethodDidOpen, protocol.DidOpenTextDocumentParams{})
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 0,
			wantLog:  "malformed",
		},
		{
			name: "initialize without params",
			script: func(t *testing.T, c *client) {
				m := c.call(protocol.MethodInitialize, nil)
				if m.Error != nil {
					t.Fatalf("initialize: %+v", m.Error)
				}
				wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
				c.notify(protocol.MethodExit, nil)
			},
			wantCode: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := start(t, defaultOpts())
			tt.script(t, c)
			if code := c.wait(); code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantLog != "" && !strings.Contains(c.log.String(), tt.wantLog) {
				t.Fatalf("log %q does not mention %q", c.log.String(), tt.wantLog)
			}
		})
	}
}

// A reply that cannot be written must be logged, not crash the loop.
func TestReplyFailureLogged(t *testing.T) {
	var logBuf bytes.Buffer
	srv := New(Options{Log: &logBuf})
	in := frameOf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	code := srv.Run(strings.NewReader(in), failWriter{})
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(logBuf.String(), "reply to initialize") {
		t.Fatalf("log = %q", logBuf.String())
	}
}

// New must tolerate a zero Options: no log writer and no legend still yield
// a valid initialize result with empty, non-null legend arrays.
func TestZeroOptions(t *testing.T) {
	var out bytes.Buffer
	in := frameOf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if code := New(Options{}).Run(strings.NewReader(in), &out); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), `"legend":{"tokenTypes":[],"tokenModifiers":[]}`) {
		t.Fatalf("initialize result = %s", out.String())
	}
}

func TestLegendOrder(t *testing.T) {
	types := map[uint32]string{
		TokenKeyword: "keyword", TokenModifier: "modifier", TokenVariable: "variable",
		TokenNumber: "number", TokenLabel: "label", TokenMacro: "macro",
		TokenComment: "comment", TokenString: "string", TokenOperator: "operator",
		TokenType: "type", TokenParameter: "parameter",
	}
	if len(types) != len(TokenTypes) {
		t.Fatalf("%d constants, %d legend entries", len(types), len(TokenTypes))
	}
	for idx, name := range types {
		if TokenTypes[idx] != name {
			t.Errorf("TokenTypes[%d] = %q, want %q", idx, TokenTypes[idx], name)
		}
	}
	mods := map[uint32]string{ModDeclaration: "declaration", ModDefaultLibrary: "defaultLibrary", ModDeprecated: "deprecated"}
	if len(mods) != len(TokenModifiers) {
		t.Fatalf("%d modifier constants, %d legend entries", len(mods), len(TokenModifiers))
	}
	for bit, name := range mods {
		i := 0
		for b := bit; b > 1; b >>= 1 {
			i++
		}
		if TokenModifiers[i] != name {
			t.Errorf("modifier bit %#x -> %q, want %q", bit, TokenModifiers[i], name)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func frameOf(body string) string {
	return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

// TestSemanticTokensDelta covers the delta protocol: a delta against the
// last result is the edit between them, an unknown or superseded base gets
// the full stream, and closing a document forgets its base.
func TestSemanticTokensDelta(t *testing.T) {
	c := start(t, Options{Engine: &fakeEngine{}})
	initialize(t, c)
	delta := func(prev string) *jsonrpc.Message {
		return c.call(protocol.MethodSemanticTokensDelta, protocol.SemanticTokensDeltaParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: testURI}, PreviousResultID: prev,
		})
	}
	wantResult(t, delta("1"), `{"data":[]}`) // not open
	openDoc(c, "\tnop\n")
	wantResult(t, semtok(c), `{"resultId":"1","data":[0,1,5,0,0]}`)
	wantResult(t, delta("1"), `{"resultId":"2","edits":[]}`)
	c.notify(protocol.MethodDidChange, protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{URI: testURI, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: "\tnop; nop\n"}},
	})
	wantResult(t, delta("2"), `{"resultId":"3","edits":[{"start":0,"deleteCount":5,"data":[0,1,10,0,0]}]}`)
	wantResult(t, delta("2"), `{"resultId":"4","data":[0,1,10,0,0]}`) // superseded base
	wantResult(t, delta("nope"), `{"resultId":"5","data":[0,1,10,0,0]}`)
	c.notify(protocol.MethodDidClose, protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: testURI}})
	openDoc(c, "\tnop; nop\n")
	wantResult(t, delta("5"), `{"resultId":"6","data":[0,1,10,0,0]}`) // base forgotten at close
	wantError(t, c.call(protocol.MethodSemanticTokensDelta, "bad"), jsonrpc.InvalidParams)
}

func TestTokenEdits(t *testing.T) {
	tok := func(vals ...uint32) []uint32 { return vals }
	cases := []struct {
		name     string
		old, cur []uint32
		want     string
	}{
		{"same", tok(0, 1, 2, 3, 4), tok(0, 1, 2, 3, 4), `[]`},
		{"both empty", nil, nil, `[]`},
		{"from empty", nil, tok(0, 1, 2, 3, 4), `[{"start":0,"deleteCount":0,"data":[0,1,2,3,4]}]`},
		{"to empty", tok(0, 1, 2, 3, 4), nil, `[{"start":0,"deleteCount":5}]`},
		{"middle token", tok(0, 1, 2, 3, 4, 1, 1, 2, 3, 4, 1, 1, 2, 3, 4), tok(0, 1, 2, 3, 4, 1, 1, 9, 3, 4, 1, 1, 2, 3, 4),
			`[{"start":5,"deleteCount":5,"data":[1,1,9,3,4]}]`},
		{"insert", tok(0, 1, 2, 3, 4, 1, 1, 2, 3, 4), tok(0, 1, 2, 3, 4, 0, 5, 1, 0, 0, 1, 1, 2, 3, 4),
			`[{"start":5,"deleteCount":0,"data":[0,5,1,0,0]}]`},
		// A repeated token must not let the prefix and suffix overlap.
		{"repeat", tok(0, 1, 2, 3, 4), tok(0, 1, 2, 3, 4, 0, 1, 2, 3, 4), `[{"start":5,"deleteCount":0,"data":[0,1,2,3,4]}]`},
	}
	for _, c := range cases {
		got, err := json.Marshal(tokenEdits(c.old, c.cur))
		if err != nil || string(got) != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
