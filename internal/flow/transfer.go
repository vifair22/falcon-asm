package flow

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// Transfer functions. The supported subset, with exact semantics where
// the inputs are known:
//
//	mov/movabs (immediate, register, segment and control registers; a
//	  store of the stack pointer to an absolute symbol and its reload),
//	movz*/movs*/movzx/movsx/movsxd (zero and sign extension),
//	xor/sub of a register with itself (zero idiom), and/or/xor/add/sub,
//	inc/dec/not/neg, shl/sal/shr/sar/rol/ror by a known count, lea,
//	xchg, cbw/cwde/cdqe and cwd/cdq/cqo (both spellings),
//	push/pop (through a small abstract stack, so a popped value is the
//	  pushed one), pushf/popf (IF and DF travel through the stack),
//	  pusha/popa, enter/leave, add/sub/lea on the stack pointer,
//	call (callee summary or Config.CallClobbers), ret/lret/iret,
//	cld/std/cli/sti, int (Config.IntClobbers/IntEffect), int3/into,
//	string instructions with and without rep (rsi/rdi/rcx stepped when DF
//	  and the count are known), loop/loope/loopne,
//	in/out, cpuid/rdtsc/rdtscp/rdmsr/rdpmc/xgetbv/rdpkru, mul/div/imul/idiv
//	  (one operand), lahf, xlat, the BCD adjusts, syscall, cmpxchg,
//	  cmpxchg8b/16b, lss/lds/les/lfs/lgs, lmsw, clts,
//	vzeroupper/vzeroall and the VEX/EVEX vs legacy SSE upper-state rule.
//
// Everything else: the destination operand (the last one, unless the
// instruction only reads it) becomes Unknown with Def at the statement,
// the six status flags the isa tables say it writes get FlagDef at the
// statement, and DF/IF become Unknown when the tables say it writes them.
// Instructions in categories with wide register side effects (SGX, VMX,
// PCONFIG, UINTR, FRED, ...) and anything unresolved make every register
// Unknown.

// sizeOf maps an AT&T suffix letter to bits.
func sizeOf(c byte) int {
	switch c {
	case 'b':
		return 8
	case 'w':
		return 16
	case 'l', 'd':
		return 32
	case 'q':
		return 64
	}
	return 0
}

// suffixBits is the operand size the mnemonic spells: the resolved AT&T
// suffix, or the trailing letter of a mnemonic that is an alias of the
// instruction (pushfd, lodsb, iretq).
func suffixBits(st *asm.Stmt) int {
	if len(st.Suffix) == 1 {
		return sizeOf(st.Suffix[0])
	}
	if st.Inst != nil && st.Mnemonic != st.Inst.Name && len(st.Mnemonic) > len(st.Inst.Name) {
		return sizeOf(st.Mnemonic[len(st.Mnemonic)-1])
	}
	return 0
}

// operandReg returns the register part an operand names.
func operandReg(op *asm.Operand) (regPart, bool) {
	if op.Kind != asm.OpReg {
		return regPart{}, false
	}
	if op.Reg != nil {
		p, ok := regByIsa[op.Reg]
		return p, ok
	}
	return lookupReg(op.RegName)
}

// insnInfo is what step needs to know about an instruction beyond its
// name, computed once per instruction per analysis: the isa flag strings
// are parsed on demand and would otherwise be parsed on every step.
type insnInfo struct {
	fw       asm.Flags
	readOnly bool // the last operand is only read
	opaque   bool
	implicit []regPart
}

func (a *analyzer) info(st *asm.Stmt) *insnInfo {
	if in, ok := a.infos[st.Inst]; ok {
		return in
	}
	inst := st.Inst
	in := &insnInfo{
		fw:       st.FlagsWritten(),
		readOnly: readOnlyLast[inst.Name] || strings.HasPrefix(inst.Name, "prefetch"),
		opaque:   opaqueCategories[inst.Category] && inst.Name != "syscall" || inst.Name == "sysenter",
		implicit: implicitWrites[inst.Name],
	}
	switch inst.Category {
	case "COND_BR", "UNCOND_BR", "CALL", "RET", "PUSH":
		in.readOnly = true
	}
	if a.infos == nil {
		a.infos = map[*isa.Instruction]*insnInfo{}
	}
	a.infos[inst] = in
	return in
}

// opBits is the operand's width in bits: the register's, the suffix's for
// memory, 0 when neither says.
func opBits(st *asm.Stmt, op *asm.Operand) int {
	if p, ok := operandReg(op); ok {
		return p.width()
	}
	if op.Kind == asm.OpMem {
		if w := op.Width(); w != 0 {
			return w
		}
		return suffixBits(st)
	}
	return 0
}

// dataBits is the instruction's operand size: the destination's width,
// else the suffix's.
func dataBits(st *asm.Stmt) int {
	for k := len(st.Ops) - 1; k >= 0; k-- {
		if w := opBits(st, &st.Ops[k]); w != 0 {
			return w
		}
	}
	return suffixBits(st)
}

