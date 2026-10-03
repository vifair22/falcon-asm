package boot

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Rule 38: the mode-switch sequence, checked along the CFG's paths.
//
// The transitions are found from proven values only: a CR0 write that makes
// PE known set (real -> protected), known clear (back to real mode), PG
// known set (paging; long mode when the document also sets EFER.LME), a
// CR4 write setting PAE, a wrmsr to EFER setting LME. For each, the steps
// that must come before it are checked as "out of order" only when the
// document performs the step after it, on a path from it, and on no path
// before it: code that loads the GDT after setting PE plainly believes it
// has not been loaded yet, whereas a function that sets PE with no lgdt in
// sight may have a caller that did it, and is left alone. The steps that
// must come right after a switch (the far jump, the segment reloads) are
// checked on the straight-line code that follows it.

// maxBetween is how many instructions may run between setting PE and the
// far jump. The processor fetched them under the old CS already; past a
// few, the code is doing work in a half-switched mode.
const maxBetween = 3

// maxReload bounds the walk after a far jump looking for segment reloads.
const maxReload = 40

func (f *facts) loc(i int) []protocol.Location {
	s := &f.p.Stmts[i]
	return []protocol.Location{{URI: f.p.Doc.URI, Range: f.p.Range(s.Start, s.End)}}
}

func (f *facts) report(c *lint.Context, at int, msg string, rel ...int) {
	var locs []protocol.Location
	for _, r := range rel {
		if r >= 0 {
			locs = append(locs, f.loc(r)...)
		}
	}
	s := &f.p.Stmts[at]
	c.ReportFinding(lint.Finding{Range: f.p.Range(s.Start, s.End), Message: msg, Related: locs})
}

// outOfOrder reports at statement at when the step want happens after it
// (and on no path before it).
func (f *facts) outOfOrder(c *lint.Context, at int, want ev, msg string) {
	if f.mayBefore(at, want) {
		return
	}
	if k := f.after(at, want); k >= 0 {
		f.report(c, at, fmt.Sprintf(msg, f.line(k)), k)
	}
}

func checkModeSwitch(c *lint.Context) {
	f := factsOf(c)
	if f == nil || f.any&switchEvents|f.any&evLME == 0 {
		return
	}
	long := f.any&evLME != 0
	f.stmtsOf(evPESet, func(p int) {
		if st := f.res.Before(p); st != nil && st.IF == flow.Set {
			f.report(c, p, "CR0.PE is set with interrupts enabled (IF is set on every path here): an interrupt before the far jump and the IDT load "+
				"goes through the real-mode vector table as if it were protected-mode gates; cli first")
		}
		f.outOfOrder(c, p, evLGDT, "lgdt comes after the CR0.PE write (line %d): the far jump that completes the switch loads CS from whatever GDT was loaded before; load the GDT first")
		f.switchJump(c, p, false)
	})
	f.stmtsOf(evPEClear, func(q int) {
		if m := f.p.Stmts[q].Mode; m != asm.Mode16 {
			f.report(c, q, fmt.Sprintf("CR0.PE is cleared in .code%d code: real mode keeps the code segment's cached size, so leave protected mode from a 16-bit code segment "+
				"(far jump to a 16-bit code selector with a 64 KiB limit first, then clear PE)", m))
		}
		f.switchJump(c, q, true)
	})
	f.stmtsOf(evPGSet, func(g int) {
		f.outOfOrder(c, g, evCR3, "CR3 is loaded at line %d, after paging is enabled here: the first fetch after the CR0 write walks whatever page tables CR3 held; load CR3 first")
		if !long || !f.mayBefore(g, evLME) && f.after(g, evLME) < 0 {
			return
		}
		if st := f.res.Before(g); st != nil {
			if v := st.Get(flow.CR4); v.Mask&(1<<5) != 0 && v.V&(1<<5) == 0 {
				f.report(c, g, "CR4.PAE is known clear when CR0.PG is set for long mode: activating long mode without PAE raises #GP; set CR4.PAE first")
			}
		}
		f.outOfOrder(c, g, evPAESet, "CR4.PAE is set at line %d, after CR0.PG: long mode activates with PG and needs PAE already set (#GP otherwise); set CR4.PAE first")
		f.outOfOrder(c, g, evLME, "EFER.LME is set at line %d, after CR0.PG: changing LME while paging is on raises #GP; set EFER.LME before enabling paging")
		if f.mayBefore(g, evLME) {
			if k, why := f.nextTransfer(g); why == "mode" && f.p.Stmts[k].Mode == asm.Mode64 {
				f.report(c, g, fmt.Sprintf("no far jump after enabling long mode: execution runs on into .code64 code at line %d in compatibility mode, which decodes it as 32-bit code; "+
					"far jump to a 64-bit code selector (L=1) first", f.line(k)), k)
			}
		}
	})
	f.stmtsOf(evLME, func(l int) {
		f.outOfOrder(c, l, evPAESet, "CR4.PAE is set at line %d, after EFER.LME: the long-mode sequence enables PAE, then sets LME, then PG")
	})
}

