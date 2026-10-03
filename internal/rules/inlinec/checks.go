package inlinec

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// ---- asm-operand-ref --------------------------------------------------

func checkOperandRefs(c *lint.Context) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip {
			continue
		}
		st := m.st
		n := len(st.Outputs) + len(st.Inputs)
		total := n
		if st.Goto {
			total += len(st.Labels)
		}
		for _, r := range m.refs {
			end := r.Offset + r.Len
			refText := st.Asm[r.Offset:end]
			switch {
			case r.Name != "" && r.Index < 0:
				reportAsm(c, m, r.Offset, end, fmt.Sprintf("%s names no operand%s", refText, knownNames(st)))
				continue
			case r.Index >= total:
				msg := fmt.Sprintf("%s refers to operand %d, but the statement has %d %s",
					refText, r.Index, total, plural(total, "operand", "operands"))
				var fixes []lint.Fix
				if _, _, isReg := family(refText[1:]); isReg && r.Modifier != 0 {
					msg += fmt.Sprintf("; a register is written %%%s in extended asm", refText)
					if cs, _ := asmToC(st, r.Offset, end); cs < len(text) && text[cs] == '%' {
						fixes = append(fixes, fixFrom(c, "Write the register as %"+refText, cEdit{cs, cs, "%"}))
					}
				}
				reportAsm(c, m, r.Offset, end, msg, fixes...)
				continue
			}
			switch {
			case r.Modifier == 'l' && !st.Goto:
				reportAsm(c, m, r.Offset, end, fmt.Sprintf("%s: the %%l modifier names a goto label, and this is not an asm goto", refText))
			case r.Modifier == 'l' && r.Index < n:
				reportAsm(c, m, r.Offset, end, fmt.Sprintf("%s: %%l takes a goto label, but operand %d is an %s",
					refText, r.Index, map[bool]string{true: "output", false: "input"}[r.Index < len(st.Outputs)]))
			case r.Modifier != 0 && inlineasm.ModifierHover(r.Modifier) == "":
				reportAsm(c, m, r.Offset, end, fmt.Sprintf("%s: '%c' is not an x86 operand modifier", refText, r.Modifier))
			}
		}
	}
}

func knownNames(st *inlineasm.Stmt) string {
	var names []string
	for _, o := range append(append([]inlineasm.Operand(nil), st.Outputs...), st.Inputs...) {
		if o.Name != "" {
			names = append(names, o.Name)
		}
	}
	for _, l := range st.Labels {
		names = append(names, l.Name)
	}
	if len(names) == 0 {
		return " (no operand has a name)"
	}
	return " (named: " + strings.Join(names, ", ") + ")"
}

// ---- asm-missing-clobber ----------------------------------------------

func checkMissingClobber(c *lint.Context) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip {
			continue
		}
		f := m.getFacts()
		var missing []*regWrite
		for _, fam := range f.order {
			w := m.firstObservable(f, fam)
			if w == nil || fam == "rsp" || hasClobber(m.st, fam) || m.boundOutput(fam) >= 0 || !existsIn(fam, m.mode) {
				continue
			}
			if k := m.boundInput(fam); k >= 0 {
				reportAsm(c, m, w.start, w.end, fmt.Sprintf("%s %%%s, which is input operand %s; GCC assumes inputs are "+
					"unchanged and a clobber cannot overlap an operand, so make it an in/out operand (\"+%s\")",
					writesWord(w), displayName(fam, m.mode), m.opName(k), constraintLetter(fam)))
				continue
			}
			missing = append(missing, w)
		}
		if len(missing) == 0 {
			continue
		}
		style := styleOf(m.st.Clobbers, m.mode)
		names := make([]string, len(missing))
		for i, w := range missing {
			names[i] = style.clobberName(w.fam)
		}
		allEdit, okAll := clobberEdit(text, m.st, names)
		for i, w := range missing {
			msg := fmt.Sprintf("%s %%%s, but it is neither an output nor in the clobber list; GCC may keep a live value there",
				writesWord(w), displayName(w.fam, m.mode))
			var fixes []lint.Fix
			if okAll {
				fixes = append(fixes, fixFrom(c, "Add "+plural(len(names), "clobber ", "clobbers ")+quoteList(names), allEdit))
				if len(names) > 1 {
					if one, ok := clobberEdit(text, m.st, names[i:i+1]); ok {
						fixes = append(fixes, fixFrom(c, "Add clobber "+quoteList(names[i:i+1]), one))
					}
				}
			}
			reportAsm(c, m, w.start, w.end, msg, fixes...)
		}
	}
}

