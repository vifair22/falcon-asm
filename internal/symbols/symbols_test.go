package symbols

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// at returns the position of the nth (0-based) occurrence of needle in
// text, plus off bytes. Fixtures are ASCII, so bytes are UTF-16 units.
func at(t *testing.T, text, needle string, nth, off int) protocol.Position {
	t.Helper()
	i, from := -1, 0
	for k := 0; k <= nth; k++ {
		j := strings.Index(text[from:], needle)
		if j < 0 {
			t.Fatalf("occurrence %d of %q not found", nth, needle)
		}
		i = from + j
		from = i + 1
	}
	i += off
	line := strings.Count(text[:i], "\n")
	col := i - (strings.LastIndex(text[:i], "\n") + 1)
	return protocol.Position{Line: line, Character: col}
}

type lc struct {
	file string // base name of the URI
	line int    // zero based
	col  int
}

func locs(ls []protocol.Location) []lc {
	out := make([]lc, 0, len(ls))
	for _, l := range ls {
		out = append(out, lc{filepath.Base(uriKey(l.URI)), l.Range.Start.Line, l.Range.Start.Character})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		return out[i].col < out[j].col
	})
	return out
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs("../../testdata/falcon-bios")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readFixture(t *testing.T, name string) (uri, text string) {
	t.Helper()
	p := filepath.Join(fixtureDir(t), name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return pathURI(p), string(b)
}

// fixtureIndex indexes the falcon-bios tree with stage2.S open.
func fixtureIndex(t *testing.T) (*Index, string, string) {
	t.Helper()
	ix := NewIndex(nil)
	ix.AddRoot(fixtureDir(t))
	uri, text := readFixture(t, "stage2.S")
	ix.Update(uri, text, nil)
	return ix, uri, text
}

func lineOf(t *testing.T, name, needle string) int {
	t.Helper()
	_, text := readFixture(t, name)
	i := strings.Index(text, needle)
	if i < 0 {
		t.Fatalf("%q not in %s", needle, name)
	}
	return strings.Count(text[:i], "\n")
}

func TestDefinitionFixtures(t *testing.T) {
	ix, uri, text := fixtureIndex(t)
	tests := []struct {
		name   string
		needle string
		nth    int
		off    int
		want   []lc
	}{
		{"global label in another file", "jmp     pmode_enter", 0, 9,
			[]lc{{"pmode.S", lineOf(t, "pmode.S", "pmode_enter:"), 0}}},
		{"global data label via operand", "$stage2_32, pmode_entry", 0, 13,
			[]lc{{"pmode.S", lineOf(t, "pmode.S", "pmode_entry:"), 0}}},
		{"extern operand", ".extern fault_install", 0, 9,
			[]lc{{"idt.S", lineOf(t, "idt.S", "fault_install:"), 0}}},
		{"immediate names local label", "$stage2_32", 0, 3,
			[]lc{{"stage2.S", lineOf(t, "stage2.S", "stage2_32:"), 0}}},
		{"private label resolves in this file", "call    puts", 0, 9,
			[]lc{{"stage2.S", lineOf(t, "stage2.S", "\nputs:") + 1, 0}}},
		{"definition token itself", "\nputs:", 0, 2,
			[]lc{{"stage2.S", lineOf(t, "stage2.S", "\nputs:") + 1, 0}}},
		{"undefined extern", "call    stage3_load", 0, 9, []lc{}},
		{"mnemonic is not a symbol", "jmp     pmode_enter", 0, 1, nil},
		{"register is not a symbol", "%esp", 0, 2, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ix.Definition(uri, at(t, text, tc.needle, tc.nth, tc.off))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if g := locs(got); !reflect.DeepEqual(g, tc.want) {
				t.Fatalf("got %v, want %v", g, tc.want)
			}
		})
	}
}

func TestDefinitionUnknownDocAndPosition(t *testing.T) {
	ix, uri, _ := fixtureIndex(t)
	if got := ix.Definition("file:///nope.S", protocol.Position{}); got != nil {
		t.Fatalf("unknown doc: %v", got)
	}
	if got := ix.Definition(uri, protocol.Position{Line: 100000}); got != nil {
		t.Fatalf("past end: %v", got)
	}
	if got := ix.Definition(uri, protocol.Position{Line: 0, Character: -1}); got != nil {
		t.Fatalf("negative: %v", got)
	}
	// Blank space between tokens.
	if got := ix.Definition(uri, protocol.Position{Line: 0, Character: 0}); got != nil {
		t.Fatalf("comment start: %v", got)
	}
}

