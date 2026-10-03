package toolchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotExpanded means the preprocessor ran but the requested lines did
// not survive it: they sit in an inactive #if region.
var ErrNotExpanded = errors.New("line not in the preprocessor output")

// Markers bracket the lines to expand. They are identifiers no project
// defines, so cpp passes them through untouched, and lines of their own,
// so a marker can never merge with the expansion.
const (
	expandBegin = "falcon_asm_expand_begin_6c1f"
	expandEnd   = "falcon_asm_expand_end_6c1f"
)

// ExpandLines preprocesses text (the document's current contents, which
// may be unsaved) with the file's flags and returns what lines first..last
// (zero-based, inclusive: one logical line with its continuations) become.
// The whole text is preprocessed, so the #defines above the line and the
// headers it includes apply exactly as in a build; the result is what the
// assembler sees for that line.
func (r *Runner) ExpandLines(ctx context.Context, file string, f Flags, text string, first, last int) (string, error) {
	file, err := filepath.Abs(file)
	if err != nil {
		return "", fmt.Errorf("expand: %w", err)
	}
	compiler, err := compilerPath(f)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	if first < 0 || last < first || last >= len(lines) {
		return "", fmt.Errorf("expand %s: lines %d-%d out of range", file, first+1, last+1)
	}
	var b strings.Builder
	for i, l := range lines {
		if i == first {
			b.WriteString(expandBegin + "\n")
		}
		b.WriteString(l)
		b.WriteByte('\n')
		if i == last {
			b.WriteString(expandEnd + "\n")
		}
	}
	ctx, done := r.begin(ctx, "expand\x00"+file)
	defer done()
	tmp, err := os.MkdirTemp("", "falcon-asm-x-*")
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", file, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	in := filepath.Join(tmp, filepath.Base(file))
	if err := os.WriteFile(in, []byte(b.String()), 0o600); err != nil {
		return "", fmt.Errorf("expand %s: %w", file, err)
	}
	// The copy lives in a temp dir, so the file's own directory is added
	// for its quoted includes.
	args := append([]string{"-iquote", filepath.Dir(file)}, f.Args...)
	args = append(args, "-E", "-P", "-fdiagnostics-color=never", "-x", "assembler-with-cpp", in)
	res, err := run(ctx, compiler, f, args)
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", file, err)
	}
	return between(res.stdout)
}

// between returns the output lines between the markers, without the blank
// lines -P leaves where comments and directives were.
func between(out string) (string, error) {
	_, rest, ok := strings.Cut(out, expandBegin)
	if !ok {
		return "", ErrNotExpanded
	}
	body, _, ok := strings.Cut(rest, expandEnd)
	if !ok {
		return "", ErrNotExpanded
	}
	var keep []string
	for _, l := range strings.Split(body, "\n") {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, strings.TrimRight(l, " \t\r"))
		}
	}
	return strings.Join(keep, "\n"), nil
}
