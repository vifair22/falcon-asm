package symbols

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func benchDoc(b *testing.B, lines int) *docstore.Document {
	s := docstore.New()
	s.Open("file:///bench/b.S", 1, benchdata.Large(b, lines))
	d, _ := s.Get("file:///bench/b.S")
	return d
}

// BenchmarkIndexUpdate is what the index does with every didChange of a
// 20k-line document.
func BenchmarkIndexUpdate(b *testing.B) {
	doc := benchDoc(b, 20000)
	ix := NewIndex(nil)
	b.SetBytes(int64(len(doc.Text)))
	b.ReportAllocs()
	for b.Loop() {
		ix.Update(doc.URI, doc.Text, nil)
	}
}

// BenchmarkReferences finds a label's references in an indexed 20k-line
// document (no workspace root, so no scan).
func BenchmarkReferences(b *testing.B) {
	doc := benchDoc(b, 20000)
	ix := NewIndex(nil)
	ix.Update(doc.URI, doc.Text, nil)
	// Line 0 of pmode-free boot.S is a comment; find the first label def.
	f := parse(doc.URI, doc.Text, false)
	var at protocol.Position
	for _, d := range f.defs {
		if d.kind == kindLabel {
			at = protocol.Position{Line: d.at.line, Character: d.at.col}
			break
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		ix.References(doc.URI, at, true)
	}
}

func BenchmarkDocumentSymbols(b *testing.B) {
	doc := benchDoc(b, 20000)
	b.ReportAllocs()
	for b.Loop() {
		DocumentSymbols(doc)
	}
}

func BenchmarkFoldingRanges(b *testing.B) {
	doc := benchDoc(b, 20000)
	b.ReportAllocs()
	for b.Loop() {
		FoldingRanges(doc)
	}
}
