// Package callconv is the calling convention model of roadmap section 4
// (items 31 and 32): which registers carry arguments and results, which a
// callee must preserve, and what the stack must look like at a call, for
// the conventions x86 assembly is written against (AMD64 System V, MS x64,
// the i386 cdecl, stdcall, fastcall and gcc regparm conventions), plus a
// custom convention declared in a comment for hand-written helpers and
// none, which promises nothing and so checks nothing.
//
// It also parses the in-source annotations that select a convention or
// mark an interrupt handler (`falcon-asm: cc=... handler vector=14`), so
// the rules, the project's target resolution and the configuration
// validator agree on one spelling.
//
// Registers are flow register families: a convention speaks of rbx, and a
// 32-bit function preserving ebx, or a 16-bit helper preserving bx, is
// preserving the same family.
package callconv

import (
	"fmt"
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

// Kind identifies a convention.
type Kind int

// Conventions.
const (
	None Kind = iota
	SysV
	MS
	Cdecl
	Stdcall
	Fastcall
	Regparm
	Custom
)

// RegSet is a set of register families (general, segment, control, mask
// and vector), as flow numbers them.
type RegSet [2]uint64

// Add puts r in the set; out-of-range families are ignored.
func (s *RegSet) Add(r flow.Reg) {
	if r >= 0 && r < flow.NumRegs {
		s[r/64] |= 1 << uint(r%64)
	}
}

// Del removes r from the set.
func (s *RegSet) Del(r flow.Reg) {
	if r >= 0 && r < flow.NumRegs {
		s[r/64] &^= 1 << uint(r%64)
	}
}

// Has reports whether r is in the set.
func (s RegSet) Has(r flow.Reg) bool {
	return r >= 0 && r < flow.NumRegs && s[r/64]&(1<<uint(r%64)) != 0
}

// Empty reports whether the set holds nothing.
func (s RegSet) Empty() bool { return s[0] == 0 && s[1] == 0 }

// Regs lists the set's families in ascending order.
func (s RegSet) Regs() []flow.Reg {
	var out []flow.Reg
	for r := flow.Reg(0); r < flow.NumRegs; r++ {
		if s.Has(r) {
			out = append(out, r)
		}
	}
	return out
}

func setOf(rs ...flow.Reg) RegSet {
	var s RegSet
	for _, r := range rs {
		s.Add(r)
	}
	return s
}

// Conv is one calling convention.
//
// Preserved and Clobbered need not cover every register: a custom
// convention names only what its author declared, and a register in
// neither set is one the rules know nothing about, so they stay quiet
// about it. The stack pointer is in neither: stack balance is the
// dataflow rules' business (item 21).
type Conv struct {
	Kind Kind
	// Name is the canonical spelling, for messages: "sysv", "regparm(2)".
	Name string
	// Bits is the code width the convention is defined for: 64 for the
	// AMD64 conventions, 32 for the i386 ones, 0 for custom and none,
	// which apply to whatever mode the function is in.
	Bits int
	// Args are the integer argument registers in order; VecArgs counts
	// the xmm argument registers from xmm0.
	Args    []flow.Reg
	VecArgs int
	// Returns are the integer result registers (edx:eax counts both);
	// VecReturns counts xmm result registers from xmm0.
	Returns    []flow.Reg
	VecReturns int
	// Preserved are the callee-saved families; Clobbered the families a
	// call may change (argument and result registers included).
	Preserved RegSet
	Clobbered RegSet
	// RedZone is how many bytes below the stack pointer a function may
	// use without moving it (SysV's 128); 0 means none.
	RedZone int
	// Shadow is the space the caller reserves above the return address
	// for the callee to spill its register arguments into (MS x64's 32).
	Shadow int
	// Align is the stack alignment the convention requires at a call
	// instruction, 0 when the rules do not check it.
	Align int
	// CalleePops reports a convention whose callee removes its stack
	// arguments ("ret $n").
	CalleePops bool
	// SavesControl reports that the x87 control word and the MXCSR
	// control bits are callee-saved.
	SavesControl bool
}

// IsArg reports whether r is one of the convention's integer argument
// registers.
func (c *Conv) IsArg(r flow.Reg) bool {
	for _, a := range c.Args {
		if a == r {
			return true
		}
	}
	return false
}

// IsReturn reports whether r is one of the convention's integer result
// registers.
func (c *Conv) IsReturn(r flow.Reg) bool {
	for _, a := range c.Returns {
		if a == r {
			return true
		}
	}
	return false
}

// Checked reports whether the convention promises anything at all.
func (c *Conv) Checked() bool { return c != nil && c.Kind != None }

// AppliesTo reports whether the convention can describe a function
// assembled at the given width: an AMD64 convention for 32-bit code is a
// target that does not fit the function, not a promise about it.
func (c *Conv) AppliesTo(bits int) bool {
	return c != nil && (c.Bits == 0 || c.Bits == bits)
}

var (
	gprs = []flow.Reg{flow.RAX, flow.RCX, flow.RDX, flow.RBX, flow.RBP, flow.RSI, flow.RDI,
		flow.R8, flow.R9, flow.R10, flow.R11, flow.R12, flow.R13, flow.R14, flow.R15}
	i386 = []flow.Reg{flow.RAX, flow.RCX, flow.RDX, flow.RBX, flow.RBP, flow.RSI, flow.RDI}
)

func vecRange(lo, hi int) RegSet {
	var s RegSet
	for n := lo; n <= hi; n++ {
		s.Add(flow.Vec(n))
	}
	return s
}

func union(a, b RegSet) RegSet { return RegSet{a[0] | b[0], a[1] | b[1]} }

// complement is the general registers of list not in keep.
func complement(list []flow.Reg, keep RegSet) RegSet {
	var s RegSet
	for _, r := range list {
		if !keep.Has(r) {
			s.Add(r)
		}
	}
	return s
}

func sysv() *Conv {
	keep := setOf(flow.RBX, flow.RBP, flow.R12, flow.R13, flow.R14, flow.R15)
	return &Conv{
		Kind: SysV, Name: "sysv", Bits: 64,
		Args:    []flow.Reg{flow.RDI, flow.RSI, flow.RDX, flow.RCX, flow.R8, flow.R9},
		VecArgs: 8, Returns: []flow.Reg{flow.RAX, flow.RDX}, VecReturns: 2,
		Preserved: keep, Clobbered: union(complement(gprs, keep), vecRange(0, 31)),
		RedZone: 128, Align: 16, SavesControl: true,
	}
}

func ms() *Conv {
	keep := union(setOf(flow.RBX, flow.RBP, flow.RDI, flow.RSI, flow.R12, flow.R13, flow.R14, flow.R15),
		vecRange(6, 15))
	return &Conv{
		Kind: MS, Name: "ms", Bits: 64,
		Args:    []flow.Reg{flow.RCX, flow.RDX, flow.R8, flow.R9},
		VecArgs: 4, Returns: []flow.Reg{flow.RAX}, VecReturns: 1,
		Preserved: keep,
		Clobbered: union(union(complement(gprs, keep), vecRange(0, 5)), vecRange(16, 31)),
		Shadow:    32, Align: 16, SavesControl: true,
	}
}

func i386Conv(k Kind, name string, args []flow.Reg, pops bool) *Conv {
	keep := setOf(flow.RBX, flow.RSI, flow.RDI, flow.RBP)
	return &Conv{
		Kind: k, Name: name, Bits: 32, Args: args,
		Returns:   []flow.Reg{flow.RAX, flow.RDX},
		Preserved: keep, Clobbered: union(complement(i386, keep), vecRange(0, 7)),
		CalleePops: pops,
	}
}

// regparmOrder is gcc's -mregparm order.
var regparmOrder = []flow.Reg{flow.RAX, flow.RDX, flow.RCX}

// Lookup returns the convention a name selects: sysv (also sysv64, amd64,
// x86-64), ms (ms64, win64, microsoft), cdecl, stdcall, fastcall,
// regparm(N) (also regparmN, regparm=N, regparm for 3), none, and custom,
// which on its own declares nothing (see NewCustom). Case and surrounding
// space are ignored.
func Lookup(name string) (*Conv, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "sysv", "sysv64", "amd64", "x86-64", "x86_64", "sysv_abi":
		return sysv(), true
	case "ms", "ms64", "win64", "microsoft", "ms_abi":
		return ms(), true
	case "cdecl":
		return i386Conv(Cdecl, "cdecl", nil, false), true
	case "stdcall":
		return i386Conv(Stdcall, "stdcall", nil, true), true
	case "fastcall":
		return i386Conv(Fastcall, "fastcall", []flow.Reg{flow.RCX, flow.RDX}, true), true
	case "custom":
		return &Conv{Kind: Custom, Name: "custom"}, true
	case "none", "":
		return &Conv{Kind: None, Name: "none"}, true
	}
	if k, ok := regparmCount(n); ok {
		return i386Conv(Regparm, fmt.Sprintf("regparm(%d)", k), regparmOrder[:k], false), true
	}
	return nil, false
}