// writeFile creates dir/name with text and returns its absolute path.
func writeFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const headerText = `/* biosdefs-like */
#ifndef DEFS_H
#define DEFS_H
#define STACK_TOP   0x7C00
#define SLOT(n)     (STACK_TOP + (n) * 4)
struct x { int y; };
default:
#endif
`

const asmHeaderText = `	.macro PUTC ch
	movb $\ch, %al
	.endm
	.globl hdr_func
hdr_func:
	ret
SHARED_EQU = 3
`

const mainText = `#include <start/defs.h>
#include "asm.inc"
	.text
	.globl _start
_start:
	movl    $STACK_TOP, %esp
	movl    SLOT(2), %eax
	PUTC    'x'
	call    hdr_func
	movl    $SHARED_EQU, %eax
	movl    $LOCAL_C, %eax
	call    helper
2:	jmp     2b
	jmp     1f
1:	jmp     1b
	jmp     3f
	jz      1b ; 1: jmp 1f
1:	ret
#define LOCAL_C 5
helper:	ret
	.set    count, 0
	.set    count, count + 1
`

func headerIndex(t *testing.T) (*Index, string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	hdr := writeFile(t, dir, "include/start/defs.h", headerText)
	inc := writeFile(t, dir, "src/asm.inc", asmHeaderText)
	main := writeFile(t, dir, "src/main.S", mainText)
	ix := NewIndex(nil)
	uri := pathURI(main)
	ix.Update(uri, mainText, []string{hdr, inc})
	return ix, uri, hdr, inc, main
}

func TestDefinitionHeadersAndLocals(t *testing.T) {
	ix, uri, _, _, _ := headerIndex(t)
	tests := []struct {
		name   string
		needle string
		nth    int
		off    int
		want   []lc
	}{
		{"define in C header via immediate", "$STACK_TOP", 0, 2, []lc{{"defs.h", 3, 8}}},
		{"function-like define", "SLOT(2)", 0, 1, []lc{{"defs.h", 4, 8}}},
		{"asm macro in asm header", "PUTC    'x'", 0, 1, []lc{{"asm.inc", 0, 8}}},
		{"global label in asm header", "call    hdr_func", 0, 10, []lc{{"asm.inc", 4, 0}}},
		{"equ in asm header", "$SHARED_EQU", 0, 3, []lc{{"asm.inc", 6, 0}}},
		{"define later in doc", "$LOCAL_C", 0, 2, []lc{{"main.S", 18, 8}}},
		{"label later in doc", "call    helper", 0, 10, []lc{{"main.S", 19, 0}}},
		{"repeated .set gives all", "count + 1", 0, 1, []lc{{"main.S", 20, 9}, {"main.S", 21, 9}}},
		{"2: jmp 2b on one line", "2b", 0, 0, []lc{{"main.S", 12, 0}}},
		{"forward", "1f", 0, 1, []lc{{"main.S", 14, 0}}},
		{"backward", "1:\tjmp     1b", 0, 11, []lc{{"main.S", 14, 0}}},
		{"backward across statements", "jz      1b", 0, 8, []lc{{"main.S", 14, 0}}},
		{"forward after ; on same line", "1: jmp 1f", 0, 7, []lc{{"main.S", 17, 0}}},
		{"local def itself", "1: jmp 1f", 0, 0, []lc{{"main.S", 16, 14}}},
		{"dangling forward", "3f", 0, 0, []lc{}},
		{"cpp ifndef word in header context", "#include <start/defs.h>", 0, 3, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ix.Definition(uri, at(t, mainText, tc.needle, tc.nth, tc.off))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if g := locs(got); !reflect.DeepEqual(g, tc.want) {
				t.Fatalf("got %v, want %v", g, tc.want)
			}
		})
	}
}

