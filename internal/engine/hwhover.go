package engine

import (
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/rules/hw"
)

// hwState is the hardware index of one snapshot: the document parsed with
// its target mode and macros, its descriptor and page tables found and the
// constants of every block tracked, once per version. A hover over a
// descriptor then costs a lookup, not a scan of the file.
type hwState struct {
	once sync.Once
	ix   *hw.Index

	hintsOnce sync.Once
	hints     []protocol.InlayHint // register hints, in line order (reghints.go)
}

// hwIndex is built with the dataflow solution, so hover sees values that
// cross labels (`movb $0x42, %ah` behind 2:, the int after 4:). The flow
// analysis runs once per version, on the first hardware hover or hint.
func (s *snapshot) hwIndex() *hw.Index {
	s.hw.once.Do(func() {
		prog := asm.Parse(s.doc, s.tokens(), asm.Config{InitialMode: s.targetOf().Mode, Macros: s.cfg.Macros})
		s.hw.ix = hw.NewIndexFlow(prog, s.flowOf(prog))
	})
	return s.hw.ix
}

// flowOf analyzes prog; a panic inside the analysis costs the cross-label
// values (the index falls back to block-local ones), never the hover.
func (s *snapshot) flowOf(prog *asm.Program) (res *flow.Result) {
	defer func() {
		if p := recover(); p != nil {
			if s.logf != nil {
				s.logf("hover: flow analysis panicked: %v", p)
			}
			res = nil
		}
	}()
	return flow.Analyze(flow.Build(prog), flow.Config{})
}

// withHardware appends the hardware meaning of what is under the cursor
// (a decoded descriptor, the BIOS service an int calls, the port or MSR a
// constant names) to the ordinary hover, or makes it the hover when there
// is no other.
func (s *snapshot) withHardware(pos protocol.Position, h *protocol.Hover) *protocol.Hover {
	off, err := s.doc.Offset(pos)
	if err != nil {
		return h
	}
	md, start, end := s.hwIndex().Hover(hw.DB(s.tables), off)
	return mergeHover(s, h, md, start, end)
}

// cHelperHover is the hardware hover on a C helper call (`outb(0x92, v)`).
// Macros come from the document's own #defines and the toolchain's table.
func (s *snapshot) cHelperHover(pos protocol.Position) *protocol.Hover {
	off, err := s.doc.Offset(pos)
	if err != nil {
		return nil
	}
	md, start, end := hw.CHover(s.doc.Text, off, hw.DB(s.tables), func(name string) (string, bool) {
		m, ok := s.macros[name]
		if !ok || m.Function {
			return "", false
		}
		return m.Value, true
	})
	return mergeHover(s, nil, md, start, end)
}

func mergeHover(s *snapshot, h *protocol.Hover, md string, start, end int) *protocol.Hover {
	if md == "" {
		return h
	}
	if h == nil {
		r := protocol.Range{Start: s.doc.Position(start), End: s.doc.Position(end)}
		return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: md}, Range: &r}
	}
	out := *h
	out.Contents.Value = h.Contents.Value + "\n\n---\n\n" + md
	return &out
}
