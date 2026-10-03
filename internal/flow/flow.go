// Package flow builds a control-flow graph over an asm.Program and runs a
// forward dataflow analysis over it: known register values, flag
// definitions, the direction and interrupt flags, stack depth and segment
// register state, per statement.
//
// Soundness rule: any state the analysis cannot prove is Unknown, and every
// rule built on this package treats Unknown as "do not warn". An indirect
// jump, a computed far jump, a call to an unknown function or an
// unreachable-from-entry block all degrade to Unknown, never to a guess.
//
// A few facts rest on conventions rather than proof, because without them
// the commonest boot-code idioms would be all Unknown:
//
//   - a call returns with the stack as it was (ret pops what call pushed,
//     plus "ret $n" for a callee in the document);
//   - an int returns to the next instruction with the stack balanced;
//   - pushed slots survive while the stack pointer is elsewhere, so a
//     restored frame pointer or a parked stack pointer ("mov %esp, SYM" ...
//     "mov SYM, %esp") brings the depth and the saved values back, and a
//     store through a register is assumed not to hit the parked copy;
//   - only a label whose address escapes (global, $label, lea, data) can
//     be the target of an indirect jump.
//
// Files: cfg.go builds the graph and functions, regs.go is the register
// model, state.go the abstract state and its join, transfer.go the
// per-instruction effects, dataflow.go the solver, call summaries and the
// Result queries.
package flow

import "git.airies.net/vifair22/falcon-asm/internal/asm"

// Block is a basic block: statements First..Last inclusive (indices into
// Program.Stmts, instructions and labels only).
type Block struct {
	ID          int
	First, Last int
	Succ, Pred  []int
	// Entry is set for blocks control can reach from outside the
	// document or through an untracked edge: global labels, the first
	// block, targets of indirect jumps (all labels when the document
	// contains one), interrupt handlers.
	Entry bool
	// Exit records how control leaves: fallthrough, jump, conditional
	// jump, call (returns to next), ret, iret, hlt loop, indirect jump,
	// far jump (mode switch).
	Exit ExitKind

	// Why says which entry heuristics marked the block (Entry is
	// Why != 0). EntryIndirect alone means "a label in a document with an
	// indirect jump": such a block is not a function start and the
	// analysis does not assume an unknown state on entry to it, because
	// only a label whose address escapes (EntryGlobal, EntryAddrTaken) can
	// be an indirect jump's target.
	Why EntryReason
	// Target is the block a direct jump, conditional jump or resolvable
	// far jump goes to; -1 when there is none or it is not in this
	// document.
	Target int
	// Callee is the block a direct call (near or far) enters; -1 for an
	// indirect call or a callee defined elsewhere. Calls are not Succ
	// edges: control returns to the next statement.
	Callee int
	// FarSel is the selector (or real-mode segment) of a far jump or call
	// written with an immediate one; FarSelKnown is false otherwise.
	FarSel      uint64
	FarSelKnown bool
	// IntoData is the index of the first data statement (a data directive
	// or the label naming it) that control falls into from the end of
	// this block, -1 when it does not. Rule 28 reads it; whether the
	// fallthrough is real (a call that never returns, a hlt nothing
	// resumes) is the rule's call, from Exit.
	IntoData int

	fall int // fallthrough successor, -1 when none (see CFG.Fall)
}

// EntryReason is a set of entry heuristics.
type EntryReason uint8

// Entry heuristics.
const (
	// EntryFirst: the first code block of the document or of a section,
	// which the linker places after code this document cannot see.
	EntryFirst EntryReason = 1 << iota
	// EntryGlobal: starts with a .globl/.global/.weak label.
	EntryGlobal
	// EntryAddrTaken: starts with a label whose address is taken
	// ($label, lea, a data directive, a macro argument). Interrupt
	// handlers installed into an IDT or IVT are found this way: their
	// addresses are loaded or stored as data.
	EntryAddrTaken
	// EntryNoPred: starts with a label and has no predecessor, so
	// something outside the graph must reach it.
	EntryNoPred
	// EntryIndirect: starts with a label in a document holding an
	// indirect jump.
	EntryIndirect
)