// hasPrefix reports a prefix on the statement or on a lone prefix
// statement ("rep;") right before it.
func (a *analyzer) hasPrefix(i int, names ...string) bool {
	check := func(ps []string) bool {
		for _, p := range ps {
			for _, n := range names {
				if p == n {
					return true
				}
			}
		}
		return false
	}
	if check(a.p.Stmts[i].Prefixes) {
		return true
	}
	if i > 0 {
		prev := &a.p.Stmts[i-1]
		if prev.Kind == asm.Instr && prev.Mnemonic == "" && !prev.InMacroDef {
			return check(prev.Prefixes)
		}
	}
	return false
}

// modeBits is the default operand size, with data16/data32 applied.
func (a *analyzer) modeBits(i int, mode asm.Mode) int {
	switch mode {
	case asm.Mode16:
		if a.hasPrefix(i, "data32") {
			return 32
		}
		return 16
	case asm.Mode32:
		if a.hasPrefix(i, "data16") {
			return 16
		}
		return 32
	}
	if a.hasPrefix(i, "data16") {
		return 16
	}
	return 64
}

// addrBits is the address size (the width of the count and index
// registers string instructions and loop use).
func (a *analyzer) addrBits(i int, mode asm.Mode) int {
	switch mode {
	case asm.Mode16:
		if a.hasPrefix(i, "addr32") {
			return 32
		}
		return 16
	case asm.Mode32:
		if a.hasPrefix(i, "addr16") {
			return 16
		}
		return 32
	}
	if a.hasPrefix(i, "addr32") {
		return 32
	}
	return 64
}

// stackBits is the stack pointer width a push or pop moves (sp, esp or
// rsp), approximated from the mode: the B bit of SS is not known.
func stackBits(mode asm.Mode) int {
	switch mode {
	case asm.Mode16:
		return 16
	case asm.Mode32:
		return 32
	}
	return 64
}

func lowPart(r Reg, w int) regPart {
	return regPart{r: r, lo: 0, hi: int8(w - 1), wide: int16(w)}
}

// read returns an operand's value at width w (immediates truncated,
// registers read from their bit range).
func read(s *State, op *asm.Operand, w int) Value {
	switch op.Kind {
	case asm.OpImm:
		if op.ImmVal.Known {
			return konst(uint64(op.ImmVal.V), w)
		}
	case asm.OpReg:
		if p, ok := lookupReg(op.RegName); ok {
			return readPart(s, p, w)
		}
	}
	return Value{}
}

// readPart reads a register part at width w.
func readPart(s *State, p regPart, w int) Value {
	if !p.r.HasValue() {
		return Value{}
	}
	v, m := s.Part(p.r, int(p.lo), int(p.hi))
	wm := widthMask(w)
	return Value{V: v & wm, Mask: m & wm, Def: s.get(p.r).Def}
}

// writePart stores v (its low bits) into a register part with the
// architectural write rule: a 32-bit general register write in 64-bit mode
// zero-extends; narrower writes merge. A control register written outside
// 64-bit mode is a 32-bit register, its upper half known zero.
func (s *State) writePart(p regPart, v Value, def int) {
	r := p.r
	if !r.HasValue() {
		return
	}
	lo, hi := int(p.lo), int(p.hi)
	w := hi - lo + 1
	vm := v.Mask & widthMask(w)
	old := s.get(r)
	var nv Value
	switch {
	case r.IsGPR() && lo == 0 && hi == 31 && s.Mode == asm.Mode64:
		nv = Value{V: v.V & vm, Mask: vm | 0xFFFFFFFF00000000}
	case r.IsControl() && s.Mode != asm.Mode64:
		vm &= 0xFFFFFFFF
		nv = Value{V: v.V & vm, Mask: vm | 0xFFFFFFFF00000000}
	default:
		m := bitsMask(lo, hi)
		nv = Value{V: old.V&^m | (v.V&vm)<<uint(lo), Mask: old.Mask&^m | vm<<uint(lo)}
	}
	nv.V &= nv.Mask
	nv.Def = def
	s.set(r, nv)
	switch r {
	case RSP:
		s.unknownSP()
	case RBP:
		s.FrameKnown = false
	}
}

// setRSP updates the stack pointer's value without touching the tracked
// depth: for the instructions that move it by a known amount.
func (s *State) setRSP(delta int, def int) {
	w := stackBits(s.Mode)
	old := s.get(RSP)
	part := Value{V: old.V & widthMask(w), Mask: old.Mask & widthMask(w)}
	nv := valAdd(part, konst(uint64(int64(delta)), w), w)
	m := widthMask(w)
	s.set(RSP, Value{V: old.V&^m | nv.V, Mask: old.Mask&^m | nv.Mask, Def: def})
}

// havoc forgets everything an opaque statement (a macro call, an unknown
// instruction) could change. Def is DefMerged: it may or may not have
// written each register, so no rule may blame it.
func (s *State) havoc() {
	for r := range s.regs {
		s.regs[r] = rv{}
		s.defs[r] = DefMerged
	}
	for k := range s.FlagDef {
		s.FlagDef[k] = DefMerged
	}
	s.DF, s.IF = Unknown, Unknown
	s.loseStack()
	s.vecKnown = 0
}

