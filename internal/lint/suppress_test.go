package lint

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// at is a finding of code at the start of line.
func at(code string, line int) Finding {
	return Finding{Code: code, Severity: Warning, Range: protocol.Range{
		Start: protocol.Position{Line: line, Character: 1}, End: protocol.Position{Line: line, Character: 4},
	}}
}

func lines(fs []Finding) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.Code+"@"+itoa(f.Range.Start.Line))
	}
	return strings.Join(parts, " ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func asmSup(text string) *Suppressions {
	return AsmSuppressions(&docstore.Document{URI: "file:///a.S", Text: text}, nil)
}

func TestAsmSuppressionForms(t *testing.T) {
	text := "" +
		"\tnop /* falcon-asm: ignore=x-reg */\n" + // 0
		"\tnop # falcon-asm: ignore-next=x-lvl\n" + // 1
		"\n" + // 2
		"/* a comment line */\n" + // 3
		"\tnop\n" + // 4: ignore-next lands here
		"# falcon-asm: ignore-begin=x-reg\n" + // 5
		"\tnop\n" + // 6
		"# falcon-asm: ignore-end=x-reg\n" + // 7
		"\tnop\n" + // 8
		"// falcon-asm: ignore=x-lvl, x-reg because reasons\n" + // 9
		"# falcon-asm: ignore-begin=all\n" + // 10
		"\tnop\n" + // 11
		"# FALCON-ASM: IGNORE-END=ALL level=v3\n" // 12
	s := asmSup(text)
	in := []Finding{
		at("x-reg", 0), at("x-lvl", 0), // only x-reg is ignored on line 0
		at("x-lvl", 4), at("x-reg", 4),
		at("x-reg", 6), at("x-lvl", 6),
		at("x-reg", 8),
		at("x-reg", 9), at("x-lvl", 9),
		at("x-a", 11),
	}
	got := lines(s.Filter(in))
	if got != "x-lvl@0 x-reg@4 x-lvl@6 x-reg@8" {
		t.Fatalf("kept %s", got)
	}
	if u := s.Unused(Info, map[string]bool{"x-reg": true, "x-lvl": true}); len(u) != 0 {
		t.Fatalf("unused = %v", u)
	}
}

