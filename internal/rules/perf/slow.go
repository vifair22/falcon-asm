package perf

import (
	"fmt"
	"math/bits"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Codes of rule 60.
const (
	CodeSlowLoop  = "slow-loop"
	CodeSlowEnter = "slow-enter"
	CodeXchgMem   = "xchg-mem-lock"
	CodeBtMem     = "bt-mem"
	CodeLea3      = "lea-three-component"
	CodeDivConst  = "div-by-constant"
)

func init() {
	lint.Register(&lint.Rule{
		Code:    CodeSlowLoop,
		Section: section,
		Default: lint.Info,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "loop/loope/loopne in 32- or 64-bit code: microcoded on Intel cores; dec + jnz is faster.",
			Why: "Intel cores decode loop into several uops and run it far slower than the dec/jnz pair, which " +
				"macro-fuses into one uop (Agner Fog's instruction tables list loop at about 7 uops on Skylake " +
				"and loope/loopne at more; the Intel optimization manual advises against it). Compilers never " +
				"emit it. In 16-bit code the rule is quiet: there it is usually boot code optimised for size, " +
				"where loop's two bytes matter more than its speed. dec does not preserve the flags that loop " +
				"preserves, so the rewrite is not offered as an automatic fix.",
			Bad:  "1:\taddl (%esi), %eax\n\taddl $4, %esi\n\tloop 1b",
			Good: "1:\taddl (%esi), %eax\n\taddl $4, %esi\n\tdecl %ecx\n\tjnz 1b",
		},
		Check: checkSlowLoop,
	})
	lint.Register(&lint.Rule{
		Code:    CodeSlowEnter,
		Section: section,
		Default: lint.Info,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "enter with a nesting level, or any enter in 64-bit code: microcoded; push/mov/lea is faster.",
			Why: "enter is microcoded on every modern core (Agner Fog's tables list a dozen or more uops for " +
				"level 0 and far more per nesting level), where the push/mov/sub sequence it replaces is three " +
				"simple instructions; leave, by contrast, is cheap and fine to keep. Compilers stopped emitting " +
				"enter decades ago. A nesting level is reported in every mode; level 0 only in 64-bit code, " +
				"where it is most likely hot code rather than size-optimised boot code. The fix uses lea rather " +
				"than sub to set the frame size because enter does not touch the flags.",
			Bad:  "\t.code64\n\tenter $32, $0",
			Good: "\t.code64\n\tpushq %rbp; movq %rsp, %rbp; leaq -32(%rsp), %rsp",
		},
		Check: checkSlowEnter,
	})
	lint.Register(&lint.Rule{
		Code:    CodeXchgMem,
		Section: section,
		Default: lint.Info,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "xchg with a memory operand is implicitly locked: atomic, and as slow as any locked instruction.",
			Why: "xchg with memory asserts LOCK whether or not the prefix is written (Intel SDM Vol. 2, XCHG), " +
				"so it is a full memory barrier costing roughly 20 cycles or more on current cores. That is " +
				"exactly right for a spinlock or a semaphore, and wasteful for a plain swap of a register with " +
				"a variable no other agent touches, which two or three movs do far faster. The rule is a note, " +
				"not a fix: only the author knows whether the atomicity is needed.",
			Bad:  "\txchgl %eax, saved_value",
			Good: "\tmovl saved_value, %edx\n\tmovl %eax, saved_value\n\tmovl %edx, %eax",
		},
		Check: checkXchgMem,
	})
	lint.Register(&lint.Rule{
		Code:    CodeBtMem,
		Section: section,
		Default: lint.Info,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "bt/bts/btr/btc with a register bit offset into memory: bit-string addressing, microcoded and slow.",
			Why: "With a register offset and a memory operand these instructions address a bit string: the " +
				"offset may reach far outside the named operand (Intel SDM Vol. 2, BT, \"bit base\"), so the " +
				"core computes the address in microcode (Agner Fog's tables list around 10 uops for bt m,r " +
				"and more for bts/btr/btc). Loading the word and testing the register form is a handful of " +
				"fast uops; an immediate offset is fine as it is. Make sure the bit-string semantics (an offset " +
				"beyond the operand's width) are not relied on before rewriting.",
			Bad:  "\tbtl %ecx, (%rdi)",
			Good: "\tmovl (%rdi), %eax\n\tbtl %ecx, %eax",
		},
		Check: checkBtMem,
	})
	lint.Register(&lint.Rule{
		Code:    CodeLea3,
		Section: section,
		Default: lint.Info,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "lea with base, index and displacement: 3-cycle latency on Sandy Bridge through Ice Lake.",
			Why: "On Intel cores from Sandy Bridge to Ice Lake a lea with all three components runs on a single " +
				"port with 3-cycle latency, where a two-component lea is one cycle on two ports (Intel " +
				"optimization manual, \"LEA\"; Agner Fog's tables). Splitting it into two leas, or an lea and an " +
				"add, is faster on a critical path. The rule runs only when the target CPU is one of those " +
				"profiles; other cores (Alder Lake and later, AMD Zen) handle it well enough not to bother.",
			Bad:  "# .falcon-asm.json: {\"target\": {\"cpu\": \"skylake\"}}\n\tleaq 8(%rdi,%rsi,4), %rax",
			Good: "# .falcon-asm.json: {\"target\": {\"cpu\": \"skylake\"}}\n\tleaq (%rdi,%rsi,4), %rax\n\taddq $8, %rax",
		},
		Check: checkLea3,
	})
	lint.Register(&lint.Rule{
		Code:      CodeDivConst,
		Section:   section,
		Default:   lint.Hint,
		Langs:     lint.LangAsm | lint.LangInline,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "div/idiv by a register holding a known constant: a shift or a multiply by the reciprocal is faster.",
			Why: "Integer division is the slowest common ALU instruction: tens of cycles of latency for 32 bits " +
				"and more for 64 bits on all but the newest cores (Agner Fog's tables; Ice Lake made 64-bit " +
				"division much faster). When the dataflow proves the divisor constant, an unsigned power of two " +
				"is a shift (and a mask for the remainder), a signed one a shift with a rounding bias, and any " +
				"other constant a multiply by its fixed-point reciprocal (Granlund and Montgomery, \"Division " +
				"by invariant integers using multiplication\", 1994), which is what compilers emit. Hint " +
				"severity: in cold code the division is often clearer. In 16-bit code only powers of two are " +
				"reported, since the reciprocal trick needs a wider multiply than the dividend.",
			Bad:  "\tmovl $16, %ecx\n\txorl %edx, %edx\n\tdivl %ecx",
			Good: "\tmovl %eax, %edx\n\tandl $15, %edx\n\tshrl $4, %eax",
		},
		Check: checkDivConst,
	})
}

