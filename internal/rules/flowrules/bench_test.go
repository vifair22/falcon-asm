package flowrules

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The rules are measured against the flow analysis they sit on, on the
// same 20k-line document as internal/flow's BenchmarkFlowLarge: the target
// is the rules adding less than the flow costs, so linting stays under
// twice the analysis.
//
//	go test -bench Large ./internal/rules/flowrules

func BenchmarkFlowOnlyLarge(b *testing.B) {
	p := prog(benchdata.Large(b, 20000), asm.Mode16)
	b.ReportAllocs()
	for b.Loop() {
		flow.Analyze(flow.Build(p), flow.Config{})
	}
}

func BenchmarkFlowRulesLarge(b *testing.B) {
	p := prog(benchdata.Large(b, 20000), asm.Mode16)
	rules := ourRules()
	b.ReportAllocs()
	for b.Loop() {
		lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: lint.Target{Mode: 16}, Rules: rules})
	}
}

// noopRule is a flow rule that does nothing: lint.Run with it alone is the
// floor the rules are measured from (the flow analysis plus the runner).
var noopRule = &lint.Rule{Code: "zz-bench-noop", Section: 3, Default: lint.Info, Langs: lint.LangAsm,
	NeedsFlow: true, Doc: lint.Doc{Summary: "benchmark floor"}, Check: func(*lint.Context) {}}

func BenchmarkFlowRunnerFloorLarge(b *testing.B) {
	p := prog(benchdata.Large(b, 20000), asm.Mode16)
	rules := []*lint.Rule{noopRule}
	b.ReportAllocs()
	for b.Loop() {
		lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: lint.Target{Mode: 16}, Rules: rules})
	}
}
