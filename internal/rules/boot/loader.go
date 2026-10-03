package boot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Rule 43: the sector count a loader passes to INT 13h against the size of
// the stage it loads. "Stage 2 grew past what stage 1 reads" fails only
// once the stage crosses a sector boundary, and then as a jump into the
// zeroes after the part that was read.
//
// The count: AL for AH=02h; the DAP's count word for AH=42h, when the
// packet is built as constants nothing in the file writes at run time. A
// read inside a loop that makes progress (the count of one call is not
// the stage's size) has no count to compare.
//
// The stage, and its size:
//   - `falcon-asm: loads=<label>`: from the label to <label>_end (or
//     <name>_end for <name>_start), or to the end of its section, from the
//     listing;
//   - `falcon-asm: loads=<file>`: a binary's size on disk, or an assembly
//     source's assembled size (its sections, assembled with its own flags
//     from the compile database);
//   - with no annotation, a boot sector that reads into the rest of its
//     own section: the bytes after the sector, when the read's
//     destination (ES:BX, or the DAP's segment:offset) is a constant
//     0x7C00 + n inside the section.
//
// When the count is not constant, a `<STAGE>_SECTORS` macro or .equ
// naming the stage (STAGE2_SECTORS for loads=stage2.S) is the count the
// loader is built for, and `falcon-asm: sectors=<n>` states it outright.
//
// A boot sector or an annotated loader whose fit cannot be shown gets one
// information finding (loader-sector-count-unproven) saying what is
// missing; anything else stays silent.

const sectorSize = 512

// bootLoad is where the BIOS loads a boot sector.
const bootLoad = 0x7C00

// readCall is one INT 13h read: where it is, its AH, and what is known of
// its count and destination.
type readCall struct {
	stmt  int
	ah    uint64
	count uint64
	known bool   // count is known
	why   string // why the count is unknown
	from  int    // statement the count comes from, -1 when none
	dest  uint64 // linear destination address
	destK bool
}

// annotation is the loader's falcon-asm items: loads=, sectors=.
type annotation struct {
	loads, sectors string
	line           int
}

func checkLoader(c *lint.Context, info bool) {
	f := factsOf(c)
	if f == nil || !f.hasInt13() {
		return
	}
	f = f.withBIOS(c)
	reads := f.reads()
	if len(reads) == 0 {
		return
	}
	ann, annotated := f.annotation()
	targets := f.loadTargets()
	if !annotated {
		// Without an annotation, only a boot sector that jumps to a fixed
		// address it did not assemble is a stage loader; one that only
		// chains to another boot sector at 0x7C00 loads a sector, which
		// a one-sector read covers.
		if !f.isBootSector() || len(targets) == 0 {
			return
		}
		chain := true
		for _, t := range targets {
			chain = chain && t == bootLoad
		}
		if chain {
			return
		}
	}
	st, stOK, stWhy := f.stage(c, ann, annotated, reads)
	if !stOK && !annotated && len(targets) > 0 {
		stWhy = fmt.Sprintf("nothing names the stage it loads and jumps to at %#x (add `falcon-asm: loads=<label or file>`)", targets[0])
	}
	reported := false
	for _, r := range reads {
		count, countOK, from, why := r.count, r.known, r.from, r.why
		if !countOK && stOK {
			if n, ok := f.declaredSectors(ann, st.name); ok {
				count, countOK, from, why = n, true, -1, ""
			}
		}
		switch {
		case countOK && stOK:
			if count*sectorSize < st.size && !info {
				need := (st.size + sectorSize - 1) / sectorSize
				msg := fmt.Sprintf("INT 13h AH=%02Xh reads %d sectors (%d bytes), but %s is %d bytes (%d sectors): the end of the stage is never loaded",
					r.ah, count, count*sectorSize, st.name, st.size, need)
				f.report(c, r.stmt, msg, from, st.stmt)
			}
		case info && !reported:
			reasons := []string{}
			if !stOK {
				reasons = append(reasons, stWhy)
			}
			if !countOK {
				reasons = append(reasons, why)
			}
			reported = true
			f.report(c, r.stmt, "cannot prove the loaded stage fits the INT 13h read: "+strings.Join(reasons, "; "))
		}
	}
}