// ExitKind is how a block ends.
type ExitKind int

// Block exits.
const (
	ExitFall ExitKind = iota
	ExitJump
	ExitCond
	ExitCall
	ExitRet
	ExitIret
	ExitHalt
	ExitIndirect
	ExitFar
	// ExitTrap is ud2/ud0/ud1: raises #UD, control does not continue.
	ExitTrap
)

// CFG is the graph for one program.
type CFG struct {
	Prog    *asm.Program
	Blocks  []Block
	BlockOf []int // statement index -> block ID, -1 for non-code statements

	// Funcs partitions the reachable code into functions; see funcs.go.
	Funcs []Func
	// FuncOf maps a block ID to the index in Funcs of the function that
	// owns it, -1 for a block no function start reaches.
	FuncOf []int
	// HasIndirect reports an indirect jump anywhere in the document.
	HasIndirect bool
}

// Func is one function: an entry block (an Entry block or a call target)
// and every block reachable from it without crossing a call edge or
// another function start. Blocks overlap when two starts share a tail;
// FuncOf names one owner (the start with the lowest block ID).
type Func struct {
	Entry  int    // block ID of the start
	Blocks []int  // block IDs, ascending, Entry included
	Name   string // first label of the entry block, "" when unlabeled
	Label  int    // statement index defining Name, -1 when unlabeled
}

// Build constructs the CFG. Implemented in cfg.go.
func Build(p *asm.Program) *CFG { return build(p) }

// Tri is a three-valued flag: Unknown, Clear (0), Set (1).
type Tri int8

// Tri values.
const (
	Unknown Tri = iota
	Clear
	Set
)

// Value is a register's known contents. Known bits only: Mask marks which
// bits of V are known (so movb $0x42, %ah on an unknown %eax knows bits
// 8..15). Bits of V outside Mask are zero.
type Value struct {
	V    uint64
	Mask uint64
	Def  int // statement index that last wrote the register, -1 unknown/entry
}

// Def sentinels. A Def of DefEntry means the register holds what it held
// when the function was entered (nothing wrote it, or a pop restored the
// pushed entry value): the callee-saved checks rely on that. DefMerged
// means paths disagree about the last writer.
const (
	DefEntry  = -1
	DefMerged = -2
)

// Reg identifies a register family (rax..r15, segment regs, cr0..cr8,
// xmm/ymm/zmm by number, k0..k7). Width-specific names map to a family plus
// a bit range; see RegOf in regs.go.
type Reg int

// Flag indexes for State.FlagDef.
const (
	FlagCF = iota
	FlagPF
	FlagAF
	FlagZF
	FlagSF
	FlagOF
)

