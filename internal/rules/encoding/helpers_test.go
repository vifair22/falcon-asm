package encoding

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

var allCodes = []string{
	CodeNoForm, CodeSizeMismatch, CodeSizeAmbiguous, CodeREXHighByte, CodeAddr16, CodeAddrReg,
	CodeSreg, CodeFixedReg, CodeImmRange, CodeDataRange, CodePrefix, CodeAVXDeco, CodeBranchRange,
}

func rules(t testing.TB) []*lint.Rule {
	t.Helper()
	var out []*lint.Rule
	for _, c := range allCodes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		out = append(out, r)
	}
	return out
}

func parse(text string, mode asm.Mode) *asm.Program {
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	toks := lexer.Tokenize(text)
	return asm.Parse(doc, toks, asm.Config{InitialMode: mode})
}

// lintText runs this package's rules over text assembled in mode.
func lintText(t testing.TB, text string, mode asm.Mode, lang lint.Lang, lst *toolchain.AsmResult) []lint.Finding {
	t.Helper()
	return lint.Run(lint.Input{
		Prog: parse(text, mode), Lang: lang, Target: lint.Target{Mode: mode}, Rules: rules(t), Listing: lst,
	})
}

// summary renders findings as "code@line" in order.
func summary(fs []lint.Finding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = fmt.Sprintf("%s@%d", f.Code, f.Range.Start.Line)
	}
	return strings.Join(parts, " ")
}

// applyFix applies a fix's edits to text.
func applyFix(t testing.TB, text string, fx lint.Fix) string {
	t.Helper()
	doc := &docstore.Document{Text: text}
	edits := append([]protocol.TextEdit(nil), fx.Edits...)
	sort.Slice(edits, func(i, j int) bool {
		a, _ := doc.Offset(edits[i].Range.Start)
		b, _ := doc.Offset(edits[j].Range.Start)
		return a > b
	})
	for _, e := range edits {
		s, err1 := doc.Offset(e.Range.Start)
		en, err2 := doc.Offset(e.Range.End)
		if err1 != nil || err2 != nil {
			t.Fatalf("bad edit %+v", e)
		}
		text = text[:s] + e.NewText + text[en:]
	}
	return text
}

// TestDump prints this package's findings for the files named in
// FALCON_ENC_DUMP (comma-separated, each optionally "path:mode"); a
// development aid for checking a corpus, skipped otherwise.
func TestDump(t *testing.T) {
	spec := os.Getenv("FALCON_ENC_DUMP")
	if spec == "" {
		t.Skip("FALCON_ENC_DUMP not set")
	}
	for _, item := range strings.Split(spec, ",") {
		path, mode := item, asm.Mode64
		if i := strings.LastIndexByte(item, ':'); i > 0 {
			switch item[i+1:] {
			case "16":
				mode = asm.Mode16
			case "32":
				mode = asm.Mode32
			}
			path = item[:i]
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		lines := strings.Split(text, "\n")
		for _, f := range lintText(t, text, mode, lint.LangAsm, nil) {
			line := ""
			if f.Range.Start.Line < len(lines) {
				line = strings.TrimSpace(lines[f.Range.Start.Line])
			}
			fmt.Printf("%s:%d: %s: %s\t| %s\n", path, f.Range.Start.Line+1, f.Code, f.Message, line)
		}
	}
}
