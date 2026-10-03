// Package perf holds the performance lints of roadmap section 8: the
// AVX-SSE transition (missing vzeroupper), false output dependencies and
// partial flag merges, and slow or microcoded instruction forms.
//
// Performance advice must never drown correctness findings, so every rule
// here defaults to Info or Hint except the two that are about behaviour as
// much as speed: missing-vzeroupper (the transition penalty is large and
// the psABI expects clean upper state at function boundaries) and
// inc-no-carry (a CF read right after inc/dec reads a stale flag, which is
// usually a bug). Rules that depend on a microarchitecture consult the
// target CPU profile and stay quiet for processors the issue does not
// affect.
//
// Item 61 (latency/throughput hover) is declined: see docs/DESIGN.md,
// "Performance lints".
package perf

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/rules/cpu"
)

// section is the roadmap section every rule here belongs to.
const section = 8

// profileName resolves the target CPU to a profile name. known is false
// when no CPU is set or the name matches no profile ("native"): callers
// treat both as "could be any processor".
func profileName(c *lint.Context) (name string, known bool) {
	p, ok := cpu.Profile(c.Target.CPU)
	if !ok {
		return "", false
	}
	return p.Name, true
}

// generic reports a profile that names an ABI level rather than a part:
// code built for x86-64-v3 runs on every vendor's cores, so it gets the
// advice that applies to any of them.
func generic(name string) bool { return strings.HasPrefix(name, "x86-64") }

// amd reports the AMD profiles.
func amd(name string) bool {
	switch name {
	case "k6", "k6-2", "athlon", "athlon-xp", "k8", "k8-sse3":
		return true
	}
	return strings.HasPrefix(name, "znver")
}

// instr returns statement i when it is a resolved instruction that is
// really code at its place (not a line of a .macro body).
func instr(p *asm.Program, i int) *asm.Stmt {
	s := &p.Stmts[i]
	if s.Kind != asm.Instr || s.Inst == nil || s.InMacroDef {
		return nil
	}
	return s
}

// reportMnem reports on statement i's mnemonic, which keeps the squiggle
// short on long memory operands.
func reportMnem(c *lint.Context, i int, msg string, fixes ...lint.Fix) {
	s := &c.Prog.Stmts[i]
	c.ReportRange(s.MnemStart, s.MnemStart+len(s.Mnemonic), msg, fixes...)
}

// insertFix builds a fix that puts instruction text immediately before
// statement i. A statement with a label gets the text after the label (a
// jump to the label must run it too), and one sharing its line with an
// earlier statement gets it inline, so the order of execution is what the
// fix says. A prefixed statement gets no fix: inserting between a prefix
// and its mnemonic would change what the prefix applies to.
func insertFix(c *lint.Context, i int, title, text string) []lint.Fix {
	s := &c.Prog.Stmts[i]
	if len(s.Prefixes) > 0 {
		return nil
	}
	inline := len(s.Labels) > 0
	for j := i - 1; j >= 0 && !inline; j-- {
		prev := &c.Prog.Stmts[j]
		if prev.EndLine < s.Line {
			break
		}
		if prev.Kind != asm.Empty {
			inline = true
		}
	}
	if inline {
		return []lint.Fix{lint.NewFix(title, c.ReplaceRange(s.MnemStart, s.MnemStart, text+"; "))}
	}
	return []lint.Fix{lint.NewFix(title, c.InsertLineAbove(i, text))}
}

// regFamily maps a register operand to its flow family and bit range.
func regFamily(op *asm.Operand) (r flow.Reg, lo, hi int, ok bool) {
	if op.Kind != asm.OpReg || op.Reg == nil {
		return flow.NoReg, 0, 0, false
	}
	return flow.RegOf(op.Reg.Name)
}

// mentions reports whether family r appears in op: as the register, or as
// a memory operand's base or index.
func mentions(op *asm.Operand, r flow.Reg) bool {
	switch op.Kind {
	case asm.OpReg:
		f, _, _, ok := regFamily(op)
		return ok && f == r
	case asm.OpMem:
		for _, n := range []string{op.Base, op.Index} {
			if n == "" {
				continue
			}
			if f, _, _, ok := flow.RegOf(n); ok && f == r {
				return true
			}
		}
	}
	return false
}

// flowOf returns the dataflow result, nil when the analysis is missing.
func flowOf(c *lint.Context) (*flow.CFG, *flow.Result) {
	g, res := c.Flow()
	if g == nil || res == nil || c.Prog == nil {
		return nil, nil
	}
	return g, res
}
