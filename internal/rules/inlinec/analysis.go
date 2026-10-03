package inlinec

import "strings"

// The per-statement facts the clobber, memory, early-clobber, hard-coded
// register and volatile rules share: which registers the template leaves
// changed, which memory it stores to, and where each operand is read and
// written. Computed once per statement.

// regWrite is one write of a register family.
type regWrite struct {
	fam        string
	insn       int
	start, end int // Asm offsets to report: the operand, or the mnemonic for an implicit write
	implicit   bool
	mnem       string
}

// memWrite is a store the template makes.
type memWrite struct {
	insn       int
	start, end int
	// described is set when the store goes through a memory operand
	// (%0 with an "m" constraint), which tells GCC about it.
	described bool
	// input is the operand index when the store goes through an input
	// memory operand, -1 otherwise.
	input int
	// seg is set for a store through %fs or %gs: memory the compiler
	// cannot address itself (thread or CPU-local data), so it has nothing
	// cached that a "memory" clobber would flush.
	seg bool
}

// segOverride reports a memory operand addressed through %fs or %gs.
func segOverride(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(t, "%fs:") || strings.HasPrefix(t, "%gs:")
}

// identityStore reports a read-modify-write that leaves memory as it was
// (lock addl $0, (%rsp); orl $0, x): a fence or a cache-line touch, not a
// store any cached copy could disagree with.
func identityStore(in *insn) bool {
	if len(in.ops) != 2 || in.ops[0].kind != oImm {
		return false
	}
	switch in.name {
	case "add", "sub", "or", "xor":
		return strings.TrimSpace(strings.TrimPrefix(in.ops[0].text, "$")) == "0"
	}
	return false
}

// opAccess is one appearance of an operand reference in the template.
type opAccess struct {
	insn       int
	start, end int
	write      bool
	read       bool
}

// regRead is a literal register the template reads.
type regRead struct {
	fam        string
	insn       int
	start, end int
}

type stmtFacts struct {
	pending map[string][]*regWrite // unrestored writes by family, in template order
	order   []string               // families in order of first write
	mem     []memWrite
	access  map[int][]opAccess // operand index -> appearances, in template order
	reads   []regRead
	written regSet         // every family written at all, restored or not
	first   map[string]int // family -> first instruction writing it, restored or not
	inOrder regSet
}

// save is an open save of a register: pushed, or copied or exchanged into
// another location, waiting for the matching restore.
type save struct {
	fam  string
	via  string // "push", or the other operand's text for mov/xchg
	insn int
}

