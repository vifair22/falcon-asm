package project

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Doc is one document to lint.
type Doc struct {
	Doc *docstore.Document
	C   bool // a C-family document: lint its inline asm and the C text
	// Toks are the assembly document's tokens; nil tokenizes here.
	Toks []lexer.Token
	// Prog is the assembly document parsed from Toks with Target.Mode and
	// Macros, when the caller already has it (the engine shares one per
	// version with hover); nil parses here.
	Prog    *asm.Program
	Target  lint.Target
	Config  lint.FileConfig
	Macros  map[string]string // toolchain macro values, for the parse; may be nil
	Listing *toolchain.AsmResult
	Tables  lint.Tables
	// Inline are the C document's asm statements; nil extracts them.
	Inline []inlineasm.Stmt
	// Memo is shared with the assembly run, so a caller can seed values it
	// already computed (the engine seeds its analysis); nil is fresh.
	Memo *lint.Memo
	Log  func(format string, args ...any)
	// Workspace is the symbol rules' view of the rest of the workspace
	// (symbols.Index.LintView); nil leaves those rules quiet.
	Workspace lint.Workspace
}

// Result is a linted document.
type Result struct {
	Findings []lint.Finding
	// Suppressions are the document's suppression comments, kept for the
	// "ignore on this line" quick fix.
	Suppressions *lint.Suppressions
}

// Lint runs every rule over the document and applies its suppressions.
func Lint(d Doc) Result {
	if d.C {
		return lintC(d)
	}
	toks := d.Toks
	if toks == nil {
		toks = lexer.Tokenize(d.Doc.Text)
	}
	prog := d.Prog
	if prog == nil {
		prog = asm.Parse(d.Doc, toks, asm.Config{InitialMode: d.Target.Mode, Macros: d.Macros})
	}
	var b lint.Batch
	b.Add(lint.Input{
		Prog: prog, Lang: lint.LangAsm, Target: d.Target, Listing: d.Listing, Tables: d.Tables,
		Config: d.Config, Memo: d.Memo, Log: d.Log, Workspace: d.Workspace,
		// The editor and the CLI both report what a macro's body does at
		// each invocation, on the invocation.
		ExpandMacros: true,
	})
	sup := lint.AsmSuppressions(d.Doc, toks)
	return Result{Findings: b.Finish(sup, d.Config), Suppressions: sup}
}

// lintC lints each complete asm statement as a program of its own, with
// findings and fixes mapped back into the C document, then the C text
// itself, and applies the C file's suppression comments to the lot. A
// Partial statement (a macro in the template) is skipped: its Asm is only
// a prefix, and a finding about half a template would mislead.
func lintC(d Doc) Result {
	stmts := d.Inline
	if stmts == nil {
		stmts = inlineasm.Extract(d.Doc.Text)
	}
	var b lint.Batch
	for i := range stmts {
		st := &stmts[i]
		if st.Partial || st.Asm == "" {
			continue
		}
		vdoc := &docstore.Document{URI: d.Doc.URI, Text: st.Asm}
		toks := lexer.Tokenize(st.Asm)
		prog := asm.Parse(vdoc, toks, asm.Config{InitialMode: d.Target.Mode})
		b.Add(lint.Input{
			Prog: prog, Lang: lint.LangInline, Target: d.Target, Tables: d.Tables, Config: d.Config, Log: d.Log,
			MapRange: func(r protocol.Range) (protocol.Range, bool) { return MapRange(d.Doc, vdoc, st, r) },
			MapEdit:  func(e protocol.TextEdit) (protocol.TextEdit, bool) { return mapEdit(d.Doc, vdoc, st, e) },
		})
	}
	b.Add(lint.Input{
		Prog: &asm.Program{Doc: d.Doc}, Lang: lint.LangC, Target: d.Target, Tables: d.Tables,
		Config: d.Config, Inline: stmts, Log: d.Log,
	})
	sup := lint.CSuppressions(d.Doc)
	return Result{Findings: b.Finish(sup, d.Config), Suppressions: sup}
}

// MapRange converts a range in an inline asm template document to the C
// document. The end maps through its last byte rather than one past it:
// one past may sit across a closing quote and a string break, which would
// stretch the range over C source that is not part of the token.
func MapRange(cdoc, vdoc *docstore.Document, st *inlineasm.Stmt, r protocol.Range) (protocol.Range, bool) {
	start, err1 := vdoc.Offset(r.Start)
	end, err2 := vdoc.Offset(r.End)
	if err1 != nil || err2 != nil {
		return protocol.Range{}, false
	}
	cs, ce := mapOffsets(st, start, end)
	return protocol.Range{Start: cdoc.Position(cs), End: cdoc.Position(ce)}, true
}

func mapOffsets(st *inlineasm.Stmt, start, end int) (int, int) {
	cs := st.Map(start)
	ce := cs
	if end > start {
		ce = st.Map(end-1) + 1
	}
	return cs, ce
}

// mapEdit carries a template edit into the C source only when that is a
// plain text edit there too: the C text under the mapped range must equal
// the template text (no escape sequence, %% or string break inside it),
// and the new text must need no escaping in a C string or an extended asm
// template. Anything else would need re-escaping the fix cannot know how
// to do, and a quick fix that breaks the build is worse than none.
func mapEdit(cdoc, vdoc *docstore.Document, st *inlineasm.Stmt, e protocol.TextEdit) (protocol.TextEdit, bool) {
	if strings.ContainsAny(e.NewText, "\"\\\n\r%") {
		return protocol.TextEdit{}, false
	}
	start, err1 := vdoc.Offset(e.Range.Start)
	end, err2 := vdoc.Offset(e.Range.End)
	if err1 != nil || err2 != nil {
		return protocol.TextEdit{}, false
	}
	cs, ce := mapOffsets(st, start, end)
	if ce > len(cdoc.Text) || cdoc.Text[cs:ce] != vdoc.Text[start:end] {
		return protocol.TextEdit{}, false
	}
	return protocol.TextEdit{Range: protocol.Range{Start: cdoc.Position(cs), End: cdoc.Position(ce)}, NewText: e.NewText}, true
}
