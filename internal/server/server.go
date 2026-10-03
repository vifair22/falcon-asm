// Package server implements the LSP lifecycle and method dispatch for
// falcon-asm. Language features (hover, semantic tokens) live in their own
// packages and are injected through Options, so this package depends only on
// the transport, the protocol types and the document store.
//
// Requests are handled one at a time, in arrival order, on the read loop's
// goroutine. Every handler is a cheap function of an in-memory document, so
// concurrency would buy nothing measurable, while sequential handling makes
// ordering trivially correct: a hover always sees every didChange sent before
// it. It also means $/cancelRequest can safely be ignored, since by the time
// it is read the request it names has already been answered.
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

// HoverFunc produces hover content for a position. Returning nil, nil means
// there is nothing to show and is answered with a null result.
type HoverFunc func(doc *docstore.Document, pos protocol.Position) (*protocol.Hover, error)

// SemanticTokensFunc produces the relative-encoded token stream for a whole
// document.
type SemanticTokensFunc func(doc *docstore.Document) ([]uint32, error)

// Options configures a Server. Nil function fields disable the feature
// without disabling the capability: hover answers null and semantic tokens
// answer an empty stream, so a client never sees an error for a feature that
// simply has nothing to say yet.
type Options struct {
	Hover          HoverFunc
	SemanticTokens SemanticTokensFunc
	TokenTypes     []string
	TokenModifiers []string

	// Name and Version are reported in the initialize result.
	Name    string
	Version string

	// Log receives diagnostic output. It must not be the protocol stream;
	// main passes stderr. Nil discards.
	Log io.Writer
}

type state int

const (
	stateNew state = iota
	stateRunning
	stateShutdown
)

// Server is a single LSP session. It is not reusable: call Run once.
type Server struct {
	opts  Options
	docs  *docstore.Store
	log   *log.Logger
	conn  *jsonrpc.Conn
	state state
	exit  int
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
	return &Server{
		opts: opts,
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
		return nil, nil
	case protocol.MethodHover:
		return s.hover(m.Params)
	case protocol.MethodSemanticTokensFull:
		return s.semanticTokens(m.Params)
	default:
		return nil, jsonrpc.NewError(jsonrpc.MethodNotFound, "method not found: %s", m.Method)
	}
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
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: true,
				Change:    protocol.SyncIncremental,
			},
			HoverProvider: true,
			SemanticTokensProvider: &protocol.SemanticTokensOptions{
				Legend: protocol.SemanticTokensLegend{
					TokenTypes:     s.opts.TokenTypes,
					TokenModifiers: s.opts.TokenModifiers,
				},
				Full: true,
			},
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
	return nil
}

func (s *Server) didClose(params json.RawMessage) error {
	var p protocol.DidCloseTextDocumentParams
	if err := decode(params, &p); err != nil {
		return err
	}
	s.docs.Close(p.TextDocument.URI)
	return nil
}

func (s *Server) hover(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.HoverParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	doc, ok := s.docs.Get(p.TextDocument.URI)
	if !ok || s.opts.Hover == nil {
		return nil, nil
	}
	h, err := s.opts.Hover(doc, p.Position)
	if err != nil {
		return nil, jsonrpc.NewError(jsonrpc.InternalError, "hover: %v", err)
	}
	if h == nil {
		// Return an untyped nil so the reply is "result":null regardless of
		// how the encoder treats a typed nil inside an interface.
		return nil, nil
	}
	return h, nil
}

func (s *Server) semanticTokens(params json.RawMessage) (any, *jsonrpc.Error) {
	var p protocol.SemanticTokensParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	doc, ok := s.docs.Get(p.TextDocument.URI)
	if !ok || s.opts.SemanticTokens == nil {
		return protocol.SemanticTokens{}, nil
	}
	data, err := s.opts.SemanticTokens(doc)
	if err != nil {
		return nil, jsonrpc.NewError(jsonrpc.InternalError, "semantic tokens: %v", err)
	}
	return protocol.SemanticTokens{Data: data}, nil
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