func checkSlowLoop(c *lint.Context) {
	p := c.Prog
	if p == nil {
		return
	}
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || s.Mode == asm.Mode16 {
			continue
		}
		switch s.Inst.Name {
		case "loop", "loope", "loopne":
			reportMnem(c, i, s.Mnemonic+" is microcoded and slow on Intel cores; dec plus jnz (which macro-fuse) is faster")
		}
	}
}

func checkSlowEnter(c *lint.Context) {
	p := c.Prog
	if p == nil {
		return
	}
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || s.Inst.Name != "enter" || len(s.Ops) != 2 {
			continue
		}
		size, level := s.Ops[0], s.Ops[1]
		if level.Kind != asm.OpImm || !level.ImmVal.Known {
			continue
		}
		if level.ImmVal.V&0x1F != 0 {
			reportMnem(c, i, fmt.Sprintf("enter with nesting level %d is microcoded and very slow; build the frame with push/mov/sub", level.ImmVal.V))
			continue
		}
		if s.Mode != asm.Mode64 {
			continue
		}
		var fixes []lint.Fix
		if size.Kind == asm.OpImm && size.ImmVal.Known && len(s.Labels) == 0 && len(s.Prefixes) == 0 {
			text := "pushq %rbp; movq %rsp, %rbp"
			if n := size.ImmVal.V & 0xFFFF; n != 0 {
				text += fmt.Sprintf("; leaq -%d(%%rsp), %%rsp", n)
			}
			fixes = append(fixes, lint.NewFix("Replace enter with push/mov/lea", c.ReplaceRange(s.MnemStart, s.End, text)))
		}
		reportMnem(c, i, "enter is microcoded; push %rbp, mov %rsp, %rbp and lea for the frame are faster", fixes...)
	}
}

