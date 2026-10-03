package server

import (
	"encoding/json"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// HoverFunc produces hover content for a position. Returning nil, nil means
// there is nothing to show. Kept as the contract hover.Hover is written to.
type HoverFunc func(doc *docstore.Document, pos protocol.Position) (*protocol.Hover, error)

// SemanticTokensFunc produces the relative-encoded token stream for a whole
// document. Kept as the contract semtok.Encode is written to.
type SemanticTokensFunc func(doc *docstore.Document) ([]uint32, error)

// Session is what the engine learns at initialize.
type Session struct {
	// RootURI is the workspace root: initialize's rootUri, else the first
	// workspace folder; "" when the client opened no folder.
	RootURI string
	// InitOptions is initializationOptions exactly as sent; the engine owns
	// its schema, so the server never has to change when a setting is added.
	InitOptions json.RawMessage
	// Publish sends textDocument/publishDiagnostics. It is safe to call from
	// any goroutine.
	Publish func(protocol.PublishDiagnosticsParams)
}

// Engine is everything language-specific. The server owns the protocol,
// the lifecycle and the document text; it hands the engine immutable
// document snapshots and turns its answers into replies. Every method is
// called from the server's single request goroutine, but an engine may do
// work in the background (toolchain runs) and publish from there.
//
// Query methods return nil for "nothing here"; the server sends null (or
// an empty token stream). An error wrapping a *RequestFailedError reaches the
// user as its message; any other error is an internal error.
type Engine interface {
	Initialize(s Session)
	Open(doc *docstore.Document, languageID string)
	Change(doc *docstore.Document)
	Save(uri string)
	Close(uri string)
	// Shutdown stops background work, waiting briefly for runs in flight.
	Shutdown()

	Hover(doc *docstore.Document, pos protocol.Position) (*protocol.Hover, error)
	SemanticTokens(doc *docstore.Document) []uint32
	Definition(doc *docstore.Document, pos protocol.Position) []protocol.Location
	References(doc *docstore.Document, pos protocol.Position, includeDecl bool) []protocol.Location
	PrepareRename(doc *docstore.Document, pos protocol.Position) (*protocol.PrepareRenameResult, error)
	Rename(doc *docstore.Document, pos protocol.Position, newName string) (*protocol.WorkspaceEdit, error)
	DocumentSymbols(doc *docstore.Document) []protocol.DocumentSymbol
	FoldingRanges(doc *docstore.Document) []protocol.FoldingRange
	InlayHints(doc *docstore.Document, rng protocol.Range) []protocol.InlayHint
	Completion(doc *docstore.Document, pos protocol.Position) *protocol.CompletionList
}

// RequestFailedError is an error meant for the user (a refused rename),
// answered with the LSP RequestFailed code and its message verbatim.
type RequestFailedError struct {
	Err error
}

func (e *RequestFailedError) Error() string { return e.Err.Error() }
func (e *RequestFailedError) Unwrap() error { return e.Err }

// NopEngine answers every query with nothing. It is the default, so a
// server built without features still speaks a correct protocol.
type NopEngine struct{}

var _ Engine = NopEngine{}

// Initialize implements Engine.
func (NopEngine) Initialize(Session) {}

// Open implements Engine.
func (NopEngine) Open(*docstore.Document, string) {}

// Change implements Engine.
func (NopEngine) Change(*docstore.Document) {}

// Save implements Engine.
func (NopEngine) Save(string) {}

// Close implements Engine.
func (NopEngine) Close(string) {}

// Shutdown implements Engine.
func (NopEngine) Shutdown() {}

// Hover implements Engine.
func (NopEngine) Hover(*docstore.Document, protocol.Position) (*protocol.Hover, error) {
	return nil, nil
}

// SemanticTokens implements Engine.
func (NopEngine) SemanticTokens(*docstore.Document) []uint32 { return nil }

// Definition implements Engine.
func (NopEngine) Definition(*docstore.Document, protocol.Position) []protocol.Location { return nil }

// References implements Engine.
func (NopEngine) References(*docstore.Document, protocol.Position, bool) []protocol.Location {
	return nil
}

// PrepareRename implements Engine.
func (NopEngine) PrepareRename(*docstore.Document, protocol.Position) (*protocol.PrepareRenameResult, error) {
	return nil, nil
}

// Rename implements Engine.
func (NopEngine) Rename(*docstore.Document, protocol.Position, string) (*protocol.WorkspaceEdit, error) {
	return nil, nil
}

// DocumentSymbols implements Engine.
func (NopEngine) DocumentSymbols(*docstore.Document) []protocol.DocumentSymbol { return nil }

// FoldingRanges implements Engine.
func (NopEngine) FoldingRanges(*docstore.Document) []protocol.FoldingRange { return nil }

// InlayHints implements Engine.
func (NopEngine) InlayHints(*docstore.Document, protocol.Range) []protocol.InlayHint { return nil }

// Completion implements Engine.
func (NopEngine) Completion(*docstore.Document, protocol.Position) *protocol.CompletionList {
	return nil
}
