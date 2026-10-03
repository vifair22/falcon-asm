package symbols

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func TestLintView(t *testing.T) {
	dir := t.TempDir()
	main := writeFile(t, dir, "boot/main.S", "#include \"defs.h\"\n\t.include \"inc/frag.inc\"\n\t.globl _start\n_start:\n\tcall fraglabel\n")
	writeFile(t, dir, "boot/defs.h", "#define SEL 8\n")
	frag := writeFile(t, dir, "boot/inc/frag.inc", "fraglabel:\n\tjmp _start\n\t.include \"nested.inc\"\n")
	writeFile(t, dir, "boot/inc/nested.inc", "deep:\n")
	writeFile(t, dir, "c/k.c", "int kmain(void) { return 0x1fu; }\n")
	writeFile(t, dir, "x.ld", "ENTRY(ld_sym)\n")
	writeFile(t, dir, "other.s", "\t.globl pub\npub:\npriv:\n")
	lonely := writeFile(t, dir, "lonely.S", "#include <sys/none.h>\n\t.include \"gone.inc\"\n")
	ix := NewIndex(nil)
	ix.AddRoot(dir)

	v := ix.LintView(pathURI(main))
	for name, want := range map[string]bool{
		"fraglabel": true, "deep": true, "SEL": true, "pub": true, "kmain": true, "ld_sym": true,
		"priv": false, "nope": false, "1fu": false, "_start": false,
	} {
		if got := v.Defined(name); got != want {
			t.Errorf("Defined(%s) = %v", name, got)
		}
	}
	if !v.Complete() {
		t.Error("main's includes are all found")
	}
	if v.Referenced("_start") != true || v.Referenced("pub") || !v.Referenced("ld_sym") {
		t.Error("Referenced")
	}
	// From the fragment, the includer is part of the unit.
	fv := ix.LintView(pathURI(frag))
	if !fv.Defined("_start") || !fv.Referenced("fraglabel") || !fv.Complete() {
		t.Error("fragment view")
	}
	if len(fv.GlobalDefinitions("_start")) != 1 {
		t.Errorf("unit definitions = %v", fv.GlobalDefinitions("_start"))
	}
	if lv := ix.LintView(pathURI(lonely)); lv.Complete() {
		t.Error("unresolved includes are incomplete")
	}
	if nv := ix.LintView("untitled:x"); nv.Complete() || nv.Defined("deep") || nv.GlobalDefinitions("pub") != nil {
		t.Error("an unindexed document knows nothing of its own includes")
	} else if !nv.Defined("pub") {
		t.Error("globals are visible to an unindexed document")
	}

	// Navigation follows .include from the main file.
	defs := ix.Definition(pathURI(main), protocol.Position{Line: 4, Character: 8})
	if len(defs) != 1 || filepath.Base(defs[0].URI) != "frag.inc" {
		t.Errorf("definition across .include = %v", defs)
	}
}

func TestLintViewWordsRescan(t *testing.T) {
	dir := t.TempDir()
	c := writeFile(t, dir, "a.c", "void one(void);\n")
	ix := NewIndex(nil)
	ix.rescanEvery = 0
	ix.AddRoot(dir)
	if !ix.LintView("untitled:x").Defined("one") {
		t.Fatal("word not found")
	}
	// A changed file is reread; a removed one is dropped.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(c, []byte("void two_two(void);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v := ix.LintView("untitled:x")
	if v.Defined("one") || !v.Defined("two_two") {
		t.Fatal("changed word file not reread")
	}
	if err := os.Remove(c); err != nil {
		t.Fatal(err)
	}
	if ix.LintView("untitled:x").Defined("two_two") {
		t.Fatal("removed word file kept")
	}
}

func TestIncludeResolver(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/a.s", "\t.include \"x.inc\"\n#include <sys.h>\n")
	writeFile(t, dir, "elsewhere/x.inc", "xlabel:\n")
	writeFile(t, dir, "sysinc/sys.h", "#define SYSMAC 1\n")
	ix := NewIndex(nil)
	var asked []bool
	ix.SetIncludeResolver(func(from, name string, angled bool) (string, bool) {
		asked = append(asked, angled)
		if angled {
			return filepath.Join(dir, "sysinc", name), true
		}
		return filepath.Join(dir, "elsewhere", name), true
	})
	ix.AddRoot(filepath.Join(dir, "src"))
	v := ix.LintView(pathURI(filepath.Join(dir, "src/a.s")))
	if !v.Defined("xlabel") || !v.Defined("SYSMAC") || !v.Complete() {
		t.Fatal("custom resolver not used")
	}
	if len(asked) != 2 || asked[0] || !asked[1] {
		t.Fatalf("angled = %v", asked)
	}
}

func TestDefaultResolveAndHelpers(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "r/inc.h", "")
	if got, ok := defaultResolve(filepath.Join(dir, "x/a.S"), "inc.h", false, []string{filepath.Join(dir, "r")}); !ok || got != p {
		t.Fatalf("root fallback: %s %v", got, ok)
	}
	if _, ok := defaultResolve(filepath.Join(dir, "r/a.S"), "inc.h", true, nil); ok {
		t.Fatal("angled include searched the includer's directory")
	}
	if got, ok := defaultResolve("", p, true, nil); !ok || got != p {
		t.Fatal("absolute")
	}
	// Linker script detection itself is toolchain.IsLinkerScript's test.
	if !isWordFile("jmod.ld.S") {
		t.Fatal("a preprocessed linker script is a word file")
	}
	if !isWordFile("a.hpp") || isWordFile("a.S") {
		t.Fatal("isWordFile")
	}
	if got := uniqueWords("a b_1 a 0x1f 9z c"); len(got) != 3 || got[2] != "c" {
		t.Fatalf("uniqueWords = %v", got)
	}
	if !suffixMatch("/x/y/inc/a.h", "inc/a.h") || !suffixMatch("/x/a.h", "./a.h") || suffixMatch("/x/ba.h", "a.h") {
		t.Fatal("suffixMatch")
	}
}
