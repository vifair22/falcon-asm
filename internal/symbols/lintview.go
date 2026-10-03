package symbols

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// maxIncludeDepth bounds how deep includes of files read from disk are
// followed; cpp's own limit is 200, but nothing real nests past a handful.
const maxIncludeDepth = 32

// wsState is the part of the index that serves the lint rules' views: the
// include resolver for files read from disk, and the words of the C
// sources and linker scripts under the roots. Guarded by Index.mu.
type wsState struct {
	gen     uint64 // bumps whenever a disk file is added, replaced or dropped
	resolve func(from, name string, angled bool) (string, bool)

	words    map[string]*wordFile // C sources and linker scripts, by path
	wordsGen uint64
	agg      *wordAgg
}

// wordFile is a C source or a linker script reduced to the identifiers it
// mentions. Neither is parsed: C definitions would need a C parser, and a
// symbol mentioned at all in either is reason enough not to call it
// undefined or unused.
type wordFile struct {
	words []string // unique, cloned from the text so the text can be freed
	ld    bool     // a linker script
	mod   time.Time
	size  int64
	root  string
}

// wordAgg is the union of every word file's words, rebuilt when a word
// file changes rather than per query.
type wordAgg struct {
	gen uint64
	ext map[string]bool // every word of a C source or linker script
	ld  map[string]bool // the linker scripts' words alone
}

// SetIncludeResolver sets how #include and .include names of files read
// from disk are resolved. nil restores the default: relative to the
// including file's directory, then to each workspace root. The engine and
// the CLI resolve their own documents' includes with compile flags; this
// covers the files those include in turn and the files found by the scan.
func (ix *Index) SetIncludeResolver(fn func(from, name string, angled bool) (string, bool)) {
	ix.mu.Lock()
	ix.ws.resolve = fn
	ix.mu.Unlock()
}

// resolveIncludes resolves the include names of a file read from disk.
// Unresolved names are dropped; the name heuristic of includesFile still
// sees them.
func (ix *Index) resolveIncludes(from string, f *file) []string {
	if len(f.incNames) == 0 {
		return nil
	}
	ix.mu.RLock()
	fn := ix.ws.resolve
	roots := make([]string, 0, len(ix.roots))
	for r := range ix.roots {
		roots = append(roots, r)
	}
	ix.mu.RUnlock()
	if fn == nil {
		fn = func(from, name string, angled bool) (string, bool) {
			return defaultResolve(from, name, angled, roots)
		}
	}
	var out []string
	for i, n := range f.incNames {
		angled := i < len(f.incAngled) && f.incAngled[i]
		if p, ok := fn(from, n, angled); ok {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

func defaultResolve(from, name string, angled bool, roots []string) (string, bool) {
	if filepath.IsAbs(name) {
		return name, isRegular(name)
	}
	var dirs []string
	if !angled {
		dirs = append(dirs, filepath.Dir(from))
	}
	dirs = append(dirs, roots...)
	for _, d := range dirs {
		if p := filepath.Join(d, name); isRegular(p) {
			return p, true
		}
	}
	return "", false
}

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// cExts are the C-family sources whose words count as definitions.
var cExts = map[string]bool{
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".hh": true,
}

// isWordFile reports a C source or a linker script. A preprocessed linker
// script (jmod.ld.S) is also an assembly source to the scan; its words
// still name the symbols the link needs.
func isWordFile(p string) bool {
	return cExts[strings.ToLower(filepath.Ext(p))] || isLinkerScript(p)
}

func isLinkerScript(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	ext := filepath.Ext(b)
	if ext == ".ld" || ext == ".lds" {
		return true
	}
	stem := strings.TrimSuffix(b, ext)
	return ext == ".s" && (strings.HasSuffix(stem, ".ld") || strings.HasSuffix(stem, ".lds"))
}

// scanWords reads the word files found under root, skipping those whose
// mtime and size are unchanged, and drops the ones that disappeared.
func (ix *Index) scanWords(root string, paths []string) {
	work := make(chan string)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(paths)) {
		wg.Go(func() {
			for p := range work {
				ix.loadWords(p, root)
			}
		})
	}
	for _, p := range paths {
		work <- p
	}
	close(work)
	wg.Wait()
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
	}
	ix.mu.Lock()
	for p, w := range ix.ws.words {
		if w.root == root && !seen[p] {
			delete(ix.ws.words, p)
			ix.ws.wordsGen++
		}
	}
	ix.mu.Unlock()
}

func (ix *Index) loadWords(p, root string) {
	st, err := os.Stat(p)
	if err != nil {
		return
	}
	ix.mu.RLock()
	cur, ok := ix.ws.words[p]
	ix.mu.RUnlock()
	if ok && cur.mod.Equal(st.ModTime()) && cur.size == st.Size() {
		return
	}
	text, err := ix.read(p)
	if err != nil {
		return
	}
	w := &wordFile{words: uniqueWords(text), ld: isLinkerScript(p), mod: st.ModTime(), size: st.Size(), root: root}
	ix.mu.Lock()
	if ix.ws.words == nil {
		ix.ws.words = map[string]*wordFile{}
	}
	ix.ws.words[p] = w
	ix.ws.wordsGen++
	ix.mu.Unlock()
}

// uniqueWords returns the distinct C identifiers of text, each cloned so
// the text itself is not kept alive by the index.
func uniqueWords(text string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case isCIdentStart(c):
			j := i + 1
			for j < len(text) && isCIdentChar(text[j]) {
				j++
			}
			if w := text[i:j]; !seen[w] {
				seen[w] = true
				out = append(out, strings.Clone(w))
			}
			i = j
		case c >= '0' && c <= '9':
			// A number's suffix or hex digits are not identifiers.
			for i < len(text) && isCIdentChar(text[i]) {
				i++
			}
		default:
			i++
		}
	}
	return out
}

