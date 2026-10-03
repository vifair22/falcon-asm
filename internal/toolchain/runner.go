package toolchain

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/pe"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// DefaultTimeout bounds one compiler invocation sequence. A boot sector
// assembles in milliseconds; anything near this is a hung or remote
// compiler wrapper, and a stale result is worse than none.
const DefaultTimeout = 10 * time.Second

var (
	// ErrNoToolchain means the compiler named by the flags is not on PATH.
	// Callers log it once and carry on without toolchain features.
	ErrNoToolchain = errors.New("compiler not found")
	// ErrSuperseded is the cancellation cause when a newer run for the same
	// file and kind started; callers drop such results silently.
	ErrSuperseded = errors.New("superseded by a newer run")
	// ErrNotAssembly is returned by Assemble for C and other non-assembly
	// sources.
	ErrNotAssembly = errors.New("not an assembly source")
)

// Runner invokes the toolchain. The zero value is ready to use. At most one
// run per (kind, file) is in flight: starting a new one cancels the old,
// since only the result for the newest save matters.
type Runner struct {
	Timeout time.Duration // 0 means DefaultTimeout

	mu      sync.Mutex
	running map[string]*runSlot
}

type runSlot struct {
	cancel context.CancelCauseFunc
}

// begin registers a run and cancels any earlier one under the same key.
func (r *Runner) begin(ctx context.Context, key string) (context.Context, func()) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithCancelCause(ctx)
	tctx, tcancel := context.WithTimeout(cctx, timeout)
	slot := &runSlot{cancel: cancel}
	r.mu.Lock()
	if r.running == nil {
		r.running = map[string]*runSlot{}
	}
	if old := r.running[key]; old != nil {
		old.cancel(ErrSuperseded)
	}
	r.running[key] = slot
	r.mu.Unlock()
	return tctx, func() {
		tcancel()
		cancel(nil)
		r.mu.Lock()
		if r.running[key] == slot {
			delete(r.running, key)
		}
		r.mu.Unlock()
	}
}

// ctxErr reports why ctx ended, preferring the cancel cause so a
// superseded run is distinguishable from a timeout.
func ctxErr(ctx context.Context) error {
	if c := context.Cause(ctx); c != nil {
		return c
	}
	return ctx.Err()
}

// Macro is one preprocessor definition visible to a file.
type Macro struct {
	Value    string
	Function bool   // function-like: NAME(params) value
	Params   string // the parameter list without parentheses, e.g. "x,y"
	// Def is where the macro is defined, when a #define for it was found in
	// the file or its includes; nil for builtins and command-line -D.
	Def *protocol.Location
}

// language picks the -x for a source, by extension: .S and .sx go through
// cpp, .s does not, and anything else is treated as C.
func language(file string) string {
	switch filepath.Ext(file) {
	case ".S", ".sx":
		return "assembler-with-cpp"
	case ".s":
		return "assembler"
	case ".cc", ".cpp", ".cxx", ".C", ".hpp", ".hh":
		return "c++"
	}
	return "c"
}

// compilerPath checks the compiler exists before anything is written, so
// the common "no cross compiler installed" case is a sentinel, not an exec
// error string.
func compilerPath(f Flags) (string, error) {
	if f.Compiler == "" {
		return "", ErrNoToolchain
	}
	p, err := exec.LookPath(f.Compiler)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNoToolchain, f.Compiler)
	}
	return p, nil
}

type execResult struct {
	stdout, stderr string
	exitErr        error // non-nil when the tool ran and exited non-zero
}

// run executes one tool invocation in f.Dir with a C locale, so message
// prefixes ("Error:", "error:") are the English ones the parsers expect.
func run(ctx context.Context, compiler string, f Flags, args []string) (execResult, error) {
	cmd := exec.CommandContext(ctx, compiler, args...)
	cmd.Dir = f.Dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	// A killed driver can leave cc1 or as holding the pipes; do not wait
	// for them beyond this.
	cmd.WaitDelay = time.Second
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	res := execResult{stdout: so.String(), stderr: se.String()}
	if ctx.Err() != nil {
		return res, fmt.Errorf("run %s: %w", filepath.Base(compiler), ctxErr(ctx))
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.exitErr = err
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("run %s: %w", filepath.Base(compiler), err)
	}
	return res, nil
}

