package layout

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Budget is one size budget of a file: a stage that ends at a location
// move measured from a start symbol (`. = _start + 510`), or a section
// given a budget by a `falcon-asm: budget=<bytes>` comment.
type Budget struct {
	Name    string // stage or section name shown to the user
	Section string
	Limit   int64 // the budget in bytes
	// Used is the bytes in use. When Exact is false it is a lower bound
	// (assembly failed, so only the listed bytes are known), and such a
	// budget is only reported when even the lower bound is over.
	Used  int64
	Exact bool
	// Line is where the lens goes: the start symbol's line for a stage,
	// the comment's line for a section budget.
	Line int
	// SetBy is the line that sets the limit: the move or the comment.
	SetBy int
	Base  string // the start symbol; "" for section budgets
}

// Free is the room left; negative when over.
func (b Budget) Free() int64 { return b.Limit - b.Used }

// Title is the lens text, e.g. "stage1: 440 / 510 bytes (70 free)".
func (b Budget) Title() string {
	switch {
	case !b.Exact:
		return fmt.Sprintf("%s: at least %d / %d bytes (%d+ over)", b.Name, b.Used, b.Limit, -b.Free())
	case b.Free() < 0:
		return fmt.Sprintf("%s: %d / %d bytes (%d over)", b.Name, b.Used, b.Limit, -b.Free())
	}
	return fmt.Sprintf("%s: %d / %d bytes (%d free)", b.Name, b.Used, b.Limit, b.Free())
}