// defineFlags marks the status flags in f as written by statement i.
func (s *State) defineFlags(f asm.Flags, i int) {
	if f&asm.FlagCF != 0 {
		s.FlagDef[FlagCF] = i
	}
	if f&asm.FlagPF != 0 {
		s.FlagDef[FlagPF] = i
	}
	if f&asm.FlagAF != 0 {
		s.FlagDef[FlagAF] = i
	}
	if f&asm.FlagZF != 0 {
		s.FlagDef[FlagZF] = i
	}
	if f&asm.FlagSF != 0 {
		s.FlagDef[FlagSF] = i
	}
	if f&asm.FlagOF != 0 {
		s.FlagDef[FlagOF] = i
	}
}

// isVEX reports a VEX/EVEX-encoded vector instruction: its 128-bit writes
// zero the upper ymm/zmm bits, where a legacy SSE write keeps them.
func isVEX(st *asm.Stmt) bool {
	if st.Inst == nil || !strings.HasPrefix(st.Inst.Name, "v") {
		return false
	}
	switch st.Inst.Category {
	case "VTX", "SYSTEM":
		return false
	}
	return true
}

// writeDest makes an operand's register unknown, written by i, with the
// vector upper-state rule for vector registers.
func (s *State) writeDest(st *asm.Stmt, op *asm.Operand, i int) {
	p, ok := operandReg(op)
	if !ok {
		return
	}
	switch {
	case p.r.HasValue():
		s.writePart(p, Value{}, i)
	case p.r.IsVector() && isVEX(st):
		if p.wide == 128 {
			s.setUpper(p.r, Clear)
		} else {
			s.setUpper(p.r, Set)
		}
	}
}

// memKey names an absolute memory operand (no base or index), the only
// kind the stack-pointer save slot is tracked through: an FNV-1a hash of
// segment and displacement, 0 for any other operand.
func memKey(op *asm.Operand) uint64 {
	if op.Kind != asm.OpMem || op.Base != "" || op.Index != "" || op.Disp == "" {
		return 0
	}
	h := uint64(14695981039346656037)
	mix := func(t string) {
		for i := 0; i < len(t); i++ {
			h ^= uint64(t[i])
			h *= 1099511628211
		}
	}
	mix(op.Seg)
	mix(":")
	mix(op.Disp)
	if h == 0 {
		h = 1
	}
	return h
}

// store notes a memory write: a store over the stack pointer's save slot
// forgets it. Stores through a register are assumed not to hit it.
func (s *State) store(op *asm.Operand) {
	if s.spSave != 0 && memKey(op) == s.spSave {
		s.spSave = 0
	}
}

var readOnlyLast = map[string]bool{
	"cmp": true, "test": true, "bt": true, "ptest": true, "vptest": true, "vtestps": true,
	"vtestpd": true, "ucomiss": true, "ucomisd": true, "comiss": true, "comisd": true,
	"vucomiss": true, "vucomisd": true, "vcomiss": true, "vcomisd": true, "vucomish": true,
	"vcomish": true, "kortestb": true, "kortestw": true, "kortestd": true, "kortestq": true,
	"ktestb": true, "ktestw": true, "ktestd": true, "ktestq": true, "bound": true,
	"out": true, "outs": true, "push": true, "nop": true, "verr": true, "verw": true,
	"lgdt": true, "lidt": true, "lldt": true, "ltr": true, "invlpg": true, "invlpga": true,
	"invpcid": true, "clflush": true, "clflushopt": true, "clwb": true, "cldemote": true,
	"wrmsr": true, "wrfsbase": true, "wrgsbase": true, "ptwrite": true, "monitor": true,
	"umonitor": true, "umwait": true, "tpause": true, "ldmxcsr": true, "vldmxcsr": true,
	"fldcw": true, "fldenv": true, "frstor": true, "fxrstor": true, "fxrstor64": true,
	"xrstor": true, "xrstor64": true, "xrstors": true, "xrstors64": true, "fcom": true,
	"fcomp": true, "fucom": true, "fucomp": true, "ftst": true, "ficom": true, "ficomp": true,
	"enter": true, "int": true, "xabort": true, "prefetchw": true, "prefetchwt1": true,
	"incsspd": true, "incsspq": true, "lkgs": true,
}

// opaqueCategories have register side effects too wide or too
// leaf-dependent to list: every register becomes unknown.
var opaqueCategories = map[string]bool{
	"SGX": true, "VTX": true, "PCONFIG": true, "UINTR": true, "FRED": true,
	"MSRLIST": true, "KEYLOCKER": true, "KEYLOCKER_WIDE": true, "SYSCALL": true,
}

