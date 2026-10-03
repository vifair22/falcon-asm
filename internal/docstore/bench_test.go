package docstore

import (
	"fmt"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// BenchmarkChange is one typed character in the middle of a large
// document: the cost every keystroke pays before any feature runs.
func BenchmarkChange(b *testing.B) {
	for _, lines := range []int{20000, 100000} {
		text := benchdata.Large(b, lines)
		mid := strings.Count(text, "\n") / 2
		b.Run(fmt.Sprintf("%dk", lines/1000), func(b *testing.B) {
			s := New()
			s.Open("file:///b.S", 1, text)
			at := protocol.Position{Line: mid, Character: 0}
			ch := []protocol.TextDocumentContentChangeEvent{{Range: &protocol.Range{Start: at, End: at}, Text: "x"}}
			v := 1
			b.ReportAllocs()
			for b.Loop() {
				v++
				if err := s.Change("file:///b.S", v, ch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPosition converts offsets spread over a large document, which
// semantic tokens and every range in a reply do.
func BenchmarkPosition(b *testing.B) {
	s := New()
	s.Open("file:///b.S", 1, benchdata.Large(b, 20000))
	d, _ := s.Get("file:///b.S")
	n := len(d.Text)
	b.ReportAllocs()
	for b.Loop() {
		for off := 0; off < n; off += 4096 {
			d.Position(off)
		}
	}
}
