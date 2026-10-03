package engine

import (
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hover"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/project"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// inlineHover answers hover in a C document. Operand references,
// constraints and clobbers are explained by inlineasm itself; instructions
// and registers in the template get the GAS hover, run over the template
// as a document of its own and mapped back into C coordinates.
func (s *snapshot) inlineHover(pos protocol.Position) (*protocol.Hover, error) {
	cOff, err := s.doc.Offset(pos)
	if err != nil {
		return nil, nil
	}
	stmt := inlineasm.Find(s.statements(), cOff)
	if stmt == nil {
		return nil, nil
	}
	if text, ok := stmt.HoverAt(cOff); ok {
		return &protocol.Hover{Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: text}}, nil
	}
	asmOff, ok := stmt.AsmOffset(cOff)
	if !ok {
		return nil, nil
	}
	vdoc := &docstore.Document{URI: s.doc.URI, Text: stmt.Asm}
	h, err := hover.HoverWith(vdoc, vdoc.Position(asmOff), hover.Options{
		LineNumber: func(line int) int { return templateLine(s.doc, vdoc, stmt, line) },
	})
	if err != nil || h == nil {
		return nil, err
	}
	if h.Range != nil {
		r, ok := project.MapRange(s.doc, vdoc, stmt, *h.Range)
		if !ok {
			h.Range = nil
		} else {
			h.Range = &r
		}
	}
	return h, nil
}

// templateLine maps a line of the template document to the C line holding
// it, so a label hover says where the label sits in the C file.
func templateLine(cdoc, vdoc *docstore.Document, st *inlineasm.Stmt, line int) int {
	off, err := vdoc.Offset(protocol.Position{Line: line})
	if err != nil {
		return line
	}
	return cdoc.Position(st.Map(off)).Line
}
