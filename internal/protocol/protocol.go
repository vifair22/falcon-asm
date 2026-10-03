// Package protocol holds the subset of Language Server Protocol types that
// falcon-asm actually exchanges. It is written by hand rather than generated
// from the metaModel so the surface stays small and readable; add a type here
// only when a handler needs it.
package protocol

import (
	"encoding/json"
	"slices"
	"strconv"
)

// Method names the server dispatches on or emits.
const (
	MethodInitialize         = "initialize"
	MethodInitialized        = "initialized"
	MethodShutdown           = "shutdown"
	MethodExit               = "exit"
	MethodCancelRequest      = "$/cancelRequest"
	MethodDidOpen            = "textDocument/didOpen"
	MethodDidChange          = "textDocument/didChange"
	MethodDidClose           = "textDocument/didClose"
	MethodHover              = "textDocument/hover"
	MethodSemanticTokensFull = "textDocument/semanticTokens/full"
	// MethodSemanticTokensDelta asks for the edits since an earlier result.
	MethodSemanticTokensDelta = "textDocument/semanticTokens/full/delta"
	MethodLogMessage          = "window/logMessage"
	MethodPublishDiagnostics  = "textDocument/publishDiagnostics"
)

// Position is zero based. Character counts UTF-16 code units, which is the
// LSP default encoding and the only one falcon-asm advertises.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is half open: End is exclusive.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range inside a particular document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// ClientInfo identifies the editor; used only for logging.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeParams keeps client capabilities raw: the server does not vary
// its behaviour by client capability yet, and decoding the full tree would
// pull in dozens of types for nothing.
type InitializeParams struct {
	ProcessID             *int              `json:"processId"`
	ClientInfo            *ClientInfo       `json:"clientInfo,omitempty"`
	RootURI               *string           `json:"rootUri"`
	Capabilities          json.RawMessage   `json:"capabilities,omitempty"`
	InitializationOptions json.RawMessage   `json:"initializationOptions,omitempty"`
	Trace                 string            `json:"trace,omitempty"`
	WorkspaceFolders      []WorkspaceFolder `json:"workspaceFolders,omitempty"`
}

// WorkspaceFolder is one root the client has open; the first one is the
// workspace root when rootUri is absent.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// ServerInfo is reported back in the initialize result.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeResult answers initialize.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

// TextDocumentSyncKind selects how didChange carries edits.
type TextDocumentSyncKind int

// Sync kinds defined by the LSP.
const (
	SyncNone        TextDocumentSyncKind = 0
	SyncFull        TextDocumentSyncKind = 1
	SyncIncremental TextDocumentSyncKind = 2
)

// TextDocumentSyncOptions is used instead of the bare kind number because
// some clients (vscode-languageclient among them) only send didOpen and
// didClose when openClose is explicitly true.
type TextDocumentSyncOptions struct {
	OpenClose bool                 `json:"openClose"`
	Change    TextDocumentSyncKind `json:"change"`
	Save      *SaveOptions         `json:"save,omitempty"`
}

// SaveOptions asks for didSave. The text is not wanted: the toolchain reads
// the file from disk, which is what it would assemble anyway.
type SaveOptions struct {
	IncludeText bool `json:"includeText"`
}

// DocumentFilter selects documents by language id.
type DocumentFilter struct {
	Language string `json:"language,omitempty"`
}

// SemanticTokensLegend maps wire indices to token type and modifier names.
type SemanticTokensLegend struct {
	TokenTypes     []string `json:"tokenTypes"`
	TokenModifiers []string `json:"tokenModifiers"`
}

// SemanticTokensOptions advertises semantic token support: whole-document
// requests, and deltas against the previous result. Range requests are not
// offered; a delta already makes an edit's reply as small as a range's.
//
// DocumentSelector makes this the registration-options shape, which clients
// that honour it (VS Code) use to ask only for the listed languages, so C
// files opened for inline asm keep their own language server's tokens.
type SemanticTokensOptions struct {
	DocumentSelector []DocumentFilter     `json:"documentSelector,omitempty"`
	Legend           SemanticTokensLegend `json:"legend"`
	// Full is true, or SemanticTokensFullOptions to offer deltas.
	Full  any  `json:"full"`
	Range bool `json:"range"`
}

// SemanticTokensFullOptions is the object form of the full capability.
type SemanticTokensFullOptions struct {
	Delta bool `json:"delta"`
}

// ServerCapabilities lists what the server implements.
type ServerCapabilities struct {
	TextDocumentSync       *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	HoverProvider          bool                     `json:"hoverProvider,omitempty"`
	SemanticTokensProvider *SemanticTokensOptions   `json:"semanticTokensProvider,omitempty"`
	DefinitionProvider     bool                     `json:"definitionProvider,omitempty"`
	ReferencesProvider     bool                     `json:"referencesProvider,omitempty"`
	RenameProvider         *RenameOptions           `json:"renameProvider,omitempty"`
	DocumentSymbolProvider bool                     `json:"documentSymbolProvider,omitempty"`
	FoldingRangeProvider   bool                     `json:"foldingRangeProvider,omitempty"`
	InlayHintProvider      bool                     `json:"inlayHintProvider,omitempty"`
	CompletionProvider     *CompletionOptions       `json:"completionProvider,omitempty"`
	CodeActionProvider     *CodeActionOptions       `json:"codeActionProvider,omitempty"`
	// Items 47 and 85: size budget lenses, workspace symbols, call hierarchy.
	CodeLensProvider        *CodeLensOptions `json:"codeLensProvider,omitempty"`
	WorkspaceSymbolProvider bool             `json:"workspaceSymbolProvider,omitempty"`
	CallHierarchyProvider   bool             `json:"callHierarchyProvider,omitempty"`
}

