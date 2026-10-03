package perf

import "git.airies.net/vifair22/falcon-asm/internal/flow"

// hotness answers "does this block run repeatedly?" from the CFG alone,
// for advice that only pays in hot code. A block is hot when it lies on a
// cycle of the graph other than the one its own backward branch closes
// (an enclosing loop), or when the function owning it is called from a
// block that is hot or on a cycle, or takes part in recursion. Code
// entered from outside the document with no caller here (a global
// routine, setup run once) is not hot: nothing in the document says it
// repeats, and size-tuned setup code is exactly where slow-but-short
// forms are chosen on purpose.
type hotness struct {
	g       *flow.CFG
	callers map[int][]int // function entry block -> call-site blocks
	cycle   map[int]bool  // block -> lies on some cycle (memo)
	fn      map[int]int   // function index -> 0 unknown, 1 visiting, 2 cold, 3 hot
}

func newHotness(g *flow.CFG) *hotness {
	h := &hotness{g: g, callers: map[int][]int{}, cycle: map[int]bool{}, fn: map[int]int{}}
	for k := range g.Blocks {
		if callee := g.Blocks[k].Callee; callee >= 0 {
			h.callers[callee] = append(h.callers[callee], k)
		}
	}
	return h
}

// reaches reports whether target is reachable from any of starts over
// successor edges.
func (h *hotness) reaches(starts []int, target int) bool {
	seen := make([]bool, len(h.g.Blocks))
	stack := append([]int(nil), starts...)
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if k == target {
			return true
		}
		if k < 0 || k >= len(seen) || seen[k] {
			continue
		}
		seen[k] = true
		stack = append(stack, h.g.Blocks[k].Succ...)
	}
	return false
}

// onCycle reports whether block b can reach itself.
func (h *hotness) onCycle(b int) bool {
	v, ok := h.cycle[b]
	if !ok {
		v = h.reaches(h.g.Blocks[b].Succ, b)
		h.cycle[b] = v
	}
	return v
}

// enclosed reports whether block b, which ends in a backward branch to
// its Target, also lies on a cycle that leaves b by another edge: an outer
// loop around the inner one.
func (h *hotness) enclosed(b int) bool {
	bl := &h.g.Blocks[b]
	var other []int
	for _, s := range bl.Succ {
		if s != bl.Target {
			other = append(other, s)
		}
	}
	return h.reaches(other, b)
}

// hotFunc reports whether function fi runs repeatedly: called from a
// block on a cycle, from a hot function, or recursively.
func (h *hotness) hotFunc(fi int) bool {
	if fi < 0 || fi >= len(h.g.Funcs) {
		return false
	}
	switch h.fn[fi] {
	case 1:
		// A call cycle back to a function still being decided is
		// recursion, which repeats as surely as a loop.
		return true
	case 2:
		return false
	case 3:
		return true
	}
	h.fn[fi] = 1
	hot := false
	for _, s := range h.callers[h.g.Funcs[fi].Entry] {
		if h.onCycle(s) || h.hotFunc(h.g.FuncOf[s]) {
			hot = true
			break
		}
	}
	h.fn[fi] = 2
	if hot {
		h.fn[fi] = 3
	}
	return hot
}