// Budgets computes the file's budgets from the program and the listing of
// the same text. Each start symbol gets one stage, bounded by its
// outermost move (the boot sector's `. = _start + 510`, not the reserved
// slots in front of it), so the lens answers "how much room is left";
// every move is still checked on its own by budget-exceeded.
func Budgets(p *asm.Program, res *toolchain.AsmResult) []Budget {
	if p == nil || p.Doc == nil || res == nil {
		return nil
	}
	ms := moves(p, res)
	type key struct{ sec, base string }
	outer := map[key]move{}
	var order []key
	for _, m := range ms {
		if !m.known {
			continue
		}
		k := key{m.section, m.base}
		prev, seen := outer[k]
		if !seen {
			order = append(order, k)
		}
		if !seen || m.off > prev.off {
			outer[k] = m
		}
	}
	var out []Budget
	for _, k := range order {
		if b, ok := stageBudget(p, res, outer[k]); ok {
			out = append(out, b)
		}
	}
	for _, sb := range sectionBudgets(p) {
		if b, ok := sb.measure(res); ok {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// stageBudget measures the stage that m closes. gas fills from the end of
// the stage's contents up to m's target, so the room left is exactly the
// padding m inserts; used is the target minus that.
func stageBudget(p *asm.Program, res *toolchain.AsmResult, m move) (Budget, bool) {
	b := Budget{Name: stageName(m.section, m.base), Section: m.section, Limit: m.off, SetBy: m.line, Base: m.base, Line: m.line}
	from := -1
	if m.base != "" {
		idx, ok := p.Labels[m.base]
		if !ok || p.Stmts[idx].Section != m.section {
			// A base in another section (or another file) is not a
			// stage this file can measure.
			return Budget{}, false
		}
		b.Line, from = p.Stmts[idx].Line, p.Stmts[idx].Line-1
	}
	if b.Limit <= 0 {
		return Budget{}, false
	}
	if pad, ok := res.PadBytes(m.line); ok && int64(pad) <= b.Limit {
		b.Used, b.Exact = b.Limit-int64(pad), true
		return b, true
	}
	if lb := listedBytes(res, m.section, from, m.line); lb > b.Limit {
		b.Used = lb
		return b, true
	}
	return Budget{}, false
}

// stageName prefers the part of a dotted section name that names the
// stage (.text.stage1 -> stage1), then the start symbol.
func stageName(sec, base string) string {
	if i := strings.LastIndexByte(sec, '.'); i > 0 && i < len(sec)-1 {
		return sec[i+1:]
	}
	if base != "" {
		return base
	}
	return sec
}

// sectionBudget is a `falcon-asm: budget=` comment.
type sectionBudget struct {
	section string
	limit   int64
	line    int
	start   int // byte offsets of the comment, for reporting
	end     int
}

func (sb sectionBudget) measure(res *toolchain.AsmResult) (Budget, bool) {
	b := Budget{Name: sb.section, Section: sb.section, Limit: sb.limit, Line: sb.line, SetBy: sb.line}
	if res == nil {
		return Budget{}, false
	}
	if sz, ok := res.SectionSizes[sb.section]; ok {
		b.Used, b.Exact = int64(sz), true
		return b, true
	}
	// No object (assembly failed): the listed bytes are a lower bound,
	// worth showing only when they already exceed the budget.
	if lb := listedBytes(res, sb.section, -1, 1<<31-1); lb > sb.limit {
		b.Used = lb
		return b, true
	}
	return Budget{}, false
}

// sectionBudgets reads every `falcon-asm: budget=<bytes>` and
// `budget=<section>:<bytes>` item in the file's comments. The short form
// names the section in effect where the comment is.
func sectionBudgets(p *asm.Program) []sectionBudget {
	var out []sectionBudget
	for i := range p.Toks {
		t := &p.Toks[i]
		if t.Kind != lexer.Comment {
			continue
		}
		_, rest, ok := strings.Cut(t.Text, "falcon-asm:")
		if !ok {
			continue
		}
		off := p.TokOffset(i)
		for _, f := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' || r == '*' || r == '/' }) {
			v, ok := strings.CutPrefix(f, "budget=")
			if !ok {
				continue
			}
			sec := ""
			if j := strings.LastIndexByte(v, ':'); j >= 0 {
				sec, v = v[:j], v[j+1:]
			}
			n, ok := parseBytes(v)
			if !ok {
				continue
			}
			if sec == "" {
				sec = sectionAt(p, off)
			}
			out = append(out, sectionBudget{section: sec, limit: n, line: t.Line, start: off, end: off + t.Len})
		}
	}
	return out
}

// parseBytes reads 512, 0x200, 4K, 4KiB, 1M or 1MiB.
func parseBytes(s string) (int64, bool) {
	mul := int64(1)
	low := strings.ToLower(s)
	for _, suf := range []struct {
		s string
		m int64
	}{{"kib", 1 << 10}, {"k", 1 << 10}, {"mib", 1 << 20}, {"m", 1 << 20}} {
		if strings.HasSuffix(low, suf.s) && !strings.HasPrefix(low, "0x") {
			low, mul = strings.TrimSuffix(low, suf.s), suf.m
			break
		}
	}
	n, err := strconv.ParseInt(low, 0, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mul, true
}

// sectionAt is the section in effect at byte offset off: the section of
// the first statement after it, or what the last statement leaves.
func sectionAt(p *asm.Program, off int) string {
	i := sort.Search(len(p.Stmts), func(i int) bool { return p.Stmts[i].Start >= off })
	if i < len(p.Stmts) {
		return p.Stmts[i].Section
	}
	if len(p.Stmts) == 0 {
		return ".text"
	}
	last := &p.Stmts[len(p.Stmts)-1]
	switch last.Name {
	case ".text", ".data", ".bss":
		return last.Name
	case ".section", ".pushsection":
		if len(last.Args) > 0 {
			if f := strings.Fields(last.Args[0]); len(f) > 0 {
				return strings.Trim(f[0], "\"")
			}
		}
	}
	return last.Section
}

// overlap is one location move that org-overlap reports, found from the
// source alone: its target is behind (or, with data in between, equal to)
// an earlier move's in the same section and relative to the same symbol.
type overlap struct {
	m, prev move
	equal   bool
}

// overlaps is memoized: budget-exceeded leaves these lines to org-overlap,
// which explains them better.
func overlaps(c *lint.Context) []overlap {
	v, _ := c.Memo("layout/overlaps", func() any { return findOverlaps(c.Prog, moves(c.Prog, nil)) }).([]overlap)
	return v
}

func findOverlaps(p *asm.Program, ms []move) []overlap {
	type key struct{ sec, base string }
	high := map[key]move{} // the move with the highest target so far
	var out []overlap
	for _, m := range ms {
		if !m.known {
			continue
		}
		k := key{m.section, m.base}
		prev, seen := high[k]
		switch {
		case !seen || m.off > prev.off:
			high[k] = m
		case m.off < prev.off:
			out = append(out, overlap{m: m, prev: prev})
		default:
			for i := prev.stmt + 1; i < m.stmt; i++ {
				if st := &p.Stmts[i]; st.Section == m.section && !st.InMacroDef && emitting(st) {
					out = append(out, overlap{m: m, prev: prev, equal: true})
					break
				}
			}
		}
	}
	return out
}

func refText(base string, off int64) string {
	if base == "" {
		return fmt.Sprintf("offset %#x", off)
	}
	return fmt.Sprintf("%s + %#x", base, off)
}

// gasBackwards are the lines where gas refused to move the location
// counter backwards. That message names neither the stage nor how much
// too large it is; budget-exceeded replaces it with one that does, and
// being an Error on the same line keeps gas's own from being shown too.
func gasBackwards(p *asm.Program, res *toolchain.AsmResult) map[int]bool {
	path, ok := docPath(p)
	if !ok || res == nil {
		return nil
	}
	var lines map[int]bool
	for _, d := range res.Diagnostics[path] {
		if strings.Contains(d.Message, "move .org backwards") {
			if lines == nil {
				lines = map[int]bool{}
			}
			lines[d.Range.Start.Line] = true
		}
	}
	return lines
}

func checkBudgets(c *lint.Context) {
	p, res := c.Prog, c.Listing
	if p == nil || p.Doc == nil || res == nil {
		return
	}
	for _, sb := range sectionBudgets(p) {
		if b, ok := sb.measure(res); ok && b.Used > b.Limit {
			c.ReportRange(sb.start, sb.end, fmt.Sprintf("section %s is over its %d-byte budget: %s bytes (%s)",
				b.Section, b.Limit, atLeast(b), overBy(b)))
		}
	}
	back := gasBackwards(p, res)
	if len(back) == 0 {
		return
	}
	claimed := map[int]bool{}
	for _, o := range overlaps(c) {
		claimed[o.m.line] = true
	}
	ms := moves(p, res)
	for i, m := range ms {
		if !back[m.line] || claimed[m.line] {
			continue
		}
		c.ReportStmt(m.stmt, backwardsMessage(p, res, ms[:i], m))
	}
}

func atLeast(b Budget) string {
	if b.Exact {
		return strconv.FormatInt(b.Used, 10)
	}
	return "at least " + strconv.FormatInt(b.Used, 10)
}

func overBy(b Budget) string {
	if b.Exact {
		return fmt.Sprintf("%d over", -b.Free())
	}
	return fmt.Sprintf("at least %d over", -b.Free())
}

// backwardsMessage explains a move gas could not make. The region that
// overflowed starts at the previous move to the same base when there is
// one (a reserved slot, such as the boot sector's stage 2 LBA), else at
// the start symbol: that is the stage whose contents grew too large.
func backwardsMessage(p *asm.Program, res *toolchain.AsmResult, before []move, m move) string {
	const tail = " (gas: attempt to move .org backwards)"
	if !m.known {
		return "the code and data before this line already reach past its target" + tail
	}
	for i := len(before) - 1; i >= 0; i-- {
		prev := before[i]
		if prev.section != m.section {
			continue
		}
		if !prev.known || prev.base != m.base {
			break
		}
		slot := m.off - prev.off
		msg := fmt.Sprintf("the data placed at %s by line %d runs past this line's target %s: the slot holds %d bytes",
			refText(prev.base, prev.off), prev.line+1, refText(m.base, m.off), slot)
		if lb := listedBytes(res, m.section, prev.line, m.line); lb > slot {
			msg += fmt.Sprintf(" and at least %d are placed there", lb)
		}
		return msg + tail
	}
	name := stageName(m.section, m.base)
	from, where := -1, "the start of "+m.section
	if m.base != "" {
		if idx, ok := p.Labels[m.base]; ok {
			from = p.Stmts[idx].Line - 1
			where = fmt.Sprintf("%s (line %d)", m.base, p.Stmts[idx].Line+1)
		} else {
			where = m.base
		}
	}
	msg := fmt.Sprintf("%s is over its %d-byte budget: the code and data from %s reach past this line's target",
		name, m.off, where)
	if lb := listedBytes(res, m.section, from, m.line); lb > m.off {
		msg += fmt.Sprintf(" by at least %d bytes", lb-m.off)
	}
	return msg + tail
}

func checkOverlaps(c *lint.Context) {
	for _, o := range overlaps(c) {
		var msg string
		if o.equal {
			msg = fmt.Sprintf("this move targets %s, the same place as the move at line %d, but data is placed after that one: the two regions overlap",
				refText(o.m.base, o.m.off), o.prev.line+1)
		} else {
			msg = fmt.Sprintf("this move targets %s, behind the move at line %d to %s: gas cannot move the location counter backwards, and the regions overlap",
				refText(o.m.base, o.m.off), o.prev.line+1, refText(o.prev.base, o.prev.off))
		}
		c.ReportFinding(lint.Finding{
			Range:   c.Prog.Range(c.Prog.Stmts[o.m.stmt].Start, c.Prog.Stmts[o.m.stmt].End),
			Message: msg,
			Related: []protocol.Location{{URI: c.Prog.Doc.URI, Range: c.Prog.Range(c.Prog.Stmts[o.prev.stmt].Start, c.Prog.Stmts[o.prev.stmt].End)}},
		})
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:         "budget-exceeded",
		Section:      6,
		Default:      lint.Error,
		Langs:        lint.LangAsm,
		NeedsListing: true,
		Doc: lint.Doc{
			Summary: "A stage or section is larger than its size budget.",
			Why: "Boot code lives in fixed-size slots: a boot sector has 510 bytes before 0x55AA, a stage " +
				"has the sectors its loader reads. The usual guard is a location move such as " +
				"`. = _start + 510`, which gas refuses once the code in front of it has grown past the " +
				"target, with the unhelpful \"attempt to move .org backwards\". This rule replaces that " +
				"message with one naming the stage, its budget and, from the listing, how far over it is " +
				"at least. A section can also be given a budget with a `falcon-asm: budget=<bytes>` " +
				"comment inside it (or `budget=<section>:<bytes>` anywhere); its size comes from the " +
				"assembled object. The editor shows every budget as a code lens on the stage's start " +
				"label, e.g. \"stage1: 440 / 510 bytes (70 free)\". Needs the toolchain.",
			Bad:  "\t.section .text.stage1, \"ax\"\n_start:\n\t.fill 520, 1, 0x90\n\t. = _start + 510\n\t.byte 0x55, 0xAA",
			Good: "\t.section .text.stage1, \"ax\"\n_start:\n\t.fill 440, 1, 0x90\n\t. = _start + 510\n\t.byte 0x55, 0xAA",
		},
		Check: checkBudgets,
	})
	lint.Register(&lint.Rule{
		Code:    "org-overlap",
		Section: 6,
		Default: lint.Error,
		Langs:   lint.LangAsm,
		Doc: lint.Doc{
			Summary: "A .org or `. =` target is behind, or the same as, an earlier one in the section.",
			Why: "Fixed layouts are written as a series of location moves: the boot code, then " +
				"`.org _start + 0x1B2` for a slot the installer patches, then the partition table at " +
				"446. If a later target is lower than an earlier one (a typo, or a constant changed in " +
				"one header but not another), the regions overlap; gas stops with \"attempt to move .org " +
				"backwards\" and no hint of which earlier line it means. Two moves to the same target " +
				"with data between them overlap the same way. Both are found from the source alone, with " +
				"macro values from the toolchain when it has run; targets relative to different symbols " +
				"are not compared.",
			Bad:  "\t.org 0x1BE\npart_table:\n\t.fill 64, 1, 0\n\t.org 0x1B8\ndisk_sig:\n\t.long 0",
			Good: "\t.org 0x1B8\ndisk_sig:\n\t.long 0\n\t.org 0x1BE\npart_table:\n\t.fill 64, 1, 0",
		},
		Check: checkOverlaps,
	})
}