// switchJump checks the far jump that must follow a PE write (to real mode
// when toReal) and the segment reloads at its target.
func (f *facts) switchJump(c *lint.Context, p int, toReal bool) {
	what := "setting CR0.PE"
	cs := "CS still holds a real-mode segment until a far jump loads a selector"
	if toReal {
		what = "clearing CR0.PE"
		cs = "CS still holds the protected-mode descriptor until a far jump loads a real-mode segment"
	}
	k, why := f.nextTransfer(p)
	switch why {
	case "mode":
		f.report(c, p, fmt.Sprintf("no far jump after %s: execution runs on into .code%d code at line %d; %s", what, f.p.Stmts[k].Mode, f.line(k), cs), k)
		return
	case "near":
		f.report(c, p, fmt.Sprintf("the first control transfer after %s is `%s` (line %d), not a far jump: %s", what, f.p.Stmts[k].Mnemonic, f.line(k), cs), k)
		return
	case "far":
	default:
		return
	}
	if n := f.between(p, k); n > maxBetween {
		f.report(c, p, fmt.Sprintf("%d instructions run between %s and the far jump at line %d: they execute with %s; jump right after the CR0 write", n, what, f.line(k), strings.TrimPrefix(cs, "CS still holds ")), k)
	}
	b := f.g.BlockOf[k]
	if b < 0 || f.g.Blocks[b].Exit != flow.ExitFar || f.g.Blocks[b].Target < 0 {
		return
	}
	t := f.g.Blocks[f.g.Blocks[b].Target].First
	if toReal {
		if tc := f.firstInstr(t); tc >= 0 && f.p.Stmts[tc].Mode != asm.Mode16 {
			f.report(c, k, fmt.Sprintf("the far jump after clearing CR0.PE lands in .code%d code at line %d: real mode runs it as 16-bit code", f.p.Stmts[tc].Mode, f.line(tc)), tc)
		}
	}
	f.checkReloads(c, t, p, k, toReal)
}

// isTransfer reports a statement that leaves straight-line code.
func isTransfer(s *asm.Stmt) bool {
	switch instName(s) {
	case "int", "int3", "into", "hlt", "ud2", "syscall", "sysenter", "sysret", "sysexit":
		return true
	}
	return s.IsBranch() || s.IsRet() || s.IsIret()
}

func isFarTransfer(s *asm.Stmt) bool {
	return s.IsFar() && (s.IsUncondJump() || s.IsCall() || s.IsRet())
}

// passive directives change nothing that is assembled at this place, so
// the walk goes through them.
var passive = map[string]bool{
	".code16": true, ".code32": true, ".code64": true, ".code16gcc": true, ".globl": true, ".global": true,
	".type": true, ".size": true, ".local": true, ".weak": true, ".extern": true, ".hidden": true,
	".p2align": true, ".balign": true, ".align": true, ".loc": true, ".file": true, ".cfi_startproc": true,
	".cfi_endproc": true,
}

// nextTransfer walks the straight-line code after statement p to the first
// control transfer. why is "far", "near" (any other transfer), "mode" (the
// code runs on into statements assembled for another mode before any
// transfer) or "" (data, another section, a macro: nothing to say).
func (f *facts) nextTransfer(p int) (k int, why string) {
	mode := f.p.Stmts[p].Mode
	sec := f.p.Stmts[p].Section
	for k = p + 1; k < len(f.p.Stmts) && k <= p+64; k++ {
		s := &f.p.Stmts[k]
		switch s.Kind {
		case asm.Empty, asm.Label:
			continue
		case asm.Directive:
			if passive[s.Name] {
				continue
			}
			return k, ""
		case asm.Instr:
		default:
			return k, ""
		}
		if s.Section != sec || s.InMacroDef {
			return k, ""
		}
		if s.Mode != mode {
			return k, "mode"
		}
		if isFarTransfer(s) {
			return k, "far"
		}
		if isTransfer(s) {
			return k, "near"
		}
	}
	return -1, ""
}

// between counts the instructions strictly between statements a and b.
func (f *facts) between(a, b int) int {
	n := 0
	for k := a + 1; k < b; k++ {
		if f.p.Stmts[k].Kind == asm.Instr {
			n++
		}
	}
	return n
}

