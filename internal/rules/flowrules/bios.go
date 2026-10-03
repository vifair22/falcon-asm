package flowrules

import (
	"fmt"
	"slices"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeIFCritical,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "BIOS call inside a cli section that returns with interrupts enabled before a mode switch (INT 15h ends with sti).",
			Why: "A BIOS handler is free to enable interrupts, and several do whatever flags the caller had: " +
				"SeaBIOS ends its INT 15h handler with sti, and INT 13h and INT 16h enable interrupts while " +
				"they wait for the hardware. Code that executes cli, calls one of them and then goes on to " +
				"lgdt, a CR0 write or a far jump into protected mode runs that sequence with interrupts on: " +
				"a timer tick in the middle lands in a real-mode handler with half-switched state and the " +
				"machine triple-faults or wanders into the ROM. The rule reports the int when interrupts " +
				"were disabled before it (on every path) and a mode-switch step is reached after it with no " +
				"cli in between. The fix repeats cli after the int.",
			Bad:  "\tcli\n\tmovw $0x2401, %ax\n\tint $0x15\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0",
			Good: "\tcli\n\tmovw $0x2401, %ax\n\tint $0x15\n\tcli\n\tlgdt gdt_desc\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0",
		},
		Check: checkIFCritical,
	})
	lint.Register(&lint.Rule{
		Code:      CodeBIOSCFUnchecked,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "BIOS call that reports errors in CF whose CF is overwritten before anything tests it.",
			Why: "INT 13h, most of INT 15h and the PCI BIOS report failure by returning with CF set (and a " +
				"status in AH). Ignoring it turns a failed disk read into a jump into whatever the buffer " +
				"held. The rule finds the service from AH (and AL) at the int, and follows every path after " +
				"it: a jc/jnc/jb/jae, adc, sbb, setc, pushf or anything else reading CF checks it; an " +
				"instruction that writes CF first (cmp, add, clc, another int) loses it. A path that " +
				"returns hands CF to the caller, so the call sites in the document are checked the same " +
				"way. A call on the path, or a service the dataflow cannot name, stops the check.",
			Bad:  "\tmovw $0x0201, %ax\n\tint $0x13\n\tcmpw $0xAA55, 0x7DFE\n\tjne bad",
			Good: "\tmovw $0x0201, %ax\n\tint $0x13\n\tjc disk_error\n\tcmpw $0xAA55, 0x7DFE\n\tjne bad",
		},
		Check: checkCFUnchecked,
	})
	lint.Register(&lint.Rule{
		Code:      CodeBIOSClobber,
		Section:   sectionDataflow,
		Default:   lint.Warning,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Register a BIOS service is documented to destroy, read after the int without being reloaded.",
			Why: "Some services change registers beyond their outputs: INT 10h AH=00h returns the mode " +
				"byte in AL and leaves AH undefined, INT 13h AH=08h loads ES:DI with a table pointer and " +
				"clobbers AL, INT 15h AH=87h destroys AX. The rule names the service from AH (and AL) at " +
				"the int and reports a read of a documented clobber that every path reaches without a " +
				"write in between. Outputs are not clobbers: reading CX after INT 13h AH=08h is the point " +
				"of the call. A push is not a use, and a register saved before the int and popped after it " +
				"is its old self again.",
			Bad:  "\tmovw $0x0003, %ax\n\tint $0x10\n\tmovb %ah, %bl",
			Good: "\tmovw $0x0003, %ax\n\tint $0x10\n\tmovb $0x0F, %ah\n\tint $0x10\n\tmovb %ah, %bl",
		},
		Check: func(c *lint.Context) { checkClobbers(c, false) },
	})
	lint.Register(&lint.Rule{
		Code:      CodeBIOSClobberQuirk,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Register some BIOSes destroy across a call although no reference says so (BP over INT 10h), read after the int.",
			Why: "Old video BIOSes destroy BP during scroll and teletype output, some E820 implementations " +
				"change EDX. No specification allows it, but the machines exist and boot code written for " +
				"them saves the register or reloads it. Reported like bios-clobber, at Information, " +
				"because QEMU and modern firmware preserve the register and the code works there.",
			Bad:  "\tmovw $0x0E41, %ax\n\tint $0x10\n\tmovw (%bp), %si",
			Good: "\tpushw %bp\n\tmovw $0x0E41, %ax\n\tint $0x10\n\tpopw %bp\n\tmovw (%bp), %si",
		},
		Check: func(c *lint.Context) { checkClobbers(c, true) },
	})
}

// enablesInterrupts says why a BIOS call may return with IF set, "" when
// the table does not say it does.
func enablesInterrupts(call biosCall) string {
	if call.n == 0x15 {
		return "SeaBIOS ends its INT 15h handler with sti"
	}
	if call.svc != nil {
		if t := strings.ToLower(call.svc.IF); strings.Contains(t, "enables interrupts") {
			return "it " + t
		}
	}
	if call.vec != nil {
		for _, q := range call.vec.Quirks {
			l := strings.ToLower(q)
			if strings.Contains(l, "enables interrupts") || strings.Contains(l, "interrupts enabled") {
				return strings.TrimSuffix(strings.TrimSpace(l), ".")
			}
		}
	}
	return ""
}

