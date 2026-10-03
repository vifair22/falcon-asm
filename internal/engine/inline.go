package engine

import (
	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hover"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
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
		r := mapRange(s.doc, vdoc, stmt, *h.Range)
		h.Range = &r
	}
	return h, nil
}

// inlineDiagnostics analyzes every complete asm statement with the C file's
// mode and level and maps the findings into the C document. A Partial
// statement (a macro in the template) is skipped: its Asm is only a prefix,
// and a mode warning about half a template would mislead.
func (s *snapshot) inlineDiagnostics() []protocol.Diagnostic {
	cfg := s.cfg
	// Analyze looks for the level comment in the text it is given, which
	// for a template is never the C file's own comment.
	if lvl, ok := analysis.LevelOverride(s.doc.Text); ok {
		cfg.Level, cfg.LevelSource = lvl, "falcon-asm: level comment"
	}
	cfg.Macros = nil
	var out []protocol.Diagnostic
	stmts := s.statements()
	for i := range stmts {
		st := &stmts[i]
		if st.Partial || st.Asm == "" {
			continue
		}
		vdoc := &docstore.Document{URI: s.doc.URI, Text: st.Asm}
		for _, d := range analysis.Analyze(vdoc, cfg).Diagnostics {
			d.Range = mapRange(s.doc, vdoc, st, d.Range)
			out = append(out, d)
		}
	}
	return out
}

// mapRange converts a range in the template document to the C document.
// The end maps through its last byte rather than one past it: one past may
// sit across a closing quote and a string break, which would stretch the
// range over C source that is not part of the token.
func mapRange(cdoc, vdoc *docstore.Document, st *inlineasm.Stmt, r protocol.Range) protocol.Range {
	start, err1 := vdoc.Offset(r.Start)
	end, err2 := vdoc.Offset(r.End)
	if err1 != nil || err2 != nil {
		return protocol.Range{}
	}
	cs := st.Map(start)
	ce := cs
	if end > start {
		ce = st.Map(end-1) + 1
	}
	return protocol.Range{Start: cdoc.Position(cs), End: cdoc.Position(ce)}
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