func writesWord(w *regWrite) string {
	if w.implicit {
		return w.mnem + " implicitly writes"
	}
	return w.mnem + " writes"
}

// constraintLetter is the fixed constraint naming fam, for messages.
func constraintLetter(fam string) string {
	switch fam {
	case "rax":
		return "a"
	case "rbx":
		return "b"
	case "rcx":
		return "c"
	case "rdx":
		return "d"
	case "rsi":
		return "S"
	case "rdi":
		return "D"
	}
	return "r"
}

// ---- asm-missing-memory-clobber ---------------------------------------

func checkMissingMemory(c *lint.Context) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip || hasClobber(m.st, "memory") || m.hasMemoryOutput() {
			continue
		}
		f := m.getFacts()
		for _, mw := range f.mem {
			if mw.described || mw.seg || m.unobservable(mw.insn) {
				continue
			}
			in := &m.insns[mw.insn]
			var msg string
			switch {
			case mw.input >= 0:
				msg = fmt.Sprintf("%s stores to input operand %s, which GCC assumes the asm only reads, and \"memory\" is not clobbered",
					in.mnem, m.opName(mw.input))
			case mw.start == in.mnemStart:
				msg = fmt.Sprintf("%s stores through %%%s, which is not a memory operand, and \"memory\" is not clobbered; "+
					"GCC may keep the old contents cached across the asm", in.mnem, displayName("rdi", m.mode))
			default:
				msg = fmt.Sprintf("%s stores to %s, which is not a memory operand, and \"memory\" is not clobbered; "+
					"GCC may keep the old contents cached across the asm", in.mnem, m.st.Asm[mw.start:mw.end])
			}
			var fixes []lint.Fix
			if e, ok := clobberEdit(text, m.st, []string{"memory"}); ok {
				fixes = append(fixes, fixFrom(c, `Add clobber "memory"`, e))
			}
			reportAsm(c, m, mw.start, mw.end, msg, fixes...)
			break
		}
	}
}

// hasMemoryOutput reports an output operand that may be memory. Such an
// operand usually names the object the template stores to ("=m"(*p) next
// to "r"(p)), and the address cannot be matched to it here, so the memory
// rule stays quiet.
func (m *stmtModel) hasMemoryOutput() bool {
	for i := range m.ops {
		if m.ops[i].out && m.ops[i].mem {
			return true
		}
	}
	return false
}

// ---- asm-early-clobber ------------------------------------------------

func checkEarlyClobber(c *lint.Context) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip {
			continue
		}
		f := m.getFacts()
		for k := range m.st.Outputs {
			o := &m.ops[k]
			if o.rw || o.early || o.flag || !o.regLike() {
				continue
			}
			w := m.firstOutputWrite(f, k)
			if w < 0 {
				continue
			}
			j, r := m.laterInputRead(f, k, w)
			if j < 0 {
				continue
			}
			op := &m.st.Outputs[k]
			msg := fmt.Sprintf("output %s is written by %s before input %s is read by %s; without '&' GCC may give both the same register",
				m.opName(k), m.insns[w].mnem, m.opName(j), m.insns[r].mnem)
			var fixes []lint.Fix
			if e, ok := earlyClobberEdit(text, op); ok {
				fixes = append(fixes, fixFrom(c, "Mark "+m.opName(k)+" early-clobber (&)", e))
			}
			c.ReportRange(op.ConstraintSpan.Start, op.ConstraintSpan.End, msg, fixes...)
		}
	}
}

// firstOutputWrite is the first instruction writing output k: through its
// reference, or, for a fixed-register output, through that register
// (cpuid writing "=a"). -1 when the template never writes it.
func (m *stmtModel) firstOutputWrite(f *stmtFacts, k int) int {
	w := -1
	for _, a := range f.access[k] {
		if a.write {
			w = a.insn
			break
		}
	}
	for fam := range m.ops[k].fixed {
		if i, ok := f.first[fam]; ok && (w < 0 || i < w) {
			w = i
		}
	}
	return w
}