// hasInt13 reports an `int $0x13` anywhere: the cheap gate before the
// loader's own analysis.
func (f *facts) hasInt13() bool {
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if instName(s) == "int" && len(s.Ops) == 1 && s.Ops[0].ImmVal.Known && s.Ops[0].ImmVal.V == 0x13 {
			return true
		}
	}
	return false
}

// reads lists the INT 13h calls that may be reads (AH=02h or 42h on some
// path).
func (f *facts) reads() []readCall {
	var out []readCall
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if instName(s) != "int" || len(s.Ops) != 1 || !s.Ops[0].ImmVal.Known || s.Ops[0].ImmVal.V != 0x13 || !f.res.Reachable(i) {
			continue
		}
		axs, ok := hw.Candidates(f.res, i, "ah", 4)
		if !ok {
			continue
		}
		loop := f.progressLoop(i)
		for _, ah := range axs {
			if ah != 0x02 && ah != 0x42 {
				continue
			}
			r := readCall{stmt: i, ah: ah, from: -1}
			switch {
			case loop:
				r.why = "the read runs in a loop that moves on after each call, so one call's count is not the stage's size"
			case ah == 0x02:
				f.chsCount(&r)
			default:
				f.dapCount(&r)
			}
			out = append(out, r)
		}
	}
	return out
}

// chsCount reads AL (and ES:BX) for an AH=02h read whose AH is known on
// every path: with AH and AL path-dependent, the AL of the AH=02h path is
// taken from AX.
func (f *facts) chsCount(r *readCall) {
	axs, ok := hw.Candidates(f.res, r.stmt, "ax", 4)
	n := 0
	if ok {
		for _, ax := range axs {
			if ax>>8 == 0x02 {
				r.count, n = ax&0xFF, n+1
			}
		}
	}
	if n != 1 {
		r.why = "AL (the sector count) is not a constant here"
		return
	}
	r.known = true
	r.from = f.defOf(r.stmt, flow.RAX)
	es, okES := f.res.KnownAt(r.stmt, "es")
	bx, okBX := f.res.KnownAt(r.stmt, "bx")
	if okES && okBX {
		r.dest, r.destK = es<<4+bx, true
	}
}

// dapCount reads the packet DS:SI names: its count word at offset 2, and
// its segment:offset buffer, when the packet is constants that no
// instruction of the file writes.
func (f *facts) dapCount(r *readCall) {
	label := f.regLabel(r.stmt, flow.RSI)
	if label == "" {
		r.why = "DS:SI does not name a packet laid out in this file"
		return
	}
	if f.writesNear(label) {
		r.why = fmt.Sprintf("the packet %s is written at run time", label)
		return
	}
	b, ok := f.dataBytes(label, 8)
	if !ok {
		r.why = fmt.Sprintf("the packet %s is not laid out as constants", label)
		return
	}
	r.count = uint64(b[2]) | uint64(b[3])<<8
	r.known = true
	r.from = f.p.Labels[label]
	off := uint64(b[4]) | uint64(b[5])<<8
	seg := uint64(b[6]) | uint64(b[7])<<8
	r.dest, r.destK = seg<<4+off, true
}

// defOf is the statement that last wrote fam before i, -1 when merged or
// unknown.
func (f *facts) defOf(i int, fam flow.Reg) int {
	st := f.res.Before(i)
	if st == nil {
		return -1
	}
	if d := st.Get(fam).Def; d >= 0 {
		return d
	}
	return -1
}

// regLabel is the label fam holds before i, from flow's last writer:
// `movw $dap, %si` or `leaw dap, %si`.
func (f *facts) regLabel(i int, fam flow.Reg) string {
	d := f.defOf(i, fam)
	if d < 0 {
		return ""
	}
	s := &f.p.Stmts[d]
	if len(s.Ops) != 2 || s.Ops[1].Kind != asm.OpReg {
		return ""
	}
	src := &s.Ops[0]
	var name string
	switch instName(s) {
	case "mov":
		if src.Kind == asm.OpImm {
			name = strings.TrimSpace(src.Imm)
		}
	case "lea":
		if src.Kind == asm.OpMem && src.Base == "" && src.Index == "" {
			name = strings.TrimSpace(src.Disp)
		}
	}
	if _, ok := f.p.Labels[name]; !ok {
		return ""
	}
	return name
}

