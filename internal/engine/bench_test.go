package engine

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/server"
)

// benchEngine is an engine with no toolchain and no database, whose
// debounced publishes never fire during a benchmark.
func benchEngine(b *testing.B) *Engine {
	e := New(Config{
		Toolchain: &fakeTC{},
		LoadDB:    func(string) (FlagDB, error) { return nil, fs.ErrNotExist },
		FindDB:    func(string) (FlagDB, error) { return nil, fs.ErrNotExist },
		Debounce:  time.Hour,
	})
	e.Initialize(server.Session{Publish: func(protocol.PublishDiagnosticsParams) {}})
	b.Cleanup(e.Shutdown)
	return e
}

// editor applies single-character edits through a real store, so each
// iteration hands the engine a new document version the way didChange does.
type editor struct {
	s   *docstore.Store
	uri string
	v   int
	at  protocol.Position
}

func newEditor(b *testing.B, e *Engine, lines int) *editor {
	text := benchdata.Large(b, lines)
	ed := &editor{s: docstore.New(), uri: "file:///bench/b.S", v: 1}
	ed.at = protocol.Position{Line: strings.Count(text, "\n") / 2}
	ed.s.Open(ed.uri, 1, text)
	doc, _ := ed.s.Get(ed.uri)
	e.Open(doc, "falcon-asm")
	return ed
}

func (ed *editor) edit(b *testing.B) *docstore.Document {
	ed.v++
	ch := []protocol.TextDocumentContentChangeEvent{{Range: &protocol.Range{Start: ed.at, End: ed.at}, Text: "x"}}
	if err := ed.s.Change(ed.uri, ed.v, ch); err != nil {
		b.Fatal(err)
	}
	doc, _ := ed.s.Get(ed.uri)
	return doc
}

var sizes = []int{20000, 100000}

// BenchmarkChange is the synchronous cost of didChange inside the engine
// (the store's own cost is excluded by building the versions first).
func BenchmarkChange(b *testing.B) {
	for _, lines := range sizes {
		b.Run(fmt.Sprintf("%dk", lines/1000), func(b *testing.B) {
			e := benchEngine(b)
			ed := newEditor(b, e, lines)
			doc := ed.edit(b)
			b.ReportAllocs()
			for b.Loop() {
				e.Change(doc)
			}
		})
	}
}

// BenchmarkSnapshot builds one version's derived data: tokens and analysis.
func BenchmarkSnapshot(b *testing.B) {
	for _, lines := range sizes {
		b.Run(fmt.Sprintf("%dk", lines/1000), func(b *testing.B) {
			e := benchEngine(b)
			ed := newEditor(b, e, lines)
			e.mu.Lock()
			st := e.docs[ed.uri]
			e.mu.Unlock()
			b.ReportAllocs()
			for b.Loop() {
				e.mu.Lock()
				s := e.newSnapshotLocked(st, st.doc)
				e.mu.Unlock()
				s.tokens()
				s.analysis()
			}
		})
	}
}

// BenchmarkEditThenQuery is a keystroke followed by the request an editor
// sends after it: the full cost of one edit-and-answer cycle.
func BenchmarkEditThenQuery(b *testing.B) {
	queries := map[string]func(e *Engine, d *docstore.Document, at protocol.Position){
		"semanticTokens": func(e *Engine, d *docstore.Document, _ protocol.Position) { e.SemanticTokens(d) },
		"completion":     func(e *Engine, d *docstore.Document, at protocol.Position) { e.Completion(d, at) },
		"documentSymbol": func(e *Engine, d *docstore.Document, _ protocol.Position) { e.DocumentSymbols(d) },
		"foldingRange":   func(e *Engine, d *docstore.Document, _ protocol.Position) { e.FoldingRanges(d) },
		"hover":          func(e *Engine, d *docstore.Document, at protocol.Position) { _, _ = e.Hover(d, at) },
	}
	for _, name := range []string{"semanticTokens", "completion", "documentSymbol", "foldingRange", "hover"} {
		q := queries[name]
		for _, lines := range sizes {
			b.Run(fmt.Sprintf("%s/%dk", name, lines/1000), func(b *testing.B) {
				e := benchEngine(b)
				ed := newEditor(b, e, lines)
				b.ReportAllocs()
				for b.Loop() {
					doc := ed.edit(b)
					e.Change(doc)
					q(e, doc, protocol.Position{Line: ed.at.Line, Character: 1})
				}
			})
		}
	}
}
