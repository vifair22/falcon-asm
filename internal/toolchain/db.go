// Package toolchain runs the project's real compiler over assembly files:
// it reads compile_commands.json for each file's flags, asks the
// preprocessor for macro definitions, and assembles into a private temp
// directory to collect assembler errors, a listing (offsets and bytes per
// source line) and the lines the preprocessor removed. Everything here is
// best effort; a missing database or compiler is reported once by the
// caller and never shown to the user as an error.
package toolchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// DBFile is the conventional compile database name.
const DBFile = "compile_commands.json"

// DB is a parsed compile database. It is immutable after LoadDB and safe
// for concurrent use.
type DB struct {
	// Root is the project directory the database describes: the directory
	// holding it, or the parent when it lives in root/build.
	Root string
	// Path is the compile_commands.json that was read.
	Path    string
	entries []entry
	byFile  map[string]int
}

type entry struct {
	dir  string
	file string // absolute and cleaned
	argv []string
}

type rawEntry struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
}

// LoadDB reads compile_commands.json from root, or from root/build when the
// build system writes it there (meson, out-of-tree CMake).
func LoadDB(root string) (*DB, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("load compile database: %w", err)
	}
	for _, p := range []string{
		filepath.Join(abs, DBFile),
		filepath.Join(abs, "build", DBFile),
	} {
		db, err := readDB(p, abs)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return db, err
	}
	return nil, fmt.Errorf("load compile database under %s: %w", abs, fs.ErrNotExist)
}

// LoadDBFile reads a database the user named explicitly. A directory is
// searched the way LoadDB searches a root. Root is the file's directory, or
// its parent when that directory is named build, matching LoadDB.
func LoadDBFile(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("load compile database: %w", err)
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		return LoadDB(abs)
	}
	root := filepath.Dir(abs)
	if filepath.Base(root) == "build" {
		root = filepath.Dir(root)
	}
	return readDB(abs, root)
}

// FindDB walks up from file's directory to the filesystem root and loads the
// first database found, which is how a file opened outside any workspace
// folder still gets its project's flags.
func FindDB(file string) (*DB, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("find compile database: %w", err)
	}
	dir := filepath.Dir(abs)
	for {
		db, err := LoadDB(dir)
		if err == nil {
			return db, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("find compile database above %s: %w", abs, fs.ErrNotExist)
		}
		dir = parent
	}
}

func readDB(path, root string) (*DB, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw []rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	db := &DB{Root: root, Path: path, byFile: make(map[string]int, len(raw))}
	for _, r := range raw {
		argv := r.Arguments
		if len(argv) == 0 {
			// A malformed command line spoils one entry, not the database.
			if argv, err = SplitCommand(r.Command); err != nil {
				continue
			}
		}
		if len(argv) == 0 || r.File == "" {
			continue
		}
		dir := r.Directory
		if dir == "" {
			dir = filepath.Dir(path)
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(filepath.Dir(path), dir)
		}
		file := r.File
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		e := entry{dir: filepath.Clean(dir), file: filepath.Clean(file), argv: argv}
		// Later duplicates win, matching what clangd does with a file that is
		// built twice: the last command is usually the most specific.
		if i, ok := db.byFile[e.file]; ok {
			db.entries[i] = e
			continue
		}
		db.byFile[e.file] = len(db.entries)
		db.entries = append(db.entries, e)
	}
	return db, nil
}

// Len is the number of usable entries.
func (db *DB) Len() int { return len(db.entries) }

// Flags returns the flags for file. An exact entry wins; otherwise the
// flags are borrowed, first from another entry in the same directory, then
// from any entry under the same top-level directory of Root, preferring an
// entry of the same kind (assembly or C) and the closest directory. A
// borrowed result has Exact false. Headers and .inc files are never in a
// database, and borrowing is what gives them include paths and a mode.
func (db *DB) Flags(file string) (Flags, bool) {
	if db == nil {
		return Flags{}, false
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return Flags{}, false
	}
	abs = filepath.Clean(abs)
	if i, ok := db.byFile[abs]; ok {
		return newFlags(db.entries[i], abs, true), true
	}
	dir := filepath.Dir(abs)
	if e, ok := db.pick(abs, func(e entry) bool { return filepath.Dir(e.file) == dir }); ok {
		return newFlags(e, abs, false), true
	}
	rel, err := filepath.Rel(db.Root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return Flags{}, false
	}
	top, _, found := strings.Cut(filepath.ToSlash(rel), "/")
	if !found {
		// A file directly in Root has no top-level subdirectory to share.
		return Flags{}, false
	}
	prefix := filepath.Join(db.Root, top) + string(filepath.Separator)
	if e, ok := db.pick(abs, func(e entry) bool { return strings.HasPrefix(e.file, prefix) }); ok {
		return newFlags(e, abs, false), true
	}
	return Flags{}, false
}