// TextDocumentItem is the full document sent on open.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentIdentifier names a document.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// VersionedTextDocumentIdentifier names a document at a specific version.
type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// DidOpenTextDocumentParams is the didOpen payload.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent is either a ranged edit or, when Range is
// nil, a replacement of the whole document. RangeLength is deprecated in the
// LSP and ignored; it is kept so decoding does not silently drop it.
type TextDocumentContentChangeEvent struct {
	Range       *Range `json:"range,omitempty"`
	RangeLength *int   `json:"rangeLength,omitempty"`
	Text        string `json:"text"`
}

// DidChangeTextDocumentParams is the didChange payload. Changes apply in
// order, each against the text produced by the previous one.
type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// DidCloseTextDocumentParams is the didClose payload.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// TextDocumentPositionParams is the common shape of position requests.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// HoverParams is the textDocument/hover payload.
type HoverParams struct {
	TextDocumentPositionParams
}

// MarkupKind selects how MarkupContent is rendered.
type MarkupKind string

// Markup kinds defined by the LSP.
const (
	PlainText MarkupKind = "plaintext"
	Markdown  MarkupKind = "markdown"
)

// MarkupContent is rendered text.
type MarkupContent struct {
	Kind  MarkupKind `json:"kind"`
	Value string     `json:"value"`
}

// Hover is the hover result. A request with nothing to show is answered
// with null, not an empty Hover.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// SemanticTokensParams is the semanticTokens/full payload.
type SemanticTokensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// SemanticTokens carries the relative-encoded token stream, five integers
// per token.
type SemanticTokens struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

// SemanticTokensDeltaParams is the semanticTokens/full/delta payload.
type SemanticTokensDeltaParams struct {
	TextDocument     TextDocumentIdentifier `json:"textDocument"`
	PreviousResultID string                 `json:"previousResultId"`
}

// SemanticTokensEdit replaces DeleteCount integers at Start with Data.
type SemanticTokensEdit struct {
	Start       int      `json:"start"`
	DeleteCount int      `json:"deleteCount"`
	Data        []uint32 `json:"data,omitempty"`
}

// SemanticTokensDelta answers a delta request whose previous result the
// server still holds. Edits must encode as [], not null, when nothing
// changed.
type SemanticTokensDelta struct {
	ResultID string               `json:"resultId,omitempty"`
	Edits    []SemanticTokensEdit `json:"edits"`
}

// MarshalJSON guarantees "data":[] for an empty stream; a nil slice would
// encode as null, which clients treat as a malformed result.
func (t SemanticTokens) MarshalJSON() ([]byte, error) {
	return t.AppendJSON(nil), nil
}

// AppendJSON appends the JSON encoding to dst. A 20k-line document has
// about 94k integers; encoding/json walks them by reflection, and then
// re-scans the output of any MarshalJSON, so the reply path in
// internal/jsonrpc calls this directly and the numbers are formatted once.
func (t SemanticTokens) AppendJSON(dst []byte) []byte {
	// Deltas and lengths are mostly one or two digits: three bytes per
	// integer with the comma avoids regrowing a megabyte buffer.
	dst = slices.Grow(dst, 3*len(t.Data)+64)
	if t.ResultID != "" {
		// strconv quoting is Go syntax, not JSON, for unusual bytes.
		id, _ := json.Marshal(t.ResultID)
		dst = append(dst, `{"resultId":`...)
		dst = append(dst, id...)
		dst = append(dst, `,"data":[`...)
	} else {
		dst = append(dst, `{"data":[`...)
	}
	for i, v := range t.Data {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = strconv.AppendUint(dst, uint64(v), 10)
	}
	return append(dst, "]}"...)
}

// MessageType is the severity of a window/logMessage.
type MessageType int

// Message types defined by the LSP.
const (
	MessageError   MessageType = 1
	MessageWarning MessageType = 2
	MessageInfo    MessageType = 3
	MessageLog     MessageType = 4
)

// LogMessageParams is the window/logMessage payload.
type LogMessageParams struct {
	Type    MessageType `json:"type"`
	Message string      `json:"message"`
}

// DiagnosticSeverity ranks a Diagnostic.
type DiagnosticSeverity int

// Diagnostic severities defined by the LSP.
const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// Diagnostic is one problem report. Code is a string here because every code
// falcon-asm will emit is symbolic (an ISA set name, a rule id).
type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity,omitempty"`
	Code     string             `json:"code,omitempty"`
	// CodeDescription links the code to its documentation (docs/RULES.md).
	CodeDescription    *CodeDescription               `json:"codeDescription,omitempty"`
	Source             string                         `json:"source,omitempty"`
	Message            string                         `json:"message"`
	Tags               []DiagnosticTag                `json:"tags,omitempty"`
	RelatedInformation []DiagnosticRelatedInformation `json:"relatedInformation,omitempty"`
}

// CodeDescription is where a diagnostic code is explained.
type CodeDescription struct {
	Href string `json:"href"`
}

// DiagnosticRelatedInformation points at another place that matters to a
// diagnostic (the GDT entry a selector names, say).
type DiagnosticRelatedInformation struct {
	Location Location `json:"location"`
	Message  string   `json:"message"`
}

// DiagnosticTag changes how a client renders a diagnostic. Unnecessary is
// how inactive preprocessor regions are faded without a client extension.
type DiagnosticTag int

// Diagnostic tags defined by the LSP.
const (
	TagUnnecessary DiagnosticTag = 1
	TagDeprecated  DiagnosticTag = 2
)

// PublishDiagnosticsParams replaces all diagnostics for one document.
// Diagnostics must encode as [] (not null) to clear; callers pass a non-nil
// slice.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}
