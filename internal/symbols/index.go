package symbols

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// ErrRefused wraps every reason a rename is declined, so the server can
// tell a user-facing refusal from an internal failure.
var ErrRefused = errors.New("rename refused")

// Scan limits. The file cap keeps a root pointed at a home directory or a
// kernel tree from stalling the first request; the rescan interval keeps a
// burst of requests from walking the tree once each.
const (
	defaultMaxFiles    = 5000
	defaultRescanEvery = 5 * time.Second
)

// skipDirs are never descended into: build output holds generated copies of
// sources, and the others are not source at all.
var skipDirs = map[string]bool{"build": true, ".git": true, "node_modules": true}

// diskFile is a file read from disk: a workspace source or an include.
type diskFile struct {
	f    *file
	mod  time.Time
	size int64
	root string // the root that found it; "" for includes
}

// Index is the workspace symbol index. It is safe for concurrent use.
//
// Parsed files are immutable, so a query copies the file pointers under the
// read lock and does its work without holding it.
type Index struct {
	read func(path string) (string, error)

	mu    sync.RWMutex
	open  map[string]*file     // by key
	disk  map[string]*diskFile // by path
	roots map[string]time.Time // root -> last scan; zero means never

	scanMu      sync.Mutex // one tree walk at a time
	maxFiles    int
	rescanEvery time.Duration
}

// NewIndex returns an empty index. read loads a file that is not open in
// the editor (includes and workspace sources); nil means os.ReadFile.
func NewIndex(read func(path string) (string, error)) *Index {
	if read == nil {
		read = func(p string) (string, error) {
			b, err := os.ReadFile(p)
			return string(b), err
		}
	}
	return &Index{
		read:        read,
		open:        make(map[string]*file),
		disk:        make(map[string]*diskFile),
		roots:       make(map[string]time.Time),
		maxFiles:    defaultMaxFiles,
		rescanEvery: defaultRescanEvery,
	}
}

// Update replaces an open document's text and the absolute paths of the
// headers it includes (resolved by the caller through the toolchain include
// directories). Headers are read now, so queries never block on them.
func (ix *Index) Update(uri, text string, includes []string) {
	f := parse(uri, text, isCHeader(uriKey(uri)))
	f.includes = make([]string, 0, len(includes))
	for _, p := range includes {
		f.includes = append(f.includes, filepath.Clean(p))
	}
	ix.mu.Lock()
	ix.open[f.key] = f
	ix.mu.Unlock()
	for _, p := range f.includes {
		ix.loadDisk(p, "")
	}
}

// Remove forgets an open document. A copy on disk under a root is still
// indexed from disk on the next scan.
func (ix *Index) Remove(uri string) {
	ix.mu.Lock()
	delete(ix.open, uriKey(uri))
	ix.mu.Unlock()
}

// AddRoot registers a workspace folder. It is walked lazily, on the first
// query that needs workspace symbols.
func (ix *Index) AddRoot(dir string) {
	dir = filepath.Clean(dir)
	ix.mu.Lock()
	if _, ok := ix.roots[dir]; !ok {
		ix.roots[dir] = time.Time{}
	}
	ix.mu.Unlock()
}

// loadDisk reads path unless the cached copy has the same mtime and size.
// A file that cannot be stat'ed (a test's virtual read func, a vanished
// file) is read when not cached and otherwise left as cached.
func (ix *Index) loadDisk(path, root string) {
	st, statErr := os.Stat(path)
	ix.mu.RLock()
	cur, cached := ix.disk[path]
	ix.mu.RUnlock()
	if cached {
		if statErr != nil || (cur.mod.Equal(st.ModTime()) && cur.size == st.Size()) {
			return
		}
	}
	text, err := ix.read(path)
	if err != nil {
		return
	}
	d := &diskFile{f: parse(pathURI(path), text, isCHeader(path)), root: root}
	if statErr == nil {
		d.mod, d.size = st.ModTime(), st.Size()
	}
	ix.mu.Lock()
	if cur, ok := ix.disk[path]; ok && cur.root != "" && root == "" {
		d.root = cur.root
	}
	ix.disk[path] = d
	ix.mu.Unlock()
}

// ensureScanned walks every root whose last walk is older than the rescan
// interval, re-reading only files whose mtime or size changed and dropping
// ones that disappeared.
func (ix *Index) ensureScanned() {
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()
	ix.mu.RLock()
	var due []string
	now := time.Now()
	for r, last := range ix.roots {
		if last.IsZero() || now.Sub(last) >= ix.rescanEvery {
			due = append(due, r)
		}
	}
	ix.mu.RUnlock()
	sort.Strings(due)
	for _, r := range due {
		ix.scanRoot(r)
	}
}