func checkIFCritical(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, res := f.p, f.res
	for _, i := range sortedInts(f) {
		call := f.ints[i]
		why := enablesInterrupts(call)
		if why == "" || res.In[i].IF != flow.Clear {
			continue
		}
		at, ok := searchFrom(f.g, i, func(j int) verdict {
			st := &p.Stmts[j]
			switch {
			case instName(st) == "cli", instName(st) == "sti", st.IsCall(), st.IsRet(), st.IsIret(), st.Kind == asm.MacroCall:
				return done
			case isModeSwitch(st):
				return found
			}
			return keep
		})
		if !ok {
			continue
		}
		msg := fmt.Sprintf("%s returns with interrupts enabled (%s); repeat cli after it: %s at line %d runs with interrupts on",
			call.title(), why, stmtText(p, at), lineOf(p, at))
		var fixes []lint.Fix
		if e, ok := insertLineBelow(c, p, i, "cli"); ok {
			fixes = append(fixes, lint.NewFix("Insert cli after the int", e))
		}
		c.ReportStmt(i, msg, fixes...)
	}
}

// sortedInts lists the resolved int statements in source order, so
// findings come out deterministically.
func sortedInts(f *facts) []int {
	out := make([]int, 0, len(f.ints))
	for i := range f.p.Stmts {
		if _, ok := f.ints[i]; ok {
			out = append(out, i)
		}
	}
	return out
}

func checkCFUnchecked(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p := f.p
	for _, i := range sortedInts(f) {
		call := f.ints[i]
		if call.svc == nil || call.svc.Err != hwdb.ErrCF && call.svc.Err != hwdb.ErrCFAH || advisory(call) {
			continue
		}
		at, caller, ok := cfLost(f, i, true)
		if !ok {
			continue
		}
		var msg string
		if caller >= 0 {
			msg = fmt.Sprintf("%s reports errors in CF, which this function returns to its caller, and the call at line %d does not test it before %s at line %d overwrites it",
				call.title(), lineOf(p, caller), p.Stmts[at].Mnemonic, lineOf(p, at))
		} else {
			msg = fmt.Sprintf("%s reports errors in CF, but %s at line %d overwrites CF before anything tests it", call.title(), stmtText(p, at), lineOf(p, at))
		}
		c.ReportStmt(i, msg)
	}
}

// advisory reports the services whose failure code customarily ignores
// because the next step finds out anyway: a disk reset between retries
// (the retried read reports), and the BIOS A20 enable (the wraparound test
// or the fast gate that follows decides). Reporting them would teach users
// to suppress the rule.
func advisory(call biosCall) bool {
	s := call.svc
	if s.AH == nil {
		return false
	}
	switch {
	case call.n == 0x13 && *s.AH == 0x00:
		return true
	case call.n == 0x15 && *s.AH == 0x24 && s.AL != nil && *s.AL == 0x01:
		return true
	}
	return false
}

// cfLost follows the paths after statement from and returns the first
// statement that writes CF before anything reads it. With viaRet, a path
// that returns continues at every call site of the function in the
// document (one level, without following their returns), and caller names
// the call whose path lost CF; -1 when the int's own function did.
func cfLost(f *facts, from int, viaRet bool) (at, caller int, ok bool) {
	p, g := f.p, f.g
	returns := false
	at, ok = searchFrom(g, from, func(j int) verdict {
		st := &p.Stmts[j]
		if st.Kind == asm.MacroCall || st.Kind == asm.Instr && st.Inst == nil && st.Mnemonic != "" {
			return done
		}
		if st.Kind != asm.Instr || st.Inst == nil {
			return keep
		}
		switch {
		case flagsRead(st)&asm.FlagCF != 0:
			return done
		case st.IsCall(), st.IsIret():
			return done
		case st.IsRet():
			returns = true
			return done
		case st.Inst.Name == "int" || flagsWritten(st)&asm.FlagCF != 0:
			return found
		}
		return keep
	})
	if ok || !viaRet || !returns {
		return at, -1, ok
	}
	b := g.BlockOf[from]
	if b < 0 || g.FuncOf[b] < 0 {
		return -1, -1, false
	}
	entry := g.Funcs[g.FuncOf[b]].Entry
	for k := range g.Blocks {
		if g.Blocks[k].Callee != entry || g.Blocks[k].Exit != flow.ExitCall {
			continue
		}
		site := g.Blocks[k].Last
		if !f.res.Reachable(site) {
			continue
		}
		if at, _, ok := cfLost(f, site, false); ok {
			return at, site, true
		}
	}
	return -1, -1, false
}

// clobberMasks are the bits of each register family a service destroys:
// documented ones (Clobbers) and those some BIOSes destroy (MayClobber, of
// the service and of its vector), outputs removed from both.
type clobberMasks struct {
	doc, quirk map[flow.Reg]uint64
}

