package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Rule 44: what real-mode code can reach. A real-mode address is
// segment * 16 + a 16-bit offset, so nothing above 0x10FFEF (FFFF:FFFF,
// the top of the HMA) is reachable, a 16-bit offset computed past 0xFFFF
// wraps to the bottom of the segment, and code whose instructions run past
// offset 0xFFFF of its code segment wraps IP to 0.
//
// real-mode-reach works from the dataflow: a memory operand in real-mode
// code whose segment and offset registers and displacement are all known.
// real-mode-layout needs the listing: a label referenced from real-mode
// code that the linker script places above the HMA, and real-mode code
// linked past offset 0xFFFF or spanning more than 64 KiB.
//
// Real mode is .code16 code that the dataflow does not prove runs with
// CR0.PE set, and that is not reached through a protected-mode far jump
// (thunk_16 in 16-bit protected mode is not real mode). A file that clears
// CR0.PE may be setting up unreal mode (segment limits raised past 64 KiB
// on purpose), so 32-bit offsets are not judged in one.

// hmaTop is the highest address real mode reaches: FFFF:FFFF.
const hmaTop = 0x10FFEF

// realBlocks marks the blocks that run in real mode as far as the CFG
// shows: .code16 blocks not reached from a far jump made with PE set.
func (f *facts) pm16() []bool {
	pm := make([]bool, len(f.g.Blocks))
	var queue []int
	for b := range f.g.Blocks {
		bl := &f.g.Blocks[b]
		if bl.Exit != flow.ExitFar || bl.Target < 0 {
			continue
		}
		j := bl.Last
		pe, known := f.peAt(j)
		if f.p.Stmts[j].Mode != asm.Mode16 && !(known && !pe) || known && pe {
			queue = append(queue, bl.Target)
		}
	}
	for len(queue) > 0 {
		b := queue[0]
		queue = queue[1:]
		if pm[b] {
			continue
		}
		pm[b] = true
		if f.gen(b, f.g.Blocks[b].Last+1)&evPEClear != 0 {
			continue // real mode from the clear on
		}
		bl := &f.g.Blocks[b]
		if bl.Exit == flow.ExitFar {
			continue // the next far jump decides for its own target
		}
		queue = append(queue, bl.Succ...)
	}
	return pm
}

func (f *facts) peAt(i int) (pe, known bool) {
	v, m, ok := f.res.ValueAt(i, "cr0")
	if !ok || m&1 == 0 {
		return false, false
	}
	return v&1 != 0, true
}

// realMode reports whether statement i runs in real mode.
func (f *facts) realMode(i int, pm []bool) bool {
	s := &f.p.Stmts[i]
	if s.Mode != asm.Mode16 {
		return false
	}
	if pe, known := f.peAt(i); known {
		return !pe
	}
	b := f.g.BlockOf[i]
	return b >= 0 && !pm[b]
}

var reg32 = map[string]bool{"eax": true, "ebx": true, "ecx": true, "edx": true, "esi": true, "edi": true, "ebp": true, "esp": true}

func hasPrefix(s *asm.Stmt, p string) bool {
	for _, x := range s.Prefixes {
		if x == p {
			return true
		}
	}
	return false
}

func checkReach(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	pm := f.pm16()
	unreal := f.any&evPEClear != 0
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.Kind != asm.Instr || s.InMacroDef || !f.res.Reachable(i) || !f.realMode(i, pm) {
			continue
		}
		switch name := instName(s); {
		case name == "lea" || name == "nop" || strings.HasPrefix(name, "prefetch") || s.IsBranch():
			continue
		}
		for k := range s.Ops {
			op := &s.Ops[k]
			if op.Kind != asm.OpMem || op.RIPRel {
				continue
			}
			f.reachOperand(c, i, k, op, unreal)
		}
	}
}

