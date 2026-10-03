package flow

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
)

func TestRegOf(t *testing.T) {
	cases := []struct {
		name   string
		r      Reg
		lo, hi int
	}{
		{"rax", RAX, 0, 63}, {"eax", RAX, 0, 31}, {"ax", RAX, 0, 15}, {"al", RAX, 0, 7},
		{"ah", RAX, 8, 15}, {"%dx", RDX, 0, 15}, {"BH", RBX, 8, 15}, {"spl", RSP, 0, 7},
		{"sil", RSI, 0, 7}, {"r8", R8, 0, 63}, {"r9d", R9, 0, 31}, {"r10w", R10, 0, 15},
		{"r11b", R11, 0, 7}, {"r15l", R15, 0, 7}, {"es", ES, 0, 15}, {"gs", GS, 0, 15},
		{"cr0", CR0, 0, 63}, {"cr8", CR8, 0, 63}, {"k3", K3, 0, 63},
		{"xmm5", Vec(5), 0, 127}, {"ymm31", Vec(31), 0, 255}, {"zmm0", Vec(0), 0, 511},
	}
	for _, c := range cases {
		r, lo, hi, ok := RegOf(c.name)
		if !ok || r != c.r || lo != c.lo || hi != c.hi {
			t.Errorf("RegOf(%q) = %v %d %d %v", c.name, r, lo, hi, ok)
		}
	}
	for _, n := range []string{"dr7", "st(0)", "mm0", "rip", "eflags", "cr1", ""} {
		if _, _, _, ok := RegOf(n); ok {
			t.Errorf("RegOf(%q) resolved", n)
		}
	}
}

func TestRegNames(t *testing.T) {
	if RAX.String() != "rax" || ES.String() != "es" || CR4.String() != "cr4" || K7.String() != "k7" ||
		Vec(12).String() != "xmm12" || Reg(-5).String() != "reg(-5)" {
		t.Fatal("String")
	}
	names := map[string]string{
		RAX.Name(64): "rax", RAX.Name(32): "eax", RAX.Name(16): "ax", RAX.Name(8): "al",
		R12.Name(8): "r12b", Vec(3).Name(256): "ymm3", Vec(3).Name(128): "xmm3",
		Vec(3).Name(512): "zmm3", DS.Name(16): "ds", CR3.Name(32): "cr3", K1.Name(16): "k1",
	}
	for got, want := range names {
		if got != want {
			t.Errorf("Name: %q want %q", got, want)
		}
	}
	if RAX.Name(12) != "" || Vec(1).Name(64) != "" || NoReg.Name(64) != "" {
		t.Fatal("no such width")
	}
	preds := []struct {
		r                                Reg
		gpr, seg, ctl, mask, vec, hasVal bool
	}{
		{RSP, true, false, false, false, false, true},
		{FS, false, true, false, false, false, true},
		{CR2, false, false, true, false, false, true},
		{K0, false, false, false, true, false, false},
		{Vec(31), false, false, false, false, true, false},
		{NoReg, false, false, false, false, false, false},
	}
	for _, p := range preds {
		if p.r.IsGPR() != p.gpr || p.r.IsSegment() != p.seg || p.r.IsControl() != p.ctl ||
			p.r.IsMask() != p.mask || p.r.IsVector() != p.vec || p.r.HasValue() != p.hasVal {
			t.Errorf("predicates of %v", p.r)
		}
	}
	if NumRegs != Vec(31)+1 {
		t.Fatal("NumRegs")
	}
}

func TestValueOps(t *testing.T) {
	unk := Value{}
	if v := valShl(konst(1, 8), 9, 8); !v.Known(0xFF) || v.V != 0 {
		t.Fatal("shl past width")
	}
	if v := valShr(konst(0x80, 8), 8, 8); !v.Known(0xFF) || v.V != 0 {
		t.Fatal("shr past width")
	}
	if v := valShr(unk, 4, 8); v.Mask != 0xF0 {
		t.Fatalf("shr shifts in zeros: %#x", v.Mask)
	}
	if v := valSar(konst(0x80, 8), 20, 8); v.V != 0xFF {
		t.Fatalf("sar clamps: %#x", v.V)
	}
	if v := valSar(unk, 2, 8); v.Mask != 0 {
		t.Fatalf("sar of unknown sign: %#x", v.Mask)
	}
	if v := valSar(Value{V: 0, Mask: 0x80}, 2, 8); v.Mask != 0xE0 || v.V != 0 {
		t.Fatalf("sar of a known-positive: %#x/%#x", v.V, v.Mask)
	}
	if v := valRol(konst(0x81, 8), 0, 8); v.V != 0x81 {
		t.Fatal("rol 0")
	}
	if v := valRor(konst(0x01, 8), 1, 8); v.V != 0x80 {
		t.Fatal("ror")
	}
	if widthMask(0) != 0 || widthMask(64) != ^uint64(0) {
		t.Fatal("widthMask")
	}
	if v := extend(Value{V: 0, Mask: 0x7F}, 8, 16, true); v.Mask != 0x7F {
		t.Fatal("sign unknown: upper unknown")
	}
	if v := valAnd(unk, konst(0x0F, 8), 8); v.Mask != 0xF0 || v.V != 0 {
		t.Fatalf("and with zeros knows them: %#x", v.Mask)
	}
	if v := valOr(unk, konst(0x0F, 8), 8); v.Mask != 0x0F || v.V != 0x0F {
		t.Fatal("or with ones knows them")
	}
	if signExt(0xFF, 8) != -1 || signExt(0x7F, 8) != 127 {
		t.Fatal("signExt")
	}
	if sizeOf('x') != 0 || suffixBits(&asm.Stmt{}) != 0 {
		t.Fatal("sizes")
	}
}