// Macros returns every macro defined after preprocessing file with its
// flags (builtins included, since a .S file may test __x86_64__), each with
// the location of its #define when one is found by scanning file and the
// headers it includes.
func (r *Runner) Macros(ctx context.Context, file string, f Flags) (map[string]Macro, error) {
	file, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("macros: %w", err)
	}
	lang := language(file)
	if lang == "assembler" {
		// gas never sees cpp macros in a plain .s file.
		return map[string]Macro{}, nil
	}
	compiler, err := compilerPath(f)
	if err != nil {
		return nil, err
	}
	ctx, done := r.begin(ctx, "macros\x00"+file)
	defer done()
	args := append(append([]string{}, f.Args...), "-E", "-dM", "-x", lang, file)
	res, err := run(ctx, compiler, f, args)
	if err != nil {
		return nil, fmt.Errorf("macros %s: %w", file, err)
	}
	if res.exitErr != nil && res.stdout == "" {
		return nil, fmt.Errorf("macros %s: %w: %s", file, res.exitErr, firstLine(res.stderr))
	}
	macros := ParseDefines(res.stdout)
	f.File = file
	locateMacros(macros, file, f)
	return macros, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// ParseDefines reads `gcc -dM` output.
func ParseDefines(out string) map[string]Macro {
	m := map[string]Macro{}
	for _, l := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(l, "\r"), "#define ")
		if !ok {
			continue
		}
		n := 0
		for n < len(rest) && isIdentByte(rest[n], n == 0) {
			n++
		}
		if n == 0 {
			continue
		}
		name := rest[:n]
		rest = rest[n:]
		var mac Macro
		if strings.HasPrefix(rest, "(") {
			end := strings.IndexByte(rest, ')')
			if end < 0 {
				continue
			}
			mac.Function, mac.Params = true, rest[1:end]
			rest = rest[end+1:]
		}
		mac.Value = strings.TrimSpace(rest)
		m[name] = mac
	}
	return m
}

func isIdentByte(c byte, first bool) bool {
	if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
		return true
	}
	return !first && c >= '0' && c <= '9'
}

