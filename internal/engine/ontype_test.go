package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// tempFiles lists the on-type copies left in dir.
func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), TempPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func lensTitles(ls []protocol.CodeLens) string {
	var s []string
	for _, l := range ls {
		s = append(s, itoa(l.Range.Start.Line)+":"+l.Command.Title)
	}
	return strings.Join(s, "|")
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// With real gcc: the unsaved buffer is assembled from beside the source, so
// a quoted include next to it resolves even though the compiler runs from
// elsewhere; its diagnostics and listing apply to the version typed; and
// no copy is left behind.
func TestOnTypeRealToolchain(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not installed")
	}
	h0 := &harness{t: t, dir: t.TempDir()}
	src := filepath.Join(h0.dir, "src")
	path, uri := h0.file("src/boot.S", "\t.section .text.s1, \"ax\"\n_start:\n\tnop\n")
	h0.file("src/defs.h", "#define VAL 7\n")
	db := fakeDB{path: {Compiler: "gcc", Dir: h0.dir, File: path, Exact: true}}
	h := newHarness(t, setup{
		cfg:  Config{Toolchain: &toolchain.Runner{}, OnTypeDelay: 20 * time.Millisecond},
		opts: `{"toolchainOnType":true}`, root: h0.dir, db: db,
	})
	h.dir = h0.dir
	h.open(uri, "falcon-asm", 1, "\t.section .text.s1, \"ax\"\n_start:\n\tnop\n")
	h.waitPub(uri, version(1))

	bad := "#include \"defs.h\"\n\t.section .text.s1, \"ax\"\n_start:\n\tbogusinsn %eax\n"
	h.change(uri, 2, bad)
	p := h.waitPub(uri, func(p protocol.PublishDiagnosticsParams) bool {
		return version(2)(p) && hasMsg("bogusinsn")(p)
	})
	if d := p.Diagnostics[len(p.Diagnostics)-1]; d.Range.Start.Line != 3 {
		t.Fatalf("gas diagnostic = %+v", d)
	}
	good := "#include \"defs.h\"\n\t.section .text.s1, \"ax\"\n_start:\n\t.fill VAL, 1, 0x90\n\t. = _start + 16\n"
	d3 := h.change(uri, 3, good)
	deadline := time.Now().Add(5 * time.Second)
	for lensTitles(h.e.CodeLens(d3)) != "2:s1: 7 / 16 bytes (9 free)" {
		if time.Now().After(deadline) {
			t.Fatalf("lens = %q; log: %s", lensTitles(h.e.CodeLens(d3)), h.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if left := tempFiles(t, src); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	// The file on disk was never touched.
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "nop") {
		t.Fatalf("source changed: %q", data)
	}
}

// gatedOnType opens a document whose on-type run blocks inside the
// toolchain, so the tests can act while the copy exists.
func gatedOnType(t *testing.T) (*harness, string, string, chan string) {
	t.Helper()
	h0 := &harness{t: t, dir: t.TempDir()}
	path, uri := h0.file("a.S", "\tnop\n")
	db := fakeDB{path: {Compiler: "cc", Dir: h0.dir, File: path}}
	h := newHarness(t, setup{cfg: Config{OnTypeDelay: time.Millisecond}, opts: `{"toolchainOnType":true}`, root: h0.dir, db: db})
	h.dir = h0.dir
	files := make(chan string, 8)
	h.tc.asm = []asmResult{{}, {gate: make(chan struct{})}}
	h.tc.started = make(chan string, 16)
	h.open(uri, "falcon-asm", 1, "\tnop\n")
	h.waitPub(uri, version(1))
	h.change(uri, 2, "\tnop\n\tnop\n")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-h.tc.started:
		case <-deadline:
			t.Fatal("on-type run never started")
		}
		calls := h.tc.Calls()
		if last := calls[len(calls)-1]; strings.HasPrefix(last, "assemble "+TempPrefix) {
			files <- strings.TrimPrefix(last, "assemble ")
			break
		}
	}
	return h, path, uri, files
}

func TestOnTypeCleanupOnShutdown(t *testing.T) {
	h, path, _, files := gatedOnType(t)
	name := <-files
	if !strings.HasSuffix(name, "-a.S") {
		t.Fatalf("copy name = %q", name)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), name))
	if err != nil || string(data) != "\tnop\n\tnop\n" {
		t.Fatalf("copy = %q, %v", data, err)
	}
	h.e.Shutdown()
	if left := tempFiles(t, filepath.Dir(path)); len(left) != 0 {
		t.Fatalf("temp files left after shutdown: %v", left)
	}
}

func TestOnTypeCancelledBySaveAndClose(t *testing.T) {
	h, path, uri, _ := gatedOnType(t)
	h.e.Save(uri) // cancels the gated run; the save's own run follows
	deadline := time.Now().Add(5 * time.Second)
	for len(tempFiles(t, filepath.Dir(path))) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("copy not removed after save")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A change then close before the delay: nothing runs.
	h.e.cfg.OnTypeDelay = time.Hour
	h.change(uri, 3, "\tnop\n")
	h.e.Close(uri)
	h.e.mu.Lock()
	n := len(h.e.onType.runs)
	h.e.mu.Unlock()
	if n != 0 {
		t.Fatalf("runs left: %d", n)
	}
}

