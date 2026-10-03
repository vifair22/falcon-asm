package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRealTree runs against a real checkout when FALCON_ASM_REAL_TREE names
// one (e.g. airies-boot). It is opt-in because the tree is not part of this
// repository; it only reads it, and all output goes to a temp dir.
func TestRealTree(t *testing.T) {
	root := os.Getenv("FALCON_ASM_REAL_TREE")
	if root == "" {
		t.Skip("FALCON_ASM_REAL_TREE not set")
	}
	db, err := LoadDB(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "falcon-bios/src/start/boot.S")
	f, ok := db.Flags(file)
	if !ok {
		t.Fatalf("no flags for %s", file)
	}
	t.Logf("flags: %s %v mode=%d exact=%v", f.Compiler, f.Args, f.Mode, f.Exact)
	var r Runner
	res, err := r.Assemble(context.Background(), file, f)
	if err != nil {
		t.Fatal(err)
	}
	for p, ds := range res.Diagnostics {
		for _, d := range ds {
			t.Logf("diag %s:%d: %s", p, d.Range.Start.Line+1, d.Message)
		}
	}
	for _, l := range res.Listing {
		if isLocationMove(l.Source) {
			n, ok := res.PadBytes(l.Line)
			t.Logf("line %d %q section=%s off=%#x pad=%d ok=%v", l.Line+1, l.Source, l.Section, l.Offset, n, ok)
		}
	}
	t.Logf("inactive: %v", res.Inactive)
	for _, e := range db.entries {
		if filepath.Ext(e.file) != ".S" {
			continue
		}
		ef, _ := db.Flags(e.file)
		er, err := r.Assemble(context.Background(), e.file, ef)
		if err != nil {
			t.Errorf("%s: %v", e.file, err)
			continue
		}
		nd := 0
		for _, ds := range er.Diagnostics {
			nd += len(ds)
		}
		t.Logf("%s: diags=%d listing=%d inactive=%v", filepath.Base(e.file), nd, len(er.Listing), er.Inactive)
	}
	macros, err := r.Macros(context.Background(), file, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"STAGE1_S2_LBA_OFF", "STAGE1_S2_SECTORS_OFF"} {
		m := macros[name]
		t.Logf("macro %s = %q def=%+v", name, m.Value, m.Def)
	}
}
