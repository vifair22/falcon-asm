package flowrules

import (
	"fmt"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeStackImbalance,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "ret/iret with pushed bytes still on the stack, or paths that join with different stack depths.",
			Why: "ret pops the return address from wherever the stack pointer is. A push without its pop " +
				"on some path (an early exit taken between the two, typically) makes ret jump to the pushed " +
				"value. The rule tracks the stack depth relative to the function's entry and reports a " +
				"ret, lret or iret reached with a known non-zero depth, and a block that two forward paths " +
				"reach with different known depths, which is where the imbalance starts. A depth that is " +
				"unknown (an untracked stack pointer write) is never reported, a loop that pushes once per " +
				"iteration is not a join of two paths, and `push $target; ret` (a jump through ret, the " +
				"top of the stack an immediate or a memory value) is taken as deliberate.",
			Bad:  "f:\n\tpushw %bx\n\ttestw %ax, %ax\n\tjz 1f\n\tpopw %bx\n1:\tret",
			Good: "f:\n\tpushw %bx\n\ttestw %ax, %ax\n\tjz 1f\n\tnop\n1:\tpopw %bx\n\tret",
		},
		Check: checkStack,
	})
	lint.Register(&lint.Rule{
		Code:      CodePushfPopf,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Function that saves the flags with pushf but returns on some path without popf.",
			Why: "A function that begins with pushf and restores the flags with popf before returning " +
				"promises its caller the interrupt and direction flags come back as they went in. A path " +
				"to ret that skips the popf (an error exit added later) breaks that promise silently: " +
				"interrupts stay off after the call, or a std leaks out. Reported at the ret, only for " +
				"functions that do restore with popf somewhere, so the save/restore intent is clear.",
			Bad:  "f:\n\tpushfw\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tpopfw\n\tret\n1:\taddw $2, %sp\n\tret",
			Good: "f:\n\tpushfw\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tpopfw\n\tret\n1:\tpopfw\n\tret",
		},
		Check: checkPushfPopf,
	})
	lint.Register(&lint.Rule{
		Code:      CodeCliSti,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Function that disables interrupts with cli and returns on some path without sti or popf.",
			Why: "A cli that is undone with sti (or by popf restoring saved flags) on the normal path but " +
				"not on an early return leaves interrupts disabled in the caller, which shows up much later " +
				"as a lost timer tick or a keyboard that stops responding. Only functions that re-enable " +
				"interrupts on some path after the cli are checked; mode-switch code (CR0 writes, lgdt, " +
				"far jumps), boot entries and interrupt handlers keep interrupts off on purpose.",
			Bad:  "f:\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tsti\n1:\tret",
			Good: "f:\n\tcli\n\ttestw %ax, %ax\n\tjz 1f\n\tnop\n1:\tsti\n\tret",
		},
		Check: checkCliSti,
	})
}

func checkStack(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	for b := range g.Blocks {
		bl := &g.Blocks[b]
		switch bl.Exit {
		case flow.ExitRet, flow.ExitIret:
			if msg := imbalance(f, bl.Last); msg != "" {
				c.ReportStmt(bl.Last, msg)
			}
		}
		if bl.Entry || len(bl.Pred) < 2 {
			continue
		}
		at := firstInstr(g, b)
		if at < 0 || !res.Reachable(at) {
			continue
		}
		type arm struct{ depth, from int }
		var arms []arm
		ok := true
		for _, pr := range bl.Pred {
			pb := &g.Blocks[pr]
			out := &res.Out[pr]
			// A back edge (a loop pushing per iteration), a fallthrough
			// out of data, a predecessor in another function and an
			// unknown depth all make the comparison meaningless.
			if pr >= b || pb.IntoData >= 0 && g.Fall(pr) == b || g.FuncOf[pr] != g.FuncOf[b] ||
				!out.Reachable || !out.StackKnown {
				ok = false
				break
			}
			if g.Fall(pr) == b && structuralBetween(p, pb.Last, bl.First) {
				// .if/.else arms or #ifdef blocks: only one is assembled.
				ok = false
				break
			}
			arms = append(arms, arm{out.Stack, pb.Last})
		}
		if !ok {
			continue
		}
		sort.Slice(arms, func(x, y int) bool { return arms[x].from < arms[y].from })
		for k := 1; k < len(arms); k++ {
			if arms[k].depth != arms[0].depth {
				c.ReportStmt(at, fmt.Sprintf("paths join here with different stack depths: %d bytes pushed coming from line %d, %d from line %d",
					arms[0].depth, lineOf(p, arms[0].from), arms[k].depth, lineOf(p, arms[k].from)))
				break
			}
		}
	}
}

