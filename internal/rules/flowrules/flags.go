package flowrules

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeFlagsClobbered,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Flags of a cmp/test overwritten before the conditional instruction that was meant to read them.",
			Why: "A jcc, setcc, cmovcc, adc or sbb reads the flags of the last instruction that wrote them. " +
				"An inc, add, shift or call slipped in between a cmp and its jump (a pointer bump moved up, " +
				"a debug print added) silently changes what the jump tests. The rule reports a flag reader " +
				"whose flags come from such an instruction when, in the same block, a cmp or test before it " +
				"wrote those flags and nothing read them in between: the cmp's result is dead, which is " +
				"what makes the clobber a mistake rather than a deliberate test of the inc. Carry chains " +
				"(add then adc), flags restored with popf or sahf, and BIOS calls (which return status in " +
				"the flags) are not reported.",
			Bad:  "\tcmpb $0, (%si)\n\tincw %si\n\tjne next",
			Good: "\tincw %si\n\tcmpb $0, -1(%si)\n\tjne next",
		},
		Check: checkFlags,
	})
}

// isCompare reports the instructions whose only effect is the flags.
func isCompare(st *asm.Stmt) bool {
	switch instName(st) {
	case "cmp", "test", "bt":
		return true
	}
	return false
}

// carries reports instructions that continue a multi-word carry chain.
func carries(name string) bool {
	switch name {
	case "adc", "sbb", "rcl", "rcr", "adcx", "adox":
		return true
	}
	return false
}

var flagNames = []struct {
	f    asm.Flags
	name string
}{{asm.FlagCF, "CF"}, {asm.FlagZF, "ZF"}, {asm.FlagSF, "SF"}, {asm.FlagOF, "OF"}, {asm.FlagPF, "PF"}, {asm.FlagAF, "AF"}}

func flagList(f asm.Flags) string {
	var out []string
	for _, n := range flagNames {
		if f&n.f != 0 {
			out = append(out, n.name)
		}
	}
	return strings.Join(out, "/")
}

func checkFlags(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	for b := range g.Blocks {
		// A block entered only by returning from a call continues the
		// caller's block: "cmp; call f; je" is one sequence.
		var list []int
		collect := func(i int) bool {
			if p.Stmts[i].Kind == asm.Instr || p.Stmts[i].Kind == asm.MacroCall {
				list = append(list, i)
			}
			return true
		}
		if pr := g.Blocks[b].Pred; len(pr) == 1 && g.Blocks[pr[0]].Exit == flow.ExitCall && g.Fall(pr[0]) == b {
			stmts(g, pr[0], collect)
		}
		first := len(list)
		stmts(g, b, collect)
		for k := first; k < len(list); k++ {
			r := list[k]
			rs := &p.Stmts[r]
			reads := flagsRead(rs) & asm.FlagsStatus
			name := instName(rs)
			if reads == 0 || name == "pushf" || name == "pushfq" || name == "lahf" || !res.Reachable(r) {
				continue
			}
			w, cmp := clobberedCompare(p, list[:k], reads, carries(name))
			if cmp < 0 {
				continue
			}
			lost := reads & flagWrites(&p.Stmts[w]) & flagWrites(&p.Stmts[cmp])
			c.ReportStmt(r, fmt.Sprintf("%s reads %s from %s at line %d, which overwrote the flags of %s at line %d",
				rs.Mnemonic, flagList(lost), stmtText(p, w), lineOf(p, w), stmtText(p, cmp), lineOf(p, cmp)))
		}
	}
}

// clobberedCompare looks back from the end of before (the statements of
// the block ahead of a flag reader) for the instruction w that wrote the
// flags the reader reads, and returns it with the cmp/test it overwrote;
// cmp is -1 when there is no such pair.
func clobberedCompare(p *asm.Program, before []int, reads asm.Flags, chain bool) (w, cmp int) {
	k := len(before) - 1
	for ; k >= 0; k-- {
		st := &p.Stmts[before[k]]
		if st.Kind == asm.MacroCall || st.Inst == nil {
			return -1, -1
		}
		if flagWrites(st)&reads != 0 {
			break
		}
	}
	if k < 0 {
		return -1, -1
	}
	w = before[k]
	ws := &p.Stmts[w]
	switch name := instName(ws); {
	case isCompare(ws):
		return -1, -1
	case name == "popf" || name == "popfq" || name == "sahf" || name == "int" || name == "iret":
		// Restored or produced on purpose: the reader means these flags.
		return -1, -1
	case chain && (carries(name) || name == "add" || name == "sub" || name == "neg" ||
		name == "shl" || name == "sal" || name == "shr" || name == "sar"):
		return -1, -1
	}
	for k--; k >= 0; k-- {
		st := &p.Stmts[before[k]]
		if st.Kind == asm.MacroCall || st.Inst == nil {
			return -1, -1
		}
		if flagsRead(st)&asm.FlagsStatus != 0 {
			// The earlier flags were used; w's are the ones meant.
			return -1, -1
		}
		if flagWrites(st)&asm.FlagsStatus == 0 {
			continue
		}
		if isCompare(st) && flagWrites(st)&reads&flagWrites(ws) != 0 {
			return w, before[k]
		}
		return -1, -1
	}
	return -1, -1
}

// flagWrites is the flags an instruction may change; a call counts as
// changing every status flag, since the callee runs in between.
func flagWrites(st *asm.Stmt) asm.Flags {
	if st.IsCall() {
		return asm.FlagsStatus
	}
	return flagsWritten(st)
}
