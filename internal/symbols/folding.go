package symbols

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// blockFamily pairs the directives that open, split and close one kind of
// foldable block. Each family keeps its own stack, so a mismatched .endif
// cannot close a #if or a .macro.
type blockFamily struct {
	open  func(d string) bool
	split func(d string) bool
	close func(d string) bool
}

var asmFamilies = []blockFamily{
	{ // .macro ... .endm
		open:  func(d string) bool { return d == ".macro" },
		split: func(string) bool { return false },
		close: func(d string) bool { return d == ".endm" || d == ".endmacro" },
	},
	{ // .rept/.irp/.irpc ... .endr
		open:  func(d string) bool { return d == ".rept" || d == ".irp" || d == ".irpc" },
		split: func(string) bool { return false },
		close: func(d string) bool { return d == ".endr" },
	},
	{ // .if* ... .else/.elseif ... .endif
		open:  func(d string) bool { return strings.HasPrefix(d, ".if") },
		split: func(d string) bool { return d == ".else" || strings.HasPrefix(d, ".elseif") },
		close: func(d string) bool { return d == ".endif" },
	},
}

var cppFamily = blockFamily{
	open:  func(d string) bool { return d == "if" || d == "ifdef" || d == "ifndef" },
	split: func(d string) bool { return d == "else" || strings.HasPrefix(d, "elif") },
	close: func(d string) bool { return d == "endif" },
}

// FoldingRanges returns foldable regions: multi-line block comments, runs
// of two or more whole-line comments, .macro/.endm, .rept/.endr, .if/.endif
// and #if/#endif. An #else or .else ends the branch before it and starts a
// new one, the way C editors fold conditionals, so either branch can be
// hidden on its own. The closing line stays visible.
func FoldingRanges(doc *docstore.Document) []protocol.FoldingRange {
	toks := lexer.Tokenize(doc.Text)
	var out []protocol.FoldingRange
	add := func(start, end int, kind string) {
		if end > start {
			out = append(out, protocol.FoldingRange{StartLine: start, EndLine: end, Kind: kind})
		}
	}

	stacks := make([][]int, len(asmFamilies)+1)
	region := func(fam int, b blockFamily, d string, line int) {
		st := stacks[fam]
		switch {
		case b.open(d):
			stacks[fam] = append(st, line)
		case b.split(d):
			if n := len(st); n > 0 {
				add(st[n-1], line-1, protocol.FoldingRegion)
				st[n-1] = line
			}
		case b.close(d):
			if n := len(st); n > 0 {
				add(st[n-1], line-1, protocol.FoldingRegion)
				stacks[fam] = st[:n-1]
			}
		}
	}

	blockStart := -1 // line of an open /* still waiting for */
	runStart, runEnd := -1, -1
	flushRun := func() {
		if runStart >= 0 {
			add(runStart, runEnd, protocol.FoldingComment)
		}
		runStart, runEnd = -1, -1
	}
	lastLine := -1 // line of the previous token, to spot whole-line comments

	for _, t := range toks {
		wholeLine := t.Line != lastLine
		lastLine = t.Line
		if t.Kind != lexer.Comment && wholeLine {
			flushRun()
		}
		switch t.Kind {
		case lexer.Comment:
			switch {
			case blockStart >= 0:
				if strings.HasSuffix(t.Text, "*/") {
					add(blockStart, t.Line, protocol.FoldingComment)
					blockStart = -1
				}
			case strings.HasPrefix(t.Text, "/*"):
				if !(len(t.Text) >= 4 && strings.HasSuffix(t.Text, "*/")) {
					flushRun()
					blockStart = t.Line
				}
			case wholeLine:
				if runStart >= 0 && t.Line == runEnd+1 {
					runEnd = t.Line
				} else {
					flushRun()
					runStart, runEnd = t.Line, t.Line
				}
			}
		case lexer.Directive:
			d := strings.ToLower(t.Text)
			for k, b := range asmFamilies {
				region(k, b, d, t.Line)
			}
		case lexer.CppDirective:
			region(len(asmFamilies), cppFamily, cppName(t), t.Line)
		}
	}
	flushRun()
	if blockStart >= 0 && len(toks) > 0 {
		add(blockStart, toks[len(toks)-1].Line, protocol.FoldingComment)
	}
	return out
}
