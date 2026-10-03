// Package server implements the LSP lifecycle and method dispatch for
// falcon-asm. Language features live behind the Engine interface (the real
// one is internal/engine), so this package depends only on the transport,
// the protocol types and the document store, and is tested with fakes.
//
// Requests are handled one at a time, in arrival order, on the read loop's
// goroutine. Every handler is a cheap function of an in-memory document, so
// concurrency would buy nothing measurable, while sequential handling makes
// ordering trivially correct: a hover always sees every didChange sent before
// it. It also means $/cancelRequest can safely be ignored, since by the time
// it is read the request it names has already been answered. The slow work
// (running the toolchain) happens in the engine's own goroutines, which
// publish diagnostics through the connection's mutex-guarded writer.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Options configures a Server.
type Options struct {
	// Engine provides every language feature; nil means NopEngine.
	Engine         Engine
	TokenTypes     []string
	TokenModifiers []string

	// Name and Version are reported in the initialize result.
	Name    string
	Version string

	// Log receives diagnostic output. It must not be the protocol stream;
	// main passes stderr. Nil discards.
	Log io.Writer
}

// AsmLanguageID is the language semantic tokens are registered for. C and
// C++ documents are served too (inline asm hover and diagnostics) but keep
// their own language server's highlighting.
const AsmLanguageID = "falcon-asm"

// TriggerCharacters open completion: registers, directives, immediates.
var TriggerCharacters = []string{"%", ".", "$"}

type state int

const (
	stateNew state = iota
	stateRunning
	stateShutdown
)

// Server is a single LSP session. It is not reusable: call Run once.
type Server struct {
	opts    Options
	eng     Engine
	docs    *docstore.Store
	log     *log.Logger
	conn    *jsonrpc.Conn
	state   state
	exit    int
	stopped bool
}

// New builds a server from opts.
func New(opts Options) *Server {
	w := opts.Log
	if w == nil {
		w = io.Discard
	}
	if opts.TokenTypes == nil {
		opts.TokenTypes = []string{}
	}
	if opts.TokenModifiers == nil {
		opts.TokenModifiers = []string{}
	}
	eng := opts.Engine
	if eng == nil {
		eng = NopEngine{}
	}
	return &Server{
		opts: opts,
		eng:  eng,
		docs: docstore.New(),
		log:  log.New(w, "falcon-asm: ", log.LstdFlags|log.Lmsgprefix),
	}
}

// Run speaks LSP over r and w until the client sends exit or the stream
// ends, and returns the process exit code the LSP prescribes: 0 if shutdown
// preceded exit, 1 otherwise (including a client that vanished).
func (s *Server) Run(r io.Reader, w io.Writer) int {
	s.conn = jsonrpc.NewConn(r, w)
	s.exit = 1
	err := s.conn.Serve(s.handle)
	// A client that vanished without shutdown still must not leave
	// toolchain processes behind.
	s.stopEngine()
	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		s.log.Printf("client closed the stream without exit")
	default:
		s.log.Printf("connection failed: %v", err)
	}
	return s.exit
}

// handle dispatches one message; it returns true once exit has been seen.
func (s *Server) handle(m *jsonrpc.Message) bool {
	switch {
	case m.IsResponse():
		// The server sends no requests, so any response is stray.
		return false
	case m.IsNotification():
		return s.handleNotification(m)
	case m.IsRequest():
		s.handleRequest(m)
		return false
	default:
		s.log.Printf("dropping malformed message")
		return false
	}
}

func (s *Server) handleNotification(m *jsonrpc.Message) bool {
	if m.Method == protocol.MethodExit {
		if s.state == stateShutdown {
			s.exit = 0
		}
		return true
	}
	// The LSP says notifications before initialize are dropped, and after
	// shutdown only exit matters.
	if s.state != stateRunning {
		return false
	}
	var err error
	switch m.Method {
	case protocol.MethodInitialized, protocol.MethodCancelRequest:
	case protocol.MethodDidOpen:
		err = s.didOpen(m.Params)
	case protocol.MethodDidChange:
		err = s.didChange(m.Params)
	case protocol.MethodDidClose:
		err = s.didClose(m.Params)
	case protocol.MethodDidSave:
		err = s.didSave(m.Params)
	}
	if err != nil {
		// A notification has no reply channel; the log is the only place a
		// sync failure can surface.
		s.log.Printf("%s: %v", m.Method, err)
	}
	return false
}

func (s *Server) handleRequest(m *jsonrpc.Message) {
	result, rerr := s.dispatch(m)
	var err error
	if rerr != nil {
		err = s.conn.ReplyError(m.ID, rerr)
	} else {
		err = s.conn.Reply(m.ID, result)
	}
	if err != nil {
		s.log.Printf("reply to %s: %v", m.Method, err)
	}
}