func (f *facts) firstInstr(i int) int {
	for ; i < len(f.p.Stmts); i++ {
		switch f.p.Stmts[i].Kind {
		case asm.Instr:
			return i
		case asm.Empty, asm.Label:
		case asm.Directive:
			if !passive[f.p.Stmts[i].Name] {
				return -1
			}
		default:
			return -1
		}
	}
	return -1
}

// stackOps use SS:SP implicitly.
var stackOps = map[string]bool{
	"push": true, "pop": true, "pushf": true, "popf": true, "pusha": true, "popa": true, "call": true,
	"lcall": true, "ret": true, "lret": true, "int": true, "enter": true, "leave": true, "iret": true,
}

// stringSegs are the data segments a string instruction reads implicitly:
// DS:SI for its source, ES:DI for its destination.
var stringSegs = map[string][]string{
	"movs": {"ds", "es"}, "cmps": {"ds", "es"}, "lods": {"ds"}, "outs": {"ds"}, "stos": {"es"}, "scas": {"es"}, "ins": {"es"},
}

// segLoads names the segment register an instruction loads.
func segLoads(s *asm.Stmt) string {
	switch name := instName(s); name {
	case "mov":
		if len(s.Ops) == 2 && s.Ops[1].Kind == asm.OpReg && s.Ops[1].Reg != nil && s.Ops[1].Reg.Class == "segment" {
			return s.Ops[1].RegName
		}
	case "pop":
		if len(s.Ops) == 1 && s.Ops[0].Kind == asm.OpReg && s.Ops[0].Reg != nil && s.Ops[0].Reg.Class == "segment" {
			return s.Ops[0].RegName
		}
	case "lss", "lds", "les", "lfs", "lgs":
		return name[1:]
	}
	return ""
}

// segUses lists the segment registers an instruction accesses memory
// through (explicit operands, string operands, the stack).
func segUses(s *asm.Stmt) []string {
	name := instName(s)
	var out []string
	if stackOps[name] {
		out = append(out, "ss")
	}
	out = append(out, stringSegs[name]...)
	if name == "lea" || name == "nop" || strings.HasPrefix(name, "prefetch") {
		return out
	}
	for k := range s.Ops {
		op := &s.Ops[k]
		if op.Kind != asm.OpMem {
			continue
		}
		seg := op.Seg
		if seg == "" {
			seg = "ds"
			switch strings.TrimPrefix(op.Base, "%") {
			case "bp", "ebp", "rbp", "sp", "esp", "rsp":
				seg = "ss"
			}
		}
		out = append(out, seg)
	}
	return out
}

// checkReloads walks the straight-line code at a mode switch's far jump
// target t and reports the first use of DS, ES or SS before it is
// reloaded: after the switch each still holds what the old mode put in its
// hidden part (a real-mode base and limit, or a protected-mode descriptor).
func (f *facts) checkReloads(c *lint.Context, t, p, jump int, toReal bool) {
	reloaded := map[string]bool{"cs": true, "fs": true, "gs": true}
	n := 0
	for k := t; k < len(f.p.Stmts) && n < maxReload; k++ {
		s := &f.p.Stmts[k]
		switch s.Kind {
		case asm.Empty, asm.Label:
			continue
		case asm.Directive:
			if passive[s.Name] {
				continue
			}
			return
		case asm.Instr:
		default:
			return
		}
		n++
		if f.ev[k]&(evPESet|evPEClear) != 0 {
			return
		}
		for _, seg := range segUses(s) {
			if reloaded[seg] {
				continue
			}
			reloaded[seg] = true // one finding per register
			f.report(c, k, reloadMsg(seg, toReal, f.line(p), f.line(jump)), p, jump)
		}
		if seg := segLoads(s); seg != "" {
			reloaded[seg] = true
		}
		if isTransfer(s) {
			return
		}
	}
}

func reloadMsg(seg string, toReal bool, pLine, jLine int) string {
	use := "%" + seg + " is used"
	if seg == "ss" {
		use = "the stack is used"
	}
	if toReal {
		return fmt.Sprintf("%s before %%%s is reloaded after the return to real mode (CR0.PE cleared at line %d, far jump at line %d): "+
			"it still holds the protected-mode descriptor's base and limit; load a real-mode segment first", use, seg, pLine, jLine)
	}
	return fmt.Sprintf("%s before %%%s is reloaded after the switch to protected mode (CR0.PE set at line %d, far jump at line %d): "+
		"it still holds a real-mode segment's base and limit; load a data selector first", use, seg, pLine, jLine)
}

