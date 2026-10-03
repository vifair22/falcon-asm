// Package benchdata supplies the inputs the module's benchmarks share: the
// falcon-bios fixtures and large documents built from them, so every
// package measures against the same text and numbers compare across
// packages. It is imported by _test files only.
package benchdata

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Dir is the fixture directory, found relative to this source file so a
// benchmark works from any package directory.
func Dir() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..", "testdata", "falcon-bios")
}

// Fixtures returns every fixture's text, sorted by file name.
func Fixtures(tb testing.TB) []string {
	tb.Helper()
	paths, err := filepath.Glob(filepath.Join(Dir(), "*.S"))
	if err != nil || len(paths) == 0 {
		tb.Fatalf("no fixtures in %s: %v", Dir(), err)
	}
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, string(raw))
	}
	return out
}

// Fixture returns one fixture's text by file name.
func Fixture(tb testing.TB, name string) string {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		tb.Fatal(err)
	}
	return string(raw)
}

// Large concatenates the fixtures, in order and repeatedly, until the text
// has at least lines lines. Labels repeat across copies; nothing measured
// with it depends on them being unique (lspbench renames them because its
// navigation queries do).
func Large(tb testing.TB, lines int) string {
	tb.Helper()
	fx := Fixtures(tb)
	var b strings.Builder
	n := 0
	for i := 0; n < lines; i++ {
		t := fx[i%len(fx)]
		if !strings.HasSuffix(t, "\n") {
			t += "\n"
		}
		b.WriteString(t)
		n += strings.Count(t, "\n")
	}
	return b.String()
}
