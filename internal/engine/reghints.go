package engine

import (
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
)

// Register inlay hints (roadmap item 84): the tracked register values at
// interesting statements (int, in/out, rdmsr/wrmsr, cpuid, lgdt, mov to a
// control register, far jumps), shown after the statement's code, e.g.
// `ah=42h dl=80h`. Off by default (initializationOptions.registerHints):
// the values are a reading aid, and a line of code with a hint behind it
// is noisier than one without.
//
// The hints of a version are computed once, from the snapshot's hardware
// index and its flow result, and filtered by the requested range on each
// request, so scrolling costs a filter, not an analysis.

func (s *snapshot) regHints() []protocol.InlayHint {
	s.hw.hintsOnce.Do(func() {
		ix := s.hwIndex()
		hs := ix.RegisterHints(hw.DB(s.tables))
		if len(hs) == 0 {
			return
		}
		// One walk over the text finds every hinted line (the hints are in
		// line order), instead of a line lookup per hint.
		text := s.doc.Text
		toks := s.tokens()
		out := make([]protocol.InlayHint, 0, len(hs))
		line, start := 0, 0
		for _, h := range hs {
			want := ix.Prog.Stmts[h.Stmt].Line
			for line < want {
				nl := strings.IndexByte(text[start:], '\n')
				if nl < 0 {
					return
				}
				start += nl + 1
				line++
			}
			end := strings.IndexByte(text[start:], '\n')
			if end < 0 {
				end = len(text) - start
			}
			lineText := strings.TrimSuffix(text[start:start+end], "\r")
			out = append(out, protocol.InlayHint{
				Position:    protocol.Position{Line: want, Character: hintColumn(toks, want, lineText)},
				Label:       h.Label,
				PaddingLeft: true,
				Tooltip:     h.Tooltip,
			})
		}
		s.hw.hints = out
	})
	return s.hw.hints
}

// registerHints appends the register hints in rng to out (the listing
// hints, which share the hint column) when on is set.
func (e *Engine) registerHints(on bool, doc *docstore.Document, rng protocol.Range, out []protocol.InlayHint) []protocol.InlayHint {
	if !on {
		return out
	}
	s := e.snapshotFor(doc)
	if s.lang != langAsm {
		return out
	}
	hints := s.regHints()
	k := sort.Search(len(hints), func(k int) bool { return hints[k].Position.Line >= rng.Start.Line })
	for ; k < len(hints) && hints[k].Position.Line <= rng.End.Line; k++ {
		out = append(out, hints[k])
	}
	return out
}