// reachOperand checks one memory operand whose address is fully known.
func (f *facts) reachOperand(c *lint.Context, i, k int, op *asm.Operand, unreal bool) {
	s := &f.p.Stmts[i]
	var disp uint64
	if strings.TrimSpace(op.Disp) != "" {
		if !op.DispVal.Known {
			return
		}
		disp = uint64(op.DispVal.V)
	}
	seg := op.Seg
	if seg == "" {
		seg = "ds"
		switch op.Base {
		case "bp", "ebp", "sp", "esp":
			seg = "ss"
		}
	}
	segV, ok := f.res.KnownAt(i, seg)
	if !ok {
		return
	}
	wide := hasPrefix(s, "addr32")
	off := disp
	regs := 0
	for _, r := range []string{op.Base, op.Index} {
		if r == "" {
			continue
		}
		regs++
		wide = wide || reg32[r]
		v, ok := f.res.KnownAt(i, r)
		if !ok {
			return
		}
		if r == op.Index && op.Scale > 1 {
			v *= uint64(op.Scale)
		}
		off += v
	}
	if !wide {
		if regs > 0 && off > 0xFFFF {
			c.ReportOp(i, k, fmt.Sprintf("the offset %#x + registers comes to %#x, which 16-bit addressing wraps to %#x: the access lands at the bottom of segment %#x, not past its end",
				disp, off, off&0xFFFF, segV))
		}
		return
	}
	off &= 0xFFFFFFFF
	if unreal {
		return
	}
	if lin := segV<<4 + off; lin > hmaTop {
		c.ReportOp(i, k, fmt.Sprintf("linear address %#x (%%%s %#x * 16 + offset %#x) is beyond what real mode reaches (%#x, FFFF:FFFF); "+
			"an offset past 0xFFFF faults with real mode's 64 KiB segment limit", lin, seg, segV, off, hmaTop))
	}
}

// --- real-mode-layout: link addresses from the listing and the linker
// script.

func checkLayout(c *lint.Context) {
	f := factsOf(c)
	if f == nil || c.Listing == nil {
		return
	}
	pm := f.pm16()
	addrs := sectionAddresses(f.p)
	f.labelsAboveHMA(c, pm, addrs)
	f.segmentCross(c, pm, addrs)
}

// labelsAboveHMA reports real-mode memory operands naming a label whose
// link address is past the HMA.
func (f *facts) labelsAboveHMA(c *lint.Context, pm []bool, addrs map[string]uint64) {
	if len(addrs) == 0 {
		return
	}
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.Kind != asm.Instr || s.InMacroDef || !f.res.Reachable(i) || !f.realMode(i, pm) || instName(s) == "lea" {
			continue
		}
		for k := range s.Ops {
			op := &s.Ops[k]
			if op.Kind != asm.OpMem || op.Base != "" || op.Index != "" {
				continue
			}
			label := strings.TrimSpace(op.Disp)
			li, ok := f.p.Labels[label]
			if !ok {
				continue
			}
			sec := f.p.Stmts[li].Section
			base, ok := addrs[sec]
			if !ok {
				continue
			}
			off, ok := listingOffset(c.Listing, f.p.Stmts[li].Line, sec)
			if !ok {
				continue
			}
			if a := base + off; a > hmaTop {
				f.report(c, i, fmt.Sprintf("%s is linked at %#x (section %s at %#x in the linker script), above what real mode reaches (%#x): this real-mode access cannot get to it",
					label, a, sec, base, hmaTop), li)
			}
		}
	}
}