// Valid reports whether name selects a convention; the configuration
// validator uses it so `"cc": "regparm(2)"` is not reported as unknown.
func Valid(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// regparmCount reads regparm, regparmN, regparm=N and regparm(N).
func regparmCount(n string) (int, bool) {
	rest, ok := strings.CutPrefix(n, "regparm")
	if !ok {
		return 0, false
	}
	if rest == "" {
		return 3, true
	}
	switch {
	case strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")"):
		rest = rest[1 : len(rest)-1]
	case strings.HasPrefix(rest, "="):
		rest = rest[1:]
	}
	k, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || k < 0 || k > 3 {
		return 0, false
	}
	return k, true
}

// Names lists the canonical names, for messages and documentation.
const Names = "sysv, ms, cdecl, stdcall, fastcall, regparm(N), custom, none"

// CustomSpec is what a custom convention declares. Each list holds
// register names in any width ("ax", "%ebx", "r8"); Clobbers and Preserves
// may also be the single word "all", meaning every general register the
// other list and the result registers do not name.
type CustomSpec struct {
	Clobbers, Preserves, Args, Returns []string
}

// NewCustom builds a custom convention. An unknown register name or a
// register both clobbered and preserved is an error naming it; the rest
// of the declaration still applies, so one typo does not switch every
// check off.
func NewCustom(spec CustomSpec) (*Conv, error) {
	c := &Conv{Kind: Custom, Name: "custom"}
	var errs []string
	regs := func(list []string) ([]flow.Reg, bool) {
		var out []flow.Reg
		all := false
		for _, n := range list {
			n = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(n), "%"))
			switch {
			case n == "":
				continue
			case n == "all":
				all = true
				continue
			case n == "none":
				continue
			}
			r, _, _, ok := flow.RegOf(n)
			if !ok || !r.IsGPR() && !r.IsVector() && !r.IsSegment() {
				errs = append(errs, fmt.Sprintf("unknown register %q", n))
				continue
			}
			out = append(out, r)
		}
		return out, all
	}
	args, _ := regs(spec.Args)
	rets, _ := regs(spec.Returns)
	clob, clobAll := regs(spec.Clobbers)
	pres, presAll := regs(spec.Preserves)
	c.Args, c.Returns = args, rets
	for _, r := range clob {
		c.Clobbered.Add(r)
	}
	for _, r := range rets {
		c.Clobbered.Add(r)
	}
	for _, r := range pres {
		if c.Clobbered.Has(r) {
			errs = append(errs, fmt.Sprintf("%s is both clobbered and preserved", r.String()))
			continue
		}
		c.Preserved.Add(r)
	}
	if presAll {
		for _, r := range gprs {
			if !c.Clobbered.Has(r) {
				c.Preserved.Add(r)
			}
		}
	}
	if clobAll {
		for _, r := range gprs {
			if !c.Preserved.Has(r) {
				c.Clobbered.Add(r)
			}
		}
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return c, nil
}
