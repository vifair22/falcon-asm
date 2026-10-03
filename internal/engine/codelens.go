package engine

import (
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/rules/layout"
)

// CodeLens implements server.CodeLensEngine: one title-only lens per size
// budget (roadmap item 47), from layout.Budgets, the same computation the
// budget-exceeded rule reports from. Like the listing hints, lenses appear
// only while the last toolchain run describes the buffer; the parse they
// need is therefore paid once per run, not per keystroke.
func (e *Engine) CodeLens(doc *docstore.Document) []protocol.CodeLens {
	s := e.snapshotFor(doc)
	if s.lang != langAsm || s.listing == nil {
		return nil
	}
	p := s.program()
	var out []protocol.CodeLens
	for _, b := range layout.Budgets(p, s.listing) {
		pos := protocol.Position{Line: b.Line}
		out = append(out, protocol.CodeLens{
			Range:   protocol.Range{Start: pos, End: pos},
			Command: &protocol.Command{Title: b.Title()},
		})
	}
	return out
}