// implicitWrites lists registers an instruction writes without naming
// them. 32-bit parts zero-extend in 64-bit mode, as the hardware does.
var implicitWrites = map[string][]regPart{
	"cpuid":      {lowPart(RAX, 32), lowPart(RBX, 32), lowPart(RCX, 32), lowPart(RDX, 32)},
	"rdtsc":      {lowPart(RAX, 32), lowPart(RDX, 32)},
	"rdtscp":     {lowPart(RAX, 32), lowPart(RDX, 32), lowPart(RCX, 32)},
	"rdmsr":      {lowPart(RAX, 32), lowPart(RDX, 32)},
	"rdpmc":      {lowPart(RAX, 32), lowPart(RDX, 32)},
	"xgetbv":     {lowPart(RAX, 32), lowPart(RDX, 32)},
	"rdpkru":     {lowPart(RAX, 32), lowPart(RDX, 32)},
	"rdpru":      {lowPart(RAX, 32), lowPart(RDX, 32)},
	"lahf":       {{r: RAX, lo: 8, hi: 15, wide: 8}},
	"xlat":       {lowPart(RAX, 8)},
	"aaa":        {lowPart(RAX, 16)},
	"aas":        {lowPart(RAX, 16)},
	"aam":        {lowPart(RAX, 16)},
	"aad":        {lowPart(RAX, 16)},
	"daa":        {lowPart(RAX, 8)},
	"das":        {lowPart(RAX, 8)},
	"syscall":    {lowPart(RCX, 64), lowPart(R11, 64)},
	"cmpxchg8b":  {lowPart(RAX, 32), lowPart(RDX, 32)},
	"cmpxchg16b": {lowPart(RAX, 64), lowPart(RDX, 64)},
	"xbegin":     {lowPart(RAX, 32)},
	"smsw":       nil,
	"lss":        {lowPart(SS, 16)},
	"lds":        {lowPart(DS, 16)},
	"les":        {lowPart(ES, 16)},
	"lfs":        {lowPart(FS, 16)},
	"lgs":        {lowPart(GS, 16)},
}

// step applies statement i's effect to s.
func (a *analyzer) step(s *State, i int) {
	st := &a.p.Stmts[i]
	switch st.Kind {
	case asm.Instr:
	case asm.MacroCall:
		s.havoc()
		return
	default:
		return
	}
	if st.Inst == nil {
		if st.Mnemonic != "" {
			s.havoc()
		}
		return
	}
	inst := st.Inst
	info := a.info(st)
	if info.opaque {
		s.havoc()
		return
	}
	switch inst.Name {
	case "int3", "into", "int1":
		// The tables list the flags the handler starts with; the
		// handler returns with iret, which restores them.
		return
	}
	fw := info.fw
	s.defineFlags(fw, i)
	if fw&asm.FlagDF != 0 {
		s.DF = Unknown
	}
	if fw&asm.FlagIF != 0 {
		s.IF = Unknown
	}
	if a.special(s, st, i) {
		return
	}
	a.generic(s, st, i, info)
}

// generic is the fallback: destinations and implicit outputs unknown.
func (a *analyzer) generic(s *State, st *asm.Stmt, i int, info *insnInfo) {
	name := st.Inst.Name
	n := len(st.Ops)
	if n > 0 && !info.readOnly {
		last := &st.Ops[n-1]
		if last.Kind == asm.OpMem {
			s.store(last)
		}
		s.writeDest(st, last, i)
		switch name {
		case "xchg", "xadd", "mulx":
			if n >= 2 {
				s.writeDest(st, &st.Ops[n-2], i)
			}
		case "cmpxchg":
			s.writePart(lowPart(RAX, dataBits(st)), Value{}, i)
		}
	}
	for _, p := range info.implicit {
		s.writePart(p, Value{}, i)
	}
}

