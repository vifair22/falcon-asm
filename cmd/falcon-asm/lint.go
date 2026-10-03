package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/project"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/symbols"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"

	// The CLI lints with every rule.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/all"
)

const lintUsage = `usage: falcon-asm lint [flags] [paths...]

Lints GAS (AT&T) x86 assembly (.S, .s) and the inline asm of C sources
(.c, .h, .cc, .cpp) under the given files and directories (default ".").
Directories named build, .git and node_modules are skipped. Each file's
target (mode, x86-64 level, CPU, calling convention) comes from the
compile database, .falcon-asm.json and in-file comments, as in the editor.

Exit status: 0 when nothing at or above --severity was found, 1 when
something was, 2 on a usage or configuration error.

Flags:
`

// lintOptions are the parsed flags of the lint subcommand.
type lintOptions struct {
	format    string
	config    string
	ccdb      string
	toolchain bool
	cFiles    bool
	fix       bool
	threshold lint.Severity
	changed   bool
	jobs      int
	paths     []string
}

func parseLintFlags(args []string, stderr io.Writer) (lintOptions, int, bool) {
	var o lintOptions
	fs := flag.NewFlagSet("falcon-asm lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, lintUsage)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.format, "format", "text", "output format: text (file:line:col: severity: message [code]), json or sarif")
	fs.StringVar(&o.config, "config", "", "configuration file (default: the nearest .falcon-asm.json above each file)")
	fs.StringVar(&o.ccdb, "compile-commands", "", "compile_commands.json, or a directory holding one (default: ./ or ./build/, else the nearest above each file)")
	fs.BoolVar(&o.toolchain, "toolchain", false, "also assemble each file with its compiler and report gas diagnostics (slower)")
	fs.BoolVar(&o.cFiles, "c", true, "lint inline asm in C sources found in directories")
	fs.BoolVar(&o.fix, "fix", false, "apply every non-conflicting quick fix in place, then report what remains")
	sev := fs.String("severity", "warning", "lowest severity that makes the exit status 1: error, warning, info or hint")
	fs.BoolVar(&o.changed, "changed", false, "only lint files changed against git HEAD, plus untracked ones (for pre-commit)")
	fs.IntVar(&o.jobs, "jobs", runtime.GOMAXPROCS(0), "files linted in parallel")
	// Flags may follow paths ("lint src --fix"): parse, take one path,
	// parse the rest again. After "--" everything is a path.
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return o, 0, false
			}
			return o, 2, false
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		if consumedDashDash(args, rest) {
			o.paths = append(o.paths, rest...)
			break
		}
		o.paths = append(o.paths, rest[0])
		rest = rest[1:]
	}
	switch o.format {
	case "text", "json", "sarif":
	default:
		fmt.Fprintf(stderr, "falcon-asm lint: unknown --format %q (want text, json or sarif)\n", o.format)
		return o, 2, false
	}
	s, ok := lint.ParseSeverity(*sev)
	if !ok || s == lint.Off {
		fmt.Fprintf(stderr, "falcon-asm lint: unknown --severity %q (want error, warning, info or hint)\n", *sev)
		return o, 2, false
	}
	o.threshold = s
	if o.jobs < 1 {
		o.jobs = 1
	}
	if len(o.paths) == 0 {
		o.paths = []string{"."}
	}
	return o, 0, true
}

// consumedDashDash reports whether the flag parser stopped at "--" right
// before rest, which makes everything after it a path.
func consumedDashDash(all, rest []string) bool {
	i := len(all) - len(rest) - 1
	return i >= 0 && all[i] == "--"
}

// lintFile is one file to lint.
type lintFile struct {
	path    string // as found (relative to the working directory when given so)
	abs     string
	c       bool
	display string
}

// item is one reported problem, from a rule or from gas.
type item struct {
	file   *lintFile
	doc    *docstore.Document
	source string // "falcon-asm" or "gas"
	lint.Finding
}

// fileResult is what linting one file produced.
type fileResult struct {
	items []item
	fixed int
	err   error
}

// linter holds what files share: configurations and databases found by
// walking up, cached per directory.
type linter struct {
	o      lintOptions
	stderr io.Writer
	tc     *toolchain.Runner
	// index is the workspace symbol index over the paths being linted,
	// scanned once and shared by every file's symbol rules.
	index *symbols.Index

	mu        sync.Mutex
	cfgByDir  map[string]*lint.Config
	cfgSeen   map[*lint.Config]bool
	explicit  *lint.Config
	db        *toolchain.DB
	dbByDir   map[string]*toolchain.DB
	notes     map[string]bool
	cfgFailed bool
}

