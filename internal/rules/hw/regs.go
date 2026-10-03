package hw

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// The register tracker follows constants through straight-line code, one
// block at a time: `movb $0x42, %ah` ... `int $0x13`, `movl $MSR, %ecx` ...
// `wrmsr`. It is deliberately local. Every label starts from nothing (some
// other path may jump there with other values), every call, int and
// unconditional jump forgets everything, and any instruction whose register
// effects are not spelled out below forgets everything too. That makes it
// blind to values that cross a label, which the dataflow analysis
// (internal/flow) adds when the index is built with it (flowvals.go); what
// the tracker does see, it sees correctly.

// regSlot locates a general register name: the architectural register, the
// bit offset of the named part and its width.
type regSlot struct {
	slot  int
	shift uint
	width uint
}

var gprs = func() map[string]regSlot {
	m := map[string]regSlot{}
	legacy := []string{"a", "c", "d", "b"}
	for i, r := range legacy {
		m[r+"l"] = regSlot{i, 0, 8}
		m[r+"h"] = regSlot{i, 8, 8}
		m[r+"x"] = regSlot{i, 0, 16}
		m["e"+r+"x"] = regSlot{i, 0, 32}
		m["r"+r+"x"] = regSlot{i, 0, 64}
	}
	for i, r := range []string{"sp", "bp", "si", "di"} {
		m[r] = regSlot{4 + i, 0, 16}
		m[r+"l"] = regSlot{4 + i, 0, 8}
		m["e"+r] = regSlot{4 + i, 0, 32}
		m["r"+r] = regSlot{4 + i, 0, 64}
	}
	for n := 8; n < 16; n++ {
		r := "r" + itoa(n)
		m[r+"b"] = regSlot{n, 0, 8}
		m[r+"w"] = regSlot{n, 0, 16}
		m[r+"d"] = regSlot{n, 0, 32}
		m[r] = regSlot{n, 0, 64}
	}
	return m
}()

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return "1" + string(rune('0'+n-10))
}

// rv is what is known about a register (or a part of one): the bits in
// known have the values in val. sym is set when the register holds the
// address of a label (`movw $dap, %si`), as wide as symW bits. writers are
// the statements whose results make up the current value, so a consumer
// can say what each of them was for.
type rv struct {
	known, val uint64
	sym        string
	symW       uint
	writers    []int
}

// full reports whether every bit of a width-bit value is known.
func (v rv) full(width uint) bool { return v.known&maskW(width) == maskW(width) }

// set are the bits known to be 1.
func (v rv) set() uint64 { return v.known & v.val }

func maskW(w uint) uint64 {
	if w >= 64 {
		return ^uint64(0)
	}
	return 1<<w - 1
}

type regFile [16]rv

func (f *regFile) read(name string) (rv, bool) {
	s, ok := gprs[name]
	if !ok {
		return rv{}, false
	}
	r := f[s.slot]
	m := maskW(s.width)
	out := rv{known: r.known >> s.shift & m, val: r.val >> s.shift & m, writers: r.writers}
	if s.shift == 0 && r.sym != "" && s.width <= r.symW {
		out.sym, out.symW = r.sym, s.width
	}
	return out, true
}

// write stores v into the named part. A 32-bit write clears the upper half,
// as it does in 64-bit mode; in other modes the upper half does not exist,
// so the same rule is harmless. A partial write keeps the other bits and
// adds its writer to the chain; a full write starts a new chain.
func (f *regFile) write(name string, v rv, stmt int) {
	s, ok := gprs[name]
	if !ok {
		return
	}
	r := &f[s.slot]
	m := maskW(s.width) << s.shift
	partial := s.width < 32
	if !partial {
		m = ^uint64(0)
		v.known |= ^maskW(s.width)
		v.val &= maskW(s.width)
	}
	r.known = r.known&^m | v.known<<s.shift&m
	r.val = r.val&^m | v.val<<s.shift&m
	if partial {
		r.writers = append(append([]int(nil), r.writers...), stmt)
	} else {
		r.writers = []int{stmt}
	}
	if len(v.writers) > 0 && !partial {
		r.writers = append(append([]int(nil), v.writers...), stmt)
	}
	r.sym, r.symW = "", 0
	if v.sym != "" && s.shift == 0 {
		r.sym, r.symW = v.sym, s.width
	}
}

