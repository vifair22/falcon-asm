package lint

import (
	"strings"
	"testing"
)

// testRule is a minimal valid rule; tests adjust a copy.
func testRule(code string) *Rule {
	return &Rule{
		Code: code, Section: 3, Default: Warning, Langs: LangAsm,
		Doc:   Doc{Summary: "test rule " + code, Why: "why", Bad: "bad", Good: "good"},
		Check: func(*Context) {},
	}
}

// register adds rules for the duration of a test.
func register(t *testing.T, rules ...*Rule) {
	t.Helper()
	for _, r := range rules {
		Register(r)
		code := r.Code
		t.Cleanup(func() { unregister(code) })
	}
}

func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		p := recover()
		if p == nil {
			t.Fatalf("no panic, want %q", want)
		}
		if msg, _ := p.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic %v, want %q", p, want)
		}
	}()
	fn()
}

func TestRegisterValidation(t *testing.T) {
	mod := func(f func(r *Rule)) *Rule {
		r := testRule("x-test")
		f(r)
		return r
	}
	tests := []struct {
		name string
		r    *Rule
		want string
	}{
		{"nil", nil, "Register(nil)"},
		{"bad code", mod(func(r *Rule) { r.Code = "Bad_Code" }), "invalid rule code"},
		{"all", mod(func(r *Rule) { r.Code = "all" }), "invalid rule code"},
		{"section low", mod(func(r *Rule) { r.Section = 0 }), "section 0"},
		{"section high", mod(func(r *Rule) { r.Section = 13 }), "section 13"},
		{"no check", mod(func(r *Rule) { r.Check = nil }), "no Check"},
		{"severity", mod(func(r *Rule) { r.Default = 9 }), "invalid default severity"},
		{"no lang", mod(func(r *Rule) { r.Langs = 0 }), "no language"},
		{"no summary", mod(func(r *Rule) { r.Doc.Summary = " " }), "no Doc.Summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { mustPanic(t, tt.want, func() { Register(tt.r) }) })
	}
	register(t, testRule("x-dup"))
	mustPanic(t, "registered twice", func() { Register(testRule("x-dup")) })
}

func TestRulesOrderAndLookup(t *testing.T) {
	a, b, c := testRule("x-b"), testRule("x-a"), testRule("x-c")
	c.Section = 1
	register(t, a, b, c)
	var got []string
	for _, r := range Rules() {
		if r.Code == "x-a" || r.Code == "x-b" || r.Code == "x-c" {
			got = append(got, r.Code)
		}
	}
	if strings.Join(got, ",") != "x-c,x-a,x-b" {
		t.Fatalf("order = %v", got)
	}
	// The sorted view is cached and shared until the registry changes.
	first, again := Rules(), Rules()
	if &first[0] != &again[0] {
		t.Fatal("sorted view rebuilt without a change")
	}
	if r, ok := Lookup("x-a"); !ok || r != b {
		t.Fatal("lookup x-a")
	}
	if _, ok := Lookup("x-none"); ok {
		t.Fatal("lookup of an unknown code")
	}
	if _, ok := Lookup(UnusedSuppressionCode); !ok {
		t.Fatal("unused-suppression is not registered")
	}
}

func TestValidCode(t *testing.T) {
	for code, want := range map[string]bool{
		"mode-register": true, "a": true, "x86-level2": true, "": false, "all": false, "-a": false,
		"a-": false, "a--b": false, "A": false, "a_b": false, "1a": false, "a b": false,
	} {
		if got := ValidCode(code); got != want {
			t.Errorf("ValidCode(%q) = %v", code, got)
		}
	}
}

func TestSeverity(t *testing.T) {
	for in, want := range map[string]Severity{
		"off": Off, "ERROR": Error, " warning ": Warning, "warn": Warning, "info": Info, "information": Info, "hint": Hint,
	} {
		if got, ok := ParseSeverity(in); !ok || got != want {
			t.Errorf("ParseSeverity(%q) = %v %v", in, got, ok)
		}
	}
	if _, ok := ParseSeverity("loud"); ok {
		t.Error("loud parsed")
	}
	if Warning.String() != "warning" || Severity(7).String() != "Severity(7)" {
		t.Errorf("String = %s %s", Warning, Severity(7))
	}
	tests := []struct {
		s, min Severity
		want   bool
	}{
		{Error, Warning, true}, {Warning, Warning, true}, {Info, Warning, false}, {Hint, Info, false},
		{Off, Hint, false}, {Hint, Off, true}, {Error, Error, true},
	}
	for _, tt := range tests {
		if got := tt.s.AtLeast(tt.min); got != tt.want {
			t.Errorf("%v.AtLeast(%v) = %v", tt.s, tt.min, got)
		}
	}
}

func TestLangString(t *testing.T) {
	if got := (LangAsm | LangInline | LangC).String(); got != "assembly, inline asm, C" {
		t.Errorf("all = %q", got)
	}
	if got := Lang(0).String(); got != "none" {
		t.Errorf("none = %q", got)
	}
}

// TestMain registers the rules the configuration tests name; the real
// rules live in packages that import this one.
func TestMain(m *testing.M) {
	Register(testRule("x-reg"))
	Register(testRule("x-lvl"))
	m.Run()
}
