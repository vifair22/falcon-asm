package hw

import (
	"fmt"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// marker is a location move to a fixed offset of a boot sector:
// `.org 510`, `.org _start + 440`, `. = _start + 510`.
type marker struct {
	stmt int
	sym  string // "" for an offset from the section start
	off  int64
}

// markers lists the location moves of the document whose target is a
// constant offset from a label of this document (or from the section).
func (ix *Index) markers() []marker {
	var out []marker
	for i := range ix.Prog.Stmts {
		s := &ix.Prog.Stmts[i]
		if s.Kind != asm.Directive || s.InMacroDef {
			continue
		}
		var expr string
		switch {
		case s.Name == ".org" && len(s.Args) >= 1:
			expr = s.Args[0]
		case s.Name == ".set" && len(s.Args) == 2 && s.Args[0] == ".":
			expr = s.Args[1]
		default:
			continue
		}
		if v, ok := ix.lay.evalConst(expr); ok {
			out = append(out, marker{stmt: i, off: v})
			continue
		}
		if sym, add, ok := ix.lay.symbolic(expr); ok && ix.isLabel(sym) {
			out = append(out, marker{stmt: i, sym: sym, off: add})
		}
	}
	return out
}

// bootSector is the 510 marker of a boot sector, when the document has one.
func (ix *Index) bootSector() (marker, bool) {
	for _, m := range ix.markers() {
		if m.off == 510 {
			return m, true
		}
	}
	return marker{}, false
}

// lineOffset is the listing offset of source line line in section sec: the
// first emitting row at or after it, since a label alone on its line emits
// nothing and has no offset of its own.
func lineOffset(ls []toolchain.ListingLine, line int, sec string) (uint64, bool) {
	for _, l := range ls {
		if l.Line < line || l.Section != sec || !l.HasOffset {
			continue
		}
		if l.Unknown {
			return 0, false
		}
		return l.Offset, true
	}
	return 0, false
}

// sectorStart is the listing offset of the sector's first byte: the
// marker's label, or the section start for `.org 510`.
func (ix *Index) sectorStart(ls []toolchain.ListingLine, m marker) (uint64, bool) {
	if m.sym == "" {
		return 0, true
	}
	li, ok := ix.labelStmt(m.sym)
	if !ok {
		return 0, false
	}
	return lineOffset(ls, ix.Prog.Stmts[li].Line, ix.Prog.Stmts[li].Section)
}

// signature reads the two bytes after the 510 marker: the elements the
// layout gives them, else the listing's bytes for the next emitting row
// (which has the macros cpp expanded).
func (ix *Index) signature(c *lint.Context, m marker) (b [2]byte, stmt int, ok bool) {
	stmt = -1
	for k := m.stmt + 1; k < len(ix.Prog.Stmts); k++ {
		if es := ix.lay.stmtElems(k); len(es) > 0 {
			stmt = k
			break
		}
		s := &ix.Prog.Stmts[k]
		if s.Kind == asm.Instr || s.Kind == asm.Directive && !silent[s.Name] && ix.lay.size[k] != 0 {
			stmt = k
			break
		}
	}
	if stmt < 0 {
		return b, -1, false
	}
	p := ix.lay.stmt[stmt]
	if p.run >= 0 {
		data, known := bytesOf(ix.lay.elemsAt(p, 2), p.off)
		if len(data) == 2 && known[0] && known[1] {
			return [2]byte{data[0], data[1]}, stmt, true
		}
	}
	if c.Listing != nil {
		for _, l := range c.Listing.Listing {
			if l.Line >= ix.Prog.Stmts[stmt].Line && l.HasOffset && len(l.Bytes) >= 2 {
				return [2]byte{l.Bytes[0], l.Bytes[1]}, stmt, true
			}
		}
	}
	return b, stmt, false
}

func checkBootSignature(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil {
		return
	}
	m, ok := ix.bootSector()
	if !ok {
		return
	}
	sig, stmt, ok := ix.signature(c, m)
	switch {
	case stmt < 0:
		c.ReportStmt(m.stmt, "boot sector with no signature after the move to offset 510: the BIOS boots a sector only when bytes 510-511 are 55h AAh (.byte 0x55, 0xAA or .word 0xAA55)")
		return
	case !ok:
		return
	case sig == [2]byte{0xAA, 0x55}:
		c.ReportStmt(stmt, "boot signature bytes are AA 55: .word 0x55AA is stored little-endian; write .word 0xAA55 or .byte 0x55, 0xAA")
		return
	case sig != [2]byte{0x55, 0xAA}:
		c.ReportStmt(stmt, fmt.Sprintf("boot signature bytes are %02X %02X, want 55 AA: the BIOS will not boot this sector", sig[0], sig[1]))
		return
	}
	// The bytes are right; with a listing, check they land at 510.
	if c.Listing == nil {
		return
	}
	ls := c.Listing.Listing
	start, ok1 := ix.sectorStart(ls, m)
	at, ok2 := lineOffset(ls, ix.Prog.Stmts[stmt].Line, ix.Prog.Stmts[stmt].Section)
	if ok1 && ok2 && at-start != 510 {
		c.ReportStmt(stmt, fmt.Sprintf("boot signature lands at offset %d of the sector, not 510", int64(at)-int64(start)))
	}
}

// Partition table area of an MBR: four 16-byte entries at 446.
const (
	ptStart = 446
	ptEnd   = 510
)

func checkMBROverlap(c *lint.Context) {
	ix := indexOf(c)
	if ix == nil || c.Listing == nil {
		return
	}
	m, ok := ix.bootSector()
	if !ok {
		return
	}
	isMBR := false
	ptMarker := -1
	for _, mk := range ix.markers() {
		if mk.sym != m.sym {
			continue
		}
		switch mk.off {
		case 440, 444:
			isMBR = true
		case ptStart:
			isMBR = true
			ptMarker = mk.stmt
		}
	}
	for i := range ix.Prog.Stmts {
		if _, ok := ix.note(i, "mbr"); ok {
			isMBR = true
			break
		}
	}
	if !isMBR {
		return
	}
	ls := c.Listing.Listing
	start, ok := ix.sectorStart(ls, m)
	if !ok {
		return
	}
	sec := ix.Prog.Stmts[m.stmt].Section
	reported := map[int]bool{}
	for k, l := range ls {
		if l.Section != sec || !l.HasOffset || l.Unknown || l.Offset < start {
			continue
		}
		size := uint64(len(l.Bytes))
		for _, n := range ls[k+1:] {
			if n.Section == sec && n.HasOffset {
				size = n.Offset - l.Offset
				break
			}
		}
		lo, hi := l.Offset-start, l.Offset-start+size
		if hi <= ptStart || lo >= ptEnd || size == 0 {
			continue
		}
		idx := ix.Prog.StmtAt(l.Line)
		if len(idx) == 0 {
			continue
		}
		si := idx[0]
		s := &ix.Prog.Stmts[si]
		// Padding from a location move is not code, and data placed after
		// an explicit move to 446 is the partition table itself.
		if s.Kind == asm.Directive && (s.Name == ".org" || s.Name == ".set" && len(s.Args) > 0 && s.Args[0] == ".") ||
			ptMarker >= 0 && si > ptMarker || si == m.stmt || reported[si] {
			continue
		}
		reported[si] = true
		c.ReportStmt(si, fmt.Sprintf("emits bytes %d-%d of the MBR, inside the partition table (446-509): partitioning tools overwrite them, and they corrupt the table",
			max(lo, ptStart), min(hi, ptEnd)-1))
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:         "boot-signature",
		Section:      5,
		Default:      lint.Warning,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "Boot sector whose bytes 510-511 are not 55h AAh.",
			Why: "The BIOS loads a boot sector and jumps to it only when its last two bytes are 55h AAh. The " +
				"usual mistake is `.word 0x55AA`, which x86 stores little-endian as AA 55, and the sector is " +
				"silently skipped. A file is taken as a boot sector when it moves to offset 510 (`.org 510`, " +
				"`. = _start + 510`); the signature must follow, and with the listing its offset from the start " +
				"label is checked to be 510.",
			Bad:  "_start:\n\tcli\n\t. = _start + 510\n\t.word 0x55AA",
			Good: "_start:\n\tcli\n\t. = _start + 510\n\t.word 0xAA55",
		},
		Check: checkBootSignature,
	})
	lint.Register(&lint.Rule{
		Code:         "mbr-partition-overlap",
		Section:      5,
		Default:      lint.Warning,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "Code or data of an MBR emitted into the partition table (bytes 446-509).",
			Why: "A master boot record shares its sector with the partition table: bytes 446-509 are the disk's, " +
				"written by fdisk and every installer, and 440-445 the disk signature. Boot code that grows into " +
				"them is overwritten when the disk is partitioned, or corrupts the table when the sector is " +
				"written. A boot sector is taken as an MBR when it places something at 440 or 446 relative to " +
				"its start, or carries a `falcon-asm: mbr` comment; data after an explicit move to 446 is the " +
				"partition table and is allowed. Offsets come from the assembler listing.",
			Bad:  "# falcon-asm: mbr\n_start:\n\t.fill 450, 1, 0x90\n\t. = _start + 510\n\t.byte 0x55, 0xAA",
			Good: "# falcon-asm: mbr\n_start:\n\t.fill 400, 1, 0x90\n\t. = _start + 510\n\t.byte 0x55, 0xAA",
		},
		Check: checkMBROverlap,
	})
}
