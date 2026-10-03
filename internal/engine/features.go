package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/completion"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hover"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/semtok"
	"git.airies.net/vifair22/falcon-asm/internal/server"
	"git.airies.net/vifair22/falcon-asm/internal/symbols"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// Hover implements server.Engine. Assembly gets the full hover with macro
// values; C gets hover only inside asm statements, the rest is clangd's.
func (e *Engine) Hover(doc *docstore.Document, pos protocol.Position) (*protocol.Hover, error) {
	s := e.snapshotFor(doc)
	switch s.lang {
	case langConfig:
		return nil, nil
	case langC:
		h, err := s.inlineHover(pos)
		if h == nil && err == nil {
			h = s.cHelperHover(pos)
		}
		return h, err
	}
	h, err := hover.HoverWith(doc, pos, hover.Options{Tokens: s.tokens(), Macro: s.macro, Expansion: s.expansion})
	if err != nil {
		return h, err
	}
	return s.withHardware(pos, h), nil
}

// SemanticTokens implements server.Engine. C documents get none: the
// registration selector already keeps VS Code from asking, and other
// clients must not have clangd's highlighting overwritten.
func (e *Engine) SemanticTokens(doc *docstore.Document) []uint32 {
	s := e.snapshotFor(doc)
	if s.lang != langAsm {
		return nil
	}
	var deprecated map[protocol.Position]bool
	for _, d := range s.analysis().Diagnostics {
		if d.Code == analysis.CodeModeInstruction || d.Code == analysis.CodeModeRegister {
			if deprecated == nil {
				deprecated = map[protocol.Position]bool{}
			}
			deprecated[d.Range.Start] = true
		}
	}
	opts := semtok.Options{Tokens: s.tokens(), Deprecated: deprecated}
	if len(s.macros) > 0 {
		opts.IsMacro = func(name string) bool { _, ok := s.macros[name]; return ok }
	}
	return semtok.EncodeWith(doc, opts)
}

// Definition implements server.Engine: the index first, then the toolchain's
// record of where a macro from a header or the command line was defined.
func (e *Engine) Definition(doc *docstore.Document, pos protocol.Position) []protocol.Location {
	s := e.snapshotFor(doc)
	if s.lang != langAsm {
		return nil
	}
	if locs := e.index.Definition(doc.URI, pos); len(locs) > 0 {
		return locs
	}
	if name := s.wordAt(pos); name != "" {
		if m, ok := s.macros[name]; ok && m.Def != nil {
			return []protocol.Location{*m.Def}
		}
	}
	return nil
}

// References implements server.Engine.
func (e *Engine) References(doc *docstore.Document, pos protocol.Position, includeDecl bool) []protocol.Location {
	if e.snapshotFor(doc).lang != langAsm {
		return nil
	}
	return e.index.References(doc.URI, pos, includeDecl)
}

// PrepareRename implements server.Engine.
func (e *Engine) PrepareRename(doc *docstore.Document, pos protocol.Position) (*protocol.PrepareRenameResult, error) {
	if e.snapshotFor(doc).lang != langAsm {
		return nil, nil
	}
	r, placeholder, err := e.index.PrepareRename(doc.URI, pos)
	if err != nil {
		return nil, userFacing(err)
	}
	return &protocol.PrepareRenameResult{Range: r, Placeholder: placeholder}, nil
}

// Rename implements server.Engine.
func (e *Engine) Rename(doc *docstore.Document, pos protocol.Position, newName string) (*protocol.WorkspaceEdit, error) {
	if e.snapshotFor(doc).lang != langAsm {
		return nil, nil
	}
	edit, err := e.index.Rename(doc.URI, pos, newName)
	if err != nil {
		return nil, userFacing(err)
	}
	return edit, nil
}

// userFacing marks a refused rename for the user; anything else stays an
// internal error.
func userFacing(err error) error {
	if errors.Is(err, symbols.ErrRefused) {
		return &server.RequestFailedError{Err: err}
	}
	return fmt.Errorf("rename: %w", err)
}

// DocumentSymbols implements server.Engine.
func (e *Engine) DocumentSymbols(doc *docstore.Document) []protocol.DocumentSymbol {
	s := e.snapshotFor(doc)
	if s.lang != langAsm {
		return nil
	}
	return s.symbols().DocumentSymbols()
}