func (f *regFile) forget(names ...string) {
	for _, n := range names {
		if s, ok := gprs[n]; ok {
			f[s.slot] = rv{}
		}
	}
}

// Event kinds: what a statement does with tracked registers.
const (
	evInt   = "int"   // int $N: AH/AL select the BIOS service
	evPort  = "port"  // in/out: the port, in DX or an immediate
	evMSR   = "msr"   // rdmsr/wrmsr: ECX the index, EDX:EAX the value written
	evCPUID = "cpuid" // cpuid: EAX the leaf, ECX the subleaf
	evCR    = "cr"    // mov to a control register, xsetbv
	evSeg   = "seg"   // mov to a segment register
)

// event is one consumer of tracked registers.
type event struct {
	kind  string
	stmt  int
	write bool  // out, wrmsr
	width uint  // in/out access width
	n     int64 // int vector, port number when immediate, CR number
	cr    string
	sreg  string
	// the register values the consumer saw
	a, c, d rv // eax (or ah/al via a), ecx, edx
	src     rv // mov to cr/sreg: the source register
	srcName string
	imm     bool // port given as an immediate
}

// role is what an immediate-writing statement turned out to feed.
type role struct {
	ev  int    // index into tracker.events
	reg string // "a", "c", "d", "src"
}

type tracker struct {
	prog   *asm.Program
	events []event
	roles  map[int][]role
	// sym reports whether a name is a label of this document (not a macro),
	// for symbolic register values.
	isLabel func(string) bool
}

// safe instructions write nothing but their explicit destination (and the
// stack pointer, which is not tracked).
var safe = map[string]bool{
	"cmp": true, "test": true, "push": true, "nop": true, "cli": true, "sti": true, "cld": true,
	"std": true, "clc": true, "stc": true, "cmc": true, "hlt": true, "lgdt": true, "lidt": true,
	"sgdt": true, "sidt": true, "lldt": true, "ltr": true, "invlpg": true, "wbinvd": true,
	"invd": true, "pause": true, "lfence": true, "mfence": true, "sfence": true, "bt": true,
	"bts": true, "btr": true, "btc": true, "clts": true, "lmsw": true, "smsw": true, "verr": true,
	"verw": true, "out": true, "wrmsr": true, "xsetbv": true, "not": true, "neg": true,
	"rol": true, "ror": true, "rcl": true, "rcr": true, "sar": true, "adc": true, "sbb": true,
	"bsf": true, "bsr": true, "bswap": true, "movzx": true, "movsx": true, "movzbw": true,
	"movzbl": true, "movzwl": true, "movsbw": true, "movsbl": true, "movswl": true,
	"movzbq": true, "movzwq": true, "movsbq": true, "movswq": true, "movslq": true,
}

func (t *tracker) run() {
	var f regFile
	for i := range t.prog.Stmts {
		s := &t.prog.Stmts[i]
		if len(s.Labels) > 0 || s.InMacroDef {
			f = regFile{}
		}
		switch s.Kind {
		case asm.Empty, asm.Label:
			continue
		case asm.Directive:
			if !silent[s.Name] {
				f = regFile{}
			}
			continue
		case asm.Instr:
		default:
			f = regFile{}
			continue
		}
		if s.InMacroDef || s.Inst == nil || len(s.Prefixes) > 0 && !onlyLock(s.Prefixes) {
			f = regFile{}
			continue
		}
		t.step(&f, i, s)
	}
}

func onlyLock(p []string) bool {
	for _, x := range p {
		if x != "lock" {
			return false
		}
	}
	return true
}

func isGPR(op *asm.Operand) bool {
	if op.Kind != asm.OpReg {
		return false
	}
	_, ok := gprs[op.RegName]
	return ok
}

func (t *tracker) addEvent(e event, regs map[string]rv) {
	t.events = append(t.events, e)
	idx := len(t.events) - 1
	for name, v := range regs {
		for _, w := range v.writers {
			t.roles[w] = append(t.roles[w], role{ev: idx, reg: name})
		}
	}
}

