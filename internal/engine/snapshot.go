package engine

import (
	"path/filepath"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hover"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
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

	toksOnce sync.Once
	toks     []lexer.Token

	anOnce sync.Once
	an     analysis.Result

	stmtsOnce sync.Once
	stmts     []inlineasm.Stmt

	diagOnce sync.Once
	diags    []protocol.Diagnostic
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
	}
	s.cfg = analysis.Config{Level: e.level, LevelSource: e.levelSource}
	if st.hasFlags {
		s.cfg.InitialMode = analysis.Mode(st.flags.Mode)
		if s.cfg.Level == 0 && st.flags.Level != 0 {
			s.cfg.Level, s.cfg.LevelSource = st.flags.Level, st.flags.LevelSource
		}
	}
	if len(st.macros) > 0 {
		s.cfg.Macros = make(map[string]string, len(st.macros))
		for name, m := range st.macros {
			s.cfg.Macros[name] = m.Value
		}
	}
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

func (s *snapshot) analysis() analysis.Result {
	s.anOnce.Do(func() { s.an = analysis.AnalyzeTokens(s.doc, s.tokens(), s.cfg) })
	return s.an
}

func (s *snapshot) statements() []inlineasm.Stmt {
	s.stmtsOnce.Do(func() { s.stmts = inlineasm.Extract(s.doc.Text) })
	return s.stmts
}

// diagnostics are falcon-asm's own findings for the document: the analysis
// of an assembly file, or of every inline asm statement of a C file.
func (s *snapshot) diagnostics() []protocol.Diagnostic {
	s.diagOnce.Do(func() {
		if s.lang == langC {
			s.diags = s.inlineDiagnostics()
			return
		}
		s.diags = s.analysis().Diagnostics
	})
	return s.diags
}

// modeAt is the code size in effect at the start of line.
func (s *snapshot) modeAt(line int) int {
	m := s.analysis().ModeAtLine
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
			name = strings.Trim(arg.Text, "\"")
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