func TestModeAndPrefixSizes(t *testing.T) {
	_, r := run(t, `
f:	data32 pushf
	nop
	pushw $1
	nop
	pushl $sym
	nop
	pushw (%bx)
	popw (%bx)
	nop
	addr32 lodsb
	nop
`, asm.Mode16, Config{})
	if s := stateAt(t, r, "nop"); s.Stack != 4 {
		t.Fatalf("data32 pushf: %d", s.Stack)
	}
	if s := r.Before(at(t, r.CFG.Prog, "nop", 1)); s.Stack != 6 {
		t.Fatalf("pushw $1: %d", s.Stack)
	}
	s := r.Before(at(t, r.CFG.Prog, "nop", 2))
	if top, _ := s.Top(); s.Stack != 10 || top.Val.Mask != 0 || top.Src != NoReg {
		t.Fatalf("pushl $sym: %d %+v", s.Stack, top)
	}
	if s := r.Before(at(t, r.CFG.Prog, "nop", 3)); s.Stack != 10 {
		t.Fatalf("push/pop memory: %d", s.Stack)
	}
	_, r = run(t, `
f:	data16 pushf
	nop
	addr16 lodsb
	pushq $1
	nop
`, asm.Mode32, Config{})
	if s := stateAt(t, r, "nop"); s.Stack != 2 {
		t.Fatalf("data16 pushf: %d", s.Stack)
	}
	_, r = run(t, `
f:	pushfq
	data16 pushf
	movl $5, %esi
	cld
	addr32 lodsb
	nop
	lodsq
	nop
	pushq %fs
	nop
`, asm.Mode64, Config{})
	if s := stateAt(t, r, "movl"); s.Stack != 10 {
		t.Fatalf("64-bit pushes: %d", s.Stack)
	}
	wantKnown(t, r, "nop", "rsi", 6)
	if v, ok := r.KnownAt(at(t, r.CFG.Prog, "nop", 1), "rsi"); !ok || v != 14 {
		t.Fatalf("lodsq steps 8: %d", v)
	}
	if s := r.Before(at(t, r.CFG.Prog, "nop", 2)); s.Stack != 18 {
		t.Fatalf("push %%fs in 64-bit: %d", s.Stack)
	}
}

func TestStackSaveSlot(t *testing.T) {
	_, r := run(t, `
f:	pushl %ebx
	movl %esp, save
	movl $0, save
	movl $0x1000, %esp
	movl save, %esp
	nop
	ret
g:	pushl %ebx
	movl %esp, save
	movl $0x1000, %esp
	movl save, %esp
	popl %ebx
	ret
`, asm.Mode32, Config{})
	if s := stateAt(t, r, "nop"); s.StackKnown {
		t.Fatal("a store over the slot forgets it")
	}
	s := stateAt(t, r, "popl")
	if !s.StackKnown || s.Stack != 4 {
		t.Fatalf("slot restore: %v %d", s.StackKnown, s.Stack)
	}
	if s.Get(RBX).Def != DefEntry {
		t.Fatal("the pushed ebx survives the trip")
	}
}

func TestLeaForms(t *testing.T) {
	_, r := run(t, `
	movq $0x10, %rax
	leaq sym(%rip), %rbx
	leaq %fs:8, %rcx
	leaq unknown(%rax), %rdx
	leaq 4(%rax,%rax,4), %rsi
	leaq (,%rax,8), %rdi
	leaq 2(%rax,%rax,2), %r8
	nop
`, asm.Mode64, Config{})
	n := at(t, r.CFG.Prog, "nop", 0)
	for _, reg := range []string{"rbx", "rcx", "rdx"} {
		if _, ok := r.KnownAt(n, reg); ok {
			t.Fatalf("%s unknown", reg)
		}
	}
	wantKnown(t, r, "nop", "rsi", 0x10+0x40+4)
	wantKnown(t, r, "nop", "rdi", 0x80)
	wantKnown(t, r, "nop", "r8", 0x10+0x20+2)
}