func (m *stmtModel) facts() *stmtFacts {
	f := &stmtFacts{access: map[int][]opAccess{}, written: regSet{},
		first: map[string]int{}, inOrder: regSet{}}
	var stack []string // pushed families, "" for anything else
	var saves []save
	all := map[string][]*regWrite{} // every write since its family's open save, to drop on restore

	write := func(w regWrite) {
		if !f.written[w.fam] {
			f.first[w.fam] = w.insn
		}
		f.written.add(w.fam)
		if !f.inOrder[w.fam] {
			f.inOrder.add(w.fam)
			f.order = append(f.order, w.fam)
		}
		all[w.fam] = append(all[w.fam], &w)
	}
	restore := func(fam string, from int) {
		// Every write of fam at or after the save is undone by the restore.
		var keep []*regWrite
		for _, w := range all[fam] {
			if w.insn < from {
				keep = append(keep, w)
			}
		}
		all[fam] = keep
	}
	findSave := func(fam, via string) int {
		for k := len(saves) - 1; k >= 0; k-- {
			if saves[k].fam == fam && saves[k].via == via {
				return k
			}
		}
		return -1
	}

	for i := range m.insns {
		in := &m.insns[i]
		if !in.known {
			continue
		}
		written := map[int]bool{}
		for _, p := range writtenOps(in) {
			written[p] = true
		}
		m.recordAccess(f, i, in, written)
		if handled := m.saveRestore(in, i, &stack, &saves, findSave, restore, write); handled {
			continue
		}
		for p := range in.ops {
			op := &in.ops[p]
			if !written[p] {
				continue
			}
			// Writing the location a save went to destroys the saved value.
			for k := len(saves) - 1; k >= 0; k-- {
				if saves[k].via == op.text && saves[k].insn != i {
					saves = append(saves[:k], saves[k+1:]...)
				}
			}
			switch op.kind {
			case oReg:
				if op.fam != "" {
					write(regWrite{fam: op.fam, insn: i, start: op.start, end: op.end, mnem: in.mnem})
				}
			case oMem:
				if op.base != "rsp" && !identityStore(in) {
					f.mem = append(f.mem, memWrite{insn: i, start: op.start, end: op.end, input: -1, seg: segOverride(op.text)})
				}
			case oRef:
				if op.ref >= 0 && op.ref < len(m.ops) {
					info := &m.ops[op.ref]
					if (info.mem && !info.regLike() || info.mem && info.out) && !identityStore(in) {
						mw := memWrite{insn: i, start: op.start, end: op.end, described: info.out, input: -1, seg: op.seg}
						if !info.out {
							mw.input = op.ref
						}
						f.mem = append(f.mem, mw)
					}
				}
			}
		}
		for _, fam := range implicitWrites(in) {
			write(regWrite{fam: fam, insn: i, start: in.mnemStart, end: in.mnemStart + len(in.mnem), implicit: true, mnem: in.mnem})
		}
		if implicitMemWrite(in) {
			f.mem = append(f.mem, memWrite{insn: i, start: in.mnemStart, end: in.mnemStart + len(in.mnem), input: -1})
		}
	}
	f.pending = all
	return f
}

// firstObservable is the first pending write of fam the compiled code
// after the asm can see, nil when there is none.
func (m *stmtModel) firstObservable(f *stmtFacts, fam string) *regWrite {
	for _, w := range f.pending[fam] {
		if !m.unobservable(w.insn) {
			return w
		}
	}
	return nil
}

// saveRestore handles the instructions that save a register for later
// (push %rbx, xchg %ebx, %1, mov %rbx, %2) and the ones that put it back.
// A register restored to its saved value is not clobbered, which is what
// keeps the classic PIC-safe CPUID sequence from reporting %ebx. It
// returns true when the instruction was fully accounted for.
func (m *stmtModel) saveRestore(in *insn, i int, stack *[]string, saves *[]save,
	findSave func(fam, via string) int, restore func(string, int), write func(regWrite)) bool {
	// Only a copy of the whole register saves it: in 64-bit code
	// "xchgl %ebx, %1" ... "xchgl %ebx, %1" gives %rbx back with its upper
	// half zeroed, which is still a clobber.
	regOf := func(p int) string {
		if p >= len(in.ops) || in.ops[p].kind != oReg {
			return ""
		}
		op := &in.ops[p]
		switch {
		case isGPR(op.fam) && op.width >= min(in.mode, 64):
			return op.fam
		case (isXMM(op.fam) && op.width >= 128 || isMask(op.fam)) && vectorMoves[in.name]:
			return op.fam
		}
		return ""
	}
	name := in.name
	if vectorMoves[name] {
		name = "mov"
	}
	switch name {
	case "push":
		fam := ""
		if len(in.ops) == 1 {
			fam = regOf(0)
		}
		*stack = append(*stack, fam)
		if fam != "" {
			*saves = append(*saves, save{fam: fam, via: "push", insn: i})
		}
		return true
	case "pop":
		if len(in.ops) != 1 {
			return false
		}
		fam := regOf(0)
		top := ""
		if n := len(*stack); n > 0 {
			top = (*stack)[n-1]
			*stack = (*stack)[:n-1]
		}
		if fam != "" && top == fam {
			if k := findSave(fam, "push"); k >= 0 {
				restore(fam, (*saves)[k].insn)
				*saves = append((*saves)[:k], (*saves)[k+1:]...)
				return true
			}
		}
		return false
	case "xchg":
		if len(in.ops) != 2 {
			return false
		}
		for p := 0; p < 2; p++ {
			fam, other := regOf(p), in.ops[1-p]
			if fam == "" || other.kind == oReg && other.fam == fam {
				continue
			}
			if k := findSave(fam, other.text); k >= 0 {
				restore(fam, (*saves)[k].insn)
				*saves = append((*saves)[:k], (*saves)[k+1:]...)
				// Exchanging two saved registers back restores both.
				if fam2 := regOf(1 - p); fam2 != "" {
					if k2 := findSave(fam2, in.ops[p].text); k2 >= 0 {
						restore(fam2, (*saves)[k2].insn)
						*saves = append((*saves)[:k2], (*saves)[k2+1:]...)
						return true
					}
				}
				m.writeOther(in, i, 1-p, write)
				return true
			}
			*saves = append(*saves, save{fam: fam, via: other.text, insn: i})
		}
		return false
	case "mov":
		if len(in.ops) != 2 {
			return false
		}
		src, dst := regOf(0), regOf(1)
		if dst != "" && src != dst {
			if k := findSave(dst, in.ops[0].text); k >= 0 {
				restore(dst, (*saves)[k].insn)
				*saves = append((*saves)[:k], (*saves)[k+1:]...)
				return true
			}
		}
		if src != "" && dst != src && in.ops[1].kind != oMem {
			*saves = append(*saves, save{fam: src, via: in.ops[1].text, insn: i})
		}
	}
	return false
}

