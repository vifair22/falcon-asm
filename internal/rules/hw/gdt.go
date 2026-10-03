package hw

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

// DescTable is a descriptor table found in the document: a GDT (or IDT)
// whose address a pseudo-descriptor (`gdt_desc: .word limit; .long gdt`)
// hands to lgdt (lidt), or, for hover only, a run of quadwords shaped like
// one (a null descriptor followed by present code and data descriptors).
type DescTable struct {
	IDT   bool
	Label string // "" for a table found by shape with no label
	Run   int
	Off   int64 // the table's first byte within Run
	// Loaded is set when an lgdt/lidt in this document loads the table;
	// only those are used to resolve selectors.
	Loaded     bool
	LoadStmt   int
	DescStmt   int // the statement holding the limit
	Limit      int64
	LimitKnown bool
	Entries    []Entry
}

// Entry is one descriptor (or gate) of a table.
type Entry struct {
	Index int    // selector index (offset / 8)
	Off   int64  // offset within the run
	Size  int    // 8, or 16 for a long-mode system descriptor or gate
	Lo    uint64 // the first quadword, when Known
	Hi    uint64 // the second quadword of a 16-byte entry, when Known
	Known bool
	Stmt  int // the statement emitting the entry's first byte
	Last  int // the statement emitting its last byte
}

// maxTable bounds how much of a run is read as a table: a full GDT or IDT
// is 64 KiB, real ones are a few dozen bytes, and a hover must stay fast.
const maxTable = 4096

func (ix *Index) findTables() {
	seen := map[string]*DescTable{}
	descs := map[string]bool{}
	for i := range ix.Prog.Stmts {
		s := &ix.Prog.Stmts[i]
		if s.Kind != asm.Instr || s.Inst == nil || len(s.Ops) != 1 {
			continue
		}
		idt := false
		switch s.Inst.Name {
		case "lgdt":
		case "lidt":
			idt = true
		default:
			continue
		}
		op := &s.Ops[0]
		desc := strings.TrimSpace(op.Disp)
		if op.Kind != asm.OpMem || op.Base != "" || op.Index != "" || !ix.isLabel(desc) {
			continue
		}
		key := desc
		if idt {
			key += "/idt"
		}
		if descs[key] {
			continue
		}
		t := ix.pseudoDescriptor(desc, idt)
		if t == nil {
			continue
		}
		descs[key] = true
		t.LoadStmt = i
		if t.Label != "" {
			seen[t.Label] = t
		}
		long := s.Mode == asm.Mode64
		ix.fillEntries(t, long)
		ix.tables = append(ix.tables, t)
	}
	ix.findByShape(seen)
}

// pseudoDescriptor reads `.word limit` and `.long base` (or `.quad base`)
// at label desc. The base must be a label of this document for the table to
// be found; an installed base (`.long 0`, patched at run time) leaves only
// the limit, which gdt-limit still checks.
func (ix *Index) pseudoDescriptor(desc string, idt bool) *DescTable {
	es := ix.lay.elemsFrom(desc, 10)
	if len(es) < 2 || es[0].Size != 2 {
		return nil
	}
	t := &DescTable{IDT: idt, Loaded: true, DescStmt: es[0].Stmt, Run: -1}
	if es[0].Known {
		t.Limit, t.LimitKnown = int64(es[0].Val), true
	}
	b := es[1]
	if b.Sym != "" && b.Addend == 0 && (b.Size == 4 || b.Size == 8) && ix.isLabel(b.Sym) {
		if p, ok := ix.lay.labels[b.Sym]; ok && p.run >= 0 {
			t.Label, t.Run, t.Off = b.Sym, p.run, p.off
		}
	}
	return t
}

// fillEntries splits the table's bytes into descriptors. In 64-bit code a
// system descriptor (TSS, LDT, call gate) and every IDT gate take 16 bytes.
func (ix *Index) fillEntries(t *DescTable, long bool) {
	if t.Run < 0 {
		return
	}
	n := int64(maxTable)
	if t.LimitKnown && t.Limit+1 < n {
		n = t.Limit + 1
	}
	es := ix.lay.elemsAt(pos{t.Run, t.Off}, n)
	data, known := bytesOf(es, t.Off)
	for off := int64(0); off+8 <= int64(len(data)); {
		e := Entry{Index: int(off / 8), Off: t.Off + off, Size: 8, Known: allKnown(known[off : off+8])}
		e.Lo = le64(data[off : off+8])
		size := int64(8)
		if long && (t.IDT || e.Known && e.Lo != 0 && e.Lo>>44&1 == 0 && sys64[int(e.Lo>>40&0xF)]) {
			size = 16
		}
		if off+size > int64(len(data)) {
			size = 8
		}
		if size == 16 {
			e.Size = 16
			e.Hi = le64(data[off+8 : off+16])
			e.Known = e.Known && allKnown(known[off+8:off+16])
		}
		e.Stmt = ix.lay.elemStmtAt(t.Run, e.Off)
		e.Last = ix.lay.elemStmtAt(t.Run, e.Off+size-1)
		t.Entries = append(t.Entries, e)
		off += size
	}
}

