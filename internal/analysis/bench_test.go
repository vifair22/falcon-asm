package analysis

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// BenchmarkAnalyzeTokens is mode tracking, mode and level diagnostics and
// symbol collection over a 20k-line document, tokens supplied.
func BenchmarkAnalyzeTokens(b *testing.B) {
	s := docstore.New()
	s.Open("file:///b.S", 1, benchdata.Large(b, 20000))
	doc, _ := s.Get("file:///b.S")
	toks := lexer.Tokenize(doc.Text)
	cfg := Config{InitialMode: Mode16, Level: 3}
	b.SetBytes(int64(len(doc.Text)))
	b.ReportAllocs()
	for b.Loop() {
		AnalyzeTokens(doc, toks, cfg)
	}
}