// laterInputRead finds an input that may share output k's register and is
// read after instruction w on a path that does not leave the template
// first. It returns the input and the reading instruction, or -1.
func (m *stmtModel) laterInputRead(f *stmtFacts, k, w int) (int, int) {
	o := &m.ops[k]
	for j := len(m.st.Outputs); j < len(m.ops); j++ {
		in := &m.ops[j]
		if in.tiedTo >= 0 || in.immOnly() || !canShare(o, in, m.mode) {
			continue
		}
		for _, a := range f.access[j] {
			if !a.read || a.insn <= w {
				continue
			}
			if m.leavesBetween(w, a.insn) {
				break
			}
			if m.onlyCopiesBefore(f, k, j, a.insn) {
				continue
			}
			return j, a.insn
		}
	}
	return -1, -1
}

// onlyCopiesBefore reports that every write of output k before
// instruction r copies input j into it ("movl %1, %0"). Sharing a register
// with j is then harmless up to r: the copy leaves j's value where it was.
func (m *stmtModel) onlyCopiesBefore(f *stmtFacts, k, j, r int) bool {
	for fam := range m.ops[k].fixed {
		if i, ok := f.first[fam]; ok && i < r {
			return false
		}
	}
	for _, a := range f.access[k] {
		if !a.write || a.insn >= r {
			continue
		}
		in := &m.insns[a.insn]
		if in.name != "mov" || len(in.ops) != 2 || in.ops[0].kind != oRef || in.ops[0].ref != j {
			return false
		}
	}
	return true
}

// leavesBetween reports an unconditional jump or return in [from, to).
func (m *stmtModel) leavesBetween(from, to int) bool {
	for i := from; i < to && i < len(m.insns); i++ {
		if m.insns[i].uncond {
			return true
		}
	}
	return false
}

// canShare reports whether GCC may give output o and input i the same
// register (or put i's address in o's register).
func canShare(o, i *opInfo, mode int) bool {
	if o.anyReg || i.anyReg {
		return true
	}
	if len(o.fixed) > 0 && len(i.fixed) > 0 {
		// Two fixed registers either differ or are the same on purpose.
		return false
	}
	for fam := range i.fixed {
		if o.regOK(fam, mode) {
			return true
		}
	}
	for fam := range o.fixed {
		if i.regOK(fam, mode) || i.mem && isGPR(fam) {
			return true
		}
	}
	oGPR := o.gpr || o.byteQ || o.highQ
	iGPR := i.gpr || i.byteQ || i.highQ
	return oGPR && (iGPR || i.mem) || o.vec && i.vec || o.mask && i.mask
}

// ---- asm-hardcoded-reg ------------------------------------------------

func checkHardcodedReg(c *lint.Context) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip {
			continue
		}
		f := m.getFacts()
		seen := regSet{}
		for _, rd := range f.reads {
			fam := rd.fam
			if seen[fam] || fam == "rsp" || fam == "rbp" || f.written[fam] || hasClobber(m.st, fam) ||
				!existsIn(fam, m.mode) {
				continue
			}
			seen.add(fam)
			bound, chooser := false, -1
			for i := range m.ops {
				switch {
				case m.ops[i].fixed[fam]:
					bound = true
				case chooser < 0 && m.ops[i].regOK(fam, m.mode):
					chooser = i
				}
			}
			if bound || chooser < 0 {
				continue
			}
			op, _ := m.st.Operand(chooser)
			msg := fmt.Sprintf("%%%s is read here, but no constraint binds it and no clobber reserves it; GCC may allocate %s (%q) to it",
				displayName(fam, m.mode), m.opName(chooser), op.Constraint)
			var fixes []lint.Fix
			name := styleOf(m.st.Clobbers, m.mode).clobberName(fam)
			if e, ok := clobberEdit(text, m.st, []string{name}); ok {
				fixes = append(fixes, fixFrom(c, "Add clobber "+quoteList([]string{name}), e))
			}
			reportAsm(c, m, rd.start, rd.end, msg, fixes...)
		}
	}
}

// ---- asm-missing-volatile ---------------------------------------------