// FoldingRanges implements server.Engine.
func (e *Engine) FoldingRanges(doc *docstore.Document) []protocol.FoldingRange {
	s := e.snapshotFor(doc)
	if s.lang != langAsm {
		return nil
	}
	return symbols.FoldingRangesTokens(s.tokens())
}

// Completion implements server.Engine.
func (e *Engine) Completion(doc *docstore.Document, pos protocol.Position) *protocol.CompletionList {
	s := e.snapshotFor(doc)
	if s.lang != langAsm {
		return nil
	}
	return completion.Complete(doc, pos, completion.Context{
		Mode:   s.modeAt(pos.Line),
		Tokens: s.tokens(),
		Labels: e.index.Labels,
		Macros: func() []string { return mergeNames(e.index.Macros(), projectMacros(s.macros)) },
	})
}

// projectMacros lists toolchain macros defined in the project's own files.
// The table also holds a few hundred compiler builtins (__GNUC__ ...) that
// would drown the operand completions a user actually wants.
func projectMacros(m map[string]toolchain.Macro) []string {
	var out []string
	for name, mac := range m {
		if mac.Def != nil {
			out = append(out, name)
		}
	}
	return out
}

func mergeNames(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, n := range list {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// wordAt returns the symbol-like token text at pos ("$NAME" without the
// sigil), or "".
func (s *snapshot) wordAt(pos protocol.Position) string {
	off, err := s.doc.Offset(pos)
	if err != nil {
		return ""
	}
	lineStart, _ := s.doc.Offset(protocol.Position{Line: pos.Line})
	col := off - lineStart
	for _, t := range s.tokens() {
		if t.Line != pos.Line || col < t.Col || col > t.Col+t.Len {
			continue
		}
		switch t.Kind {
		case lexer.LabelRef, lexer.Identifier, lexer.Mnemonic:
			return t.Text
		case lexer.Immediate:
			return strings.TrimPrefix(t.Text, "$")
		}
	}
	return ""
}

// InlayHints implements server.Engine: per-line offsets and encoded bytes
// from the last listing, and padding counts on location moves. Hints are
// only given while the buffer still matches the file that was assembled;
// after an edit, lines may have moved and a hint on the wrong line is worse
// than none.
func (e *Engine) InlayHints(doc *docstore.Document, rng protocol.Range) []protocol.InlayHint {
	e.mu.Lock()
	st := e.docs[doc.URI]
	regs := e.regHints && st != nil && st.lang == langAsm
	if !e.listingHints || st == nil || st.lang != langAsm || st.asm == nil || st.asmVersion != doc.Version {
		e.mu.Unlock()
		return e.registerHints(regs, doc, rng, nil)
	}
	res := st.asm
	e.mu.Unlock()
	return e.registerHints(regs, doc, rng, listingHints(doc, res, rng))
}

// maxHintBytes is how many encoded bytes a hint shows before "...": enough
// for most instructions, short enough not to push long lines off screen.
const maxHintBytes = 8

func listingHints(doc *docstore.Document, res *toolchain.AsmResult, rng protocol.Range) []protocol.InlayHint {
	var out []protocol.InlayHint
	for i, l := range res.Listing {
		if l.Line < rng.Start.Line || l.Line > rng.End.Line {
			continue
		}
		text, ok := doc.Line(l.Line)
		if !ok {
			continue
		}
		pos := protocol.Position{Line: l.Line, Character: docstore.UTF16Len(text)}
		// A location move that fills nothing has no address in the
		// listing, but "pads 0 bytes" on a boot sector's ". = _start + 510"
		// is exactly the news that the budget is used up.
		if n, ok := res.PadBytes(l.Line); ok {
			tip := fmt.Sprintf("section %s: nothing to fill", l.Section)
			if l.HasOffset {
				tip = fmt.Sprintf("section %s: location counter moves from +0x%x to +0x%x",
					l.Section, l.Offset, l.Offset+uint64(n))
			}
			out = append(out, protocol.InlayHint{
				Position: pos, Label: fmt.Sprintf("pads %d bytes", n), PaddingLeft: true, Tooltip: tip,
			})
			continue
		}
		if !l.HasOffset || len(l.Bytes) == 0 {
			continue
		}
		shown := l.Bytes
		more := ""
		if len(shown) > maxHintBytes {
			shown, more = shown[:maxHintBytes], " ..."
		}
		hexes := make([]string, len(shown))
		for k, b := range shown {
			hexes[k] = fmt.Sprintf("%02x", b)
		}
		size := lineSize(res, i)
		unit := "bytes"
		if size == 1 {
			unit = "byte"
		}
		out = append(out, protocol.InlayHint{
			Position:    pos,
			Label:       fmt.Sprintf("+0x%x  %s%s", l.Offset, strings.Join(hexes, " "), more),
			PaddingLeft: true,
			Tooltip:     fmt.Sprintf("section %s, offset 0x%x, %d %s", l.Section, l.Offset, size, unit),
		})
	}
	return out
}

// lineSize is a listing line's true size. gas truncates long byte dumps, so
// the distance to the next offset in the same section (or the section end)
// is the authority, with the listed bytes as the floor.
func lineSize(res *toolchain.AsmResult, i int) int {
	l := res.Listing[i]
	size := len(l.Bytes)
	next := uint64(0)
	found := false
	for _, n := range res.Listing[i+1:] {
		if n.HasOffset && n.Section == l.Section && n.Offset > l.Offset {
			next, found = n.Offset, true
			break
		}
	}
	if !found {
		if end, ok := res.SectionSizes[l.Section]; ok && end > l.Offset {
			next, found = end, true
		}
	}
	if found && int(next-l.Offset) > size {
		size = int(next - l.Offset)
	}
	return size
}

// CodeActions implements server.Engine: the quick fixes of every finding
// touching rng, and for each finding an action adding a suppression
// comment for its code on its line. Fixes come from the snapshot's lint
// run, so they describe exactly the text the client holds.
func (e *Engine) CodeActions(doc *docstore.Document, rng protocol.Range, ctx protocol.CodeActionContext) []protocol.CodeAction {
	if !wantsQuickFix(ctx.Only) {
		return nil
	}
	s := e.snapshotFor(doc)
	if s.lang == langConfig {
		return nil
	}
	res := s.lintResult()
	var fixes, ignores []protocol.CodeAction
	ignored := map[string]bool{}
	for _, f := range res.Findings {
		if !f.Overlaps(rng) {
			continue
		}
		diag := []protocol.Diagnostic{f.Diagnostic()}
		for i, fx := range f.Fixes {
			fixes = append(fixes, protocol.CodeAction{
				Title:       fx.Title,
				Kind:        protocol.CodeActionKindQuickFix,
				Diagnostics: diag,
				IsPreferred: i == 0,
				Edit:        &protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{doc.URI: fx.Edits}},
			})
		}
		// Suppressing the report about a suppression would only stack
		// comments; removing the stale one is the fix.
		key := fmt.Sprintf("%d %s", f.Range.Start.Line, f.Code)
		if f.Code == lint.UnusedSuppressionCode || ignored[key] || res.Suppressions == nil {
			continue
		}
		ignored[key] = true
		fx := res.Suppressions.IgnoreFix(f.Range.Start.Line, f.Code)
		ignores = append(ignores, protocol.CodeAction{
			Title:       fx.Title,
			Kind:        protocol.CodeActionKindQuickFix,
			Diagnostics: diag,
			Edit:        &protocol.WorkspaceEdit{Changes: map[string][]protocol.TextEdit{doc.URI: fx.Edits}},
		})
	}
	// Real fixes first: a client showing one action shows the best one.
	return append(fixes, ignores...)
}

// wantsQuickFix honours the client's "only" filter. Kinds are
// hierarchical: a filter admits its own kind and the kinds below it, so
// only "quickfix" itself and the empty (root) kind admit ours; a filter
// for a sub-kind such as "quickfix.x" is narrower than what we offer.
func wantsQuickFix(only []string) bool {
	if len(only) == 0 {
		return true
	}
	for _, k := range only {
		if k == "" || k == protocol.CodeActionKindQuickFix {
			return true
		}
	}
	return false
}