// segmentCross reports real-mode code that runs past offset 0xFFFF of its
// code segment: a .code16 region of a section longer than 64 KiB, or one
// whose link address runs past 0xFFFF. Real-mode code that jumps to its own
// labels uses their link addresses as IP values (`ljmp $SEG, $label`), so
// an instruction linked past 0xFFFF is one IP cannot hold: execution wraps
// to offset 0 of the segment before reaching it.
func (f *facts) segmentCross(c *lint.Context, pm []bool, addrs map[string]uint64) {
	type region struct {
		first, last int
		lo, hi      uint64
	}
	regs := map[string]*region{}
	var order []string
	for i := range f.p.Stmts {
		s := &f.p.Stmts[i]
		if s.Kind != asm.Instr || s.InMacroDef || !f.res.Reachable(i) || !f.realMode(i, pm) {
			continue
		}
		off, ok := listingOffset(c.Listing, s.Line, s.Section)
		if !ok {
			continue
		}
		r := regs[s.Section]
		if r == nil {
			r = &region{first: i, lo: off}
			regs[s.Section] = r
			order = append(order, s.Section)
		}
		r.last, r.hi = i, off
	}
	for _, sec := range order {
		r := regs[sec]
		if r.hi-r.lo >= 0x10000 {
			f.report(c, r.first, fmt.Sprintf("the real-mode code of section %s spans %#x bytes (to line %d): more than one 64 KiB code segment can hold, so IP wraps before the end", sec, r.hi-r.lo, f.line(r.last)), r.last)
			continue
		}
		base, ok := addrs[sec]
		if !ok || base+r.lo > 0xFFFF {
			// Linked wholly above 64 KiB: code copied to its run address
			// before it runs (a trampoline), which the link address does
			// not describe.
			continue
		}
		if end := base + r.hi; end > 0xFFFF {
			f.report(c, r.last, fmt.Sprintf("this real-mode code is linked at %#x (section %s at %#x in the linker script): past 0xFFFF, IP wraps to the start of the code segment before it gets here",
				end, sec, base))
		}
	}
}

// The linker script reader is the smallest that answers "at what address
// does this section start": `. = N;` and output sections with an address,
// filled from input section lists. Only a section named exactly as the
// first input of an output section with a known address gets an address;
// anything the reader does not follow is no address, so no finding.

var (
	ldComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	ldAssign  = regexp.MustCompile(`^\.\s*=\s*(0[xX][0-9a-fA-F]+|[0-9]+)\s*;`)
	ldOutSec  = regexp.MustCompile(`^([.\w]+)\s*(0[xX][0-9a-fA-F]+|[0-9]+)?\s*(\(\s*NOLOAD\s*\))?\s*:\s*(AT\s*\([^)]*\)\s*)?\{`)
	ldInput   = regexp.MustCompile(`\*\s*\(\s*([^)\s]+)`)
)

// parseLD returns, for each input section named first in an output section
// with a known address, that address.
func parseLD(src string) map[string]uint64 {
	src = ldComment.ReplaceAllString(src, " ")
	k := strings.Index(src, "SECTIONS")
	if k < 0 {
		return nil
	}
	body := src[k+len("SECTIONS"):]
	if b := strings.IndexByte(body, '{'); b >= 0 {
		body = body[b+1:]
	}
	out := map[string]uint64{}
	var loc uint64
	locKnown := false
	for {
		body = strings.TrimLeft(body, " \t\r\n;")
		if body == "" || body[0] == '}' {
			return out
		}
		if m := ldAssign.FindStringSubmatch(body); m != nil {
			v, err := strconv.ParseUint(m[1], 0, 64)
			loc, locKnown = v, err == nil
			body = body[len(m[0]):]
			continue
		}
		m := ldOutSec.FindStringSubmatchIndex(body)
		if m == nil {
			// Something this reader does not follow: skip the statement,
			// and the location counter is unknown after it.
			end := strings.IndexAny(body, ";}")
			if end < 0 {
				return out
			}
			locKnown = false
			body = body[end+1:]
			continue
		}
		addr, addrKnown := loc, locKnown
		if m[4] >= 0 {
			v, err := strconv.ParseUint(body[m[4]:m[5]], 0, 64)
			addr, addrKnown = v, err == nil
		}
		inner := body[m[1]:]
		depth, end := 1, -1
		for i := 0; i < len(inner) && end < 0; i++ {
			switch inner[i] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			return out
		}
		if first := ldInput.FindStringSubmatch(inner[:end]); first != nil && addrKnown && !strings.ContainsAny(first[1], "*?[") {
			if _, dup := out[first[1]]; !dup {
				out[first[1]] = addr
			}
		}
		locKnown = false
		body = inner[end+1:]
	}
}

// Linker scripts are cached by path, size and modification time.
var ldCache = struct {
	sync.Mutex
	m map[string]ldEntry
}{m: map[string]ldEntry{}}

type ldEntry struct {
	mod   time.Time
	size  int64
	addrs map[string]uint64
}

// maxLDDepth bounds the walk up from the document looking for scripts.
const maxLDDepth = 4

