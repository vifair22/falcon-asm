package engine

import (
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Workspace symbols and the call hierarchy (roadmap item 85) are answered
// by the symbol index, which already holds every open document, the
// headers they include and the workspace's .S/.s files. C documents get
// nothing: their functions belong to clangd.

// WorkspaceSymbols implements server.WorkspaceSymbolEngine.
func (e *Engine) WorkspaceSymbols(query string) []protocol.SymbolInformation {
	return e.index.WorkspaceSymbols(query, 0)
}

// PrepareCallHierarchy implements server.CallHierarchyEngine.
func (e *Engine) PrepareCallHierarchy(doc *docstore.Document, pos protocol.Position) []protocol.CallHierarchyItem {
	if e.snapshotFor(doc).lang != langAsm {
		return nil
	}
	return e.index.PrepareCallHierarchy(doc.URI, pos)
}

// IncomingCalls implements server.CallHierarchyEngine.
func (e *Engine) IncomingCalls(item protocol.CallHierarchyItem) []protocol.CallHierarchyIncomingCall {
	return e.index.IncomingCalls(item)
}

// OutgoingCalls implements server.CallHierarchyEngine.
func (e *Engine) OutgoingCalls(item protocol.CallHierarchyItem) []protocol.CallHierarchyOutgoingCall {
	return e.index.OutgoingCalls(item)
}