func runLint(args []string, stdout, stderr io.Writer) int {
	o, code, ok := parseLintFlags(args, stderr)
	if !ok {
		return code
	}
	l := &linter{
		o: o, stderr: stderr, tc: &toolchain.Runner{},
		cfgByDir: map[string]*lint.Config{}, cfgSeen: map[*lint.Config]bool{},
		dbByDir: map[string]*toolchain.DB{}, notes: map[string]bool{},
	}
	if o.config != "" {
		c, err := lint.LoadConfig(o.config)
		if err != nil {
			fmt.Fprintf(stderr, "falcon-asm lint: %v\n", err)
			return 2
		}
		l.explicit = c
	}
	if o.ccdb != "" {
		db, err := toolchain.LoadDBFile(o.ccdb)
		if err != nil {
			fmt.Fprintf(stderr, "falcon-asm lint: --compile-commands: %v\n", err)
			return 2
		}
		l.db = db
	} else if db, err := toolchain.LoadDB("."); err == nil {
		l.db = db
	}

	l.index = symbols.NewIndex(nil)
	l.index.SetIncludeResolver(l.resolveInclude)
	for _, p := range o.paths {
		if abs, err := filepath.Abs(p); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				abs = filepath.Dir(abs)
			}
			l.index.AddRoot(abs)
		}
	}
	files, err := collect(o.paths, o.cFiles)
	if err != nil {
		fmt.Fprintf(stderr, "falcon-asm lint: %v\n", err)
		return 2
	}
	if o.changed {
		if set, ok := changedFiles(); ok {
			files = filterChanged(files, set)
		} else {
			fmt.Fprintln(stderr, "falcon-asm lint: --changed: not a git repository; linting everything")
		}
	}
	// Configurations are resolved before any linting so a broken one
	// stops the run with status 2 instead of linting under defaults.
	for i := range files {
		l.configFor(files[i].abs)
	}
	if l.cfgFailed {
		return 2
	}

	results := make([]fileResult, len(files))
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(o.jobs, max(1, len(files))); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				results[i] = l.lintPath(&files[i])
			}
		}()
	}
	for i := range files {
		work <- i
	}
	close(work)
	wg.Wait()

	var items []item
	status := 0
	for i, r := range results {
		if r.err != nil {
			fmt.Fprintf(stderr, "falcon-asm lint: %s: %v\n", files[i].display, r.err)
			status = 2
			continue
		}
		if r.fixed > 0 {
			fmt.Fprintf(stderr, "%s: applied %d fix%s\n", files[i].display, r.fixed, plural(r.fixed, "", "es"))
		}
		items = append(items, r.items...)
	}
	switch o.format {
	case "json":
		err = writeJSON(stdout, items)
	case "sarif":
		err = writeSARIF(stdout, items)
	default:
		err = writeText(stdout, stderr, items, len(files))
	}
	if err != nil {
		fmt.Fprintf(stderr, "falcon-asm lint: write: %v\n", err)
		return 2
	}
	if status != 0 {
		return status
	}
	for _, it := range items {
		if it.Severity.AtLeast(o.threshold) {
			return 1
		}
	}
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// skipDirs are never descended into: build output, VCS metadata and
// JavaScript dependencies hold nothing of the project's own assembly.
var skipDirs = map[string]bool{"build": true, ".git": true, "node_modules": true}

// collect expands the path arguments. A file named explicitly is linted
// whatever its extension (as assembly unless it is C); a directory
// contributes its .S/.s files, and C sources when withC is set.
func collect(paths []string, withC bool) ([]lintFile, error) {
	seen := map[string]bool{}
	var out []lintFile
	add := func(p string) error {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if seen[abs] {
			return nil
		}
		seen[abs] = true
		out = append(out, lintFile{path: p, abs: abs, c: project.IsC(p), display: displayPath(abs)})
		return nil
	}
	for _, root := range paths {
		st, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", root, err)
		}
		if !st.IsDir() {
			if err := add(root); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != root && skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if project.IsAsm(p) || withC && project.IsC(p) {
				return add(p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].display < out[j].display })
	return out, nil
}

// displayPath is how a file is named in output: relative to the working
// directory when it lies below it, which is what an editor's problem
// matcher and a reviewer expect.
func displayPath(abs string) string {
	wd, err := os.Getwd()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs
	}
	return rel
}

