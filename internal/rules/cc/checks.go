package cc

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func checkAnnotation(c *lint.Context, m *model) {
	for _, p := range m.problems {
		c.ReportRange(p.start, p.end, "falcon-asm annotation: "+p.msg)
	}
}

// line is a statement's one-based line, for messages.
func (m *model) line(i int) int { return m.prog.Stmts[i].Line + 1 }

// loc is a statement's location, for Related.
func (m *model) loc(i int) protocol.Location {
	st := &m.prog.Stmts[i]
	start := st.Start
	if st.Kind == asm.Instr && st.MnemStart > start {
		start = st.MnemStart
	}
	return protocol.Location{URI: m.prog.Doc.URI, Range: m.prog.Range(start, st.End)}
}

// report emits a finding on statement i (from its mnemonic, so the labels
// before it are not underlined) with related locations.
func (m *model) report(c *lint.Context, i int, msg string, related []int, fixes ...lint.Fix) {
	st := &m.prog.Stmts[i]
	start := st.Start
	if st.Kind == asm.Instr && st.MnemStart > start {
		start = st.MnemStart
	}
	f := lint.Finding{Range: m.prog.Range(start, st.End), Message: msg, Fixes: fixes}
	for _, r := range related {
		f.Related = append(f.Related, m.loc(r))
	}
	c.ReportFinding(f)
}

// reportOp emits a finding on operand op of statement i, or on the whole
// statement for an implicit operand (op < 0).
func (m *model) reportOp(c *lint.Context, i, op int, msg string, related []int) {
	st := &m.prog.Stmts[i]
	if op < 0 || op >= len(st.Ops) {
		m.report(c, i, msg, related)
		return
	}
	f := lint.Finding{Range: m.prog.Range(st.Ops[op].Start, st.Ops[op].End), Message: msg}
	for _, r := range related {
		f.Related = append(f.Related, m.loc(r))
	}
	c.ReportFinding(f)
}

// regName spells family r at the function's width: %bx in 16-bit code.
func regName(r flow.Reg, bits int) string {
	if r.IsVector() {
		return "%" + r.String()
	}
	if n := r.Name(bits); n != "" {
		return "%" + n
	}
	return "%" + r.String()
}

// fname names a function for messages.
func (f *function) fname() string {
	if f.name != "" {
		return f.name
	}
	return "this function"
}

// exits lists the statements ending function fi's blocks with one of the
// given exits.
func (m *model) exits(fi int, kinds ...flow.ExitKind) []int {
	var out []int
	for _, b := range m.g.Funcs[fi].Blocks {
		bl := &m.g.Blocks[b]
		for _, k := range kinds {
			if bl.Exit == k {
				out = append(out, bl.Last)
			}
		}
	}
	return out
}

// returns lists the statements where function fi returns to its caller:
// a ret, or an lret at the entry depth. A ret or lret with something still
// pushed is a jump (push $sel; push $addr; lretq switches modes, push
// addr; ret is a computed jump) and is not a return; an lret on an
// unknown stack is assumed to be one of those, since a far-called
// function returns from a balanced frame.
func (m *model) returns(fi int) []int {
	var out []int
	for _, i := range m.exits(fi, flow.ExitRet) {
		s := m.res.Before(i)
		if s == nil || !s.Reachable {
			continue
		}
		far := m.prog.Stmts[i].Inst != nil && m.prog.Stmts[i].Inst.Name == "lret"
		if s.StackKnown && s.Stack != 0 || far && !s.StackKnown {
			continue
		}
		out = append(out, i)
	}
	return out
}

// calls lists the call statements of function fi.
func (m *model) calls(fi int) []int {
	var out []int
	m.eachStmt(fi, func(i int) bool {
		if m.prog.Stmts[i].IsCall() && m.res.Reachable(i) {
			out = append(out, i)
		}
		return true
	})
	return out
}

func checkCalleeSaved(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.conv == nil || f.handler != nil {
			continue
		}
		for _, ret := range m.returns(fi) {
			for _, r := range f.conv.Preserved.Regs() {
				if !r.IsGPR() || r == flow.RSP || r >= flow.R8 && f.bits != 64 {
					continue
				}
				if w, ok := m.unrestored(fi, r, ret); ok {
					m.report(c, ret, fmt.Sprintf("%s is callee-saved under %s, but %s returns here with it changed "+
						"(written on line %d) and not restored", regName(r, f.bits), f.conv.Name, f.fname(), m.line(w)),
						[]int{w})
				}
			}
		}
		if len(m.returns(fi)) == 0 {
			continue
		}
		m.vectorSaved(c, fi)
		if f.conv.SavesControl {
			m.controlSaved(c, fi)
		}
	}
}

