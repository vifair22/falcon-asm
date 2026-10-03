package asm

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestMacroDefs(t *testing.T) {
	src := "\t.macro Show a, b=7, c:vararg # comment\n\t.long \\a\n\t.macro inner\n\t.endm\n\t.endm\n" +
		"\t.macro req x:req y\n\tnop\n\t.endm\n\t.macro show\n\t.endm\n\t.macro open p\n\tnop\n"
	p := parseSrc(t, src, Config{})
	defs := p.MacroDefs()
	if len(defs) != 3 {
		t.Fatalf("defs = %v", defs)
	}
	show := defs["show"]
	want := []MacroParam{{Name: "a"}, {Name: "b", Default: "7"}, {Name: "c", Vararg: true}}
	if show.Name != "Show" || !reflect.DeepEqual(show.Params, want) || show.Stmt != 0 || show.End != 4 {
		t.Fatalf("show = %+v", show)
	}
	if show.Body != "\t.long \\a\n\t.macro inner\n\t.endm\n" {
		t.Fatalf("body = %q", show.Body)
	}
	if r := defs["req"]; !reflect.DeepEqual(r.Params, []MacroParam{{Name: "x", Required: true}, {Name: "y"}}) {
		t.Fatalf("req = %+v", r.Params)
	}
	if o := defs["open"]; o.End != -1 || o.Body != "\tnop\n" {
		t.Fatalf("open = %+v", o)
	}
}

// The expected texts match what gas 2.46 lists for the same source with
// -alm (spacing aside): arguments split at blanks and commas after blanks
// around operators are squeezed, quotes are stripped, a vararg takes the
// rest, keyword arguments bind by name and \@ counts every execution.
func TestExpandMacros(t *testing.T) {
	src := "\t.macro show a, b=7, c:vararg\n\t.long \\a\n\t.long \\b\n\t.ascii \"<\\c>\"\n\t.long \\@\n\t.endm\n" +
		"\t.macro kw x, y\n\t.long \\x - \\y\nlbl\\@\\():\tshow \\x\n\t.exitm\n\thlt\n\t.endm\n" +
		"\tshow 1 + 2, 5\n\tshow 1 2 3 4\n\tkw y=1, x=10 /* c */\n\tshow \"q r\"\n\t.macro need r:req\n\t.long \\r \\unknown \\\\\n\t.endm\n\tneed\n"
	p := parseSrc(t, src, Config{})
	got := p.ExpandMacros(0)
	want := []Expansion{
		{Stmt: 12, Macro: "show", Text: "\t.long 1+2\n\t.long 5\n\t.ascii \"<>\"\n\t.long 0\n"},
		{Stmt: 13, Macro: "show", Text: "\t.long 1\n\t.long 2\n\t.ascii \"<3 4>\"\n\t.long 1\n"},
		{Stmt: 14, Macro: "kw", Text: "\t.long 10 - 1\nlbl2:\n\t.long 10\n\t.long 7\n\t.ascii \"<>\"\n\t.long 3\n"},
		{Stmt: 15, Macro: "show", Text: "\t.long q r\n\t.long 7\n\t.ascii \"<>\"\n\t.long 4\n"},
		{Stmt: 19, Macro: "need", Text: "\t.long  \\unknown \\\\\n", Err: "missing required argument r"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%#v\nwant\n%#v", got, want)
	}
	if got := p.ExpandMacros(2); len(got) != 2 {
		t.Fatalf("limit: %d", len(got))
	}
	x := p.NewExpander()
	if e, ok := x.Expand("SHOW", "9"); !ok || !strings.HasPrefix(e.Text, "\t.long 9\n") || e.Stmt != -1 {
		t.Fatalf("Expand = %+v", e)
	}
	if _, ok := x.Expand("nope", ""); ok {
		t.Fatal("unknown macro expanded")
	}
	if _, ok := x.ExpandStmt(p, 0); ok {
		t.Fatal("a .macro line is not an invocation")
	}
	if _, ok := x.ExpandStmt(p, 99); ok {
		t.Fatal("out of range")
	}
	if parseSrc(t, "\tnop\n", Config{}).ExpandMacros(0) != nil {
		t.Fatal("no macros, no expansions")
	}
}

func TestExpandCaps(t *testing.T) {
	// Unbounded recursion stops at the depth cap.
	p := parseSrc(t, "\t.macro r\n\tnop\n\tr\n\t.endm\n\tr\n", Config{})
	e := p.ExpandMacros(0)[0]
	if !strings.Contains(e.Err, "nesting deeper") || strings.Count(e.Text, "nop") != maxExpandDepth {
		t.Fatalf("recursion: %q %d", e.Err, strings.Count(e.Text, "nop"))
	}
	// Exponential nesting stops at the line cap.
	var b strings.Builder
	b.WriteString("\t.macro m0\n\tnop\n\tnop\n\t.endm\n")
	for i := 1; i <= 13; i++ {
		b.WriteString("\t.macro m" + itoa(i) + "\n\tm" + itoa(i-1) + "\n\tm" + itoa(i-1) + "\n\t.endm\n")
	}
	b.WriteString("\tm13\n")
	p = parseSrc(t, b.String(), Config{})
	e = p.ExpandMacros(0)[0]
	if !strings.Contains(e.Err, "truncated") {
		t.Fatalf("line cap: %q", e.Err)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestMacroHelpers(t *testing.T) {
	if got := scrub("  a ,  b  +  c \"x  y\" "); got != "a,b+c \"x  y\"" {
		t.Fatalf("scrub = %q", got)
	}
	if got := splitMacroArgs(`(a, b) "c, d" e`); !reflect.DeepEqual(got, []string{"(a, b)", `"c, d"`, "e"}) {
		t.Fatalf("split = %q", got)
	}
	if splitMacroArgs("") != nil {
		t.Fatal("empty split")
	}
	if stripLineComment(`a "#b" # c`) != `a "#b" ` || stripLineComment("a // b") != "a " {
		t.Fatal("stripLineComment")
	}
	w, l, r := lineHead("a: b:\tfoo x, y\n")
	if w != "foo" || l != "a: b:" || r != " x, y" {
		t.Fatalf("lineHead = %q %q %q", w, l, r)
	}
}

func TestResolveLocal(t *testing.T) {
	src := "\tjmp 1f\n\tjmp 1b\n\t.macro m\n\tjmp 2f\n2:\n\tjmp 3b\n\t.endm\n\tjmp 4f\n1:\n"
	p := parseSrc(t, src, Config{})
	cases := []struct {
		stmt int
		ref  string
		st   LocalStatus
	}{
		{0, "1f", LocalFound}, {1, "1b", LocalMissing}, {3, "2f", LocalFound}, {5, "3b", LocalUnknown},
		{7, "4f", LocalMissing}, {0, "xf", LocalInvalid}, {0, "1x", LocalInvalid}, {99, "1f", LocalInvalid},
	}
	for _, c := range cases {
		if _, st := p.ResolveLocal(c.stmt, c.ref); st != c.st {
			t.Errorf("ResolveLocal(%d, %s) = %v, want %v", c.stmt, c.ref, st, c.st)
		}
	}
}