func masksOf(call biosCall) clobberMasks {
	m := clobberMasks{doc: map[flow.Reg]uint64{}, quirk: map[flow.Reg]uint64{}}
	if call.svc == nil {
		return m
	}
	out := map[flow.Reg]uint64{}
	addAll := func(dst map[flow.Reg]uint64, names []string) {
		for _, n := range names {
			for _, r := range biosRegs(n) {
				fr, lo, hi, ok := flow.RegOf(r)
				if ok && fr.HasValue() {
					dst[fr] |= bitRange(lo, hi)
				}
			}
		}
	}
	for _, o := range call.svc.Out {
		addAll(out, []string{o[0]})
	}
	addAll(m.doc, call.svc.Clobbers)
	addAll(m.quirk, call.svc.MayClobber)
	if call.vec != nil {
		addAll(m.quirk, call.vec.MayClobber)
	}
	for r, bits := range m.doc {
		m.doc[r] = bits &^ out[r]
	}
	for r, bits := range m.quirk {
		m.quirk[r] = bits &^ out[r] &^ m.doc[r]
	}
	return m
}

// biosRegs splits a BIOS table register field ("ES:DI buffer", "CX:DX",
// "AL") into lowercase register names; words that are not registers are
// dropped by the caller's RegOf.
func biosRegs(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == ':' || r == ',' || r == ' ' || r == '/'
	})
}

func bitRange(lo, hi int) uint64 {
	w := hi - lo + 1
	if w >= 64 {
		return ^uint64(0)
	}
	return (uint64(1)<<uint(w) - 1) << uint(lo)
}

func checkClobbers(c *lint.Context, quirk bool) {
	f := factsOf(c)
	if f == nil || len(f.ints) == 0 {
		return
	}
	p, res := f.p, f.res
	// Only the ints whose service destroys something can be blamed; a
	// statement is looked at only when one of them last wrote one of its
	// registers, which keeps the scan to a few array reads per statement.
	masks := map[int]clobberMasks{}
	for at, call := range f.ints {
		if call.svc == nil {
			continue
		}
		m := masksOf(call)
		if quirk && len(m.quirk) > 0 || !quirk && len(m.doc) > 0 {
			masks[at] = m
		}
	}
	if len(masks) == 0 {
		return
	}
	isBlamed := make([]bool, len(p.Stmts))
	var fams []flow.Reg
	for at, m := range masks {
		isBlamed[at] = true
		set := m.doc
		if quirk {
			set = m.quirk
		}
		for r := range set {
			if !slices.Contains(fams, r) {
				fams = append(fams, r)
			}
		}
	}
	type key struct {
		at int
		r  flow.Reg
	}
	done := map[key]bool{}
	var buf []regRead
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.Inst.Name == "push" || !res.Reachable(i) {
			continue
		}
		blamed := false
		for _, r := range fams {
			if d := res.In[i].Get(r).Def; d >= 0 && isBlamed[d] {
				blamed = true
				break
			}
		}
		if !blamed {
			continue
		}
		buf = appendReads(buf[:0], p, i)
		for _, u := range buf {
			d := res.In[i].Get(u.r).Def
			m, ok := masks[d]
			if !ok || done[key{d, u.r}] {
				continue
			}
			call := f.ints[d]
			bits := m.doc[u.r]
			if quirk {
				bits = m.quirk[u.r]
			}
			if u.bits()&bits == 0 {
				continue
			}
			done[key{d, u.r}] = true
			name := "%" + u.name()
			var msg string
			if quirk {
				msg = fmt.Sprintf("%s is read after %s (line %d) without being reloaded; some BIOSes destroy it across this call", name, call.title(), lineOf(p, d))
			} else {
				msg = fmt.Sprintf("%s is read after %s (line %d) without being reloaded, but the service destroys it", name, call.title(), lineOf(p, d))
			}
			if u.op >= 0 {
				c.ReportOp(i, u.op, msg)
			} else {
				c.ReportStmt(i, msg)
			}
		}
	}
}

// insertLineBelow is an edit adding text as a new line after the line of
// statement i, indented like it. ok is false when another statement
// shares the line: the new line would land after it, not after i.
func insertLineBelow(c *lint.Context, p *asm.Program, i int, text string) (e protocol.TextEdit, ok bool) {
	if i+1 < len(p.Stmts) && p.Stmts[i+1].Line == p.Stmts[i].EndLine && p.Stmts[i+1].Kind != asm.Cpp {
		return e, false
	}
	src := p.Doc.Text
	end := p.Stmts[i].End
	nl := strings.IndexByte(src[end:], '\n')
	if nl < 0 {
		return e, false
	}
	lineStart := end + nl + 1
	ls := p.Stmts[i].Start
	for ls > 0 && src[ls-1] != '\n' {
		ls--
	}
	ind := ls
	for ind < len(src) && (src[ind] == ' ' || src[ind] == '\t') {
		ind++
	}
	indent := src[ls:ind]
	if ind == p.Stmts[i].Start && len(p.Stmts[i].Labels) > 0 {
		// "1:\tint $0x15": indent the new line like code, not like the label.
		indent = "\t"
	}
	eol := "\n"
	if lineStart >= 2 && src[lineStart-2] == '\r' {
		eol = "\r\n"
	}
	return c.ReplaceRange(lineStart, lineStart, indent+text+eol), true
}
