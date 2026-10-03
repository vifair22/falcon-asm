package cc

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/callconv"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The model is everything the convention rules share about one document,
// computed once (lint.Context.Memo) and read by each rule: which
// convention every function follows and where that came from, which
// functions are interrupt handlers, the annotation problems, and a
// dataflow solution in which calls to code outside the document clobber
// what the file's convention says they clobber rather than every
// register (the engine's shared solution assumes the worst, which would
// make every callee-saved register look written by every call).

const memoKey = "cc/model"

// function is one flow function as the convention rules see it.
type function struct {
	idx  int // index in CFG.Funcs
	name string
	stmt int // the statement defining name, -1 when unlabeled
	bits int // 16, 32 or 64: the mode of its entry
	// conv is nil when the function follows no checked convention
	// (none, a 16-bit function nothing declared, a handler).
	conv *callconv.Conv
	// src says where conv came from, for messages.
	src string
	// explicit reports a convention from an annotation on the function.
	explicit bool
	// entered reports a function something calls or the linker can
	// reach (a call target or a global), so its entry stack holds a
	// return address and nothing else: the premise of the stack checks.
	entered bool
	handler *handler
}

// handler describes an interrupt or exception handler.
type handler struct {
	vector int    // -1 when unknown
	kind   string // "", "errcode", "noerrcode" or "service"
	ivt    bool   // installed in the real-mode vector table
	how    string // how it was found, for messages
}

// preserves reports whether the handler must preserve every register: a
// hardware interrupt or exception handler must, a software service (INT
// 13h and friends) returns its results in registers.
func (h *handler) preserves() bool { return h.kind != "service" }

// errcode reports whether the processor pushes an error code for the
// handler's vector: Set, Clear or Unknown.
func (h *handler) errcode() flow.Tri {
	switch {
	case h.kind == "errcode":
		return flow.Set
	case h.kind == "noerrcode" || h.ivt:
		return flow.Clear
	case h.vector >= 0:
		if callconv.ErrorCode(h.vector) {
			return flow.Set
		}
		return flow.Clear
	}
	return flow.Unknown
}

// problem is an annotation that could not be understood.
type problem struct {
	start, end int
	msg        string
}

type model struct {
	prog *asm.Program
	g    *flow.CFG
	res  *flow.Result
	// fileConv is the convention for functions with no annotation of
	// their own (nil: none), fileSrc where it came from, fileComment
	// whether a comment declared it (which makes it apply to 16-bit code
	// too when it is a mode-free custom convention).
	fileConv    *callconv.Conv
	fileSrc     string
	fileComment bool
	funcs       []function
	byEntry     map[int]int // entry block ID -> index in funcs
	problems    []problem
}

// modelOf returns the document's model, nil when there is no flow.
func modelOf(c *lint.Context) *model {
	v := c.Memo(memoKey, func() any { return build(c) })
	m, _ := v.(*model)
	return m
}

func build(c *lint.Context) *model {
	g, base := c.Flow()
	if g == nil || base == nil || c.Prog == nil {
		return nil
	}
	m := &model{prog: c.Prog, g: g, byEntry: map[int]int{}}
	anns := m.annotations()
	m.fileDefault(c.Target, anns)
	m.functions(anns)
	m.handlers()
	m.res = base
	if m.fileConv != nil {
		m.res = flow.Analyze(g, flow.Config{CallClobbers: m.fileConv.Clobbered.Regs()})
	}
	return m
}

// annotation is one parsed falcon-asm comment and the statement whose
// label it is attached to (-1 for a file-level one).
type annotation struct {
	ann        callconv.Annotation
	conv       *callconv.Conv
	attach     int
	start, end int
}

