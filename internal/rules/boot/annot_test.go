package boot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// stubMacros makes fileMacros return m (or err), as a compile database
// would, and clears the cache around the test.
func stubMacros(t *testing.T, m map[string]string, err error) {
	t.Helper()
	old := fileMacros
	clearCaches := func() {
		macroCache.Lock()
		macroCache.m = map[string]cachedMacros{}
		macroCache.Unlock()
		a20Cache.Lock()
		a20Cache.m = map[string]cachedA20{}
		a20Cache.Unlock()
	}
	clearCaches()
	t.Cleanup(func() { fileMacros = old; clearCaches() })
	fileMacros = func(string) (map[string]string, error) { return m, err }
}

// ---- mode-switch-a20 ----

var a20Codes = []string{"mode-switch-a20"}

// airies-boot's AP trampoline (falcon-bios/src/mp/aptramp.S) sets PE with
// no A20 code: mp.c sends the SIPI, so nothing in the file says it is a
// trampoline, and the finding stands with its hint.
func TestAptrampUnmarked(t *testing.T) {
	got := lintText(t, fixture(t, "aptramp.S"), opts{macros: headerMacros(t)}, a20Codes...)
	f := mustHave(t, got, "mode-switch-a20", "nothing in this file enables A20")
	if !strings.Contains(f.Message, "`falcon-asm: a20=<file or function>`") {
		t.Errorf("no hint: %s", f.Message)
	}
}

// A copied trampoline, marked: the switch runs on an application
// processor, which inherits A20, and the alignment rules have nothing to
// say about a page it is copied to.
func TestAptrampCopiedTrampoline(t *testing.T) {
	text := strings.Replace(fixture(t, "aptramp.S"), "\t.code16\naptramp_start:\n",
		"\t.code16\n/* falcon-asm: trampoline=copied */\naptramp_start:\n", 1)
	if text == fixture(t, "aptramp.S") {
		t.Fatal("fixture changed")
	}
	if got := lintText(t, text, opts{macros: headerMacros(t)}, "mode-switch-a20", "sipi-trampoline", "sipi-unproven"); len(got) != 0 {
		t.Fatalf("copied trampoline: %v", got)
	}
	// A plain `trampoline` mark skips the A20 rule too; the alignment
	// verdict is then sipi-unproven's business.
	plain := strings.Replace(text, "trampoline=copied", "trampoline", 1)
	got := lintText(t, plain, opts{macros: headerMacros(t)}, "mode-switch-a20", "sipi-unproven")
	if len(got) != 1 || got[0].Code != "sipi-unproven" {
		t.Fatalf("in-place trampoline: %v", got)
	}
}

// A trampoline recognised from the ICR write that starts it, in the same
// file: no mark needed.
func TestSIPIFromICR(t *testing.T) {
	src := "\t.code32\n\tmovl $(0x4600 | (ap_start >> 12)), 0xFEE00300\n\tret\n" +
		"\t.balign 4096\n\t.code16\nap_start:\n\tcli\n\tlgdtl gdtr\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n" +
		"\tljmpl $8, $ap32\n\t.code32\nap32:\n\thlt\ngdtr:\t.word 0\n\t.long 0\n"
	if got := lintText(t, src, opts{mode: asm.Mode32}, a20Codes...); len(got) != 0 {
		t.Fatalf("ICR trampoline: %v", got)
	}
	// The same switch outside the trampoline is reported.
	noSIPI := strings.Replace(src, "(0x4600 | (ap_start >> 12))", "0x4500", 1)
	mustHave(t, lintText(t, noSIPI, opts{mode: asm.Mode32}, a20Codes...), "mode-switch-a20", "nothing in this file enables A20")
}

