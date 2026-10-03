package engine

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// expTC is the scripted toolchain plus the cpp expansion capability.
type expTC struct {
	fakeTC
	mu    sync.Mutex
	calls int
	out   string
	err   error
}

func (x *expTC) ExpandLines(_ context.Context, _ string, _ toolchain.Flags, _ string, _, _ int) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.calls++
	return x.out, x.err
}

func hoverText(t *testing.T, h *harness, d *docstore.Document, line, char int) string {
	t.Helper()
	hv, err := h.e.Hover(d, protocol.Position{Line: line, Character: char})
	if err != nil {
		t.Fatal(err)
	}
	if hv == nil {
		return ""
	}
	return hv.Contents.Value
}

func TestHoverGasMacroExpansion(t *testing.T) {
	h := newHarness(t, setup{})
	h.file("macros.inc", "\t.macro inc_mac r\n\tpushl %\\r\n\t.endm\n")
	text := "\t.include \"macros.inc\"\n\t.macro save a, b=ebx\n\tpushl %\\a\n\tpushl %\\b\nl\\@:\n\t.endm\n" +
		"\tsave eax\n\tsave ecx, edx\n\tinc_mac esi\n\tnop\n"
	_, uri := h.file("t.S", text)
	d := h.open(uri, "falcon-asm", 1, text)
	got := hoverText(t, h, d, 6, 2)
	if !strings.Contains(got, "Expands to:\n```asm\n\tpushl %eax\n\tpushl %ebx\nl0:\n```") || !strings.Contains(got, "save") {
		t.Fatalf("hover = %q", got)
	}
	if got := hoverText(t, h, d, 7, 2); !strings.Contains(got, "pushl %edx\nl1:") {
		t.Fatalf("second invocation = %q", got)
	}
	if got := hoverText(t, h, d, 8, 2); !strings.Contains(got, "\tpushl %esi\n```") {
		t.Fatalf(".include'd macro = %q", got)
	}
	if got := hoverText(t, h, d, 9, 2); strings.Contains(got, "Expands") {
		t.Fatalf("instruction hover expanded: %q", got)
	}
}

func TestHoverCppExpansion(t *testing.T) {
	tc := &expTC{out: "nop; nop"}
	h := newHarness(t, setup{cfg: Config{Toolchain: tc}})
	text := "#define TWICE(x) x; x\n\tTWICE(nop)\n\tUNKNOWN 1\n"
	_, uri := h.file("c.S", text)
	d := h.open(uri, "falcon-asm", 1, text)
	got := hoverText(t, h, d, 1, 2)
	if !strings.Contains(got, "#define TWICE(x) x; x") || !strings.Contains(got, "```asm\nnop; nop\n```") {
		t.Fatalf("hover = %q", got)
	}
	hoverText(t, h, d, 1, 3)
	if tc.calls != 1 {
		t.Fatalf("preprocessor ran %d times; the result is cached per version", tc.calls)
	}
	if got := hoverText(t, h, d, 2, 2); strings.Contains(got, "Expands") {
		t.Fatalf("an unknown word was expanded: %q", got)
	}
	// A failed run is cached as no expansion.
	tc.out = ""
	d2 := h.change(uri, 2, text+"\n")
	if got := hoverText(t, h, d2, 1, 2); strings.Contains(got, "Expands") {
		t.Fatalf("failed run shown: %q", got)
	}
}

func TestHoverCppExpansionRealCC(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc not on PATH")
	}
	h := newHarness(t, setup{cfg: Config{Toolchain: &toolchain.Runner{}}})
	h.file("defs.h", "#define SEL 0x10\n")
	text := "#include \"defs.h\"\n#define LOAD(r) movw $SEL, %r\n\tLOAD(ax)\n"
	_, uri := h.file("r.S", text)
	d := h.open(uri, "falcon-asm", 1, text)
	if got := hoverText(t, h, d, 2, 2); !strings.Contains(got, "movw $0x10, %ax") {
		t.Fatalf("hover = %q", got)
	}
}

func TestLintWorkspaceAndExpansions(t *testing.T) {
	h := newHarness(t, setup{})
	text := "\t.code64\n\t.macro m\n\tpusha\n\t.endm\n\tm\n\tcall missing_fn\n"
	_, uri := h.file("w.S", text)
	h.open(uri, "falcon-asm", 1, text)
	h.waitPub(uri, func(p protocol.PublishDiagnosticsParams) bool {
		return hasMsg("in expansion of m")(p) && hasMsg("undefined symbol missing_fn")(p)
	})
}