// annotations finds every falcon-asm comment with a convention or
// handler item and attaches it to a label: the label on the comment's own
// line, or the first label below it when only comments and declaration
// directives (.globl, .type, .align, ...) come between. Anything else,
// a blank line or code first, makes it a file-level annotation.
func (m *model) annotations() []annotation {
	p := m.prog
	type line struct {
		code bool
	}
	lines := map[int]*line{}
	for i := range p.Toks {
		t := &p.Toks[i]
		l := lines[t.Line]
		if l == nil {
			l = &line{}
			lines[t.Line] = l
		}
		if t.Kind != lexer.Comment {
			l.code = true
		}
	}
	var out []annotation
	for i := range p.Toks {
		t := &p.Toks[i]
		if t.Kind != lexer.Comment || !strings.Contains(strings.ToLower(t.Text), callconv.Marker) {
			continue
		}
		a, ok := callconv.ParseComment(t.Text)
		if !ok {
			continue
		}
		start := p.TokOffset(i)
		an := annotation{ann: a, attach: -1, start: start, end: start + len(t.Text)}
		var probs []string
		an.conv, probs = a.Conv()
		probs = append(probs, a.Problems...)
		for _, msg := range probs {
			m.problems = append(m.problems, problem{an.start, an.end, msg})
		}
		an.attach = m.attachment(t.Line, func(l int) (exists, code bool) {
			li := lines[l]
			if li == nil {
				return false, false
			}
			return true, li.code
		})
		out = append(out, an)
	}
	return out
}

// declDirectives may sit between a function's annotation and its label.
var declDirectives = map[string]bool{
	".globl": true, ".global": true, ".weak": true, ".type": true, ".hidden": true, ".protected": true,
	".internal": true, ".local": true, ".align": true, ".balign": true, ".p2align": true, ".func": true,
}

// attachment is the statement holding the label an annotation on line l
// belongs to, -1 when it is file-level.
func (m *model) attachment(l int, info func(int) (exists, code bool)) int {
	p := m.prog
	for at := l; at <= l+64; at++ {
		exists, code := info(at)
		if !exists {
			return -1
		}
		if !code {
			continue // a comment line
		}
		decl := true
		for _, si := range p.StmtAt(at) {
			st := &p.Stmts[si]
			if len(st.Labels) > 0 {
				return si
			}
			if st.Kind != asm.Directive || !declDirectives[st.Name] {
				decl = false
			}
		}
		if !decl || at == l {
			// Code first, or a comment trailing a statement that is not
			// a label: nothing below is this comment's.
			return -1
		}
	}
	return -1
}

// fileDefault picks the convention for functions without their own: the
// first file-level annotation naming one, else the target's.
func (m *model) fileDefault(t lint.Target, anns []annotation) {
	for _, a := range anns {
		if a.attach < 0 && a.conv != nil {
			m.fileSrc = fmt.Sprintf("falcon-asm: cc comment on line %d", m.prog.Doc.Position(a.start).Line+1)
			m.fileComment = true
			if a.conv.Checked() {
				m.fileConv = a.conv
			}
			return
		}
	}
	conv, ok := callconv.Lookup(t.CC)
	// A bare "custom" from the configuration declares no lists, so it
	// promises nothing a rule could check.
	if !ok || !conv.Checked() || conv.Kind == callconv.Custom {
		return
	}
	m.fileConv = conv
	m.fileSrc = t.Sources["cc"]
	if m.fileSrc == "" {
		m.fileSrc = "target"
	}
}

// defaultConv is the convention of code at the given width that declares
// none of its own: the file's, when it fits the width. 16-bit code has no
// standard convention, so only a comment can give it one.
func (m *model) defaultConv(bits int) *callconv.Conv {
	c := m.fileConv
	if c == nil || !c.AppliesTo(bits) || bits == 16 && !m.fileComment {
		return nil
	}
	return c
}

func modeBits(md asm.Mode) int {
	switch md {
	case asm.Mode16:
		return 16
	case asm.Mode32:
		return 32
	}
	return 64
}