// writesNear reports an instruction that may write the packet: a store
// naming its label, or any store through a register (which could point
// anywhere, the packet included).
func (f *facts) writesNear(label string) bool {
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.Kind != asm.Instr || len(s.Ops) == 0 {
			continue
		}
		dst := &s.Ops[len(s.Ops)-1]
		if dst.Kind != asm.OpMem {
			continue
		}
		switch instName(s) {
		case "cmp", "test", "push", "lgdt", "lidt", "bt", "lea", "nop", "jmp", "call", "ljmp", "lcall":
			continue
		}
		if dst.Base != "" || dst.Index != "" || strings.Contains(dst.Disp, label) {
			return true
		}
	}
	return false
}

// dataBytes lays out the first n bytes after label from .byte/.word/.long/
// .quad constants; false when any is not a constant.
func (f *facts) dataBytes(label string, n int) ([]byte, bool) {
	li, ok := f.p.Labels[label]
	if !ok {
		return nil, false
	}
	var out []byte
	for i := li; i < len(f.p.Stmts) && len(out) < n; i++ {
		s := &f.p.Stmts[i]
		if i > li && len(s.Labels) > 0 && s.Kind == asm.Label {
			continue
		}
		switch s.Kind {
		case asm.Empty, asm.Label:
			continue
		case asm.Directive:
		default:
			return nil, false
		}
		size := map[string]int{".byte": 1, ".word": 2, ".short": 2, ".hword": 2, ".2byte": 2, ".long": 4, ".int": 4, ".4byte": 4, ".quad": 8, ".8byte": 8}[s.Name]
		if size == 0 {
			if passive[s.Name] {
				continue
			}
			return nil, false
		}
		for _, a := range s.Args {
			v, ok := f.eval(a)
			if !ok {
				return nil, false
			}
			for k := 0; k < size; k++ {
				out = append(out, byte(v>>(8*uint(k))))
			}
		}
	}
	return out, len(out) >= n
}

// eval is a constant expression with the program's macros.
func (f *facts) eval(expr string) (uint64, bool) {
	expr = strings.TrimSpace(expr)
	for k := 0; k < 8; k++ {
		v, ok := f.p.Macros[expr]
		if !ok {
			break
		}
		expr = strings.TrimSpace(v)
	}
	expr = strings.TrimSuffix(strings.TrimSuffix(expr, "u"), "U")
	v, err := strconv.ParseUint(expr, 0, 64)
	return v, err == nil
}

// progressLoop reports a read whose call is repeated by a loop that does
// not only retry: control comes back to it from its success path (CF
// clear), or there is no CF check to tell a retry from progress.
func (f *facts) progressLoop(i int) bool {
	b := f.g.BlockOf[i]
	if b < 0 {
		return true
	}
	bl := &f.g.Blocks[b]
	// The CF branch closing the call's block, if any.
	starts := bl.Succ
	if bl.Exit == flow.ExitCond {
		j := f.p.Stmts[bl.Last].Inst.Name
		ok := -1
		switch j {
		case "jc", "jb", "jnae":
			ok = f.g.Fall(b)
		case "jnc", "jae", "jnb":
			ok = bl.Target
		}
		if ok >= 0 {
			starts = []int{ok}
		}
	}
	seen := map[int]bool{}
	queue := append([]int(nil), starts...)
	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]
		if x == b {
			return true
		}
		if x < 0 || seen[x] {
			continue
		}
		seen[x] = true
		queue = append(queue, f.g.Blocks[x].Succ...)
	}
	return false
}

