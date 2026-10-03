package hw

import (
	"fmt"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Values across labels. The block-local tracker (regs.go) is exact inside
// a block and blind past a label; the dataflow solution (internal/flow) is
// what reaches across them: `movb $0x42, %ah` behind label 2: feeding the
// `int $0x13` after label 4:, `movw $port, %dx` before a polling loop. When
// the caller has a flow result, every consumer event the tracker recorded is
// widened with the bits flow knows that the tracker does not, and the
// statement flow names as the register's last writer gets the event as a
// role (so hover on its immediate says what it was for). Bits both know
// are left as the tracker has them: inside a block the two agree, and the
// tracker is the one written for these consumers.
//
// Without a flow result (an inline asm template whose flow the caller did
// not build, a macro expansion) the index is the block-local one, exactly
// as before.

// NewIndexFlow builds the index of p, widening its constants with the
// dataflow solution res; res may be nil, which is NewIndex.
func NewIndexFlow(p *asm.Program, res *flow.Result) *Index {
	return newIndex(p, res)
}

// Flow is the dataflow solution the index was built with; nil when none.
func (ix *Index) Flow() *flow.Result { return ix.fl }

// applyFlow widens the recorded events with flow's values and adds the
// consumers only flow can see (an xsetbv whose ECX was set before a label).
func (ix *Index) applyFlow() {
	res := ix.fl
	if res == nil || res.CFG == nil || res.CFG.Prog != ix.Prog {
		ix.fl = nil
		return
	}
	for k := range ix.trk.events {
		e := &ix.trk.events[k]
		if !res.Reachable(e.stmt) {
			continue
		}
		switch e.kind {
		case evInt:
			e.a = ix.widen(k, e.a, "eax", "a")
		case evPort:
			if e.write {
				e.a = ix.widen(k, e.a, "eax", "a")
			} else {
				e.a = ix.widen(k, e.a, "eax", "")
			}
			if !e.imm {
				e.d = ix.widen(k, e.d, "dx", "d")
			}
		case evMSR:
			role := ""
			if e.write {
				role = "a"
			}
			e.a = ix.widen(k, e.a, "eax", role)
			e.c = ix.widen(k, e.c, "ecx", "c")
			if e.write {
				role = "d"
			}
			e.d = ix.widen(k, e.d, "edx", role)
		case evCPUID:
			e.a = ix.widen(k, e.a, "eax", "a")
			e.c = ix.widen(k, e.c, "ecx", "c")
		case evCR:
			if e.cr == "xcr0" {
				e.a = ix.widen(k, e.a, "eax", "a")
				e.d = ix.widen(k, e.d, "edx", "d")
			} else if e.srcName != "" {
				e.src = ix.widen(k, e.src, e.srcName, "src")
			}
		case evSeg:
			if e.srcName != "" {
				e.src = ix.widen(k, e.src, e.srcName, "src")
			}
		}
	}
	for i := range ix.Prog.Stmts {
		s := &ix.Prog.Stmts[i]
		if s.Kind != asm.Instr || s.Inst == nil || s.Inst.Name != "xsetbv" || !res.Reachable(i) {
			continue
		}
		if _, ok := ix.eventAt(i, evCR); ok {
			continue
		}
		if c, ok := res.KnownAt(i, "ecx"); !ok || c != 0 {
			continue
		}
		ix.trk.events = append(ix.trk.events, event{kind: evCR, stmt: i, cr: "xcr0", c: rv{known: ^uint64(0)}})
		k := len(ix.trk.events) - 1
		e := &ix.trk.events[k]
		e.a = ix.widen(k, e.a, "eax", "a")
		e.d = ix.widen(k, e.d, "edx", "d")
		ix.evAt[i] = append(ix.evAt[i], k)
	}
}

// widen adds what flow knows of reg before the event's statement to v, and
// records the role of the statement flow names as reg's last writer.
func (ix *Index) widen(ev int, v rv, reg, roleName string) rv {
	stmt := ix.trk.events[ev].stmt
	val, mask, ok := ix.fl.ValueAt(stmt, reg)
	if ok {
		add := mask &^ v.known
		v.known |= add
		v.val = v.val&^add | val&add
	}
	if roleName == "" {
		return v
	}
	fam, _, _, found := flow.RegOf(reg)
	st := ix.fl.Before(stmt)
	if !found || st == nil {
		return v
	}
	defs := []int{st.Get(fam).Def}
	if defs[0] == flow.DefMerged {
		// The paths disagree about the writer; each predecessor's own
		// writer fed this consumer on its path.
		defs = predDefs(ix.fl, stmt, fam)
	}
	for _, def := range defs {
		if def < 0 || def >= stmt || contains(v.writers, def) {
			continue
		}
		v.writers = append(append([]int(nil), v.writers...), def)
		ix.trk.roles[def] = append(ix.trk.roles[def], role{ev: ev, reg: roleName})
	}
	return v
}

// predDefs are the last writers of fam on each path into stmt's block,
// when the block itself does not write it before stmt.
func predDefs(res *flow.Result, stmt int, fam flow.Reg) []int {
	b := res.CFG.BlockOf[stmt]
	if b < 0 {
		return nil
	}
	bl := &res.CFG.Blocks[b]
	if def := res.Before(stmt).Get(fam).Def; def >= bl.First && def < stmt {
		return nil
	}
	var out []int
	for _, p := range bl.Pred {
		if p >= 0 && p < len(res.Out) && res.Out[p].Reachable {
			out = append(out, res.Out[p].Get(fam).Def)
		}
	}
	return out
}

func contains(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// peAt reports the CR0.PE bit flow knows before statement i.
func (ix *Index) peAt(i int) (pe, known bool) {
	if ix.fl == nil {
		return false, false
	}
	v, m, ok := ix.fl.ValueAt(i, "cr0")
	if !ok || m&1 == 0 {
		return false, false
	}
	return v&1 != 0, true
}

// flowSym is the label a register holds before statement i according to
// flow's last writer: `movw $dap, %si` before a label, then the int after
// it. "" when the writer is not such a move.
func (ix *Index) flowSym(i int, reg string) string {
	if ix.fl == nil {
		return ""
	}
	fam, _, _, ok := flow.RegOf(reg)
	st := ix.fl.Before(i)
	if !ok || st == nil || !st.Reachable {
		return ""
	}
	def := st.Get(fam).Def
	if def < 0 || def >= len(ix.Prog.Stmts) {
		return ""
	}
	s := &ix.Prog.Stmts[def]
	if s.Kind != asm.Instr || s.Inst == nil || len(s.Ops) != 2 || s.Ops[1].Kind != asm.OpReg {
		return ""
	}
	if dfam, _, _, ok := flow.RegOf(s.Ops[1].RegName); !ok || dfam != fam {
		return ""
	}
	src := &s.Ops[0]
	switch s.Inst.Name {
	case "mov":
		if name := strings.TrimSpace(src.Imm); src.Kind == asm.OpImm && ix.isLabel(name) {
			return name
		}
	case "lea":
		if name := strings.TrimSpace(src.Disp); src.Kind == asm.OpMem && src.Base == "" && src.Index == "" && ix.isLabel(name) {
			return name
		}
	}
	return ""
}

// Candidates returns the values reg may hold before statement stmt: the
// one value flow knows, or, when the paths into stmt's block disagree and
// the block does not write reg before stmt, one value per predecessor
// (`movw $0x0201, %ax; jmp 4f` and `2: movb $0x42, %ah` meeting at
// `4: int $0x13`). ok is false when any path leaves reg not fully known,
// when the block can be entered from outside the document, or when there
// are more than max distinct values.
func Candidates(res *flow.Result, stmt int, reg string, max int) (vals []uint64, ok bool) {
	if res == nil || res.CFG == nil || stmt < 0 || stmt >= len(res.CFG.BlockOf) {
		return nil, false
	}
	fam, lo, hi, found := flow.RegOf(reg)
	if !found || !fam.HasValue() || !res.Reachable(stmt) {
		return nil, false
	}
	full := widthMask(hi - lo + 1)
	if v, m, known := res.ValueAt(stmt, reg); known && m == full {
		return []uint64{v}, true
	}
	b := res.CFG.BlockOf[stmt]
	if b < 0 {
		return nil, false
	}
	bl := &res.CFG.Blocks[b]
	st := res.Before(stmt)
	if def := st.Get(fam).Def; def >= bl.First && def < stmt || bl.Entry || len(bl.Pred) == 0 {
		return nil, false
	}
	seen := map[uint64]bool{}
	for _, p := range bl.Pred {
		if p < 0 || p >= len(res.Out) || !res.Out[p].Reachable {
			continue
		}
		out := res.Out[p]
		v, m := out.Part(fam, lo, hi)
		if m != full {
			return nil, false
		}
		if !seen[v] {
			seen[v] = true
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 || len(vals) > max {
		return nil, false
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	return vals, true
}

func widthMask(w int) uint64 {
	if w >= 64 {
		return ^uint64(0)
	}
	return uint64(1)<<uint(w) - 1
}

// ValueText renders what is known of a w-bit value: the hex number when
// every bit is known, otherwise a nibble pattern with ? for the unknown
// nibbles ("0x??42" for AH known in AX) and the exact mask when a nibble
// is only partly known.
func ValueText(v, mask uint64, w int) string {
	full := widthMask(w)
	mask &= full
	if mask == full {
		return fmt.Sprintf("%#x", v&full)
	}
	n := (w + 3) / 4
	var b strings.Builder
	partial := false
	for k := n - 1; k >= 0; k-- {
		nm := mask >> (4 * uint(k)) & 0xF
		switch nm {
		case 0xF:
			fmt.Fprintf(&b, "%X", v>>(4*uint(k))&0xF)
		default:
			if nm != 0 {
				partial = true
			}
			b.WriteByte('?')
		}
	}
	s := "0x" + b.String()
	if partial {
		s += fmt.Sprintf(" (bits %#x known: %#x)", mask, v&mask)
	}
	return s
}

// IndexOf is the document's index as the hardware rules see it (shared
// through the lint memo, with the run's flow result), for rule packages
// that build on it: the boot protocol checks read its GDT.
func IndexOf(c *lint.Context) *Index { return indexOf(c) }

// Descriptor decodes the GDT entry a constant selector names in the single
// GDT the document loads, and returns the statement emitting it (for a
// finding's Related list). ok is false for the null or an LDT selector, no
// GDT, or an entry that is not laid out with constants.
func (ix *Index) Descriptor(sel int64) (d hwdb.Descriptor, stmt int, ok bool) {
	g := ix.GDT()
	if g == nil || sel&4 != 0 || sel&^3 == 0 {
		return hwdb.Descriptor{}, -1, false
	}
	e, found := g.entryAt(int(sel >> 3))
	if !found || !e.Known || e.Lo == 0 {
		return hwdb.Descriptor{}, -1, false
	}
	return hwdb.ParseDescriptor(e.Lo, nil), e.Stmt, true
}