// vectorMoves are the whole-register moves of vector and mask registers,
// which save and restore them the way mov does a general register.
var vectorMoves = setOf("movdqa", "movdqu", "movaps", "movups", "movapd", "movupd",
	"vmovdqa", "vmovdqu", "vmovaps", "vmovups", "vmovapd", "vmovupd",
	"vmovdqa32", "vmovdqa64", "vmovdqu8", "vmovdqu16", "vmovdqu32", "vmovdqu64", "kmovq", "kmovw")

// writeOther records the write a restoring xchg makes to its other operand.
func (m *stmtModel) writeOther(in *insn, i, p int, write func(regWrite)) {
	op := &in.ops[p]
	if op.kind == oReg && op.fam != "" {
		write(regWrite{fam: op.fam, insn: i, start: op.start, end: op.end, mnem: in.mnem})
	}
}

// recordAccess notes every operand reference and literal register the
// instruction reads or writes.
func (m *stmtModel) recordAccess(f *stmtFacts, i int, in *insn, written map[int]bool) {
	last := len(in.ops) - 1
	for p := range in.ops {
		op := &in.ops[p]
		// The destination of a pure write is not read; everything else
		// (sources, read-modify-write destinations) is.
		read := !(written[p] && p == last && pureWrite(in))
		switch op.kind {
		case oRef:
			if op.ref >= 0 {
				f.access[op.ref] = append(f.access[op.ref], opAccess{insn: i, start: op.start, end: op.end, write: written[p], read: read})
			}
		case oReg:
			if op.fam != "" && read {
				f.reads = append(f.reads, regRead{fam: op.fam, insn: i, start: op.start, end: op.end})
			}
		case oMem:
			for _, r := range op.addrRefs {
				if r >= 0 {
					f.access[r] = append(f.access[r], opAccess{insn: i, start: op.start, end: op.end, read: true})
				}
			}
			for _, fam := range op.addrFams {
				f.reads = append(f.reads, regRead{fam: fam, insn: i, start: op.start, end: op.end})
			}
		}
	}
}

// unobservable reports whether every path from instruction i leaves the
// template through a jump or return before any other branch: the asm never
// returns to compiled code from there, so registers and memory it changes
// on the way out are nobody's concern (a kernel entry, a stack switch).
// An instruction the model does not know might branch, so it ends the
// search with false.
func (m *stmtModel) unobservable(i int) bool {
	for k := i; k < len(m.insns); k++ {
		in := &m.insns[k]
		switch {
		case !in.known:
			return false
		case in.leaves:
			return true
		case in.branch:
			return false
		}
	}
	return false
}
