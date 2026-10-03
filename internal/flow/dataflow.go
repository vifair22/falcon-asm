package flow

import (
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// The solver is a forward worklist over blocks in reverse postorder. Only
// block entry states are kept while iterating; the per-statement In array
// is written once, by a final walk over each reached block, so a loop
// that needs several passes does not copy every statement's state each
// time.
//
// Termination: every field of State moves down a finite lattice under
// join (known bits only disappear, Defs only become DefMerged, Tri only
// becomes Unknown, stack facts only become unknown, slots only drop), and
// after widenAfter changes to a block's entry state every further change
// collapses the changed values straight to unknown.
//
// Calls: a callee defined in the document is summarized (the registers,
// flags and vector state anything it runs may write, how many argument
// bytes its ret pops). Registers a callee provably restores (Def still
// DefEntry at every ret, which push/pop through the abstract stack
// preserves) are then dropped from its summary: a first solve over just
// the called functions' code finds them, before the solve over the whole
// program uses the sharper summaries.

const widenAfter = 8

// regSet is a set of register families.
type regSet [2]uint64

func (s *regSet) add(r Reg)      { s[r/64] |= 1 << uint(r%64) }
func (s regSet) has(r Reg) bool  { return s[r/64]&(1<<uint(r%64)) != 0 }
func (s *regSet) del(r Reg)      { s[r/64] &^= 1 << uint(r%64) }
func (s *regSet) union(o regSet) { s[0] |= o[0]; s[1] |= o[1] }

// summary is what a call to a function does, as far as the caller sees.
type summary struct {
	writes   regSet
	df, ifl  bool
	unknown  bool  // something in it is opaque: treat like an unknown callee
	pop      int   // bytes "ret $n" pops; -1 when rets disagree
	callees  []int // function indexes it calls
	computed bool
}

type analyzer struct {
	g        *CFG
	p        *asm.Program
	cfg      Config
	callClob regSet
	sums     []summary
	funcAt   []int // block ID -> index of the function it starts, -1
	in       []State
	order    []int
	infos    map[*isa.Instruction]*insnInfo
}

func analyze(g *CFG, cfg Config) *Result {
	a := &analyzer{g: g, p: g.Prog, cfg: cfg}
	a.prepare()
	a.in = make([]State, len(g.Blocks))
	if callees := a.calledEntries(); len(callees) > 0 {
		a.order = a.rpo(callees)
		a.solve(callees, nil, nil)
		a.refine()
		clear(a.in)
	}
	starts := make([]int, len(g.Funcs))
	for k, f := range g.Funcs {
		starts[k] = f.Entry
	}
	a.order = a.rpo(starts)
	r := &Result{CFG: g, In: make([]State, len(a.p.Stmts)), Out: make([]State, len(g.Blocks)), an: a}
	a.solve(starts, r.In, r.Out)
	return r
}

// calledEntries are the entry blocks of the functions some call enters.
func (a *analyzer) calledEntries() []int {
	var out []int
	for fi := range a.sums {
		if a.sums[fi].computed {
			out = append(out, a.g.Funcs[fi].Entry)
		}
	}
	return out
}

func (a *analyzer) prepare() {
	if a.cfg.CallClobbers != nil {
		for _, r := range a.cfg.CallClobbers {
			if r >= 0 && r < NumRegs {
				a.callClob.add(r)
			}
		}
	} else {
		for r := Reg(0); r < NumGPR; r++ {
			a.callClob.add(r)
		}
		for _, r := range []Reg{ES, DS, FS, GS} {
			a.callClob.add(r)
		}
		for r := K0; r < NumRegs; r++ {
			a.callClob.add(r)
		}
	}
	a.callClob.del(RSP)
	a.funcAt = make([]int, len(a.g.Blocks))
	for k := range a.funcAt {
		a.funcAt[k] = -1
	}
	for fi, f := range a.g.Funcs {
		a.funcAt[f.Entry] = fi
	}
	a.sums = make([]summary, len(a.g.Funcs))
	a.summaries()
}

// rpo orders the blocks reachable from the start blocks in reverse
// postorder.
func (a *analyzer) rpo(starts []int) []int {
	n := len(a.g.Blocks)
	seen := make([]bool, n)
	post := make([]int, 0, n)
	type frame struct{ b, k int }
	var stack []frame
	for _, e := range starts {
		if seen[e] {
			continue
		}
		seen[e] = true
		stack = append(stack[:0], frame{e, 0})
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			succ := a.g.Blocks[top.b].Succ
			if top.k < len(succ) {
				t := succ[top.k]
				top.k++
				if !seen[t] {
					seen[t] = true
					stack = append(stack, frame{t, 0})
				}
				continue
			}
			post = append(post, top.b)
			stack = stack[:len(stack)-1]
		}
	}
	for i, j := 0, len(post)-1; i < j; i, j = i+1, j-1 {
		post[i], post[j] = post[j], post[i]
	}
	return post
}

