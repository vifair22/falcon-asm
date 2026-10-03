package protocol

import (
	"encoding/json"
	"testing"
)

// These pin wire shapes that clients are strict about; a stray omitempty or
// a nil slice here breaks editors in ways that are tedious to trace back.
func TestWireShapes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"empty tokens encode as array", SemanticTokens{}, `{"data":[]}`},
		{"empty tokens via pointer", &SemanticTokens{}, `{"data":[]}`},
		{"tokens", SemanticTokens{ResultID: "1", Data: []uint32{0, 1, 2, 3, 4}}, `{"resultId":"1","data":[0,1,2,3,4]}`},
		{"full replace has no range", TextDocumentContentChangeEvent{Text: "x"}, `{"text":"x"}`},
		{
			"ranged change",
			TextDocumentContentChangeEvent{Range: &Range{End: Position{Line: 1, Character: 2}}, Text: ""},
			`{"range":{"start":{"line":0,"character":0},"end":{"line":1,"character":2}},"text":""}`,
		},
		{
			"capabilities",
			ServerCapabilities{
				TextDocumentSync: &TextDocumentSyncOptions{OpenClose: true, Change: SyncIncremental},
				HoverProvider:    true,
				SemanticTokensProvider: &SemanticTokensOptions{
					Legend: SemanticTokensLegend{TokenTypes: []string{"keyword"}, TokenModifiers: []string{}},
					Full:   true,
				},
			},
			`{"textDocumentSync":{"openClose":true,"change":2},"hoverProvider":true,"semanticTokensProvider":{"legend":{"tokenTypes":["keyword"],"tokenModifiers":[]},"full":true,"range":false}}`,
		},
		{
			"hover",
			Hover{Contents: MarkupContent{Kind: Markdown, Value: "**mov**"}},
			`{"contents":{"kind":"markdown","value":"**mov**"}}`,
		},
		{
			"diagnostics clear",
			PublishDiagnosticsParams{URI: "file:///a.S", Diagnostics: []Diagnostic{}},
			`{"uri":"file:///a.S","diagnostics":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestDecodeHoverParams(t *testing.T) {
	in := `{"textDocument":{"uri":"file:///a.S"},"position":{"line":3,"character":7}}`
	var p HoverParams
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	if p.TextDocument.URI != "file:///a.S" || p.Position != (Position{Line: 3, Character: 7}) {
		t.Fatalf("decoded %+v", p)
	}
}