func (m *model) functions(anns []annotation) {
	g, p := m.g, m.prog
	attached := map[int]*annotation{}
	for k := range anns {
		if anns[k].attach >= 0 {
			attached[anns[k].attach] = &anns[k]
		}
	}
	called := map[int]bool{}
	for k := range g.Blocks {
		if cb := g.Blocks[k].Callee; cb >= 0 {
			called[cb] = true
		}
	}
	used := map[int]bool{}
	for fi := range g.Funcs {
		f := &g.Funcs[fi]
		bl := &g.Blocks[f.Entry]
		fn := function{idx: fi, name: f.Name, stmt: f.Label, bits: modeBits(g.ModeOf(f.Entry))}
		fn.entered = called[f.Entry] || bl.Why&flow.EntryGlobal != 0
		if fn.name == "_start" || fn.name == "start" || numericLabel(fn.name) {
			// A program entry point is jumped to with whatever stack the
			// loader made, not called; a call to a numeric local label
			// is a trick inside one function (a retpoline, a return
			// stack fill), not a call to a function with an ABI.
			fn.entered = false
		}
		var ann *annotation
		for i := bl.First; i <= bl.Last && i < len(p.Stmts); i++ {
			if a := attached[i]; a != nil {
				ann, used[i] = a, true
				break
			}
			if p.Stmts[i].Kind == asm.Instr {
				break
			}
		}
		switch {
		case ann != nil && ann.ann.Handler:
			fn.handler = &handler{vector: ann.ann.Vector, kind: ann.ann.HandlerKind, ivt: fn.bits == 16,
				how: "falcon-asm: handler comment"}
		case ann != nil && ann.conv != nil:
			if ann.conv.Checked() {
				fn.conv = ann.conv
			}
			fn.explicit = true
			fn.src = fmt.Sprintf("falcon-asm: cc comment on line %d", p.Doc.Position(ann.start).Line+1)
		default:
			fn.conv = m.defaultConv(fn.bits)
			fn.src = m.fileSrc
		}
		m.byEntry[f.Entry] = len(m.funcs)
		m.funcs = append(m.funcs, fn)
	}
	for k := range anns {
		a := &anns[k]
		if a.attach >= 0 && !used[a.attach] {
			st := &p.Stmts[a.attach]
			m.problems = append(m.problems, problem{a.start, a.end, fmt.Sprintf(
				"the annotation is on label %s, which is not a function start (nothing calls it, takes its address or makes it global)",
				st.Labels[0])})
		}
	}
}

// eachStmt calls fn for every statement of function fi in source order of
// its blocks; fn returning false stops the walk.
func (m *model) eachStmt(fi int, fn func(int) bool) {
	g := m.g
	for _, b := range g.Funcs[fi].Blocks {
		bl := &g.Blocks[b]
		for i := bl.First; i <= bl.Last; i++ {
			if g.BlockOf[i] != b {
				continue
			}
			if !fn(i) {
				return
			}
		}
	}
}

// funcAtLabel is the function whose entry statement defines the label.
func (m *model) funcAtLabel(name string) (int, bool) {
	si, ok := m.prog.Labels[name]
	if !ok || si < 0 || si >= len(m.g.BlockOf) {
		return 0, false
	}
	b := m.g.BlockOf[si]
	if b < 0 {
		return 0, false
	}
	fi, ok := m.byEntry[b]
	return fi, ok
}

// callee returns, for a call statement, the in-document function it
// enters (-1 for an external or indirect callee) and the convention the
// callee follows (nil: none).
func (m *model) callee(call int) (int, *callconv.Conv) {
	b := m.g.BlockOf[call]
	if b >= 0 {
		if cb := m.g.Blocks[b].Callee; cb >= 0 {
			if fi, ok := m.byEntry[cb]; ok {
				return fi, m.funcs[fi].conv
			}
		}
	}
	return -1, m.defaultConv(modeBits(m.prog.Stmts[call].Mode))
}

// calleeName names the callee of a call statement for messages.
func (m *model) calleeName(call int) string {
	if fi, _ := m.callee(call); fi >= 0 && m.funcs[fi].name != "" {
		return m.funcs[fi].name
	}
	st := &m.prog.Stmts[call]
	if len(st.Ops) > 0 {
		return strings.TrimPrefix(st.Ops[len(st.Ops)-1].Text, "*")
	}
	return "the callee"
}

