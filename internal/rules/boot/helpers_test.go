package boot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

var defineRe = regexp.MustCompile(`(?m)^#define[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]+(.*)$`)

// headerMacros reads the object-like #defines of testdata/biosdefs.h, the
// header every airies-boot fixture includes.
func headerMacros(t testing.TB) map[string]string {
	t.Helper()
	b, err := os.ReadFile("testdata/biosdefs.h")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, g := range defineRe.FindAllStringSubmatch(string(b), -1) {
		v := g[2]
		if k := strings.Index(v, "/*"); k >= 0 {
			v = v[:k]
		}
		v = strings.TrimSpace(v)
		v = strings.TrimSuffix(strings.TrimSuffix(v, "ull"), "u")
		if v != "" {
			m[g[1]] = v
		}
	}
	return m
}

func parse(t testing.TB, uri, text string, mode asm.Mode, macros map[string]string) *asm.Program {
	t.Helper()
	doc := &docstore.Document{URI: uri, Text: text}
	toks := lexer.Tokenize(text)
	return asm.Parse(doc, toks, asm.Config{InitialMode: mode, Macros: macros})
}

func fixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type opts struct {
	mode    asm.Mode
	macros  map[string]string
	listing *toolchain.AsmResult
	uri     string
}

func lintText(t testing.TB, text string, o opts, codes ...string) []lint.Finding {
	t.Helper()
	var rules []*lint.Rule
	for _, c := range codes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		rules = append(rules, r)
	}
	if o.mode == 0 {
		o.mode = asm.Mode16
	}
	if o.uri == "" {
		o.uri = "file:///t.S"
	}
	p := parse(t, o.uri, text, o.mode, o.macros)
	return lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: lint.Target{Mode: o.mode}, Listing: o.listing,
		Tables: hwdb.Default(), Rules: rules})
}

func mustHave(t testing.TB, fs []lint.Finding, code, sub string) lint.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Code == code && strings.Contains(f.Message, sub) {
			return f
		}
	}
	t.Fatalf("no %s finding containing %q in %v", code, sub, fs)
	return lint.Finding{}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func replace(s, from, to string) string { return strings.Replace(s, from, to, 1) }

// assemble writes text to dir/name and runs the real toolchain on it.
func assemble(t *testing.T, dir, name, text string) (string, *toolchain.AsmResult) {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not installed")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	var r toolchain.Runner
	res, err := r.Assemble(context.Background(), path, toolchain.Flags{Compiler: "gcc", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return path, res
}

// lintAssembled assembles text and lints it with its listing.
func lintAssembled(t *testing.T, dir, name, text string, macros map[string]string, codes ...string) []lint.Finding {
	t.Helper()
	path, res := assemble(t, dir, name, text)
	return lintText(t, text, opts{macros: macros, listing: res, uri: toolchain.PathToURI(path)}, codes...)
}