// a20=<file>, a20=<file>:<label>, a20=<label>: resolved and checked.
func TestA20Annotation(t *testing.T) {
	stubMacros(t, headerMacros(t), nil)
	dir := t.TempDir()
	start := filepath.Join(dir, "start")
	mp := filepath.Join(dir, "mp")
	for _, d := range []string{start, mp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"pmode.S", "thunk.S"} {
		if err := os.WriteFile(filepath.Join(start, f), []byte(fixture(t, f)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	uri := toolchain.PathToURI(filepath.Join(mp, "aptramp.S"))
	base := fixture(t, "aptramp.S")
	with := func(target string) []lint.Finding {
		text := strings.Replace(base, "\t.code16\naptramp_start:\n", "\t.code16\n/* falcon-asm: a20="+target+" */\naptramp_start:\n", 1)
		return lintText(t, text, opts{macros: headerMacros(t), uri: uri}, a20Codes...)
	}
	for _, ok := range []string{"../start/pmode.S", "../start/pmode.S:pmode_enter"} {
		if got := with(ok); len(got) != 0 {
			t.Errorf("a20=%s: %v", ok, got)
		}
	}
	cases := []struct{ target, msg string }{
		{"../start/thunk.S", "../start/thunk.S does not enable A20"},
		{"../start/missing.S", "names no file"},
		{"../start/pmode.S:nosuch", "pmode.S has no label nosuch"},
		{"../start/pmode.S:pmode_32", "pmode_32 in ../start/pmode.S does not enable A20"},
		{"ap16", "ap16, in this file, does not enable A20"},
		{"nowhere", "names no label of this file, no global of a file linked with it and no file"},
	}
	for _, c := range cases {
		f := mustHave(t, with(c.target), "mode-switch-a20", c.msg)
		if !strings.Contains(f.Message, "`falcon-asm: a20="+c.target+"` does not prove") {
			t.Errorf("%s: %s", c.target, f.Message)
		}
	}
}

// fakeWS finds labels in fixed files, as the symbol index does for files
// linked with the document.
type fakeWS map[string]string

func (w fakeWS) Defined(name string) bool { _, ok := w[name]; return ok }
func (w fakeWS) Referenced(string) bool   { return false }
func (w fakeWS) Complete() bool           { return true }
func (w fakeWS) GlobalDefinitions(name string) []protocol.Location {
	if p, ok := w[name]; ok {
		return []protocol.Location{{URI: toolchain.PathToURI(p)}}
	}
	return nil
}

// a20=<global>: the workspace says which linked file defines it.
func TestA20AnnotationWorkspace(t *testing.T) {
	stubMacros(t, headerMacros(t), nil)
	dir := t.TempDir()
	pm := filepath.Join(dir, "pmode.S")
	if err := os.WriteFile(pm, []byte(fixture(t, "pmode.S")), 0o644); err != nil {
		t.Fatal(err)
	}
	tk := filepath.Join(dir, "thunk.S")
	if err := os.WriteFile(tk, []byte(fixture(t, "thunk.S")), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := fakeWS{"pmode_enter": pm, "thunk_bios": tk}
	run := func(target string) []lint.Finding {
		text := strings.Replace(fixture(t, "aptramp.S"), "\t.code16\naptramp_start:\n", "\t.code16\n/* falcon-asm: a20="+target+" */\naptramp_start:\n", 1)
		p := parse(t, toolchain.PathToURI(filepath.Join(dir, "ap.S")), text, asm.Mode16, headerMacros(t))
		r, _ := lint.Lookup("mode-switch-a20")
		return lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: lint.Target{Mode: asm.Mode16}, Tables: hwdb.Default(),
			Rules: []*lint.Rule{r}, Workspace: ws})
	}
	if got := run("pmode_enter"); len(got) != 0 {
		t.Fatalf("a20=pmode_enter: %v", got)
	}
	mustHave(t, run("thunk_bios"), "mode-switch-a20", "thunk_bios (thunk.S) does not enable A20")
}

// ---- loader-sector-count max= ----

// stage2Bin writes a stage of n bytes next to the loader.
func stage2Bin(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "stage2.bin"), make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// airies-boot's stage 1 loaders (falcon-bios/src/start/boot.S and
// boot_fd.S): the installer patches the count, the read loops, and the
// build holds stage 2 to STAGE2_MBR_MAX_SECTORS. max= states that bound.
func TestLoaderMaxAnnotation(t *testing.T) {
	m := headerMacros(t)
	if m["STAGE2_MBR_MAX_SECTORS"] == "" {
		m["STAGE2_MBR_MAX_SECTORS"] = "62"
	}
	stubMacros(t, nil, errors.New("no compile database"))
	dir := t.TempDir()
	for _, name := range []string{"boot.S", "boot_fd.S"} {
		src := strings.ReplaceAll(fixture(t, name), "#include <start/biosdefs.h>", "/* falcon-asm: loads=stage2.bin max=STAGE2_MBR_MAX_SECTORS */")

		// Unannotated: the hint names the annotation that would prove it.
		plain := strings.ReplaceAll(fixture(t, name), "#include <start/biosdefs.h>", "")
		f := mustHave(t, lintAssembled(t, dir, name, plain, m, loaderCodes...), "loader-sector-count-unproven", "loop")
		if !strings.Contains(f.Message, "`falcon-asm: loads=<label or file> max=<sectors or symbol>`") {
			t.Errorf("%s: no hint: %s", name, f.Message)
		}

		// Within the bound: proven, nothing to say.
		stage2Bin(t, dir, 20000)
		if got := lintAssembled(t, dir, name, src, m, loaderCodes...); len(got) != 0 {
			t.Errorf("%s within: %v", name, got)
		}
		// Exactly the bound fits.
		stage2Bin(t, dir, 62*512)
		if got := lintAssembled(t, dir, name, src, m, loaderCodes...); len(got) != 0 {
			t.Errorf("%s at the bound: %v", name, got)
		}
		// One byte past: the warning.
		stage2Bin(t, dir, 62*512+1)
		mustHave(t, lintAssembled(t, dir, name, src, m, loaderCodes...), "loader-sector-count",
			"stage2.bin is 31745 bytes (63 sectors), past the 62 sectors (31744 bytes) `max=STAGE2_MBR_MAX_SECTORS` holds it to")

		// The symbol unknown to the run and to the compile database.
		stage2Bin(t, dir, 2048)
		bad := strings.Replace(src, "max=STAGE2_MBR_MAX_SECTORS", "max=STAGE2_NOPE", 1)
		f = mustHave(t, lintAssembled(t, dir, name, bad, m, loaderCodes...), "loader-sector-count-unproven", "max=STAGE2_NOPE cannot be resolved")
		if !strings.Contains(f.Message, "no compile database") {
			t.Errorf("%s: %s", name, f.Message)
		}
	}
}

// max= resolves through a number, a .equ of the file, and the macros the
// file's headers define (the compile database), in that order of reach.
func TestLoaderMaxResolution(t *testing.T) {
	dir := t.TempDir()
	stage2Bin(t, dir, 1500)
	m := headerMacros(t)
	src := strings.ReplaceAll(fixture(t, "boot.S"), "#include <start/biosdefs.h>", "/* falcon-asm: loads=stage2.bin max=MAXV */")
	cases := []struct {
		name, max, extra string
		tc               map[string]string
		code, msg        string
	}{
		{"number within", "3", "", nil, "", ""},
		{"number exceeded", "2", "", nil, "loader-sector-count", "past the 2 sectors (1024 bytes) `max=2`"},
		{"expression", "(1 + 2)", "", nil, "", ""},
		{"equ", "S2MAX", "\t.equ S2MAX, 3\n", nil, "", ""},
		{"equ exceeded", "S2MAX", "\t.equ S2MAX, 2\n", nil, "loader-sector-count", "past the 2 sectors"},
		{"header via the compile database", "HDR_MAX", "", map[string]string{"HDR_MAX": "4"}, "", ""},
		{"header exceeded", "HDR_MAX", "", map[string]string{"HDR_MAX": "1"}, "loader-sector-count", "past the 1 sectors"},
		{"unknown", "HDR_MAX", "", map[string]string{}, "loader-sector-count-unproven", "max=HDR_MAX cannot be resolved: HDR_MAX is not a number, nor a macro"},
		{"not constant", "3 +", "", nil, "loader-sector-count-unproven", "is not a constant expression"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubMacros(t, c.tc, nil)
			text := strings.Replace(src, "max=MAXV", "max="+strings.ReplaceAll(c.max, " ", ""), 1)
			if c.extra != "" {
				text = strings.Replace(text, "\t.code16\n", "\t.code16\n"+c.extra, 1)
			}
			got := lintAssembled(t, dir, "b.S", text, m, loaderCodes...)
			if c.code == "" {
				if len(got) != 0 {
					t.Fatalf("%v", got)
				}
				return
			}
			mustHave(t, got, c.code, c.msg)
		})
	}
}

// A suppression comment's reason is free text: words in it that look like
// annotations are not read.
func TestNotesStopAtIgnore(t *testing.T) {
	p := parse(t, "file:///n.S", "\tnop /* falcon-asm: ignore=x: see loads=foo.bin max=3 */\n\tnop /* falcon-asm: a20=pmode.S */\n", asm.Mode16, nil)
	ns := notesOf(p)
	if len(ns) != 1 || ns[0].items["a20"] != "pmode.S" {
		t.Fatalf("%v", ns)
	}
	if v, _, ok := noteValue(p, "loads"); ok {
		t.Fatalf("loads=%s read from a reason", v)
	}
}

func TestSplitA20Target(t *testing.T) {
	cases := []struct{ in, file, label string }{
		{"pmode.S", "pmode.S", ""},
		{"../start/pmode.S", "../start/pmode.S", ""},
		{"../start/pmode.S:pmode_enter", "../start/pmode.S", "pmode_enter"},
		{"pmode_enter", "", "pmode_enter"},
		{"sub/stage", "sub/stage", ""},
	}
	for _, c := range cases {
		if f, l := splitA20Target(c.in); f != c.file || l != c.label {
			t.Errorf("%s: %q %q", c.in, f, l)
		}
	}
}