// defs is what can reach a statement in a family along the paths into
// it: the entry value, the statements that wrote it, and whether some
// path could not be followed (something opaque wrote it, an external
// callee's convention says nothing about it).
type defs struct {
	entry   bool
	writes  []int
	unknown bool
}

func (d *defs) add(w int) {
	for _, x := range d.writes {
		if x == w {
			return
		}
	}
	d.writes = append(d.writes, w)
}

// reaching resolves the definitions of family r reaching statement i,
// path by path. The analysis keeps one definition per statement and
// meets disagreeing paths into DefMerged; where it did, this walks back
// through the predecessors' exit states until each path names its own
// writer, which is what "on some path" needs. A call whose callee
// preserves r under its convention is looked through, to the definition
// reaching the call.
func (m *model) reaching(i int, r flow.Reg) defs { return m.reachingDepth(i, r, 0) }

func (m *model) reachingDepth(i int, r flow.Reg, depth int) defs {
	var out defs
	// opaque handles a statement that may or may not have written r (a
	// software interrupt, a call to code with no declared convention):
	// the entry value may or may not survive it, but a write before it
	// has certainly replaced the entry value either way.
	opaque := func(def int) {
		out.unknown = true
		if depth >= 8 {
			return
		}
		for _, w := range m.reachingDepth(def, r, depth+1).writes {
			out.add(w)
		}
	}
	seenBlk := map[int]bool{}
	seenCall := map[int]bool{}
	var at func(i int)
	var blockStart func(b int)
	resolve := func(def int) {
		switch {
		case def == flow.DefEntry:
			out.entry = true
			return
		case def < 0:
			out.unknown = true
			return
		}
		st := &m.prog.Stmts[def]
		if st.Inst != nil && (st.Inst.Name == "int" || st.Inst.Name == "into" || st.Inst.Name == "int1") {
			// A software interrupt is a call to code with no declared
			// convention (the BIOS, a DOS service): what it changes is
			// not known here.
			opaque(def)
			return
		}
		if !st.IsCall() {
			out.add(def)
			return
		}
		fi, conv := m.callee(def)
		switch {
		case conv != nil && conv.Preserved.Has(r):
			if !seenCall[def] {
				seenCall[def] = true
				at(def)
			}
		case fi >= 0, conv != nil && conv.Clobbered.Has(r):
			// For a callee in the document the analysis summarized its
			// code, so the call writing r is a fact about that code.
			out.add(def)
		default:
			opaque(def)
		}
	}
	// mergedFromStart reports whether family r's definition is merged at
	// every statement of block b up to (not including) upto, and nothing
	// opaque runs there: then the merge happened where the block's
	// predecessors meet. A merge made inside the block (a pop of a slot
	// whose pushed value was itself merged, or an opaque statement) is
	// not one the predecessors can explain.
	mergedFromStart := func(b, upto int) bool {
		bl := &m.g.Blocks[b]
		for k := bl.First; k < upto; k++ {
			if m.g.BlockOf[k] != b {
				continue
			}
			if m.havocStmt(k) {
				return false
			}
			if s := m.res.Before(k); s != nil && s.Reachable && s.Get(r).Def != flow.DefMerged {
				return false
			}
		}
		return true
	}
	blockStart = func(b int) {
		if seenBlk[b] {
			return
		}
		seenBlk[b] = true
		if len(seenBlk) > 512 {
			out.unknown = true
			return
		}
		if _, ok := m.byEntry[b]; ok {
			out.entry = true
		}
		bl := &m.g.Blocks[b]
		for _, p := range bl.Pred {
			o := &m.res.Out[p]
			if !o.Reachable {
				continue
			}
			d := o.Get(r).Def
			if d != flow.DefMerged {
				resolve(d)
				continue
			}
			if !mergedFromStart(p, m.g.Blocks[p].Last+1) {
				out.unknown = true
				continue
			}
			blockStart(p)
		}
	}
	at = func(i int) {
		s := m.res.Before(i)
		if s == nil || !s.Reachable {
			return
		}
		d := s.Get(r).Def
		if d != flow.DefMerged {
			resolve(d)
			return
		}
		b := m.g.BlockOf[i]
		if b < 0 || !mergedFromStart(b, i) {
			out.unknown = true
			return
		}
		blockStart(b)
	}
	at(i)
	return out
}

