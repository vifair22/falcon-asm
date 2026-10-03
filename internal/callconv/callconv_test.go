package callconv

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

func TestLookup(t *testing.T) {
	cases := []struct {
		names []string
		kind  Kind
		name  string
		bits  int
	}{
		{[]string{"sysv", "SysV64", "amd64", "x86-64", "x86_64", "sysv_abi"}, SysV, "sysv", 64},
		{[]string{"ms", "ms64", "win64", "microsoft", " MS_ABI "}, MS, "ms", 64},
		{[]string{"cdecl"}, Cdecl, "cdecl", 32},
		{[]string{"stdcall"}, Stdcall, "stdcall", 32},
		{[]string{"fastcall"}, Fastcall, "fastcall", 32},
		{[]string{"regparm", "regparm3", "regparm(3)", "regparm=3", "regparm( 3 )"}, Regparm, "regparm(3)", 32},
		{[]string{"regparm(0)"}, Regparm, "regparm(0)", 32},
		{[]string{"custom"}, Custom, "custom", 0},
		{[]string{"none", ""}, None, "none", 0},
	}
	for _, tc := range cases {
		for _, n := range tc.names {
			c, ok := Lookup(n)
			if !ok || c.Kind != tc.kind || c.Name != tc.name || c.Bits != tc.bits || !Valid(n) {
				t.Errorf("%q: %+v %v", n, c, ok)
			}
		}
	}
	for _, n := range []string{"pascal", "regparm(4)", "regparm(-1)", "regparmx", "regparm("} {
		if _, ok := Lookup(n); ok || Valid(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestConventions(t *testing.T) {
	sysv, _ := Lookup("sysv")
	if !sysv.IsArg(flow.RDI) || sysv.IsArg(flow.RBX) || !sysv.IsReturn(flow.RDX) || sysv.IsReturn(flow.RCX) {
		t.Error("sysv args/returns")
	}
	if !sysv.Preserved.Has(flow.R12) || sysv.Preserved.Has(flow.RSP) || sysv.Clobbered.Has(flow.RSP) ||
		!sysv.Clobbered.Has(flow.Vec(15)) || sysv.RedZone != 128 || sysv.Align != 16 || !sysv.SavesControl {
		t.Errorf("sysv: %+v", sysv)
	}
	ms, _ := Lookup("ms")
	if !ms.Preserved.Has(flow.RSI) || !ms.Preserved.Has(flow.Vec(6)) || ms.Clobbered.Has(flow.Vec(6)) ||
		!ms.Clobbered.Has(flow.Vec(5)) || !ms.Clobbered.Has(flow.Vec(16)) || ms.Shadow != 32 || ms.RedZone != 0 ||
		len(ms.Returns) != 1 {
		t.Errorf("ms: %+v", ms)
	}
	fc, _ := Lookup("fastcall")
	if !fc.CalleePops || !fc.IsArg(flow.RCX) || !fc.IsArg(flow.RDX) || fc.IsArg(flow.RAX) {
		t.Errorf("fastcall: %+v", fc)
	}
	rp, _ := Lookup("regparm(2)")
	if len(rp.Args) != 2 || rp.Args[0] != flow.RAX || rp.Args[1] != flow.RDX || rp.CalleePops {
		t.Errorf("regparm: %+v", rp)
	}
	cd, _ := Lookup("cdecl")
	if cd.Clobbered.Has(flow.RBX) || !cd.Clobbered.Has(flow.RCX) || cd.Clobbered.Has(flow.R8) {
		t.Errorf("cdecl: %+v", cd)
	}
	if !cd.AppliesTo(32) || cd.AppliesTo(16) || !cd.Checked() {
		t.Error("cdecl applies")
	}
	none, _ := Lookup("none")
	var nilConv *Conv
	if none.Checked() || nilConv.Checked() || nilConv.AppliesTo(32) || !none.AppliesTo(16) {
		t.Error("none")
	}
}

func TestRegSet(t *testing.T) {
	var s RegSet
	if !s.Empty() {
		t.Fatal("empty")
	}
	s.Add(flow.RBX)
	s.Add(flow.Vec(31))
	s.Add(flow.NoReg)
	s.Add(flow.NumRegs)
	if !s.Has(flow.RBX) || !s.Has(flow.Vec(31)) || s.Has(flow.NoReg) || s.Has(flow.NumRegs) || s.Empty() {
		t.Fatalf("%v", s)
	}
	if got := s.Regs(); len(got) != 2 || got[0] != flow.RBX {
		t.Fatalf("%v", got)
	}
	s.Del(flow.RBX)
	s.Del(flow.NoReg)
	if s.Has(flow.RBX) {
		t.Fatal("del")
	}
}

func TestCustom(t *testing.T) {
	c, err := NewCustom(CustomSpec{Clobbers: []string{"ax", "%CX"}, Preserves: []string{"bx", "si", "di"},
		Args: []string{"ax", "bx"}, Returns: []string{"ax"}})
	if err != nil || !c.Clobbered.Has(flow.RCX) || !c.Preserved.Has(flow.RDI) || !c.IsArg(flow.RBX) ||
		!c.IsReturn(flow.RAX) || c.Preserved.Has(flow.RDX) || c.Clobbered.Has(flow.RDX) {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = NewCustom(CustomSpec{Clobbers: []string{"ax", "none", ""}, Preserves: []string{"all"}, Returns: []string{"dx"}})
	if err != nil || !c.Preserved.Has(flow.RBP) || c.Preserved.Has(flow.RDX) || c.Preserved.Has(flow.RAX) ||
		!c.Clobbered.Has(flow.RDX) {
		t.Fatalf("all: %+v %v", c, err)
	}
	c, err = NewCustom(CustomSpec{Clobbers: []string{"all"}, Preserves: []string{"bp"}})
	if err != nil || !c.Clobbered.Has(flow.R15) || c.Clobbered.Has(flow.RBP) {
		t.Fatalf("clobbers all: %+v %v", c, err)
	}
	c, err = NewCustom(CustomSpec{Clobbers: []string{"ax", "zz", "cr0"}, Preserves: []string{"ax", "es", "xmm6"}})
	if err == nil || !strings.Contains(err.Error(), "\"zz\"") || !strings.Contains(err.Error(), "\"cr0\"") ||
		!strings.Contains(err.Error(), "both") || !c.Preserved.Has(flow.ES) || !c.Preserved.Has(flow.Vec(6)) {
		t.Fatalf("errors: %+v %v", c, err)
	}
}

func TestParseComment(t *testing.T) {
	if _, ok := ParseComment("# just a comment"); ok {
		t.Error("no marker")
	}
	if _, ok := ParseComment("# falcon-asm: ignore=all"); ok {
		t.Error("foreign items only")
	}
	a, ok := ParseComment("/* falcon-asm: ignore=x CC=Custom clobbers=ax,cx preserves=bx args=si returns=ax trailing words handler */")
	if !ok || a.CC != "custom" || len(a.Spec.Clobbers) != 2 || a.Spec.Args[0] != "si" || !a.HasSpec || a.Handler {
		t.Fatalf("%+v", a)
	}
	c, probs := a.Conv()
	if c == nil || c.Kind != Custom || len(probs) != 0 {
		t.Fatalf("%+v %v", c, probs)
	}
	a, ok = ParseComment("# FALCON-ASM: handler=errcode vector=0x0e")
	if !ok || !a.Handler || a.HandlerKind != "errcode" || a.Vector != 14 {
		t.Fatalf("%+v", a)
	}
	if c, probs := a.Conv(); c != nil || len(probs) != 0 {
		t.Fatalf("handler conv: %v %v", c, probs)
	}
	a, _ = ParseComment("# falcon-asm: handler vector=256 handler=what")
	if len(a.Problems) != 2 || a.Vector != -1 {
		t.Fatalf("%+v", a)
	}
	for text, want := range map[string]string{
		"# falcon-asm: cc=pascal":                "unknown calling convention",
		"# falcon-asm: cc=cdecl args=ax":         "apply only to cc=custom",
		"# falcon-asm: cc=custom":                "declares nothing",
		"# falcon-asm: cc=custom preserves=nope": "unknown register",
	} {
		a, _ := ParseComment(text)
		if _, probs := a.Conv(); len(probs) != 1 || !strings.Contains(probs[0], want) {
			t.Errorf("%s: %v", text, probs)
		}
	}
	// Lists alone imply cc=custom, with no problem to report.
	a, _ = ParseComment("# falcon-asm: args=si returns=ax clobbers=dx")
	if c, probs := a.Conv(); c == nil || c.Kind != Custom || len(probs) != 0 {
		t.Errorf("lists without cc=: %v %v", c, probs)
	}
	a, _ = ParseComment("# falcon-asm: cc=cdecl args=ax")
	if _, probs := a.Conv(); len(probs) != 1 || !strings.Contains(probs[0], "drop cc=cdecl") {
		t.Errorf("cdecl with lists: %v", probs)
	}
	a, _ = ParseComment("# falcon-asm: cc=ms")
	if c, probs := a.Conv(); c == nil || c.Kind != MS || len(probs) != 0 {
		t.Errorf("ms: %v %v", c, probs)
	}
}

func TestErrorCode(t *testing.T) {
	var got []int
	for v := 0; v < 256; v++ {
		if ErrorCode(v) {
			got = append(got, v)
		}
	}
	want := []int{8, 10, 11, 12, 13, 14, 17, 21, 29, 30}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}