// pick chooses deterministically among candidates: same kind of source
// first, then the longest shared directory prefix, then path order.
func (db *DB) pick(file string, match func(entry) bool) (entry, bool) {
	var cands []entry
	for _, e := range db.entries {
		if match(e) {
			cands = append(cands, e)
		}
	}
	if len(cands) == 0 {
		return entry{}, false
	}
	asm := isAsmPath(file)
	sort.SliceStable(cands, func(i, j int) bool {
		ai, aj := isAsmPath(cands[i].file) == asm, isAsmPath(cands[j].file) == asm
		if ai != aj {
			return ai
		}
		ci, cj := commonPrefix(cands[i].file, file), commonPrefix(cands[j].file, file)
		if ci != cj {
			return ci > cj
		}
		return cands[i].file < cands[j].file
	})
	return cands[0], true
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// isAsmPath treats headers and .inc as assembly-ish: in an assembly project
// an include is far more often pulled into a .S than into C, and the only
// cost of guessing wrong is borrowing a C entry's include paths instead.
func isAsmPath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".s", ".sx", ".asm", ".inc", ".h":
		return true
	}
	return false
}

// Flags is what falcon-asm needs from one compile command.
type Flags struct {
	Compiler string   // argv[0], resolved against Dir when it is a relative path
	Args     []string // filtered (see filterArgs)
	Dir      string   // working directory of the command
	File     string   // the file the flags were asked for (absolute)
	Exact    bool     // true when the database has an entry for File itself

	Mode        int       // 16, 32 or 64
	Level       isa.Level // from -march; 0 when absent or not an x86-64-vN name
	LevelSource string    // the flag Level came from, e.g. "-march=x86-64-v3"

	includeDirs []string // quoted-only (-iquote), then -I, -isystem, -idirafter, in order
	quoteDirs   int      // how many leading includeDirs are -iquote only
}

func newFlags(e entry, file string, exact bool) Flags {
	f := Flags{Compiler: e.argv[0], Dir: e.dir, File: file, Exact: exact}
	if strings.ContainsRune(f.Compiler, filepath.Separator) && !filepath.IsAbs(f.Compiler) {
		f.Compiler = filepath.Join(e.dir, f.Compiler)
	}
	f.Args = filterArgs(e.argv[1:])
	f.Mode = modeFromArgs(f.Compiler, f.Args)
	f.Level, f.LevelSource = levelFromArgs(f.Args)
	f.includeDirs, f.quoteDirs = includeDirsFromArgs(f.Args, e.dir)
	return f
}

// pairOpts take their value as the next argument when not joined.
var pairOpts = map[string]bool{
	"-I": true, "-iquote": true, "-isystem": true, "-idirafter": true,
	"-D": true, "-U": true, "-include": true, "-imacros": true,
	"-Xassembler": true, "-Xpreprocessor": true, "-target": true,
	"-isysroot": true, "-iprefix": true, "-iwithprefix": true,
}

// dropPairOpts are removed together with their value: outputs, dependency
// files and the language switch, which the Runner sets itself.
var dropPairOpts = map[string]bool{
	"-o": true, "-MF": true, "-MT": true, "-MQ": true, "-x": true,
	"-Xlinker": true, "-L": true, "-l": true,
}

// filterArgs keeps the options that change what the preprocessor or the
// assembler sees (include paths, defines, target and mode, -Wa, pass
// through, -f and -O because they set predefined macros such as __PIC__ and
// __OPTIMIZE__) and drops everything that names outputs, sources or
// warnings, so the Runner can put its own -c/-E/-o/-x in their place.
func filterArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case dropPairOpts[a]:
			i++
		case pairOpts[a]:
			if i+1 < len(args) {
				out = append(out, a, args[i+1])
			}
			i++
		case !strings.HasPrefix(a, "-"):
			// A source file or a stray positional.
		case a == "-c", a == "-S", a == "-E", a == "-M", a == "-MM", a == "-MD",
			a == "-MMD", a == "-MP", a == "-MG", a == "-pipe", a == "-save-temps":
		case strings.HasPrefix(a, "-o"), strings.HasPrefix(a, "-MF"),
			strings.HasPrefix(a, "-MT"), strings.HasPrefix(a, "-MQ"),
			strings.HasPrefix(a, "-x"), strings.HasPrefix(a, "-l"),
			strings.HasPrefix(a, "-L"), strings.HasPrefix(a, "-Wl,"):
		case strings.HasPrefix(a, "-Wa,"), strings.HasPrefix(a, "-Wp,"):
			out = append(out, a)
		case strings.HasPrefix(a, "-W"), strings.HasPrefix(a, "-g"),
			strings.HasPrefix(a, "-fdiagnostics"):
		case strings.HasPrefix(a, "-I"), strings.HasPrefix(a, "-D"),
			strings.HasPrefix(a, "-U"), strings.HasPrefix(a, "-m"),
			strings.HasPrefix(a, "-std="), strings.HasPrefix(a, "-f"),
			strings.HasPrefix(a, "-O"), strings.HasPrefix(a, "-iquote"),
			strings.HasPrefix(a, "-isystem"), strings.HasPrefix(a, "-idirafter"),
			strings.HasPrefix(a, "--target="), strings.HasPrefix(a, "--sysroot"),
			a == "-nostdinc", a == "-undef", a == "-ansi", a == "-pthread":
			out = append(out, a)
		}
	}
	return out
}

