package flow

import (
	"fmt"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

func parse(tb testing.TB, src string, mode asm.Mode) *asm.Program {
	tb.Helper()
	s := docstore.New()
	s.Open("file:///t.S", 1, src)
	d, _ := s.Get("file:///t.S")
	return asm.Parse(d, lexer.Tokenize(src), asm.Config{InitialMode: mode})
}

// run parses, builds and analyzes.
func run(tb testing.TB, src string, mode asm.Mode, cfg Config) (*CFG, *Result) {
	tb.Helper()
	g := Build(parse(tb, src, mode))
	checkCFG(tb, g)
	return g, Analyze(g, cfg)
}

func fixture(tb testing.TB, name string) (*CFG, *Result) {
	tb.Helper()
	return run(tb, benchdata.Fixture(tb, name), asm.Mode16, Config{})
}

// at finds the n-th (0-based) statement whose source text contains sub.
func at(tb testing.TB, p *asm.Program, sub string, n int) int {
	tb.Helper()
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if strings.Contains(p.Doc.Text[st.Start:st.End], sub) {
			if n == 0 {
				return i
			}
			n--
		}
	}
	tb.Fatalf("no statement %q", sub)
	return -1
}

// atExact finds the n-th statement whose text is exactly text.
func atExact(tb testing.TB, p *asm.Program, text string, n int) int {
	tb.Helper()
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if p.Doc.Text[st.Start:st.End] == text {
			if n == 0 {
				return i
			}
			n--
		}
	}
	tb.Fatalf("no statement %q", text)
	return -1
}

// blockAtText is the block of the statement containing sub.
func blockAtText(tb testing.TB, g *CFG, sub string) int {
	tb.Helper()
	b := g.BlockOf[at(tb, g.Prog, sub, 0)]
	if b < 0 {
		tb.Fatalf("%q is not in a block", sub)
	}
	return b
}

// checkCFG asserts the graph's invariants.
func checkCFG(tb testing.TB, g *CFG) {
	tb.Helper()
	for k := range g.Blocks {
		b := &g.Blocks[k]
		if b.ID != k || b.First > b.Last {
			tb.Fatalf("block %d: bad ID/range %+v", k, *b)
		}
		if g.BlockOf[b.First] != k || g.BlockOf[b.Last] != k {
			tb.Fatalf("block %d: First/Last not mapped to it", k)
		}
		if b.Entry != (b.Why != 0) {
			tb.Fatalf("block %d: Entry %v Why %v", k, b.Entry, b.Why)
		}
		for _, s := range b.Succ {
			if !contains(g.Blocks[s].Pred, k) {
				tb.Fatalf("edge %d->%d missing pred", k, s)
			}
		}
		for _, p := range b.Pred {
			if !contains(g.Blocks[p].Succ, k) {
				tb.Fatalf("edge %d->%d missing succ", p, k)
			}
		}
	}
	for i, b := range g.BlockOf {
		if b >= 0 && (i < g.Blocks[b].First || i > g.Blocks[b].Last) {
			tb.Fatalf("stmt %d mapped to block %d outside its range", i, b)
		}
		if b >= 0 && g.Prog.Stmts[i].InMacroDef {
			tb.Fatalf("stmt %d inside a .macro body is in block %d", i, b)
		}
	}
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// dump renders the graph for failure messages.
func dump(g *CFG) string {
	var b strings.Builder
	for k := range g.Blocks {
		bl := &g.Blocks[k]
		first := &g.Prog.Stmts[bl.First]
		last := &g.Prog.Stmts[bl.Last]
		fmt.Fprintf(&b, "B%d [%d..%d] %q..%q exit=%d succ=%v pred=%v why=%b data=%d callee=%d\n",
			k, bl.First, bl.Last, g.Prog.Doc.Text[first.Start:first.End], g.Prog.Doc.Text[last.Start:last.End],
			bl.Exit, bl.Succ, bl.Pred, bl.Why, bl.IntoData, bl.Callee)
	}
	return b.String()
}