func TestOnTypeOffByDefault(t *testing.T) {
	h0 := &harness{t: t, dir: t.TempDir()}
	path, uri := h0.file("a.S", "\tnop\n")
	h := newHarness(t, setup{cfg: Config{OnTypeDelay: time.Millisecond}, root: h0.dir, db: fakeDB{path: {Compiler: "cc"}}})
	h.open(uri, "falcon-asm", 1, "\tnop\n")
	h.waitPub(uri, version(1))
	h.change(uri, 2, "\tnop\n\tnop\n")
	h.waitPub(uri, version(2))
	time.Sleep(30 * time.Millisecond)
	for _, c := range h.tc.Calls() {
		if strings.Contains(c, TempPrefix) {
			t.Fatalf("on-type run while off: %v", h.tc.Calls())
		}
	}
	if h.e.onTypeDelay() != time.Millisecond || (&Engine{}).onTypeDelay() != DefaultOnTypeDelay {
		t.Fatal("delay")
	}
}

func TestOnTypeHelpers(t *testing.T) {
	dir := t.TempDir()
	// A read-only directory: the copy cannot be written, which is logged
	// once and is otherwise a skipped run.
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := writeTemp(filepath.Join(ro, "x"), "a"); err == nil && os.Geteuid() != 0 {
		t.Fatal("write into read-only dir succeeded")
	}
	p := filepath.Join(dir, "y")
	if err := writeTemp(p, "a"); err != nil {
		t.Fatal(err)
	}
	if err := writeTemp(p, "b"); err == nil {
		t.Fatal("exclusive create overwrote")
	}
	if a, b := tempPath("/s/boot.S"), tempPath("/s/boot.S"); a == b || filepath.Dir(a) != "/s" || !strings.HasSuffix(a, "-boot.S") {
		t.Fatalf("temp paths %q %q", a, b)
	}
	res := &toolchain.AsmResult{Diagnostics: map[string][]protocol.Diagnostic{"/s/.t": {{Message: "x"}}, "/s/h.h": {{Message: "y"}}}}
	remapTemp(res, "/s/.t", "/s/boot.S")
	if len(res.Diagnostics["/s/boot.S"]) != 1 || res.Diagnostics["/s/.t"] != nil {
		t.Fatalf("remap = %v", res.Diagnostics)
	}
	remapTemp(nil, "", "")
	if !errors.Is(firstErr(nil, os.ErrClosed), os.ErrClosed) || firstErr() != nil {
		t.Fatal("firstErr")
	}
}

// A run whose copy cannot be written logs and leaves state alone.
func TestOnTypeUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	h0 := &harness{t: t, dir: t.TempDir()}
	path, uri := h0.file("ro/a.S", "\tnop\n")
	if err := os.Chmod(filepath.Dir(path), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
	h := newHarness(t, setup{cfg: Config{OnTypeDelay: time.Millisecond}, opts: `{"toolchainOnType":true}`, root: h0.dir, db: fakeDB{path: {Compiler: "cc"}}})
	h.open(uri, "falcon-asm", 1, "\tnop\n")
	h.waitPub(uri, version(1))
	h.change(uri, 2, "\tnop\n\tnop\n")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.log.String(), "on-type assembly off") {
		if time.Now().After(deadline) {
			t.Fatalf("log = %s", h.log)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEngineNavigationFeatures(t *testing.T) {
	h := newHarness(t, setup{})
	_, uri := h.file("m.S", "")
	src := "\t.globl entry\nentry:\n\tcall helper\nhelper:\n\tret\n"
	d := h.open(uri, "falcon-asm", 1, src)
	if syms := h.e.WorkspaceSymbols("help"); len(syms) != 1 || syms[0].Name != "helper" {
		t.Fatalf("workspace symbols = %+v", syms)
	}
	items := h.e.PrepareCallHierarchy(d, protocol.Position{Line: 2, Character: 7})
	if len(items) != 1 || items[0].Name != "helper" {
		t.Fatalf("prepare = %+v", items)
	}
	if in := h.e.IncomingCalls(items[0]); len(in) != 1 || in[0].From.Name != "entry" {
		t.Fatalf("incoming = %+v", in)
	}
	entry := h.e.PrepareCallHierarchy(d, protocol.Position{Line: 1, Character: 1})
	if out := h.e.OutgoingCalls(entry[0]); len(out) != 1 || out[0].To.Name != "helper" {
		t.Fatalf("outgoing = %+v", out)
	}
	_, curi := h.file("m.c", "")
	c := h.open(curi, "c", 1, "int main(void) { return 0; }\n")
	if h.e.PrepareCallHierarchy(c, protocol.Position{}) != nil || h.e.CodeLens(c) != nil {
		t.Fatal("C documents get no call hierarchy or lenses")
	}
	if h.e.CodeLens(d) != nil {
		t.Fatal("no listing, no lenses")
	}
}

// Diagnostics a run attributes to another file reach that file; a failing
// run is logged and changes nothing.
func TestOnTypeIncludesAndErrors(t *testing.T) {
	h0 := &harness{t: t, dir: t.TempDir()}
	path, uri := h0.file("a.S", "\tnop\n")
	hdr := filepath.Join(h0.dir, "inc.h")
	h := newHarness(t, setup{cfg: Config{OnTypeDelay: time.Millisecond}, opts: `{"toolchainOnType":true}`, root: h0.dir, db: fakeDB{path: {Compiler: "cc"}}})
	h.tc.asm = []asmResult{{}, {r: &toolchain.AsmResult{Diagnostics: map[string][]protocol.Diagnostic{
		hdr: {{Range: lineRange(0), Severity: protocol.SeverityError, Message: "header trouble"}},
	}}}, {err: os.ErrPermission}}
	h.open(uri, "falcon-asm", 1, "\tnop\n")
	h.waitPub(uri, version(1))
	h.change(uri, 2, "\tnop\n\tnop\n")
	h.waitPub(toolchain.PathToURI(hdr), hasMsg("header trouble"))
	h.change(uri, 3, "\tnop\n")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.log.String(), "permission denied") {
		if time.Now().After(deadline) {
			t.Fatalf("log = %s", h.log)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