func checkA20(c *lint.Context) {
	f := factsOf(c)
	if f == nil || f.any&evPESet == 0 || f.any&evA20 != 0 {
		return
	}
	at := -1
	f.stmtsOf(evPESet, func(p int) {
		// A return trip (a thunk's ascent after its descent) finds A20 as
		// the first ascent left it, and a SIPI trampoline runs after the
		// boot processor set the platform up: A20 is the chipset's.
		if at >= 0 || f.mayBefore(p, evPEClear) || f.trampolineSwitch(c, p) {
			return
		}
		at = p
	})
	if at < 0 {
		return
	}
	target, _, annotated := noteValue(f.p, "a20")
	if !annotated {
		f.report(c, at, "CR0.PE is set, but nothing in this file enables A20 (port 0x92 bit 1, keyboard controller command 0xD1, or INT 15h AX=2401h): "+
			"unless an earlier stage did, every odd megabyte aliases the one below it; if one did, name it with `falcon-asm: a20=<file or function>`, "+
			"and mark a SIPI trampoline `falcon-asm: trampoline` (`trampoline=copied` when it is copied to its page)")
		return
	}
	if ok, why := f.a20Proof(c, target); !ok {
		f.report(c, at, fmt.Sprintf("CR0.PE is set and nothing in this file enables A20, and `falcon-asm: a20=%s` does not prove an earlier stage did: %s", target, why))
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:      "mode-switch-order",
		Section:   5,
		Default:   lint.Warning,
		Langs:     lint.LangAsm,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Mode switch with a step missing or out of order: lgdt, cli, the far jump and segment reloads around CR0.PE; PAE, CR3, EFER.LME and PG for long mode.",
			Why: "Real to protected mode is: interrupts off, lgdt, set CR0.PE, a far jump that loads CS with a code selector as the very next " +
				"control transfer, then DS, ES and SS reloaded before anything uses them. Protected to long mode is CR4.PAE, CR3, EFER.LME, " +
				"then CR0.PG, then a far jump into a 64-bit code segment. Back to real mode is a 16-bit code segment, PE cleared, a far jump " +
				"to real-mode code and real-mode segment loads. None of these mistakes is an error the assembler can see, and each one is a " +
				"triple fault: an instant reset with nothing on the screen. The transitions are found from values the dataflow proves (the " +
				"PE, PG, PAE and LME bits written); a step is out of order when this file performs it after the transition on some path and " +
				"on no path before it, and steps that follow a transition are checked on the code right after it. Unknown values are silent.",
			Bad:  "\t.code16\n\tcli\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n\tlgdt gdt_desc\n\tljmpl $8, $pm\n\t.code32\npm:\n\thlt\ngdt_desc:\t.word 0\n\t.long 0",
			Good: "\t.code16\n\tcli\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n\tljmpl $8, $pm\n\t.code32\npm:\n\thlt\ngdt_desc:\t.word 0\n\t.long 0",
		},
		Check: checkModeSwitch,
	})
	lint.Register(&lint.Rule{
		Code:      "mode-switch-a20",
		Section:   5,
		Default:   lint.Info,
		Langs:     lint.LangAsm,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Switch to protected mode in a file that never enables A20.",
			Why: "With the A20 line masked every odd megabyte of physical memory aliases the one below it, so anything placed at 1 MiB " +
				"lands on the interrupt vector table. The file sets CR0.PE but nothing in it opens A20 (the fast gate, bit 1 of port 0x92; " +
				"the keyboard controller's write-output-port command 0xD1; INT 15h AX=2401h). Information only: an earlier stage may have " +
				"done it, which a single file cannot show. A return to protected mode after a trip to real mode is not reported, and " +
				"neither is a SIPI trampoline (one the sipi rules recognise, or one marked `falcon-asm: trampoline` or " +
				"`falcon-asm: trampoline=copied`): A20 is a chipset gate, not processor state, and an application processor inherits " +
				"it. When an earlier stage opens it, `falcon-asm: a20=<target>` says which: a file relative to this one " +
				"(`a20=../start/pmode.S`), a function in one (`a20=../start/pmode.S:pmode_enter`), or a label of this file or a global " +
				"of a file linked with it (`a20=pmode_enter`). The target is checked: it must itself enable A20 (directly or in a " +
				"function it calls), or the finding stays and says why the annotation does not hold.",
			Bad: "\t.code16\n\tcli\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n\tljmpl $8, $pm\n\t.code32\npm:\n\thlt\ngdt_desc:\t.word 0\n\t.long 0",
			Good: "# pmode.S\n\t.code16\n\t.globl pmode_enter\npmode_enter:\n\tinb $0x92, %al\n\torb $2, %al\n\toutb %al, $0x92\n\tret\n" +
				"# ap.S\n\t.code16\n\t/* falcon-asm: a20=pmode.S */\n\tcli\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n" +
				"\tljmpl $8, $pm\n\t.code32\npm:\n\thlt\ngdt_desc:\t.word 0\n\t.long 0",
		},
		Check: checkA20,
	})
}
