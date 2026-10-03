package hw

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

// maxCandidates bounds how many path-dependent values a hover lists: past
// a handful the list says less than "unknown" does.
const maxCandidates = 4

// intHover is the BIOS service an int calls. When the paths into the int
// disagree about AH (boot.S's `movw $0x0201, %ax; jmp 4f` and
// `2: movb $0x42, %ah` meeting at `4: int $0x13`), each service is listed.
func (ix *Index) intHover(db *hwdb.DB, e event) string {
	if md := biosHover(db, e); md != "" || ix.fl == nil {
		return md
	}
	ahs, ok := Candidates(ix.fl, e.stmt, "ah", maxCandidates)
	if !ok || len(ahs) < 2 {
		return ""
	}
	var parts, names []string
	for _, ah := range ahs {
		if md := db.BIOSHover(uint64(e.n), ah, 0, false); md != "" {
			parts = append(parts, md)
			names = append(names, fmt.Sprintf("%02Xh", ah))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	head := fmt.Sprintf("AH depends on the path here: %s", strings.Join(names, " or "))
	return head + "\n\n" + strings.Join(parts, "\n\n---\n\n")
}

func cpuidSub(e event) uint64 {
	if e.c.full(32) {
		return e.c.val & 0xFFFFFFFF
	}
	return 0
}

// msrEventHover is rdmsr/wrmsr with its index known: the MSR, and for a
// wrmsr the value written, whole or the bits known to be set.
func msrEventHover(db *hwdb.DB, e event) string {
	if !e.c.full(32) {
		return ""
	}
	index := e.c.val & 0xFFFFFFFF
	var val *uint64
	if e.write && e.a.full(32) && e.d.full(32) {
		v := e.d.val<<32 | e.a.val&0xFFFFFFFF
		val = &v
	}
	md := db.MSRHover(index, val)
	if md == "" || !e.write || val != nil {
		return md
	}
	set := (e.d.set()&0xFFFFFFFF)<<32 | e.a.set()&0xFFFFFFFF
	if set == 0 {
		return md
	}
	note := fmt.Sprintf("writes a value with bits %#x known set", set)
	if index == eferIndex {
		if names := setNames("efer", set); names != "" {
			note += ": " + names
		}
	}
	return md + "\n\n" + note
}

// setNames names the fields of a decoded register value that are set.
func setNames(kind string, v uint64) string {
	d, ok := hwdb.Decode(kind, v)
	if !ok {
		return ""
	}
	var names []string
	for _, f := range d.Fields {
		names = append(names, "`"+f.Name+"`")
	}
	return strings.Join(names, " ")
}

// crWriteHover decodes what a mov to a control register (or xsetbv)
// writes: the whole value when it is known, else the bits known to be set
// and clear.
func crWriteHover(e event, mode asm.Mode) string {
	kind := crKind[e.cr]
	if e.cr == "cr3" {
		kind = "cr3"
	}
	if kind == "" {
		return ""
	}
	name := strings.ToUpper(kind)
	v := e.src
	width := uint(32)
	if e.cr == "xcr0" {
		v = rv{known: e.d.known<<32 | e.a.known&0xFFFFFFFF, val: e.d.val<<32 | e.a.val&0xFFFFFFFF}
		width = 64
	} else if mode == asm.Mode64 {
		width = 64
	}
	m := maskW(width)
	if v.full(width) {
		var b strings.Builder
		fmt.Fprintf(&b, "**%s** value written: %#x\n\n", name, v.val&m)
		if d, ok := hwdb.Decode(kind, v.val&m); ok {
			writeDecoded(&b, d)
		}
		return b.String()
	}
	set, clear := v.set()&m, v.known&^v.val&m
	if set == 0 && clear == 0 {
		return ""
	}
	var parts []string
	if set != 0 {
		p := fmt.Sprintf("bits %#x known set", set)
		if n := setNames(kind, set); n != "" {
			p += " (" + n + ")"
		}
		parts = append(parts, p)
	}
	if clear != 0 {
		p := fmt.Sprintf("bits %#x known clear", clear)
		if n := setNames(kind, clear); n != "" && kind != "cr3" {
			p += " (" + n + ")"
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("**%s** write: %s", name, strings.Join(parts, "; "))
}

// regHover is a register operand's known value before the statement and,
// when the statement changes it, after: "`%dx` = 0x3fd (COM1 LSR)". Values
// the paths disagree about are listed per path.
func (ix *Index) regHover(db *hwdb.DB, i int, reg string) string {
	if ix.fl == nil || !ix.fl.Reachable(i) {
		return ""
	}
	fam, lo, hi, ok := flow.RegOf(reg)
	if !ok || !fam.HasValue() {
		return ""
	}
	w := hi - lo + 1
	before := ix.fl.Before(i)
	after := ix.fl.After(i)
	v, m := before.Part(fam, lo, hi)
	var av, am uint64
	if after != nil {
		av, am = after.Part(fam, lo, hi)
	}
	var lines []string
	vals, multi := Candidates(ix.fl, i, reg, maxCandidates)
	switch {
	case multi && len(vals) > 1:
		var xs []string
		for _, x := range vals {
			xs = append(xs, fmt.Sprintf("`%#x`", x))
		}
		lines = append(lines, fmt.Sprintf("`%%%s` = %s, depending on the path", reg, strings.Join(xs, " or ")))
	case m != 0:
		lines = append(lines, fmt.Sprintf("`%%%s` = `%s`%s", reg, ValueText(v, m, w), ix.meaning(db, i, fam, lo, v, m, w, false)))
	}
	if am != 0 && (av != v || am != m) {
		lines = append(lines, fmt.Sprintf("after: `%%%s` = `%s`%s", reg, ValueText(av, am, w), ix.meaning(db, i, fam, lo, av, am, w, true)))
	}
	return strings.Join(lines, "\n\n")
}

// meaning names what a value is for: the port an in/out at the statement
// uses it as (or, after a write, the consumer it feeds), the MSR an
// rdmsr/wrmsr names, the BIOS service an int calls, the bits set in a
// control register.
func (ix *Index) meaning(db *hwdb.DB, i int, fam flow.Reg, lo int, v, m uint64, w int, after bool) string {
	full := m == widthMask(w)
	if fam.IsControl() {
		kind := strings.ToLower(fam.String())
		if kind == "cr3" || kind == "cr2" || kind == "cr8" {
			return ""
		}
		if n := setNames(kind, v&m); n != "" {
			return " (" + n + " set)"
		}
		return ""
	}
	if !full {
		return ""
	}
	var evs []int
	if after {
		for _, r := range ix.trk.roles[i] {
			evs = append(evs, r.ev)
		}
	} else {
		evs = ix.evAt[i]
	}
	for _, k := range evs {
		e := ix.trk.events[k]
		switch {
		case e.kind == evPort && !e.imm && fam == flow.RDX && w == 16 && lo == 0:
			if h, ok := db.PortFor(v, e.write); ok {
				return " (" + h.Name() + ")"
			}
		case e.kind == evMSR && fam == flow.RCX && w >= 32:
			if name, _, ok := db.MSR(v & 0xFFFFFFFF); ok {
				return " (" + name + ")"
			}
		case e.kind == evInt && fam == flow.RAX && w == 8 && lo == 8:
			if name, _, ok := db.BIOS(uint64(e.n), v); ok {
				return " (" + name + ")"
			}
		case e.kind == evCPUID && fam == flow.RAX && w >= 32:
			return fmt.Sprintf(" (CPUID leaf %#x)", v)
		}
	}
	return ""
}