// special handles the instructions with exact semantics; false sends the
// statement to generic.
func (a *analyzer) special(s *State, st *asm.Stmt, i int) bool {
	ops := st.Ops
	n := len(ops)
	switch name := st.Inst.Name; name {
	case "mov", "movabs", "movq", "movd":
		// movq/movd resolve to the MMX/SSE moves, but between general
		// registers and immediates they are the integer move.
		if n != 2 {
			return false
		}
		a.mov(s, st, i)
		return true
	case "movzx", "movzbw", "movzbl", "movzbq", "movzwl", "movzwq",
		"movsx", "movsxd", "movsbw", "movsbl", "movsbq", "movswl", "movswq", "movslq":
		if n != 2 {
			return false
		}
		src, ok1 := operandReg(&ops[0])
		dst, ok2 := operandReg(&ops[1])
		if !ok2 || !dst.r.HasValue() {
			return false
		}
		v := Value{}
		if ok1 && src.r.HasValue() {
			sw := src.width()
			v = extend(read(s, &ops[0], sw), sw, dst.width(), strings.HasPrefix(name, "movs"))
		}
		s.writePart(dst, v, i)
		return true
	case "xor", "sub", "and", "or", "add":
		return a.alu(s, st, i)
	case "inc", "dec", "not", "neg":
		if n != 1 {
			return false
		}
		p, ok := operandReg(&ops[0])
		if !ok || !p.r.HasValue() || p.r == RSP {
			return false
		}
		w := p.width()
		v := read(s, &ops[0], w)
		switch name {
		case "inc":
			v = valAdd(v, konst(1, w), w)
		case "dec":
			v = valSub(v, konst(1, w), w)
		case "not":
			v = valNot(v, w)
		case "neg":
			v = valNeg(v, w)
		}
		s.writePart(p, v, i)
		return true
	case "shl", "sal", "shr", "sar", "rol", "ror":
		return a.shift(s, st, i)
	case "lea":
		return a.lea(s, st, i)
	case "xchg":
		if n != 2 {
			return false
		}
		p0, ok0 := operandReg(&ops[0])
		p1, ok1 := operandReg(&ops[1])
		if !ok0 || !ok1 || !p0.r.HasValue() || !p1.r.HasValue() || p0.r == RSP || p1.r == RSP {
			return false
		}
		w := p0.width()
		v0, v1 := read(s, &ops[0], w), read(s, &ops[1], w)
		v0.Mask, v1.Mask = v0.Mask&widthMask(w), v1.Mask&widthMask(w)
		s.writePart(p0, v1, i)
		s.writePart(p1, v0, i)
		return true
	case "cbw", "cbtw", "cwde", "cwtl", "cdqe", "cltq":
		from := map[string]int{"cbw": 8, "cbtw": 8, "cwde": 16, "cwtl": 16, "cdqe": 32, "cltq": 32}[name]
		v := extend(readPart(s, lowPart(RAX, from), from), from, 2*from, true)
		s.writePart(lowPart(RAX, 2*from), v, i)
		return true
	case "cwd", "cwtd", "cdq", "cltd", "cqo", "cqto":
		w := map[string]int{"cwd": 16, "cwtd": 16, "cdq": 32, "cltd": 32, "cqo": 64, "cqto": 64}[name]
		v := extend(readPart(s, lowPart(RAX, w), w), w, 2*w, true)
		s.writePart(lowPart(RDX, w), valShr(v, uint(w), 2*w), i)
		return true
	case "push":
		if n != 1 {
			return false
		}
		a.push(s, st, i)
		return true
	case "pop":
		if n != 1 {
			return false
		}
		a.pop(s, st, i)
		return true
	case "pushf", "pushfq":
		w := suffixBits(st)
		if name == "pushfq" {
			w = 64
		}
		if w == 0 {
			w = a.modeBits(i, s.Mode)
		}
		v := Value{Def: i}
		if s.IF != Unknown {
			v.Mask |= 1 << 9
			if s.IF == Set {
				v.V |= 1 << 9
			}
		}
		if s.DF != Unknown {
			v.Mask |= 1 << 10
			if s.DF == Set {
				v.V |= 1 << 10
			}
		}
		s.setRSP(-w/8, i)
		s.push(w/8, v, NoReg, true)
		return true
	case "popf", "popfq":
		w := suffixBits(st)
		if name == "popfq" {
			w = 64
		}
		if w == 0 {
			w = a.modeBits(i, s.Mode)
		}
		slot, ok := s.pop(w / 8)
		s.setRSP(w/8, i)
		s.IF, s.DF = Unknown, Unknown
		if ok {
			if slot.Val.Mask&(1<<9) != 0 {
				s.IF = triOf(slot.Val.V&(1<<9) != 0)
			}
			if slot.Val.Mask&(1<<10) != 0 {
				s.DF = triOf(slot.Val.V&(1<<10) != 0)
			}
		}
		return true
	case "pusha":
		a.pusha(s, st, i)
		return true
	case "popa":
		a.popa(s, st, i)
		return true
	case "enter":
		w := a.modeBits(i, s.Mode)
		if sb := suffixBits(st); sb != 0 {
			w = sb
		}
		s.setRSP(-w/8, i)
		s.push(w/8, s.get(RBP), RBP, false)
		s.set(RBP, Value{Def: i})
		if s.StackKnown {
			s.FrameDepth, s.FrameKnown = s.Stack, true
		}
		size := Value{}
		if n == 2 {
			size = read(s, &ops[0], 16)
		}
		if n != 2 || !size.Known(0xFFFF) || !ops[1].ImmVal.Known || ops[1].ImmVal.V != 0 {
			s.set(RSP, Value{Def: i})
			s.unknownSP()
			return true
		}
		s.adjust(int(size.V))
		s.setRSP(-int(size.V), i)
		return true
	case "leave":
		w := a.modeBits(i, s.Mode)
		if sb := suffixBits(st); sb != 0 {
			w = sb
		}
		if !s.FrameKnown {
			s.set(RSP, Value{Def: i})
			s.unknownSP()
			s.set(RBP, Value{Def: i})
			return true
		}
		s.setDepth(s.FrameDepth)
		s.set(RSP, Value{Def: i})
		slot, ok := s.pop(w / 8)
		s.FrameKnown = false
		v := Value{Def: i}
		if ok {
			v = slot.Val
		}
		s.writePartKeep(lowPart(RBP, w), v)
		return true
	case "call", "lcall":
		a.call(s, st, i)
		return true
	case "ret", "lret", "iret", "iretq":
		w := suffixBits(st)
		if name == "iretq" {
			w = 64
		}
		if w == 0 {
			w = a.modeBits(i, s.Mode)
		}
		pop := w / 8
		switch name {
		case "lret":
			pop *= 2
		case "iret", "iretq":
			pop *= 3
		}
		if n == 1 && ops[0].ImmVal.Known {
			pop += int(ops[0].ImmVal.V)
		}
		s.adjust(-pop)
		s.setRSP(pop, i)
		return true
	case "cld":
		s.DF = Clear
		return true
	case "std":
		s.DF = Set
		return true
	case "cli":
		s.IF = Clear
		return true
	case "sti":
		s.IF = Set
		return true
	case "int":
		a.intN(s, st, i)
		return true
	case "in":
		if n == 0 {
			w := suffixBits(st)
			if w == 0 {
				w = 8
			}
			s.writePart(lowPart(RAX, w), Value{}, i)
			return true
		}
		return false
	case "movs", "stos", "lods", "scas", "cmps", "ins", "outs":
		a.stringOp(s, st, i)
		return true
	case "loop", "loope", "loopne", "loopz", "loopnz":
		w := a.addrBits(i, s.Mode)
		p := lowPart(RCX, w)
		s.writePart(p, valSub(readPart(s, p, w), konst(1, w), w), i)
		return true
	case "mul", "div", "imul", "idiv":
		if n != 1 {
			return false
		}
		w := opBits(st, &ops[0])
		switch w {
		case 8:
			s.writePart(lowPart(RAX, 16), Value{}, i)
		case 16, 32, 64:
			s.writePart(lowPart(RAX, w), Value{}, i)
			s.writePart(lowPart(RDX, w), Value{}, i)
		default:
			s.writePart(lowPart(RAX, 64), Value{Def: i}, i)
			s.writePart(lowPart(RDX, 64), Value{Def: i}, i)
		}
		return true
	case "lmsw":
		old := s.get(CR0)
		v := Value{}
		if n == 1 {
			v = read(s, &ops[0], 16)
		}
		// Bits 1..3 load; bit 0 (PE) can be set but never cleared.
		low := Value{V: old.V & 0xF, Mask: old.Mask & 0xF}
		nv := Value{V: v.V & 0xE, Mask: v.Mask & 0xE}
		pe := valOr(Value{V: low.V & 1, Mask: low.Mask & 1}, Value{V: v.V & 1, Mask: v.Mask & 1}, 1)
		nv.V |= pe.V
		nv.Mask |= pe.Mask
		s.set(CR0, Value{V: old.V&^0xF | nv.V, Mask: old.Mask&^0xF | nv.Mask, Def: i})
		return true
	case "clts":
		v := s.get(CR0)
		s.set(CR0, Value{V: v.V &^ 8, Mask: v.Mask | 8, Def: i})
		return true
	case "vzeroupper", "vzeroall":
		for k := 0; k < 16; k++ {
			s.setUpper(Vec(k), Clear)
		}
		return true
	}
	return false
}

