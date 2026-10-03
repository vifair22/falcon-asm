package server

import (
	"encoding/json"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// extraEngine adds the optional features to the scripted fake.
type extraEngine struct {
	fakeEngine
	lastItem protocol.CallHierarchyItem
}

func (f *extraEngine) CodeLens(d *docstore.Document) []protocol.CodeLens {
	return []protocol.CodeLens{{Command: &protocol.Command{Title: "lens " + d.URI}}}
}

func (f *extraEngine) WorkspaceSymbols(q string) []protocol.SymbolInformation {
	if q == "" {
		return nil
	}
	return []protocol.SymbolInformation{{Name: q, Kind: protocol.SymbolKindFunction}}
}

func (f *extraEngine) PrepareCallHierarchy(d *docstore.Document, p protocol.Position) []protocol.CallHierarchyItem {
	return []protocol.CallHierarchyItem{{Name: "fn", URI: d.URI, SelectionRange: protocol.Range{Start: p}}}
}

func (f *extraEngine) IncomingCalls(it protocol.CallHierarchyItem) []protocol.CallHierarchyIncomingCall {
	f.lastItem = it
	return []protocol.CallHierarchyIncomingCall{{From: it, FromRanges: []protocol.Range{{}}}}
}

func (f *extraEngine) OutgoingCalls(it protocol.CallHierarchyItem) []protocol.CallHierarchyOutgoingCall {
	return nil
}

func TestExtraFeatures(t *testing.T) {
	opts := defaultOpts()
	opts.Engine = &extraEngine{}
	c := start(t, opts)
	res := initialize(t, c)
	caps := res.Capabilities
	if caps.CodeLensProvider == nil || caps.CodeLensProvider.ResolveProvider || !caps.WorkspaceSymbolProvider || !caps.CallHierarchyProvider {
		t.Fatalf("capabilities = %+v", caps)
	}
	openDoc(c, "\tnop\n")
	doc := protocol.TextDocumentIdentifier{URI: testURI}
	wantResult(t, c.call(protocol.MethodCodeLens, protocol.CodeLensParams{TextDocument: doc}),
		`[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"command":{"title":"lens `+testURI+`","command":""}}]`)
	wantResult(t, c.call(protocol.MethodCodeLens, protocol.CodeLensParams{TextDocument: protocol.TextDocumentIdentifier{URI: "file:///none"}}), "null")
	wantResult(t, c.call(protocol.MethodWorkspaceSymbol, protocol.WorkspaceSymbolParams{Query: ""}), "[]")
	wantResult(t, c.call(protocol.MethodWorkspaceSymbol, protocol.WorkspaceSymbolParams{Query: "x"}),
		`[{"name":"x","kind":12,"location":{"uri":"","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}}}]`)
	m := c.call(protocol.MethodPrepareCallHierarchy, protocol.CallHierarchyPrepareParams{TextDocument: doc, Position: protocol.Position{Line: 0, Character: 2}})
	var items []protocol.CallHierarchyItem
	if err := json.Unmarshal(m.Result, &items); err != nil || len(items) != 1 {
		t.Fatalf("prepare = %s", m.Result)
	}
	m = c.call(protocol.MethodIncomingCalls, protocol.CallHierarchyCallsParams{Item: items[0]})
	var in []protocol.CallHierarchyIncomingCall
	if err := json.Unmarshal(m.Result, &in); err != nil || len(in) != 1 || in[0].From.SelectionRange.Start.Character != 2 {
		t.Fatalf("incoming = %s", m.Result)
	}
	wantResult(t, c.call(protocol.MethodOutgoingCalls, protocol.CallHierarchyCallsParams{Item: items[0]}), "null")
	wantResult(t, c.call(protocol.MethodPrepareCallHierarchy, protocol.CallHierarchyPrepareParams{TextDocument: protocol.TextDocumentIdentifier{URI: "file:///none"}}), "null")
	// Malformed params are InvalidParams for every one of them.
	for _, method := range []string{protocol.MethodCodeLens, protocol.MethodWorkspaceSymbol, protocol.MethodPrepareCallHierarchy, protocol.MethodIncomingCalls, protocol.MethodOutgoingCalls} {
		wantError(t, c.call(method, []int{1}), jsonrpc.InvalidParams)
	}
}

// An engine without the features advertises none of them and answers
// MethodNotFound, as for any unknown method.
func TestExtraFeaturesAbsent(t *testing.T) {
	c := start(t, defaultOpts())
	res := initialize(t, c)
	if res.Capabilities.CodeLensProvider != nil || res.Capabilities.WorkspaceSymbolProvider || res.Capabilities.CallHierarchyProvider {
		t.Fatalf("capabilities = %+v", res.Capabilities)
	}
	for _, method := range []string{protocol.MethodCodeLens, protocol.MethodWorkspaceSymbol, protocol.MethodPrepareCallHierarchy, protocol.MethodIncomingCalls, protocol.MethodOutgoingCalls} {
		wantError(t, c.call(method, map[string]any{}), jsonrpc.MethodNotFound)
	}
}