// vectorSaved reports a callee-saved vector register (MS x64's
// xmm6-xmm15) written in a function that never stores it to memory. The
// analysis tracks no vector values, so this is the syntactic check: a
// save is a store of the register anywhere in the function.
func (m *model) vectorSaved(c *lint.Context, fi int) {
	f := &m.funcs[fi]
	written := map[flow.Reg]int{}
	saved := map[flow.Reg]bool{}
	m.eachStmt(fi, func(i int) bool {
		st := &m.prog.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || !m.res.Reachable(i) {
			return true
		}
		n := len(st.Ops)
		if st.Inst.Name == "vzeroall" {
			for k := 0; k < 16; k++ {
				if _, seen := written[flow.Vec(k)]; !seen {
					written[flow.Vec(k)] = i
				}
			}
			return true
		}
		if n == 0 {
			return true
		}
		last := &st.Ops[n-1]
		for k := 0; k < n-1; k++ {
			if r, _, _, ok := flow.RegOf(st.Ops[k].RegName); ok && st.Ops[k].Kind == asm.OpReg && r.IsVector() && last.Kind == asm.OpMem {
				saved[r] = true
			}
		}
		if last.Kind != asm.OpReg || vecReadOnly(st.Inst.Name) {
			return true
		}
		if r, _, _, ok := flow.RegOf(last.RegName); ok && r.IsVector() {
			if _, seen := written[r]; !seen {
				written[r] = i
			}
		}
		return true
	})
	for _, r := range f.conv.Preserved.Regs() {
		w, ok := written[r]
		if !r.IsVector() || !ok || saved[r] {
			continue
		}
		m.report(c, w, fmt.Sprintf("%s is callee-saved under %s, but %s changes it and never saves it", regName(r, f.bits),
			f.conv.Name, f.fname()), nil)
	}
}

// vecReadOnly are vector instructions that only read their last operand.
func vecReadOnly(name string) bool {
	switch name {
	case "ptest", "vptest", "vtestps", "vtestpd", "ucomiss", "ucomisd", "comiss", "comisd",
		"vucomiss", "vucomisd", "vcomiss", "vcomisd":
		return true
	}
	return false
}

// controlSaved reports a function that loads the MXCSR or the x87
// control word without ever storing it: SysV and MS x64 make their
// control bits callee-saved, and a caller's rounding mode or exception
// masks silently change otherwise.
func (m *model) controlSaved(c *lint.Context, fi int) {
	f := &m.funcs[fi]
	type ctl struct {
		load          int
		saved         bool
		what, name    string
		loads, stores []string
	}
	regs := []*ctl{
		{load: -1, what: "the MXCSR", name: "The MXCSR control bits are",
			loads: []string{"ldmxcsr", "vldmxcsr"}, stores: []string{"stmxcsr", "vstmxcsr"}},
		{load: -1, what: "the x87 control word", name: "The x87 control word is",
			loads:  []string{"fldcw", "finit", "fninit", "fldenv"},
			stores: []string{"fstcw", "fnstcw", "fstenv", "fnstenv", "fsave", "fnsave"}},
	}
	m.eachStmt(fi, func(i int) bool {
		st := &m.prog.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || !m.res.Reachable(i) {
			return true
		}
		for _, x := range regs {
			for _, n := range x.loads {
				if st.Inst.Name == n && x.load < 0 {
					x.load = i
				}
			}
			for _, n := range x.stores {
				if st.Inst.Name == n {
					x.saved = true
				}
			}
		}
		return true
	})
	for _, x := range regs {
		if x.load >= 0 && !x.saved {
			m.report(c, x.load, fmt.Sprintf("%s callee-saved under %s, but %s changes %s without saving it first",
				x.name, f.conv.Name, f.fname(), x.what), nil)
		}
	}
}

func checkCallerSaved(c *lint.Context, m *model) {
	p := m.prog
	for j := range p.Stmts {
		st := &p.Stmts[j]
		if st.Kind != asm.Instr || st.Inst == nil || st.InMacroDef {
			continue
		}
		before := m.res.Before(j)
		if before == nil || !before.Reachable {
			continue
		}
		for _, u := range reads(st) {
			call := before.Get(u.r).Def
			if call < 0 || !p.Stmts[call].IsCall() {
				continue
			}
			_, conv := m.callee(call)
			if conv == nil || !conv.Clobbered.Has(u.r) || conv.IsReturn(u.r) {
				continue
			}
			bits := modeBits(st.Mode)
			m.reportOp(c, j, u.op, fmt.Sprintf("%s is read here, but the call to %s on line %d may have changed it "+
				"(caller-saved under %s); keep the value in a callee-saved register or reload it after the call",
				regName(u.r, bits), m.calleeName(call), m.line(call), conv.Name), []int{call})
		}
	}
}