func triOf(b bool) Tri {
	if b {
		return Set
	}
	return Clear
}

// writePartKeep stores a value with the Def it carries (a pop restoring
// a pushed register keeps the pushed value's writer).
func (s *State) writePartKeep(p regPart, v Value) {
	s.writePart(p, v, v.Def)
}

func (a *analyzer) mov(s *State, st *asm.Stmt, i int) {
	src, dst := &st.Ops[0], &st.Ops[1]
	dp, dok := operandReg(dst)
	if !dok {
		if dst.Kind == asm.OpMem {
			if sp, ok := operandReg(src); ok && sp.r == RSP && s.StackKnown {
				if k := memKey(dst); k != 0 {
					s.spSave, s.spDepth = k, s.Stack
					return
				}
			}
			s.store(dst)
		}
		return
	}
	if !dp.r.HasValue() {
		s.writeDest(st, dst, i)
		return
	}
	w := dp.width()
	sp, sok := operandReg(src)
	switch {
	case dp.r == RSP && src.Kind == asm.OpMem && s.spSave != 0 && memKey(src) == s.spSave:
		// The parked stack pointer comes back: the depth it had.
		s.set(RSP, Value{Def: i})
		s.setDepth(s.spDepth)
		return
	case dp.r == RSP && sok && sp.r == RBP && s.FrameKnown:
		v := read(s, src, w)
		s.set(RSP, Value{V: v.V, Mask: v.Mask, Def: i})
		s.setDepth(s.FrameDepth)
		return
	}
	v := Value{}
	if src.Kind == asm.OpImm || (sok && sp.r.HasValue()) {
		v = read(s, src, w)
	}
	depth, known := s.Stack, s.StackKnown
	s.writePart(dp, v, i)
	if dp.r == RBP && sok && sp.r == RSP && known {
		s.FrameDepth, s.FrameKnown = depth, true
	}
}

func (a *analyzer) alu(s *State, st *asm.Stmt, i int) bool {
	if len(st.Ops) != 2 {
		return false
	}
	src, dst := &st.Ops[0], &st.Ops[1]
	dp, ok := operandReg(dst)
	if !ok || !dp.r.HasValue() {
		return false
	}
	name := st.Inst.Name
	w := dp.width()
	if (name == "xor" || name == "sub") && src.Kind == asm.OpReg && src.RegName == dst.RegName {
		if dp.r == RSP {
			return false
		}
		s.writePart(dp, konst(0, w), i)
		return true
	}
	b := read(s, src, w)
	if dp.r == RSP && (name == "add" || name == "sub") && b.Known(widthMask(w)) && src.Kind == asm.OpImm {
		delta := int(int64(b.V))
		if w < 64 {
			delta = int(signExt(b.V, w))
		}
		if name == "add" {
			delta = -delta
		}
		s.adjust(delta)
		s.setRSP(-delta, i)
		return true
	}
	x := read(s, dst, w)
	var r Value
	switch name {
	case "xor":
		r = valXor(x, b, w)
	case "and":
		r = valAnd(x, b, w)
	case "or":
		r = valOr(x, b, w)
	case "add":
		r = valAdd(x, b, w)
	case "sub":
		r = valSub(x, b, w)
	}
	s.writePart(dp, r, i)
	return true
}

func signExt(v uint64, w int) int64 {
	sh := uint(64 - w)
	return int64(v<<sh) >> sh
}