func checkXchgMem(c *lint.Context) {
	p := c.Prog
	if p == nil {
		return
	}
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || s.Inst.Name != "xchg" || len(s.Ops) != 2 {
			continue
		}
		if s.Ops[0].Kind == asm.OpMem || s.Ops[1].Kind == asm.OpMem {
			reportMnem(c, i, "xchg with memory is implicitly locked: atomic and slow (a full barrier); use movs if atomicity is not needed")
		}
	}
}

func checkBtMem(c *lint.Context) {
	p := c.Prog
	if p == nil {
		return
	}
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || len(s.Ops) != 2 {
			continue
		}
		switch s.Inst.Name {
		case "bt", "bts", "btr", "btc":
		default:
			continue
		}
		if s.Ops[0].Kind == asm.OpReg && s.Ops[1].Kind == asm.OpMem {
			reportMnem(c, i, s.Mnemonic+" with a register offset into memory uses bit-string addressing and is microcoded; load the value and test the register")
		}
	}
}

// lea3Slow lists the profiles with the slow three-component lea.
var lea3Slow = map[string]bool{
	"sandybridge": true, "ivybridge": true, "haswell": true, "broadwell": true,
	"skylake": true, "skylake-avx512": true, "icelake": true, "icelake-server": true,
}

func checkLea3(c *lint.Context) {
	name, known := profileName(c)
	if !known || !lea3Slow[name] || c.Prog == nil {
		return
	}
	p := c.Prog
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || s.Inst.Name != "lea" || len(s.Ops) != 2 {
			continue
		}
		m := &s.Ops[0]
		if m.Kind != asm.OpMem || m.Base == "" || m.Index == "" || m.Disp == "" ||
			m.DispVal.Known && m.DispVal.V == 0 {
			continue
		}
		reportMnem(c, i, fmt.Sprintf("lea with base, index and displacement has 3-cycle latency on %s; split it (lea without the displacement, then add)", name))
	}
}

func checkDivConst(c *lint.Context) {
	_, res := flowOf(c)
	if res == nil {
		return
	}
	p := c.Prog
	for i := range p.Stmts {
		s := instr(p, i)
		if s == nil || len(s.Ops) != 1 || s.Ops[0].Kind != asm.OpReg || s.Ops[0].Reg == nil {
			continue
		}
		name := s.Inst.Name
		if name != "div" && name != "idiv" {
			continue
		}
		d, ok := res.KnownAt(i, s.Ops[0].Reg.Name)
		if !ok || d == 0 || name == "idiv" && negative(d, s.Ops[0].Reg.Width) {
			// A negative signed divisor is rare enough not to advise on.
			continue
		}
		var msg string
		switch {
		case d&(d-1) == 0:
			k := bits.TrailingZeros64(d)
			if name == "div" {
				msg = fmt.Sprintf("division by the constant %d: shift right by %d (and mask with %d for the remainder) instead", d, k, d-1)
			} else {
				msg = fmt.Sprintf("signed division by the constant %d: an arithmetic shift by %d with a rounding bias for negative dividends is much faster", d, k)
			}
		case s.Mode == asm.Mode16:
			continue
		default:
			msg = fmt.Sprintf("division by the constant %d: a multiply by its fixed-point reciprocal (what compilers emit) is much faster", d)
		}
		reportMnem(c, i, msg)
	}
}

// negative reports whether the w-bit value d has its sign bit set.
func negative(d uint64, w int) bool {
	if w <= 0 || w > 64 {
		return false
	}
	return d>>uint(w-1)&1 != 0
}