// step applies one instruction to the register file and records the events
// it is a consumer for.
func (t *tracker) step(f *regFile, i int, s *asm.Stmt) {
	name := s.Inst.Name
	ops := s.Ops
	var dst *asm.Operand
	if len(ops) > 0 {
		dst = &ops[len(ops)-1]
	}
	switch name {
	case "int":
		if len(ops) == 1 && ops[0].Kind == asm.OpImm && ops[0].ImmVal.Known {
			a, _ := f.read("eax")
			t.addEvent(event{kind: evInt, stmt: i, n: ops[0].ImmVal.V, a: a}, map[string]rv{"a": a})
		}
		*f = regFile{}
		return
	case "in", "out":
		t.portEvent(f, i, s, name == "out")
		if name == "in" && dst != nil && isGPR(dst) {
			f.forget(dst.RegName)
		}
		return
	case "rdmsr", "wrmsr":
		a, _ := f.read("eax")
		c, _ := f.read("ecx")
		d, _ := f.read("edx")
		regs := map[string]rv{"c": c}
		if name == "wrmsr" {
			regs["a"], regs["d"] = a, d
		}
		t.addEvent(event{kind: evMSR, stmt: i, write: name == "wrmsr", a: a, c: c, d: d}, regs)
		if name == "rdmsr" {
			f.forget("eax", "edx")
		}
		return
	case "cpuid":
		a, _ := f.read("eax")
		c, _ := f.read("ecx")
		t.addEvent(event{kind: evCPUID, stmt: i, a: a, c: c}, map[string]rv{"a": a, "c": c})
		f.forget("eax", "ebx", "ecx", "edx")
		return
	case "xsetbv":
		a, _ := f.read("eax")
		c, _ := f.read("ecx")
		d, _ := f.read("edx")
		if c.full(32) && c.val == 0 {
			t.addEvent(event{kind: evCR, stmt: i, cr: "xcr0", a: a, c: c, d: d}, map[string]rv{"a": a, "d": d})
		}
		return
	case "rdtsc", "xgetbv":
		f.forget("eax", "edx")
		return
	case "mov":
		t.mov(f, i, s)
		return
	case "lea":
		if dst != nil && isGPR(dst) && len(ops) == 2 && ops[0].Kind == asm.OpMem && ops[0].Base == "" &&
			ops[0].Index == "" && ops[0].Seg == "" && t.isLabel(ops[0].Disp) {
			f.write(dst.RegName, rv{sym: ops[0].Disp}, i)
		} else if dst != nil && isGPR(dst) {
			f.write(dst.RegName, rv{}, i)
		}
		return
	case "xor", "sub", "or", "and", "add", "shl", "sal", "shr", "inc", "dec":
		t.arith(f, i, s, name)
		return
	case "xchg", "xadd", "cmpxchg":
		for k := range ops {
			if isGPR(&ops[k]) {
				f.write(ops[k].RegName, rv{}, i)
			}
		}
		return
	case "jmp", "ljmp", "ret", "lret", "iret", "call", "lcall", "hlt", "ud2", "sysret", "sysexit":
		*f = regFile{}
		return
	}
	if strings.HasPrefix(name, "j") || strings.HasPrefix(name, "set") || strings.HasPrefix(name, "cmov") || safe[name] {
		// Conditional jumps fall through with every register unchanged.
		if dst != nil && isGPR(dst) && !strings.HasPrefix(name, "j") && name != "cmp" && name != "test" &&
			name != "bt" && name != "push" {
			f.write(dst.RegName, rv{}, i)
		}
		return
	}
	*f = regFile{}
}

func (t *tracker) portEvent(f *regFile, i int, s *asm.Stmt, write bool) {
	ops := s.Ops
	if len(ops) != 2 {
		return
	}
	portOp, dataOp := &ops[0], &ops[1]
	if write {
		portOp, dataOp = &ops[1], &ops[0]
	}
	w := uint(0)
	if dataOp.Kind == asm.OpReg {
		w = uint(dataOp.Width())
	}
	a, _ := f.read("eax")
	e := event{kind: evPort, stmt: i, write: write, width: w, a: a}
	regs := map[string]rv{}
	switch {
	case portOp.Kind == asm.OpImm && portOp.ImmVal.Known:
		e.n, e.imm = portOp.ImmVal.V, true
	case portOp.Kind == asm.OpReg && portOp.RegName == "dx":
		d, _ := f.read("dx")
		e.d = d
		regs["d"] = d
	default:
		return
	}
	if write {
		regs["a"] = a
	}
	t.addEvent(e, regs)
}

