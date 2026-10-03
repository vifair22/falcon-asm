package protocol

// Method names for code lenses, workspace symbols and the call hierarchy
// (roadmap items 47 and 85).
const (
	MethodCodeLens             = "textDocument/codeLens"
	MethodWorkspaceSymbol      = "workspace/symbol"
	MethodPrepareCallHierarchy = "textDocument/prepareCallHierarchy"
	MethodIncomingCalls        = "callHierarchy/incomingCalls"
	MethodOutgoingCalls        = "callHierarchy/outgoingCalls"
)

// CodeLensOptions advertises code lenses. The server sends finished
// lenses (title included), so it never needs a resolve round trip.
type CodeLensOptions struct {
	ResolveProvider bool `json:"resolveProvider"`
}

// CodeLensParams is the textDocument/codeLens payload.
type CodeLensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// Command is what a lens shows and runs. An empty Command string makes a
// title-only lens: clients draw it and do nothing on click.
type Command struct {
	Title   string `json:"title"`
	Command string `json:"command"`
}

// CodeLens is a line of text drawn above Range.
type CodeLens struct {
	Range   Range    `json:"range"`
	Command *Command `json:"command,omitempty"`
}

// WorkspaceSymbolParams is the workspace/symbol payload.
type WorkspaceSymbolParams struct {
	Query string `json:"query"`
}

// SymbolInformation is one workspace symbol result.
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName,omitempty"`
}

// SymbolKindFile marks a call hierarchy item standing for a file's
// top-level code, which no label encloses.
const SymbolKindFile SymbolKind = 1

// CallHierarchyItem is a function (a call target or global label) in the
// call hierarchy. Clients send it back unchanged in the incoming and
// outgoing requests, so URI and SelectionRange identify it.
type CallHierarchyItem struct {
	Name           string     `json:"name"`
	Kind           SymbolKind `json:"kind"`
	Detail         string     `json:"detail,omitempty"`
	URI            string     `json:"uri"`
	Range          Range      `json:"range"`
	SelectionRange Range      `json:"selectionRange"`
}

// CallHierarchyPrepareParams is the textDocument/prepareCallHierarchy payload.
type CallHierarchyPrepareParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// CallHierarchyCallsParams is the payload of both incomingCalls and
// outgoingCalls.
type CallHierarchyCallsParams struct {
	Item CallHierarchyItem `json:"item"`
}

// CallHierarchyIncomingCall is a caller of the item and where it calls it.
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyOutgoingCall is a callee of the item and where the item
// calls it (ranges in the item's document).
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}
