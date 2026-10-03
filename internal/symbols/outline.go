package symbols

import (
	"sort"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// sectionDirectives start a new outline section named by the directive
// itself; .section and .pushsection are named by their argument.
var sectionDirectives = map[string]bool{".text": true, ".data": true, ".bss": true}

type section struct {
	name string
	at   span
}

// DocumentSymbols builds the outline: sections at the top, and inside each
// the labels, .set/.equ symbols, .macro and #define names defined in it.
// Symbols before the first section are top level. Local numeric labels are
// left out; they are noise in an outline. A .set name repeated (the usual
// counter inside .rept) is listed once.
func DocumentSymbols(doc *docstore.Document) []protocol.DocumentSymbol {
	f := parse(doc.URI, doc.Text, isCHeader(uriKey(doc.URI)))
	secs := f.sections()
	macroEnd := f.macroEnds()

	// Boundaries close a label's range: the next label, section or macro.
	var bounds []int
	for _, s := range secs {
		bounds = append(bounds, s.at.line)
	}
	for _, d := range f.defs {
		if d.kind == kindLabel || d.kind == kindAsmMacro {
			bounds = append(bounds, d.at.line)
		}
	}
	sort.Ints(bounds)
	last := len(f.lines) - 1
	nextBound := func(line int) int {
		i := sort.SearchInts(bounds, line+1)
		if i < len(bounds) {
			return bounds[i] - 1
		}
		return last
	}

	top := make([]protocol.DocumentSymbol, 0, len(secs))
	for _, s := range secs {
		end := last
		for _, o := range secs {
			if o.at.line > s.at.line {
				end = o.at.line - 1
				break
			}
		}
		top = append(top, f.symbol(s.name, "", protocol.SymbolKindModule, s.at, end))
	}
	secOf := func(line int) int {
		k := -1
		for i, s := range secs {
			if s.at.line <= line {
				k = i
			}
		}
		return k
	}

	seenEqu := map[string]bool{}
	var loose []protocol.DocumentSymbol
	for _, d := range f.defs {
		var sym protocol.DocumentSymbol
		switch d.kind {
		case kindLabel:
			kind, detail := protocol.SymbolKindVariable, ""
			if f.globals[d.name] {
				kind, detail = protocol.SymbolKindFunction, ".globl"
			}
			sym = f.symbol(d.name, detail, kind, d.at, nextBound(d.at.line))
		case kindEqu:
			if seenEqu[d.name] {
				continue
			}
			seenEqu[d.name] = true
			sym = f.symbol(d.name, d.detail, protocol.SymbolKindVariable, d.at, d.at.line)
		case kindDefine:
			sym = f.symbol(d.name, d.detail, protocol.SymbolKindConstant, d.at, d.at.line)
		case kindAsmMacro:
			sym = f.symbol(d.name, d.detail, protocol.SymbolKindNamespace, d.at, macroEnd[d.at.line])
		}
		if k := secOf(d.at.line); k >= 0 {
			top[k].Children = append(top[k].Children, sym)
		} else {
			loose = append(loose, sym)
		}
	}
	return append(loose, top...)
}

// symbol builds an entry whose range runs from the name to the end of
// endLine, never ending before the name itself.
func (f *file) symbol(name, detail string, kind protocol.SymbolKind, at span, endLine int) protocol.DocumentSymbol {
	sel := f.rng(at)
	endLine = max(endLine, at.line)
	end := protocol.Position{Line: endLine, Character: f.lineEnd(endLine)}
	if endLine == at.line && end.Character < sel.End.Character {
		end.Character = sel.End.Character
	}
	return protocol.DocumentSymbol{
		Name:           name,
		Detail:         detail,
		Kind:           kind,
		Range:          protocol.Range{Start: sel.Start, End: end},
		SelectionRange: sel,
	}
}

// sections lists the section-switching directives in order. .previous and
// .popsection are not followed: the outline would need a section stack for
// a construct the fixtures never use.
func (f *file) sections() []section {
	var out []section
	for i, t := range f.toks {
		if t.Kind != lexer.Directive {
			continue
		}
		d := strings.ToLower(t.Text)
		switch {
		case sectionDirectives[d]:
			out = append(out, section{t.Text, tokSpan(t)})
		case d == ".section" || d == ".pushsection":
			name := t.Text
			if i+1 < len(f.toks) && f.toks[i+1].Line == t.Line && f.toks[i+1].Kind != lexer.Comment {
				name = f.toks[i+1].Text
			}
			out = append(out, section{name, tokSpan(t)})
		}
	}
	return out
}

// macroEnds maps each .macro line to its .endm line (or the last line when
// unterminated).
func (f *file) macroEnds() map[int]int {
	out := map[int]int{}
	var stack []int
	for _, t := range f.toks {
		if t.Kind != lexer.Directive {
			continue
		}
		switch strings.ToLower(t.Text) {
		case ".macro":
			stack = append(stack, t.Line)
		case ".endm", ".endmacro":
			if n := len(stack); n > 0 {
				out[stack[n-1]] = t.Line
				stack = stack[:n-1]
			}
		}
	}
	for _, l := range stack {
		out[l] = len(f.lines) - 1
	}
	return out
}
