package lint

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Rule returns the rule being run.
func (c *Context) Rule() *Rule { return c.rule }

// Severity is the severity this run reports at: the rule's default or the
// configured one for the file.
func (c *Context) Severity() Severity { return c.sev }

// Lang is the kind of document being linted.
func (c *Context) Lang() Lang {
	if c.in == nil {
		return LangAsm
	}
	return c.in.Lang
}

// Inline is the C document's asm statements when Lang is LangC; nil
// otherwise.
func (c *Context) Inline() []inlineasm.Stmt {
	if c.in == nil {
		return nil
	}
	return c.in.Inline
}

// Workspace is the workspace symbol view of the document, or nil when the
// caller has none (a single buffer, a test, a macro expansion). Rules that
// need it must treat nil as "nothing is known" and stay quiet.
func (c *Context) Workspace() Workspace {
	if c.in == nil {
		return nil
	}
	return c.in.Workspace
}

// Config is the configuration resolved for the file being linted.
func (c *Context) Config() FileConfig {
	if c.in == nil {
		return FileConfig{}
	}
	return c.in.Config
}

// Memo returns the value computed by fn under key, computing it at most
// once per document run and sharing it with every other rule. Rules that
// derive the same structure (an operand table, a symbol map) use it so the
// cost is paid once; a value seeded by the caller is returned as is.
func (c *Context) Memo(key string, fn func() any) any {
	if c.in == nil || c.in.Memo == nil {
		return fn()
	}
	return c.in.Memo.Get(key, fn)
}

// Flow returns the control-flow graph and the dataflow solution, computed
// once per document and shared by every rule. Both are nil when the
// analysis could not be built (it panicked, which is logged once).
func (c *Context) Flow() (*flow.CFG, *flow.Result) {
	if c.cfg == nil && c.lazy != nil {
		c.cfg, c.flowRes = c.lazy()
	}
	return c.cfg, c.flowRes
}

// Report emits a finding over r, in the program document's coordinates.
func (c *Context) Report(r protocol.Range, msg string, fixes ...Fix) {
	c.ReportFinding(Finding{Range: r, Message: msg, Fixes: fixes})
}

// ReportFinding emits a fully built finding; Code and Severity are always
// the running rule's, whatever f says, so a rule cannot misattribute.
func (c *Context) ReportFinding(f Finding) {
	if c.findings == nil {
		return
	}
	f.Code = c.rule.Code
	f.Severity = c.sev
	*c.findings = append(*c.findings, f)
}

// ReportRange emits a finding over the byte offsets [start, end) of the
// program's document.
func (c *Context) ReportRange(start, end int, msg string, fixes ...Fix) {
	r, ok := c.byteRange(start, end)
	if !ok {
		return
	}
	c.Report(r, msg, fixes...)
}

// ReportStmt emits a finding covering statement stmtIdx. An index out of
// range is ignored rather than trusted.
func (c *Context) ReportStmt(stmtIdx int, msg string, fixes ...Fix) {
	if c.Prog == nil || stmtIdx < 0 || stmtIdx >= len(c.Prog.Stmts) {
		return
	}
	s := &c.Prog.Stmts[stmtIdx]
	c.ReportRange(s.Start, s.End, msg, fixes...)
}

// ReportOp emits a finding covering operand opIdx of statement stmtIdx.
func (c *Context) ReportOp(stmtIdx, opIdx int, msg string, fixes ...Fix) {
	if c.Prog == nil || stmtIdx < 0 || stmtIdx >= len(c.Prog.Stmts) {
		return
	}
	s := &c.Prog.Stmts[stmtIdx]
	if opIdx < 0 || opIdx >= len(s.Ops) {
		return
	}
	c.ReportRange(s.Ops[opIdx].Start, s.Ops[opIdx].End, msg, fixes...)
}

// byteRange converts byte offsets of the program document to a range.
func (c *Context) byteRange(start, end int) (protocol.Range, bool) {
	if c.Prog == nil || c.Prog.Doc == nil || start < 0 || end < start || end > len(c.Prog.Doc.Text) {
		return protocol.Range{}, false
	}
	d := c.Prog.Doc
	return protocol.Range{Start: d.Position(start), End: d.Position(end)}, true
}

// NewFix builds a quick fix from edits.
func NewFix(title string, edits ...protocol.TextEdit) Fix {
	return Fix{Title: title, Edits: edits}
}

// ReplaceRange is an edit replacing the bytes [start, end) of the program
// document with text. Out-of-range offsets clamp to the document.
func (c *Context) ReplaceRange(start, end int, text string) protocol.TextEdit {
	n := 0
	if c.Prog != nil && c.Prog.Doc != nil {
		n = len(c.Prog.Doc.Text)
	}
	start = max(0, min(start, n))
	end = max(start, min(end, n))
	r, _ := c.byteRange(start, end)
	return protocol.TextEdit{Range: r, NewText: text}
}

// InsertBefore is an edit inserting text immediately before statement
// stmtIdx (before its labels, if it has any). Text is inserted as given:
// the caller supplies any separator ("cld; " or "cld\n\t").
func (c *Context) InsertBefore(stmtIdx int, text string) protocol.TextEdit {
	off := 0
	if c.Prog != nil && stmtIdx >= 0 && stmtIdx < len(c.Prog.Stmts) {
		off = c.Prog.Stmts[stmtIdx].Start
	}
	return c.ReplaceRange(off, off, text)
}

// InsertLineAbove is an edit adding text as a new line above the line of
// statement stmtIdx, indented like that line, so an inserted `cld` lines
// up with the code it protects.
func (c *Context) InsertLineAbove(stmtIdx int, text string) protocol.TextEdit {
	if c.Prog == nil || c.Prog.Doc == nil || stmtIdx < 0 || stmtIdx >= len(c.Prog.Stmts) {
		return c.ReplaceRange(0, 0, text+"\n")
	}
	src := c.Prog.Doc.Text
	off := min(c.Prog.Stmts[stmtIdx].Start, len(src))
	lineStart := off
	for lineStart > 0 && src[lineStart-1] != '\n' && src[lineStart-1] != '\r' {
		lineStart--
	}
	indentEnd := lineStart
	for indentEnd < len(src) && (src[indentEnd] == ' ' || src[indentEnd] == '\t') {
		indentEnd++
	}
	// Keep the file's own line ending: a lone \n in a CRLF file shows up
	// as a stray change in every diff of the line.
	eol := "\n"
	if i := strings.IndexByte(src[lineStart:], '\n'); i > 0 && src[lineStart+i-1] == '\r' {
		eol = "\r\n"
	}
	return c.ReplaceRange(lineStart, lineStart, src[lineStart:indentEnd]+text+eol)
}
