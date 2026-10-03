package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
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

// program is the version parsed as an assembly program, with the target
// mode and macros the lint rules parse it with: one parse per version,
// shared by the lint run, the hardware hover, the register hints and the
// code lenses.
func (s *snapshot) program() *asm.Program {
	s.progOnce.Do(func() {
		s.prog = asm.Parse(s.doc, s.tokens(), asm.Config{InitialMode: s.targetOf().Mode, Macros: s.cfg.Macros})
	})
	return s.prog
}

// flow is the CFG and dataflow solution of program(), once per version,
// shared the same way. Both are nil when the analysis panicked: a hover
// then loses the cross-label values (the index falls back to block-local
// ones), and the lint rules that need flow are skipped, never the rest.
func (s *snapshot) flow() (*flow.CFG, *flow.Result) {
	s.flowOnce.Do(func() {
		defer func() {
			if p := recover(); p != nil {
				if s.logf != nil {
					s.logf("flow analysis panicked: %v", p)
				}
				s.cfgG, s.flowRes = nil, nil
			}
		}()
		g := flow.Build(s.program())
		s.cfgG = g
		s.flowRes = flow.Analyze(g, flow.Config{})
	})
	return s.cfgG, s.flowRes
}

// hwIndex is built with the dataflow solution, so hover sees values that
// cross labels (`movb $0x42, %ah` behind 2:, the int after 4:). It is the
// same index the hardware rules use (hw.Provide), built once per version
// by whichever asks first: the debounced lint run or a hover.
func (s *snapshot) hwIndex() *hw.Index {
	s.hw.once.Do(func() {
		_, res := s.flow()
		s.hw.ix = hw.NewIndexFlow(s.program(), res)
	})
	return s.hw.ix
}

// mayHaveHardware reports whether the hardware index can say anything
// about the token at pos, from the tokens alone. The index costs a parse,
// the dataflow and a scan of the file on the first hover of a version;
// a hover on `add`, a label, a comment or a mov between general
// registers would pay that for nothing, and those are most hovers.
func (s *snapshot) mayHaveHardware(pos protocol.Position) bool {
	off, err := s.doc.Offset(pos)
	if err != nil {
		return false
	}
	lineStart, _ := s.doc.Offset(protocol.Position{Line: pos.Line})
	col := off - lineStart
	toks := s.tokens()
	lo := sort.Search(len(toks), func(i int) bool { return toks[i].Line >= pos.Line })
	hi := lo
	for hi < len(toks) && toks[hi].Line == pos.Line {
		hi++
	}
	line := toks[lo:hi]
	var at *lexer.Token
	for i := range line {
		t := &line[i]
		if t.Col <= col && col <= t.Col+t.Len && (at == nil || t.Kind != lexer.Punct) {
			at = t
		}
	}
	if at == nil {
		return false
	}
	switch at.Kind {
	case lexer.Comment, lexer.LabelDef, lexer.LocalLabelDef, lexer.CppDirective, lexer.CppArg, lexer.Prefix, lexer.String:
		return false
	case lexer.Mnemonic:
		if !hw.MnemonicMayHover(at.Text) {
			return false
		}
		// mov only says something when it writes a control register.
		if m := strings.ToLower(at.Text); m == "mov" || len(m) == 4 && strings.HasPrefix(m, "mov") {
			for _, t := range line {
				r := strings.ToLower(t.Text)
				if t.Kind == lexer.Register && (strings.HasPrefix(r, "%cr") || strings.HasPrefix(r, "%xcr")) {
					return true
				}
			}
			return false
		}
	}
	return true
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
	md = trimHardware(md)
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

// maxHardwareLines caps the hardware part of a hover, in non-blank lines.
// The hardware tables are reference cards (every quirk of a BIOS service,
// every field of a structure); a hover is read in passing, and the design
// keeps the instruction hover to about six lines, so what is appended
// must stay of the same order.
const maxHardwareLines = 12

// trimHardware makes the hardware tables' markdown fit a hover: it keeps
// what is specific to the operand (the register or service, its bits, the
// value written, warnings) and drops the reference material (Notes and
// Quirks lists, structure layout tables, confidence remarks), drops any
// paragraph an earlier section already said (two roles of one constant
// render the same port twice), and cuts what is left at maxHardwareLines.
func trimHardware(md string) string {
	if md == "" {
		return ""
	}
	seen := map[string]bool{}
	var sections []string
	for _, sec := range strings.Split(md, "\n\n---\n\n") {
		paras := strings.Split(sec, "\n\n")
		var kept []string
		for i := 0; i < len(paras); i++ {
			p := strings.TrimSpace(paras[i])
			switch {
			case p == "":
				continue
			case p == "Notes:" || p == "Quirks:":
				// The list that follows is the block's body.
				if i+1 < len(paras) && strings.HasPrefix(strings.TrimSpace(paras[i+1]), "- ") {
					i++
				}
				continue
			case strings.HasPrefix(p, "_Confidence:"), strings.HasPrefix(p, "|"):
				continue
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			kept = append(kept, p)
		}
		if len(kept) > 0 {
			sections = append(sections, strings.Join(kept, "\n\n"))
		}
	}
	return capLines(strings.Join(sections, "\n\n---\n\n"), maxHardwareLines)
}

// capLines keeps the first max non-blank lines of md and says how many
// were left out.
func capLines(md string, maxLines int) string {
	lines := strings.Split(md, "\n")
	n := 0
	for i, l := range lines {
		if strings.TrimSpace(l) == "" || l == "---" {
			continue
		}
		n++
		if n > maxLines {
			rest := 0
			for _, r := range lines[i:] {
				if strings.TrimSpace(r) != "" && r != "---" {
					rest++
				}
			}
			kept := strings.TrimRight(strings.Join(lines[:i], "\n"), "\n-")
			unit := "lines"
			if rest == 1 {
				unit = "line"
			}
			return fmt.Sprintf("%s\n\n_(%d more %s)_", strings.TrimRight(kept, "\n"), rest, unit)
		}
	}
	return md
}