func (s *Server) dispatch(m *jsonrpc.Message) (any, *jsonrpc.Error) {
	switch s.state {
	case stateNew:
		if m.Method != protocol.MethodInitialize {
			return nil, jsonrpc.NewError(jsonrpc.ServerNotInitialized, "server not initialized")
		}
	case stateShutdown:
		return nil, jsonrpc.NewError(jsonrpc.InvalidRequest, "server is shut down")
	case stateRunning:
	}
	switch m.Method {
	case protocol.MethodInitialize:
		return s.initialize(m.Params)
	case protocol.MethodShutdown:
		s.state = stateShutdown
		s.stopEngine()
		return nil, nil
	case protocol.MethodHover:
		return s.hover(m.Params)
	case protocol.MethodSemanticTokensFull:
		return s.semanticTokens(m.Params)
	case protocol.MethodDefinition:
		return s.definition(m.Params)
	case protocol.MethodReferences:
		return s.references(m.Params)
	case protocol.MethodPrepareRename:
		return s.prepareRename(m.Params)
	case protocol.MethodRename:
		return s.rename(m.Params)
	case protocol.MethodDocumentSymbol:
		return s.documentSymbol(m.Params)
	case protocol.MethodFoldingRange:
		return s.foldingRange(m.Params)
	case protocol.MethodInlayHint:
		return s.inlayHint(m.Params)
	case protocol.MethodCompletion:
		return s.completion(m.Params)
	default:
		return nil, jsonrpc.NewError(jsonrpc.MethodNotFound, "method not found: %s", m.Method)
	}
}

// stopEngine shuts the engine down once, whichever of shutdown, exit or a
// closed stream comes first.
func (s *Server) stopEngine() {
	if s.stopped {
		return
	}
	s.stopped = true
	s.eng.Shutdown()
}

func (s *Server) initialize(params json.RawMessage) (any, *jsonrpc.Error) {
	if s.state != stateNew {
		return nil, jsonrpc.NewError(jsonrpc.InvalidRequest, "initialize sent twice")
	}
	var p protocol.InitializeParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	if p.ClientInfo != nil {
		s.log.Printf("initialize from %s %s", p.ClientInfo.Name, p.ClientInfo.Version)
	}
	s.state = stateRunning
	root := ""
	switch {
	case p.RootURI != nil && *p.RootURI != "":
		root = *p.RootURI
	case len(p.WorkspaceFolders) > 0:
		root = p.WorkspaceFolders[0].URI
	}
	conn := s.conn
	s.eng.Initialize(Session{
		RootURI:     root,
		InitOptions: p.InitializationOptions,
		Publish: func(d protocol.PublishDiagnosticsParams) {
			if d.Diagnostics == nil {
				d.Diagnostics = []protocol.Diagnostic{}
			}
			if err := conn.Notify(protocol.MethodPublishDiagnostics, d); err != nil {
				s.log.Printf("publish diagnostics for %s: %v", d.URI, err)
			}
		},
	})
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: true,
				Change:    protocol.SyncIncremental,
				Save:      &protocol.SaveOptions{IncludeText: false},
			},
			HoverProvider: true,
			SemanticTokensProvider: &protocol.SemanticTokensOptions{
				DocumentSelector: []protocol.DocumentFilter{{Language: AsmLanguageID}},
				Legend: protocol.SemanticTokensLegend{
					TokenTypes:     s.opts.TokenTypes,
					TokenModifiers: s.opts.TokenModifiers,
				},
				Full: true,
			},
			DefinitionProvider:     true,
			ReferencesProvider:     true,
			RenameProvider:         &protocol.RenameOptions{PrepareProvider: true},
			DocumentSymbolProvider: true,
			FoldingRangeProvider:   true,
			InlayHintProvider:      true,
			CompletionProvider:     &protocol.CompletionOptions{TriggerCharacters: TriggerCharacters},
		},
		ServerInfo: &protocol.ServerInfo{Name: s.opts.Name, Version: s.opts.Version},
	}, nil
}

func (s *Server) didOpen(params json.RawMessage) error {
	var p protocol.DidOpenTextDocumentParams
	if err := decode(params, &p); err != nil {
		return err
	}
	s.docs.Open(p.TextDocument.URI, p.TextDocument.Version, p.TextDocument.Text)
	if doc, ok := s.docs.Get(p.TextDocument.URI); ok {
		s.eng.Open(doc, p.TextDocument.LanguageID)
	}
	return nil
}

