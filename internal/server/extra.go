package server

import (
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Features added after the Engine interface settled are optional
// interfaces: an engine opts in by implementing one, the capability is
// advertised only then, and engines and fakes written before them keep
// compiling unchanged.

// CodeLensEngine answers textDocument/codeLens (size budgets, item 47).
type CodeLensEngine interface {
	CodeLens(doc *docstore.Document) []protocol.CodeLens
}

// WorkspaceSymbolEngine answers workspace/symbol (item 85).
type WorkspaceSymbolEngine interface {
	WorkspaceSymbols(query string) []protocol.SymbolInformation
}

// CallHierarchyEngine answers the call hierarchy requests (item 85).
type CallHierarchyEngine interface {
	PrepareCallHierarchy(doc *docstore.Document, pos protocol.Position) []protocol.CallHierarchyItem
	IncomingCalls(item protocol.CallHierarchyItem) []protocol.CallHierarchyIncomingCall
	OutgoingCalls(item protocol.CallHierarchyItem) []protocol.CallHierarchyOutgoingCall
}

func (s *Server) extraCapabilities(c *protocol.ServerCapabilities) {
	if _, ok := s.eng.(CodeLensEngine); ok {
		// Lenses arrive with their titles, so there is nothing to resolve.
		c.CodeLensProvider = &protocol.CodeLensOptions{ResolveProvider: false}
	}
	if _, ok := s.eng.(WorkspaceSymbolEngine); ok {
		c.WorkspaceSymbolProvider = true
	}
	if _, ok := s.eng.(CallHierarchyEngine); ok {
		c.CallHierarchyProvider = true
	}
}

// dispatchExtra handles the optional methods; ok is false for a method it
// does not know or an engine without the feature (MethodNotFound then).
func (s *Server) dispatchExtra(m *jsonrpc.Message) (any, *jsonrpc.Error, bool) {
	switch m.Method {
	case protocol.MethodCodeLens:
		eng, ok := s.eng.(CodeLensEngine)
		if !ok {
			return nil, nil, false
		}
		var p protocol.CodeLensParams
		doc, open, jerr := s.docAt(m.Params, &p, func() string { return p.TextDocument.URI })
		if jerr != nil || !open {
			return nil, jerr, true
		}
		return nilIfEmpty(eng.CodeLens(doc)), nil, true
	case protocol.MethodWorkspaceSymbol:
		eng, ok := s.eng.(WorkspaceSymbolEngine)
		if !ok {
			return nil, nil, false
		}
		var p protocol.WorkspaceSymbolParams
		if jerr := decode(m.Params, &p); jerr != nil {
			return nil, jerr, true
		}
		// An empty list rather than null: some clients treat null from
		// workspace/symbol as "not supported" and stop asking.
		res := eng.WorkspaceSymbols(p.Query)
		if res == nil {
			res = []protocol.SymbolInformation{}
		}
		return res, nil, true
	case protocol.MethodPrepareCallHierarchy:
		eng, ok := s.eng.(CallHierarchyEngine)
		if !ok {
			return nil, nil, false
		}
		var p protocol.CallHierarchyPrepareParams
		doc, open, jerr := s.docAt(m.Params, &p, func() string { return p.TextDocument.URI })
		if jerr != nil || !open {
			return nil, jerr, true
		}
		return nilIfEmpty(eng.PrepareCallHierarchy(doc, p.Position)), nil, true
	case protocol.MethodIncomingCalls, protocol.MethodOutgoingCalls:
		eng, ok := s.eng.(CallHierarchyEngine)
		if !ok {
			return nil, nil, false
		}
		var p protocol.CallHierarchyCallsParams
		if jerr := decode(m.Params, &p); jerr != nil {
			return nil, jerr, true
		}
		if m.Method == protocol.MethodIncomingCalls {
			return nilIfEmpty(eng.IncomingCalls(p.Item)), nil, true
		}
		return nilIfEmpty(eng.OutgoingCalls(p.Item)), nil, true
	}
	return nil, nil, false
}
