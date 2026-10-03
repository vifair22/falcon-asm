package protocol

// Types for the phase 2 and 3 features: navigation, outline, folding, inlay
// hints, completion and rename. Only the fields the server sets or reads.

// Method names added in phase 2 and 3.
const (
	MethodDidSave          = "textDocument/didSave"
	MethodDefinition       = "textDocument/definition"
	MethodReferences       = "textDocument/references"
	MethodPrepareRename    = "textDocument/prepareRename"
	MethodRename           = "textDocument/rename"
	MethodDocumentSymbol   = "textDocument/documentSymbol"
	MethodFoldingRange     = "textDocument/foldingRange"
	MethodInlayHint        = "textDocument/inlayHint"
	MethodCompletion       = "textDocument/completion"
	MethodDidChangeWatched = "workspace/didChangeWatchedFiles"
)

// DidSaveTextDocumentParams is sent after the client wrote the file.
type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

// ReferenceParams asks for every use of the symbol at a position.
type ReferenceParams struct {
	TextDocumentPositionParams
	Context struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}

// RenameParams renames the symbol at a position.
type RenameParams struct {
	TextDocumentPositionParams
	NewName string `json:"newName"`
}

// TextEdit replaces Range with NewText.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit groups edits by document URI.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// SymbolKind values used for assembly symbols.
type SymbolKind int

// The subset of LSP symbol kinds the outline uses.
const (
	SymbolKindModule    SymbolKind = 2  // section
	SymbolKindFunction  SymbolKind = 12 // global label
	SymbolKindVariable  SymbolKind = 13 // local label, .set/.equ
	SymbolKindConstant  SymbolKind = 14 // #define
	SymbolKindNamespace SymbolKind = 3  // .macro
)

// DocumentSymbol is one outline entry; Children nest labels under sections.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// FoldingRange kinds.
const (
	FoldingComment = "comment"
	FoldingRegion  = "region"
)

// FoldingRange folds whole lines StartLine..EndLine.
type FoldingRange struct {
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Kind      string `json:"kind,omitempty"`
}

// InlayHintParams asks for hints inside a range of a document.
type InlayHintParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

// InlayHint is text drawn inline at Position.
type InlayHint struct {
	Position     Position `json:"position"`
	Label        string   `json:"label"`
	PaddingLeft  bool     `json:"paddingLeft,omitempty"`
	PaddingRight bool     `json:"paddingRight,omitempty"`
	Tooltip      string   `json:"tooltip,omitempty"`
}

// CompletionItemKind values used by the server.
type CompletionItemKind int

// The subset of completion kinds the server uses.
const (
	CompletionKindFunction  CompletionItemKind = 3  // mnemonic
	CompletionKindVariable  CompletionItemKind = 6  // register
	CompletionKindKeyword   CompletionItemKind = 14 // directive, prefix
	CompletionKindConstant  CompletionItemKind = 21 // macro
	CompletionKindReference CompletionItemKind = 18 // label
)

// CompletionParams is a position plus how completion was triggered.
type CompletionParams struct {
	TextDocumentPositionParams
}

// CompletionItem is one suggestion.
type CompletionItem struct {
	Label      string             `json:"label"`
	Kind       CompletionItemKind `json:"kind,omitempty"`
	Detail     string             `json:"detail,omitempty"`
	SortText   string             `json:"sortText,omitempty"`
	FilterText string             `json:"filterText,omitempty"`
	InsertText string             `json:"insertText,omitempty"`
	TextEdit   *TextEdit          `json:"textEdit,omitempty"`
}

// CompletionList is returned so the client re-asks as the prefix grows.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// RenameOptions advertises rename; PrepareProvider lets the client ask
// whether a position is renamable before prompting for a name.
type RenameOptions struct {
	PrepareProvider bool `json:"prepareProvider"`
}

// PrepareRenameResult is the range a rename would edit and the text to
// prefill the prompt with.
type PrepareRenameResult struct {
	Range       Range  `json:"range"`
	Placeholder string `json:"placeholder"`
}

// CompletionOptions advertises completion and the characters that open it
// without a typed word.
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

// DocumentSymbolParams asks for a document's outline.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// FoldingRangeParams asks for a document's folding ranges.
type FoldingRangeParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}
