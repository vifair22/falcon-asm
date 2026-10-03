package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

// pageTable is a paging structure found in the document: a label whose
// address reaches CR3 (`movl $pml4, %eax; movl %eax, %cr3`), or one that an
// entry of such a table points at (`.quad pdpt + 3`). kinds lists the entry
// kind at this level under each paging mode the document may use: one with
// 4-byte entries (32-bit paging) or 64-bit code in the file (4-level
// paging), two when 8-byte entries could be PAE or 4-level, in which case a
// bit only counts as reserved when it is reserved under both.
type pageTable struct {
	label string
	kinds []string
	elems []Elem
}

var (
	ladder32  = []string{"pde32", "pte32"}
	ladderPAE = []string{"pae-pdpte", "pae-pde", "pae-pte"}
	ladder4   = []string{"pml4e", "pdpte", "pde", "pte"}
)

// maxPageTables bounds the walk; a boot loader identity-maps with a
// handful of tables.
const maxPageTables = 64

func (ix *Index) findPageTables() {
	ix.pages = map[string]*pageTable{}
	for _, e := range ix.trk.events {
		if e.kind != evCR || e.cr != "cr3" || e.src.sym == "" {
			continue
		}
		ix.walkPages(e.src.sym)
	}
}

func (ix *Index) walkPages(root string) {
	type item struct {
		label string
		depth int
	}
	es := ix.lay.elemsFrom(root, 4096)
	if len(es) == 0 {
		return
	}
	var ladders [][]string
	switch {
	case es[0].Size == 4:
		ladders = [][]string{ladder32}
	case es[0].Size == 8 && ix.code64:
		ladders = [][]string{ladder4}
	case es[0].Size == 8:
		ladders = [][]string{ladder4, ladderPAE}
	default:
		return
	}
	queue := []item{{root, 0}}
	for len(queue) > 0 && len(ix.pages) < maxPageTables {
		it := queue[0]
		queue = queue[1:]
		if ix.pages[it.label] != nil {
			continue
		}
		var kinds []string
		for _, l := range ladders {
			if it.depth < len(l) {
				kinds = append(kinds, l[it.depth])
			}
		}
		if len(kinds) == 0 {
			continue
		}
		// A table ends at 4 KiB or at the next label, which starts the
		// next object (often the next table, laid out right after it).
		pt := &pageTable{label: it.label, kinds: kinds, elems: untilLabel(ix, ix.lay.elemsFrom(it.label, 4096))}
		ix.pages[it.label] = pt
		for _, e := range pt.elems {
			// A large page (PS) points at memory, not at a table.
			if e.Sym != "" && e.Addend&1 == 1 && e.Addend&0x80 == 0 && ix.isLabel(e.Sym) {
				queue = append(queue, item{e.Sym, it.depth + 1})
			}
		}
	}
}

// pageEntry returns the table and kinds for a data statement inside one.
func (ix *Index) pageEntries(i int) (*pageTable, []Elem) {
	p := ix.lay.stmt[i]
	if p.run < 0 {
		return nil, nil
	}
	for _, pt := range ix.pages {
		if len(pt.elems) == 0 {
			continue
		}
		first := pt.elems[0]
		if ix.lay.stmt[first.Stmt].run != p.run {
			continue
		}
		var out []Elem
		for _, e := range pt.elems {
			if e.Stmt == i {
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return pt, out
		}
	}
	return nil, nil
}

// entryValue is what an entry decodes as: its value, or for `table + flags`
// the flags alone (the address is the label's, unknown until link time).
func entryValue(e Elem) (uint64, bool) {
	switch {
	case e.Known:
		return e.Val, true
	case e.Sym != "":
		return uint64(e.Addend), true
	}
	return 0, false
}

// reservedPTE is the reserved bits of v under every kind.
func reservedPTE(kinds []string, v uint64) (uint64, string) {
	res := ^uint64(0)
	msg := ""
	for _, k := range kinds {
		d := hwdb.DecodePTE(k, v)
		res &= d.Reserved
		if d.Reserved != 0 && msg == "" {
			msg = strings.Join(d.Problems, "; ")
		}
	}
	return res, msg
}

// pteHover renders one entry.
func pteHover(pt *pageTable, e Elem) string {
	v, ok := entryValue(e)
	if !ok {
		return ""
	}
	var b strings.Builder
	for k, kind := range pt.kinds {
		d := hwdb.DecodePTE(kind, v)
		if k > 0 {
			b.WriteString("\n\nor, under PAE paging: ")
		}
		if e.Sym != "" {
			fmt.Fprintf(&b, "**%s** -> `%s`, flags %#x: %s", kind, e.Sym, v, d.Summary)
		} else {
			fmt.Fprintf(&b, "**%s** %#x: %s", kind, v, d.Summary)
		}
		for _, p := range d.Problems {
			fmt.Fprintf(&b, "\n\n- problem: %s", p)
		}
		for _, n := range d.Notes {
			fmt.Fprintf(&b, "\n\n- %s", n)
		}
	}
	return b.String()
}
