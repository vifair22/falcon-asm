package flowrules

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeDFUnknown,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "String instruction reached with the direction flag not provably clear (no cld on some path).",
			Why: "movs, stos, lods, scas, cmps, ins and outs step their index registers up when DF is 0 and " +
				"down when it is 1. Code written for one direction that runs with the other copies backwards " +
				"over the wrong memory. The BIOS makes no promise about DF at the boot sector's entry, and an " +
				"interrupt handler runs with whatever the interrupted code had, so both must execute cld " +
				"before their first string instruction. The rule reports a string instruction when a cld " +
				"runs on some paths to it but not on others (a std or the entry state reaches it), and, once " +
				"per function, the first string instruction of a boot entry or interrupt handler that no cld " +
				"precedes. A DF that is unknown because of a call, an int or a popf is not reported: the " +
				"analysis cannot see what the callee left. The fix inserts cld before the instruction.",
			Bad:  "_start:\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x500, %di\n\tmovw $64, %cx\n\trep stosw",
			Good: "_start:\n\tcld\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x500, %di\n\tmovw $64, %cx\n\trep stosw",
		},
		Check: checkDF,
	})
}

// DF provenance: which kinds of definition reach a statement. The flow
// state only says DF is Unknown; the rule needs to know why, because an
// Unknown made of "cld on one path, nothing on the other" is a bug and an
// Unknown made of "a call we cannot see into" is not evidence of anything.
const (
	dfClear  uint8 = 1 << iota // a cld (or a popf restoring a clear DF)
	dfSet                      // a std
	dfEntry                    // the function's entry state, untouched
	dfOpaque                   // a call, int, popf or macro the analysis cannot see through
)

func checkDF(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	any := false
	for i := range p.Stmts {
		if isStringOp(&p.Stmts[i]) && res.Reachable(i) {
			any = true
			break
		}
	}
	if !any {
		return
	}
	// gen is a block's own effect on DF: the kind of its last DF
	// definition, 0 when it has none and passes its input through.
	gen := make([]uint8, len(g.Blocks))
	for b := range g.Blocks {
		stmts(g, b, func(i int) bool {
			if k := dfDef(f, i); k != 0 {
				gen[b] = k
			}
			return true
		})
	}
	in := make([]uint8, len(g.Blocks))
	start := make([]bool, len(g.Blocks))
	for k := range g.Funcs {
		start[g.Funcs[k].Entry] = true
		in[g.Funcs[k].Entry] = dfEntry
	}
	out := func(b int) uint8 {
		if gen[b] != 0 {
			return gen[b]
		}
		return in[b]
	}
	// Monotone over a four-bit set, so the round-robin settles in a few
	// passes; the cap only guards against a mistake.
	for pass, changed := 0, true; changed && pass < 64; pass++ {
		changed = false
		for b := range g.Blocks {
			s := in[b]
			if start[b] {
				s |= dfEntry
			}
			for _, pr := range g.Blocks[b].Pred {
				if g.Blocks[pr].IntoData >= 0 && g.Fall(pr) == b {
					s |= dfOpaque
					continue
				}
				s |= out(pr)
			}
			if s != in[b] {
				in[b] = s
				changed = true
			}
		}
	}
	reported := map[int]bool{}
	for b := range g.Blocks {
		cur := in[b]
		fi := g.FuncOf[b]
		stmts(g, b, func(i int) bool {
			if k := dfDef(f, i); k != 0 {
				cur = k
				return true
			}
			st := &p.Stmts[i]
			if !isStringOp(st) || !res.Reachable(i) || res.In[i].DF != flow.Unknown || reported[fi] {
				return true
			}
			if msg := dfMessage(f, fi, i, cur); msg != "" {
				reported[fi] = true
				at := insertAbove(p, i)
				c.ReportStmt(i, msg, lint.NewFix("Insert cld before the string instruction", c.InsertLineAbove(at, "cld")))
			}
			return true
		})
	}
}

// dfMessage decides whether a string instruction whose DF is Unknown with
// provenance src is worth reporting, and says why.
func dfMessage(f *facts, fi, i int, src uint8) string {
	if src&dfOpaque != 0 || fi < 0 {
		return ""
	}
	name := f.p.Stmts[i].Mnemonic
	if src&dfClear != 0 {
		if src&dfSet != 0 {
			at := firstStd(f, fi)
			if at >= 0 {
				return fmt.Sprintf("the direction flag may be set at this %s: the std at line %d reaches it on some path without a cld", name, lineOf(f.p, at))
			}
			return fmt.Sprintf("the direction flag may be set at this %s: a std reaches it on some path without a cld", name)
		}
		return fmt.Sprintf("cld runs on some paths to this %s but not on all: on the others the direction flag is whatever the caller left", name)
	}
	switch {
	case fi == f.bootFunc:
		return fmt.Sprintf("%s runs with the direction flag the BIOS left: nothing since the boot entry executed cld", name)
	case isHandler(f.g, fi):
		return fmt.Sprintf("%s runs with the direction flag the interrupted code left: the handler has not executed cld", name)
	}
	return ""
}

// dfDef classifies statement i as a DF definition: dfClear, dfSet, dfOpaque,
// or 0 when it leaves DF alone.
func dfDef(f *facts, i int) uint8 {
	st := &f.p.Stmts[i]
	switch st.Kind {
	case asm.MacroCall:
		return dfOpaque
	case asm.Instr:
	default:
		return 0
	}
	if st.Inst == nil {
		if st.Mnemonic != "" {
			return dfOpaque
		}
		return 0
	}
	switch st.Inst.Name {
	case "cld":
		return dfClear
	case "std":
		return dfSet
	case "call", "lcall", "int", "popf", "popfq", "iret", "iretq":
	default:
		if flagsWritten(st)&asm.FlagDF == 0 {
			return 0
		}
	}
	if !f.res.Reachable(i) {
		return dfOpaque
	}
	switch f.after(i).DF {
	case flow.Clear:
		return dfClear
	case flow.Set:
		return dfSet
	}
	return dfOpaque
}

// firstStd is the first std of function fi, -1 when it has none.
func firstStd(f *facts, fi int) int {
	best := -1
	for _, b := range f.g.Funcs[fi].Blocks {
		stmts(f.g, b, func(i int) bool {
			if instName(&f.p.Stmts[i]) == "std" && (best < 0 || i < best) {
				best = i
			}
			return true
		})
	}
	return best
}

// isHandler reports a function that leaves through iret: an interrupt or
// exception handler, entered with the interrupted code's flags.
func isHandler(g *flow.CFG, fi int) bool {
	for _, b := range g.Funcs[fi].Blocks {
		if g.Blocks[b].Exit == flow.ExitIret {
			st := &g.Prog.Stmts[g.Blocks[b].Last]
			if st.IsIret() {
				return true
			}
		}
	}
	return false
}