// annotation finds `falcon-asm: loads=... sectors=...`.
func (f *facts) annotation() (annotation, bool) {
	var a annotation
	found := false
	for _, t := range f.p.Toks {
		if t.Kind != lexer.Comment {
			continue
		}
		_, rest, ok := strings.Cut(t.Text, "falcon-asm:")
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(strings.TrimSpace(rest), "*/")
		for _, it := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' }) {
			k, v, _ := strings.Cut(it, "=")
			switch strings.ToLower(k) {
			case "loads":
				a.loads, a.line, found = v, t.Line, true
			case "sectors":
				a.sectors, found = v, true
			}
		}
	}
	return a, found
}

// loadTargets are the linear addresses real-mode far jumps with a constant
// segment and offset go to (`ljmp $0, $STAGE2_LOAD`): code the document
// did not assemble, so code it loaded. The reset vector (FFFF:0000) is a
// reboot, not a stage.
func (f *facts) loadTargets() []uint64 {
	var out []uint64
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.Mode != asm.Mode16 || !s.IsFar() || !s.IsUncondJump() || len(s.Ops) != 2 || !f.res.Reachable(i) {
			continue
		}
		seg, off := &s.Ops[0], &s.Ops[1]
		if seg.Kind != asm.OpImm || off.Kind != asm.OpImm || !seg.ImmVal.Known || !off.ImmVal.Known {
			continue
		}
		if pe, known := f.peAt(i); known && pe {
			continue
		}
		lin := uint64(seg.ImmVal.V&0xFFFF)<<4 + uint64(off.ImmVal.V&0xFFFF)
		if lin != 0xFFFF0 {
			out = append(out, lin)
		}
	}
	return out
}

// isBootSector reports the 510-byte signature marker of a boot sector.
func (f *facts) isBootSector() bool {
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.Kind != asm.Directive {
			continue
		}
		var arg string
		switch {
		case s.Name == ".org" && len(s.Args) >= 1:
			arg = s.Args[0]
		case s.Name == ".set" && len(s.Args) == 2 && s.Args[0] == ".":
			arg = s.Args[1]
		default:
			continue
		}
		a := strings.ReplaceAll(arg, " ", "")
		if a == "510" || a == "0x1fe" || a == "0x1FE" || strings.HasSuffix(a, "+510") || strings.HasSuffix(a, "+0x1fe") || strings.HasSuffix(a, "+0x1FE") {
			return true
		}
	}
	return false
}

// stageInfo is the loaded stage.
type stageInfo struct {
	name string
	size uint64
	stmt int // the label, -1 for a file
}

// stage determines what the loader loads and how big it is.
func (f *facts) stage(c *lint.Context, a annotation, annotated bool, reads []readCall) (stageInfo, bool, string) {
	if annotated && a.loads != "" {
		if li, ok := f.p.Labels[a.loads]; ok {
			size, ok := f.labelSize(c, a.loads)
			if !ok {
				return stageInfo{}, false, fmt.Sprintf("the size of %s is not known from the listing", a.loads)
			}
			return stageInfo{name: a.loads, size: size, stmt: li}, true, ""
		}
		size, ok, why := fileSize(f.p, a.loads)
		if !ok {
			return stageInfo{}, false, why
		}
		return stageInfo{name: filepath.Base(a.loads), size: size, stmt: -1}, true, ""
	}
	// A boot sector reading into the rest of its own section.
	for _, r := range reads {
		if !r.destK || r.dest <= bootLoad || c.Listing == nil {
			continue
		}
		sec := f.p.Stmts[r.stmt].Section
		total, ok := c.Listing.SectionSizes[sec]
		off := r.dest - bootLoad
		if ok && off >= sectorSize && off < total {
			return stageInfo{name: fmt.Sprintf("the rest of section %s (from offset %#x)", sec, off), size: total - off, stmt: -1}, true, ""
		}
	}
	return stageInfo{}, false, "nothing names the stage it loads (add `falcon-asm: loads=<label or file>`)"
}