// locateMacros scans main and everything it includes (plus -include and
// -imacros files) in cpp's order and records the last #define of each
// macro, which is the one in effect afterwards. Conditionals are not
// evaluated, so a name defined in both branches of an #if points at the
// later one; that is the documented best-effort limit.
func locateMacros(macros map[string]Macro, main string, f Flags) {
	visited := map[string]bool{}
	var scan func(path string, depth int)
	scan = func(path string, depth int) {
		if depth > 32 || visited[path] {
			return
		}
		visited[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		cs := &commentState{}
		for i, raw := range strings.Split(string(data), "\n") {
			wasBlock := cs.inBlock
			code := cs.strip(strings.TrimRight(raw, "\r"))
			if wasBlock {
				continue
			}
			name, ok := directiveName(code)
			if !ok {
				continue
			}
			s := strings.TrimLeft(code, " \t")
			s = strings.TrimLeft(s[1:], " \t")
			s = strings.TrimLeft(s[len(name):], " \t")
			switch name {
			case "define":
				n := 0
				for n < len(s) && isIdentByte(s[n], n == 0) {
					n++
				}
				mac, known := macros[s[:n]]
				if n == 0 || !known {
					continue
				}
				col := strings.Index(raw, s[:n])
				if col < 0 {
					continue
				}
				prefix := raw[:col]
				start := docstore.UTF16Len(prefix)
				mac.Def = &protocol.Location{
					URI: PathToURI(path),
					Range: protocol.Range{
						Start: protocol.Position{Line: i, Character: start},
						End:   protocol.Position{Line: i, Character: start + n},
					},
				}
				macros[s[:n]] = mac
			case "include", "include_next", "import":
				if p, ok := resolveDirective(f, path, s); ok {
					scan(p, depth+1)
				}
			}
		}
	}
	for i := 0; i+1 < len(f.Args); i++ {
		if f.Args[i] == "-include" || f.Args[i] == "-imacros" {
			name := f.Args[i+1]
			p := name
			if !filepath.IsAbs(p) {
				p = filepath.Join(f.Dir, name)
			}
			if !isFile(p) {
				var ok bool
				if p, ok = f.ResolveInclude("", name, true); !ok {
					continue
				}
			}
			scan(p, 0)
		}
	}
	scan(main, 0)
}

// resolveDirective resolves the operand of an #include line; computed
// includes (#include MACRO) are skipped.
func resolveDirective(f Flags, from, operand string) (string, bool) {
	operand = strings.TrimSpace(operand)
	if len(operand) < 2 {
		return "", false
	}
	var closer byte
	switch operand[0] {
	case '"':
		closer = '"'
	case '<':
		closer = '>'
	default:
		return "", false
	}
	end := strings.IndexByte(operand[1:], closer)
	if end < 0 {
		return "", false
	}
	return f.ResolveInclude(from, operand[1:1+end], closer == '>')
}

// AsmResult is what one Assemble produced.
type AsmResult struct {
	// Diagnostics from the preprocessor and the assembler, keyed by
	// absolute file path; errors in included files are also mirrored onto
	// the main file's #include line.
	Diagnostics map[string][]protocol.Diagnostic
	Listing     []ListingLine    // main-file lines gas listed, in source order
	Inactive    []protocol.Range // main-file conditional bodies cpp removed
	// SectionSizes come from the object file when assembly succeeded; they
	// close the last padding gap of a section, which the listing cannot.
	SectionSizes map[string]uint64
}

// PadBytes is PadBytes over r.Listing, falling back to the section size
// when the location move is the last thing in its section.
func (r *AsmResult) PadBytes(line int) (int, bool) {
	return padBytes(r.Listing, line, r.SectionSizes)
}

// Assemble preprocesses file (when it is a .S) into a private temp dir,
// assembles the result through the compiler driver with a listing, and
// maps everything back to the original files. Assembler failure is a
// result with diagnostics, not an error; errors are reserved for a missing
// compiler, cancellation and timeouts, and I/O on the temp dir.
func (r *Runner) Assemble(ctx context.Context, file string, f Flags) (*AsmResult, error) {
	file, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("assemble: %w", err)
	}
	file = filepath.Clean(file)
	lang := language(file)
	if lang != "assembler-with-cpp" && lang != "assembler" {
		return nil, fmt.Errorf("assemble %s: %w", file, ErrNotAssembly)
	}
	compiler, err := compilerPath(f)
	if err != nil {
		return nil, err
	}
	ctx, done := r.begin(ctx, "assemble\x00"+file)
	defer done()

	tmp, err := os.MkdirTemp("", "falcon-asm-*")
	if err != nil {
		return nil, fmt.Errorf("assemble %s: %w", file, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	res := &AsmResult{}
	var raw []rawDiag
	tmpS := filepath.Join(tmp, "pp.s")
	asmInput := file
	var lm *LineMap
	if lang == "assembler-with-cpp" {
		// -dD keeps #define/#undef in the output (as gas comments) so
		// inactive-region detection can see which definitions survived.
		args := append(append([]string{}, f.Args...),
			"-E", "-dD", "-fdiagnostics-color=never", "-x", lang, file, "-o", tmpS)
		pp, err := run(ctx, compiler, f, args)
		if err != nil {
			return nil, fmt.Errorf("assemble %s: preprocess: %w", file, err)
		}
		raw = append(raw, parseCppStderr(pp.stderr, file, f.Dir)...)
		if pp.exitErr != nil {
			if len(raw) == 0 {
				return nil, fmt.Errorf("assemble %s: preprocess: %w: %s", file, pp.exitErr, firstLine(pp.stderr))
			}
			res.Diagnostics = toDiagnostics(raw, file)
			return res, nil
		}
		out, err := os.ReadFile(tmpS)
		if err != nil {
			return nil, fmt.Errorf("assemble %s: %w", file, err)
		}
		lm = ParseLineMap(string(out), file, f.Dir)
		if src, err := os.ReadFile(file); err == nil {
			res.Inactive = InactiveRanges(string(src), lm.Present(string(out)))
		}
		asmInput = tmpS
	} else {
		lm = identityMap(file)
	}

	obj := filepath.Join(tmp, "out.o")
	lst := filepath.Join(tmp, "out.lst")
	// -alm lists macro and .rept expansions row by row, which is what lets
	// section switches inside a macro be tracked; plain -al folds them.
	args := append(append([]string{}, f.Args...),
		"-c", "-x", "assembler", asmInput, "-o", obj, "-Wa,-alm="+lst)
	as, err := run(ctx, compiler, f, args)
	if err != nil {
		return nil, fmt.Errorf("assemble %s: %w", file, err)
	}
	gasRaw := parseGasStderr(as.stderr, tmpS, f.Dir, lm)
	var rows []rawListing
	if text, err := os.ReadFile(lst); err == nil {
		rows = parseGasListing(string(text))
		inAsm, _ := os.ReadFile(asmInput)
		via := markIncluded(rows, splitLines(string(inAsm)), lm, func(name string) (string, bool) {
			return f.ResolveGasInclude(file, name)
		})
		mirrorGasIncludes(gasRaw, via, file)
	}
	raw = append(raw, gasRaw...)
	if as.exitErr != nil && len(raw) == 0 {
		// Report something rather than a silent clean file.
		raw = append(raw, rawDiag{file: file, col: -1, sev: protocol.SeverityError,
			msg: "assembler failed: " + firstLine(as.stderr), via: -1})
	}
	res.Diagnostics = toDiagnostics(raw, file)
	if rows != nil {
		res.Listing = buildListing(rows, lm)
	}
	res.SectionSizes = sectionSizes(obj)
	return res, nil
}

// identityMap stands in for a cpp line map on a plain .s file, which gas
// reads directly: output line n is source line n.
func identityMap(file string) *LineMap {
	lm := &LineMap{Main: file, Includes: map[int]bool{}}
	data, err := os.ReadFile(file)
	if err != nil {
		return lm
	}
	n := strings.Count(string(data), "\n") + 1
	lm.Lines = make([]Origin, n)
	for i := range lm.Lines {
		lm.Lines[i] = Origin{File: file, Line: i, Via: -1}
	}
	return lm
}

// sectionSizes reads section sizes from the object, ELF or PE/COFF (the
// latter for mingw cross builds). Failure just means no sizes.
func sectionSizes(obj string) map[string]uint64 {
	if ef, err := elf.Open(obj); err == nil {
		defer func() { _ = ef.Close() }()
		m := map[string]uint64{}
		for _, s := range ef.Sections {
			if s.Name != "" {
				m[s.Name] = s.Size
			}
		}
		return m
	}
	if pf, err := pe.Open(obj); err == nil {
		defer func() { _ = pf.Close() }()
		m := map[string]uint64{}
		for _, s := range pf.Sections {
			m[s.Name] = uint64(s.Size)
		}
		return m
	}
	return nil
}

// PathToURI converts an absolute path to a file:// URI.
func PathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

// URIToPath converts a file:// URI back to a path; ok is false for other
// schemes, which have no file on disk for the compiler to read.
func URIToPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	return filepath.FromSlash(u.Path), true
}