// sideEffects are instructions whose effect is not captured by their
// outputs, with the reason a message gives.
var sideEffects = func() map[string]string {
	m := map[string]string{}
	for _, n := range []string{"in", "out", "ins", "outs"} {
		m[n] = "does port I/O"
	}
	for _, n := range []string{"int", "int1", "int3", "into", "syscall", "sysenter", "vmcall", "vmmcall"} {
		m[n] = "calls into the kernel, firmware or hypervisor"
	}
	for _, n := range []string{"rdtsc", "rdtscp", "rdpmc", "rdrand", "rdseed"} {
		m[n] = "returns a different value every time"
	}
	for _, n := range []string{
		"wrmsr", "cli", "sti", "lgdt", "lidt", "lldt", "ltr", "lmsw", "invlpg", "invlpga", "invpcid",
		"wbinvd", "wbnoinvd", "invd", "hlt", "xsetbv", "wrpkru", "swapgs", "wrfsbase", "wrgsbase",
		"clflush", "clflushopt", "clwb", "sysexit", "sysret", "iret", "iretq", "mwait", "monitor", "umwait", "umonitor", "tpause",
		"vmlaunch", "vmresume", "vmwrite", "vmxon", "vmxoff", "vmptrld", "vmclear", "xrstor", "xrstor64",
		"xrstors", "xrstors64", "fxrstor", "fxrstor64", "ldmxcsr", "vldmxcsr", "stac", "clac", "ud2",
		"mfence", "sfence", "lfence", "serialize", "rsm", "popf", "popfd", "popfq", "popfw",
	} {
		m[n] = "changes processor state"
	}
	return m
}()

func checkMissingVolatile(c *lint.Context) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip || m.st.Volatile || len(m.st.Outputs) == 0 {
			continue
		}
		f := m.getFacts()
		at, reason := -1, ""
		bracketed := m.flagsBracketed()
		for i := range m.insns {
			in := &m.insns[i]
			if r := sideEffect(in); r != "" && !(bracketed && strings.HasPrefix(in.name, "popf")) {
				at, reason = i, r
				break
			}
		}
		for _, mw := range f.mem {
			if !mw.described && (at < 0 || mw.insn < at) {
				at, reason = mw.insn, "stores to memory that is not an output"
				break
			}
		}
		if at < 0 {
			continue
		}
		in := &m.insns[at]
		msg := fmt.Sprintf("asm has outputs but is not volatile, and %s %s; GCC may delete it when the outputs are unused, or merge or move it",
			in.mnem, reason)
		var fixes []lint.Fix
		if e, ok := volatileEdit(text, m.st); ok {
			fixes = append(fixes, fixFrom(c, "Make the asm volatile", e))
		}
		reportAsm(c, m, in.mnemStart, in.mnemStart+len(in.mnem), msg, fixes...)
	}
}

// flagsBracketed reports a template that saves the flags first and
// restores them last (pushf ... popf, with the stack balanced): the popf
// in between, as in the classic "can this EFLAGS bit be toggled" probe,
// leaves no state behind.
func (m *stmtModel) flagsBracketed() bool {
	first, last, depth := "", "", 0
	for i := range m.insns {
		n := m.insns[i].name
		switch {
		case strings.HasPrefix(n, "pushf"), n == "push":
			depth++
		case strings.HasPrefix(n, "popf"), n == "pop":
			depth--
		default:
			continue
		}
		if first == "" {
			first = n
		}
		last = n
	}
	return depth == 0 && strings.HasPrefix(first, "pushf") && strings.HasPrefix(last, "popf")
}

func sideEffect(in *insn) string {
	if !in.known {
		return ""
	}
	if r, ok := sideEffects[in.name]; ok {
		return r
	}
	if in.name == "mov" && len(in.ops) == 2 {
		if d := in.ops[1]; d.kind == oReg && d.fam == "" {
			switch t := strings.ToLower(d.text); {
			case strings.HasPrefix(t, "%cr"):
				return "writes a control register"
			case strings.HasPrefix(t, "%dr"), strings.HasPrefix(t, "%db"):
				return "writes a debug register"
			case len(t) == 3 && t[2] == 's':
				return "loads a segment register"
			}
		}
	}
	return ""
}

// ---- asm-operand-width ------------------------------------------------

// sameWidthOps are instructions whose register and memory operands all
// have the instruction's operand size; shifts and rotates except for the
// count.
var sameWidthOps = setOf("mov", "add", "sub", "and", "or", "xor", "cmp", "test", "adc", "sbb", "xchg",
	"xadd", "cmpxchg", "inc", "dec", "neg", "not", "bsf", "bsr", "popcnt", "lzcnt", "tzcnt", "bswap",
	"imul", "mul", "div", "idiv", "bt", "bts", "btr", "btc", "andn", "push", "pop")