// words returns the aggregate of the word files, rebuilding it only when
// one changed since the last build.
func (ix *Index) words() *wordAgg {
	ix.mu.RLock()
	a := ix.ws.agg
	gen := ix.ws.wordsGen
	ix.mu.RUnlock()
	if a != nil && a.gen == gen {
		return a
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if a := ix.ws.agg; a != nil && a.gen == ix.ws.wordsGen {
		return a
	}
	a = &wordAgg{gen: ix.ws.wordsGen, ext: map[string]bool{}, ld: map[string]bool{}}
	for _, w := range ix.ws.words {
		for _, s := range w.words {
			a.ext[s] = true
			if w.ld {
				a.ld[s] = true
			}
		}
	}
	ix.ws.agg = a
	return a
}

// LintView returns the lint rules' view of the document uri (it satisfies
// lint.Workspace). It is cheap to create: the index is scanned and the
// view's tables are built on its first question, once, and every question
// after that is a map lookup or a walk over the document's include unit.
func (ix *Index) LintView(uri string) *LintView {
	return &LintView{ix: ix, key: uriKey(uri)}
}

// LintView answers lint rules' questions about the rest of the workspace
// as seen from one document. Safe for concurrent use.
type LintView struct {
	ix  *Index
	key string

	once     sync.Once
	v        *view
	f        *file             // the document; nil when the index does not have it
	edges    map[*file][]*file // file -> files it includes
	unit     []*file           // includers and includes of f, transitively, without f
	inUnit   map[*file]bool
	included map[*file]bool     // files some other file includes
	globals  map[string][]*file // name -> files defining it as a global symbol
	words    *wordAgg
}

func (w *LintView) init() {
	w.once.Do(func() {
		w.ix.ensureScanned()
		w.v = w.ix.snapshot()
		w.words = w.ix.words()
		w.f = w.v.byKey[w.key]
		w.buildEdges()
		w.buildUnit()
		w.globals = map[string][]*file{}
		for _, g := range w.v.files {
			for name := range g.globals {
				if g.definesSymbol(name) {
					w.globals[name] = append(w.globals[name], g)
				}
			}
		}
	})
}

// definesSymbol reports a label or symbol assignment of name in f, which
// is what a .globl exports.
func (f *file) definesSymbol(name string) bool {
	for _, i := range f.byName[name] {
		if k := f.defs[i].kind; k == kindLabel || k == kindEqu {
			return true
		}
	}
	return false
}

// buildEdges resolves every file's includes to files of the view: the
// resolved paths, then the names by path suffix (the only evidence for a
// file whose includes could not be resolved).
func (w *LintView) buildEdges() {
	byBase := map[string][]*file{}
	for _, g := range w.v.files {
		b := path.Base(filepath.ToSlash(g.key))
		byBase[b] = append(byBase[b], g)
	}
	w.edges = map[*file][]*file{}
	w.included = map[*file]bool{}
	for _, g := range w.v.files {
		seen := map[*file]bool{}
		add := func(h *file) {
			if h != nil && h != g && !seen[h] {
				seen[h] = true
				w.edges[g] = append(w.edges[g], h)
				w.included[h] = true
			}
		}
		for _, p := range g.includes {
			add(w.v.byKey[p])
		}
		for _, n := range g.incNames {
			for _, h := range byBase[path.Base(n)] {
				if suffixMatch(h.key, n) {
					add(h)
				}
			}
		}
	}
}

// suffixMatch reports whether the path key ends with the include name n.
func suffixMatch(key, n string) bool {
	k := filepath.ToSlash(key)
	return k == n || strings.HasSuffix(k, "/"+strings.TrimPrefix(n, "./"))
}

// buildUnit collects what is assembled together with the document: the
// files that include it (transitively), and everything those and the
// document include (transitively).
func (w *LintView) buildUnit() {
	w.inUnit = map[*file]bool{}
	if w.f == nil {
		return
	}
	rev := map[*file][]*file{}
	for g, hs := range w.edges {
		for _, h := range hs {
			rev[h] = append(rev[h], g)
		}
	}
	top := []*file{w.f}
	seen := map[*file]bool{w.f: true}
	for i := 0; i < len(top); i++ {
		for _, g := range rev[top[i]] {
			if !seen[g] {
				seen[g] = true
				top = append(top, g)
			}
		}
	}
	queue := append([]*file(nil), top...)
	for i := 0; i < len(queue); i++ {
		for _, h := range w.edges[queue[i]] {
			if !seen[h] {
				seen[h] = true
				queue = append(queue, h)
			}
		}
	}
	for _, g := range queue {
		if g != w.f {
			w.unit = append(w.unit, g)
			w.inUnit[g] = true
		}
	}
}

// Defined implements lint.Workspace.
func (w *LintView) Defined(name string) bool {
	w.init()
	for _, u := range w.unit {
		if len(u.byName[name]) > 0 {
			return true
		}
	}
	for _, g := range w.globals[name] {
		if g != w.f {
			return true
		}
	}
	return w.words.ext[name]
}

// Referenced implements lint.Workspace.
func (w *LintView) Referenced(name string) bool {
	w.init()
	for _, u := range w.unit {
		if len(u.occs[name]) > 0 {
			return true
		}
	}
	return w.words.ld[name]
}

// Complete implements lint.Workspace: every include name of the document
// matches a file the index has.
func (w *LintView) Complete() bool {
	w.init()
	if w.f == nil {
		return false
	}
next:
	for _, n := range w.f.incNames {
		for _, h := range w.edges[w.f] {
			if suffixMatch(h.key, n) {
				continue next
			}
		}
		// A resolved path need not end in the name as written
		// ("../x.inc" resolves to a cleaned path).
		for _, p := range w.f.includes {
			if filepath.Base(p) == path.Base(n) {
				continue next
			}
		}
		return false
	}
	return true
}

// GlobalDefinitions implements lint.Workspace.
func (w *LintView) GlobalDefinitions(name string) []protocol.Location {
	w.init()
	if w.f == nil {
		return nil
	}
	var out []protocol.Location
	labels := func(g *file) {
		for _, i := range g.byName[name] {
			if g.defs[i].kind == kindLabel {
				out = append(out, g.loc(g.defs[i].at))
			}
		}
	}
	for _, u := range w.unit {
		labels(u)
	}
	if w.included[w.f] || !isAsmSource(w.f.key) {
		return out
	}
	for _, g := range w.globals[name] {
		if g == w.f || w.inUnit[g] || w.included[g] || !isAsmSource(g.key) || !w.linked(w.f, g) {
			continue
		}
		labels(g)
	}
	return out
}

func isAsmSource(key string) bool {
	ext := filepath.Ext(key)
	return ext == ".S" || ext == ".s"
}

// linked reports evidence that a and b go into one link: one of them
// references a global symbol only the other defines. Two sources that
// merely share a name (every standalone tool's _start) are separate
// programs as far as anything here can tell.
func (w *LintView) linked(a, b *file) bool {
	return w.uses(a, b) || w.uses(b, a)
}

func (w *LintView) uses(a, b *file) bool {
	for name := range b.globals {
		defs := w.globals[name]
		if len(defs) == 1 && defs[0] == b && len(a.occs[name]) > 0 && len(a.byName[name]) == 0 {
			return true
		}
	}
	return false
}