func (s *Server) didChange(params json.RawMessage) error {
	var p protocol.DidChangeTextDocumentParams
	if err := decode(params, &p); err != nil {
		return err
	}
	if err := s.docs.Change(p.TextDocument.URI, p.TextDocument.Version, p.ContentChanges); err != nil {
		return fmt.Errorf("apply edits: %w", err)
	}
	if doc, ok := s.docs.Get(p.TextDocument.URI); ok {
		s.eng.Change(doc)
	}
	return nil
}

func (s *Server) didSave(params json.RawMessage) error {
	var p protocol.DidSaveTextDocumentParams
	if err := decode(params, &p); err != nil {
		return err
	}
	if _, ok := s.docs.Get(p.TextDocument.URI); ok {
		s.eng.Save(p.TextDocument.URI)
	}
	return nil
}

func (s *Server) didClose(params json.RawMessage) error {
	var p protocol.DidCloseTextDocumentParams
	if err := decode(params, &p); err != nil {
		return err
	}
	s.docs.Close(p.TextDocument.URI)
	s.eng.Close(p.TextDocument.URI)
	return nil
}

// docAt decodes a position request and finds its document. ok is false
// when the document is not open, which every feature answers with null.
func (s *Server) docAt(params json.RawMessage, p any, uri func() string) (*docstore.Document, bool, *jsonrpc.Error) {
	if err := decode(params, p); err != nil {
		return nil, false, err
	}
	doc, ok := s.docs.Get(uri())
	return doc, ok, nil
}

// reply turns an engine error into the wire error. Refusals the user should
// read go out as RequestFailed with the message unchanged.
func reply(method string, v any, err error) (any, *jsonrpc.Error) {
	if err == nil {
		return v, nil
	}
	if rf, ok := errors.AsType[*RequestFailedError](err); ok {
		return nil, jsonrpc.NewError(jsonrpc.RequestFailed, "%s", rf.Error())
	}
	return nil, jsonrpc.NewError(jsonrpc.InternalError, "%s: %v", method, err)
}

func (s *Server) hover(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.HoverParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	h, err := s.eng.Hover(doc, p.Position)
	if err != nil || h == nil {
		// An untyped nil keeps the reply "result":null regardless of how
		// the encoder treats a typed nil inside an interface.
		return reply("hover", nil, err)
	}
	return h, nil
}

func (s *Server) semanticTokens(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.SemanticTokensParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil {
		return nil, jerr
	}
	if !ok {
		return protocol.SemanticTokens{}, nil
	}
	return protocol.SemanticTokens{Data: s.eng.SemanticTokens(doc)}, nil
}

func (s *Server) definition(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.TextDocumentPositionParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	return nilIfEmpty(s.eng.Definition(doc, p.Position)), nil
}

func (s *Server) references(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.ReferenceParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	return nilIfEmpty(s.eng.References(doc, p.Position, p.Context.IncludeDeclaration)), nil
}

func (s *Server) prepareRename(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.TextDocumentPositionParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	r, err := s.eng.PrepareRename(doc, p.Position)
	if err != nil || r == nil {
		return reply("prepareRename", nil, err)
	}
	return r, nil
}

func (s *Server) rename(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.RenameParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	e, err := s.eng.Rename(doc, p.Position, p.NewName)
	if err != nil || e == nil {
		return reply("rename", nil, err)
	}
	return e, nil
}

func (s *Server) documentSymbol(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.DocumentSymbolParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	return nilIfEmpty(s.eng.DocumentSymbols(doc)), nil
}

func (s *Server) foldingRange(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.FoldingRangeParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	return nilIfEmpty(s.eng.FoldingRanges(doc)), nil
}

func (s *Server) inlayHint(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.InlayHintParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	return nilIfEmpty(s.eng.InlayHints(doc, p.Range)), nil
}

func (s *Server) completion(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.CompletionParams
	doc, ok, jerr := s.docAt(params, &p, func() string { return p.TextDocument.URI })
	if jerr != nil || !ok {
		return nil, jerr
	}
	if l := s.eng.Completion(doc, p.Position); l != nil {
		return l, nil
	}
	return nil, nil
}

// nilIfEmpty answers an empty slice as null. The LSP allows either for
// these methods; null is what "nothing here" means everywhere else in the
// server, and an untyped nil cannot become a typed-nil "null" by accident.
func nilIfEmpty[T any](v []T) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

// decode treats absent params as an empty object, since several LSP methods
// make params optional and an empty struct is the right zero value.
func decode(params json.RawMessage, v any) *jsonrpc.Error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return jsonrpc.NewError(jsonrpc.InvalidParams, "invalid params: %v", err)
	}
	return nil
}
