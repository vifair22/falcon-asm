package perf

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Codes of rule 59.
const (
	CodeFalseDep    = "false-dependency"
	CodePartialFlag = "partial-flags-stall"
	CodeIncNoCarry  = "inc-no-carry"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeFalseDep,
		Section:   section,
		Default:   lint.Info,
		Langs:     lint.LangAsm | lint.LangInline,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "popcnt/lzcnt/tzcnt into a register that was not zeroed first: a false dependency on its old value.",
			Why: "These instructions write their whole destination, but on affected Intel cores the hardware " +
				"still waits for the destination's previous value: lzcnt and tzcnt on Haswell through Skylake " +
				"(fixed in Cannon Lake), popcnt from Sandy Bridge through Skylake as well (fixed in Ice Lake). " +
				"In a loop the result then chains through iterations that are really independent; the effect " +
				"is why GCC (its avoid_false_dep_for_bmi tuning) and Clang emit a zeroing xor before them " +
				"when tuning for these cores. The xor idiom is recognised at rename and costs no " +
				"execution unit. The rule applies when no CPU is targeted, for an x86-64-vN level target, and " +
				"for the affected profiles; it stays quiet when the destination is also a source (the dependency " +
				"is real), was last written by a zeroing idiom or an immediate move, or is 16 bits wide (a " +
				"32-bit xor would change the bits a 16-bit result keeps).",
			Bad:  "\tpopcntq (%rdi), %rax\n\taddq %rax, %rcx",
			Good: "\txorl %eax, %eax\n\tpopcntq (%rdi), %rax\n\taddq %rax, %rcx",
		},
		Check: checkFalseDep,
	})
	lint.Register(&lint.Rule{
		Code:      CodePartialFlag,
		Section:   section,
		Default:   lint.Info,
		Langs:     lint.LangAsm | lint.LangInline,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "CF read while the latest flag writer is inc/dec: the flags must be merged from two instructions.",
			Why: "inc and dec write every status flag except CF, so a later instruction reading CF needs flags " +
				"from two different producers. P6-family cores (Pentium Pro through Core 2) stall until the " +
				"inc/dec retires; Sandy Bridge and later insert a flag-merging uop (Agner Fog, \"The " +
				"microarchitecture of Intel, AMD and VIA CPUs\", partial flags stall sections). The pattern is " +
				"intentional in carry-chain loops (adc with an inc'd index) and cheap on modern cores, hence " +
				"Info. Using add/sub $1 writes every flag and avoids the merge, where the old CF is not needed.",
			Bad:  "\tclc\n\tdecl %ecx\n\tadcl $0, %eax",
			Good: "\tclc\n\tsubl $1, %ecx\n\tadcl $0, %eax",
		},
		Check: checkPartialFlags,
	})
	lint.Register(&lint.Rule{
		Code:      CodeIncNoCarry,
		Section:   section,
		Default:   lint.Warning,
		Langs:     lint.LangAsm | lint.LangInline,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Branch, setcc or cmovcc on CF right after inc/dec, which do not set CF: it tests a stale carry.",
			Why: "inc and dec leave CF as it was (Intel SDM Vol. 2, INC/DEC: \"The CF flag is not affected\"), " +
				"so `incl %eax; jc overflow` never sees the increment's carry; it tests whatever an older " +
				"instruction left there. This is a correctness bug far more often than an idiom, and it is " +
				"also a partial flags merge. The rule fires when a jcc, setcc or cmovcc reading CF immediately " +
				"follows the inc/dec. It stays quiet when the instruction just before the inc/dec wrote CF " +
				"(`cmp; inc; jb` deliberately carries the compare's CF across the inc) and for adc/sbb/rcl/rcr, " +
				"which consume a carry chain on purpose (see partial-flags-stall). Use add/sub $1 to get a carry.",
			Bad:  "\tincl %eax\n\tjc overflow",
			Good: "\taddl $1, %eax\n\tjc overflow",
		},
		Check: checkIncNoCarry,
	})
}

// falseDepAffected says whether the target may be a core with the false
// output dependency for the instruction.
func falseDepAffected(c *lint.Context, name string) bool {
	p, known := profileName(c)
	if !known || generic(p) {
		return true
	}
	switch p {
	case "haswell", "broadwell", "skylake", "skylake-avx512":
		return true
	case "sandybridge", "ivybridge":
		// lzcnt/tzcnt there decode as bsr/bsf, whose dependency on the
		// destination is architectural, not false.
		return name == "popcnt"
	}
	return false
}

func checkFalseDep(c *lint.Context) {
	_, res := flowOf(c)
	if res == nil {
		return
	}
	p := c.Prog
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || len(s.Ops) != 2 {
			continue
		}
		name := s.Inst.Name
		if name != "popcnt" && name != "lzcnt" && name != "tzcnt" {
			continue
		}
		dst := &s.Ops[1]
		r, _, hi, ok := regFamily(dst)
		if !ok || !r.IsGPR() || hi < 31 || mentions(&s.Ops[0], r) || !falseDepAffected(c, name) {
			continue
		}
		st := res.Before(i)
		if st == nil || !st.Reachable || breaksDep(p, st.Get(r).Def, r) {
			continue
		}
		z := r.Name(32)
		xor := fmt.Sprintf("xorl %%%s, %%%s", z, z)
		msg := fmt.Sprintf("%s has a false dependency on the old value of %%%s on Intel cores before %s; zero it first (%s)",
			name, dst.Reg.Name, fixedIn(name), xor)
		reportMnem(c, i, msg, insertFix(c, i, "Insert "+xor, xor)...)
	}
}

