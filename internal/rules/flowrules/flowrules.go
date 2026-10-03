// Package flowrules holds the dataflow rules of roadmap section 3 (items
// 19-30) and the CPUID guard rule of section 2 (item 17). Every rule here
// reads the CFG and the forward dataflow solution internal/flow computes
// once per document (lint.Context.Flow), and follows the roadmap's ground
// rule strictly: state the analysis cannot prove (an Unknown flag, a
// DefMerged register, an unknown stack depth, a BIOS service whose AH is
// not a constant) never produces a finding.
//
// Several rules need facts about the document as a whole (which function
// the BIOS enters, the BIOS service each int names, the registers each
// statement reads). Those are derived once per run and shared through the
// lint memo, so the rules together cost a small multiple of one walk over
// the statements on top of the flow analysis itself.
//
// Files: flowrules.go (facts shared by the rules), reads.go (register
// reads per instruction), walk.go (path searches and dominators), and one
// file per roadmap item or pair of items.
package flowrules

import (
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Rule codes.
const (
	CodeDFUnknown        = "df-unknown"
	CodeSegmentUnset     = "segment-unset"
	CodeStackImbalance   = "stack-imbalance"
	CodePushfPopf        = "pushf-popf-pair"
	CodeCliSti           = "cli-sti-pair"
	CodePartialRead      = "partial-register-read"
	CodeUninitRead       = "uninit-read"
	CodeIFCritical       = "if-enabled-critical"
	CodeFlagsClobbered   = "flags-clobbered"
	CodeBIOSCFUnchecked  = "bios-cf-unchecked"
	CodeBIOSClobber      = "bios-clobber"
	CodeBIOSClobberQuirk = "bios-clobber-quirk"
	CodeUnreachable      = "unreachable-code"
	CodeFallIntoData     = "fallthrough-into-data"
	CodeBusySpin         = "busy-spin"
	CodeSelfModifying    = "self-modifying-code"
	CodeFeatureUnguarded = "feature-unguarded"
	factsKey             = "flowrules/facts"
	sectionDataflow      = 3
	sectionTarget        = 2
	langs                = lint.LangAsm
)

// facts is what the rules share about one document.
type facts struct {
	g   *flow.CFG
	res *flow.Result
	p   *asm.Program
	db  *hwdb.DB

	// bootFunc is the index in g.Funcs of the function the BIOS enters
	// (the first code of a 16-bit boot sector, or _start), -1 when the
	// document has none.
	bootFunc int
	// ints maps an int statement to the BIOS service it calls, for every
	// int whose vector and function number the dataflow knows.
	ints map[int]biosCall
}

// biosCall is one int statement resolved against the BIOS table.
type biosCall struct {
	svc *hwdb.BIOSService // nil when only the vector is known
	vec *hwdb.BIOSVector  // nil when the table has no per-vector entry
	n   uint64            // the vector
}

// title names the call for messages: "INT 13h AH=02h" or "INT 15h".
func (b biosCall) title() string {
	if b.svc != nil {
		return b.svc.Key()
	}
	return "INT " + hex2(b.n) + "h"
}

func hex2(v uint64) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[v>>4&0xF], digits[v&0xF]})
}

// factsOf returns the shared facts, nil when there is no flow.
func factsOf(c *lint.Context) *facts {
	g, res := c.Flow()
	if g == nil || res == nil || c.Prog == nil {
		return nil
	}
	v := c.Memo(factsKey, func() any { return buildFacts(c, g, res) })
	f, _ := v.(*facts)
	return f
}

func buildFacts(c *lint.Context, g *flow.CFG, res *flow.Result) *facts {
	f := &facts{g: g, res: res, p: c.Prog, db: dbOf(c.Tables), bootFunc: -1}
	f.bootFunc = bootFunction(g)
	f.ints = map[int]biosCall{}
	for i := range c.Prog.Stmts {
		st := &c.Prog.Stmts[i]
		if instName(st) != "int" || !res.Reachable(i) {
			continue
		}
		if call, ok := f.resolveInt(i, st); ok {
			f.ints[i] = call
		}
	}
	return f
}