func (t *tracker) mov(f *regFile, i int, s *asm.Stmt) {
	ops := s.Ops
	if len(ops) != 2 {
		*f = regFile{}
		return
	}
	src, dst := &ops[0], &ops[1]
	if dst.Kind == asm.OpReg && dst.Reg != nil && !isGPR(dst) {
		if !isGPR(src) {
			return
		}
		v, _ := f.read(src.RegName)
		switch dst.Reg.Class {
		case "control":
			t.addEvent(event{kind: evCR, stmt: i, cr: dst.RegName, src: v, srcName: src.RegName}, map[string]rv{"src": v})
		case "segment":
			t.addEvent(event{kind: evSeg, stmt: i, sreg: dst.RegName, src: v, srcName: src.RegName}, map[string]rv{"src": v})
		}
		return
	}
	if !isGPR(dst) {
		return // a store: no register changes
	}
	switch {
	case src.Kind == asm.OpImm && src.ImmVal.Known:
		f.write(dst.RegName, rv{known: ^uint64(0), val: uint64(src.ImmVal.V)}, i)
	case src.Kind == asm.OpImm && t.isLabel(strings.TrimSpace(src.Imm)):
		f.write(dst.RegName, rv{sym: strings.TrimSpace(src.Imm)}, i)
	case isGPR(src):
		v, _ := f.read(src.RegName)
		v.known &= maskW(gprs[src.RegName].width)
		f.write(dst.RegName, v, i)
	default:
		f.write(dst.RegName, rv{}, i)
	}
}

// arith folds the arithmetic a constant survives: or and and set or clear
// known bits even when the rest of the register is unknown (`orl $1, %eax`
// after reading CR0 sets PE, whatever else CR0 held); the others need
// every bit of the operand known.
func (t *tracker) arith(f *regFile, i int, s *asm.Stmt, name string) {
	ops := s.Ops
	dst := &ops[len(ops)-1]
	if !isGPR(dst) {
		return
	}
	w := gprs[dst.RegName].width
	m := maskW(w)
	cur, _ := f.read(dst.RegName)
	cur.sym = ""
	if len(ops) == 2 && (name == "xor" || name == "sub") && isGPR(&ops[0]) && ops[0].RegName == dst.RegName {
		f.write(dst.RegName, rv{known: ^uint64(0)}, i)
		return
	}
	var imm uint64
	hasImm := false
	switch {
	case len(ops) == 2 && ops[0].Kind == asm.OpImm && ops[0].ImmVal.Known:
		imm, hasImm = uint64(ops[0].ImmVal.V)&m, true
	case len(ops) == 1 && (name == "inc" || name == "dec"):
		imm, hasImm = 1, true
	case len(ops) == 1 && (name == "shl" || name == "sal" || name == "shr"):
		imm, hasImm = 1, true
	}
	if !hasImm {
		f.write(dst.RegName, rv{}, i)
		return
	}
	out := rv{writers: cur.writers}
	switch name {
	case "or":
		out.known = cur.known | imm
		out.val = cur.val | imm
	case "and":
		out.known = cur.known | ^imm&m
		out.val = cur.val & imm
	default:
		if !cur.full(w) {
			f.write(dst.RegName, rv{}, i)
			return
		}
		var v uint64
		switch name {
		case "xor":
			v = cur.val ^ imm
		case "add", "inc":
			v = cur.val + imm
		case "sub", "dec":
			v = cur.val - imm
		case "shl", "sal":
			v = cur.val << (imm & 63)
		case "shr":
			v = cur.val >> (imm & 63)
		}
		out.known, out.val = ^uint64(0), v&m
	}
	f.write(dst.RegName, out, i)
}