func (ix *Index) scanRoot(root string) {
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory should not end the walk.
			if d != nil && d.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext == ".S" || ext == ".s" {
			paths = append(paths, filepath.Clean(p))
			if len(paths) >= ix.maxFiles {
				return fs.SkipAll
			}
		}
		return nil
	})
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
		ix.loadDisk(p, root)
	}
	ix.mu.Lock()
	for p, d := range ix.disk {
		if d.root == root && !seen[p] {
			delete(ix.disk, p)
		}
	}
	ix.roots[root] = time.Now()
	ix.mu.Unlock()
}

// view is a consistent set of files for one query: open documents first,
// then disk files an open document does not shadow.
type view struct {
	files []*file
	byKey map[string]*file
}

func (ix *Index) snapshot() *view {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	v := &view{byKey: make(map[string]*file, len(ix.open)+len(ix.disk))}
	for k, f := range ix.open {
		v.byKey[k] = f
	}
	for k, d := range ix.disk {
		if _, ok := v.byKey[k]; !ok {
			v.byKey[k] = d.f
		}
	}
	keys := make([]string, 0, len(v.byKey))
	for k := range v.byKey {
		keys = append(keys, k)
	}
	// Sorted so results are stable across calls, which tests and users
	// both notice.
	sort.Strings(keys)
	for _, k := range keys {
		v.files = append(v.files, v.byKey[k])
	}
	return v
}

// includesFile reports whether f includes hdr: either the caller resolved
// it for f, or f has an #include whose name is a path suffix of hdr (the
// only evidence there is for files read from disk, which have no resolved
// include list).
func (f *file) includesFile(hdr *file) bool {
	for _, p := range f.includes {
		if p == hdr.key {
			return true
		}
	}
	h := filepath.ToSlash(hdr.key)
	for _, n := range f.incNames {
		if h == n || strings.HasSuffix(h, "/"+n) {
			return true
		}
	}
	return false
}

// scope is how far a symbol's references reach.
type scope int

const (
	scopeFile   scope = iota // a non-.globl label, .set or macro in a source file
	scopeHeader              // defined in a header: the header and its includers
	scopeGlobal              // a .globl label, or an unresolved name
)

// target is the symbol a query position names.
type target struct {
	name  string
	home  *file // where it is defined; the queried file when unresolved
	defs  []protocol.Location
	kind  symKind
	scope scope

	local  bool // numeric local label
	defTok int  // its LocalLabelDef token, or -1 when dangling
}

// lookup resolves name as seen from f: f's own definitions, then the
// headers f includes, then .globl labels anywhere.
func (v *view) lookup(f *file, name string) (*target, bool) {
	if t, ok := defsIn(f, name); ok {
		if t.kind == kindLabel && f.globals[name] {
			t.scope = scopeGlobal
		}
		return t, true
	}
	for _, h := range v.files {
		if h == f || !f.includesFile(h) {
			continue
		}
		if t, ok := defsIn(h, name); ok {
			t.scope = scopeHeader
			if t.kind == kindLabel && h.globals[name] {
				t.scope = scopeGlobal
			}
			return t, true
		}
	}
	var t *target
	for _, g := range v.files {
		if !g.globals[name] {
			continue
		}
		for _, di := range g.byName[name] {
			d := g.defs[di]
			if d.kind != kindLabel {
				continue
			}
			if t == nil {
				t = &target{name: name, home: g, kind: kindLabel, scope: scopeGlobal}
			}
			t.defs = append(t.defs, g.loc(d.at))
		}
	}
	return t, t != nil
}

func defsIn(f *file, name string) (*target, bool) {
	idx := f.byName[name]
	if len(idx) == 0 {
		return nil, false
	}
	t := &target{name: name, home: f, kind: f.defs[idx[0]].kind, scope: scopeFile}
	for _, i := range idx {
		t.defs = append(t.defs, f.loc(f.defs[i].at))
	}
	return t, true
}

// symbolName extracts the symbol a token names, or "" when the token is not
// a symbol (punctuation, a register, a real instruction, a \arg).
func symbolName(f *file, i int, col int) (string, span) {
	t := f.toks[i]
	switch t.Kind {
	case lexer.LabelDef, lexer.LabelRef:
		return t.Text, tokSpan(t)
	case lexer.Mnemonic:
		if _, _, ok := isa.Lookup(t.Text); ok || !isSymbolName(t.Text) {
			return "", span{}
		}
		return t.Text, tokSpan(t)
	case lexer.Identifier:
		if isSymbolName(t.Text) && t.Text != "." {
			return t.Text, tokSpan(t)
		}
	case lexer.Immediate:
		if n := t.Text[1:]; isSymbolName(n) {
			return n, span{t.Line, t.Col + 1, t.Len - 1}
		}
	case lexer.CppArg:
		for _, w := range cWords(t.Text) {
			s, e := t.Col+w.off, t.Col+w.off+len(w.word)
			if col >= s && col <= e && w.word != "defined" {
				return w.word, span{t.Line, s, len(w.word)}
			}
		}
	}
	return "", span{}
}