// dbOf is the hardware database behind the lint tables (with the
// project's overrides), or the built-in one.
func dbOf(t lint.Tables) *hwdb.DB {
	if db, ok := t.(*hwdb.DB); ok && db != nil {
		return db
	}
	return hwdb.Default()
}

// resolveInt finds the service an int calls from the value of AH (and AL,
// for the services AL selects) the dataflow knows at the int. With AH
// unknown only a vector that takes no function number resolves to a
// service; the per-vector entry is kept either way, because some facts
// (INT 15h returning with IF set) hold for every function of a vector.
func (f *facts) resolveInt(i int, st *asm.Stmt) (biosCall, bool) {
	if len(st.Ops) != 1 || st.Ops[0].Kind != asm.OpImm || !st.Ops[0].ImmVal.Known {
		return biosCall{}, false
	}
	n := uint64(st.Ops[0].ImmVal.V) & 0xFF
	call := biosCall{n: n}
	call.vec, _ = f.db.BIOSVectorInfo(n)
	ah, ahOK := f.res.KnownAt(i, "ah")
	al, alOK := f.res.KnownAt(i, "al")
	if s, ok := f.db.LookupBIOS(n, ah, al, alOK); ok && (ahOK || s.AH == nil) {
		call.svc = s
	}
	if call.svc == nil && call.vec == nil {
		return biosCall{}, false
	}
	return call, true
}

// bootFunction finds the function the BIOS enters: the first code block of
// a document assembled as 16-bit code that either lays out a boot sector
// (moves to offset 510 or writes the 55h AAh signature) or names its entry
// _start or start. That is the one place where nothing about the machine
// state is known except what the BIOS boot protocol says (DL is the drive,
// CS:IP is 0:7C00 or 7C0:0), which is what segment-unset and uninit-read
// need to tell "the caller set it" from "nobody did".
func bootFunction(g *flow.CFG) int {
	if len(g.Funcs) == 0 {
		return -1
	}
	fi := -1
	for k := range g.Funcs {
		bl := &g.Blocks[g.Funcs[k].Entry]
		if bl.Why&flow.EntryFirst != 0 && bl.First == firstCode(g) {
			fi = k
			break
		}
	}
	if fi < 0 || g.ModeOf(g.Funcs[fi].Entry) != asm.Mode16 {
		return -1
	}
	switch g.Funcs[fi].Name {
	case "_start", "start":
		return fi
	}
	if bootLayout(g.Prog) {
		return fi
	}
	return -1
}

// firstCode is the statement that opens the document's first block.
func firstCode(g *flow.CFG) int {
	if len(g.Blocks) == 0 {
		return -1
	}
	return g.Blocks[0].First
}

// bootLayout reports a boot sector: a move to offset 510 (`.org 510`,
// `. = _start + 510`) or the 55h AAh signature written as data.
func bootLayout(p *asm.Program) bool {
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Directive || st.InMacroDef {
			continue
		}
		switch st.Name {
		case ".org":
			if len(st.Args) > 0 && endsWith510(st.Args[0]) {
				return true
			}
		case ".set", ".equ", "=":
			if len(st.Args) > 1 && st.Args[0] == "." && endsWith510(st.Args[1]) {
				return true
			}
		case ".word", ".short", ".value", ".2byte", ".hword":
			for _, a := range st.Args {
				if strings.EqualFold(strings.TrimSpace(a), "0xaa55") {
					return true
				}
			}
		case ".byte":
			if len(st.Args) >= 2 && strings.EqualFold(strings.TrimSpace(st.Args[len(st.Args)-2]), "0x55") &&
				strings.EqualFold(strings.TrimSpace(st.Args[len(st.Args)-1]), "0xaa") {
				return true
			}
		}
	}
	return false
}

