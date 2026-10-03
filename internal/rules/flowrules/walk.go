package flowrules

import "git.airies.net/vifair22/falcon-asm/internal/flow"

// verdict is what a forward search does after looking at one statement.
type verdict int

const (
	// keep follows the path on to the next statement.
	keep verdict = iota
	// done ends this path without a finding: the condition was met, or
	// the path leads somewhere the analysis cannot follow.
	done
	// found ends the whole search: the path shows the problem.
	found
)

// searchLimit bounds how many blocks one search may enter, so a search
// started at every int of a generated 20k-line document stays linear in
// practice; a search cut short reports nothing, which is the safe answer.
const searchLimit = 512

// searchFrom walks every path forward from the statement after start,
// calling visit on each statement once, and returns the statement visit
// answered found at. Paths end at a done verdict, at a block with no
// successor (ret, a jump out of the document, a halt with nothing after
// it) and at a fallthrough into data, which the analysis cannot follow.
func searchFrom(g *flow.CFG, start int, visit func(i int) verdict) (int, bool) {
	b0 := g.BlockOf[start]
	if b0 < 0 {
		return -1, false
	}
	seen := map[int]bool{}
	var work []int
	// walkBlock visits block b from statement from on; it reports whether
	// the path continues to the block's successors.
	walkBlock := func(b, from int) (int, verdict) {
		bl := &g.Blocks[b]
		for i := from; i <= bl.Last; i++ {
			if g.BlockOf[i] != b {
				continue
			}
			switch v := visit(i); v {
			case keep:
			default:
				return i, v
			}
		}
		return -1, keep
	}
	push := func(b int) {
		bl := &g.Blocks[b]
		for _, s := range bl.Succ {
			if bl.IntoData >= 0 && s == g.Fall(b) {
				continue
			}
			if !seen[s] {
				seen[s] = true
				work = append(work, s)
			}
		}
	}
	if at, v := walkBlock(b0, start+1); v == found {
		return at, true
	} else if v == keep {
		push(b0)
	}
	for n := 0; len(work) > 0; n++ {
		if n >= searchLimit {
			return -1, false
		}
		b := work[len(work)-1]
		work = work[:len(work)-1]
		at, v := walkBlock(b, g.Blocks[b].First)
		switch v {
		case found:
			return at, true
		case keep:
			push(b)
		}
	}
	return -1, false
}

// dominators computes the immediate dominator of every block of function
// fi reachable from its entry (Cooper, Harvey and Kennedy's iterative
// algorithm over reverse postorder). idom maps a block ID to its immediate
// dominator; the entry maps to itself and blocks the entry does not reach
// are absent.
func dominators(g *flow.CFG, fi int) map[int]int {
	f := &g.Funcs[fi]
	in := make(map[int]bool, len(f.Blocks))
	for _, b := range f.Blocks {
		in[b] = true
	}
	// Reverse postorder from the entry, over edges inside the function.
	order := make([]int, 0, len(f.Blocks))
	seen := map[int]bool{f.Entry: true}
	type frame struct{ b, k int }
	stack := []frame{{f.Entry, 0}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		succ := g.Blocks[top.b].Succ
		if top.k < len(succ) {
			t := succ[top.k]
			top.k++
			if in[t] && !seen[t] {
				seen[t] = true
				stack = append(stack, frame{t, 0})
			}
			continue
		}
		order = append(order, top.b)
		stack = stack[:len(stack)-1]
	}
	rpo := make(map[int]int, len(order))
	for k := range order {
		rpo[order[k]] = len(order) - 1 - k
	}
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	idom := map[int]int{f.Entry: f.Entry}
	intersect := func(a, b int) int {
		for a != b {
			for rpo[a] > rpo[b] {
				a = idom[a]
			}
			for rpo[b] > rpo[a] {
				b = idom[b]
			}
		}
		return a
	}
	for changed := true; changed; {
		changed = false
		for _, b := range order[1:] {
			nd := -1
			for _, p := range g.Blocks[b].Pred {
				if _, ok := idom[p]; !ok || !seen[p] {
					continue
				}
				if nd < 0 {
					nd = p
				} else {
					nd = intersect(p, nd)
				}
			}
			if old, ok := idom[b]; nd >= 0 && (!ok || old != nd) {
				idom[b] = nd
				changed = true
			}
		}
	}
	return idom
}

// dominates reports whether block a dominates block b under idom.
func dominates(idom map[int]int, a, b int) bool {
	for {
		if a == b {
			return true
		}
		p, ok := idom[b]
		if !ok || p == b {
			return false
		}
		b = p
	}
}