func (a *analyzer) shift(s *State, st *asm.Stmt, i int) bool {
	ops := st.Ops
	if len(ops) == 0 {
		return false
	}
	dst := &ops[len(ops)-1]
	dp, ok := operandReg(dst)
	if !ok || !dp.r.HasValue() || dp.r == RSP {
		return false
	}
	w := dp.width()
	cnt := Value{V: 1, Mask: ^uint64(0)}
	if len(ops) == 2 {
		cnt = read(s, &ops[0], 8)
	}
	cm := uint64(31)
	if w == 64 {
		cm = 63
	}
	if !cnt.Known(cm) {
		s.writePart(dp, Value{}, i)
		return true
	}
	c := uint(cnt.V & cm)
	x := read(s, dst, w)
	var r Value
	switch st.Inst.Name {
	case "shl", "sal":
		r = valShl(x, c, w)
	case "shr":
		r = valShr(x, c, w)
	case "sar":
		r = valSar(x, c, w)
	case "rol":
		r = valRol(x, c, w)
	case "ror":
		r = valRor(x, c, w)
	}
	s.writePart(dp, r, i)
	return true
}

func (a *analyzer) lea(s *State, st *asm.Stmt, i int) bool {
	if len(st.Ops) != 2 || st.Ops[0].Kind != asm.OpMem {
		return false
	}
	m, dst := &st.Ops[0], &st.Ops[1]
	dp, ok := operandReg(dst)
	if !ok || !dp.r.HasValue() {
		return false
	}
	w := dp.width()
	if m.RIPRel || m.Seg != "" {
		s.writePart(dp, Value{}, i)
		return true
	}
	addr := konst(0, 64)
	if m.Disp != "" {
		if !m.DispVal.Known {
			addr = Value{}
		} else {
			addr = konst(uint64(m.DispVal.V), 64)
		}
	}
	if m.Base != "" {
		bp, ok := lookupReg(m.Base)
		if !ok {
			return false
		}
		if bp.r == RSP && dp.r == RSP && m.Index == "" && addr.Known(^uint64(0)) {
			// lea -8(%rsp), %rsp moves the stack like sub does.
			d := int(int64(addr.V))
			s.adjust(-d)
			s.setRSP(d, i)
			return true
		}
		addr = valAdd(addr, readPart(s, bp, bp.width()), 64)
	}
	if m.Index != "" {
		ip, ok := lookupReg(m.Index)
		if !ok {
			return false
		}
		x := readPart(s, ip, ip.width())
		sh := uint(0)
		switch m.Scale {
		case 2:
			sh = 1
		case 4:
			sh = 2
		case 8:
			sh = 3
		}
		addr = valAdd(addr, valShl(x, sh, 64), 64)
	}
	s.writePart(dp, Value{V: addr.V & widthMask(w), Mask: addr.Mask & widthMask(w)}, i)
	return true
}

func (a *analyzer) pushBits(s *State, st *asm.Stmt, i int) int {
	if w := suffixBits(st); w != 0 {
		return w
	}
	if len(st.Ops) == 1 {
		if p, ok := operandReg(&st.Ops[0]); ok && p.r.IsGPR() {
			return p.width()
		}
	}
	return a.modeBits(i, s.Mode)
}

func (a *analyzer) push(s *State, st *asm.Stmt, i int) {
	w := a.pushBits(s, st, i)
	op := &st.Ops[0]
	v := Value{Def: i}
	src := NoReg
	switch op.Kind {
	case asm.OpImm:
		v = konst(uint64(op.ImmVal.V), w)
		if !op.ImmVal.Known {
			v = Value{}
		}
		v.Def = i
	case asm.OpReg:
		if p, ok := operandReg(op); ok && p.r.HasValue() {
			rw := p.width()
			v = read(s, op, rw)
			if rw < w && p.r.IsSegment() {
				// A segment register push writes the selector in the low
				// 16 bits; what the rest holds varies by processor.
				v = Value{V: v.V, Mask: v.Mask & 0xFFFF, Def: v.Def}
			}
			if p.lo == 0 {
				src = p.r
			}
		}
	}
	s.setRSP(-w/8, i)
	s.push(w/8, v, src, false)
}

func (a *analyzer) pop(s *State, st *asm.Stmt, i int) {
	w := a.pushBits(s, st, i)
	op := &st.Ops[0]
	slot, ok := s.pop(w / 8)
	s.setRSP(w/8, i)
	p, rok := operandReg(op)
	if !rok {
		if op.Kind == asm.OpMem {
			s.store(op)
		}
		return
	}
	if p.r == RSP {
		s.set(RSP, Value{Def: i})
		s.unknownSP()
		return
	}
	v := Value{Def: i}
	if ok {
		v = slot.Val
	}
	s.writePart(p, v, v.Def)
}

var pushaOrder = [8]Reg{RAX, RCX, RDX, RBX, RSP, RBP, RSI, RDI}

func (a *analyzer) pusha(s *State, st *asm.Stmt, i int) {
	w := suffixBits(st)
	if w == 0 {
		w = a.modeBits(i, s.Mode)
	}
	if w == 64 {
		s.havoc()
		return
	}
	sp := s.get(RSP)
	for _, r := range pushaOrder {
		v := s.get(r)
		if r == RSP {
			v = sp
		}
		v = Value{V: v.V & widthMask(w), Mask: v.Mask & widthMask(w), Def: v.Def}
		src := r
		if r == RSP {
			src = NoReg
		}
		s.setRSP(-w/8, i)
		s.push(w/8, v, src, false)
	}
}