func TestShiftForms(t *testing.T) {
	_, r := run(t, `
	movb $0x81, %al
	salb %al
	nop
	movb $5, %cl
	rolb %cl, %al
	nop
	shlq $63, %rbx
	nop
	shlb %dl, %al
	nop
`, asm.Mode64, Config{})
	wantKnown(t, r, "nop", "al", 0x02)
	if v, _ := r.KnownAt(at(t, r.CFG.Prog, "nop", 1), "al"); v != 0x40 {
		t.Fatalf("rol: %#x", v)
	}
	if _, m, _ := r.ValueAt(at(t, r.CFG.Prog, "nop", 2), "rbx"); m != 1<<63-1 {
		t.Fatalf("shl 63 knows 63 zero bits: %#x", m)
	}
	if _, ok := r.KnownAt(at(t, r.CFG.Prog, "nop", 3), "al"); ok {
		t.Fatal("unknown count")
	}
}

func TestStateHelpers(t *testing.T) {
	var s State
	s.StackKnown = true
	s.push(4, konst(1, 32), RAX, false)
	s.setDepth(8)
	if s.Stack != 8 || len(s.Slots()) != 1 {
		t.Fatal("setDepth growing keeps slots")
	}
	s.setDepth(0)
	if len(s.Slots()) != 0 {
		t.Fatal("setDepth shrinking drops slots")
	}
	s.Stack = 1 << 15
	s.push(4, konst(1, 32), RAX, false)
	if len(s.Slots()) != 0 {
		t.Fatal("deep pushes are not recorded")
	}
	for k := 0; k < 12; k++ {
		s.setDepth(4 * k)
		s.push(4, konst(uint64(k), 32), RAX, false)
	}
	if len(s.Slots()) != maxSlots || s.Slots()[0].Val.V != 2 {
		t.Fatalf("oldest slots fall off: %+v", s.Slots()[0])
	}
	s.unknownSP()
	if len(s.Slots()) != 0 || s.StackKnown {
		t.Fatal("unknownSP without a frame or parked pointer")
	}
	if g := s.Get(Vec(1)); g.Def != DefEntry || g.Mask != 0 {
		t.Fatal("Get of a vector")
	}
	if triOf(true) != Set || triOf(false) != Clear {
		t.Fatal("triOf")
	}

	// Join: unreachable sources are ignored, an unreachable destination
	// takes the source, widening collapses changed values.
	a := State{Reachable: true, StackKnown: true}
	a.set(RAX, konst(0x10, 64))
	b := a
	b.set(RAX, konst(0x11, 64))
	if a.join(&State{}, false) {
		t.Fatal("join with unreachable")
	}
	var c State
	if !c.join(&a, false) || c.get(RAX) != a.get(RAX) {
		t.Fatal("join into unreachable")
	}
	w := a
	if !w.join(&b, true) || w.get(RAX).Mask != 0 {
		t.Fatal("widening")
	}
	a.FrameKnown, a.FrameDepth, a.spSave, a.spDepth = true, 4, 7, 4
	b.FrameKnown, b.spSave = false, 0
	a.DF, b.DF = Set, Clear
	a.IF, b.IF = Clear, Set
	a.setUpper(Vec(0), Set)
	if !a.join(&b, false) || a.FrameKnown || a.spSave != 0 || a.DF != Unknown || a.IF != Unknown ||
		a.UpperDirty(Vec(0)) != Unknown {
		t.Fatal("join of frame, slot, flags, vector")
	}
	a.StackKnown, b.StackKnown = true, true
	a.Stack, b.Stack = 4, 4
	a.push(4, konst(1, 32), RAX, false)
	b.push(4, konst(2, 32), RBX, false)
	if !a.join(&b, false) {
		t.Fatal("slot join")
	}
	if top, _ := a.Top(); top.Src != NoReg || top.Val.Mask == ^uint64(0)&0xFFFFFFFF {
		t.Fatalf("slot join: %+v", top)
	}
	d := State{Reachable: true, StackKnown: true}
	e := d
	d.push(4, konst(1, 32), RAX, true)
	e.push(4, konst(1, 32), RAX, false)
	if !d.join(&e, false) || len(d.Slots()) != 0 {
		t.Fatal("pushf slot does not match a push slot")
	}
}

func TestDefsAtJoin(t *testing.T) {
	_, r := run(t, `
f:	je 1f
	movl $1, %ebx
	jmp 2f
1:	movl $1, %ebx
2:	nop
`, asm.Mode32, Config{})
	s := stateAt(t, r, "nop")
	if v := s.Get(RBX); v.Mask != 0xFFFFFFFF || v.Def != DefMerged {
		t.Fatalf("same value, different writers: %+v", v)
	}
}
