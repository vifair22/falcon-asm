package flow

import (
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// Register families. The numbering is the hardware encoding order for the
// general registers so a family number reads like a ModRM register number,
// followed by the families whose values are tracked (segment and control
// registers), then the families that are only tracked for their effect
// (mask registers, vector registers).
//
// Values are kept only for families below NumValueRegs: a uint64 cannot
// hold a vector register, and nothing a rule asks about a k register needs
// its value.
const (
	RAX Reg = iota
	RCX
	RDX
	RBX
	RSP
	RBP
	RSI
	RDI
	R8
	R9
	R10
	R11
	R12
	R13
	R14
	R15
	ES
	CS
	SS
	DS
	FS
	GS
	CR0
	CR2
	CR3
	CR4
	CR8
	// NumValueRegs is the number of families State.Regs holds a Value
	// for: the 16 general, 6 segment and 5 control registers.
	NumValueRegs
)

// Families past the value-tracked ones.
const (
	K0 Reg = NumValueRegs + iota
	K1
	K2
	K3
	K4
	K5
	K6
	K7
	// XMM0 is the first vector family: xmmN, ymmN and zmmN are one
	// family, XMM0+N, because a write to any of them changes the others.
	XMM0
	// NumRegs is one past the last family (XMM0+31).
	NumRegs = XMM0 + 32
)

// NoReg is "no register family".
const NoReg Reg = -1

// NumGPR is the number of general-purpose families (RAX..R15).
const NumGPR = 16

// Vec returns the vector family xmmN/ymmN/zmmN.
func Vec(n int) Reg { return XMM0 + Reg(n) }

// IsGPR reports a general-purpose family.
func (r Reg) IsGPR() bool { return r >= RAX && r <= R15 }

// IsSegment reports a segment register family.
func (r Reg) IsSegment() bool { return r >= ES && r <= GS }

// IsControl reports a control register family.
func (r Reg) IsControl() bool { return r >= CR0 && r <= CR8 }

// IsMask reports an AVX-512 mask register family.
func (r Reg) IsMask() bool { return r >= K0 && r <= K7 }

// IsVector reports an xmm/ymm/zmm family.
func (r Reg) IsVector() bool { return r >= XMM0 && r < NumRegs }

// HasValue reports whether State.Regs tracks the family's contents.
func (r Reg) HasValue() bool { return r >= 0 && r < NumValueRegs }

var gpr64 = [NumGPR]string{"rax", "rcx", "rdx", "rbx", "rsp", "rbp", "rsi", "rdi",
	"r8", "r9", "r10", "r11", "r12", "r13", "r14", "r15"}
var gpr32 = [NumGPR]string{"eax", "ecx", "edx", "ebx", "esp", "ebp", "esi", "edi",
	"r8d", "r9d", "r10d", "r11d", "r12d", "r13d", "r14d", "r15d"}
var gpr16 = [NumGPR]string{"ax", "cx", "dx", "bx", "sp", "bp", "si", "di",
	"r8w", "r9w", "r10w", "r11w", "r12w", "r13w", "r14w", "r15w"}
var gpr8 = [NumGPR]string{"al", "cl", "dl", "bl", "spl", "bpl", "sil", "dil",
	"r8b", "r9b", "r10b", "r11b", "r12b", "r13b", "r14b", "r15b"}
var gpr8h = [4]string{"ah", "ch", "dh", "bh"}
var segNames = [6]string{"es", "cs", "ss", "ds", "fs", "gs"}
var crNames = [5]string{"cr0", "cr2", "cr3", "cr4", "cr8"}

// String is the family's canonical name: the 64-bit general register,
// the segment or control register, kN, or xmmN for a vector family.
func (r Reg) String() string {
	switch {
	case r.IsGPR():
		return gpr64[r]
	case r.IsSegment():
		return segNames[r-ES]
	case r.IsControl():
		return crNames[r-CR0]
	case r.IsMask():
		return "k" + strconv.Itoa(int(r-K0))
	case r.IsVector():
		return "xmm" + strconv.Itoa(int(r-XMM0))
	}
	return "reg(" + strconv.Itoa(int(r)) + ")"
}

// Name is the family's register name at a width in bits: (RAX, 16) is
// "ax", (RAX, 8) is "al", (Vec(3), 256) is "ymm3". Segment, control and
// mask families have one name whatever the width. "" when the family has
// no name at that width.
func (r Reg) Name(width int) string {
	switch {
	case r.IsGPR():
		switch width {
		case 64:
			return gpr64[r]
		case 32:
			return gpr32[r]
		case 16:
			return gpr16[r]
		case 8:
			return gpr8[r]
		}
		return ""
	case r.IsVector():
		n := strconv.Itoa(int(r - XMM0))
		switch width {
		case 128:
			return "xmm" + n
		case 256:
			return "ymm" + n
		case 512:
			return "zmm" + n
		}
		return ""
	case r >= 0 && r < NumRegs:
		return r.String()
	}
	return ""
}

// regPart is one register name: its family and the bits of the family it
// covers.
type regPart struct {
	r      Reg
	lo, hi int8 // inclusive bit range; hi is capped at 63 for vectors
	wide   int16
}

var regTable = buildRegTable()

// regByIsa is regTable keyed by the isa table entry a parsed operand
// already points at: a pointer hash instead of a string hash on every
// register operand the analysis reads.
var regByIsa = func() map[*isa.Register]regPart {
	m := map[*isa.Register]regPart{}
	for _, r := range isa.Registers() {
		if p, ok := regTable[r.Name]; ok {
			m[r] = p
		}
	}
	return m
}()

func buildRegTable() map[string]regPart {
	m := make(map[string]regPart, 256)
	for i := Reg(0); i < NumGPR; i++ {
		m[gpr64[i]] = regPart{i, 0, 63, 64}
		m[gpr32[i]] = regPart{i, 0, 31, 32}
		m[gpr16[i]] = regPart{i, 0, 15, 16}
		m[gpr8[i]] = regPart{i, 0, 7, 8}
		if i >= R8 {
			// GAS also accepts the AMD spelling r8l.
			m[gpr64[i]+"l"] = regPart{i, 0, 7, 8}
		}
	}
	for i, n := range gpr8h {
		m[n] = regPart{Reg(i), 8, 15, 8}
	}
	for i, n := range segNames {
		m[n] = regPart{ES + Reg(i), 0, 15, 16}
	}
	for i, n := range crNames {
		m[n] = regPart{CR0 + Reg(i), 0, 63, 64}
	}
	for i := 0; i < 8; i++ {
		m["k"+strconv.Itoa(i)] = regPart{K0 + Reg(i), 0, 63, 64}
	}
	for i := 0; i < 32; i++ {
		n := strconv.Itoa(i)
		m["xmm"+n] = regPart{Vec(i), 0, 63, 128}
		m["ymm"+n] = regPart{Vec(i), 0, 63, 256}
		m["zmm"+n] = regPart{Vec(i), 0, 63, 512}
	}
	return m
}

// RegOf maps a register name ("ah", "eax", "%dx", "cr0", "ymm3") to its
// family and the inclusive bit range [lo, hi] of the family it names. A
// vector name reports its full width (hi 127, 255 or 511) although only
// the family is tracked. ok is false for names outside the model (debug,
// x87, mmx, bound, tile registers, rip, eflags).
func RegOf(name string) (r Reg, lo, hi int, ok bool) {
	p, ok := lookupReg(name)
	if !ok {
		return NoReg, 0, 0, false
	}
	hi = int(p.hi)
	if p.r.IsVector() {
		hi = int(p.wide) - 1
	}
	return p.r, int(p.lo), hi, true
}

func lookupReg(name string) (regPart, bool) {
	name = strings.TrimPrefix(name, "%")
	p, ok := regTable[name]
	if !ok {
		p, ok = regTable[strings.ToLower(name)]
	}
	return p, ok
}

// width is the part's width in bits (64 for control registers, 128/256/512
// for vector names).
func (p regPart) width() int {
	if p.r.IsVector() {
		return int(p.wide)
	}
	return int(p.hi-p.lo) + 1
}