// endsWith510 matches "510", "0x1fe" and "label + 510".
func endsWith510(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndexByte(s, '+'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	return s == "510" || s == "0x1fe"
}

// instName is the resolved instruction's name, "" for anything else.
func instName(st *asm.Stmt) string {
	if st.Kind != asm.Instr || st.Inst == nil {
		return ""
	}
	return st.Inst.Name
}

// isStringOp reports the string instructions (movs, stos, lods, scas,
// cmps, ins, outs) in every spelling the tables resolve.
func isStringOp(st *asm.Stmt) bool {
	switch instName(st) {
	case "movs", "stos", "lods", "scas", "cmps", "ins", "outs":
		return true
	}
	return false
}

// lineOf is the 1-based line of statement i, for messages.
func lineOf(p *asm.Program, i int) int { return p.Stmts[i].Line + 1 }

// stmtText is the statement's source text, for messages.
func stmtText(p *asm.Program, i int) string {
	st := &p.Stmts[i]
	if p.Doc == nil || st.End > len(p.Doc.Text) {
		return st.Mnemonic
	}
	return strings.Join(strings.Fields(p.Doc.Text[st.MnemStart:st.End]), " ")
}

// stmts calls fn for every statement of block b that belongs to it, in
// order; fn returns false to stop.
func stmts(g *flow.CFG, b int, fn func(i int) bool) {
	bl := &g.Blocks[b]
	for i := bl.First; i <= bl.Last; i++ {
		if g.BlockOf[i] == b && !fn(i) {
			return
		}
	}
}

// prefixStmt reports a statement that is only a prefix written on its own
// ("rep" on the line before movsb): an edit must not separate the two.
func prefixStmt(st *asm.Stmt) bool {
	return st.Kind == asm.Instr && st.Mnemonic == "" && len(st.Prefixes) > 0
}

// insertAbove is the statement a line inserted "before i" must go above: a
// lone prefix statement right before i stays attached to it.
func insertAbove(p *asm.Program, i int) int {
	if i > 0 && prefixStmt(&p.Stmts[i-1]) {
		return i - 1
	}
	return i
}

// hasRep reports a rep-family prefix on the statement or on a lone prefix
// statement right before it.
func hasRep(p *asm.Program, i int) bool {
	check := func(ps []string) bool {
		for _, x := range ps {
			switch x {
			case "rep", "repe", "repz", "repne", "repnz":
				return true
			}
		}
		return false
	}
	if check(p.Stmts[i].Prefixes) {
		return true
	}
	return i > 0 && prefixStmt(&p.Stmts[i-1]) && check(p.Stmts[i-1].Prefixes)
}

// instFlags caches the parsed flag sets of an instruction: the isa tables
// keep them as strings, and several rules ask for every statement.
var instFlags sync.Map // instruction table entry -> [2]asm.Flags{read, written}

func flagsOf(st *asm.Stmt) [2]asm.Flags {
	if st.Kind != asm.Instr || st.Inst == nil {
		return [2]asm.Flags{}
	}
	if v, ok := instFlags.Load(st.Inst); ok {
		return v.([2]asm.Flags)
	}
	fl := [2]asm.Flags{st.FlagsRead(), st.FlagsWritten()}
	instFlags.Store(st.Inst, fl)
	return fl
}

// flagsRead is the EFLAGS bits the statement reads.
func flagsRead(st *asm.Stmt) asm.Flags { return flagsOf(st)[0] }

// flagsWritten is the EFLAGS bits the statement changes or leaves undefined.
func flagsWritten(st *asm.Stmt) asm.Flags { return flagsOf(st)[1] }

// after is the state after statement i, read from where the analysis
// stored it (the next statement of the block, or the block's exit state)
// rather than recomputed: Result.After copies and steps a whole State.
func (f *facts) after(i int) *flow.State {
	b := f.g.BlockOf[i]
	if b < 0 {
		return f.res.After(i)
	}
	last := f.g.Blocks[b].Last
	for j := i + 1; j <= last; j++ {
		if f.g.BlockOf[j] == b {
			return &f.res.In[j]
		}
	}
	return &f.res.Out[b]
}