// entryMisalign is how far the address depth bytes below a 64-bit
// function's entry rsp, plus disp, is from a 16-byte boundary: entry rsp
// is 8 past one, because the call pushed the return address onto an
// aligned stack.
func entryMisalign(depth, disp int) int {
	return ((8-depth+disp)%16 + 16) % 16
}

func checkStackAlign(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.conv == nil || f.conv.Align == 0 || f.bits != 64 || !f.entered || f.handler != nil {
			continue
		}
		for _, k := range m.calls(fi) {
			if _, conv := m.callee(k); conv == nil || conv.Align == 0 {
				continue
			}
			st := m.res.Before(k)
			if !st.StackKnown {
				continue
			}
			if mis := entryMisalign(st.Stack, 0); mis != 0 {
				m.report(c, k, fmt.Sprintf("rsp is %d bytes off a 16-byte boundary at this call (%d bytes below the "+
					"return address); %s requires 16-byte alignment at a call", mis, st.Stack, f.conv.Name), nil)
			}
		}
	}
}

func checkShadowSpace(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.conv == nil || f.conv.Shadow == 0 || f.bits != 64 || f.handler != nil {
			continue
		}
		for _, k := range m.calls(fi) {
			_, conv := m.callee(k)
			if conv == nil || conv.Shadow == 0 {
				continue
			}
			st := m.res.Before(k)
			if !st.StackKnown {
				continue
			}
			room := st.Stack
			if top, ok := st.Top(); ok {
				room = st.Stack - top.Depth
			}
			if room < conv.Shadow {
				m.report(c, k, fmt.Sprintf("only %d bytes are reserved below the last push at this call; %s callers must "+
					"leave %d bytes of shadow space for the callee to spill its register arguments into",
					max(room, 0), conv.Name, conv.Shadow), nil)
			}
		}
	}
}

// stackOperand returns the memory operand of st based on the stack
// pointer (no index), with its known displacement.
func stackOperand(st *asm.Stmt, base flow.Reg) (int, int, bool) {
	for k := range st.Ops {
		op := &st.Ops[k]
		if op.Kind != asm.OpMem || op.Index != "" || op.Base == "" || op.Disp != "" && !op.DispVal.Known {
			continue
		}
		if r, _, _, ok := flow.RegOf(op.Base); ok && r == base {
			return k, int(op.DispVal.V), true
		}
	}
	return 0, 0, false
}

func checkRedZone(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.conv == nil || f.conv.Bits != 64 || f.bits != 64 || f.handler != nil {
			continue
		}
		m.eachStmt(fi, func(i int) bool {
			st := &m.prog.Stmts[i]
			if st.Kind != asm.Instr || st.Inst == nil || !m.res.Reachable(i) {
				return true
			}
			switch {
			case st.Inst.Name == "lea", strings.HasPrefix(st.Inst.Name, "prefetch"), st.Inst.Name == "nop":
				return true
			}
			k, disp, ok := stackOperand(st, flow.RSP)
			if !ok || disp >= 0 {
				return true
			}
			switch {
			case f.conv.RedZone == 0:
				m.reportOp(c, i, k, fmt.Sprintf("this accesses %d bytes below rsp, but %s has no red zone: an interrupt, "+
					"exception dispatch or debugger may overwrite it at any time; move rsp down first", -disp, f.conv.Name), nil)
			case -disp > f.conv.RedZone:
				m.reportOp(c, i, k, fmt.Sprintf("this accesses %d bytes below rsp, beyond the %d-byte red zone %s protects; "+
					"move rsp down first", -disp, f.conv.RedZone, f.conv.Name), nil)
			}
			return true
		})
	}
}

// alignedSSE are the moves that fault on an address not aligned to 16.
var alignedSSE = map[string]bool{
	"movaps": true, "movapd": true, "movdqa": true, "movntps": true, "movntpd": true, "movntdq": true,
	"movntdqa": true, "vmovaps": true, "vmovapd": true, "vmovdqa": true, "vmovdqa32": true, "vmovdqa64": true,
	"vmovntps": true, "vmovntpd": true, "vmovntdq": true, "vmovntdqa": true, "fxsave": true, "fxrstor": true,
	"fxsave64": true, "fxrstor64": true,
}