// imbalance is the stack-imbalance message for the return at statement i,
// "" when the depth is unknown, zero, or the bytes on top are a target the
// code pushed to jump through the return.
func imbalance(f *facts, i int) string {
	st := &f.p.Stmts[i]
	if !st.IsRet() && !st.IsIret() || !f.res.Reachable(i) {
		return ""
	}
	s := &f.res.In[i]
	if !s.StackKnown || s.Stack == 0 {
		return ""
	}
	if s.Stack > 0 {
		if st.IsIret() && builtFrame(s) {
			// pushf, push cs, push target; iret: a far jump (or a
			// mode switch) through a frame built for the purpose.
			return ""
		}
		top, ok := s.Top()
		if ok && top.Depth == s.Stack && !top.Flags && (top.Src == flow.NoReg || top.Src.IsSegment()) {
			// push $target; ret, or push %es; push $off; lret: a jump.
			return ""
		}
		what := ""
		if ok && top.Depth == s.Stack && top.Src.IsGPR() {
			what = fmt.Sprintf(" (%%%s on top)", top.Src.Name(top.Width*8))
		}
		return fmt.Sprintf("%s with %d bytes still pushed%s: it returns to the pushed value, not to the caller", st.Mnemonic, s.Stack, what)
	}
	if !calledEntry(f, i) {
		// Entered by a far jump or fallthrough from code the analysis
		// treats as another function (a mode switch continuing at a
		// label): what it pops was pushed before the "entry".
		return ""
	}
	return fmt.Sprintf("%s with %d bytes popped beyond the function's entry: it returns past the caller", st.Mnemonic, -s.Stack)
}

// calledEntry reports whether statement i belongs to a function some call
// in the document enters, the one kind of entry whose depth 0 is known to
// be the return address.
func calledEntry(f *facts, i int) bool {
	b := f.g.BlockOf[i]
	if b < 0 || f.g.FuncOf[b] < 0 {
		return false
	}
	entry := f.g.Funcs[f.g.FuncOf[b]].Entry
	for k := range f.g.Blocks {
		if f.g.Blocks[k].Callee == entry {
			return true
		}
	}
	return false
}

// builtFrame reports pushed flags among the bytes above the function's
// entry depth: the shape of an interrupt frame the code built itself.
func builtFrame(s *flow.State) bool {
	for _, sl := range s.Slots() {
		if sl.Flags && sl.Depth > 0 {
			return true
		}
	}
	return false
}

// firstInstr is the first instruction statement of block b, -1 when it has
// none.
func firstInstr(g *flow.CFG, b int) int {
	at := -1
	stmts(g, b, func(i int) bool {
		if g.Prog.Stmts[i].Kind == asm.Instr || g.Prog.Stmts[i].Kind == asm.MacroCall {
			at = i
			return false
		}
		return true
	})
	return at
}

// structuralBetween reports conditional assembly or repetition directives
// between statements a and b: edges across them join alternatives of
// which only one is assembled.
func structuralBetween(p *asm.Program, a, b int) bool {
	for i := a + 1; i < b && i < len(p.Stmts); i++ {
		st := &p.Stmts[i]
		if st.Kind != asm.Directive && st.Kind != asm.Cpp {
			continue
		}
		n := st.Name
		if strings.HasPrefix(n, ".if") || strings.HasPrefix(n, "#if") || strings.HasPrefix(n, "#el") ||
			strings.HasPrefix(n, ".el") || n == ".endif" || n == "#endif" || n == ".rept" || n == ".irp" ||
			n == ".irpc" || n == ".endr" {
			return true
		}
	}
	return false
}

