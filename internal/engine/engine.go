// Package engine is falcon-asm's feature orchestration: it keeps per-document
// state (language, cached token stream, analysis, inline asm statements,
// toolchain results), resolves each file's configuration from the compile
// database and initializationOptions, runs the toolchain in the background,
// publishes diagnostics, and answers the server's feature queries by calling
// the pure feature packages.
//
// Concurrency: the server calls in from one goroutine. Toolchain runs and
// debounced diagnostic publishes happen on their own goroutines. One mutex
// (mu) guards all mutable state; nothing slow (lexing, analysis, exec, I/O
// to the client) happens while it is held. Derived data lives in immutable
// snapshots (see snapshot.go), computed lazily, once per document version
// and configuration generation, and shared by every request that needs it.
package engine

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/server"
	"git.airies.net/vifair22/falcon-asm/internal/symbols"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Defaults for Config fields left zero.
const (
	// DefaultDebounce coalesces a burst of keystrokes into one analysis
	// publish; short enough to feel live, long enough that typing a word
	// does not re-analyze per character.
	DefaultDebounce = 150 * time.Millisecond
	// DefaultShutdownWait bounds how long shutdown waits for toolchain runs
	// after cancelling them; a cancelled exec normally ends in milliseconds.
	DefaultShutdownWait = 2 * time.Second
)

// Toolchain is the exec side, satisfied by *toolchain.Runner and by fakes
// in tests.
type Toolchain interface {
	Macros(ctx context.Context, file string, f toolchain.Flags) (map[string]toolchain.Macro, error)
	Assemble(ctx context.Context, file string, f toolchain.Flags) (*toolchain.AsmResult, error)
}

// FlagDB answers per-file compile flags; *toolchain.DB satisfies it.
type FlagDB interface {
	Flags(file string) (toolchain.Flags, bool)
}

// Config injects the engine's dependencies. Zero fields get the real
// implementations.
type Config struct {
	Toolchain  Toolchain
	LoadDB     func(root string) (FlagDB, error) // compile_commands.json at root or root/build
	LoadDBFile func(path string) (FlagDB, error) // an explicitly configured database
	FindDB     func(file string) (FlagDB, error) // walk up from a file
	ReadFile   func(path string) ([]byte, error)
	// ReadIndexFile loads files for the symbol index; nil means os.ReadFile.
	ReadIndexFile func(path string) (string, error)

	Debounce     time.Duration
	ShutdownWait time.Duration
	Log          io.Writer
}

// settings is the initializationOptions schema the VS Code client sends
// (editors/vscode/src/settings.ts). Absent booleans default to on, which is
// also what a client that sends nothing gets.
type settings struct {
	Toolchain       *bool  `json:"toolchain"`
	ListingHints    *bool  `json:"listingHints"`
	Level           string `json:"level"`
	CompileCommands string `json:"compileCommands"`
}

type lang int

const (
	langAsm lang = iota
	langC
)

// docState is one open document. Fields are guarded by Engine.mu.
type docState struct {
	uri  string
	path string // on-disk path; "" for untitled and other non-file URIs
	lang lang
	doc  *docstore.Document

	flags    toolchain.Flags
	hasFlags bool

	// Toolchain results: kept until the next run for this file finishes.
	macros     map[string]toolchain.Macro
	asm        *toolchain.AsmResult
	asmVersion int // document version the listing describes; -1 when unknown
	runSeq     int // newest run started; older results are dropped

	gen   int // bumps when outside knowledge (macros) changes
	snap  *snapshot
	timer *time.Timer
}

// Engine implements server.Engine.
type Engine struct {
	cfg   Config
	log   *log.Logger
	tc    Toolchain
	index *symbols.Index

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	pubMu  sync.Mutex // serializes check-and-send of diagnostics

	mu           sync.Mutex
	publishFn    func(protocol.PublishDiagnosticsParams)
	root         string
	toolchainOn  bool
	listingHints bool
	level        isa.Level
	levelSource  string
	db           FlagDB
	found        map[string]FlagDB // FindDB results by directory; nil value = none
	docs         map[string]*docState
	ext          map[string]map[string][]protocol.Diagnostic // target URI -> owner URI -> gas diagnostics
	logged       map[string]bool
	closed       bool
}

var _ server.Engine = (*Engine)(nil)

