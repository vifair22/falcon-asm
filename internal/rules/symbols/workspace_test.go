package symbols

import (
	"os"
	"path/filepath"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	symidx "git.airies.net/vifair22/falcon-asm/internal/symbols"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// lintFile lints one file of the tree against a real index over it, as
// the CLI does.
func lintFile(t *testing.T, ix *symidx.Index, dir, name string, codes ...string) []lint.Finding {
	t.Helper()
	p := filepath.Join(dir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	uri := toolchain.PathToURI(p)
	doc := &docstore.Document{URI: uri, Text: string(data)}
	prog := asm.Parse(doc, lexer.Tokenize(doc.Text), asm.Config{})
	return lint.Run(lint.Input{Prog: prog, Lang: lint.LangAsm, Rules: rulesFor(t, codes...), Workspace: ix.LintView(uri)})
}

func TestWorkspaceIndex(t *testing.T) {
	dir := writeTree(t, map[string]string{
		// main.s pulls helpers in with .include; the fragment uses a
		// label of its includer.
		"boot/main.s": "\t.globl _start\n_start:\n\tcall helper\n\tcall kmain\n\tcall missing\n" +
			"\tcall far_entry\n\tjmp back\nback:\n\t.include \"inc/helpers.inc\"\n",
		"boot/inc/helpers.inc": "helper:\n\tjmp back\n\tret\nunusedhelper:\n\tret\n",
		// A C source defines kmain; a linker script names ld_entry.
		"kernel/main.c": "void kmain(void) { for (;;); }\n",
		"link.ld":       "ENTRY(ld_entry)\n",
		"stage/entry.s": "ld_entry:\n\tret\nlocal_only:\n\tret\n",
		// far.s and main.s are linked (main.s calls far_entry, only far.s
		// defines it), and both define the global "shared".
		"far.s":   "\t.globl far_entry, shared\nfar_entry:\nshared:\n\tret\n",
		"near.s":  "\t.globl shared\nshared:\n\tcall far_entry\n\tret\n",
		"tool1.s": "\t.globl _start\n_start:\n\tret\n",
		"tool2.s": "\t.globl _start\n_start:\n\tret\n",
		"unres.S": "#include \"nothere.h\"\n\tcall missing2\n",
	})
	ix := symidx.NewIndex(nil)
	ix.AddRoot(dir)

	expect(t, lintFile(t, ix, dir, "boot/main.s", CodeUndefinedLabel), "undefined-label@4")
	// The fragment: back is defined by its includer, which also uses it.
	expect(t, lintFile(t, ix, dir, "boot/inc/helpers.inc", CodeUndefinedLabel, CodeUnusedLabel), "unused-label@3")
	// helper is used by the includer; ld_entry by the linker script.
	expect(t, lintFile(t, ix, dir, "stage/entry.s", CodeUnusedLabel), "unused-label@2")
	expect(t, lintFile(t, ix, dir, "near.s", CodeDuplicateDefinition), "duplicate-definition@1")
	expect(t, lintFile(t, ix, dir, "far.s", CodeDuplicateDefinition), "duplicate-definition@2")
	// Standalone tools that each define _start are separate programs.
	expect(t, lintFile(t, ix, dir, "tool1.s", CodeDuplicateDefinition), "")
	// An include that is not found leaves undefined names unknown.
	expect(t, lintFile(t, ix, dir, "unres.S", CodeUndefinedLabel), "")

	got := lintFile(t, ix, dir, "near.s", CodeDuplicateDefinition)
	if len(got) != 1 || len(got[0].Related) != 1 || filepath.Base(got[0].Related[0].URI) != "far.s" {
		t.Fatalf("related = %+v", got)
	}
}

// A label defined twice in one translation unit through an include is a
// duplicate even though both files are private.
func TestWorkspaceIncludeDuplicate(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.s":   "\t.globl dup\ndup:\n\t.include \"b.inc\"\n",
		"b.inc": "dup:\n",
	})
	ix := symidx.NewIndex(nil)
	ix.AddRoot(dir)
	expect(t, lintFile(t, ix, dir, "a.s", CodeDuplicateDefinition), "duplicate-definition@1")
}
