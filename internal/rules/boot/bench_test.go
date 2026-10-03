package boot

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The package's rules over the largest airies-boot fixture, flow included
// (the run computes it once, as the engine's lint does).
func BenchmarkBootRules(b *testing.B) {
	m := headerMacros(b)
	text := fixture(b, "stage2.S")
	var rules []*lint.Rule
	for _, c := range allCodes {
		r, _ := lint.Lookup(c)
		rules = append(rules, r)
	}
	p := parse(b, "file:///stage2.S", text, 16, m)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Tables: hwdb.Default(), Rules: rules})
	}
}
