package cc

import (
	"os"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Pinned regression excerpts from airies-boot's falcon-bios (built with
// gcc -m32, so the target convention is cdecl): files that must stay
// clean, and the faults the rules must find when a known-good file is
// broken on purpose.
//
//   - thunk.S: thunk_bios, a cdecl function that saves ebx/esi/edi/ebp,
//     parks esp in memory, drops to real mode, calls the BIOS and comes
//     back up; the restore goes through the parked stack pointer.
//   - idt.S: fault_install loads an IDT built from a stub table, so the
//     stubs are found as a handler; they never return.
//   - noe820.S: an INT 15h filter installed in the vector table through a
//     relocation expression; a service handler returning with iret.
//   - stage2-annotated.S: stage 2 with the custom conventions proposed for
//     its 16-bit helpers (put_hex8, puts, putc, io_delay, ...).

func readPinned(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lintPinned(t *testing.T, text string) ([]lint.Finding, *model) {
	t.Helper()
	rules := []*lint.Rule{}
	for _, code := range allCodes {
		r, _ := lint.Lookup(code)
		rules = append(rules, r)
	}
	probe, ok := lint.Lookup("cc-test-probe")
	if !ok {
		lint.Register(&lint.Rule{Code: "cc-test-probe", Section: 4, Default: lint.Hint, Langs: lint.LangAsm,
			NeedsFlow: true, Doc: lint.Doc{Summary: "test probe"}, Check: func(c *lint.Context) { probeModel = modelOf(c) }})
		probe, _ = lint.Lookup("cc-test-probe")
	}
	rules = append(rules, probe)
	doc := &docstore.Document{URI: "file:///pinned.S", Text: text}
	p := asm.Parse(doc, lexer.Tokenize(text), asm.Config{InitialMode: asm.Mode32})
	got := lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: lint.Target{Mode: asm.Mode32, CC: "cdecl"}, Rules: rules})
	m := probeModel
	probeModel = nil
	return got, m
}

var probeModel *model

func funcNamed(m *model, name string) *function {
	for k := range m.funcs {
		if m.funcs[k].name == name {
			return &m.funcs[k]
		}
	}
	return nil
}

func TestPinnedClean(t *testing.T) {
	for _, name := range []string{"thunk.S", "idt.S", "noe820.S", "stage2-annotated.S"} {
		got, m := lintPinned(t, readPinned(t, name))
		if len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
		if m == nil {
			t.Fatalf("%s: no model", name)
		}
	}
	_, m := lintPinned(t, readPinned(t, "thunk.S"))
	if f := funcNamed(m, "thunk_bios"); f == nil || f.conv == nil || f.conv.Name != "cdecl" || !f.entered {
		t.Errorf("thunk_bios: %+v", f)
	}
	_, m = lintPinned(t, readPinned(t, "idt.S"))
	if f := funcNamed(m, "fault_stubs"); f == nil || f.handler == nil || f.conv != nil {
		t.Errorf("fault_stubs: %+v", f)
	}
	_, m = lintPinned(t, readPinned(t, "noe820.S"))
	if f := funcNamed(m, "filter"); f == nil || f.handler == nil || f.handler.vector != 0x15 || f.handler.preserves() {
		t.Errorf("filter: %+v", f)
	}
	_, m = lintPinned(t, readPinned(t, "stage2-annotated.S"))
	if f := funcNamed(m, "putc"); f == nil || f.conv == nil || f.conv.Name != "custom" || !f.explicit {
		t.Errorf("putc: %+v", f)
	}
	if f := funcNamed(m, "gate_entry"); f == nil || f.conv == nil || f.conv.Name != "cdecl" {
		t.Errorf("gate_entry: %+v", f)
	}
}

func TestPinnedBroken(t *testing.T) {
	cases := []struct {
		file, old, new, want string
	}{
		// thunk_bios forgets to restore ebx on the way out.
		{"thunk.S", "\tpopl    %esi\n\tpopl    %ebx\n\tret", "\tpopl    %esi\n\tpopl    %eax\n\tret", "cc-callee-saved"},
		// io_delay declared as preserving everything, but its loop
		// counts in ecx.
		{"stage2-annotated.S", "cc=custom clobbers=cx preserves=all", "cc=custom preserves=all", "cc-callee-saved"},
		// The INT 15h filter returning with a near ret.
		{"noe820.S", "\tmovb    $0x86, %ah\n\tiret", "\tmovb    $0x86, %ah\n\tret", "handler-ret"},
		// The stub table's handler returning.
		{"idt.S", "\tjmp     fault_common\n", "\tret\n", "handler-ret"},
	}
	for _, tc := range cases {
		text := readPinned(t, tc.file)
		if !strings.Contains(text, tc.old) {
			t.Fatalf("%s: pinned text changed: %q", tc.file, tc.old)
		}
		got, _ := lintPinned(t, strings.Replace(text, tc.old, tc.new, 1))
		found := false
		for _, f := range got {
			if f.Code == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s, got %v", tc.file, tc.want, got)
		}
	}
}