// entryState is the state assumed where control enters from outside.
func (a *analyzer) entryState(b int) State {
	s := State{
		Reachable:  true,
		StackKnown: true,
		DF:         a.cfg.EntryDF,
		IF:         a.cfg.EntryIF,
		Mode:       a.p.Stmts[a.g.Blocks[b].First].Mode,
	}
	for r := range s.defs {
		s.defs[r] = DefEntry
	}
	for k := range s.FlagDef {
		s.FlagDef[k] = DefEntry
	}
	return s
}

// solve runs the worklist to a fixed point from the given start blocks
// over a.order, leaving each block's entry state in a.in. With in and out
// non-nil it also stores every statement's entry state and every block's
// exit state as it goes: a block is walked again whenever its entry state
// changes, so the last walk, the one whose stores survive, is the one
// from the fixed point. That saves a separate pass over the program.
func (a *analyzer) solve(starts []int, in, out []State) {
	n := len(a.g.Blocks)
	visits := make([]uint8, n)
	dirty := make([]bool, n)
	for _, e := range starts {
		es := a.entryState(e)
		a.in[e].join(&es, false)
		dirty[e] = true
	}
	var cur, tmp State
	for changed := true; changed; {
		changed = false
		for _, b := range a.order {
			if !dirty[b] {
				continue
			}
			dirty[b] = false
			changed = true
			cur = a.in[b]
			a.walk(&cur, b, in)
			if out != nil {
				out[b] = cur
			}
			bl := &a.g.Blocks[b]
			for _, t := range bl.Succ {
				src := &cur
				if t == bl.fall && bl.IntoData >= 0 {
					// Control runs through data (hand-encoded bytes,
					// or a string after a call that never returns):
					// whatever it does is unknown.
					tmp = cur
					tmp.havoc()
					src = &tmp
				}
				if a.in[t].join(src, visits[t] >= widenAfter) {
					if visits[t] < 255 {
						visits[t]++
					}
					dirty[t] = true
				}
			}
		}
	}
}

// walk runs block b's statements over cur, storing each statement's entry
// state into in when it is non-nil.
func (a *analyzer) walk(cur *State, b int, in []State) {
	bl := &a.g.Blocks[b]
	for i := bl.First; i <= bl.Last; i++ {
		if a.g.BlockOf[i] != b {
			continue
		}
		cur.Mode = a.p.Stmts[i].Mode
		if in != nil {
			in[i] = *cur
		}
		a.step(cur, i)
	}
}

// summaryAt returns the summary of the function a call statement enters,
// nil when the callee is not in the document.
func (a *analyzer) summaryAt(i int) *summary {
	b := a.g.BlockOf[i]
	if b < 0 {
		return nil
	}
	c := a.g.Blocks[b].Callee
	if c < 0 || a.funcAt[c] < 0 {
		return nil
	}
	return &a.sums[a.funcAt[c]]
}

// summaries computes the summary of every function some call enters,
// and of the functions those call.
func (a *analyzer) summaries() {
	var work []int
	for k := range a.g.Blocks {
		if c := a.g.Blocks[k].Callee; c >= 0 && a.funcAt[c] >= 0 {
			work = append(work, a.funcAt[c])
		}
	}
	var called []int
	for len(work) > 0 {
		f := work[len(work)-1]
		work = work[:len(work)-1]
		if a.sums[f].computed {
			continue
		}
		a.local(f)
		called = append(called, f)
		work = append(work, a.sums[f].callees...)
	}
	// Close over the call graph: a function writes what its callees
	// write. Monotone, so it settles.
	for changed := true; changed; {
		changed = false
		for _, f := range called {
			s := &a.sums[f]
			for _, c := range s.callees {
				cs := &a.sums[c]
				before := *s
				s.writes.union(cs.writes)
				s.df = s.df || cs.df
				s.ifl = s.ifl || cs.ifl
				s.unknown = s.unknown || cs.unknown
				if s.writes != before.writes || s.df != before.df || s.ifl != before.ifl || s.unknown != before.unknown {
					changed = true
				}
			}
		}
	}
}

const probeDef = -7