func TestFileSuppressionAndUnused(t *testing.T) {
	text := "" +
		"# falcon-asm: ignore-file=x-reg\n" + // 0
		"\tnop /* falcon-asm: ignore=x-reg */\n" + // 1: also used: every covering suppression counts
		"\tnop /* falcon-asm: ignore=x-lvl */\n" + // 2: unused, x-lvl ran
		"\tnop /* falcon-asm: ignore=x-off */\n" + // 3: x-off did not run: not judged
		"\tnop /* falcon-asm: ignore=no-such-rule */\n" + // 4: unknown code
		"\tnop /* falcon-asm: ignore=all */\n" + // 5: all, unused
		"# falcon-asm: ignore-end=x-lvl\n" + // 6: unmatched end
		"\tnop /* falcon-asm: ignore=all */\n" + // 7: would hide line 7's own report, but may not
		"# falcon-asm: ignore-next=x-lvl\n" // 8: nothing follows
	register(t, testRule("x-off"))
	s := asmSup(text)
	kept := s.Filter([]Finding{at("x-reg", 1), at("x-reg", 30)})
	if len(kept) != 0 {
		t.Fatalf("kept %v", kept)
	}
	u := s.Unused(Hint, map[string]bool{"x-reg": true, "x-lvl": true})
	var got []string
	for _, f := range u {
		if f.Code != UnusedSuppressionCode || f.Severity != Hint {
			t.Fatalf("finding %v", f)
		}
		got = append(got, itoa(f.Range.Start.Line)+":"+f.Message)
	}
	want := []string{
		"2:suppression of x-lvl matched no finding",
		`4:unknown rule code "no-such-rule" in suppression`,
		"5:suppression of all matched no finding",
		"6:ignore-end=x-lvl has no matching ignore-begin",
		"7:suppression of all matched no finding",
		"8:suppression of x-lvl matched no finding",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("unused =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The range covers the code in the comment.
	if r := u[0].Range; r.Start.Character != len("\tnop /* falcon-asm: ignore=") || r.End.Character != r.Start.Character+len("x-lvl") {
		t.Fatalf("range = %+v", r)
	}
	var nilSup *Suppressions
	if nilSup.Unused(Info, nil) != nil || len(nilSup.Filter([]Finding{at("x", 0)})) != 1 {
		t.Fatal("nil suppressions")
	}
}

func TestUnusedSuppressionsSuppressible(t *testing.T) {
	s := asmSup("# falcon-asm: ignore-file=unused-suppression\n\tnop /* falcon-asm: ignore=x-reg */\n")
	if u := s.Unused(Info, map[string]bool{"x-reg": true}); len(u) != 0 {
		t.Fatalf("unused = %v", u)
	}
}

func TestOpenRegionRunsToEOF(t *testing.T) {
	s := asmSup("# falcon-asm: ignore-begin=x-reg\n\tnop\n\tnop\n")
	if kept := s.Filter([]Finding{at("x-reg", 2), at("x-reg", 0)}); len(kept) != 0 {
		t.Fatalf("kept %v", kept)
	}
	// Nested regions pair innermost first.
	s = asmSup("# falcon-asm: ignore-begin=x-reg\n# falcon-asm: ignore-begin=x-reg\n# falcon-asm: ignore-end=x-reg\n\tnop\n# falcon-asm: ignore-end=x-reg\n\tnop\n")
	if got := lines(s.Filter([]Finding{at("x-reg", 3), at("x-reg", 5)})); got != "x-reg@5" {
		t.Fatalf("nested kept %s", got)
	}
}

func TestCSuppressions(t *testing.T) {
	src := "" +
		"const char *s = \"// falcon-asm: ignore=x-reg\";\n" + // 0: inside a string: not a comment
		"void f(void) { __asm__(\"nop\"); } // falcon-asm: ignore=x-reg\n" + // 1
		"/* falcon-asm: ignore-next=x-lvl\n" + // 2
		"   still the comment */\n" + // 3
		"\n" + // 4
		"int x = 'a'; int y = 1'000; // falcon-asm: ignore=x-lvl\n" + // 5
		"const char *r = R\"d(// falcon-asm: ignore=x-a)d\";\n" + // 6
		"// line comment \\\n" + // 7
		"   continued falcon-asm: ignore=x-b\n" + // 8
		"int z; /* unterminated falcon-asm: ignore=x-c" // 9
	s := CSuppressions(&docstore.Document{URI: "file:///a.c", Text: src})
	in := []Finding{at("x-reg", 0), at("x-reg", 1), at("x-lvl", 3), at("x-lvl", 5), at("x-lvl", 2), at("x-a", 6), at("x-b", 8), at("x-c", 9)}
	if got := lines(s.Filter(in)); got != "x-reg@0 x-lvl@3 x-lvl@2 x-a@6" {
		t.Fatalf("kept %s", got)
	}
	// ignore-next skips the rest of a block comment and blank lines: line
	// 5 is the next code line, which the x-lvl finding above covered.
	if empty := CSuppressions(&docstore.Document{Text: "int x;\n"}); len(empty.dirs) != 0 {
		t.Fatal("directives without a tag")
	}
}

func TestIgnoreFix(t *testing.T) {
	tests := []struct {
		name  string
		style CommentStyle
		text  string
		line  int
		want  string
	}{
		{"asm new", StyleAsm, "\tpushq %rax   \n", 0, "\tpushq %rax /* falcon-asm: ignore=x-reg */   \n"},
		{"c new", StyleC, "\t__asm__(\"pushq %rax\");\n", 0, "\t__asm__(\"pushq %rax\"); // falcon-asm: ignore=x-reg\n"},
		{"extend", StyleAsm, "\tpushq %rax # falcon-asm: ignore=x-lvl, x-a why\n", 0, "\tpushq %rax # falcon-asm: ignore=x-lvl, x-a,x-reg why\n"},
		{"continued", StyleC, "#define X \\\n\tasm(\"pushq %rax\")\n", 0, "#define X /* falcon-asm: ignore=x-reg */ \\\n\tasm(\"pushq %rax\")\n"},
		{"continued tight", StyleAsm, "\tmovl %eax, %ebx\\\n", 0, "\tmovl %eax, %ebx /* falcon-asm: ignore=x-reg */ \\\n"},
		{"crlf", StyleAsm, "\tnop\r\n\tpushq %rax\r\n", 1, "\tnop\r\n\tpushq %rax /* falcon-asm: ignore=x-reg */\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &docstore.Document{Text: tt.text}
			var s *Suppressions
			if tt.style == StyleC {
				s = CSuppressions(doc)
			} else {
				s = AsmSuppressions(doc, nil)
			}
			fx := s.IgnoreFix(tt.line, "x-reg")
			if fx.Title != "Ignore x-reg on this line" || len(fx.Edits) != 1 {
				t.Fatalf("fix = %+v", fx)
			}
			if got := applyEdit(t, doc, fx.Edits[0]); got != tt.want {
				t.Fatalf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func applyEdit(t *testing.T, doc *docstore.Document, e protocol.TextEdit) string {
	t.Helper()
	s, err1 := doc.Offset(e.Range.Start)
	en, err2 := doc.Offset(e.Range.End)
	if err1 != nil || err2 != nil {
		t.Fatalf("edit %+v: %v %v", e, err1, err2)
	}
	return doc.Text[:s] + e.NewText + doc.Text[en:]
}
