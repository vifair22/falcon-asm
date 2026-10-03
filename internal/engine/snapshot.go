package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/hover"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/project"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
	"git.airies.net/vifair22/falcon-asm/internal/rules/target"
	"git.airies.net/vifair22/falcon-asm/internal/symbols"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"

	// Every rule registers itself; the engine publishes their findings.
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/all"
)

// snapshot is everything derived from one document version under one
// configuration. It is immutable once built; each derived value is computed
// on first use (sync.Once) and then shared, so a hover, the semantic tokens
// and the diagnostics of one version tokenize and analyze it once.
type snapshot struct {
	doc      *docstore.Document
	lang     lang
	path     string
	gen      int
	cfg      analysis.Config
	flags    toolchain.Flags
	hasFlags bool
	macros   map[string]toolchain.Macro // toolchain table; never mutated

	tin     project.TargetInputs
	fileCfg lint.FileConfig
	tables  lint.Tables
	listing *toolchain.AsmResult // the last run's result when it describes this text
	logf    func(format string, args ...any)

	targetOnce sync.Once
	target     lint.Target

	lintOnce sync.Once
	lint     project.Result
	// lintDone is set once lint holds the version's result, so a request
	// can tell whether asking for it would mean running every rule now.
	lintDone atomic.Bool

	toksOnce sync.Once
	toks     []lexer.Token

	anOnce sync.Once
	an     analysis.Result

	modesOnce sync.Once
	modes     []analysis.Mode

	stmtsOnce sync.Once
	stmts     []inlineasm.Stmt

	symOnce sync.Once
	sym     *symbols.File

	// The program parse and its dataflow, shared by lint, hover, register
	// hints and code lenses (hwhover.go).
	progOnce sync.Once
	prog     *asm.Program
	flowOnce sync.Once
	cfgG     *flow.CFG
	flowRes  *flow.Result

	diagOnce sync.Once
	diags    []protocol.Diagnostic

	// ws is the lint rules' workspace view (expand.go and the symbol
	// rules); exp caches macro expansions for hover (expand.go).
	ws  lint.Workspace
	exp expandState
	// hw is the hardware index for hover (hwhover.go).
	hw hwState
}

// snapshotLocked returns st's current snapshot, building a new one when the
// text or the configuration generation moved on. Building is cheap (it only
// captures inputs); the work happens lazily outside the lock.
func (e *Engine) snapshotLocked(st *docState) *snapshot {
	if s := st.snap; s != nil && s.doc == st.doc && s.gen == st.gen {
		return s
	}
	st.snap = e.newSnapshotLocked(st, st.doc)
	return st.snap
}

func (e *Engine) newSnapshotLocked(st *docState, doc *docstore.Document) *snapshot {
	s := &snapshot{
		doc: doc, lang: st.lang, path: st.path, gen: st.gen,
		flags: st.flags, hasFlags: st.hasFlags, macros: st.macros,
		fileCfg: st.fileCfg, tables: st.tables, logf: e.log.Printf,
	}
	if st.asm != nil && st.asmVersion == doc.Version {
		s.listing = st.asm
	}
	if st.lang == langAsm {
		s.ws = e.index.LintView(st.uri)
	}
	s.exp.tc, _ = e.tc.(lineExpander)
	s.tin = project.TargetInputs{
		Flags: st.flags, HasFlags: st.hasFlags, Config: st.fileCfg.Target,
		SettingLevel: e.level, SettingSource: e.levelSource,
	}
	var macros map[string]string
	if len(st.macros) > 0 {
		macros = make(map[string]string, len(st.macros))
		for name, m := range st.macros {
			macros[name] = m.Value
		}
	}
	// The analysis applies the in-file level comment itself, so the target
	// without it gives the same configuration the lint rules derive from
	// the full target: that is what lets the snapshot's analysis seed the
	// rules (see lintResult) instead of running twice.
	s.cfg = target.AnalysisConfig(project.ResolveTarget(s.tin, project.LevelComment{}), macros)
	return s
}

// snapshotFor returns the snapshot for a document the server hands in. The
// server always reports a change before querying, so the open state normally
// holds this exact snapshot; otherwise (a document the engine never saw) a
// throwaway one is built with default configuration.
func (e *Engine) snapshotFor(doc *docstore.Document) *snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.docs[doc.URI]
	if st == nil {
		st = &docState{uri: doc.URI, lang: languageOf(doc.URI, ""), doc: doc}
		if p, ok := toolchain.URIToPath(doc.URI); ok {
			st.path = p
		}
		return e.newSnapshotLocked(st, doc)
	}
	if st.doc == doc {
		return e.snapshotLocked(st)
	}
	return e.newSnapshotLocked(st, doc)
}

func (s *snapshot) tokens() []lexer.Token {
	s.toksOnce.Do(func() { s.toks = lexer.Tokenize(s.doc.Text) })
	return s.toks
}

// symbols is the symbol parse of the version, shared by the index, the
// outline and every query that needs one.
func (s *snapshot) symbols() *symbols.File {
	s.symOnce.Do(func() { s.sym = symbols.Parse(s.doc.URI, s.doc.Text, s.tokens()) })
	return s.sym
}

func (s *snapshot) analysis() analysis.Result {
	s.anOnce.Do(func() { s.an = analysis.AnalyzeTokens(s.doc, s.tokens(), s.cfg) })
	return s.an
}

func (s *snapshot) statements() []inlineasm.Stmt {
	s.stmtsOnce.Do(func() { s.stmts = inlineasm.Extract(s.doc.Text) })
	return s.stmts
}

