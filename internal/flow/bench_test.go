package flow

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// The flow benchmarks run on the same inputs as asm's BenchmarkParseLarge
// and BenchmarkParseFixtures, so the ratio to parsing reads straight off:
// go test -bench 'Large|Fixtures' ./internal/asm ./internal/flow

func BenchmarkFlowLarge(b *testing.B) {
	p := parse(b, benchdata.Large(b, 20000), asm.Mode16)
	b.SetBytes(int64(len(p.Doc.Text)))
	b.ReportAllocs()
	for b.Loop() {
		Analyze(Build(p), Config{})
	}
}

func BenchmarkFlowFixtures(b *testing.B) {
	fx := benchdata.Fixtures(b)
	progs := make([]*asm.Program, len(fx))
	total := 0
	for i, src := range fx {
		progs[i] = parse(b, src, asm.Mode16)
		total += len(src)
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()
	for b.Loop() {
		for _, p := range progs {
			Analyze(Build(p), Config{})
		}
	}
}

func BenchmarkBuildLarge(b *testing.B) {
	p := parse(b, benchdata.Large(b, 20000), asm.Mode16)
	b.SetBytes(int64(len(p.Doc.Text)))
	b.ReportAllocs()
	for b.Loop() {
		Build(p)
	}
}

// BenchmarkParseLarge is the reference: asm.Parse on the same document.
func BenchmarkParseLarge(b *testing.B) {
	src := benchdata.Large(b, 20000)
	s := docstore.New()
	s.Open("file:///b.S", 1, src)
	doc, _ := s.Get("file:///b.S")
	toks := lexer.Tokenize(src)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		asm.Parse(doc, toks, asm.Config{InitialMode: asm.Mode16})
	}
}