// resolve finds the symbol at pos in the document uri.
func (ix *Index) resolve(uri string, pos protocol.Position) (*view, *file, *target, span, bool) {
	ix.ensureScanned()
	v := ix.snapshot()
	f, ok := v.byKey[uriKey(uri)]
	if !ok {
		return nil, nil, nil, span{}, false
	}
	line, col, ok := f.byteCol(pos)
	if !ok {
		return nil, nil, nil, span{}, false
	}
	i := tokenAt(f.toks, line, col)
	if i < 0 {
		return nil, nil, nil, span{}, false
	}
	tok := f.toks[i]
	if tok.Kind == lexer.LocalLabelRef || tok.Kind == lexer.LocalLabelDef {
		t := &target{name: localName(tok), home: f, local: true, defTok: -1}
		if d, ok := ResolveLocal(f.toks, i); ok {
			t.defTok = d
			t.defs = []protocol.Location{f.loc(tokSpan(f.toks[d]))}
		}
		return v, f, t, tokSpan(tok), true
	}
	name, at := symbolName(f, i, col)
	if name == "" {
		return nil, nil, nil, span{}, false
	}
	if t, ok := v.lookup(f, name); ok {
		return v, f, t, at, true
	}
	return v, f, &target{name: name, home: f, scope: scopeGlobal}, at, true
}

func localName(t lexer.Token) string {
	if t.Kind == lexer.LocalLabelRef {
		return t.Text[:len(t.Text)-1]
	}
	return t.Text
}

// Definition returns where the symbol at pos is defined: a local numeric
// label by token order in this document, anything else through lookup.
// An unresolved name returns nil.
func (ix *Index) Definition(uri string, pos protocol.Position) []protocol.Location {
	_, _, t, _, ok := ix.resolve(uri, pos)
	if !ok {
		return nil
	}
	return t.defs
}

// References returns every occurrence of the symbol at pos, definitions
// included when includeDecl is set. See refs for the scope rules.
func (ix *Index) References(uri string, pos protocol.Position, includeDecl bool) []protocol.Location {
	v, f, t, _, ok := ix.resolve(uri, pos)
	if !ok {
		return nil
	}
	return v.refs(f, t, includeDecl)
}

// refs collects occurrences in scope order. A file that defines the name
// itself without exporting it has its own symbol and is left out of a
// header or global search, which is what keeps every file's private "puts"
// apart.
func (v *view) refs(f *file, t *target, includeDecl bool) []protocol.Location {
	if t.local {
		return localRefs(f, t, includeDecl)
	}
	isDecl := make(map[protocol.Location]bool, len(t.defs))
	for _, d := range t.defs {
		isDecl[d] = true
	}
	var out []protocol.Location
	add := func(g *file) {
		for _, s := range g.occs[t.name] {
			l := g.loc(s)
			if !includeDecl && isDecl[l] {
				continue
			}
			out = append(out, l)
		}
	}
	switch t.scope {
	case scopeFile:
		add(t.home)
	case scopeHeader:
		for _, g := range v.files {
			if g == t.home || (g.includesFile(t.home) && len(g.byName[t.name]) == 0) {
				add(g)
			}
		}
	case scopeGlobal:
		for _, g := range v.files {
			if len(g.byName[t.name]) > 0 && !g.globals[t.name] {
				continue
			}
			add(g)
		}
	}
	return out
}

func localRefs(f *file, t *target, includeDecl bool) []protocol.Location {
	if t.defTok < 0 {
		return nil
	}
	var out []protocol.Location
	for i, tok := range f.toks {
		switch tok.Kind {
		case lexer.LocalLabelDef:
			if i == t.defTok && includeDecl {
				out = append(out, f.loc(tokSpan(tok)))
			}
		case lexer.LocalLabelRef:
			if d, ok := ResolveLocal(f.toks, i); ok && d == t.defTok {
				out = append(out, f.loc(tokSpan(tok)))
			}
		}
	}
	return out
}

// PrepareRename checks that the symbol at pos can be renamed and returns
// the range to edit and its current text as the placeholder.
func (ix *Index) PrepareRename(uri string, pos protocol.Position) (protocol.Range, string, error) {
	_, f, t, at, ok := ix.resolve(uri, pos)
	if !ok {
		return protocol.Range{}, "", fmt.Errorf("%w: no symbol at this position", ErrRefused)
	}
	if err := renamable(t); err != nil {
		return protocol.Range{}, "", err
	}
	return f.rng(at), t.name, nil
}