func checkPushfPopf(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g := f.p, f.g
	for fi := range g.Funcs {
		first := firstInstr(g, g.Funcs[fi].Entry)
		if first < 0 || !isPushf(&p.Stmts[first]) || !f.res.Reachable(first) || !funcHas(f, fi, isPopf) {
			continue
		}
		at, ok := searchFrom(g, first, func(i int) verdict {
			st := &p.Stmts[i]
			switch {
			case isPopf(st), st.IsIret(), st.Kind == asm.MacroCall:
				return done
			case st.IsRet():
				if imbalance(f, i) != "" || !f.res.In[i].StackKnown {
					// stack-imbalance reports it already, at the ret or
					// where the paths joined.
					return done
				}
				return found
			}
			return keep
		})
		if ok {
			c.ReportStmt(at, fmt.Sprintf("%s without popf on this path: the flags pushf saved at line %d are not restored", p.Stmts[at].Mnemonic, lineOf(p, first)))
		}
	}
}

func checkCliSti(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g := f.p, f.g
	for fi := range g.Funcs {
		if fi == f.bootFunc || isHandler(g, fi) || funcHas(f, fi, isModeCode) || !funcHas(f, fi, enablesIF) {
			continue
		}
		reported := false
		for _, b := range g.Funcs[fi].Blocks {
			stmts(g, b, func(cli int) bool {
				if reported || instName(&p.Stmts[cli]) != "cli" || !f.res.Reachable(cli) {
					return true
				}
				at, ok := searchFrom(g, cli, func(i int) verdict {
					st := &p.Stmts[i]
					switch {
					case enablesIF(st), instName(st) == "cli", st.IsIret(), st.Kind == asm.MacroCall:
						return done
					case st.IsRet():
						if f.res.In[i].IF == flow.Set {
							return done
						}
						return found
					}
					return keep
				})
				if ok {
					reported = true
					c.ReportStmt(at, fmt.Sprintf("%s with interrupts still disabled on this path: the cli at line %d is undone on other paths but not here", p.Stmts[at].Mnemonic, lineOf(p, cli)))
				}
				return true
			})
		}
	}
}

func isPushf(st *asm.Stmt) bool {
	n := instName(st)
	return n == "pushf" || n == "pushfq"
}

func isPopf(st *asm.Stmt) bool {
	n := instName(st)
	return n == "popf" || n == "popfq"
}

// enablesIF reports sti and popf, the two ways code re-enables interrupts.
func enablesIF(st *asm.Stmt) bool {
	return instName(st) == "sti" || isPopf(st)
}

// isModeSwitch reports the steps of a mode switch that must run with
// interrupts off: lgdt/lidt, a CR0 write, lmsw. A far jump on its own is
// not one: real-mode code far-jumps to the next stage all the time.
func isModeSwitch(st *asm.Stmt) bool {
	switch instName(st) {
	case "lmsw", "lgdt", "lidt":
		return true
	case "mov":
		return len(st.Ops) == 2 && st.Ops[1].Kind == asm.OpReg && st.Ops[1].RegName == "cr0"
	}
	return false
}

// isModeCode reports mode-switch steps and far jumps: a function with
// either leaves interrupts the way the next stage needs them.
func isModeCode(st *asm.Stmt) bool {
	return isModeSwitch(st) || instName(st) == "ljmp" || instName(st) == "jmp" && st.IsFar()
}

// funcHas reports whether some statement of function fi satisfies pred.
func funcHas(f *facts, fi int, pred func(*asm.Stmt) bool) bool {
	hit := false
	for _, b := range f.g.Funcs[fi].Blocks {
		stmts(f.g, b, func(i int) bool {
			hit = pred(&f.p.Stmts[i])
			return !hit
		})
		if hit {
			return true
		}
	}
	return false
}