// State is the abstract machine state before a statement.
//
// Representation (a change from the original map-based contract, for
// speed: the analysis stores one State per statement and copies it on
// every step, so it is a flat, pointer-free value of about 900 bytes with
// no per-state allocation, which also keeps the per-statement array out of
// the garbage collector's scan):
//
//   - Registers live in fixed arrays indexed by Reg for the value-tracked
//     families (Reg < NumValueRegs: general, segment and control
//     registers), value and mask in one, the Def in a parallel int32
//     array. Read them with Get or Reg (Mask 0 is unknown, which is what
//     "absent" meant) and write them with SetReg or Clobber.
//   - The callee-saved bookkeeping that was Pushed map[Reg]int is the
//     abstract stack: see Pushed, Top and Slots.
//   - Mask and vector registers carry no value; vector families carry the
//     "upper bits dirty" tri-state (UpperDirty) that vzeroupper rules use.
type State struct {
	// The small fields come first and together: the layout has no
	// padding holes, which matters at one State per statement.
	Reachable bool
	DF, IF    Tri
	// StackKnown is false after an untracked stack pointer adjustment.
	StackKnown bool
	// FrameKnown is false when rbp holds anything but a captured depth.
	FrameKnown bool
	nslot      uint8

	// vecKnown/vecDirty are the per-vector-family "upper bits dirty"
	// tri-state: bit n of vecKnown set means family n is known, and then
	// bit n of vecDirty says whether its upper bits may be non-zero.
	vecKnown, vecDirty uint32

	regs [NumValueRegs]rv
	defs [NumValueRegs]int32
	// FlagDef is the statement index that last wrote each arithmetic flag
	// (CF, PF, AF, ZF, SF, OF), -1 unknown.
	FlagDef [6]int
	// Stack is the stack depth in bytes relative to the block's function
	// entry.
	Stack int
	Mode  asm.Mode

	// FrameDepth is the stack depth captured into the frame pointer by
	// "mov %rsp, %rbp" (or enter), so leave and "mov %rbp, %rsp" restore a
	// known depth.
	FrameDepth int

	// spSave remembers "mov %esp, SYM" while the stack depth is known, so
	// "mov SYM, %esp" restores it: the idiom mode-switch code uses to park
	// the stack pointer across a trip through another mode. It is a hash of
	// the operand (0 = none) so State holds no pointer: the per-statement
	// array is then never scanned by the garbage collector.
	spSave  uint64
	spDepth int

	// slots is the top of the abstract stack, oldest first: what was
	// pushed at which depth, so pop and popf return a known value and
	// callee-saved checks can find a register's save slot.
	slots [maxSlots]slot
}

// rv is a register's known bits.
type rv struct{ v, mask uint64 }

// slot is Slot packed to 24 bytes; State carries ten of them per
// statement. The top bit of width marks a pushf slot. A push deeper than
// an int16 depth is not recorded.
type slot struct {
	v, mask uint64
	def     int32
	depth   int16
	width   uint8
	src     int8
}

const slotFlags = 0x80

func (k slot) export() Slot {
	return Slot{Depth: int(k.depth), Width: int(k.width &^ slotFlags), Src: Reg(k.src),
		Val: Value{V: k.v, Mask: k.mask, Def: int(k.def)}, Flags: k.width&slotFlags != 0}
}

// Slot is one abstract stack entry.
type Slot struct {
	Depth int   // stack depth just after the push
	Width int   // bytes pushed
	Src   Reg   // family pushed, NoReg for an immediate, memory or flags
	Val   Value // what was pushed
	Flags bool  // pushed by pushf
}

const maxSlots = 10

// Result is the dataflow solution: In[i] is the state before statement i
// (zero State, Reachable=false, for non-code or unreachable statements).
type Result struct {
	CFG *CFG
	In  []State
	// Out is the state leaving each block (indexed by block ID), after its
	// last statement; zero for a block the analysis never reached.
	Out []State

	an *analyzer
}

// Analyze runs the forward analysis to a fixed point. Implemented in
// dataflow.go.
func Analyze(g *CFG, cfg Config) *Result { return analyze(g, cfg) }

// Config tunes the analysis.
type Config struct {
	// EntryDF/EntryIF are assumed at Entry blocks (Unknown by default).
	EntryDF, EntryIF Tri
	// CallClobbers lists register families a call may change; nil means
	// every general register becomes Unknown after a call. It applies to
	// callees the analysis cannot see; a callee defined in the document
	// is summarized from its own code instead.
	CallClobbers []Reg
	// IntClobbers, when set and returning ok, names the register families
	// "int $vector" may change. Without it every general register except
	// the stack pointer, and DS/ES/FS/GS, become Unknown.
	IntClobbers func(vector uint64) (clobbers []Reg, ok bool)
	// IntEffect, when set, runs after the default effect of
	// "int $vector" (IntClobbers, IF/DF Unknown, every status flag defined
	// by the int) and may refine the state: a BIOS table modelling INT 15h
	// sets IF, or restores DF. It is not called for an unknown vector.
	IntEffect func(vector uint64, st *State)
}
