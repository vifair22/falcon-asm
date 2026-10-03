package perf

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// CodeVzeroupper is the code of rule 58.
const CodeVzeroupper = "missing-vzeroupper"

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeVzeroupper,
		Section:   section,
		Default:   lint.Warning,
		Langs:     lint.LangAsm | lint.LangInline,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "call, ret or jump out of the document while ymm/zmm upper halves are dirty: add vzeroupper first.",
			Why: "A VEX.256 or EVEX.512 write leaves the upper bits of a vector register non-zero. When legacy " +
				"(non-VEX) SSE code runs in that state, Intel cores pay for it: Sandy Bridge through Broadwell " +
				"save and restore the upper halves on each transition (on the order of 70 cycles each way, per " +
				"Agner Fog's microarchitecture guide; Intel's optimization manual says to execute vzeroupper " +
				"before any transition to legacy SSE code), " +
				"and Skylake and later instead make every SSE instruction depend on the full register and merge " +
				"it, which serializes otherwise independent code. The callee of an external call, or the caller " +
				"a ret returns to, may well be SSE code (libc, older compilers), which is why the System V psABI " +
				"convention and every compiler emit vzeroupper before leaving AVX code. The rule fires only where " +
				"the dataflow proves some upper half dirty on every path, at a call (unless the callee is in the " +
				"document and itself uses ymm/zmm or runs no legacy SSE), a ret, or a jmp to a label outside the " +
				"document. It stays quiet when the block passes ymm/zmm arguments in ymm0-ymm7 (vzeroupper would " +
				"destroy them), at a ret where only ymm0 is dirty (a __m256 return value), and for AMD CPU " +
				"targets, whose cores track the upper state without a transition penalty.",
			Bad:  "\tvaddps %ymm1, %ymm2, %ymm3\n\tvmovups %ymm3, (%rdi)\n\tret",
			Good: "\tvaddps %ymm1, %ymm2, %ymm3\n\tvmovups %ymm3, (%rdi)\n\tvzeroupper\n\tret",
		},
		Check: checkVzeroupper,
	})
}

func checkVzeroupper(c *lint.Context) {
	g, res := flowOf(c)
	if g == nil {
		return
	}
	if name, ok := profileName(c); ok && amd(name) {
		return
	}
	p := c.Prog
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || s.IsFar() {
			continue
		}
		b := g.BlockOf[i]
		if b < 0 || g.Blocks[b].Last != i {
			continue
		}
		st := res.Before(i)
		if st == nil || !st.Reachable || st.AnyUpperDirty() != flow.Set {
			continue
		}
		dirty := dirtyFamilies(st)
		bl := &g.Blocks[b]
		var what string
		switch {
		case s.IsCall():
			if passesWideArgs(p, bl, i) || calleeTolerates(g, bl.Callee) {
				continue
			}
			what = "call"
		case s.Inst.Name == "ret":
			// ymm0/zmm0 is where a __m256/__m512 is returned; zeroing
			// its upper half would corrupt the result.
			if len(dirty) == 1 && dirty[0] == flow.Vec(0) {
				continue
			}
			what = "ret"
		case s.IsUncondJump() && bl.Exit == flow.ExitJump && bl.Target < 0 &&
			len(s.Ops) == 1 && !s.Ops[0].Indirect:
			if passesWideArgs(p, bl, i) {
				continue
			}
			what = "jump out of the document"
		default:
			continue
		}
		names := make([]string, len(dirty))
		for k, r := range dirty {
			names[k] = r.Name(256)
		}
		msg := fmt.Sprintf("%s with dirty upper halves (%s): legacy SSE code after it pays the AVX-SSE transition penalty; add vzeroupper",
			what, strings.Join(names, ", "))
		reportMnem(c, i, msg, insertFix(c, i, "Insert vzeroupper", "vzeroupper")...)
	}
}

// dirtyFamilies lists the vector families known to have dirty upper bits.
func dirtyFamilies(st *flow.State) []flow.Reg {
	var out []flow.Reg
	for k := 0; k < 32; k++ {
		if st.UpperDirty(flow.Vec(k)) == flow.Set {
			out = append(out, flow.Vec(k))
		}
	}
	return out
}

// passesWideArgs reports a block that writes a ymm/zmm register among the
// psABI vector argument registers (ymm0..ymm7) before its call or tail
// jump: the callee takes __m256/__m512 arguments, and vzeroupper there
// would destroy them.
func passesWideArgs(p *asm.Program, bl *flow.Block, last int) bool {
	for j := bl.First; j < last; j++ {
		s := instr(p, j)
		if s == nil || len(s.Ops) == 0 {
			continue
		}
		op := &s.Ops[len(s.Ops)-1]
		r, _, hi, ok := regFamily(op)
		if ok && r.IsVector() && hi >= 255 && r < flow.Vec(8) {
			return true
		}
	}
	return false
}

// calleeTolerates reports an in-document callee for which dirty upper
// halves cost nothing or are expected: one that uses ymm/zmm itself (it
// takes or returns wide vectors, or is AVX code that manages its own
// state), or one that runs no legacy SSE and never leaves the document's
// view through a call or an outward jump. An unknown callee (-1) does not
// tolerate it.
func calleeTolerates(g *flow.CFG, entry int) bool {
	if entry < 0 {
		return false
	}
	var fn *flow.Func
	for k := range g.Funcs {
		if g.Funcs[k].Entry == entry {
			fn = &g.Funcs[k]
			break
		}
	}
	if fn == nil {
		return false
	}
	sse, leaves := false, false
	for _, b := range fn.Blocks {
		bl := &g.Blocks[b]
		switch bl.Exit {
		case flow.ExitCall, flow.ExitIndirect, flow.ExitFar:
			leaves = true
		case flow.ExitJump:
			if bl.Target < 0 {
				leaves = true
			}
		}
		for i := bl.First; i <= bl.Last; i++ {
			s := instr(g.Prog, i)
			if s == nil {
				continue
			}
			for k := range s.Ops {
				r, _, hi, ok := regFamily(&s.Ops[k])
				if !ok || !r.IsVector() {
					continue
				}
				if hi >= 255 {
					return true
				}
				if !strings.HasPrefix(s.Inst.Name, "v") {
					sse = true
				}
			}
		}
	}
	return !sse && !leaves
}
