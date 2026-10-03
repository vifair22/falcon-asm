package inlinec

import (
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// The rules of this package all look at the same thing: an extended asm
// statement's template read as instructions, next to its operand
// constraints. The model is built once per C document and shared through
// the run's memo.
//
// The template is parsed with internal/asm for the statement structure
// (mnemonic, prefixes, operand boundaries), but operands are classified
// here: asm sees "%k0" as the AVX-512 mask register and "%1" as a memory
// reference, while in an extended asm template both are operand
// references, which only inlineasm.OperandRefs knows about.

type opKind int

const (
	oOther opKind = iota // not understood: every rule ignores it
	oReg                 // a register written in the template (%%eax)
	oRef                 // a whole operand reference (%0, %k[x])
	oMem                 // a memory reference written in the template
	oImm                 // $expr
	oLabel               // a branch target
)

// tOp is one operand of a template instruction. Offsets are into Stmt.Asm.
type tOp struct {
	kind       opKind
	start, end int
	text       string // trimmed, without a leading '*'

	fam   string // oReg: register family, "" for a register no rule tracks (%cr0, %ds)
	width int    // oReg: width of the name in bits

	ref int  // oRef: operand index, -1 when it does not resolve
	mod byte // oRef: operand modifier, 0 when none
	seg bool // oRef: written with a segment override (%fs:%0)

	// oMem: the operands referenced inside the address (%1 in (%1), %c2 in
	// %c2(%%rip)) and the registers written literally as base or index.
	addrRefs []int
	addrFams []string
	base     string // family of the base register, "" when none or a reference
}

// insn is one template instruction.
type insn struct {
	name      string // canonical instruction name, lowercase
	mnem      string // as written
	start     int    // Asm offset of the statement (after its labels)
	mnemStart int
	end       int
	width     int // operand size implied by the mnemonic's suffix, 0 when none
	rep, lock bool
	known     bool // resolved against the ISA tables
	branch    bool // jumps or calls somewhere
	uncond    bool // never falls through (jmp, ret)
	// leaves is set for an unconditional transfer out of the template: a
	// return, or a jump to anything but a label the template defines. Code
	// after the asm is never reached through it, so nothing the template
	// did before it can be observed by the compiler.
	leaves bool
	mode   int
	ops    []tOp
}

// stmtModel is one asm statement as the rules see it.
type stmtModel struct {
	st    *inlineasm.Stmt
	index int // position in Context.Inline
	// skip is set for statements no rule may judge: a template cut short
	// by a macro, a statement the user is still typing, or one inside a
	// preprocessor branch that cannot be compiled for this target.
	skip  bool
	mode  int
	ops   []opInfo
	refs  []inlineasm.OperandRef
	insns []insn

	factsOnce sync.Once
	factsVal  *stmtFacts
}

// getFacts computes the statement's facts once; every rule of the run
// shares them.
func (m *stmtModel) getFacts() *stmtFacts {
	m.factsOnce.Do(func() { m.factsVal = m.facts() })
	return m.factsVal
}

const modelKey = "inlinec/model"

// models returns the per-statement models of the C document, built once
// per run.
func models(c *lint.Context) []*stmtModel {
	v := c.Memo(modelKey, func() any { return buildModels(c) })
	m, _ := v.([]*stmtModel)
	return m
}

func targetMode(c *lint.Context) int {
	switch m := int(c.Target.Mode); m {
	case 16, 32:
		return m
	}
	return 64
}

func buildModels(c *lint.Context) []*stmtModel {
	stmts := c.Inline()
	mode := targetMode(c)
	text := ""
	if c.Prog != nil && c.Prog.Doc != nil {
		text = c.Prog.Doc.Text
	}
	inactive := inactiveRanges(text, mode)
	out := make([]*stmtModel, 0, len(stmts))
	for i := range stmts {
		st := &stmts[i]
		m := &stmtModel{st: st, index: i, mode: mode}
		m.skip = st.Partial || st.Unterminated || st.Basic || st.Asm == "" ||
			st.End > len(text) || inRanges(inactive, st.Start)
		if !m.skip {
			m.ops = analyzeOperands(st)
			m.refs = inlineasm.OperandRefs(*st)
			m.insns = parseTemplate(st.Asm, m.refs, len(st.Outputs)+len(st.Inputs), mode)
			m.skip = foreign(st.Asm, m.insns)
		}
		out = append(out, m)
	}
	return out
}

// foreign reports a template that is not AT&T x86: Intel syntax, or
// another architecture's asm in a file shared between targets, in a branch
// the conditional evaluator could not rule out. Judging it as x86 would
// read ARM's "mov %0, sp" as a store to a symbol named sp. The signs: an
// .intel_syntax directive, a register name written without '%' (never a
// register in AT&T syntax), or instructions none of which is x86.
func foreign(text string, insns []insn) bool {
	if strings.Contains(strings.ToLower(text), ".intel_syntax") {
		return true
	}
	instrs, known := 0, 0
	for i := range insns {
		in := &insns[i]
		if in.mnem == "" {
			continue // a directive
		}
		instrs++
		if in.known {
			known++
		}
		for _, op := range in.ops {
			if (op.kind == oMem || op.kind == oLabel) && bareRegister(op.text) {
				return true
			}
		}
	}
	return instrs > 0 && known == 0
}

// bareRegister reports a register name of x86, ARM or RISC-V written as a
// plain word.
func bareRegister(t string) bool {
	if !isIdent(t) {
		return false
	}
	if _, _, ok := family(t); ok {
		return true
	}
	switch t {
	case "sp", "lr", "pc", "fp", "xzr", "wzr", "zero", "ra", "gp", "tp":
		return true
	}
	if n := len(t); n >= 2 && strings.IndexByte("rxwvqdsatS", t[0]) >= 0 {
		if _, ok := smallNumAny(t[1:]); ok {
			return true
		}
	}
	return false
}

// parseTemplate splits the template into instructions.
func parseTemplate(text string, refs []inlineasm.OperandRef, nOps, mode int) []insn {
	doc := &docstore.Document{Text: text}
	prog := asm.Parse(doc, lexer.Tokenize(text), asm.Config{InitialMode: asm.Mode(mode)})
	var out []insn
	var pending []string // prefixes written as their own statement ("rep;")
	local := map[string]bool{}
	for k := range prog.Stmts {
		for _, l := range prog.Stmts[k].Labels {
			local[l] = true
		}
	}
	for k := range prog.Stmts {
		s := &prog.Stmts[k]
		if s.Kind != asm.Instr && s.Kind != asm.MacroCall && s.Kind != asm.Directive {
			continue
		}
		if s.Kind == asm.Instr && s.Mnemonic == "" && len(s.Prefixes) > 0 {
			pending = append(pending, s.Prefixes...)
			continue
		}
		in := insn{mnem: s.Mnemonic, start: s.Start, mnemStart: s.MnemStart, end: s.End, mode: int(s.Mode)}
		if in.mode == 0 {
			in.mode = mode
		}
		if s.Kind == asm.Directive {
			// .byte-encoded instructions and the like: unknown.
			in.mnemStart = s.Start
			out = append(out, in)
			pending = nil
			continue
		}
		for _, p := range append(pending, s.Prefixes...) {
			switch strings.ToLower(p) {
			case "rep", "repe", "repz", "repne", "repnz":
				in.rep = true
			case "lock":
				in.lock = true
			}
		}
		pending = nil
		if s.Kind == asm.Instr && s.Inst != nil {
			in.known = true
			in.name = strings.ToLower(s.Inst.Name)
			in.branch = s.IsBranch() || s.IsRet() || s.IsIret()
			in.uncond = s.IsUncondJump() || s.IsRet() || s.IsIret()
		}
		in.width = suffixWidth(s.Suffix)
		if in.width == 0 && len(in.mnem) == len(in.name)+1 && strings.HasPrefix(strings.ToLower(in.mnem), in.name) {
			// Some forms (inb, outl) resolve without reporting the suffix.
			in.width = suffixWidth(strings.ToLower(in.mnem[len(in.name):]))
		}
		for j := range s.Ops {
			in.ops = append(in.ops, classify(text, &s.Ops[j], refs, in.branch))
		}
		normalizeString(&in)
		normalizeMovq(&in)
		in.leaves = in.uncond && leavesTemplate(&in, local, nOps)
		out = append(out, in)
	}
	return out
}

func suffixWidth(s string) int {
	switch s {
	case "b":
		return 8
	case "w":
		return 16
	case "l":
		return 32
	case "q":
		return 64
	}
	return 0
}

// stringOps are the string instructions; their explicit operands only
// document the implicit ones, so the rules use the implicit table.
var stringOps = map[string]bool{
	"movs": true, "stos": true, "lods": true, "scas": true, "cmps": true, "ins": true, "outs": true,
}

// normalizeString gives the operand-less movsd and cmpsd their string
// meaning (the ISA tables resolve them as the SSE instructions) and reads
// the element size from the mnemonic.
func normalizeString(in *insn) {
	if len(in.ops) == 0 && (in.name == "movsd" || in.name == "cmpsd") {
		in.name = in.name[:4]
	}
	if !stringOps[in.name] || len(in.mnem) == 0 {
		return
	}
	switch in.mnem[len(in.mnem)-1] {
	case 'b':
		in.width = 8
	case 'w':
		in.width = 16
	case 'l', 'd':
		in.width = 32
	case 'q':
		in.width = 64
	}
}

// normalizeMovq treats movq between general registers and memory as the
// 64-bit mov it is; the ISA tables resolve the mnemonic to the MMX/SSE
// movq, which no width or save rule would recognise.
func normalizeMovq(in *insn) {
	if in.name != "movq" {
		return
	}
	for _, op := range in.ops {
		if op.kind == oReg && !isGPR(op.fam) {
			return
		}
	}
	in.name, in.width = "mov", 64
}

// classify reads one operand's text.
func classify(text string, op *asm.Operand, refs []inlineasm.OperandRef, branch bool) tOp {
	start, end := op.Start, op.End
	if start < 0 || end > len(text) || start > end {
		return tOp{kind: oOther, start: max(0, min(start, len(text))), end: max(0, min(end, len(text)))}
	}
	for start < end && (text[start] == '*' || text[start] == ' ' || text[start] == '\t') {
		start++
	}
	t := tOp{start: start, end: end, text: text[start:end], ref: -1}
	// "%gs:%0" is operand 0 (a memory operand) with a segment override.
	if seg := strings.IndexByte(t.text, ':'); seg == 3 && t.text[0] == '%' && t.text[2] == 's' {
		for _, r := range refs {
			if r.Offset == start+seg+1 && r.Offset+r.Len == end {
				return tOp{kind: oRef, start: start, end: end, text: t.text, ref: r.Index, mod: r.Modifier, seg: true}
			}
		}
	}
	var inside []inlineasm.OperandRef
	for _, r := range refs {
		if r.Offset >= start && r.Offset+r.Len <= end {
			inside = append(inside, r)
		}
	}
	switch {
	case len(inside) == 1 && inside[0].Offset == start && inside[0].Offset+inside[0].Len == end:
		t.kind, t.ref, t.mod = oRef, inside[0].Index, inside[0].Modifier
	case strings.HasPrefix(t.text, "$"):
		t.kind = oImm
	case strings.HasPrefix(t.text, "%") && !strings.ContainsAny(t.text, "(:") && len(inside) == 0:
		t.kind = oReg
		t.fam, t.width, _ = family(t.text)
	case op.Kind == asm.OpLabel || branch && !strings.ContainsAny(t.text, "(%"):
		t.kind = oLabel
	default:
		t.kind = oMem
		for _, r := range inside {
			t.addrRefs = append(t.addrRefs, r.Index)
		}
		memAddress(&t, inside)
	}
	return t
}

// memAddress finds the literal base and index registers of a memory
// operand: the first two comma-separated parts inside its last parenthesis
// group. A part that is an operand reference (%1, %q2) is not a literal
// register, even when it reads like one (%k1 is operand 1, not mask k1).
func memAddress(t *tOp, refs []inlineasm.OperandRef) {
	open := strings.LastIndexByte(t.text, '(')
	closeIdx := strings.LastIndexByte(t.text, ')')
	if open < 0 || closeIdx < open {
		return
	}
	pos := t.start + open + 1
	for k, p := range strings.Split(t.text[open+1:closeIdx], ",") {
		pStart, pEnd := pos, pos+len(p)
		pos = pEnd + 1
		if k > 1 {
			break
		}
		isRef := false
		for _, r := range refs {
			if r.Offset >= pStart && r.Offset < pEnd {
				isRef = true
			}
		}
		if isRef {
			continue
		}
		if fam, _, ok := family(p); ok {
			t.addrFams = append(t.addrFams, fam)
			if k == 0 {
				t.base = fam
			}
		}
	}
}

// leavesTemplate reports whether an unconditional jump or return goes
// somewhere outside the template.
func leavesTemplate(in *insn, local map[string]bool, nOps int) bool {
	if len(in.ops) == 0 {
		return true // ret, iret
	}
	t := in.ops[len(in.ops)-1]
	switch t.kind {
	case oRef:
		// An asm goto label (%l[out]) is compiled code after the asm.
		return t.ref < nOps
	case oImm, oLabel:
		name := strings.TrimPrefix(t.text, "$")
		if n := len(name); n >= 2 && (name[n-1] == 'f' || name[n-1] == 'b') {
			if _, ok := smallNumAny(name[:n-1]); ok {
				return false
			}
		}
		return !local[name]
	}
	return true
}

// smallNumAny parses a decimal local-label number of any length.
func smallNumAny(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
