package flowrules

import (
	"fmt"
	"sort"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeBusySpin,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Infinite loop with no hlt or pause: a jmp to itself, or a loop whose exit test nothing in it can change.",
			Why: "`jmp .` stops a program but keeps the core running flat out, which on a laptop or a VM " +
				"host is heat and a stolen CPU; `cli; 1: hlt; jmp 1b` stops it for real (hlt alone, with " +
				"interrupts on, to idle). A loop whose exit condition depends only on registers it never " +
				"writes, and that reads no memory or I/O port, either exits at once or never: usually a " +
				"wrong register in the test or a missing decrement. Loops that read memory or ports, call " +
				"anything, or contain hlt or pause are polling and are not reported.",
			Bad:  "fatal:\n\tcli\n\tjmp .",
			Good: "fatal:\n\tcli\n1:\thlt\n\tjmp 1b",
		},
		Check: checkSpin,
	})
}

// maxLoopBlocks bounds the loops examined: a spin is a few instructions,
// and a big loop is doing something.
const maxLoopBlocks = 6

func checkSpin(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	seen := map[int]bool{}
	for b := range g.Blocks {
		bl := &g.Blocks[b]
		if !res.Reachable(bl.First) {
			continue
		}
		if selfJump(p, bl) {
			c.ReportStmt(bl.Last, "infinite loop with no hlt: the processor spins at full speed; stop with `cli; 1: hlt; jmp 1b`, or idle with hlt")
			continue
		}
		for _, latch := range bl.Pred {
			if latch < b || seen[b] {
				continue
			}
			body := loopBody(g, b, latch)
			if body == nil || !quiet(f, body) {
				continue
			}
			msg, at := spinVerdict(f, body)
			if msg == "" {
				continue
			}
			seen[b] = true
			c.ReportStmt(at, msg)
		}
	}
}

// selfJump reports a jump that is a loop all by itself: "jmp ." (which the
// CFG cannot resolve to a block), or "1: jmp 1b" with nothing else in its
// block.
func selfJump(p *asm.Program, bl *flow.Block) bool {
	st := &p.Stmts[bl.Last]
	if bl.Exit != flow.ExitJump || !st.IsUncondJump() || len(st.Ops) != 1 {
		return false
	}
	if op := &st.Ops[0]; op.Kind == asm.OpLabel && op.Symbol == "." || op.Text == "." {
		return true
	}
	if bl.Target != bl.ID {
		return false
	}
	for i := bl.First; i < bl.Last; i++ {
		if p.Stmts[i].Kind == asm.Instr || p.Stmts[i].Kind == asm.MacroCall {
			return false
		}
	}
	return true
}

// loopBody is the natural loop of the back edge latch -> head: the head
// plus every block that reaches the latch without passing the head, within
// the block range the two span. nil when it is too big to be a spin.
func loopBody(g *flow.CFG, head, latch int) []int {
	in := map[int]bool{head: true}
	work := []int{latch}
	for len(work) > 0 {
		b := work[len(work)-1]
		work = work[:len(work)-1]
		if in[b] {
			continue
		}
		if b < head || b > latch {
			return nil
		}
		in[b] = true
		if len(in) > maxLoopBlocks {
			return nil
		}
		work = append(work, g.Blocks[b].Pred...)
	}
	out := make([]int, 0, len(in))
	for b := range in {
		out = append(out, b)
	}
	sort.Ints(out)
	return out
}

// quiet reports a loop body with nothing in it that waits, polls or calls:
// no hlt or pause, no I/O, no memory read, no call, int or macro.
func quiet(f *facts, body []int) bool {
	ok := true
	for _, b := range body {
		stmts(f.g, b, func(i int) bool {
			st := &f.p.Stmts[i]
			switch st.Kind {
			case asm.MacroCall:
				ok = false
			case asm.Instr:
				if st.Inst == nil {
					ok = st.Mnemonic == ""
					break
				}
				switch st.Inst.Name {
				case "hlt", "pause", "in", "ins", "out", "outs", "int", "call", "lcall", "mwait", "umwait",
					"tpause", "rdtsc", "rdtscp", "rdrand", "rdseed", "cpuid", "xchg", "cmpxchg", "lock":
					ok = false
				}
				if isStringOp(st) {
					ok = false
				}
				for k := range st.Ops {
					if st.Ops[k].Kind == asm.OpMem && st.Inst.Name != "lea" {
						ok = false
					}
				}
			}
			return ok
		})
		if !ok {
			return false
		}
	}
	return true
}