func checkFrameAlign(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.conv == nil || f.conv.Align == 0 || f.bits != 64 || !f.entered || f.handler != nil {
			continue
		}
		m.eachStmt(fi, func(i int) bool {
			st := &m.prog.Stmts[i]
			if st.Kind != asm.Instr || st.Inst == nil || !alignedSSE[st.Inst.Name] || !m.res.Reachable(i) {
				return true
			}
			s := m.res.Before(i)
			k, disp, ok := stackOperand(st, flow.RSP)
			depth, known := s.Stack, s.StackKnown
			base := "rsp"
			if !ok {
				k, disp, ok = stackOperand(st, flow.RBP)
				depth, known, base = s.FrameDepth, s.FrameKnown, "rbp"
			}
			if !ok || !known {
				return true
			}
			if mis := entryMisalign(depth, disp); mis != 0 {
				m.reportOp(c, i, k, fmt.Sprintf("%s needs a 16-byte aligned address, but this one is %d bytes off: %s "+
					"is %d bytes below the return address here and the return address sits 8 past a 16-byte boundary",
					st.Mnemonic, mis, base, depth), nil)
			}
			return true
		})
	}
}

// argReads are the argument registers function fi reads before writing:
// for each, the first such read. A push is not a use (it is how a register
// is saved), so a preserved register that is also an argument is not
// mistaken for one being read.
func (m *model) argReads(fi int) map[flow.Reg]int {
	f := &m.funcs[fi]
	out := map[flow.Reg]int{}
	m.eachStmt(fi, func(i int) bool {
		st := &m.prog.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || st.Inst.Name == "push" {
			return true
		}
		before := m.res.Before(i)
		if before == nil || !before.Reachable {
			return true
		}
		for _, u := range reads(st) {
			if !f.conv.IsArg(u.r) || before.Get(u.r).Def != flow.DefEntry {
				continue
			}
			if at, seen := out[u.r]; !seen || i < at {
				out[u.r] = i
			}
		}
		return true
	})
	return out
}

// owner is the function a statement belongs to, -1 for none.
func (m *model) owner(i int) int {
	b := m.g.BlockOf[i]
	if b < 0 {
		return -1
	}
	return m.g.FuncOf[b]
}

func checkArgUnset(c *lint.Context, m *model) {
	reads := map[int]map[flow.Reg]int{}
	for fi := range m.funcs {
		caller := m.funcs[fi].conv
		if caller == nil {
			continue
		}
		for _, k := range m.calls(fi) {
			if m.owner(k) != fi {
				continue // a shared tail: reported once, from its owner
			}
			cf, conv := m.callee(k)
			if cf < 0 || conv == nil || len(conv.Args) == 0 {
				continue
			}
			ar, ok := reads[cf]
			if !ok {
				ar = m.argReads(cf)
				reads[cf] = ar
			}
			for _, r := range conv.Args {
				at, used := ar[r]
				if !used || caller.IsArg(r) || !m.reaching(k, r).entry {
					continue
				}
				m.report(c, k, fmt.Sprintf("%s reads %s as an argument (line %d), but it is not set on some path to this call",
					m.funcs[cf].fname(), regName(r, m.funcs[cf].bits), m.line(at)), []int{at})
			}
		}
	}
}

// readAfter looks for a read of family r after the call at statement
// call, before anything writes it, along every path from the return
// point up to the next call, ret or jump out of the function. It returns
// the first such read.
func (m *model) readAfter(call int, r flow.Reg) (int, bool) {
	g := m.g
	b := g.BlockOf[call]
	if b < 0 {
		return 0, false
	}
	type at struct{ b, from int }
	work := []at{}
	for _, s := range g.Blocks[b].Succ {
		work = append(work, at{s, g.Blocks[s].First})
	}
	seen := map[int]bool{}
	for len(work) > 0 && len(seen) < 64 {
		w := work[len(work)-1]
		work = work[:len(work)-1]
		if seen[w.b] {
			continue
		}
		seen[w.b] = true
		bl := &g.Blocks[w.b]
		stop := false
		for i := w.from; i <= bl.Last && !stop; i++ {
			if g.BlockOf[i] != w.b {
				continue
			}
			st := &m.prog.Stmts[i]
			if st.Kind != asm.Instr || st.Inst == nil {
				continue
			}
			if _, ok := readsReg(st, r); ok {
				return i, true
			}
			stop = m.writes(i, r) || st.IsCall() || st.IsRet() || st.IsIret()
		}
		if stop {
			continue
		}
		for _, s := range bl.Succ {
			work = append(work, at{s, g.Blocks[s].First})
		}
	}
	return 0, false
}

