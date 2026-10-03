package encoding

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// BenchmarkEncodingRules measures the section 1 rules over the large
// synthetic document (the program is parsed once, outside the loop; each
// iteration is a fresh lint run, so the shared pass runs every time).
func BenchmarkEncodingRules(b *testing.B) {
	text := benchdata.Large(b, 20000)
	p := parse(text, asm.Mode16)
	rs := rules(b)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Rules: rs, Target: lint.Target{Mode: asm.Mode16}})
	}
}