// local computes what one function's own statements write, by running
// each through the transfer function on a probe state and seeing what
// changed.
func (a *analyzer) local(f int) {
	s := &a.sums[f]
	s.computed = true
	s.pop = -2
	var probe State
	for r := range probe.defs {
		probe.defs[r] = probeDef
	}
	probe.DF, probe.IF = Tri(-1), Tri(-1)
	probe.StackKnown = true
	probe.Reachable = true
	for _, b := range a.g.Funcs[f].Blocks {
		bl := &a.g.Blocks[b]
		switch bl.Exit {
		case ExitIndirect:
			s.unknown = true
		case ExitJump:
			if bl.Target < 0 {
				// A jump out of the document: a tail call into code
				// the analysis cannot see.
				s.unknown = true
			}
		case ExitIret, ExitFar:
			s.unknown = true
		case ExitCall:
			if bl.Callee >= 0 && a.funcAt[bl.Callee] >= 0 {
				s.callees = append(s.callees, a.funcAt[bl.Callee])
			} else {
				s.writes.union(a.callClob)
				s.df, s.ifl = true, true
			}
		case ExitRet:
			st := &a.p.Stmts[bl.Last]
			pop := 0
			if len(st.Ops) == 1 {
				if !st.Ops[0].ImmVal.Known {
					pop = -1
				} else {
					pop = int(st.Ops[0].ImmVal.V)
				}
			}
			switch {
			case s.pop == -2:
				s.pop = pop
			case s.pop != pop:
				s.pop = -1
			}
		}
		for i := bl.First; i <= bl.Last; i++ {
			if a.g.BlockOf[i] != b {
				continue
			}
			st := &a.p.Stmts[i]
			if st.Kind == asm.MacroCall || (st.Kind == asm.Instr && st.Inst == nil && st.Mnemonic != "") {
				s.unknown = true
				continue
			}
			if st.Kind != asm.Instr || st.IsCall() || st.IsRet() || st.IsIret() {
				continue
			}
			if a.info(st).opaque {
				s.unknown = true
				continue
			}
			t := probe
			t.Mode = st.Mode
			a.step(&t, i)
			for r := Reg(0); r < NumValueRegs; r++ {
				if r != RSP && t.get(r) != probe.get(r) {
					s.writes.add(r)
				}
			}
			if t.DF != probe.DF {
				s.df = true
			}
			if t.IF != probe.IF {
				s.ifl = true
			}
			for k := 0; k < 32; k++ {
				if t.vecKnown&(1<<uint(k)) != 0 {
					s.writes.add(Vec(k))
				}
			}
		}
	}
	if s.pop == -2 {
		s.pop = 0
	}
}

// refine drops from each summary the registers its function restores on
// every path to a ret: those whose Def is still DefEntry when the ret
// executes.
func (a *analyzer) refine() {
	var cur State
	for fi := range a.sums {
		s := &a.sums[fi]
		if !s.computed || s.unknown {
			continue
		}
		var keep regSet
		seen := false
		for _, b := range a.g.Funcs[fi].Blocks {
			bl := &a.g.Blocks[b]
			if bl.Exit != ExitRet || !a.in[b].Reachable {
				continue
			}
			cur = a.in[b]
			a.walk(&cur, b, nil)
			var here regSet
			for r := Reg(0); r < NumValueRegs; r++ {
				if cur.get(r).Def == DefEntry {
					here.add(r)
				}
			}
			if !seen {
				keep, seen = here, true
			} else {
				keep[0] &= here[0]
				keep[1] &= here[1]
			}
		}
		for r := Reg(0); seen && r < NumValueRegs; r++ {
			if keep.has(r) {
				s.writes.del(r)
			}
		}
	}
}

// Before returns the state before statement stmt (nil out of range).
func (r *Result) Before(stmt int) *State {
	if stmt < 0 || stmt >= len(r.In) {
		return nil
	}
	return &r.In[stmt]
}

// After returns the state after statement stmt: its transfer applied to
// Before. A new State each call; nil out of range.
func (r *Result) After(stmt int) *State {
	if stmt < 0 || stmt >= len(r.In) {
		return nil
	}
	s := r.In[stmt]
	if s.Reachable && r.an != nil {
		r.an.step(&s, stmt)
	}
	return &s
}

// ValueAt returns what is known of a register before statement stmt:
// reg is any name RegOf accepts ("ah", "eax", "dx", "cr0"); v holds the
// register's bits shifted down to bit 0 and mask marks the known ones.
// ok is false when the statement is unreachable or not code, the name is
// not a value-tracked register, or no bit is known.
func (r *Result) ValueAt(stmt int, reg string) (v uint64, mask uint64, ok bool) {
	s := r.Before(stmt)
	if s == nil || !s.Reachable {
		return 0, 0, false
	}
	p, found := lookupReg(reg)
	if !found || !p.r.HasValue() {
		return 0, 0, false
	}
	v, mask = s.Part(p.r, int(p.lo), int(p.hi))
	return v, mask, mask != 0
}

// KnownAt is ValueAt for a register whose every bit is known.
func (r *Result) KnownAt(stmt int, reg string) (uint64, bool) {
	v, m, ok := r.ValueAt(stmt, reg)
	p, _ := lookupReg(reg)
	return v, ok && m == widthMask(p.width())
}

// CPUIDLeaf returns the leaf (EAX) and subleaf (ECX) a cpuid statement
// asks for, each with its own known flag.
func (r *Result) CPUIDLeaf(stmt int) (leaf uint64, leafOK bool, sub uint64, subOK bool) {
	leaf, leafOK = r.KnownAt(stmt, "eax")
	sub, subOK = r.KnownAt(stmt, "ecx")
	return
}

// Reachable reports whether the analysis reached statement stmt.
func (r *Result) Reachable(stmt int) bool {
	s := r.Before(stmt)
	return s != nil && s.Reachable
}