// sectionAddresses finds the *.ld/*.lds scripts beside the document and in
// the directories above it (up to the project root, which holds .git) and
// merges their section addresses. A section two scripts place differently
// gets none.
func sectionAddresses(p *asm.Program) map[string]uint64 {
	path, ok := toolchain.URIToPath(p.Doc.URI)
	if !ok {
		return nil
	}
	out := map[string]uint64{}
	conflict := map[string]bool{}
	dir := filepath.Dir(path)
	for depth := 0; depth < maxLDDepth; depth++ {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".ld") && !strings.HasSuffix(n, ".lds") {
				continue
			}
			for sec, a := range loadLD(filepath.Join(dir, n)) {
				if old, ok := out[sec]; ok && old != a {
					conflict[sec] = true
				}
				out[sec] = a
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for sec := range conflict {
		delete(out, sec)
	}
	return out
}

func loadLD(path string) map[string]uint64 {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	ldCache.Lock()
	e, ok := ldCache.m[path]
	ldCache.Unlock()
	if ok && e.mod.Equal(st.ModTime()) && e.size == st.Size() {
		return e.addrs
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	e = ldEntry{mod: st.ModTime(), size: st.Size(), addrs: parseLD(string(data))}
	ldCache.Lock()
	ldCache.m[path] = e
	ldCache.Unlock()
	return e.addrs
}

func init() {
	lint.Register(&lint.Rule{
		Code:      "real-mode-reach",
		Section:   5,
		Default:   lint.Warning,
		Langs:     lint.LangAsm,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Real-mode memory access past what segment:offset can reach, or a 16-bit offset that wraps.",
			Why: "A real-mode address is segment * 16 plus a 16-bit offset: nothing above 0x10FFEF (FFFF:FFFF, the top of the HMA) is " +
				"reachable, an offset past 0xFFFF with a 32-bit address faults on real mode's 64 KiB limit, and a 16-bit offset " +
				"computed past 0xFFFF silently wraps to the bottom of the segment. When the dataflow knows the segment register, the " +
				"base and index registers and the displacement of a memory operand in real-mode code, the address is checked. Files " +
				"that leave protected mode (and so may set up unreal mode on purpose) are not judged on 32-bit offsets.",
			Bad:  "\t.code16\n\tmovw $0xF000, %ax\n\tmovw %ax, %ds\n\tmovl $0x20000, %esi\n\tmovl (%esi), %eax\n",
			Good: "\t.code16\n\tmovw $0xF000, %ax\n\tmovw %ax, %ds\n\tmovl $0xFFF0, %esi\n\tmovl (%esi), %eax\n",
		},
		Check: checkReach,
	})
	lint.Register(&lint.Rule{
		Code:         "real-mode-layout",
		Section:      5,
		Default:      lint.Warning,
		Langs:        lint.LangAsm,
		NeedsFlow:    true,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "Real-mode code that reaches for a label linked above 1 MiB, or runs past the end of its 64 KiB code segment.",
			Why: "Data that real-mode code must read has to be below the HMA, and real-mode code has to fit in the 64 KiB of the " +
				"segment it runs in: IP wraps to 0 past offset 0xFFFF. Link addresses come from the linker script beside the file " +
				"(an output section with an address, or after `. = N;`, whose first input is this file's section) plus the listing's " +
				"offsets. Real-mode code uses its labels' link addresses as IP values, so code linked past 0xFFFF is code IP cannot " +
				"reach; a real-mode region longer than 64 KiB is reported whatever the addresses.",
			Bad:  "# link.ld: SECTIONS { .lo 0x7C00 : { *(.text.lo) } .hi 0x200000 : { *(.data.hi) } }\n\t.code16\n\t.section .text.lo, \"ax\"\n\tmovl hi_data, %eax\n\t.section .data.hi, \"aw\"\nhi_data:\t.long 0\n",
			Good: "# link.ld: SECTIONS { .lo 0x7C00 : { *(.text.lo) } .hi 0x200000 : { *(.data.hi) } }\n\t.code16\n\t.section .text.lo, \"ax\"\n\tmovl lo_data, %eax\n\t.section .text.lo, \"ax\"\nlo_data:\t.long 0\n",
		},
		Check: checkLayout,
	})
}