// New builds an engine; it does nothing until Initialize.
func New(cfg Config) *Engine {
	if cfg.Toolchain == nil {
		cfg.Toolchain = &toolchain.Runner{}
	}
	if cfg.LoadDB == nil {
		cfg.LoadDB = func(root string) (FlagDB, error) { return dbOrErr(toolchain.LoadDB(root)) }
	}
	if cfg.LoadDBFile == nil {
		cfg.LoadDBFile = func(p string) (FlagDB, error) { return dbOrErr(toolchain.LoadDBFile(p)) }
	}
	if cfg.FindDB == nil {
		cfg.FindDB = func(f string) (FlagDB, error) { return dbOrErr(toolchain.FindDB(f)) }
	}
	if cfg.ReadFile == nil {
		cfg.ReadFile = os.ReadFile
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = DefaultDebounce
	}
	if cfg.ShutdownWait <= 0 {
		cfg.ShutdownWait = DefaultShutdownWait
	}
	w := cfg.Log
	if w == nil {
		w = io.Discard
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		cfg:          cfg,
		log:          log.New(w, "falcon-asm: ", log.LstdFlags|log.Lmsgprefix),
		tc:           cfg.Toolchain,
		index:        symbols.NewIndex(cfg.ReadIndexFile),
		ctx:          ctx,
		cancel:       cancel,
		toolchainOn:  true,
		listingHints: true,
		found:        map[string]FlagDB{},
		docs:         map[string]*docState{},
		ext:          map[string]map[string][]protocol.Diagnostic{},
		logged:       map[string]bool{},
	}
}

// dbOrErr keeps a nil *DB from becoming a non-nil interface.
func dbOrErr(db *toolchain.DB, err error) (FlagDB, error) {
	if err != nil {
		return nil, err
	}
	return db, nil
}

// logOnce logs a message the first time key is seen. Missing compilers and
// databases are facts about the machine, not events; repeating them on
// every save would bury everything else in the log.
func (e *Engine) logOnce(key, format string, args ...any) {
	e.mu.Lock()
	seen := e.logged[key]
	e.logged[key] = true
	e.mu.Unlock()
	if !seen {
		e.log.Printf(format, args...)
	}
}

// Initialize implements server.Engine.
func (e *Engine) Initialize(s server.Session) {
	var opts settings
	if len(s.InitOptions) > 0 && string(s.InitOptions) != "null" {
		if err := json.Unmarshal(s.InitOptions, &opts); err != nil {
			e.log.Printf("initializationOptions ignored: %v", err)
			opts = settings{}
		}
	}
	root := ""
	if s.RootURI != "" {
		if p, ok := toolchain.URIToPath(s.RootURI); ok {
			root = p
		}
	}
	var db FlagDB
	if cc := strings.TrimSpace(opts.CompileCommands); cc != "" {
		if !filepath.IsAbs(cc) && root != "" {
			cc = filepath.Join(root, cc)
		}
		var err error
		if db, err = e.cfg.LoadDBFile(cc); err != nil {
			e.log.Printf("compileCommands %s: %v; falling back to discovery", cc, err)
			db = nil
		}
	}
	if db == nil && root != "" {
		// A workspace without a database at its root is normal; files get
		// one by walking up from their own directory instead.
		db, _ = e.cfg.LoadDB(root)
	}
	lvl, src := levelSetting(opts.Level)
	if opts.Level != "" && lvl == 0 {
		e.log.Printf("initializationOptions.level %q ignored: want v1..v4", opts.Level)
	}

	e.mu.Lock()
	e.publishFn = s.Publish
	e.root = root
	e.db = db
	e.toolchainOn = opts.Toolchain == nil || *opts.Toolchain
	e.listingHints = opts.ListingHints == nil || *opts.ListingHints
	e.level, e.levelSource = lvl, src
	e.mu.Unlock()
	if root != "" {
		e.index.AddRoot(root)
	}
}

// levelSetting parses the level option; "" and anything invalid mean none.
func levelSetting(s string) (isa.Level, string) {
	v := strings.ToLower(strings.TrimSpace(s))
	if len(v) == 2 && v[0] == 'v' && v[1] >= '1' && v[1] <= '4' {
		return isa.Level(v[1] - '0'), "falcon-asm.level setting " + v
	}
	return 0, ""
}