// havocStmt reports a statement the analysis treats as writing anything:
// a macro invocation or an instruction it does not know.
func (m *model) havocStmt(i int) bool {
	st := &m.prog.Stmts[i]
	return st.Kind == asm.MacroCall || st.Kind == asm.Instr && st.Inst == nil && st.Mnemonic != ""
}

// restores reports whether statement def, the last writer of r, is the
// restoring half of a save: a pop (when the analysis lost the stack and
// cannot match it to its push), popa, leave for the frame pointer, or a
// load from memory in a function that also stores r to memory (a spill
// slot the analysis does not track).
func (m *model) restores(fi int, r flow.Reg, def int) bool {
	st := &m.prog.Stmts[def]
	if st.Kind != asm.Instr || st.Inst == nil {
		return false
	}
	switch st.Inst.Name {
	case "pop":
		return len(st.Ops) == 1 && regIs(&st.Ops[0], r)
	case "popa", "popad":
		return true
	case "leave":
		return r == flow.RBP
	case "mov", "movq", "movabs":
		if !st.IsGPRMove() || len(st.Ops) != 2 || st.Ops[0].Kind != asm.OpMem || !regIs(&st.Ops[1], r) {
			return false
		}
		// The save is a store of r before the load (in source order, the
		// prologue before the epilogue) to the same operand, or to any
		// stack slot, whose offset moves with the depth. It may be in
		// another function that shares this tail (an exception fixup
		// path jumping into the epilogue), so the owner of the load
		// counts too.
		load := &st.Ops[0]
		spilled := false
		owner := m.owner(def)
		visit := func(i int) bool {
			s := &m.prog.Stmts[i]
			if i < def && s.Kind == asm.Instr && s.IsGPRMove() && len(s.Ops) == 2 &&
				regIs(&s.Ops[0], r) && s.Ops[1].Kind == asm.OpMem &&
				(sameSlot(&s.Ops[1], load) || stackSlot(&s.Ops[1]) && stackSlot(load)) {
				spilled = true
				return false
			}
			return true
		}
		m.eachStmt(fi, visit)
		if !spilled && owner >= 0 && owner != fi {
			m.eachStmt(owner, visit)
		}
		return spilled
	}
	return false
}

// sameSlot reports two memory operands naming the same address.
func sameSlot(a, b *asm.Operand) bool {
	return a.Seg == b.Seg && a.Base == b.Base && a.Index == b.Index && a.Scale == b.Scale &&
		strings.ReplaceAll(a.Disp, " ", "") == strings.ReplaceAll(b.Disp, " ", "")
}

// stackSlot reports a memory operand based on the stack or frame pointer.
func stackSlot(op *asm.Operand) bool {
	if op.Base == "" {
		return false
	}
	r, _, _, ok := flow.RegOf(op.Base)
	return ok && (r == flow.RSP || r == flow.RBP)
}

func numericLabel(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

func regIs(op *asm.Operand, r flow.Reg) bool {
	if op.Kind != asm.OpReg {
		return false
	}
	f, _, _, ok := flow.RegOf(op.RegName)
	return ok && f == r
}

// writes reports whether statement i writes family r, by the analysis.
func (m *model) writes(i int, r flow.Reg) bool {
	before := m.res.Before(i)
	if before == nil || !before.Reachable {
		return false
	}
	return m.res.After(i).Get(r).Def != before.Get(r).Def
}

// unrestored decides whether family r, which function fi must preserve,
// can reach the ret at statement ret holding something other than its
// entry value. It returns the write to blame and true when it can.
func (m *model) unrestored(fi int, r flow.Reg, ret int) (int, bool) {
	for _, w := range m.reaching(ret, r).writes {
		if !m.restores(fi, r, w) {
			return w, true
		}
	}
	return 0, false
}