// labelSize is a label's extent from the listing: to <label>_end (or
// <name>_end for <name>_start), else to the end of its section.
func (f *facts) labelSize(c *lint.Context, label string) (uint64, bool) {
	if c.Listing == nil {
		return 0, false
	}
	li := f.p.Labels[label]
	sec := f.p.Stmts[li].Section
	start, ok := listingOffset(c.Listing, f.p.Stmts[li].Line, sec)
	if !ok {
		return 0, false
	}
	for _, end := range []string{label + "_end", strings.TrimSuffix(label, "_start") + "_end"} {
		ei, ok := f.p.Labels[end]
		if !ok || f.p.Stmts[ei].Section != sec {
			continue
		}
		e, ok := listingOffset(c.Listing, f.p.Stmts[ei].Line, sec)
		if !ok {
			// An end label after the last byte of the section has no
			// listed offset; the section size is where it is.
			e, ok = c.Listing.SectionSizes[sec]
		}
		if ok && e >= start {
			return e - start, true
		}
	}
	total, ok := c.Listing.SectionSizes[sec]
	if !ok || total < start {
		return 0, false
	}
	return total - start, true
}

// listingOffset is the section offset of the first emitting listing row at
// or after line.
func listingOffset(res *toolchain.AsmResult, line int, sec string) (uint64, bool) {
	for _, l := range res.Listing {
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

// declaredSectors is the count the loader is built for, when the count
// register is not a constant: `falcon-asm: sectors=<n or name>`, or a
// macro or .equ <STAGE>_SECTORS for the stage's base name.
func (f *facts) declaredSectors(a annotation, stage string) (uint64, bool) {
	if a.sectors != "" {
		if v, ok := f.eval(a.sectors); ok {
			return v, true
		}
	}
	base := strings.ToUpper(strings.TrimSuffix(stage, filepath.Ext(stage)))
	base = strings.TrimSuffix(base, "_START")
	if base == "" {
		return 0, false
	}
	return f.eval(base + "_SECTORS")
}

// Sizes of other files are cached by path, size and modification time:
// the rule runs on every lint of the loader, the stage changes rarely.
var sizeCache = struct {
	sync.Mutex
	m map[string]cachedSize
}{m: map[string]cachedSize{}}

type cachedSize struct {
	mod  time.Time
	len  int64
	size uint64
	ok   bool
	why  string
}

// assembleFile assembles an assembly source with its compile flags and
// returns its section sizes. Tests replace it.
var assembleFile = func(path string) (map[string]uint64, error) {
	db, err := toolchain.FindDB(path)
	if err != nil {
		return nil, err
	}
	flags, ok := db.Flags(path)
	if !ok {
		return nil, fmt.Errorf("no compile flags for %s", path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := (&toolchain.Runner{}).Assemble(ctx, path, flags)
	if err != nil {
		return nil, err
	}
	if res.SectionSizes == nil {
		return nil, fmt.Errorf("%s did not assemble", filepath.Base(path))
	}
	return res.SectionSizes, nil
}

// fileSize is the size of a stage given as a file, relative to the
// document's directory.
func fileSize(p *asm.Program, name string) (uint64, bool, string) {
	path := name
	if !filepath.IsAbs(path) {
		docPath, ok := toolchain.URIToPath(p.Doc.URI)
		if !ok {
			return 0, false, fmt.Sprintf("%s: the document has no path to resolve it against", name)
		}
		path = filepath.Join(filepath.Dir(docPath), name)
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0, false, fmt.Sprintf("loads=%s names neither a label of this file nor a file", name)
	}
	sizeCache.Lock()
	cs, hit := sizeCache.m[path]
	sizeCache.Unlock()
	if hit && cs.mod.Equal(st.ModTime()) && cs.len == st.Size() {
		return cs.size, cs.ok, cs.why
	}
	cs = cachedSize{mod: st.ModTime(), len: st.Size()}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".s", ".sx":
		secs, err := assembleFile(path)
		if err != nil {
			cs.why = fmt.Sprintf("%s could not be assembled to measure it (%v)", name, err)
			break
		}
		cs.size, cs.ok = loadedSize(secs), true
	default:
		cs.size, cs.ok = uint64(st.Size()), true
	}
	sizeCache.Lock()
	sizeCache.m[path] = cs
	sizeCache.Unlock()
	return cs.size, cs.ok, cs.why
}

// loadedSize sums the sections that end up in a raw image: everything
// with contents except symbol tables, relocations, notes and debug data
// (and .bss, which has no contents).
func loadedSize(secs map[string]uint64) uint64 {
	names := make([]string, 0, len(secs))
	for n := range secs {
		names = append(names, n)
	}
	sort.Strings(names)
	var total uint64
	for _, n := range names {
		switch {
		case n == "", strings.HasPrefix(n, ".rel"), strings.HasPrefix(n, ".note"), strings.HasPrefix(n, ".debug"),
			strings.HasPrefix(n, ".bss"), strings.HasPrefix(n, ".tbss"), strings.HasPrefix(n, ".eh_frame"), n == ".symtab",
			n == ".strtab", n == ".shstrtab", n == ".comment", n == ".group", strings.HasPrefix(n, ".gnu"):
			continue
		}
		total += secs[n]
	}
	return total
}

func init() {
	lint.Register(&lint.Rule{
		Code:         "loader-sector-count",
		Section:      5,
		Default:      lint.Warning,
		Langs:        lint.LangAsm,
		NeedsFlow:    true,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "INT 13h read of fewer sectors than the stage it loads.",
			Why: "A boot sector reads the next stage with INT 13h: AL sectors for AH=02h, the disk address packet's count for AH=42h. " +
				"When the stage grows past that many sectors nothing fails at build time; the loader jumps into a stage whose tail was " +
				"never read. The count is taken from the dataflow (AL, or a packet laid out as constants that nothing writes), and the " +
				"stage from `falcon-asm: loads=<label or file>` (a label of this file measured to its _end label in the listing, a binary " +
				"by its size, an assembly source assembled with its own flags) or, in a boot sector, from a read into the rest of its " +
				"own section. A count read from memory is replaced by `falcon-asm: sectors=<n>` or a <STAGE>_SECTORS macro when there is one.",
			Bad: "\t.code16\n_start:\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x0202, %ax\n\tmovw $0x0002, %cx\n\txorw %dx, %dx\n\tmovw $0x7E00, %bx\n\tint $0x13\n" +
				"\tljmp $0, $0x7E00\n\t. = _start + 510\n\t.byte 0x55, 0xAA\nstage2:\n\t.fill 1500, 1, 0x90",
			Good: "\t.code16\n_start:\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x0203, %ax\n\tmovw $0x0002, %cx\n\txorw %dx, %dx\n\tmovw $0x7E00, %bx\n\tint $0x13\n" +
				"\tljmp $0, $0x7E00\n\t. = _start + 510\n\t.byte 0x55, 0xAA\nstage2:\n\t.fill 1500, 1, 0x90",
		},
		Check: func(c *lint.Context) { checkLoader(c, false) },
	})
	lint.Register(&lint.Rule{
		Code:         "loader-sector-count-unproven",
		Section:      5,
		Default:      lint.Info,
		Langs:        lint.LangAsm,
		NeedsFlow:    true,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "Boot sector or annotated loader whose INT 13h read cannot be shown to cover the stage it loads.",
			Why: "loader-sector-count needs a constant sector count and a known stage size. When a boot sector reads with a count " +
				"from memory (patched in by an installer), inside a loop, or without anything naming the stage, the fit cannot be " +
				"proven and this says which part is missing. Information only: annotate with `falcon-asm: loads=<label or file>` " +
				"and, for a count from memory, `falcon-asm: sectors=<n>` to have it checked.",
			Bad: "\t.code16\n_start:\n\tmovb count, %al\n\tmovb $0x02, %ah\n\tint $0x13\n\tljmp $0, $0x7E00\ncount:\t.byte 0\n" +
				"\t. = _start + 510\n\t.byte 0x55, 0xAA",
			Good: "\t.code16\n_start:\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x0201, %ax\n\tmovw $0x7E00, %bx\n\tint $0x13\n\tljmp $0, $0x7E00\n" +
				"\t. = _start + 510\n\t.byte 0x55, 0xAA\nstage2:\n\t.fill 500, 1, 0x90",
		},
		Check: func(c *lint.Context) { checkLoader(c, true) },
	})
}