func renamable(t *target) error {
	if t.local {
		return fmt.Errorf("%w: local numeric label %s cannot be renamed", ErrRefused, t.name)
	}
	if len(t.defs) == 0 {
		return fmt.Errorf("%w: %s is not defined in the workspace", ErrRefused, t.name)
	}
	return nil
}

// Rename edits every reference to the symbol at pos (see References for
// scope) to newName. It refuses local numeric labels, symbols with no
// definition in the index (renaming an extern would break the file that
// defines it), new names that are not valid symbols or that read as an
// instruction, prefix, register or directive, and new names already defined
// where the symbol is visible.
func (ix *Index) Rename(uri string, pos protocol.Position, newName string) (*protocol.WorkspaceEdit, error) {
	v, f, t, _, ok := ix.resolve(uri, pos)
	if !ok {
		return nil, fmt.Errorf("%w: no symbol at this position", ErrRefused)
	}
	if err := renamable(t); err != nil {
		return nil, err
	}
	if err := validName(newName, t.kind); err != nil {
		return nil, err
	}
	edit := &protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{}}
	if newName == t.name {
		return edit, nil
	}
	if _, clash := v.lookup(t.home, newName); clash {
		return nil, fmt.Errorf("%w: %s is already defined", ErrRefused, newName)
	}
	if _, clash := v.lookup(f, newName); clash {
		return nil, fmt.Errorf("%w: %s is already defined", ErrRefused, newName)
	}
	for _, l := range v.refs(f, t, true) {
		edit.Changes[l.URI] = append(edit.Changes[l.URI], protocol.TextEdit{Range: l.Range, NewText: newName})
	}
	// A file that receives edits and already has its own newName would
	// silently rebind its references to that one.
	for _, g := range v.files {
		if _, edited := edit.Changes[g.uri]; edited && len(g.byName[newName]) > 0 {
			return nil, fmt.Errorf("%w: %s is already defined in %s", ErrRefused, newName, g.uri)
		}
	}
	return edit, nil
}

// validName rejects names GAS would not read back as the same symbol. A
// mnemonic or prefix name would be parsed as an instruction in mnemonic
// position, and a register name collides in Intel-syntax or %-less contexts
// and is confusing everywhere else.
func validName(name string, kind symKind) error {
	if kind == kindDefine {
		if !isCIdent(name) {
			return fmt.Errorf("%w: %q is not a valid macro name", ErrRefused, name)
		}
	} else if !isSymbolName(name) {
		return fmt.Errorf("%w: %q is not a valid symbol name", ErrRefused, name)
	}
	if _, _, ok := isa.Lookup(name); ok {
		return fmt.Errorf("%w: %q is an instruction mnemonic", ErrRefused, name)
	}
	if isa.IsPrefix(name) {
		return fmt.Errorf("%w: %q is an instruction prefix", ErrRefused, name)
	}
	if _, ok := isa.LookupRegister(name); ok {
		return fmt.Errorf("%w: %q is a register name", ErrRefused, name)
	}
	if strings.HasPrefix(name, ".") {
		if _, ok := isa.LookupDirective(name); ok {
			return fmt.Errorf("%w: %q is a directive", ErrRefused, name)
		}
	}
	return nil
}

// Labels lists label and .set/.equ names for operand completion: every one
// defined in an open document plus every .globl label in the index. Private
// labels of files that are not open are left out; they cannot be referenced
// from anywhere else.
func (ix *Index) Labels() []string {
	ix.ensureScanned()
	v := ix.snapshot()
	ix.mu.RLock()
	open := make(map[*file]bool, len(ix.open))
	for _, f := range ix.open {
		open[f] = true
	}
	ix.mu.RUnlock()
	set := map[string]bool{}
	for _, f := range v.files {
		for _, d := range f.defs {
			if d.kind != kindLabel && d.kind != kindEqu {
				continue
			}
			if open[f] || (d.kind == kindLabel && f.globals[d.name]) {
				set[d.name] = true
			}
		}
	}
	return sortedKeys(set)
}

// Macros lists #define and .macro names visible from open documents: their
// own and those of headers they include.
func (ix *Index) Macros() []string {
	v := ix.snapshot()
	ix.mu.RLock()
	var open []*file
	for _, f := range ix.open {
		open = append(open, f)
	}
	ix.mu.RUnlock()
	set := map[string]bool{}
	addFrom := func(f *file) {
		for _, d := range f.defs {
			if d.kind == kindDefine || d.kind == kindAsmMacro {
				set[d.name] = true
			}
		}
	}
	for _, f := range open {
		addFrom(f)
		for _, h := range v.files {
			if h != f && f.includesFile(h) {
				addFrom(h)
			}
		}
	}
	return sortedKeys(set)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