func TestCHeaderIgnoresCLabels(t *testing.T) {
	_, _, hdr, _, _ := headerIndex(t)
	b, err := os.ReadFile(hdr)
	if err != nil {
		t.Fatal(err)
	}
	f := parse(pathURI(hdr), string(b), true)
	if len(f.byName["default"]) != 0 {
		t.Fatalf("C header label indexed: %v", f.byName)
	}
	want := []string{"DEFS_H", "STACK_TOP", "SLOT"}
	var got []string
	for _, d := range f.defs {
		got = append(got, d.name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defs %v, want %v", got, want)
	}
	if f.defs[2].detail != "(n)     (STACK_TOP + (n) * 4)" {
		t.Fatalf("detail %q", f.defs[2].detail)
	}
}

func TestDefinitionInHeaderDocument(t *testing.T) {
	// A cursor inside a #define body in the header resolves to the header.
	ix, _, hdr, _, _ := headerIndex(t)
	ix.Update(pathURI(hdr), headerText, nil)
	got := ix.Definition(pathURI(hdr), at(t, headerText, "(STACK_TOP", 0, 3))
	if g := locs(got); !reflect.DeepEqual(g, []lc{{"defs.h", 3, 8}}) {
		t.Fatalf("got %v", g)
	}
}

func TestResolveLocal(t *testing.T) {
	toks := lexer.Tokenize("1: jmp 1b\n jmp 1f\n1:\n jmp 9b\n mov %eax, %ebx\n")
	tests := []struct {
		idx  int
		want int
		ok   bool
	}{
		{0, 0, true},    // def resolves to itself
		{-1, -1, false}, // out of range
		{100, -1, false},
	}
	for _, tc := range tests {
		got, ok := ResolveLocal(toks, tc.idx)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ResolveLocal(%d) = %d,%v want %d,%v", tc.idx, got, ok, tc.want, tc.ok)
		}
	}
	find := func(text string, line int) int {
		for i, tk := range toks {
			if tk.Text == text && tk.Line == line {
				return i
			}
		}
		t.Fatalf("token %q line %d not found", text, line)
		return -1
	}
	if d, ok := ResolveLocal(toks, find("1b", 0)); !ok || toks[d].Line != 0 {
		t.Fatalf("1b -> %d %v", d, ok)
	}
	if d, ok := ResolveLocal(toks, find("1f", 1)); !ok || toks[d].Line != 2 {
		t.Fatalf("1f -> %d %v", d, ok)
	}
	if _, ok := ResolveLocal(toks, find("9b", 3)); ok {
		t.Fatal("dangling 9b resolved")
	}
	if _, ok := ResolveLocal(toks, find("mov", 4)); ok {
		t.Fatal("mnemonic resolved")
	}
}

