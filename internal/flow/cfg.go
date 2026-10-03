package flow

import (
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

// The graph is built in one pass over the statements, in source order, with
// three refinements a plain "split at labels and branches" pass gets wrong
// on real boot code:
//
//   - Fallthrough is tracked per section. Code after ".section .data ...
//     .previous" continues the code before the switch, because that is
//     where the assembler places it; the data in between lives elsewhere.
//   - Conditional assembly (.if/.else/.endif, #if/#else/#endif) and
//     repetition (.rept/.irp/.irpc ... .endr) are branches and loops of the
//     graph: either arm may be the one assembled, a repeated body may run
//     several times in a row. Treating both arms as straight-line code
//     would push twice where the program pushes once.
//   - A data directive in a code section ends the block and records
//     IntoData, but the fallthrough chain continues past it to the next
//     code, with an edge the analysis treats as unknown. Boot code
//     hand-encodes instructions with .byte (a far jump, a prefix), and
//     cutting the chain there would report the code after it as dead.
type cfgBuilder struct {
	p    *asm.Program
	g    *CFG
	pend map[string][]int // section -> blocks whose fallthrough reaches the next code there
	cur  int              // open block, -1 when none
	sec  string           // section of the open block
	// curLabels is true while the open block holds only label
	// statements, so a run of labels shares one block.
	curLabels bool
	frames    []frame
	seenSec   map[string]bool
	dataLabel []bool
	jumps     []edgeRef // direct jump targets, resolved once every block exists
	calls     []edgeRef
	ints      []int // backing store for Succ/Pred, handed out two at a time
}

// edgeRef is a block's branch to a statement.
type edgeRef struct{ from, stmt int }

// frame is an open .if/#if or .rept/.irp construct.
type frame struct {
	loop    bool
	sec     string
	pre     []int // pending fallthrough before the construct
	ends    []int // arm tails collected at .else/#else
	sawElse bool
	first   int // first block of a repeated body, -1 until created
}

func build(p *asm.Program) *CFG {
	n := len(p.Stmts)
	g := &CFG{Prog: p, BlockOf: make([]int, n)}
	for i := range g.BlockOf {
		g.BlockOf[i] = -1
	}
	b := &cfgBuilder{
		p: p, g: g, cur: -1,
		pend:    map[string][]int{},
		seenSec: map[string]bool{},
	}
	b.dataLabels()
	// About one block per four statements in the fixtures.
	g.Blocks = make([]Block, 0, n/4+1)
	for i := range p.Stmts {
		b.stmt(i)
	}
	b.close()
	for _, e := range b.jumps {
		if t := b.blockAt(e.stmt); t >= 0 {
			b.edge(e.from, t)
			g.Blocks[e.from].Target = t
		}
	}
	for _, e := range b.calls {
		g.Blocks[e.from].Callee = b.blockAt(e.stmt)
	}
	b.entries()
	g.funcs()
	return g
}

// blockAt maps a branch target statement to its block.
func (b *cfgBuilder) blockAt(stmt int) int {
	if stmt < 0 || stmt >= len(b.g.BlockOf) {
		return -1
	}
	return b.g.BlockOf[stmt]
}

func (b *cfgBuilder) edge(from, to int) {
	bl := &b.g.Blocks[from]
	for _, s := range bl.Succ {
		if s == to {
			return
		}
	}
	bl.Succ = b.add(bl.Succ, to)
	b.g.Blocks[to].Pred = b.add(b.g.Blocks[to].Pred, from)
}

// add appends to an edge list; most blocks have one or two edges each
// way, so a first edge gets room for two from a shared chunk instead of a
// small allocation of its own.
func (b *cfgBuilder) add(list []int, v int) []int {
	if list != nil {
		return append(list, v)
	}
	if len(b.ints)+2 > cap(b.ints) {
		b.ints = make([]int, 0, 1024)
	}
	n := len(b.ints)
	b.ints = b.ints[:n+2]
	list = b.ints[n : n : n+2]
	return append(list, v)
}

// close ends the open block; its fallthrough, if any, stays pending.
func (b *cfgBuilder) close() {
	if b.cur < 0 {
		return
	}
	bl := &b.g.Blocks[b.cur]
	if falls(bl.Exit) {
		b.pend[b.sec] = append(b.pend[b.sec], b.cur)
	}
	b.cur = -1
}

// falls reports exits that continue at the next statement.
func falls(k ExitKind) bool {
	switch k {
	case ExitFall, ExitCond, ExitCall, ExitHalt:
		return true
	}
	return false
}

func (b *cfgBuilder) open(i int, sec string) {
	id := len(b.g.Blocks)
	b.g.Blocks = append(b.g.Blocks, Block{
		ID: id, First: i, Last: i, Target: -1, Callee: -1, IntoData: -1, fall: -1,
	})
	for _, p := range b.pend[sec] {
		b.edge(p, id)
		b.g.Blocks[p].fall = id
	}
	b.pend[sec] = b.pend[sec][:0]
	for k := range b.frames {
		if f := &b.frames[k]; f.loop && f.first < 0 {
			f.first = id
		}
	}
	if !b.seenSec[sec] {
		b.seenSec[sec] = true
		b.g.Blocks[id].Why |= EntryFirst
	}
	b.cur, b.sec = id, sec
	b.curLabels = true
}

// stmt places statement i.
func (b *cfgBuilder) stmt(i int) {
	st := &b.p.Stmts[i]
	if st.InMacroDef {
		return
	}
	if b.cur >= 0 && st.Section != b.sec {
		b.close()
	}
	switch st.Kind {
	case asm.Instr, asm.MacroCall:
		b.code(i, st)
	case asm.Label:
		if b.dataLabel[i] {
			b.data(i, st.Section)
		} else {
			b.code(i, st)
		}
	case asm.Directive:
		switch {
		case isDataDirective(st):
			b.data(i, st.Section)
			return
		case len(st.Labels) > 0 && b.dataLabel[i]:
			b.data(i, st.Section)
		case len(st.Labels) > 0:
			// A label on a directive line names the next code.
			b.code(i, st)
		}
		b.structural(st.Name, st.Section)
	case asm.Cpp:
		b.structural(st.Name, st.Section)
	}
}

// code adds an instruction or label statement to the open block, opening
// a new one at a label, and closes it after a control transfer.
func (b *cfgBuilder) code(i int, st *asm.Stmt) {
	labelOnly := st.Kind != asm.Instr && st.Kind != asm.MacroCall
	if b.cur < 0 || (len(st.Labels) > 0 && !b.curLabels) {
		b.close()
		b.open(i, st.Section)
	}
	bl := &b.g.Blocks[b.cur]
	bl.Last = i
	b.g.BlockOf[i] = b.cur
	if !labelOnly {
		b.curLabels = false
	}
	if st.Kind != asm.Instr {
		return
	}
	k, ends := exitKind(st)
	if !ends {
		return
	}
	bl.Exit = k
	switch k {
	case ExitCond, ExitJump:
		if t := b.target(i, st); t >= 0 {
			b.jumps = append(b.jumps, edgeRef{b.cur, t})
		}
	case ExitFar:
		b.farSel(bl, st)
		if t := b.farTarget(i, st); t >= 0 {
			b.jumps = append(b.jumps, edgeRef{b.cur, t})
		}
	case ExitCall:
		b.farSel(bl, st)
		t := -1
		if st.IsFar() {
			t = b.farTarget(i, st)
		} else if len(st.Ops) == 1 && st.Ops[0].Kind == asm.OpLabel && !st.Ops[0].Indirect {
			t, _ = b.p.LabelTarget(i, &st.Ops[0])
		}
		if t >= 0 {
			b.calls = append(b.calls, edgeRef{b.cur, t})
		}
	case ExitIndirect:
		b.g.HasIndirect = true
	}
	b.close()
}

// exitKind classifies a block-ending instruction. The category decides
// most of it before any name is compared: every statement comes through
// here.
func exitKind(st *asm.Stmt) (ExitKind, bool) {
	if st.Inst == nil {
		return ExitFall, false
	}
	switch st.Inst.Category {
	case "COND_BR":
		if st.Inst.Name == "xend" {
			return ExitFall, false
		}
		return ExitCond, true
	case "CALL":
		if st.IsCall() {
			return ExitCall, true
		}
	case "UNCOND_BR":
		if !st.IsUncondJump() {
			return ExitFall, false
		}
		if st.IsFar() {
			for k := range st.Ops {
				if st.Ops[k].Indirect || st.Ops[k].Kind != asm.OpImm {
					return ExitIndirect, true
				}
			}
			return ExitFar, true
		}
		if len(st.Ops) == 1 && st.Ops[0].Kind == asm.OpLabel && !st.Ops[0].Indirect {
			return ExitJump, true
		}
		if len(st.Ops) == 0 {
			return ExitJump, true
		}
		return ExitIndirect, true
	case "RET":
		switch {
		case st.IsRet():
			return ExitRet, true
		case st.IsIret():
			return ExitIret, true
		}
	}
	switch st.Inst.Name {
	case "sysret", "sysexit", "rsm", "uiret", "eretu", "erets":
		return ExitIret, true
	case "hlt":
		return ExitHalt, true
	case "ud2", "ud0", "ud1":
		return ExitTrap, true
	}
	return ExitFall, false
}

// target resolves a near branch's label operand to a statement.
func (b *cfgBuilder) target(i int, st *asm.Stmt) int {
	for k := range st.Ops {
		if st.Ops[k].Kind == asm.OpLabel && !st.Ops[k].Indirect {
			t, _ := b.p.LabelTarget(i, &st.Ops[k])
			return t
		}
	}
	return -1
}

// farTarget resolves "ljmp $SEL, $label": the offset operand names a label
// of this document, global or local.
func (b *cfgBuilder) farTarget(i int, st *asm.Stmt) int {
	if len(st.Ops) != 2 || st.Ops[1].Kind != asm.OpImm || st.Ops[1].ImmVal.Known {
		return -1
	}
	name := strings.TrimSpace(st.Ops[1].Imm)
	if !isSymbol(name) {
		return -1
	}
	if isLocal(name) {
		t, _ := b.p.LocalTarget(i, name)
		return t
	}
	if t, ok := b.p.Labels[name]; ok {
		return t
	}
	return -1
}

func (b *cfgBuilder) farSel(bl *Block, st *asm.Stmt) {
	if st.IsFar() && len(st.Ops) == 2 && st.Ops[0].Kind == asm.OpImm && st.Ops[0].ImmVal.Known {
		bl.FarSel = uint64(st.Ops[0].ImmVal.V) & 0xFFFF
		bl.FarSelKnown = true
	}
}

// data handles a data statement: control falling off pending code lands
// in it.
func (b *cfgBuilder) data(i int, sec string) {
	if b.cur >= 0 {
		b.close()
	}
	for _, p := range b.pend[sec] {
		if bl := &b.g.Blocks[p]; bl.IntoData < 0 {
			bl.IntoData = i
		}
	}
}

// structural applies conditional assembly and repetition.
func (b *cfgBuilder) structural(name, sec string) {
	switch name {
	case ".if", ".ifdef", ".ifndef", ".ifnotdef", ".ifc", ".ifnc", ".ifeq", ".ifne",
		".ifeqs", ".ifnes", ".ifge", ".ifgt", ".ifle", ".iflt", ".ifb", ".ifnb",
		"#if", "#ifdef", "#ifndef":
		b.close()
		b.frames = append(b.frames, frame{sec: sec, pre: clone(b.pend[sec]), first: -1})
	case ".else", ".elseif", "#else", "#elif", "#elifdef", "#elifndef":
		b.close()
		f := b.top(false)
		if f == nil {
			return
		}
		f.ends = append(f.ends, b.pend[f.sec]...)
		b.pend[f.sec] = clone(f.pre)
		if name == ".else" || name == "#else" {
			f.sawElse = true
		}
	case ".endif", "#endif":
		b.close()
		f := b.top(false)
		if f == nil {
			return
		}
		out := append(f.ends, b.pend[f.sec]...)
		if !f.sawElse {
			out = append(out, f.pre...)
		}
		b.pend[f.sec] = dedupe(out)
		b.frames = b.frames[:len(b.frames)-1]
	case ".rept", ".irp", ".irpc":
		b.close()
		b.frames = append(b.frames, frame{loop: true, sec: sec, pre: clone(b.pend[sec]), first: -1})
	case ".endr":
		b.close()
		f := b.top(true)
		if f == nil {
			return
		}
		tails := b.pend[f.sec]
		if f.first >= 0 {
			for _, t := range tails {
				b.edge(t, f.first)
			}
		}
		// Zero repetitions are possible as far as the graph knows.
		b.pend[f.sec] = dedupe(append(clone(tails), f.pre...))
		b.frames = b.frames[:len(b.frames)-1]
	}
}

// top returns the innermost open frame of the kind, nil for an unmatched
// closing directive.
func (b *cfgBuilder) top(loop bool) *frame {
	if len(b.frames) == 0 || b.frames[len(b.frames)-1].loop != loop {
		return nil
	}
	return &b.frames[len(b.frames)-1]
}

func clone(s []int) []int { return append([]int(nil), s...) }

func dedupe(s []int) []int {
	sort.Ints(s)
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

var dataDirectives = map[string]bool{
	".byte": true, ".word": true, ".short": true, ".hword": true, ".value": true,
	".long": true, ".int": true, ".quad": true, ".octa": true,
	".2byte": true, ".4byte": true, ".8byte": true,
	".ascii": true, ".asciz": true, ".string": true, ".string8": true, ".string16": true,
	".string32": true, ".string64": true,
	".fill": true, ".space": true, ".skip": true, ".zero": true, ".org": true,
	".incbin": true, ".float": true, ".single": true, ".double": true, ".tfloat": true,
	".dc": true, ".dc.b": true, ".dc.w": true, ".dc.l": true, ".dc.d": true, ".dc.s": true,
	".ds": true, ".ds.b": true, ".ds.w": true, ".ds.l": true,
	".uleb128": true, ".sleb128": true, ".base64": true,
}

// isDataDirective reports a directive that emits bytes control must not
// execute: data, fill, and moving the location counter (". = x").
func isDataDirective(st *asm.Stmt) bool {
	if dataDirectives[st.Name] {
		return true
	}
	switch st.Name {
	case ".set", ".equ", "=":
		return len(st.Args) > 0 && st.Args[0] == "."
	}
	return false
}

// dataLabels marks label statements that name data: the next statement
// emitting anything in the same section is a data directive. Such a label
// is not a code block, so "gdt:" before ".quad" is not an Entry block
// falling into data.
func (b *cfgBuilder) dataLabels() {
	const (
		none = iota
		code
		data
	)
	stmts := b.p.Stmts
	b.dataLabel = make([]bool, len(stmts))
	// Statements of one section come in long runs: the map is only
	// touched when the section changes.
	next := map[string]int{}
	sec, cur := "", none
	for i := len(stmts) - 1; i >= 0; i-- {
		st := &stmts[i]
		if st.InMacroDef {
			continue
		}
		if st.Section != sec {
			next[sec] = cur
			sec, cur = st.Section, next[st.Section]
		}
		switch st.Kind {
		case asm.Instr, asm.MacroCall:
			cur = code
		case asm.Label:
			b.dataLabel[i] = cur == data
		case asm.Directive:
			if isDataDirective(st) {
				cur = data
			} else if len(st.Labels) > 0 {
				b.dataLabel[i] = cur == data
			}
		}
	}
}

// entries applies the entry heuristics.
func (b *cfgBuilder) entries() {
	g, p := b.g, b.p
	mark := func(stmt int, why EntryReason) {
		if blk := b.blockAt(stmt); blk >= 0 {
			g.Blocks[blk].Why |= why
		}
	}
	for i := range p.Stmts {
		st := &p.Stmts[i]
		switch st.Kind {
		case asm.Directive:
			switch st.Name {
			case ".globl", ".global", ".weak":
				for _, a := range st.Args {
					if t, ok := p.Labels[strings.TrimSpace(a)]; ok {
						mark(t, EntryGlobal)
					}
				}
				continue
			case ".extern", ".type", ".size", ".local", ".hidden", ".protected", ".internal",
				".ascii", ".asciz", ".string", ".section", ".file", ".ident":
				continue
			}
			for _, a := range st.Args {
				b.addrTaken(i, a, mark)
			}
		case asm.Instr, asm.MacroCall:
			far := st.IsFar()
			lea := st.Inst != nil && st.Inst.Name == "lea"
			for k := range st.Ops {
				op := &st.Ops[k]
				switch op.Kind {
				case asm.OpImm:
					if far && st.IsBranch() {
						// The offset of "ljmp $SEL, $label" is a branch
						// target, an edge already.
						continue
					}
					b.addrTaken(i, op.Imm, mark)
				case asm.OpMem:
					if lea {
						b.addrTaken(i, op.Disp, mark)
					}
				case asm.OpExpr:
					b.addrTaken(i, op.Text, mark)
				}
			}
		}
	}
	for k := range g.Blocks {
		bl := &g.Blocks[k]
		labeled := len(p.Stmts[bl.First].Labels) > 0
		if labeled && len(bl.Pred) == 0 {
			bl.Why |= EntryNoPred
		}
		if labeled && g.HasIndirect {
			bl.Why |= EntryIndirect
		}
		if k == 0 {
			bl.Why |= EntryFirst
		}
		bl.Entry = bl.Why != 0
	}
}

// addrTaken marks every label the expression text names.
func (b *cfgBuilder) addrTaken(stmt int, text string, mark func(int, EntryReason)) {
	for len(text) > 0 {
		c := text[0]
		if !isSymChar(c) {
			text = text[1:]
			continue
		}
		j := 1
		for j < len(text) && isSymChar(text[j]) {
			j++
		}
		w := text[:j]
		text = text[j:]
		switch {
		case isLocal(w):
			if t, ok := b.p.LocalTarget(stmt, w); ok {
				mark(t, EntryAddrTaken)
			}
		case c >= '0' && c <= '9':
		default:
			if t, ok := b.p.Labels[w]; ok {
				mark(t, EntryAddrTaken)
			}
		}
	}
}

func isSymChar(c byte) bool {
	return c == '_' || c == '.' || c == '$' || (c >= '0' && c <= '9') ||
		(c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isSymbol(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isSymChar(s[i]) {
			return false
		}
	}
	return true
}

// isLocal reports a local label reference, "1f" or "23b".
func isLocal(s string) bool {
	if len(s) < 2 || (s[len(s)-1] != 'f' && s[len(s)-1] != 'b') {
		return false
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Unreachable returns the blocks with no predecessor that are not Entry
// blocks: code after a jmp, ret or halt loop that nothing names.
func (g *CFG) Unreachable() []int {
	var out []int
	for k := range g.Blocks {
		if len(g.Blocks[k].Pred) == 0 && !g.Blocks[k].Entry {
			out = append(out, k)
		}
	}
	return out
}

// Fall returns the block control falls through to from block id, -1 when
// it does not fall through (or falls off the end of the section).
func (g *CFG) Fall(id int) int {
	if id < 0 || id >= len(g.Blocks) {
		return -1
	}
	return g.Blocks[id].fall
}

// ModeOf is the mode block id is assembled for (its first statement's):
// with Target it answers "does this far jump land in code of another
// mode".
func (g *CFG) ModeOf(id int) asm.Mode {
	if id < 0 || id >= len(g.Blocks) {
		return 0
	}
	return g.Prog.Stmts[g.Blocks[id].First].Mode
}

// funcs partitions the blocks into functions.
func (g *CFG) funcs() {
	n := len(g.Blocks)
	start := make([]bool, n)
	for k := range g.Blocks {
		bl := &g.Blocks[k]
		if bl.Why&^EntryIndirect != 0 {
			start[k] = true
		}
		if bl.Callee >= 0 {
			start[bl.Callee] = true
		}
	}
	g.FuncOf = make([]int, n)
	for k := range g.FuncOf {
		g.FuncOf[k] = -1
	}
	seen := make([]int, n) // last function index+1 that visited the block
	var stack []int
	for s := 0; s < n; s++ {
		if !start[s] {
			continue
		}
		fi := len(g.Funcs)
		f := Func{Entry: s, Label: -1}
		if first := &g.Prog.Stmts[g.Blocks[s].First]; len(first.Labels) > 0 {
			f.Name, f.Label = first.Labels[0], g.Blocks[s].First
		}
		stack = append(stack[:0], s)
		seen[s] = fi + 1
		for len(stack) > 0 {
			k := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			f.Blocks = append(f.Blocks, k)
			if g.FuncOf[k] < 0 {
				g.FuncOf[k] = fi
			}
			for _, t := range g.Blocks[k].Succ {
				if seen[t] == fi+1 || (start[t] && t != s) {
					continue
				}
				seen[t] = fi + 1
				stack = append(stack, t)
			}
		}
		sort.Ints(f.Blocks)
		g.Funcs = append(g.Funcs, f)
	}
}
