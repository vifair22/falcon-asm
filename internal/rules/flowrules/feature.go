package flowrules

import (
	"fmt"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeFeatureUnguarded,
		Section:   sectionTarget,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Instruction needing a CPUID feature (rdtsc, cmov, AVX2, rdrand) with no CPUID test of it on every path, beyond what the target guarantees.",
			Why: "An instruction from an extension the processor lacks raises #UD. Code that must run on " +
				"older machines tests the CPUID bit first and takes another path when it is clear. The rule " +
				"looks, in the instruction's function, for a cpuid whose leaf the dataflow knows followed by " +
				"a test, bt or and of the output register covering the feature's bit, and reports the " +
				"instruction when no such test dominates it (every path from the function's entry passes " +
				"one). Nothing is reported when the target guarantees the feature: an x86-64 level or CPU " +
				"profile that includes it, or 64-bit code for the x86-64 baseline (SSE2, CMOV, TSC ...). " +
				"Information by default, because the test often lives in another function (a startup check " +
				"that refuses to boot without the feature); silence it per line or per file there.",
			Bad:  "\t.code32\nf:\n\trdrand %eax\n\tret",
			Good: "\t.code32\nf:\n\tmovl $1, %eax\n\tcpuid\n\tbtl $30, %ecx\n\tjnc 1f\n\trdrand %eax\n1:\tret",
		},
		Check: checkFeatures,
	})
}

// featuresOf is the CPUID bits an instruction needs in the form it is
// written: the instruction's own bit (rdtsc: TSC), else the bits every form of
// its operand family (legacy/VEX.128, VEX.256, EVEX) needs. Forms that no
// bit guards, or forms that disagree (movd is MMX or SSE2 by operand),
// leave nothing the rule can name.
func featuresOf(st *asm.Stmt) []hwdb.FeatureBit {
	inst := st.Inst
	fam := vectorFamily(st)
	key := featureKey{inst, fam}
	if v, ok := featureCache.Load(key); ok {
		return v.([]hwdb.FeatureBit)
	}
	var out []hwdb.FeatureBit
	if fb, ok := hwdb.FeatureForMnemonic(inst.Name); ok {
		out = []hwdb.FeatureBit{fb}
	} else {
		forms := familyForms(inst.Forms, fam)
		for k, form := range forms {
			fs := hwdb.FeaturesForISASet(form.ISASet)
			if len(fs) == 0 {
				out = nil
				break
			}
			if k == 0 {
				out = append([]hwdb.FeatureBit(nil), fs...)
				continue
			}
			out = intersect(out, fs)
			if len(out) == 0 {
				break
			}
		}
	}
	featureCache.Store(key, out)
	return out
}

type featureKey struct {
	inst *isa.Instruction
	fam  int
}

// Operand families, as the encoder picks them.
const (
	famLegacy = iota // general, MMX, xmm0-15: legacy or VEX.128
	famYMM           // ymm0-15: VEX.256
	famEVEX          // zmm, a mask, or a vector register numbered 16 and up
)

func vectorFamily(st *asm.Stmt) int {
	fam := famLegacy
	for k := range st.Ops {
		op := &st.Ops[k]
		if op.Mask != "" || op.Broadcast != "" {
			return famEVEX
		}
		if op.Kind != asm.OpReg {
			continue
		}
		n := op.RegName
		switch {
		case strings.HasPrefix(n, "zmm"):
			return famEVEX
		case strings.HasPrefix(n, "ymm") || strings.HasPrefix(n, "xmm"):
			if len(n) == 5 && n[3] >= '1' && n[3] <= '3' {
				return famEVEX // xmm16..xmm31
			}
			if n[0] == 'y' {
				fam = famYMM
			}
		}
	}
	return fam
}