// cLanguages are the language ids served for inline asm only.
var cLanguages = map[string]bool{
	"c": true, "cpp": true, "objective-c": true, "objective-cpp": true, "cuda-cpp": true,
}

// cExts classify a URI whose language id is unknown or unhelpful.
var cExts = map[string]bool{
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".hh": true,
}

// languageOf decides how a document is served. Assembly is the default: the
// server was assembly-only before C support, and clients other than VS Code
// send ids such as "asm" or "gas" for .S files.
func languageOf(uri, languageID string) lang {
	switch {
	case languageID == server.AsmLanguageID:
		return langAsm
	case cLanguages[languageID]:
		return langC
	}
	if cExts[strings.ToLower(filepath.Ext(uri))] {
		return langC
	}
	return langAsm
}

// flagsFor resolves path's compile flags: the database from initialize,
// else the nearest one above the file, cached per directory so a project's
// files share one load. Called with mu held; FindDB only stats and reads
// small files.
func (e *Engine) flagsForLocked(path string) (toolchain.Flags, bool) {
	db := e.db
	if db == nil {
		dir := filepath.Dir(path)
		var ok bool
		if db, ok = e.found[dir]; !ok {
			found, err := e.cfg.FindDB(path)
			if err != nil {
				found = nil
			}
			e.found[dir] = found
			db = found
		}
	}
	if db == nil {
		if !e.logged["nodb"] {
			e.logged["nodb"] = true
			e.log.Printf("no %s found for %s; toolchain features are off for files without one", toolchain.DBFile, path)
		}
		return toolchain.Flags{}, false
	}
	return db.Flags(path)
}

// Open implements server.Engine.
func (e *Engine) Open(doc *docstore.Document, languageID string) {
	st := &docState{uri: doc.URI, lang: languageOf(doc.URI, languageID), doc: doc, asmVersion: -1}
	if p, ok := toolchain.URIToPath(doc.URI); ok {
		st.path = p
	}
	e.mu.Lock()
	if old := e.docs[doc.URI]; old != nil && old.timer != nil {
		old.timer.Stop()
	}
	if st.path != "" {
		st.flags, st.hasFlags = e.flagsForLocked(st.path)
	}
	e.docs[doc.URI] = st
	snap := e.snapshotLocked(st)
	e.schedulePublishLocked(st)
	e.startRunLocked(st)
	e.mu.Unlock()
	e.updateIndex(st, snap)
}

// Change implements server.Engine.
func (e *Engine) Change(doc *docstore.Document) {
	e.mu.Lock()
	st := e.docs[doc.URI]
	if st == nil {
		e.mu.Unlock()
		return
	}
	st.doc = doc
	snap := e.snapshotLocked(st)
	e.schedulePublishLocked(st)
	e.mu.Unlock()
	e.updateIndex(st, snap)
}

// Save implements server.Engine. The toolchain reads the file from disk,
// which after a save is the text the client holds.
func (e *Engine) Save(uri string) {
	e.mu.Lock()
	if st := e.docs[uri]; st != nil {
		e.startRunLocked(st)
	}
	e.mu.Unlock()
}

// Close implements server.Engine.
func (e *Engine) Close(uri string) {
	e.mu.Lock()
	st := e.docs[uri]
	if st == nil {
		e.mu.Unlock()
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	delete(e.docs, uri)
	targets := e.setExtLocked(uri, nil)
	e.mu.Unlock()
	if st.lang == langAsm {
		e.index.Remove(uri)
	}
	e.publish(uri)
	for _, t := range targets {
		e.publish(t)
	}
}

// Shutdown implements server.Engine: cancel runs, stop timers, and wait a
// bounded time for the run goroutines so no compiler outlives the server.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	e.closed = true
	for _, st := range e.docs {
		if st.timer != nil {
			st.timer.Stop()
		}
	}
	e.mu.Unlock()
	e.cancel()
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(e.cfg.ShutdownWait):
		e.log.Printf("shutdown: toolchain runs still in flight after %v", e.cfg.ShutdownWait)
	}
}

// updateIndex feeds the symbol index. C sources are not assembly and would
// only add noise; their headers reach the index through assembly includes.
func (e *Engine) updateIndex(st *docState, snap *snapshot) {
	if st.lang != langAsm {
		return
	}
	e.index.Update(st.uri, snap.doc.Text, snap.includes())
}

// sortedKeys returns m's keys in order, for deterministic publishing.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
