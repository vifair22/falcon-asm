package hover

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// BenchmarkHover hovers every mnemonic and register of pmode.S with the
// token stream supplied, as the engine calls it.
func BenchmarkHover(b *testing.B) {
	s := docstore.New()
	s.Open("file:///pmode.S", 1, benchdata.Fixture(b, "pmode.S"))
	doc, _ := s.Get("file:///pmode.S")
	toks := lexer.Tokenize(doc.Text)
	var at []protocol.Position
	for _, t := range toks {
		if t.Kind == lexer.Mnemonic || t.Kind == lexer.Register {
			at = append(at, protocol.Position{Line: t.Line, Character: t.Col + 1})
		}
	}
	opts := Options{Tokens: toks}
	b.ReportAllocs()
	for b.Loop() {
		for _, p := range at {
			if _, err := HoverWith(doc, p, opts); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(at)), "ns/hover")
}