func checkReturnUnset(c *lint.Context, m *model) {
	evidence := map[int]map[flow.Reg]int{}
	for k := range m.prog.Stmts {
		if !m.prog.Stmts[k].IsCall() || !m.res.Reachable(k) {
			continue
		}
		cf, conv := m.callee(k)
		if cf < 0 || conv == nil {
			continue
		}
		for _, r := range conv.Returns {
			if at, ok := m.readAfter(k, r); ok {
				if evidence[cf] == nil {
					evidence[cf] = map[flow.Reg]int{}
				}
				if _, seen := evidence[cf][r]; !seen {
					evidence[cf][r] = at
				}
			}
		}
	}
	for fi := range m.funcs {
		ev := evidence[fi]
		f := &m.funcs[fi]
		if len(ev) == 0 || f.conv == nil {
			continue
		}
		for _, ret := range m.returns(fi) {
			for _, r := range f.conv.Returns {
				at, ok := ev[r]
				if !ok || !m.reaching(ret, r).entry {
					continue
				}
				m.report(c, ret, fmt.Sprintf("%s can return here without setting %s, which the caller on line %d reads as its result",
					f.fname(), regName(r, f.bits), m.line(at)), []int{at})
			}
		}
	}
}

func checkHandlerRet(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.handler == nil {
			continue
		}
		for _, ret := range m.exits(fi, flow.ExitRet) {
			st := &m.prog.Stmts[ret]
			if b := m.res.Before(ret); b == nil || !b.Reachable || b.StackKnown && b.Stack > 0 {
				// Unreachable, or a jump through the stack (push addr;
				// ret) rather than a return.
				continue
			}
			if st.Inst == nil || st.Inst.Name == "lret" && len(st.Ops) > 0 {
				continue
			}
			iret := "iret"
			if st.Mode == asm.Mode64 {
				iret = "iretq"
			}
			left := "flags and code segment"
			if st.Inst.Name == "lret" {
				left = "flags"
			}
			fix := lint.NewFix("Return with "+iret, c.ReplaceRange(st.MnemStart, st.MnemStart+len(st.Mnemonic), iret))
			m.report(c, ret, fmt.Sprintf("%s is an interrupt handler (%s) but returns with %s, which leaves the "+
				"interrupted code's %s on the stack; return with %s", f.fname(), f.handler.how, st.Mnemonic, left, iret),
				nil, fix)
		}
	}
}

func checkHandlerPreserve(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.handler == nil || !f.handler.preserves() {
			continue
		}
		for _, ret := range m.exits(fi, flow.ExitIret, flow.ExitRet) {
			for r := flow.RAX; r <= flow.R15; r++ {
				if r == flow.RSP || r >= flow.R8 && f.bits != 64 {
					continue
				}
				if w, ok := m.unrestored(fi, r, ret); ok {
					m.report(c, ret, fmt.Sprintf("%s is an interrupt handler (%s) and returns with %s changed (written "+
						"on line %d); the interrupted code expects every register intact, so save and restore it",
						f.fname(), f.handler.how, regName(r, f.bits), m.line(w)), []int{w})
				}
			}
		}
	}
}

func checkHandlerErrcode(c *lint.Context, m *model) {
	for fi := range m.funcs {
		f := &m.funcs[fi]
		if f.handler == nil || f.bits == 16 {
			continue
		}
		has := f.handler.errcode()
		if has == flow.Unknown {
			continue
		}
		w := 4
		if f.bits == 64 {
			w = 8
		}
		vec := "this vector"
		if f.handler.vector >= 0 {
			vec = fmt.Sprintf("vector %d", f.handler.vector)
		}
		for _, ret := range m.exits(fi, flow.ExitIret) {
			st := m.res.Before(ret)
			if st == nil || !st.Reachable || !st.StackKnown {
				continue
			}
			switch {
			case has == flow.Set && st.Stack == 0:
				add := "addl $4, %esp"
				if w == 8 {
					add = "addq $8, %rsp"
				}
				m.report(c, ret, fmt.Sprintf("the processor pushes an error code for %s, and it is still on the stack "+
					"here: iret would pop it as the return address; discard it first (%s)", vec, add), nil,
					lint.NewFix("Discard the error code", c.InsertLineAbove(ret, add)))
			case has == flow.Clear && st.Stack == -w:
				m.report(c, ret, fmt.Sprintf("the processor pushes no error code for %s, but %s discards %d bytes "+
					"before iret, so iret pops the wrong frame", vec, f.fname(), w), nil)
			}
		}
	}
}