// spinVerdict decides whether a quiet loop can end: an exit through ret,
// an indirect or far jump or a jump out of the document can always be
// taken; a conditional exit can be taken only if what it tests changes in
// the loop. It returns the message and the statement to report at, ""
// when the loop may end.
func spinVerdict(f *facts, body []int) (string, int) {
	p, g, res := f.p, f.g, f.res
	in := map[int]bool{}
	for _, b := range body {
		in[b] = true
	}
	written := map[flow.Reg]bool{}
	for _, b := range body {
		stmts(g, b, func(i int) bool {
			if p.Stmts[i].Kind != asm.Instr || !res.Reachable(i) {
				return true
			}
			before, after := &res.In[i], f.after(i)
			for r := flow.Reg(0); r < flow.NumValueRegs; r++ {
				if before.Get(r) != after.Get(r) {
					written[r] = true
				}
			}
			return true
		})
	}
	var exits []int
	for _, b := range body {
		bl := &g.Blocks[b]
		switch bl.Exit {
		case flow.ExitRet, flow.ExitIret, flow.ExitIndirect, flow.ExitFar, flow.ExitTrap:
			return "", -1
		case flow.ExitJump:
			if bl.Target < 0 {
				return "", -1
			}
		}
		for _, s := range bl.Succ {
			if !in[s] {
				exits = append(exits, bl.Last)
				break
			}
		}
		if len(bl.Succ) == 0 && bl.Exit != flow.ExitJump {
			return "", -1
		}
	}
	head := firstInstr(g, body[0])
	if len(exits) == 0 {
		return fmt.Sprintf("infinite loop with no hlt or pause (from line %d): it spins at full speed; stop with `cli; 1: hlt; jmp 1b`, or idle with hlt", lineOf(p, head)), g.Blocks[body[len(body)-1]].Last
	}
	for _, j := range exits {
		if !invariantExit(f, j, written, in) {
			return "", -1
		}
	}
	j := exits[0]
	return fmt.Sprintf("busy loop: its exit test (%s at line %d) depends only on registers the loop never changes, so once entered it never ends", p.Stmts[j].Mnemonic, lineOf(p, j)), j
}

// invariantExit reports whether the conditional exit at statement j tests
// something the loop cannot change: its own register inputs and the inputs
// of the instruction that set its flags are never written in the loop, and
// that instruction reads no memory.
func invariantExit(f *facts, j int, written map[flow.Reg]bool, in map[int]bool) bool {
	p, g, res := f.p, f.g, f.res
	st := &p.Stmts[j]
	if !st.IsCondJump() {
		return false
	}
	for _, u := range flow.Reads(p, j) {
		if written[u.Reg] {
			return false
		}
	}
	fl := flagsRead(st) & asm.FlagsStatus
	if fl == 0 {
		return true
	}
	def := -1
	for k, n := range flagIndex {
		if fl&n == 0 {
			continue
		}
		d := res.In[j].FlagDef[k]
		if d < 0 || def >= 0 && d != def {
			return false
		}
		def = d
	}
	if def < 0 {
		return false
	}
	if b := g.BlockOf[def]; b < 0 || !in[b] {
		// Set before the loop and never rewritten in it: invariant.
		return true
	}
	ds := &p.Stmts[def]
	for k := range ds.Ops {
		if ds.Ops[k].Kind == asm.OpMem {
			return false
		}
	}
	for _, u := range flow.Reads(p, def) {
		if written[u.Reg] {
			return false
		}
	}
	return true
}

// flagIndex maps flow.FlagCF..FlagOF to the asm flag bits.
var flagIndex = [6]asm.Flags{asm.FlagCF, asm.FlagPF, asm.FlagAF, asm.FlagZF, asm.FlagSF, asm.FlagOF}