var shiftOps = setOf("shl", "shr", "sal", "sar", "rol", "ror", "rcl", "rcr", "shld", "shrd")

func sizedPosition(in *insn, p int) bool {
	switch {
	case in.name == "out" && len(in.ops) == 2:
		return p == 0 // the data; the port is %dx or an immediate
	case in.name == "in" && len(in.ops) == 2:
		return p == 1
	case shiftOps[in.name]:
		return !(len(in.ops) >= 2 && p == 0)
	case strings.HasPrefix(in.name, "cmov"):
		return true
	}
	return sameWidthOps[in.name]
}

// insnWidth is the operand size of the instruction as the template shows
// it: the suffix, or another sized operand's register.
func insnWidth(in *insn, p int) int {
	if in.width != 0 {
		return in.width
	}
	for q := range in.ops {
		if q != p && in.ops[q].kind == oReg && isGPR(in.ops[q].fam) && sizedPosition(in, q) {
			return in.ops[q].width
		}
	}
	return 0
}

func checkOperandWidth(c *lint.Context, heuristic bool) {
	text := docText(c)
	for _, m := range models(c) {
		if m.skip {
			continue
		}
		types := map[int]*cType{}
		typeOfOp := func(k int) *cType {
			if t, ok := types[k]; ok {
				return t
			}
			var tp *cType
			if op, ok := m.st.Operand(k); ok {
				if t, ok := exprType(text, m.st.Start, op.Expr, m.mode, c.Target.CC); ok {
					tp = &t
				}
			}
			types[k] = tp
			return tp
		}
		for i := range m.insns {
			in := &m.insns[i]
			for p := range in.ops {
				op := &in.ops[p]
				if op.kind != oRef || op.ref < 0 || op.ref >= len(m.ops) {
					continue
				}
				info := &m.ops[op.ref]
				gprLike := info.gpr || info.byteQ || info.highQ
				for fam := range info.fixed {
					gprLike = gprLike || isGPR(fam)
				}
				if info.flag || info.anyReg || !gprLike && !info.memOnly() {
					continue
				}
				t := typeOfOp(op.ref)
				if t == nil || t.heuristic != heuristic {
					continue
				}
				m.checkWidth(c, in, p, op, info, t)
			}
		}
	}
}

func (m *stmtModel) checkWidth(c *lint.Context, in *insn, p int, op *tOp, info *opInfo, t *cType) {
	w := t.width
	switch op.mod {
	case 0:
	case 'b', 'h':
		w = 8
	case 'w':
		w = 16
	case 'k':
		w = 32
	case 'q':
		w = 64
	default:
		return
	}
	ref := m.st.Asm[op.start:op.end]
	desc := fmt.Sprintf("%s is %s", m.exprName(op.ref), t.name)
	isReg := !info.memOnly()
	if isReg && op.mod != 0 {
		read := !info.out || info.rw
		switch {
		case t.pointer && w < t.width:
			reportAsm(c, m, op.start, op.end, fmt.Sprintf("%s truncates a pointer to %d bits (%s)", ref, w, desc))
			return
		case !t.pointer && w > t.width && read:
			reportAsm(c, m, op.start, op.end, fmt.Sprintf("%s widens a %d-bit input to %d bits (%s); the upper bits of the register are not defined",
				ref, t.width, w, desc))
			return
		}
	}
	if !sizedPosition(in, p) {
		return
	}
	iw := insnWidth(in, p)
	if iw == 0 || iw == w {
		return
	}
	if isReg {
		reportAsm(c, m, op.start, op.end, fmt.Sprintf("%s takes %d-bit operands, but %s is printed as a %d-bit register (%s)",
			in.mnem, iw, ref, w, desc))
		return
	}
	reportAsm(c, m, op.start, op.end, fmt.Sprintf("%s accesses %d bits, but %s is %d bits (%s)", in.mnem, iw, ref, w, desc))
}

// exprName names an operand's C expression for messages.
func (m *stmtModel) exprName(k int) string {
	if op, ok := m.st.Operand(k); ok {
		return "'" + op.Expr + "'"
	}
	return m.opName(k)
}