// familyForms keeps the forms of the operand family, all of them when none
// matches (the tables may not split the family the way the operands do).
func familyForms(forms []isa.Form, fam int) []isa.Form {
	var out []isa.Form
	for _, f := range forms {
		evex := strings.HasPrefix(strings.ToUpper(f.ISASet), "AVX512")
		ops := strings.ToLower(f.Operands)
		var keep bool
		switch fam {
		case famEVEX:
			keep = evex
		case famYMM:
			keep = !evex && strings.Contains(ops, "ymm")
		default:
			keep = !evex && !strings.Contains(ops, "ymm") && !strings.Contains(ops, "zmm")
		}
		if keep {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return forms
	}
	return out
}

var featureCache sync.Map

func intersect(a, b []hwdb.FeatureBit) []hwdb.FeatureBit {
	var out []hwdb.FeatureBit
	for _, x := range a {
		for _, y := range b {
			if x.Name == y.Name {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

// baseline lists the profiles the target guarantees for code in mode m:
// the CPU profile, the x86-64 level, and in 64-bit code the x86-64
// baseline every long-mode processor implements.
func baseline(t lint.Target, m asm.Mode) []*hwdb.Profile {
	var out []*hwdb.Profile
	add := func(name string) {
		if p, ok := hwdb.CPUProfile(name); ok {
			out = append(out, p)
		} else if alias, ok := hwdb.ProfileForMarch(name); ok {
			if p, ok := hwdb.CPUProfile(alias); ok {
				out = append(out, p)
			}
		}
	}
	if t.CPU != "" {
		add(t.CPU)
	}
	switch {
	case t.Level >= 2:
		add(fmt.Sprintf("x86-64-v%d", t.Level))
	case t.Level == 1 || m == asm.Mode64:
		add("x86-64")
	}
	return out
}

// guaranteed reports whether some baseline profile implements a form of
// the instruction.
func guaranteed(profiles []*hwdb.Profile, inst *isa.Instruction) bool {
	for _, p := range profiles {
		for _, form := range inst.Forms {
			if form.ISASet != "" && p.Supports(form.ISASet) {
				return true
			}
		}
	}
	return false
}

// guard is a CPUID feature test: the block whose conditional jump decides
// on bits of a cpuid output register.
type guard struct {
	block    int
	leaf     uint64
	sub      uint64
	subKnown bool
	reg      flow.Reg
	mask     uint64 // bits tested; all ones when the test's bits are not known
}

func (gd guard) covers(fb hwdb.FeatureBit) bool {
	if gd.leaf != uint64(fb.Leaf) || gd.reg != cpuidReg(fb.Reg) || gd.mask&(1<<uint(fb.Bit)) == 0 {
		return false
	}
	return !fb.HasSub || !gd.subKnown || gd.sub == uint64(fb.Subleaf)
}

func cpuidReg(name string) flow.Reg {
	switch strings.ToLower(name) {
	case "eax":
		return flow.RAX
	case "ebx":
		return flow.RBX
	case "ecx":
		return flow.RCX
	case "edx":
		return flow.RDX
	}
	return flow.NoReg
}

func checkFeatures(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	type need struct {
		stmt int
		fbs  []hwdb.FeatureBit
	}
	byFunc := map[int][]need{}
	var order []int
	bases := map[asm.Mode][]*hwdb.Profile{}
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || len(st.Inst.Forms) == 0 || !res.Reachable(i) {
			continue
		}
		fbs := featuresOf(st)
		if len(fbs) == 0 || fbs[0].Name == "LM" {
			// Long mode is a mode, not an optional extension: the mode
			// rules report a 64-bit-only instruction in other code.
			continue
		}
		base, ok := bases[st.Mode]
		if !ok {
			base = baseline(c.Target, st.Mode)
			bases[st.Mode] = base
		}
		if guaranteed(base, st.Inst) {
			continue
		}
		b := g.BlockOf[i]
		fi := -1
		if b >= 0 {
			fi = g.FuncOf[b]
		}
		if fi < 0 {
			continue
		}
		if _, ok := byFunc[fi]; !ok {
			order = append(order, fi)
		}
		byFunc[fi] = append(byFunc[fi], need{i, fbs})
	}
	for _, fi := range order {
		guards := findGuards(f, fi)
		var idom map[int]int
		if len(guards) > 0 {
			idom = dominators(g, fi)
		}
		reported := map[string]bool{}
		for _, n := range byFunc[fi] {
			b := g.BlockOf[n.stmt]
			for _, fb := range n.fbs {
				if reported[fb.Name] || guardedBy(guards, idom, b, fb) {
					continue
				}
				reported[fb.Name] = true
				st := &p.Stmts[n.stmt]
				c.ReportRange(st.MnemStart, st.MnemStart+len(st.Mnemonic), fmt.Sprintf(
					"%s needs %s (%s), and no CPUID test of that bit precedes it on every path; the target (%s) does not guarantee it",
					st.Mnemonic, fb.Name, fb.Where(), targetText(c.Target, st.Mode)))
			}
		}
	}
}

func guardedBy(guards []guard, idom map[int]int, b int, fb hwdb.FeatureBit) bool {
	for _, gd := range guards {
		if gd.covers(fb) && dominates(idom, gd.block, b) && gd.block != b {
			return true
		}
	}
	return false
}

// targetText describes the target for the message.
func targetText(t lint.Target, m asm.Mode) string {
	var parts []string
	if t.CPU != "" {
		parts = append(parts, "cpu "+t.CPU)
	}
	if t.Level > 0 {
		parts = append(parts, fmt.Sprintf("x86-64-v%d", t.Level))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d-bit code, no cpu or level set", int(m))
	}
	return strings.Join(parts, ", ")
}

// findGuards collects the CPUID feature tests of function fi: a test, bt
// or and (optionally followed by a cmp) of a register whose value comes
// from a cpuid with a known leaf, in a block that ends with a conditional
// jump.
func findGuards(f *facts, fi int) []guard {
	p, g, res := f.p, f.g, f.res
	var out []guard
	for _, b := range g.Funcs[fi].Blocks {
		if g.Blocks[b].Exit != flow.ExitCond {
			continue
		}
		stmts(g, b, func(i int) bool {
			st := &p.Stmts[i]
			name := instName(st)
			if name != "test" && name != "bt" && name != "and" || len(st.Ops) != 2 || !res.Reachable(i) {
				return true
			}
			op := &st.Ops[1]
			if op.Kind != asm.OpReg {
				return true
			}
			r, lo, _, ok := flow.RegOf(op.RegName)
			if !ok || !r.IsGPR() || lo != 0 {
				return true
			}
			src, reg, exact := cpuidSource(f, i, r, 4)
			if src < 0 {
				return true
			}
			leaf, sub, subOK, okLeaf := leafAt(res, src)
			if !okLeaf {
				return true
			}
			mask := ^uint64(0)
			if st.Ops[0].Kind == asm.OpImm && st.Ops[0].ImmVal.Known && exact {
				v := uint64(st.Ops[0].ImmVal.V)
				if name == "bt" {
					mask = 1 << (v & 63)
				} else {
					mask = v
				}
			}
			out = append(out, guard{block: b, leaf: leaf, sub: sub, subKnown: subOK, reg: reg, mask: mask})
			return true
		})
	}
	return out
}

func leafAt(res *flow.Result, i int) (leaf, sub uint64, subOK, ok bool) {
	leaf, ok, sub, subOK = res.CPUIDLeaf(i)
	return leaf, sub, subOK, ok
}

// cpuidSource follows the last writer of family r before statement i back
// through register moves and bit operations to a cpuid, returning the
// cpuid statement and the output register the value started in; -1 when
// the value does not come from one within depth steps. exact is false when
// a shift or rotate on the way moved the bits, so a test's mask no longer
// names CPUID bit positions.
func cpuidSource(f *facts, i int, r flow.Reg, depth int) (int, flow.Reg, bool) {
	p, res := f.p, f.res
	exact := true
	for ; depth > 0; depth-- {
		d := res.In[i].Get(r).Def
		if d < 0 {
			return -1, flow.NoReg, false
		}
		st := &p.Stmts[d]
		switch instName(st) {
		case "cpuid":
			if r == flow.RAX || r == flow.RBX || r == flow.RCX || r == flow.RDX {
				return d, r, exact
			}
			return -1, flow.NoReg, false
		case "mov":
			if len(st.Ops) != 2 || st.Ops[0].Kind != asm.OpReg {
				return -1, flow.NoReg, false
			}
			sr, _, _, ok := flow.RegOf(st.Ops[0].RegName)
			if !ok || !sr.IsGPR() {
				return -1, flow.NoReg, false
			}
			i, r = d, sr
		case "shr", "shl", "sar", "rol", "ror":
			i, exact = d, false
		case "and", "or", "not":
			i = d
		default:
			return -1, flow.NoReg, false
		}
	}
	return -1, flow.NoReg, false
}