// sys64 are the system types that are 16 bytes in IA-32e mode.
var sys64 = map[int]bool{2: true, 9: true, 11: true, 12: true}

func allKnown(k []bool) bool {
	for _, b := range k {
		if !b {
			return false
		}
	}
	return len(k) > 0
}

func le64(b []byte) uint64 {
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// findByShape adds, for hover, quadword runs that look like a descriptor
// table and were not found through an lgdt: a label with "gdt" in its name
// followed by quadwords, or an unlabeled run starting with the null
// descriptor whose other entries are all present code or data descriptors.
func (ix *Index) findByShape(seen map[string]*DescTable) {
	covered := map[[2]int64]bool{}
	for _, t := range ix.tables {
		if t.Run >= 0 {
			covered[[2]int64{int64(t.Run), t.Off}] = true
		}
	}
	for run, es := range ix.lay.elems {
		for k := 0; k < len(es); k++ {
			e := es[k]
			if e.Size != 8 || covered[[2]int64{int64(run), e.Off}] {
				continue
			}
			label := ix.labelAt(e.Stmt)
			named := label != "" && strings.Contains(strings.ToLower(label), "gdt") && seen[label] == nil
			if !named && !(e.Known && e.Val == 0 && ix.descriptorRun(es[k:])) {
				continue
			}
			if !named && k > 0 && es[k-1].Size == 8 && es[k-1].Off+8 == e.Off {
				continue // inside a longer quadword run
			}
			t := &DescTable{Label: label, Run: run, Off: e.Off}
			end := k
			for end < len(es) && es[end].Size == 8 && es[end].Off == e.Off+int64(end-k)*8 {
				end++
			}
			t.Limit, t.LimitKnown = int64(end-k)*8-1, true
			ix.fillEntries(t, false)
			ix.tables = append(ix.tables, t)
			covered[[2]int64{int64(run), e.Off}] = true
			k = end - 1
		}
	}
}

// descriptorRun reports whether quadwords following a zero quadword read as
// descriptors: at least two, every one known, present, with S set (code or
// data), which random 64-bit constants almost never are.
func (ix *Index) descriptorRun(es []Elem) bool {
	n := 0
	for k := 1; k < len(es) && es[k].Size == 8 && es[k].Off == es[0].Off+int64(k)*8; k++ {
		e := es[k]
		if !e.Known {
			return false
		}
		if e.Val == 0 {
			continue
		}
		d := hwdb.ParseDescriptor(e.Val, nil)
		if !d.P || !d.S {
			return false
		}
		n++
	}
	return n >= 2
}

// labelAt is the first label statement i defines, or "".
func (ix *Index) labelAt(i int) string {
	if i < 0 || i >= len(ix.Prog.Stmts) {
		return ""
	}
	s := &ix.Prog.Stmts[i]
	for _, l := range s.Labels {
		if ix.isLabel(l) {
			return l
		}
	}
	// A label alone on the line above the data.
	if i > 0 {
		p := &ix.Prog.Stmts[i-1]
		if p.Kind == asm.Label {
			for _, l := range p.Labels {
				if ix.isLabel(l) {
					return l
				}
			}
		}
	}
	return ""
}

// GDT returns the single GDT the document loads. Selector checks need to
// know which table a selector indexes; with two different tables loaded
// (a loader with a temporary GDT) that is not knowable here.
func (ix *Index) GDT() *DescTable {
	var g *DescTable
	for _, t := range ix.tables {
		if t.IDT || !t.Loaded || t.Run < 0 {
			continue
		}
		if g != nil && g.Label != t.Label {
			return nil
		}
		g = t
	}
	return g
}

// entryAt returns the entry of t a selector index names.
func (t *DescTable) entryAt(index int) (Entry, bool) {
	for _, e := range t.Entries {
		if e.Index == index {
			return e, true
		}
	}
	return Entry{}, false
}

// tableOf returns the table and entry whose bytes statement i emits.
func (ix *Index) tableOf(i int) (*DescTable, Entry, bool) {
	p := ix.lay.stmt[i]
	if p.run < 0 {
		return nil, Entry{}, false
	}
	for _, t := range ix.tables {
		if t.Run != p.run {
			continue
		}
		for _, e := range t.Entries {
			if i >= e.Stmt && i <= e.Last && e.Stmt >= 0 {
				return t, e, true
			}
		}
	}
	return nil, Entry{}, false
}

// decode is the descriptor or gate an entry holds.
func (t *DescTable) decode(e Entry, long bool) *hwdb.Decoded {
	var hi *uint64
	if e.Size == 16 {
		h := e.Hi
		hi = &h
	}
	if t.IDT {
		return hwdb.DecodeGate(e.Lo, hi)
	}
	return hwdb.DecodeDescriptor(e.Lo, hi, long)
}
