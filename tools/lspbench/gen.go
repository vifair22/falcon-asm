package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fixture is one source file used as generator input.
type fixture struct {
	name string
	text string
}

// loadFixtures reads every *.S in dir, sorted by name so every generated
// file is the same on every run.
func loadFixtures(dir string) ([]fixture, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.S"))
	if err != nil {
		return nil, fmt.Errorf("glob fixtures: %w", err)
	}
	sort.Strings(paths)
	var out []fixture
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read fixture: %w", err)
		}
		out = append(out, fixture{name: filepath.Base(p), text: string(raw)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no *.S fixtures in %s", dir)
	}
	return out, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c == '$' || c >= '0' && c <= '9'
}

// labelNames lists the symbolic labels a source defines ("name:" at the
// start of a statement). Numeric local labels are left alone: they are
// already reusable.
func labelNames(src string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(src, "\n") {
		s := strings.TrimLeft(line, " \t")
		i := 0
		for i < len(s) && isIdentChar(s[i]) {
			i++
		}
		if i == 0 || !isIdentStart(s[0]) || i >= len(s) || s[i] != ':' {
			continue
		}
		out[s[:i]] = true
	}
	return out
}

// renameLabels appends suffix to every whole-word occurrence of a label the
// source defines, so concatenated copies do not redefine each other's
// labels. It is a textual pass: comments and strings are renamed too, which
// changes nothing a benchmark can observe.
func renameLabels(src, suffix string) string {
	names := labelNames(src)
	if len(names) == 0 {
		return src
	}
	var b strings.Builder
	b.Grow(len(src) + len(src)/8)
	for i := 0; i < len(src); {
		if !isIdentStart(src[i]) || i > 0 && isIdentChar(src[i-1]) {
			b.WriteByte(src[i])
			i++
			continue
		}
		j := i + 1
		for j < len(src) && isIdentChar(src[j]) {
			j++
		}
		w := src[i:j]
		b.WriteString(w)
		if names[w] {
			b.WriteString(suffix)
		}
		i = j
	}
	return b.String()
}

// genLarge concatenates renamed fixture copies until the text has at least
// lines lines.
func genLarge(fx []fixture, lines int) string {
	var b strings.Builder
	n := 0
	for c := 0; n < lines; c++ {
		f := fx[c%len(fx)]
		text := renameLabels(f.text, fmt.Sprintf("_c%d", c))
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		b.WriteString(text)
		n += strings.Count(text, "\n")
	}
	return b.String()
}

// Workspace layout constants: file 0 defines the global label every other
// file calls, at fixed positions so the scenario can query them blind.
const (
	globalLabel = "common_entry"
	// In file 0, line 1 is "common_entry:"; in file i>0, line 2 is
	// "\tcall\tcommon_entry" with the name starting at UTF-16 column 6.
	globalDefLine  = 1
	globalCallLine = 2
	globalCallCol  = 6
)

// wsFileName names workspace file i.
func wsFileName(i int) string { return fmt.Sprintf("ws%04d.S", i) }

// genWorkspace writes n assembly files into dir: renamed fixture copies,
// each with a global entry point that calls the shared global label.
func genWorkspace(dir string, fx []fixture, n int) error {
	for i := range n {
		var b strings.Builder
		if i == 0 {
			fmt.Fprintf(&b, "\t.globl\t%s\n%s:\n\tret\n", globalLabel, globalLabel)
		} else {
			fmt.Fprintf(&b, "\t.globl\tws_entry_%d\nws_entry_%d:\n\tcall\t%s\n\tret\n", i, i, globalLabel)
		}
		b.WriteString(renameLabels(fx[i%len(fx)].text, fmt.Sprintf("_w%d", i)))
		if err := os.WriteFile(filepath.Join(dir, wsFileName(i)), []byte(b.String()), 0o644); err != nil {
			return fmt.Errorf("write workspace file: %w", err)
		}
	}
	return nil
}

// typedText is what the typing scenario types, one byte per edit: ordinary
// instruction lines with registers, an immediate and a label reference, so
// completion sees every context.
const typedText = "\tmovl\t%eax, %ebx\n\taddl\t$1, %ecx\n\tjnz\tloop_top\n\tpushw\t%ds\n"
