package flow

import (
	"reflect"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

func build16(t *testing.T, src string) *CFG {
	t.Helper()
	g := Build(parse(t, src, asm.Mode16))
	checkCFG(t, g)
	return g
}

func TestIfElse(t *testing.T) {
	g := build16(t, `
f:	cmpw $1, %ax
	je 1f
	movw $2, %bx
	jmp 2f
1:	movw $3, %bx
2:	ret
`)
	head := blockAtText(t, g, "cmpw")
	then := blockAtText(t, g, "movw $2")
	els := blockAtText(t, g, "movw $3")
	join := blockAtText(t, g, "ret")
	if g.Blocks[head].Exit != ExitCond || !sameSet(g.Blocks[head].Succ, []int{then, els}) {
		t.Fatalf("head: %s", dump(g))
	}
	if g.Blocks[then].Exit != ExitJump || !sameSet(g.Blocks[then].Succ, []int{join}) || g.Blocks[then].Target != join {
		t.Fatalf("then: %s", dump(g))
	}
	if !sameSet(g.Blocks[els].Succ, []int{join}) || !sameSet(g.Blocks[join].Pred, []int{then, els}) {
		t.Fatalf("else/join: %s", dump(g))
	}
	if g.Blocks[join].Exit != ExitRet || len(g.Blocks[join].Succ) != 0 {
		t.Fatalf("ret: %s", dump(g))
	}
	if g.Blocks[head].Target != els {
		t.Fatalf("cond target %d want %d", g.Blocks[head].Target, els)
	}
	if len(g.Unreachable()) != 0 {
		t.Fatalf("unreachable %v", g.Unreachable())
	}
}

func sameSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

func TestLoopsAndLocalLabels(t *testing.T) {
	g := build16(t, `
	movw $10, %cx
1:	decw %cx
	jnz 1b
1:	loop 1b
	jmp 1f
	nop
1:	ret
`)
	l1 := blockAtText(t, g, "decw")
	if !contains(g.Blocks[l1].Succ, l1) || !contains(g.Blocks[l1].Pred, l1) {
		t.Fatalf("self loop: %s", dump(g))
	}
	l2 := blockAtText(t, g, "loop")
	if !contains(g.Blocks[l2].Succ, l2) {
		t.Fatalf("loop 1b must target the second 1: %s", dump(g))
	}
	nop := blockAtText(t, g, "nop")
	if !reflect.DeepEqual(g.Unreachable(), []int{nop}) {
		t.Fatalf("unreachable %v want [%d]: %s", g.Unreachable(), nop, dump(g))
	}
	if got := g.Blocks[blockAtText(t, g, "jmp 1f")].Target; got != blockAtText(t, g, "ret") {
		t.Fatalf("jmp 1f target %d", got)
	}
}

func TestCalls(t *testing.T) {
	g := build16(t, `
	.globl main
main:	call helper
	call extern_fn
	call *%ax
	lcall $0x10, $helper
	ret
helper:	ret
`)
	c1 := blockAtText(t, g, "call helper")
	h := blockAtText(t, g, "helper:")
	if g.Blocks[c1].Exit != ExitCall || g.Blocks[c1].Callee != h {
		t.Fatalf("call: %s", dump(g))
	}
	if contains(g.Blocks[c1].Succ, h) || !sameSet(g.Blocks[c1].Succ, []int{blockAtText(t, g, "call extern_fn")}) {
		t.Fatalf("call must fall to next, not edge to callee: %s", dump(g))
	}
	if g.Blocks[blockAtText(t, g, "call extern_fn")].Callee != -1 || g.Blocks[blockAtText(t, g, "call *%ax")].Callee != -1 {
		t.Fatal("unresolved/indirect callee")
	}
	lc := &g.Blocks[blockAtText(t, g, "lcall")]
	if lc.Callee != h || !lc.FarSelKnown || lc.FarSel != 0x10 {
		t.Fatalf("lcall: %+v", *lc)
	}
	if g.HasIndirect {
		t.Fatal("indirect call is not an indirect jump")
	}
	if len(g.Funcs) != 2 || g.Funcs[0].Name != "main" || g.Funcs[1].Name != "helper" {
		t.Fatalf("funcs %+v", g.Funcs)
	}
	if g.FuncOf[h] != 1 || g.FuncOf[c1] != 0 {
		t.Fatalf("FuncOf %v", g.FuncOf)
	}
	if g.Blocks[h].Why&EntryNoPred == 0 {
		t.Fatal("a helper reached only by call has no predecessors")
	}
}

func TestIndirectJump(t *testing.T) {
	g := build16(t, `
	jmp *%ax
a:	nop
	jmp b
b:	ret
	movw $1, %ax
`)
	j := &g.Blocks[blockAtText(t, g, "jmp *%ax")]
	if j.Exit != ExitIndirect || len(j.Succ) != 0 || !g.HasIndirect {
		t.Fatalf("indirect: %s", dump(g))
	}
	b := &g.Blocks[blockAtText(t, g, "b:")]
	if !b.Entry || b.Why != EntryIndirect {
		t.Fatalf("label with preds in an indirect document: why %b", b.Why)
	}
	if !reflect.DeepEqual(g.Unreachable(), []int{blockAtText(t, g, "movw $1")}) {
		t.Fatalf("unlabeled code after ret stays dead: %v", g.Unreachable())
	}
	// EntryIndirect alone does not start a function.
	for _, f := range g.Funcs {
		if f.Name == "b" {
			t.Fatal("b must not start a function")
		}
	}
	// Far indirect jump too.
	g = build16(t, "ljmp *target\nx: ret\n")
	if g.Blocks[0].Exit != ExitIndirect || !g.HasIndirect {
		t.Fatalf("ljmp *: %s", dump(g))
	}
}

func TestHaltAndTrap(t *testing.T) {
	g := build16(t, `
	cli
1:	hlt
	jmp 1b
	movw $1, %ax
	hlt
	movw $2, %ax
	ud2
	movw $3, %ax
`)
	h := blockAtText(t, g, "1:\thlt")
	j := blockAtText(t, g, "jmp 1b")
	if g.Blocks[h].Exit != ExitHalt || !sameSet(g.Blocks[h].Succ, []int{j}) || !sameSet(g.Blocks[j].Succ, []int{h}) {
		t.Fatalf("halt loop: %s", dump(g))
	}
	dead := blockAtText(t, g, "movw $1")
	plain := blockAtText(t, g, "movw $2")
	if !contains(g.Blocks[dead].Succ, plain) {
		t.Fatalf("plain hlt falls through: %s", dump(g))
	}
	ud := blockAtText(t, g, "ud2")
	if g.Blocks[ud].Exit != ExitTrap || len(g.Blocks[ud].Succ) != 0 {
		t.Fatalf("ud2: %s", dump(g))
	}
	want := []int{dead, blockAtText(t, g, "movw $3")}
	if !reflect.DeepEqual(g.Unreachable(), want) {
		t.Fatalf("unreachable %v want %v: %s", g.Unreachable(), want, dump(g))
	}
}

func TestFallIntoData(t *testing.T) {
	g := build16(t, `
	.globl f
f:	call fatal
msg:	.asciz "x"
	.balign 4
tbl:	.long f
code2:	movw $1, %ax
	.byte 0xea
	.word 0, 0
	movw $2, %ax
	ret
`)
	c := &g.Blocks[blockAtText(t, g, "call fatal")]
	if c.IntoData != at(t, g.Prog, "msg:", 0) {
		t.Fatalf("IntoData %d: %s", c.IntoData, dump(g))
	}
	if g.BlockOf[at(t, g.Prog, "msg:", 0)] != -1 || g.BlockOf[at(t, g.Prog, "tbl:", 0)] != -1 {
		t.Fatal("data labels are not code")
	}
	mv := blockAtText(t, g, "movw $1")
	if g.Blocks[mv].IntoData != at(t, g.Prog, ".byte", 0) {
		t.Fatalf("hand-encoded bytes: %s", dump(g))
	}
	after := blockAtText(t, g, "movw $2")
	if !contains(g.Blocks[mv].Succ, after) || g.Fall(mv) != after {
		t.Fatalf("the chain continues past data: %s", dump(g))
	}
	if len(g.Unreachable()) != 0 {
		t.Fatalf("nothing is dead: %v", g.Unreachable())
	}
	// The call's fallthrough reaches code2 through the string.
	if g.Fall(blockAtText(t, g, "call fatal")) != blockAtText(t, g, "code2:") {
		t.Fatalf("call falls through data to code2: %s", dump(g))
	}
	if g.Blocks[blockAtText(t, g, "code2:")].Why&EntryAddrTaken != 0 {
		t.Fatal("code2 is not address-taken")
	}
	if g.Fall(-1) != -1 || g.Fall(len(g.Blocks)) != -1 {
		t.Fatal("Fall out of range")
	}
}

func TestSections(t *testing.T) {
	g := build16(t, `
	.text
a:	movw $1, %ax
	.section .data
v:	.long 0
w:	movw $9, %bx
	.previous
	movw $2, %ax
	.section .text.other
	movw $3, %ax
	ret
`)
	a := blockAtText(t, g, "movw $1")
	b := blockAtText(t, g, "movw $2")
	if a == b || !contains(g.Blocks[a].Succ, b) {
		t.Fatalf(".previous continues the text fallthrough: %s", dump(g))
	}
	if g.Blocks[a].IntoData != -1 {
		t.Fatal("data in another section is not fallen into")
	}
	o := &g.Blocks[blockAtText(t, g, "movw $3")]
	if o.Why&EntryFirst == 0 || len(o.Pred) != 0 {
		t.Fatalf("first block of a section: %s", dump(g))
	}
	if contains(g.Blocks[b].Succ, o.ID) {
		t.Fatal("no fallthrough across sections")
	}
	wb := blockAtText(t, g, "movw $9")
	if g.Blocks[wb].Why&EntryFirst == 0 {
		t.Fatal("code in .data starts its section")
	}
}

func TestMacroBodyExcluded(t *testing.T) {
	g := build16(t, `
.macro M x
1:	movw $\x, %ax
	jmp 1b
.endm
	movw $1, %bx
	M 3
	ret
`)
	for i, st := range g.Prog.Stmts {
		if st.InMacroDef && g.BlockOf[i] != -1 {
			t.Fatalf("stmt %d in macro body", i)
		}
	}
	if len(g.Blocks) != 1 || g.Blocks[0].Exit != ExitRet {
		t.Fatalf("one block, the macro call inline: %s", dump(g))
	}
}

func TestConditionalAssembly(t *testing.T) {
	g := build16(t, `
f:	movw $1, %ax
	.if X
	pushw %ax
	.else
	pushw %bx
	pushw %cx
	.endif
	nop
#ifdef Y
	movw $2, %ax
#endif
	ret
`)
	pre := blockAtText(t, g, "movw $1")
	a1 := blockAtText(t, g, "pushw %ax")
	a2 := blockAtText(t, g, "pushw %bx")
	n := blockAtText(t, g, "nop")
	if !sameSet(g.Blocks[pre].Succ, []int{a1, a2}) || !sameSet(g.Blocks[n].Pred, []int{a1, a2}) {
		t.Fatalf(".if arms: %s", dump(g))
	}
	y := blockAtText(t, g, "movw $2")
	r := blockAtText(t, g, "ret")
	if !sameSet(g.Blocks[n].Succ, []int{y, r}) || !sameSet(g.Blocks[r].Pred, []int{y, n}) {
		t.Fatalf("#ifdef without #else may be skipped: %s", dump(g))
	}

	g = build16(t, `
f:	nop
	.rept 4
	pushw %ax
	.endr
	ret
	.endr
	.endif
`)
	body := blockAtText(t, g, "pushw")
	if !contains(g.Blocks[body].Succ, body) {
		t.Fatalf(".rept body repeats: %s", dump(g))
	}
	if !sameSet(g.Blocks[blockAtText(t, g, "ret")].Pred, []int{body, blockAtText(t, g, "nop")}) {
		t.Fatalf(".rept may run zero times: %s", dump(g))
	}
}

func TestFarJump(t *testing.T) {
	g := build16(t, `
	.globl go
go:	ljmp $0x08, $pm
	.code32
pm:	movl $1, %eax
	ljmp $0x10, $(pm + 4)
	jmp $0x18, $1f
1:	ret
`)
	j := &g.Blocks[0]
	pm := blockAtText(t, g, "pm:")
	if j.Exit != ExitFar || j.Target != pm || !j.FarSelKnown || j.FarSel != 8 || !sameSet(j.Succ, []int{pm}) {
		t.Fatalf("far jump: %s", dump(g))
	}
	if g.ModeOf(j.Target) != asm.Mode32 || g.ModeOf(0) != asm.Mode16 || g.ModeOf(-1) != 0 {
		t.Fatal("target mode")
	}
	if g.Blocks[pm].Why&EntryAddrTaken != 0 {
		t.Fatal("a far jump's offset is a branch target, not an address taken")
	}
	expr := &g.Blocks[blockAtText(t, g, "pm + 4")]
	if expr.Exit != ExitFar || expr.Target != -1 || len(expr.Succ) != 0 {
		t.Fatalf("unresolvable far target: %s", dump(g))
	}
	loc := &g.Blocks[blockAtText(t, g, "jmp $0x18")]
	if loc.Target != blockAtText(t, g, "1:") {
		t.Fatalf("far jump to a local label: %s", dump(g))
	}
}

func TestEntryHeuristics(t *testing.T) {
	g := build16(t, `
	.globl  pub
pub:	ret
taken:	ret
leaq:	ret
tab:	ret
orphan:	ret
	movw $taken, %ax
	leaw leaq, %si
	.long tab
	.long 1f
1:	ret
`)
	why := func(l string) EntryReason { return g.Blocks[blockAtText(t, g, l+":")].Why }
	if why("pub")&EntryGlobal == 0 || why("pub")&EntryFirst == 0 {
		t.Fatalf("pub %b", why("pub"))
	}
	for _, l := range []string{"taken", "leaq", "tab"} {
		if why(l)&EntryAddrTaken == 0 {
			t.Fatalf("%s %b", l, why(l))
		}
	}
	if why("orphan") != EntryNoPred {
		t.Fatalf("orphan %b", why("orphan"))
	}
	if why("1")&EntryAddrTaken == 0 {
		t.Fatalf("local label in data %b", why("1"))
	}
	if u := g.Unreachable(); len(u) != 1 || u[0] != blockAtText(t, g, "movw $taken") {
		t.Fatalf("unreachable %v: %s", u, dump(g))
	}
}

func TestLabelRunsAndDirectiveLabels(t *testing.T) {
	g := build16(t, `
	nop
a:
b:
c:	nop
d:	.balign 4
	nop
	ret
`)
	if blockAtText(t, g, "a:") != blockAtText(t, g, "c:") {
		t.Fatalf("a run of labels is one block: %s", dump(g))
	}
	d := blockAtText(t, g, "d:")
	if d == blockAtText(t, g, "c:") || g.Blocks[d].Last != at(t, g.Prog, "ret", 0) {
		t.Fatalf("a label on a directive starts a block: %s", dump(g))
	}
}

func TestExits(t *testing.T) {
	cases := []struct {
		src  string
		want ExitKind
	}{
		{"iret", ExitIret},
		{"lret", ExitRet},
		{"sysretq", ExitIret},
		{"xbegin 1f\n1: nop", ExitCond},
		{"jmp", ExitJump},
		{"jmp *(%eax)", ExitIndirect},
		{"int $0x10", ExitFall},
		{"int3", ExitFall},
	}
	for _, c := range cases {
		g := Build(parse(t, c.src, asm.Mode64))
		checkCFG(t, g)
		if g.Blocks[0].Exit != c.want {
			t.Errorf("%q: exit %d want %d", c.src, g.Blocks[0].Exit, c.want)
		}
	}
}

func TestEmptyAndOddPrograms(t *testing.T) {
	for _, src := range []string{"", "; ;", ".endif\n.endr\n.else\n", "x:", ".if 1\nnop\n", "#if 0\n.rept 2\nnop\n#endif\n"} {
		g := Build(parse(t, src, asm.Mode16))
		checkCFG(t, g)
		r := Analyze(g, Config{})
		if len(r.In) != len(g.Prog.Stmts) || len(r.Out) != len(g.Blocks) {
			t.Fatalf("%q: sizes", src)
		}
	}
}

func TestFuncsOverlap(t *testing.T) {
	g := build16(t, `
	.globl a, b
a:	movw $1, %ax
	jmp common
b:	movw $2, %ax
common:	ret
`)
	if len(g.Funcs) != 2 {
		t.Fatalf("funcs %+v", g.Funcs)
	}
	c := blockAtText(t, g, "common:")
	if !contains(g.Funcs[0].Blocks, c) || !contains(g.Funcs[1].Blocks, c) {
		t.Fatalf("shared tail in both: %+v", g.Funcs)
	}
	if g.FuncOf[c] != 0 {
		t.Fatalf("owner is the lowest start: %d", g.FuncOf[c])
	}
	if g.Funcs[0].Label != at(t, g.Prog, "a:", 0) {
		t.Fatal("Func.Label")
	}
	// An unlabeled first block starts an unnamed function.
	g = build16(t, "nop\nret\n")
	if len(g.Funcs) != 1 || g.Funcs[0].Name != "" || g.Funcs[0].Label != -1 {
		t.Fatalf("unnamed: %+v", g.Funcs)
	}
}