// modeFromArgs: the last -m16/-m32/-m64/-mx32 wins, as with gcc; without
// one, the compiler's own default, which a cross triple (i686-elf-gcc)
// reveals and a native compiler on an x86-64 host does not.
func modeFromArgs(compiler string, args []string) int {
	mode := 0
	for _, a := range args {
		switch a {
		case "-m16":
			mode = 16
		case "-m32":
			mode = 32
		case "-m64", "-mx32":
			mode = 64
		}
	}
	if mode != 0 {
		return mode
	}
	base := strings.ToLower(filepath.Base(compiler))
	for _, p := range []string{"i386-", "i486-", "i586-", "i686-", "i786-"} {
		if strings.HasPrefix(base, p) {
			return 32
		}
	}
	return 64
}

// levelFromArgs maps the last -march to an x86-64 level. Only the psABI
// level names count: a CPU name such as -march=haswell implies a level, but
// guessing CPU feature sets is out of scope, and -march=native depends on
// the build host.
func levelFromArgs(args []string) (isa.Level, string) {
	var lvl isa.Level
	src := ""
	for _, a := range args {
		v, ok := strings.CutPrefix(a, "-march=")
		if !ok {
			continue
		}
		src = a
		switch v {
		case "x86-64":
			lvl = 1
		case "x86-64-v2":
			lvl = 2
		case "x86-64-v3":
			lvl = 3
		case "x86-64-v4":
			lvl = 4
		default:
			lvl = 0
		}
	}
	if lvl == 0 {
		return 0, ""
	}
	return lvl, src
}

// includeDirsFromArgs returns the search list in gcc's order: -iquote dirs
// (quoted includes only), then -I, then -isystem, then -idirafter. gcc
// orders by option kind, not by position across kinds.
func includeDirsFromArgs(args []string, dir string) ([]string, int) {
	kinds := map[string][]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, k := range []string{"-iquote", "-isystem", "-idirafter", "-I"} {
			v, ok := strings.CutPrefix(a, k)
			if !ok {
				continue
			}
			if v == "" {
				if i+1 >= len(args) {
					break
				}
				i++
				v = args[i]
			}
			if !filepath.IsAbs(v) {
				v = filepath.Join(dir, v)
			}
			kinds[k] = append(kinds[k], filepath.Clean(v))
			break
		}
	}
	var out []string
	out = append(out, kinds["-iquote"]...)
	q := len(out)
	out = append(out, kinds["-I"]...)
	out = append(out, kinds["-isystem"]...)
	out = append(out, kinds["-idirafter"]...)
	return out, q
}

// IncludeDirs is the absolute search list for a quoted include from File:
// File's own directory first, then the -iquote, -I, -isystem and
// -idirafter directories, without duplicates.
func (f Flags) IncludeDirs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if f.File != "" {
		add(filepath.Dir(f.File))
	}
	for _, d := range f.includeDirs {
		add(d)
	}
	return out
}

// ResolveInclude finds the file an #include in fromFile names, using cpp's
// search order: for "name" the including file's directory and the -iquote
// directories first; for <name> only -I, -isystem and -idirafter. The
// compiler's built-in system directories are not searched, since nothing an
// assembly project includes lives there.
func (f Flags) ResolveInclude(fromFile, name string, angled bool) (string, bool) {
	if name == "" {
		return "", false
	}
	if filepath.IsAbs(name) {
		return name, isFile(name)
	}
	var dirs []string
	if !angled {
		if fromFile != "" {
			dirs = append(dirs, filepath.Dir(fromFile))
		}
		dirs = append(dirs, f.includeDirs...)
	} else {
		dirs = f.includeDirs[f.quoteDirs:]
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if isFile(p) {
			return p, true
		}
	}
	return "", false
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
