package encoding

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// zeroOpOK are instructions that also exist without operands although
// their table forms all take some: with no operands, movsd and cmpsd are
// the string instructions.
var zeroOpOK = map[string]bool{"movsd": true, "cmpsd": true, "aad": true, "aam": true}

// checkForms matches the statement against the instruction's forms and
// reports through the rule that explains a failure best.
func (st *stmtCtx) checkForms() {
	if skipForms(st.inst) {
		return
	}
	c := formsOf(st.inst, st.apx())
	if len(st.m.ops) == 0 {
		if c.noOps || c.hasEmpty() || c.implicitOnly() || zeroOpOK[st.name] || c.unknown > 0 || st.explained || st.hashImm() {
			return
		}
		st.reportMnem(CodeNoForm, fmt.Sprintf("%s takes operands: %s", st.name, c.list(-1)))
		return
	}
	if c.noOps {
		// Instructions listed without operands often accept their
		// implicit registers spelled out (monitor %rax, %ecx, %edx).
		return
	}
	if exact := st.m.matches(c, 0); len(exact) > 0 {
		st.checkImm(exact)
		st.checkAmbiguous(exact)
		return
	}
	if st.explained || c.unknown > 0 {
		return
	}
	switch {
	case st.m.any(c, relaxDeco):
		st.decoMessage(c)
	case st.mixedVec() && st.m.any(c, relaxVec):
		st.reportMnem(CodeAVXDeco, fmt.Sprintf("%s does not mix vector widths (%s); valid: %s", st.name, st.vecList(), c.list(len(st.m.ops))))
	case st.m.any(c, relaxGPR):
		st.sizeMismatch(c)
	default:
		st.noForm(c)
	}
}

// hashImm reports a '#' comment glued to what follows ("rol.w #8, %0"):
// in m68k and ARM syntax that is an immediate operand, which the x86
// lexer took for a comment, so the operands are not really missing.
func (st *stmtCtx) hashImm() bool {
	src := st.a.prog.Doc.Text
	_, at := st.mnemRange()
	for at < len(src) && src[at] != '\n' {
		if src[at] == '#' {
			return at+1 < len(src) && src[at+1] != ' ' && src[at+1] != '\t' && src[at+1] != '\n'
		}
		at++
	}
	return false
}

// implicitOnly reports an instruction whose every form names fixed
// registers only (vmrun %rax, skinit %eax): the operands are implicit and
// may be left out.
func (c *compiled) implicitOnly() bool {
	for i := range c.forms {
		for _, s := range c.forms[i].slots {
			for _, a := range s.alts {
				if a.kind != akFixed {
					return false
				}
			}
		}
	}
	return len(c.forms) > 0
}

func (c *compiled) hasEmpty() bool {
	for i := range c.forms {
		if len(c.forms[i].slots) == 0 {
			return true
		}
	}
	return false
}

// list renders the distinct form patterns with n operands (every form when
// n < 0), at most four, for messages.
func (c *compiled) list(n int) string {
	var out []string
	seen := map[string]bool{}
	for i := range c.forms {
		f := &c.forms[i]
		if (n >= 0 && len(f.slots) != n) || len(f.slots) == 0 || seen[f.text] {
			continue
		}
		seen[f.text] = true
		out = append(out, f.text)
	}
	if len(out) == 0 {
		counts := map[int]bool{}
		for i := range c.forms {
			counts[len(c.forms[i].slots)] = true
		}
		var ns []string
		for k := range counts {
			ns = append(ns, fmt.Sprint(k))
		}
		sort.Strings(ns)
		return "it takes " + strings.Join(ns, " or ") + " operands"
	}
	if len(out) > 4 {
		out = append(out[:4], "...")
	}
	return strings.Join(out, " | ")
}

// describe names an operand's kind the way the form patterns do.
func describe(o *operand) string {
	switch o.kind {
	case asm.OpReg:
		switch o.reg.Class {
		case "gpr":
			return fmt.Sprintf("r%d", o.reg.Width)
		case "segment":
			return "sreg"
		case "control":
			return "cr"
		case "debug":
			return "dr"
		case "mmx":
			return "mm"
		case "mask":
			return "k"
		case "x87":
			return "st(i)"
		}
		return o.reg.Class
	case asm.OpMem:
		if o.vsib != 0 {
			return "vsib"
		}
		return "m"
	case asm.OpImm:
		return "imm"
	}
	return "?"
}