// changedFiles lists the files git reports as changed against HEAD
// (staged or not) and the untracked ones, as absolute paths. ok is false
// outside a repository or without git.
func changedFiles() (map[string]bool, bool) {
	top, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, false
	}
	top = strings.TrimSpace(top)
	set := map[string]bool{}
	// A repository with no commit yet has no HEAD to diff against; every
	// file in the index is then new.
	diff, err := gitOutput(top, "diff", "--name-only", "HEAD")
	if err != nil {
		if diff, err = gitOutput(top, "ls-files", "--cached"); err != nil {
			return nil, false
		}
	}
	untracked, err := gitOutput(top, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, false
	}
	for _, l := range strings.Split(diff+"\n"+untracked, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			set[filepath.Join(top, filepath.FromSlash(l))] = true
		}
	}
	return set, true
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// filterChanged keeps the files in set. Paths are compared after resolving
// symlinks, since git reports the real top-level directory.
func filterChanged(files []lintFile, set map[string]bool) []lintFile {
	out := files[:0]
	for _, f := range files {
		p := f.abs
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if set[p] || set[f.abs] {
			out = append(out, f)
		}
	}
	return out
}

// configFor is the configuration of a file: --config, else the nearest
// .falcon-asm.json above it. Each configuration's problems are printed
// once; an error among them fails the run.
func (l *linter) configFor(abs string) *lint.Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.explicit
	if c == nil {
		dir := filepath.Dir(abs)
		var cached bool
		if c, cached = l.cfgByDir[dir]; !cached {
			if p, found := lint.FindConfig(dir); found {
				loaded, err := lint.LoadConfig(p)
				if err != nil {
					fmt.Fprintf(l.stderr, "falcon-asm lint: %v\n", err)
					l.cfgFailed = true
				}
				c = loaded
			}
			l.cfgByDir[dir] = c
		}
	}
	if c != nil && !l.cfgSeen[c] {
		l.cfgSeen[c] = true
		for _, p := range c.Problems {
			fmt.Fprintf(l.stderr, "%s:%d:%d: %s: %s\n", displayPath(c.Path), p.Range.Start.Line+1, p.Range.Start.Character+1, p.Severity, p.Message)
		}
		if c.Fatal() {
			l.cfgFailed = true
		}
	}
	return c
}

// flagsFor finds a file's compile flags: the database named or found in
// the working directory, else the nearest one above the file.
func (l *linter) flagsFor(abs string) (toolchain.Flags, bool) {
	l.mu.Lock()
	db := l.db
	l.mu.Unlock()
	if db != nil {
		if f, ok := db.Flags(abs); ok {
			return f, true
		}
	}
	dir := filepath.Dir(abs)
	l.mu.Lock()
	found, cached := l.dbByDir[dir]
	l.mu.Unlock()
	if !cached {
		found, _ = toolchain.FindDB(abs)
		l.mu.Lock()
		l.dbByDir[dir] = found
		l.mu.Unlock()
	}
	if found == nil {
		return toolchain.Flags{}, false
	}
	return found.Flags(abs)
}

// resolveInclude finds what a file in the index includes: through its
// compile flags (cpp's search for #include, gas's for .include; the index
// does not say which, so both are tried) when it has some, else next to
// the file.
func (l *linter) resolveInclude(from, name string, angled bool) (string, bool) {
	if flags, ok := l.flagsFor(from); ok {
		if p, ok := flags.ResolveInclude(from, name, angled); ok {
			return p, true
		}
		if !angled {
			return flags.ResolveGasInclude(from, name)
		}
		return "", false
	}
	if angled {
		return "", false
	}
	p := name
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(from), name)
	}
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return p, true
	}
	return "", false
}

func (l *linter) noteOnce(key, format string, args ...any) {
	l.mu.Lock()
	seen := l.notes[key]
	l.notes[key] = true
	l.mu.Unlock()
	if !seen {
		fmt.Fprintf(l.stderr, "falcon-asm lint: "+format+"\n", args...)
	}
}

// lintPath lints one file, and with --fix applies the fixes and lints the
// result again, so what is reported is what is left.
func (l *linter) lintPath(f *lintFile) fileResult {
	data, err := os.ReadFile(f.abs)
	if err != nil {
		return fileResult{err: err}
	}
	items := l.lintText(f, string(data))
	if !l.o.fix {
		return fileResult{items: items}
	}
	text, n := applyFixes(string(data), items)
	if n == 0 {
		return fileResult{items: items}
	}
	if err := os.WriteFile(f.abs, []byte(text), 0o644); err != nil {
		return fileResult{err: fmt.Errorf("write fixes: %w", err)}
	}
	return fileResult{items: l.lintText(f, text), fixed: n}
}