func fixedIn(name string) string {
	if name == "popcnt" {
		return "Ice Lake"
	}
	return "Cannon Lake"
}

// breaksDep reports whether statement def (the last writer of family r)
// already starts a fresh dependency chain: a zeroing idiom or a move of an
// immediate.
func breaksDep(p *asm.Program, def int, r flow.Reg) bool {
	if def < 0 || def >= len(p.Stmts) {
		return false
	}
	s := instr(p, def)
	if s == nil || len(s.Ops) != 2 {
		return false
	}
	switch s.Inst.Name {
	case "xor", "sub":
		return mentions(&s.Ops[0], r) && mentions(&s.Ops[1], r) && s.Ops[0].Kind == asm.OpReg
	case "mov":
		return s.Ops[0].Kind == asm.OpImm && mentions(&s.Ops[1], r)
	}
	return false
}

// incDec returns the statement at index def when it is an inc or dec.
func incDec(p *asm.Program, def int) *asm.Stmt {
	if def < 0 || def >= len(p.Stmts) {
		return nil
	}
	s := instr(p, def)
	if s == nil || s.Inst.Name != "inc" && s.Inst.Name != "dec" {
		return nil
	}
	return s
}

// carryRead is a CF reader whose latest flag writer is an inc/dec.
type carryRead struct {
	reader, inc int
	cfDef       int // the CF writer the reader actually sees
}

// carryReads lists every reachable CF reader whose zero flag (and so every
// flag but CF) comes from an inc or dec, memoised for the two rules that
// share it.
func carryReads(c *lint.Context) []carryRead {
	v := c.Memo("perf/carryReads", func() any {
		_, res := flowOf(c)
		if res == nil {
			return []carryRead(nil)
		}
		var out []carryRead
		p := c.Prog
		for i := range p.Stmts {
			s := instr(p, i)
			// pushf and its kind read every flag to save them, not to
			// consume a carry; the merge there is incidental.
			if s == nil || s.FlagsRead()&asm.FlagCF == 0 || s.FlagsRead()&^asm.FlagsStatus != 0 {
				continue
			}
			st := res.Before(i)
			if st == nil || !st.Reachable {
				continue
			}
			z := st.FlagDef[flow.FlagZF]
			if incDec(p, z) == nil || st.FlagDef[flow.FlagCF] == z {
				continue
			}
			out = append(out, carryRead{reader: i, inc: z, cfDef: st.FlagDef[flow.FlagCF]})
		}
		return out
	})
	reads, _ := v.([]carryRead)
	return reads
}

// prevInstr is the instruction before statement i in the same block, -1
// when there is none.
func prevInstr(g *flow.CFG, p *asm.Program, i int) int {
	for j := i - 1; j >= 0; j-- {
		if g.BlockOf[j] != g.BlockOf[i] {
			return -1
		}
		if instr(p, j) != nil {
			return j
		}
	}
	return -1
}

// staleCarry reports the reads inc-no-carry claims: a jcc/setcc/cmovcc
// right after the inc/dec, with no CF writer right before the inc/dec.
func staleCarry(c *lint.Context, g *flow.CFG, r carryRead) bool {
	p := c.Prog
	switch p.Stmts[r.reader].Inst.Category {
	case "COND_BR", "SETCC", "CMOV":
	default:
		return false
	}
	if prevInstr(g, p, r.reader) != r.inc {
		return false
	}
	before := prevInstr(g, p, r.inc)
	return before < 0 || p.Stmts[before].FlagsWritten()&asm.FlagCF == 0
}

func checkPartialFlags(c *lint.Context) {
	g, _ := flowOf(c)
	if g == nil {
		return
	}
	for _, r := range carryReads(c) {
		if staleCarry(c, g, r) {
			continue
		}
		s := &c.Prog.Stmts[r.reader]
		msg := fmt.Sprintf("%s reads CF while the other flags come from %s on line %d: a partial flags merge (a stall on P6-family cores)",
			s.Mnemonic, c.Prog.Stmts[r.inc].Mnemonic, c.Prog.Stmts[r.inc].Line+1)
		reportMnem(c, r.reader, msg)
	}
}

func checkIncNoCarry(c *lint.Context) {
	g, _ := flowOf(c)
	if g == nil {
		return
	}
	for _, r := range carryReads(c) {
		if !staleCarry(c, g, r) {
			continue
		}
		s := &c.Prog.Stmts[r.reader]
		inc := &c.Prog.Stmts[r.inc]
		op := "add"
		if inc.Inst.Name == "dec" {
			op = "sub"
		}
		msg := fmt.Sprintf("%s reads CF, but %s does not set CF: this tests an older carry; use %s $1 to get the carry", s.Mnemonic, inc.Mnemonic, op)
		reportMnem(c, r.reader, msg)
	}
}