func (st *stmtCtx) describeOps() string {
	parts := make([]string, len(st.m.ops))
	for j := range st.m.ops {
		parts[j] = describe(&st.m.ops[j])
	}
	return strings.Join(parts, ", ")
}

// noForm reports operands no form accepts, naming what is valid. When
// every form with this many operands wants memory (or a register) where a
// register (or memory) was written, the message says just that:
// "lgdt needs a memory operand (m16&32)".
func (st *stmtCtx) noForm(c *compiled) {
	n := len(st.m.ops)
	var same []*form
	for i := range c.forms {
		if len(c.forms[i].slots) == n {
			same = append(same, &c.forms[i])
		}
	}
	if len(same) == 0 {
		st.reportMnem(CodeNoForm, fmt.Sprintf("%s does not take %d operand%s; %s", st.name, n, plural(n), c.list(-1)))
		return
	}
	for j := range st.m.ops {
		o := &st.m.ops[j]
		if o.kind == asm.OpMem {
			continue
		}
		memOnly := true
		for _, f := range same {
			for _, a := range f.slots[j].alts {
				if a.kind != akMem && a.kind != akVSIB {
					memOnly = false
				}
			}
		}
		if memOnly {
			slotText := strings.TrimSpace(strings.Split(same[0].text, ",")[j])
			st.reportOp(CodeNoForm, j, fmt.Sprintf("%s needs a memory operand here (%s), not %s", st.name, slotText, strings.TrimSpace(o.op.Text)))
			return
		}
	}
	st.reportMnem(CodeNoForm, fmt.Sprintf("no form of %s takes (%s); valid: %s", st.name, st.describeOps(), c.list(n)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sizeMismatch reports operands of the right kinds whose sizes disagree:
// with the suffix, or with each other. A fix changes the suffix when every
// operand-size register agrees on one width that gives a valid form.
func (st *stmtCtx) sizeMismatch(c *compiled) {
	picked := make([]*alt, len(st.m.ops))
	for i := range c.forms {
		if st.m.match(&c.forms[i], relaxGPR, picked) {
			break
		}
	}
	var regs []sizedReg
	for j := range st.m.ops {
		o := &st.m.ops[j]
		if o.kind == asm.OpReg && o.reg.Class == "gpr" && picked[j] != nil && picked[j].opSize {
			regs = append(regs, sizedReg{j, o.reg.Width})
		}
	}
	text := func(r sizedReg) string {
		return fmt.Sprintf("%s (%d-bit)", strings.TrimSpace(st.m.ops[r.j].op.Text), r.w)
	}
	if suf := st.m.suffix; suf != 0 {
		var bad []string
		for _, r := range regs {
			if r.w != suf {
				bad = append(bad, text(r))
			}
		}
		if len(bad) > 0 {
			msg := fmt.Sprintf("the %s suffix of %s means %d-bit operands, but %s", st.suffix, st.mnemonic(), suf, strings.Join(bad, " and "))
			if w, ok := st.agreedWidth(regs); ok {
				m := st.m
				m.suffix = w
				if m.any(c, 0) {
					newSuf := suffixFor(w)
					st.explained = true
					start, end := st.mnemRange()
					st.a.addFix(CodeSizeMismatch, start, end, msg,
						fmt.Sprintf("Use %s", st.mnemonic()[:len(st.mnemonic())-1]+newSuf), st.suffixEdit(newSuf))
					return
				}
			}
			st.reportMnem(CodeSizeMismatch, msg)
			return
		}
	} else if _, ok := st.agreedWidth(regs); !ok && len(regs) > 1 && !c.mixedWidths() {
		parts := make([]string, len(regs))
		for k, r := range regs {
			parts[k] = text(r)
		}
		st.reportMnem(CodeSizeMismatch, fmt.Sprintf("%s differ in size; %s needs operands of one size", strings.Join(parts, " and "), st.name))
		return
	}
	st.reportMnem(CodeSizeMismatch, fmt.Sprintf("no form of %s takes these operand sizes; valid: %s", st.mnemonic(), c.list(len(st.m.ops))))
}

// mixedWidths reports an instruction with a form whose general register
// operands legitimately differ in width (movzbl, crc32b, lsl).
func (c *compiled) mixedWidths() bool {
	for i := range c.forms {
		w := int16(0)
		for _, s := range c.forms[i].slots {
			for _, a := range s.alts {
				if a.kind != akGPR {
					continue
				}
				if w != 0 && a.width != w {
					return true
				}
				w = a.width
			}
		}
	}
	return false
}

// sizedReg is a general register operand j whose width w is the operand
// size.
type sizedReg struct{ j, w int }

// agreedWidth is the one width every register in regs has.
func (st *stmtCtx) agreedWidth(regs []sizedReg) (int, bool) {
	if len(regs) == 0 {
		return 0, false
	}
	w := regs[0].w
	for _, r := range regs[1:] {
		if r.w != w {
			return 0, false
		}
	}
	return w, true
}

// mixedVec reports xmm, ymm and zmm registers (or vector indexes) of
// different widths in one statement.
func (st *stmtCtx) mixedVec() bool {
	w := int16(0)
	for j := range st.m.ops {
		o := &st.m.ops[j]
		var v int16
		if o.kind == asm.OpReg {
			v = vecClass[o.reg.Class]
		}
		if v == 0 {
			continue
		}
		if w != 0 && v != w {
			return true
		}
		w = v
	}
	return false
}

func (st *stmtCtx) vecList() string {
	var parts []string
	for j := range st.m.ops {
		o := &st.m.ops[j]
		if o.kind == asm.OpReg {
			if _, ok := vecClass[o.reg.Class]; ok {
				parts = append(parts, regText(o.reg))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// decoMessage explains a statement that matches once decorations are
// ignored: a mask away from the destination, zeroing the instruction does
// not support, a missing gather/scatter mask, or a broadcast count that
// does not fill the vector.
func (st *stmtCtx) decoMessage(c *compiled) {
	last := len(st.m.ops) - 1
	for j := range st.m.ops {
		if op := st.m.ops[j].op; op.Mask != "" && j != last && !st.slotAllows(c, j, func(s *slot) bool { return s.mask }) {
			st.reportOp(CodeAVXDeco, j, "a write mask goes on the destination operand (the last one in AT&T order)")
			return
		}
	}
	for j := range st.m.ops {
		if op := st.m.ops[j].op; op.Zeroing && !st.slotAllows(c, j, func(s *slot) bool { return s.zero }) {
			st.reportOp(CodeAVXDeco, j, fmt.Sprintf("%s does not support zeroing-masking {z} here", st.name))
			return
		}
	}
	for j := range st.m.ops {
		if op := st.m.ops[j].op; op.Mask == "" && st.slotAllows(c, j, func(s *slot) bool { return s.maskReq }) {
			st.reportOp(CodeAVXDeco, j, fmt.Sprintf("the EVEX form of %s needs a completion mask such as {%%k1}", st.name))
			return
		}
	}
	for j := range st.m.ops {
		op := st.m.ops[j].op
		if op.Broadcast == "" {
			continue
		}
		want := st.bcstWant(c, j)
		msg := fmt.Sprintf("{%s} does not fill the vector operand of %s", op.Broadcast, st.name)
		if len(want) == 1 {
			msg += fmt.Sprintf("; with these registers it is {1to%d}", want[0])
			if e, ok := st.decoEdit(j, op.Broadcast, fmt.Sprintf("1to%d", want[0])); ok {
				st.explained = true
				st.a.addFix(CodeAVXDeco, op.Start, op.End, msg, fmt.Sprintf("Use {1to%d}", want[0]), e)
				return
			}
		} else if len(want) == 0 {
			msg = fmt.Sprintf("%s has no broadcast form for %s", st.name, strings.TrimSpace(op.Text))
		}
		st.reportOp(CodeAVXDeco, j, msg)
		return
	}
	st.reportMnem(CodeAVXDeco, fmt.Sprintf("%s does not accept these {k}/{z}/broadcast decorations; valid: %s", st.name, c.list(len(st.m.ops))))
}

// slotAllows reports whether operand j's slot satisfies ok in some form
// that matches with decorations relaxed.
func (st *stmtCtx) slotAllows(c *compiled, j int, ok func(*slot) bool) bool {
	for i := range c.forms {
		f := &c.forms[i]
		if st.m.match(f, relaxDeco, nil) && ok(&f.slots[j]) {
			return true
		}
	}
	return false
}

// bcstWant lists the {1toN} counts the forms matching with decorations
// relaxed would take at operand j: the vector width of the slot over its
// element size.
func (st *stmtCtx) bcstWant(c *compiled, j int) []int {
	var out []int
	seen := map[int]bool{}
	for i := range c.forms {
		f := &c.forms[i]
		if !st.m.match(f, relaxDeco, nil) {
			continue
		}
		var elem, vec int16
		for _, a := range f.slots[j].alts {
			switch {
			case a.kind == akMem && a.bcst != 0:
				elem = a.bcst
			case a.kind == akVec:
				vec = a.width
			}
		}
		if elem == 0 || vec == 0 {
			continue
		}
		if n := int(vec / elem); !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// checkAmbiguous reports a memory operand whose size nothing pins down:
// no suffix, and the forms that match take it at more than one width
// (inc (%bx): byte, word or dword). GAS picks a default and warns, or for
// movzx/movsx silently assumes a byte source. push and pop default to the
// stack width by design, so they are not reported.
func (st *stmtCtx) checkAmbiguous(exact []*form) {
	if st.m.suffix != 0 || st.suffix != "" || st.name == "push" || st.name == "pop" {
		return
	}
	if (st.name == "in" || st.name == "out") && len(st.m.ops) == 1 {
		st.reportMnem(CodeSizeAmbiguous, fmt.Sprintf("%s %s names no data register, so the size is ambiguous; write %sb, %sw or %sl",
			st.name, strings.TrimSpace(st.m.ops[0].op.Text), st.name, st.name, st.name))
		return
	}
	picked := make([]*alt, len(st.m.ops))
	for j := range st.m.ops {
		o := &st.m.ops[j]
		if o.kind != asm.OpMem || o.op.Broadcast != "" {
			continue
		}
		widths := map[int]bool{}
		gpr := true
		unsized := false
		for _, f := range exact {
			st.m.match(f, 0, picked)
			a := picked[j]
			if a == nil || a.kind != akMem || a.width == 0 {
				unsized = true
				break
			}
			// A 64-bit operand size does not exist outside 64-bit
			// code, so it is no alternative there.
			if a.sized && a.width == 64 && st.modeKnown() && st.mode() != asm.Mode64 {
				continue
			}
			widths[int(a.width)] = true
			gpr = gpr && a.sized
		}
		if unsized || len(widths) < 2 {
			continue
		}
		ws := make([]int, 0, len(widths))
		for w := range widths {
			ws = append(ws, w)
		}
		sort.Ints(ws)
		var alts []string
		for _, w := range ws {
			alts = append(alts, st.sizedMnemonic(w))
		}
		msg := fmt.Sprintf("the size of %s is ambiguous: %s could take it as %s bits; ",
			strings.TrimSpace(o.op.Text), st.name, joinInts(ws))
		if gpr {
			msg += "write the suffix (" + strings.Join(alts, ", ") + ")"
		} else {
			msg += "name the size with a register operand or the mnemonic's x/y suffix"
		}
		st.reportOp(CodeSizeAmbiguous, j, msg)
		return
	}
}

// sizedMnemonic spells the mnemonic with a memory operand of w bits. movzx
// and movsx take two size letters in AT&T (movzbl: byte to long).
func (st *stmtCtx) sizedMnemonic(w int) string {
	if st.name == "movzx" || st.name == "movsx" {
		dst := st.m.ops[len(st.m.ops)-1]
		if dst.kind == asm.OpReg {
			return st.name[:4] + suffixFor(w) + suffixFor(dst.reg.Width)
		}
	}
	return st.mnemonic() + suffixFor(w)
}

func joinInts(ws []int) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = fmt.Sprint(w)
	}
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// unsignedImm8 are the instructions (and operand positions, AT&T order)
// whose imm8 GAS takes as unsigned: a negative value is rejected there
// rather than truncated.
func (st *stmtCtx) unsignedImm8(j int) bool {
	switch st.name {
	case "int", "aam", "aad":
		return true
	case "enter":
		return j == 1
	case "bt", "bts", "btr", "btc", "shld", "shrd":
		return j == 0
	}
	return shiftNames[st.name] && j == 0
}

// immRange is the range of values operand j may hold in form f: what an
// immN field can store (signed or unsigned), except that a 64-bit operation
// sign-extends its imm32, so only -2^31..2^31-1 keeps its value.
func (st *stmtCtx) immRange(j int, picked []*alt) (lo, hi int64) {
	a := picked[j]
	switch a.width {
	case 8:
		if st.unsignedImm8(j) {
			return 0, 0xff
		}
		return -0x80, 0xff
	case 16:
		return -0x8000, 0xffff
	case 32:
		if st.op64(picked) {
			return math.MinInt32, math.MaxInt32
		}
		return math.MinInt32, math.MaxUint32
	}
	return math.MinInt64, math.MaxInt64
}

// op64 reports a form whose operation size is 64 bits, where an imm32 is
// sign-extended: a 64-bit operand-size operand, or push in 64-bit code.
func (st *stmtCtx) op64(picked []*alt) bool {
	if st.name == "push" {
		return st.modeKnown() && st.mode() == asm.Mode64 && st.m.suffix != 16
	}
	for j := range st.m.ops {
		if a := picked[j]; a != nil && a.opSize && opWidth(&st.m.ops[j], a) == 64 {
			return true
		}
	}
	return st.m.suffix == 64
}

// checkImm reports a known immediate outside what every matching form can
// hold. GAS truncates some with a warning, rejects others, and silently
// wraps small negative ones; all of them assemble to a different value
// than the one written.
func (st *stmtCtx) checkImm(exact []*form) {
	picked := make([]*alt, len(st.m.ops))
	for j := range st.m.ops {
		o := &st.m.ops[j]
		if o.kind != asm.OpImm {
			continue
		}
		v, ok := st.a.constValue(o.op.Imm)
		if !ok {
			continue
		}
		lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
		signExt := false
		for _, f := range exact {
			st.m.match(f, 0, picked)
			if picked[j] == nil || picked[j].kind != akImm {
				continue
			}
			l, h := st.immRange(j, picked)
			if picked[j].width == 32 && h == math.MaxInt32 {
				signExt = true
			}
			lo, hi = min(lo, l), max(hi, h)
		}
		if lo > hi || (v >= lo && v <= hi) {
			continue
		}
		st.reportOp(CodeImmRange, j, st.immMessage(v, lo, hi, signExt))
		return
	}
}

func (st *stmtCtx) immMessage(v, lo, hi int64, signExt bool) string {
	val := "$" + hex(v)
	switch {
	case st.name == "in" || st.name == "out":
		return fmt.Sprintf("port %s does not fit the 8-bit port field (0-0xff); GAS truncates it. Ports above 0xff go through %%dx", hex(v))
	case st.name == "int":
		return fmt.Sprintf("interrupt vector %s is out of range (0-0xff)", hex(v))
	case st.name == "ret" || st.name == "lret":
		return fmt.Sprintf("%s pops at most 0xffff bytes; %s does not fit its imm16", st.name, val)
	case signExt && hi == math.MaxInt32:
		return fmt.Sprintf("%s is not a sign-extended imm32, the largest immediate a 64-bit %s takes (-0x80000000-0x7fffffff); "+
			"GAS rejects it. A 32-bit mov into the low register zero-extends, or load it with movabs into a register", val, st.name)
	}
	return fmt.Sprintf("%s does not fit the immediate of %s (%s-%s); GAS truncates or rejects it", val, st.mnemonic(), hex(lo), hex(hi))
}