func (l *linter) lintText(f *lintFile, text string) []item {
	doc := &docstore.Document{URI: toolchain.PathToURI(f.abs), Text: text}
	flags, hasFlags := l.flagsFor(f.abs)
	fc := l.configFor(f.abs).For(f.abs)
	tables, err := lint.LoadTables(fc.Tables)
	if err != nil {
		l.noteOnce("tables", "hardware tables: %v; built-in tables only", err)
		tables, _ = lint.LoadTables(nil)
	}
	tin := project.TargetInputs{Flags: flags, HasFlags: hasFlags, Config: fc.Target}
	d := project.Doc{Doc: doc, C: f.c, Config: fc, Tables: tables, Log: func(format string, args ...any) {
		l.noteOnce(fmt.Sprintf(format, args...), format, args...)
	}}
	var gas []protocol.Diagnostic
	if f.c {
		d.Target = project.ResolveTarget(tin, project.CLevelComment(text))
	} else {
		if l.index != nil {
			d.Workspace = l.index.LintView(doc.URI)
		}
		d.Toks = lexer.Tokenize(text)
		d.Target = project.ResolveTarget(tin, project.AsmLevelComment(d.Toks))
		if l.o.toolchain && hasFlags {
			d.Macros, d.Listing, gas = l.assemble(f, flags)
		}
	}
	res := project.Lint(d)
	items := make([]item, 0, len(res.Findings)+len(gas))
	shadow := map[int]bool{}
	for _, fd := range res.Findings {
		items = append(items, item{file: f, doc: doc, source: lint.Source, Finding: fd})
		if fd.Severity.AtLeast(lint.Warning) {
			for ln := fd.Range.Start.Line; ln <= fd.Range.End.Line; ln++ {
				shadow[ln] = true
			}
		}
	}
	// As in the editor (roadmap item 78): gas on a line falcon-asm already
	// flags is the same problem told with less context.
	for _, g := range gas {
		if shadow[g.Range.Start.Line] {
			continue
		}
		code := g.Code
		if code == "" {
			code = toolchain.Source
		}
		items = append(items, item{file: f, doc: doc, source: toolchain.Source, Finding: lint.Finding{
			Code: code, Severity: lint.Severity(g.Severity), Range: g.Range, Message: g.Message,
		}})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Range.Start, items[j].Range.Start
		return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
	})
	return items
}

// assemble runs the toolchain for a file: its macro table (which the parse
// uses), the listing (rules that need one) and gas's own diagnostics.
func (l *linter) assemble(f *lintFile, flags toolchain.Flags) (map[string]string, *toolchain.AsmResult, []protocol.Diagnostic) {
	ctx := context.Background()
	var macros map[string]string
	m, err := l.tc.Macros(ctx, f.abs, flags)
	switch {
	case err == nil:
		macros = make(map[string]string, len(m))
		for k, v := range m {
			macros[k] = v.Value
		}
	case errors.Is(err, toolchain.ErrNoToolchain):
		l.noteOnce("notc", "--toolchain: %v; gas diagnostics are off", err)
		return nil, nil, nil
	case !errors.Is(err, toolchain.ErrNotAssembly):
		l.noteOnce("macros:"+err.Error(), "%s: macros: %v", f.display, err)
	}
	res, err := l.tc.Assemble(ctx, f.abs, flags)
	if err != nil {
		if !errors.Is(err, toolchain.ErrNotAssembly) && !errors.Is(err, toolchain.ErrNoToolchain) {
			l.noteOnce("asm:"+err.Error(), "%s: assemble: %v", f.display, err)
		}
		return macros, nil, nil
	}
	return macros, res, res.Diagnostics[f.abs]
}

// applyFixes applies the first fix of every item that has one, skipping a
// fix whose edits overlap one already taken (two rules rewriting the same
// operand), and returns the new text and how many fixes went in. Ignore
// comments are never applied: they are not fixes, and a bulk run that
// silenced everything would defeat the linter.
func applyFixes(text string, items []item) (string, int) {
	type edit struct {
		start, end int
		text       string
	}
	doc := &docstore.Document{Text: text}
	var taken []edit
	n := 0
next:
	for _, it := range items {
		if len(it.Fixes) == 0 {
			continue
		}
		var edits []edit
		for _, e := range it.Fixes[0].Edits {
			s, err1 := doc.Offset(e.Range.Start)
			t, err2 := doc.Offset(e.Range.End)
			if err1 != nil || err2 != nil || t < s {
				continue next
			}
			edits = append(edits, edit{s, t, e.NewText})
		}
		for _, a := range edits {
			for _, b := range taken {
				// Overlapping ranges conflict, and so do two insertions at
				// one point, whose order would be arbitrary.
				if a.start < b.end && b.start < a.end || a.start == b.start {
					continue next
				}
			}
		}
		taken = append(taken, edits...)
		n++
	}
	sort.Slice(taken, func(i, j int) bool { return taken[i].start > taken[j].start })
	for _, e := range taken {
		text = text[:e.start] + e.text + text[e.end:]
	}
	return text, n
}