func (a *analyzer) popa(s *State, st *asm.Stmt, i int) {
	w := suffixBits(st)
	if w == 0 {
		w = a.modeBits(i, s.Mode)
	}
	if w == 64 {
		s.havoc()
		return
	}
	for k := 7; k >= 0; k-- {
		r := pushaOrder[k]
		slot, ok := s.pop(w / 8)
		s.setRSP(w/8, i)
		if r == RSP {
			continue
		}
		v := Value{Def: i}
		if ok {
			v = slot.Val
		}
		s.writePart(lowPart(r, w), v, v.Def)
	}
}

// call applies a call's effect on return: the callee's summary when the
// callee is in the document and analyzable, else the configured clobbers.
// The return address push and the callee's ret cancel out; "ret $n"
// callees also pop their arguments.
func (a *analyzer) call(s *State, st *asm.Stmt, i int) {
	s.defineFlags(asm.FlagsStatus, i)
	sum := a.summaryAt(i)
	if sum != nil && !sum.unknown {
		for r := Reg(0); r < NumRegs; r++ {
			if r == RSP || !sum.writes.has(r) {
				continue
			}
			s.clobber(r, i)
		}
		if sum.df {
			s.DF = Unknown
		}
		if sum.ifl {
			s.IF = Unknown
		}
		switch {
		case sum.pop < 0:
			s.set(RSP, Value{Def: i})
			s.unknownSP()
		case sum.pop > 0:
			s.adjust(-sum.pop)
			s.setRSP(sum.pop, i)
		}
		return
	}
	for r := Reg(0); r < NumRegs; r++ {
		if r != RSP && a.callClob.has(r) {
			s.clobber(r, i)
		}
	}
	s.DF, s.IF = Unknown, Unknown
}

// clobber makes a family unknown, written by i; a vector family's upper
// state becomes unknown too. Unlike writePart it keeps the stack depth:
// the callee or handler restores the stack pointer by convention.
func (s *State) clobber(r Reg, i int) {
	switch {
	case r.HasValue():
		s.set(r, Value{Def: i})
		if r == RBP {
			s.FrameKnown = false
		}
	case r.IsVector():
		s.setUpper(r, Unknown)
	}
}

func (a *analyzer) intN(s *State, st *asm.Stmt, i int) {
	s.defineFlags(asm.FlagsStatus, i)
	s.DF, s.IF = Unknown, Unknown
	known := len(st.Ops) == 1 && st.Ops[0].Kind == asm.OpImm && st.Ops[0].ImmVal.Known
	vec := uint64(0)
	if known {
		vec = uint64(st.Ops[0].ImmVal.V) & 0xFF
	}
	clob := defaultIntClobbers
	if known && a.cfg.IntClobbers != nil {
		if rs, ok := a.cfg.IntClobbers(vec); ok {
			clob = rs
		}
	}
	for _, r := range clob {
		if r != RSP {
			s.clobber(r, i)
		}
	}
	if known && a.cfg.IntEffect != nil {
		a.cfg.IntEffect(vec, s)
	}
}

var defaultIntClobbers = []Reg{RAX, RCX, RDX, RBX, RBP, RSI, RDI, R8, R9, R10, R11, R12, R13, R14, R15,
	ES, DS, FS, GS}

// stringOp steps the index registers and the count. With DF known and,
// under rep, the count known, the new rsi/rdi are exact.
func (a *analyzer) stringOp(s *State, st *asm.Stmt, i int) {
	name := st.Inst.Name
	ew := suffixBits(st)
	for k := range st.Ops {
		if p, ok := operandReg(&st.Ops[k]); ok && p.r == RAX {
			ew = p.width()
		}
	}
	if ew == 0 && (name == "ins" || name == "outs") {
		ew = 8
	}
	aw := a.addrBits(i, s.Mode)
	rep := a.hasPrefix(i, "rep", "repe", "repz", "repne", "repnz")
	cond := name == "scas" || name == "cmps"
	cnt := Value{V: 1, Mask: ^uint64(0)}
	cp := lowPart(RCX, aw)
	if rep {
		cnt = readPart(s, cp, aw)
		if cond {
			cnt = Value{}
		}
	}
	step := func(r Reg) {
		p := lowPart(r, aw)
		v := readPart(s, p, aw)
		nv := Value{}
		if ew != 0 && s.DF != Unknown && cnt.Known(widthMask(aw)) {
			d := uint64(ew/8) * cnt.V
			if s.DF == Set {
				nv = valSub(v, konst(d, aw), aw)
			} else {
				nv = valAdd(v, konst(d, aw), aw)
			}
		}
		s.writePart(p, nv, i)
	}
	switch name {
	case "movs", "cmps":
		step(RSI)
		step(RDI)
	case "lods", "outs":
		step(RSI)
	case "stos", "scas", "ins":
		step(RDI)
	}
	if name == "lods" {
		w := ew
		if w == 0 {
			w = 64
		}
		s.writePart(lowPart(RAX, w), Value{}, i)
	}
	if rep {
		if cond {
			s.writePart(cp, Value{}, i)
		} else {
			s.writePart(cp, konst(0, aw), i)
		}
	}
}