func TestReferences(t *testing.T) {
	ix, uri, text := fixtureIndex(t)
	enterDef := lineOf(t, "pmode.S", "pmode_enter:")
	enterGlobl := lineOf(t, "pmode.S", ".globl  pmode_enter")
	extern := lineOf(t, "stage2.S", ".extern pmode_enter")
	jmp := lineOf(t, "stage2.S", "jmp     pmode_enter")

	got := locs(ix.References(uri, at(t, text, "jmp     pmode_enter", 0, 9), true))
	want := []lc{{"pmode.S", enterGlobl, 9}, {"pmode.S", enterDef, 0}, {"stage2.S", extern, 9}, {"stage2.S", jmp, 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("with decl: got %v want %v", got, want)
	}
	got = locs(ix.References(uri, at(t, text, "jmp     pmode_enter", 0, 9), false))
	want = []lc{{"pmode.S", enterGlobl, 9}, {"stage2.S", extern, 9}, {"stage2.S", jmp, 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without decl: got %v want %v", got, want)
	}

	// Private puts: only stage2.S, though six other fixtures have their own.
	for _, l := range ix.References(uri, at(t, text, "call    puts", 0, 9), true) {
		if filepath.Base(uriKey(l.URI)) != "stage2.S" {
			t.Fatalf("puts leaked into %s", l.URI)
		}
	}

	// $sym immediates count, at the symbol's column (after '$').
	s232 := locs(ix.References(uri, at(t, text, "stage2_32:", 0, 1), true))
	wantS := []lc{
		{"stage2.S", lineOf(t, "stage2.S", "$stage2_32"), 10},
		{"stage2.S", lineOf(t, "stage2.S", "stage2_32:"), 0},
	}
	if !reflect.DeepEqual(s232, wantS) {
		t.Fatalf("stage2_32: got %v want %v", s232, wantS)
	}

	// An unresolved extern is searched workspace-wide.
	if got := ix.References(uri, at(t, text, "call    stage3_load", 0, 9), true); len(got) < 2 {
		t.Fatalf("stage3_load refs: %v", got)
	}

	if got := ix.References(uri, at(t, text, "%esp", 0, 1), true); got != nil {
		t.Fatalf("register refs: %v", got)
	}
}

func TestReferencesHeaderAndLocal(t *testing.T) {
	ix, uri, hdr, _, main := headerIndex(t)
	// A second, disk-only file that includes the header by name.
	dir := filepath.Dir(main)
	other := writeFile(t, dir, "other.S", "#include <start/defs.h>\n\tmovl $STACK_TOP, %esp\n")
	// And one that does not include it but uses the name.
	writeFile(t, dir, "unrelated.S", "\tmovl $STACK_TOP, %esp\n")
	ix.AddRoot(dir)

	got := locs(ix.References(uri, at(t, mainText, "$STACK_TOP", 0, 2), true))
	want := []lc{
		{"defs.h", 3, 8},
		{"defs.h", 4, 21},
		{"main.S", 5, 10},
		{"other.S", 1, 7},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("header macro refs: got %v want %v", got, want)
	}
	_ = hdr
	_ = other

	got = locs(ix.References(uri, at(t, mainText, "1:\tjmp     1b", 0, 0), true))
	want = []lc{{"main.S", 13, 9}, {"main.S", 14, 0}, {"main.S", 14, 11}, {"main.S", 16, 9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("local refs: got %v want %v", got, want)
	}
	got = locs(ix.References(uri, at(t, mainText, "1:\tjmp     1b", 0, 0), false))
	if len(got) != 3 {
		t.Fatalf("local refs without decl: %v", got)
	}
	if got := ix.References(uri, at(t, mainText, "3f", 0, 0), true); got != nil {
		t.Fatalf("dangling local refs: %v", got)
	}

	// The .macro name and its invocation.
	got = locs(ix.References(uri, at(t, mainText, "PUTC", 0, 0), true))
	want = []lc{{"asm.inc", 0, 8}, {"main.S", 7, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("macro refs: got %v want %v", got, want)
	}
}

func editsByFile(e *protocol.WorkspaceEdit) map[string][]lc {
	out := map[string][]lc{}
	for uri, es := range e.Changes {
		var ls []protocol.Location
		for _, x := range es {
			ls = append(ls, protocol.Location{URI: uri, Range: x.Range})
		}
		out[filepath.Base(uriKey(uri))] = locs(ls)
	}
	return out
}

func TestRename(t *testing.T) {
	ix, uri, text := fixtureIndex(t)
	e, err := ix.Rename(uri, at(t, text, "jmp     pmode_enter", 0, 9), "pm_enter")
	if err != nil {
		t.Fatal(err)
	}
	got := editsByFile(e)
	if len(got) != 2 || len(got["pmode.S"]) != 2 || len(got["stage2.S"]) != 2 {
		t.Fatalf("global rename edits: %v", got)
	}
	for _, es := range e.Changes {
		for _, x := range es {
			if x.NewText != "pm_enter" || x.Range.End.Character-x.Range.Start.Character != len("pmode_enter") {
				t.Fatalf("bad edit %+v", x)
			}
		}
	}

	e, err = ix.Rename(uri, at(t, text, "call    puts", 0, 9), "print")
	if err != nil {
		t.Fatal(err)
	}
	got = editsByFile(e)
	if len(got) != 1 || len(got["stage2.S"]) < 5 {
		t.Fatalf("private rename edits: %v", got)
	}

	e, err = ix.Rename(uri, at(t, text, "call    puts", 0, 9), "puts")
	if err != nil || len(e.Changes) != 0 {
		t.Fatalf("same-name rename: %v %v", e, err)
	}
}

func TestRenameHeader(t *testing.T) {
	ix, uri, _, _, _ := headerIndex(t)
	e, err := ix.Rename(uri, at(t, mainText, "$STACK_TOP", 0, 2), "STACK_BASE")
	if err != nil {
		t.Fatal(err)
	}
	got := editsByFile(e)
	want := map[string][]lc{
		"defs.h": {{"defs.h", 3, 8}, {"defs.h", 4, 21}},
		"main.S": {{"main.S", 5, 10}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRenameRefusals(t *testing.T) {
	ix, uri, _, _, _ := headerIndex(t)
	tests := []struct {
		name    string
		needle  string
		off     int
		newName string
		msg     string
	}{
		{"local label", "2b", 0, "x", "local numeric label"},
		{"no symbol", "%esp", 1, "x", "no symbol"},
		{"unresolved", "3f", 0, "x", "local numeric"},
		{"mnemonic", "call    helper", 9, "movl", "instruction mnemonic"},
		{"mnemonic upper", "call    helper", 9, "MOV", "instruction mnemonic"},
		{"register", "call    helper", 9, "eax", "register"},
		{"prefix", "call    helper", 9, "lock", "prefix"},
		{"directive", "call    helper", 9, ".byte", "directive"},
		{"invalid", "call    helper", 9, "1abc", "not a valid symbol"},
		{"empty", "call    helper", 9, "", "not a valid symbol"},
		{"macro name with dot", "$LOCAL_C", 2, "A.B", "not a valid macro"},
		{"clash in doc", "call    helper", 9, "_start", "already defined"},
		{"clash in header", "call    helper", 9, "STACK_TOP", "already defined"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ix.Rename(uri, at(t, mainText, tc.needle, 0, tc.off), tc.newName)
			if err == nil || !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("err = %v, want refusal containing %q", err, tc.msg)
			}
		})
	}
}

func TestRenameRefusesUnresolvedAndShadowClash(t *testing.T) {
	ix, uri, text := fixtureIndex(t)
	_, err := ix.Rename(uri, at(t, text, "call    stage3_load", 0, 9), "x_load")
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("unresolved: %v", err)
	}
	// pmode.S (the home) has a private a20_ok.
	_, err = ix.Rename(uri, at(t, text, "jmp     pmode_enter", 0, 9), "a20_ok")
	if err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("home clash: %v", err)
	}
	// A third file that references the global and has its own private
	// name: neither the home's nor the caller's lookup sees it, so only the
	// per-edited-file check can refuse.
	dir := t.TempDir()
	writeFile(t, dir, "g.S", "\t.globl gf\ngf:\tret\n")
	writeFile(t, dir, "u.S", "\tcall gf\npriv:\tret\n")
	m := "\tcall gf\n"
	gix := NewIndex(nil)
	gix.AddRoot(dir)
	gix.Update("untitled:m", m, nil)
	_, err = gix.Rename("untitled:m", at(t, m, "gf", 0, 0), "priv")
	if err == nil || !strings.Contains(err.Error(), "u.S") {
		t.Fatalf("per-file clash: %v", err)
	}
	if _, err := ix.Rename("file:///none.S", protocol.Position{}, "x"); err == nil {
		t.Fatal("unknown doc renamed")
	}
}

func TestPrepareRename(t *testing.T) {
	ix, uri, _, _, _ := headerIndex(t)
	r, ph, err := ix.PrepareRename(uri, at(t, mainText, "$STACK_TOP", 0, 4))
	if err != nil || ph != "STACK_TOP" {
		t.Fatalf("prepare: %v %q", err, ph)
	}
	want := protocol.Range{Start: protocol.Position{Line: 5, Character: 10}, End: protocol.Position{Line: 5, Character: 19}}
	if r != want {
		t.Fatalf("range %v want %v", r, want)
	}
	if _, _, err := ix.PrepareRename(uri, at(t, mainText, "2b", 0, 0)); !errors.Is(err, ErrRefused) {
		t.Fatalf("local: %v", err)
	}
	if _, _, err := ix.PrepareRename(uri, at(t, mainText, "movl", 0, 0)); !errors.Is(err, ErrRefused) {
		t.Fatalf("mnemonic: %v", err)
	}
}

func TestLabelsAndMacros(t *testing.T) {
	ix, _, _, _, _ := headerIndex(t)
	labels := ix.Labels()
	for _, want := range []string{"_start", "helper", "count", "hdr_func"} {
		if !contains(labels, want) {
			t.Fatalf("Labels() = %v, missing %s", labels, want)
		}
	}
	if contains(labels, "SHARED_EQU") {
		t.Fatalf("header private equ listed: %v", labels)
	}
	macros := ix.Macros()
	for _, want := range []string{"STACK_TOP", "SLOT", "PUTC", "LOCAL_C"} {
		if !contains(macros, want) {
			t.Fatalf("Macros() = %v, missing %s", macros, want)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.S", "\t.globl alpha\nalpha:\tret\n")
	writeFile(t, dir, "sub dir/b.s", "\t.globl beta\nbeta:\tret\n")
	writeFile(t, dir, "build/gen.S", "\t.globl gamma\ngamma:\tret\n")
	writeFile(t, dir, ".git/x.S", "\t.globl gamma\ngamma:\tret\n")
	writeFile(t, dir, "node_modules/y.S", "\t.globl gamma\ngamma:\tret\n")
	writeFile(t, dir, "notes.txt", "\t.globl delta\ndelta:\n")

	reads := 0
	var mu sync.Mutex
	ix := NewIndex(func(p string) (string, error) {
		mu.Lock()
		reads++
		mu.Unlock()
		b, err := os.ReadFile(p)
		return string(b), err
	})
	ix.rescanEvery = 0
	ix.AddRoot(dir)
	ix.AddRoot(dir) // idempotent

	main := "\tcall alpha\n\tcall beta\n\tcall gamma\n"
	uri := "untitled:Untitled-1"
	ix.Update(uri, main, nil)
	if got := locs(ix.Definition(uri, at(t, main, "alpha", 0, 0))); !reflect.DeepEqual(got, []lc{{"a.S", 1, 0}}) {
		t.Fatalf("alpha: %v", got)
	}
	got := ix.Definition(uri, at(t, main, "beta", 0, 0))
	if len(got) != 1 || !strings.Contains(got[0].URI, "sub%20dir/b.s") {
		t.Fatalf("beta (percent-encoded path): %v", got)
	}
	if got := ix.Definition(uri, at(t, main, "gamma", 0, 0)); got != nil {
		t.Fatalf("gamma found in skipped dir: %v", got)
	}
	if reads != 2 {
		t.Fatalf("reads after first scan = %d, want 2", reads)
	}

	// Unchanged files are not re-read; a changed one is.
	ix.Definition(uri, at(t, main, "alpha", 0, 0))
	if reads != 2 {
		t.Fatalf("unchanged rescan read files: %d", reads)
	}
	if err := os.WriteFile(a, []byte("\n\t.globl alpha\nalpha:\tret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(a, future, future); err != nil {
		t.Fatal(err)
	}
	if got := locs(ix.Definition(uri, at(t, main, "alpha", 0, 0))); !reflect.DeepEqual(got, []lc{{"a.S", 2, 0}}) {
		t.Fatalf("alpha after edit: %v", got)
	}
	if reads != 3 {
		t.Fatalf("reads after edit = %d, want 3", reads)
	}

	// A deleted file drops out.
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if got := ix.Definition(uri, at(t, main, "alpha", 0, 0)); got != nil {
		t.Fatalf("deleted file still indexed: %v", got)
	}

	// An open document shadows its disk copy, and Remove hands back to disk.
	bPath := filepath.Join(dir, "sub dir", "b.s")
	bURI := pathURI(bPath)
	ix.Update(bURI, "\n\n\t.globl beta\nbeta:\tret\n", nil)
	if got := locs(ix.Definition(uri, at(t, main, "beta", 0, 0))); !reflect.DeepEqual(got, []lc{{"b.s", 3, 0}}) {
		t.Fatalf("open shadow: %v", got)
	}
	ix.Remove(bURI)
	if got := locs(ix.Definition(uri, at(t, main, "beta", 0, 0))); !reflect.DeepEqual(got, []lc{{"b.s", 1, 0}}) {
		t.Fatalf("after Remove: %v", got)
	}
}

func TestScanCapAndErrors(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("f%d.S", i), fmt.Sprintf("\t.globl s%d\ns%d:\n", i, i))
	}
	ix := NewIndex(func(p string) (string, error) {
		if strings.HasSuffix(p, "f4.S") {
			return "", errors.New("unreadable")
		}
		b, err := os.ReadFile(p)
		return string(b), err
	})
	ix.maxFiles = 3
	ix.AddRoot(dir)
	ix.AddRoot(filepath.Join(dir, "missing"))
	if n := len(ix.Labels()); n != 3 {
		t.Fatalf("cap: %d labels, want 3: %v", n, ix.Labels())
	}

	// A missing include and a virtual include (not on disk) behave.
	vix := NewIndex(func(p string) (string, error) {
		if p == "/virtual/v.h" {
			return "#define VIRT 1\n", nil
		}
		return "", errors.New("no such file")
	})
	src := "\tmovl $VIRT, %eax\n"
	vix.Update("file:///w/m.S", src, []string{"/virtual/v.h", "/virtual/missing.h"})
	vix.Update("file:///w/m.S", src, []string{"/virtual/v.h"}) // cached, not re-read
	if got := locs(vix.Definition("file:///w/m.S", at(t, src, "VIRT", 0, 0))); !reflect.DeepEqual(got, []lc{{"v.h", 0, 8}}) {
		t.Fatalf("virtual include: %v", got)
	}
}

func TestURIKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"file:///a/b%20c/d.S", filepath.FromSlash("/a/b c/d.S")},
		{"file:///a/./x/../d.S", filepath.FromSlash("/a/d.S")},
		{"untitled:Untitled-1", "untitled:Untitled-1"},
		{"file:", "file:"},
		{"file://%zz", "file://%zz"},
	}
	for _, tc := range tests {
		if got := uriKey(tc.in); got != tc.want {
			t.Errorf("uriKey(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
	if got := pathURI("/a/b c/#d.S"); got != "file:///a/b%20c/%23d.S" {
		t.Errorf("pathURI = %q", got)
	}
}

func TestUTF16Columns(t *testing.T) {
	// A multi-byte comment before the label shifts byte columns but not
	// UTF-16 ones beyond the character count.
	src := "/* \u00e9\U0001F600 */ foo: jmp foo\n"
	ix := NewIndex(nil)
	ix.Update("untitled:u", src, nil)
	// "/* " (3) + e-acute (1 unit) + emoji (2 units) + " */ " (4) = 10.
	got := ix.Definition("untitled:u", protocol.Position{Line: 0, Character: 21})
	if len(got) != 1 || got[0].Range.Start.Character != 10 || got[0].Range.End.Character != 13 {
		t.Fatalf("got %v", got)
	}
	// A column inside the surrogate pair snaps back without panicking.
	ix.Definition("untitled:u", protocol.Position{Line: 0, Character: 5})
}

func TestConcurrency(t *testing.T) {
	ix := NewIndex(nil)
	ix.AddRoot(fixtureDir(t))
	ix.rescanEvery = 0
	uri, text := readFixture(t, "stage2.S")
	pos := at(t, text, "jmp     pmode_enter", 0, 9)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch (g + i) % 5 {
				case 0:
					ix.Update(uri, text, nil)
				case 1:
					ix.Definition(uri, pos)
				case 2:
					ix.References(uri, pos, true)
				case 3:
					_, _ = ix.Rename(uri, pos, "renamed_enter")
				case 4:
					ix.Labels()
					ix.Macros()
					ix.Update(fmt.Sprintf("untitled:%d", g), "x: jmp x\n", nil)
					ix.Remove(fmt.Sprintf("untitled:%d", g))
				}
			}
		}(g)
	}
	wg.Wait()
	ix.Update(uri, text, nil)
	if got := ix.Definition(uri, pos); len(got) != 1 {
		t.Fatalf("after concurrent use: %v", got)
	}
}

func TestParseDetails(t *testing.T) {
	src := "# define SP 1\n.macro M a, b\n.endm\nx = 4 # c\n.set y, 5; nop\n" +
		".section .data\n.type x, @object\n\\a\n.globl\n#if defined(SP) && \"q\" == 'c' && 0x10\n#endif\n" +
		"#error \"don't\"\n.set\n"
	f := parse("untitled:p", src, false)
	wantDetail := map[string]string{"SP": "1", "M": "a, b", "x": "4", "y": "5"}
	for name, want := range wantDetail {
		idx := f.byName[name]
		if len(idx) == 0 {
			t.Fatalf("%s not defined", name)
		}
		if got := f.defs[idx[0]].detail; got != want {
			t.Errorf("%s detail %q want %q", name, got, want)
		}
	}
	if f.defs[f.byName["x"][0]].kind != kindEqu {
		t.Error("x = 4 not an equ")
	}
	if _, ok := f.occs[".data"]; ok {
		t.Error("section name indexed as occurrence")
	}
	if _, ok := f.occs["a"]; ok {
		t.Error("macro parameter indexed")
	}
	if len(f.occs["SP"]) != 2 {
		t.Errorf("SP occurrences %v", f.occs["SP"])
	}
	if _, ok := f.occs["defined"]; ok {
		t.Error("defined indexed")
	}
	if got := f.restOfStatement(len(f.toks)); got != "" {
		t.Errorf("restOfStatement past end = %q", got)
	}
	if isCIdent("") || isCIdent("1a") || isCIdent("a-b") || !isCIdent("_a1") {
		t.Error("isCIdent")
	}
	if f.lineEnd(-1) != 0 || f.lineEnd(1000) != 0 {
		t.Error("lineEnd out of range")
	}
	if r := f.rng(span{line: 1000}); r.Start.Character != 0 {
		t.Error("rng out of range")
	}
}

func TestDocumentSymbols(t *testing.T) {
	src := `#define TOP 0x10
	.macro SAVE r
	push \r
	.endm
	.section .text.boot, "ax"
	.globl start
start:
	SAVE %ax
1:	jmp 1b
helper: ret
	.set vec, 0
	.set vec, vec + 1
	.data
msg:	.asciz "hi"
	.section
	.bss
a: b:
`
	doc := &docstore.Document{URI: "file:///x/o.S", Text: src}
	got := DocumentSymbols(doc)
	type node struct {
		Name     string
		Kind     protocol.SymbolKind
		Detail   string
		Start    int
		End      int
		Children []node
	}
	var conv func([]protocol.DocumentSymbol) []node
	conv = func(ss []protocol.DocumentSymbol) []node {
		var out []node
		for _, s := range ss {
			if s.Range.Start.Line > s.SelectionRange.Start.Line || s.Range.End.Line < s.SelectionRange.End.Line ||
				(s.Range.End.Line == s.SelectionRange.End.Line && s.Range.End.Character < s.SelectionRange.End.Character) {
				t.Errorf("%s: range %v does not contain selection %v", s.Name, s.Range, s.SelectionRange)
			}
			out = append(out, node{s.Name, s.Kind, s.Detail, s.Range.Start.Line, s.Range.End.Line, conv(s.Children)})
		}
		return out
	}
	want := []node{
		{"TOP", protocol.SymbolKindConstant, "0x10", 0, 0, nil},
		{"SAVE", protocol.SymbolKindNamespace, "r", 1, 3, nil},
		{".text.boot", protocol.SymbolKindModule, "", 4, 11, []node{
			{"start", protocol.SymbolKindFunction, ".globl", 6, 8, nil},
			{"helper", protocol.SymbolKindVariable, "", 9, 11, nil},
			{"vec", protocol.SymbolKindVariable, "0", 10, 10, nil},
		}},
		{".data", protocol.SymbolKindModule, "", 12, 13, []node{
			{"msg", protocol.SymbolKindVariable, "", 13, 13, nil},
		}},
		{".section", protocol.SymbolKindModule, "", 14, 14, nil},
		{".bss", protocol.SymbolKindModule, "", 15, 17, []node{
			{"a", protocol.SymbolKindVariable, "", 16, 17, nil},
			{"b", protocol.SymbolKindVariable, "", 16, 17, nil},
		}},
	}
	if g := conv(got); !reflect.DeepEqual(g, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", g, want)
	}

	// Unterminated macro runs to the end.
	got = DocumentSymbols(&docstore.Document{URI: "untitled:m", Text: ".macro M\nnop\n"})
	if len(got) != 1 || got[0].Range.End.Line != 2 {
		t.Fatalf("unterminated macro: %+v", got)
	}
}

func TestDocumentSymbolsFixture(t *testing.T) {
	uri, text := readFixture(t, "pmode.S")
	got := DocumentSymbols(&docstore.Document{URI: uri, Text: text})
	var sec *protocol.DocumentSymbol
	for i := range got {
		if got[i].Name == ".text.stage2" {
			sec = &got[i]
		}
	}
	if sec == nil {
		t.Fatalf("no .text.stage2 section: %+v", got)
		return
	}
	names := map[string]protocol.SymbolKind{}
	for _, c := range sec.Children {
		names[c.Name] = c.Kind
	}
	if names["pmode_enter"] != protocol.SymbolKindFunction || names["a20_ok"] != protocol.SymbolKindVariable {
		t.Fatalf("children: %v", names)
	}
}

func TestFoldingRanges(t *testing.T) {
	src := `/* one
 * two
 */
// a
// b

// lone
# c
# d
/* single */
.macro M
	.rept 3
	nop
	.endr
.endm
#if A
#ifdef B
x:
#else
y:
#endif
#elif C
z:
#endif
	.if 1
	nop
	.elseif 2
	nop
	.else
	nop
	.endif
	.endif
#endif
/* open
 never closed`
	got := FoldingRanges(&docstore.Document{Text: src})
	want := []protocol.FoldingRange{
		{StartLine: 0, EndLine: 2, Kind: "comment"},
		{StartLine: 3, EndLine: 4, Kind: "comment"},
		{StartLine: 6, EndLine: 8, Kind: "comment"},
		{StartLine: 11, EndLine: 12, Kind: "region"},
		{StartLine: 10, EndLine: 13, Kind: "region"},
		{StartLine: 16, EndLine: 17, Kind: "region"},
		{StartLine: 18, EndLine: 19, Kind: "region"},
		{StartLine: 15, EndLine: 20, Kind: "region"},
		{StartLine: 21, EndLine: 22, Kind: "region"},
		{StartLine: 24, EndLine: 25, Kind: "region"},
		{StartLine: 26, EndLine: 27, Kind: "region"},
		{StartLine: 28, EndLine: 29, Kind: "region"},
		{StartLine: 33, EndLine: 34, Kind: "comment"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestFoldingRangesFixture(t *testing.T) {
	_, text := readFixture(t, "idt.S")
	got := FoldingRanges(&docstore.Document{Text: text})
	rept := lineOf(t, "idt.S", ".rept")
	endr := lineOf(t, "idt.S", ".endr")
	found := false
	for _, r := range got {
		if r.StartLine == rept && r.EndLine == endr-1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no .rept fold %d-%d in %+v", rept, endr-1, got)
	}
}