// targetOf is the document's resolved target, the level comment included.
func (s *snapshot) targetOf() lint.Target {
	s.targetOnce.Do(func() {
		var c project.LevelComment
		switch s.lang {
		case langAsm:
			c = project.AsmLevelComment(s.tokens())
		case langC:
			c = project.CLevelComment(s.doc.Text)
		}
		s.target = project.ResolveTarget(s.tin, c)
	})
	return s.target
}

// lintResult runs the lint rules over the version once. An assembly run
// reuses the snapshot's tokens and analysis; a C run its inline asm
// statements.
func (s *snapshot) lintResult() project.Result {
	s.lintOnce.Do(func() {
		d := project.Doc{
			Doc: s.doc, Target: s.targetOf(), Config: s.fileCfg, Macros: s.cfg.Macros,
			Listing: s.listing, Tables: s.tables, Log: s.logf, Workspace: s.ws,
		}
		switch s.lang {
		case langAsm:
			d.Toks = s.tokens()
			d.Prog = s.program()
			d.Memo = lint.NewMemo()
			target.Seed(d.Memo, s.analysis())
			lint.ProvideFlow(d.Memo, s.flow)
			hw.Provide(d.Memo, s.hwIndex)
		case langC:
			d.C = true
			d.Inline = s.statements()
		default:
			return
		}
		s.lint = project.Lint(d)
		s.lintDone.Store(true)
	})
	return s.lint
}

// diagnostics are falcon-asm's own findings for the document, as sent.
func (s *snapshot) diagnostics() []protocol.Diagnostic {
	s.diagOnce.Do(func() {
		fs := s.lintResult().Findings
		s.diags = make([]protocol.Diagnostic, len(fs))
		for i, f := range fs {
			s.diags[i] = f.Diagnostic()
		}
	})
	return s.diags
}

// modeAt is the code size in effect at the start of line. It uses the
// modes-only walk rather than the full analysis: completion asks on every
// keystroke, and the analysis is wanted only by the debounced publish.
func (s *snapshot) modeAt(line int) int {
	s.modesOnce.Do(func() { s.modes = analysis.ModeAtLine(s.doc, s.tokens(), s.cfg) })
	m := s.modes
	if line >= 0 && line < len(m) {
		return int(m[line])
	}
	return 64
}

// macro resolves a cpp macro for hover. The document's own #define wins
// over the toolchain's table because it reflects the text being edited;
// the table (from the last save) supplies headers, -D and builtins.
func (s *snapshot) macro(name string) (hover.Macro, bool) {
	for _, sym := range s.analysis().Symbols {
		if sym.Kind != analysis.Macro || sym.Name != name {
			continue
		}
		m := hover.Macro{Value: sym.Detail}
		// The analysis detail drops the distinction between "NAME(x) x"
		// and "NAME (x)"; the byte after the name in the source has it.
		if off, err := s.doc.Offset(sym.SelectionRange.End); err == nil && off < len(s.doc.Text) && s.doc.Text[off] == '(' {
			m.Function = true
			if end := strings.IndexByte(m.Value, ')'); strings.HasPrefix(m.Value, "(") && end > 0 {
				m.Params = strings.ReplaceAll(m.Value[1:end], " ", "")
				m.Value = strings.TrimSpace(m.Value[end+1:])
			}
		}
		m.Def = &protocol.Location{URI: s.doc.URI, Range: sym.SelectionRange}
		return m, true
	}
	if tm, ok := s.macros[name]; ok {
		return hover.Macro{Value: tm.Value, Function: tm.Function, Params: tm.Params, Def: tm.Def}, true
	}
	return hover.Macro{}, false
}

// includes resolves the document's #include and .include lines to absolute
// paths for the symbol index, with the file's own search path when it has
// compile flags and relative to its directory otherwise.
func (s *snapshot) includes() []string {
	if s.path == "" {
		return nil
	}
	toks := s.tokens()
	var out []string
	for i := 0; i+1 < len(toks); i++ {
		t, arg := toks[i], toks[i+1]
		if arg.Line != t.Line {
			continue
		}
		var name string
		angled := false
		switch {
		case t.Kind == lexer.CppDirective && strings.HasPrefix(strings.TrimLeft(strings.TrimPrefix(t.Text, "#"), " \t"), "include") && arg.Kind == lexer.CppArg:
			a := strings.TrimSpace(arg.Text)
			if len(a) < 2 {
				continue
			}
			switch {
			case a[0] == '<' && strings.IndexByte(a, '>') > 1:
				name, angled = a[1:strings.IndexByte(a, '>')], true
			case a[0] == '"' && strings.IndexByte(a[1:], '"') > 0:
				name = a[1 : 1+strings.IndexByte(a[1:], '"')]
			}
		case t.Kind == lexer.Directive && strings.EqualFold(t.Text, ".include") && arg.Kind == lexer.String:
			// gas searches its working directory and its own -I list,
			// not cpp's; with flags the gas search applies.
			if name = strings.Trim(arg.Text, "\""); name != "" && s.hasFlags {
				if p, ok := s.flags.ResolveGasInclude(s.path, name); ok {
					out = append(out, p)
				}
				continue
			}
		}
		if name == "" {
			continue
		}
		if p, ok := s.resolveInclude(name, angled); ok {
			out = append(out, p)
		}
	}
	return out
}

func (s *snapshot) resolveInclude(name string, angled bool) (string, bool) {
	if s.hasFlags {
		return s.flags.ResolveInclude(s.path, name, angled)
	}
	p := filepath.Join(filepath.Dir(s.path), name)
	if filepath.IsAbs(name) {
		p = name
	}
	if fileExists(p) {
		return p, true
	}
	return "", false
}
