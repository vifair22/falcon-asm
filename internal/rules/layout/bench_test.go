package layout

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// BenchmarkLayoutRules is what this package adds to a publish without a
// listing (only org-overlap runs then) and with one (every rule, over a
// listing-free result so the cost is the rules' own scanning).
func BenchmarkLayoutRules(b *testing.B) {
	text := benchdata.Large(b, 100000)
	// A store document, as the engine has: it carries its line index.
	st := docstore.New()
	st.Open("file:///x/large.S", 1, text)
	doc, _ := st.Get("file:///x/large.S")
	toks := lexer.Tokenize(text)
	p := asm.Parse(doc, toks, asm.Config{})
	p.Doc, p.Toks = doc, toks
	var rules []*lint.Rule
	for _, c := range allCodes {
		r, _ := lint.Lookup(c)
		rules = append(rules, r)
	}
	b.ReportAllocs()
	for b.Loop() {
		lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Rules: rules})
	}
}
