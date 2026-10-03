package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

const testURI = "file:///work/pmode.S"

// client speaks real framed JSON-RPC to a Server running on io.Pipe pairs.
type client struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	nextID int
	done   chan int
	log    *bytes.Buffer
}

func start(t *testing.T, opts Options) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	logBuf := &bytes.Buffer{}
	if opts.Log == nil {
		opts.Log = logBuf
	}
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan int, 1), log: logBuf}
	srv := New(opts)
	go func() {
		code := srv.Run(inR, outW)
		_ = outW.Close()
		_ = inR.Close()
		c.done <- code
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

// call sends a request and reads its response. The server answers requests
// in order and sends nothing unsolicited, so the next frame is the answer.
func (c *client) call(method string, params any) *jsonrpc.Message {
	c.t.Helper()
	c.nextID++
	id := json.RawMessage(mustJSON(c.t, c.nextID))
	c.send(&jsonrpc.Message{ID: &id, Method: method, Params: mustJSON(c.t, params)})
	m, err := jsonrpc.Read(c.out)
	if err != nil {
		c.t.Fatalf("read response to %s: %v", method, err)
	}
	if m.ID == nil || string(*m.ID) != string(id) {
		c.t.Fatalf("response id mismatch for %s: %+v", method, m)
	}
	return m
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

func initialize(t *testing.T, c *client) protocol.InitializeResult {
	t.Helper()
	m := c.call(protocol.MethodInitialize, map[string]any{
		"processId":    nil,
		"rootUri":      nil,
		"clientInfo":   map[string]string{"name": "test-client", "version": "1"},
		"capabilities": map[string]any{},
	})
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

func openDoc(c *client, text string) {
	c.notify(protocol.MethodDidOpen, protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: testURI, LanguageID: "gas", Version: 1, Text: text},
	})
}

func hoverAt(c *client, line, char int) *jsonrpc.Message {
	return c.call(protocol.MethodHover, protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: testURI},
		Position:     protocol.Position{Line: line, Character: char},
	}})
}

func semtok(c *client) *jsonrpc.Message {
	return c.call(protocol.MethodSemanticTokensFull, protocol.SemanticTokensParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: testURI},
	})
}

// TestEndToEnd walks a complete session with no language features wired, as
// main runs today.
func TestEndToEnd(t *testing.T) {
	c := start(t, defaultOpts())

	res := initialize(t, c)
	caps := res.Capabilities
	if caps.TextDocumentSync == nil || caps.TextDocumentSync.Change != protocol.SyncIncremental || !caps.TextDocumentSync.OpenClose {
		t.Fatalf("textDocumentSync = %+v", caps.TextDocumentSync)
	}
	if !caps.HoverProvider {
		t.Fatal("hoverProvider not advertised")
	}
	st := caps.SemanticTokensProvider
	if st == nil || !st.Full || !reflect.DeepEqual(st.Legend.TokenTypes, TokenTypes) || !reflect.DeepEqual(st.Legend.TokenModifiers, TokenModifiers) {
		t.Fatalf("semanticTokensProvider = %+v", st)
	}
	if res.ServerInfo == nil || res.ServerInfo.Name != "falcon-asm" || res.ServerInfo.Version != "test" {
		t.Fatalf("serverInfo = %+v", res.ServerInfo)
	}

	openDoc(c, "\tcli\n\tlgdt gdt_desc\n")
	c.notify(protocol.MethodDidChange, protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{URI: testURI, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{
			{Range: &protocol.Range{Start: protocol.Position{Line: 0, Character: 1}, End: protocol.Position{Line: 0, Character: 4}}, Text: "sti"},
		},
	})
	c.notify(protocol.MethodCancelRequest, map[string]int{"id": 99})

	wantResult(t, hoverAt(c, 1, 2), "null")
	wantResult(t, semtok(c), `{"data":[]}`)
	wantError(t, c.call("textDocument/definition", map[string]any{}), jsonrpc.MethodNotFound)
	c.notify("workspace/didChangeWatchedFiles", map[string]any{})

	wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
	wantError(t, hoverAt(c, 0, 0), jsonrpc.InvalidRequest)
	c.notify(protocol.MethodExit, nil)
	if code := c.wait(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestInjectedFeatures checks that hover and semantic tokens see the synced
// document and that their results and errors reach the wire intact.
func TestInjectedFeatures(t *testing.T) {
	var hoverDoc *docstore.Document
	var hoverPos protocol.Position
	failHover := false
	opts := defaultOpts()
	opts.Hover = func(doc *docstore.Document, p protocol.Position) (*protocol.Hover, error) {
		hoverDoc, hoverPos = doc, p
		if failHover {
			return nil, errors.New("hover broke")
		}
		off, err := doc.Offset(p)
		if err != nil {
			return nil, err
		}
		if off >= len(doc.Text) || doc.Text[off] == '\n' {
			return nil, nil
		}
		return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: "**" + doc.Text[off:off+3] + "**"}}, nil
	}
	failTokens := false
	opts.SemanticTokens = func(doc *docstore.Document) ([]uint32, error) {
		if failTokens {
			return nil, errors.New("tokens broke")
		}
		return []uint32{0, 1, uint32(len(doc.Text)), TokenKeyword, 0}, nil
	}
	c := start(t, opts)
	initialize(t, c)

	// Hover and tokens for a document that is not open answer empty, not
	// with an error and without calling the feature.
	wantResult(t, hoverAt(c, 0, 0), "null")
	if hoverDoc != nil {
		t.Fatal("hover func called for unopened document")
	}
	wantResult(t, semtok(c), `{"data":[]}`)

	// The emoji is two UTF-16 units, so character 5 is the 'm' of mov; this
	// proves the feature receives the wire position and converts it itself.
	openDoc(c, "# \U0001F600 mov\n")
	wantResult(t, hoverAt(c, 0, 5), `{"contents":{"kind":"markdown","value":"**mov**"}}`)
	c.notify(protocol.MethodDidChange, protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{URI: testURI, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: "    mov %ax, %bx\n"}},
	})
	wantResult(t, hoverAt(c, 0, 4), `{"contents":{"kind":"markdown","value":"**mov**"}}`)
	if hoverDoc == nil || hoverDoc.Version != 2 || hoverPos != (protocol.Position{Line: 0, Character: 4}) {
		t.Fatalf("hover saw doc %+v pos %+v", hoverDoc, hoverPos)
	}
	wantResult(t, hoverAt(c, 0, 99), "null")
	wantResult(t, semtok(c), `{"data":[0,1,17,0,0]}`)

	failHover, failTokens = true, true
	wantError(t, hoverAt(c, 0, 4), jsonrpc.InternalError)
	wantError(t, semtok(c), jsonrpc.InternalError)

	c.notify(protocol.MethodDidClose, protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: testURI}})
	failHover = false
	hoverDoc = nil
	wantResult(t, hoverAt(c, 0, 4), "null")
	if hoverDoc != nil {
		t.Fatal("hover func called after didClose")
	}

	wantResult(t, c.call(protocol.MethodShutdown, nil), "null")
	c.notify(protocol.MethodExit, nil)
	if code := c.wait(); code != 0 {
		t.Fatalf("exit code = %d", code)
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
				m, err := jsonrpc.Read(c.out)
				if err != nil {
					t.Fatal(err)
				}
				wantError(t, m, jsonrpc.ParseError)
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
