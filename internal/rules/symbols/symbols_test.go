package symbols

import (
	"fmt"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// fakeWS is a lint.Workspace with fixed answers.
type fakeWS struct {
	defined, referenced map[string]bool
	globals             map[string][]protocol.Location
	incomplete          bool
}

func (w *fakeWS) Defined(n string) bool    { return w.defined[n] }
func (w *fakeWS) Referenced(n string) bool { return w.referenced[n] }
func (w *fakeWS) Complete() bool           { return !w.incomplete }
func (w *fakeWS) GlobalDefinitions(n string) []protocol.Location {
	return w.globals[n]
}

var allCodes = []string{
	CodeUndefinedLabel, CodeUnusedLabel, CodeUnusedDefine, CodeDuplicateDefinition, CodeDuplicateLabel,
	CodeLocalLabelUnresolved, CodeLocalLabelFar, CodeGloblUndefined, CodeExternUnused,
}

func rulesFor(t *testing.T, codes ...string) []*lint.Rule {
	t.Helper()
	var out []*lint.Rule
	for _, c := range codes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		out = append(out, r)
	}
	return out
}

func runURI(t *testing.T, uri, text string, ws lint.Workspace, macros map[string]string, codes ...string) []lint.Finding {
	t.Helper()
	doc := &docstore.Document{URI: uri, Text: text}
	toks := lexer.Tokenize(text)
	p := asm.Parse(doc, toks, asm.Config{Macros: macros})
	in := lint.Input{Prog: p, Lang: lint.LangAsm, Rules: rulesFor(t, codes...)}
	if ws != nil {
		in.Workspace = ws
	}
	return lint.Run(in)
}

func run(t *testing.T, text string, ws lint.Workspace, codes ...string) []lint.Finding {
	t.Helper()
	return runURI(t, "file:///w/t.S", text, ws, nil, codes...)
}

func empty() *fakeWS { return &fakeWS{} }

// summary renders findings as "code@line:msg-prefix" for compact checks.
func summary(fs []lint.Finding) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, fmt.Sprintf("%s@%d", f.Code, f.Range.Start.Line))
	}
	return strings.Join(parts, " ")
}

func expect(t *testing.T, got []lint.Finding, want string) {
	t.Helper()
	if s := summary(got); s != want {
		t.Fatalf("findings = %q, want %q\n%v", s, want, got)
	}
}

func TestDocExamples(t *testing.T) {
	for _, code := range allCodes {
		if code == CodeDuplicateDefinition {
			continue // two files; covered by TestDuplicateGlobal
		}
		r, _ := lint.Lookup(code)
		t.Run(code, func(t *testing.T) {
			if got := run(t, r.Doc.Bad, empty(), code); len(got) == 0 {
				t.Errorf("Bad example not reported")
			}
			if got := run(t, r.Doc.Good, empty(), code); len(got) != 0 {
				t.Errorf("Good example reported: %v", got)
			}
		})
	}
}

func TestUndefinedLabel(t *testing.T) {
	text := "\tcall print_strng\n\tjmp done\n\tmovl $table, %eax\n\t.long gone\n\t.set x, nowhere+1\n" +
		"\tcall kmain\n\tcall ext\n\tmovw $SEL, %ax\n\tcall _GLOBAL_OFFSET_TABLE_\n\tcall __start_sec\n" +
		"\t.extern ext\nprint_string:\ndone:\ntable:\n\tret\n"
	ws := &fakeWS{defined: map[string]bool{"kmain": true}}
	got := run(t, text, ws, CodeUndefinedLabel)
	expect(t, got, "undefined-label@0 undefined-label@3 undefined-label@4")
	if len(got[0].Fixes) != 1 || got[0].Fixes[0].Edits[0].NewText != "print_string" {
		t.Fatalf("fix = %+v", got[0].Fixes)
	}
	if !strings.Contains(got[1].Message, "gone") {
		t.Fatalf("message = %q", got[1].Message)
	}

	// No workspace, or an include that was not found: nothing is known.
	expect(t, run(t, text, nil, CodeUndefinedLabel), "")
	expect(t, run(t, text, &fakeWS{incomplete: true}, CodeUndefinedLabel), "")

	// An ALL_CAPS name may be a -D macro until the toolchain's table is
	// known; with it (or in a plain .s file) it is reported.
	caps := "#define X 1\n\tmovw $SEL, %ax\n"
	expect(t, run(t, caps, empty(), CodeUndefinedLabel), "")
	expect(t, runURI(t, "file:///w/t.S", caps, empty(), map[string]string{"__GNUC__": "14"}, CodeUndefinedLabel), "undefined-label@1")
	expect(t, runURI(t, "file:///w/t.s", "\tmovw $SEL, %ax\n", empty(), nil, CodeUndefinedLabel), "undefined-label@0")
	expect(t, runURI(t, "file:///w/t.S", caps, empty(), map[string]string{"__GNUC__": "14", "SEL": "8"}, CodeUndefinedLabel), "")

	// Macro bodies are expanded elsewhere; labels they define count.
	mac := "\t.macro m\n\tjmp \\@foo\n\tcall inmacro\n\t.endm\n\t.macro n\nmlabel:\n\t.endm\n\tjmp mlabel\n"
	expect(t, run(t, mac, empty(), CodeUndefinedLabel), "")
	// #define names, .comm and .weakref define.
	defs := "#define F(x) x\n\t.comm buf, 4\n\t.weakref alias, target\n\tcall F(1)\n\tmovl $buf, %eax\n\tcall alias\n"
	expect(t, run(t, defs, &fakeWS{defined: map[string]bool{"target": true}}, CodeUndefinedLabel), "")
}

func TestUnusedLabel(t *testing.T) {
	text := "\t.globl exported\nexported:\n_start:\nunused:\n\tjmp used\nused:\nfromld:\n.Ldead:\n\tcall *tbl(,%eax,4)\ntbl:\n" +
		"\t.macro m\ninmac:\n\t.endm\nviamac:\n\tm viamac\n\t.type typed, @function\n\t.size typed, .-typed\ntyped:\n\tret\n"
	ws := &fakeWS{referenced: map[string]bool{"fromld": true}}
	got := run(t, text, ws, CodeUnusedLabel)
	expect(t, got, "unused-label@3 unused-label@7 unused-label@17")
	if got[0].Severity != lint.Info || got[0].Message != "label unused is never referenced" {
		t.Fatalf("finding = %v", got[0])
	}
	expect(t, run(t, text, nil, CodeUnusedLabel), "")
	// A duplicate is reported once.
	expect(t, run(t, "a:\na:\n", empty(), CodeUnusedLabel), "unused-label@0")
}

// TestUnusedLabelReached covers labels reached by offset or in sequence:
// presumed after a constant .org, declared with reached=.
func TestUnusedLabelReached(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"after .org", "\t.org 0x100\nentry:\n\tret\n", ""},
		{"after .org macro", "\t.org JRES_OFF_CODE\n/* falcon-asm: handler=service vector=0x13 */\njres:\n\tiret\n", ""},
		{"after . =", "\t. = 0x1FE\nsig:\n\t.word 0xAA55\n", ""},
		{"through emitless directives and labels", "\t.org 3\n\t.code16\nfirst:\nsecond:\n\tret\n", ""},
		{"label on the .org line", "lab:\t.org 3\nentry:\n\tret\n", "unused-label@0 unused-label@1"},
		{".org relative to a label", "start:\n\tjmp start\n\t.org start+510\nsig:\n\t.word 0xAA55\n", "unused-label@3"},
		{".org relative to .", "\t.org . + 4\nx:\n\tret\n", "unused-label@1"},
		{".org to a local label", "1:\n\t.org 1b+4\nx:\n\tret\n", "unused-label@2"},
		{"code between", "\t.org 0x100\n\tnop\nentry:\n\tret\n", "unused-label@2"},
		{"alignment between", "\t.org 0x100\n\t.balign 16\nentry:\n\tret\n", "unused-label@2"},
		{"reached=offset on the line", "\t.byte 0x55, 0xAA, 1\ninit:  /* falcon-asm: reached=offset: BIOS far-calls offset 3 */\n\tret\n", ""},
		{"reached=external above", "/* falcon-asm: reached=external */\n# another comment\nentry:\n\tret\n", ""},
		{"blank line breaks the attachment", "/* falcon-asm: reached=external */\n\nentry:\n\tret\n", "unused-label@2"},
		{"code line breaks the attachment", "\tnop /* falcon-asm: reached=external */\nentry:\n\tret\n", "unused-label@1"},
		{"multi-line comment above", "/* a vector entry,\n   falcon-asm: reached=offset */\nentry:\n\tret\n", ""},
		{"unknown value", "x: /* falcon-asm: reached=somehow */\n\tret\n", "unused-label@0"},
		{"sequence", "\tmovw $m_a, %si\nm_a: .asciz \"a\"\n/* falcon-asm: reached=sequence: walked from m_a */\n" +
			"m_b: .asciz \"b\"\nm_c:\n\t.asciz \"c\"\nm_d: .byte 0\n\nm_e: .byte 0\n", "unused-label@8"},
		{"sequence stops at a comment line", "m_a: .byte 0 /* falcon-asm: reached=sequence */\nm_b: .byte 1\n/* end */\nm_c: .byte 2\n",
			"unused-label@3"},
		{"sequence stops at code", "m_a: .byte 0 /* falcon-asm: reached=sequence */\nm_b: nop\n", "unused-label@1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, run(t, tc.text, empty(), CodeUnusedLabel), tc.want)
		})
	}
	got := run(t, "x: /* falcon-asm: reached=somehow */\n\tret\n", empty(), CodeUnusedLabel)
	if len(got) != 1 || !strings.Contains(got[0].Message, "reached=somehow is not one of offset, sequence, external") {
		t.Fatalf("unknown value: %v", got)
	}
}

func TestConstantExpr(t *testing.T) {
	labels := map[string]bool{"start": true}
	for expr, want := range map[string]bool{
		"0x1FE": true, "JRES_OFF_CODE + 2": true, "(1 << 9) - 2": true, "0x1f": true, "10": true,
		"": false, "start + 510": false, ".": false, ". + 4": false, "1b": false, "10f + 2": false,
	} {
		if got := constantExpr(expr, labels); got != want {
			t.Errorf("constantExpr(%q) = %v, want %v", expr, got, want)
		}
	}
}

func TestUnusedDefine(t *testing.T) {
	text := "#ifndef GUARD_H\n#define GUARD_H\n#define USED 1\n#define UNUSED 2\n#define VIA_DEF (USED+1)\n" +
		"\tmovl $VIA_DEF, %eax\n\t.set count, 0\n\t.set count, count+1\n\t.equ lonely, 3\nalso = 4\n\t.long also\n" +
		"\t.globl exp\n\t.set exp, 1\n#if defined(COND)\n#endif\n#define COND 1\n#define FROMWS 1\n#endif\n"
	ws := &fakeWS{referenced: map[string]bool{"FROMWS": true}}
	got := run(t, text, ws, CodeUnusedDefine)
	expect(t, got, "unused-define@3 unused-define@8")
	if got[0].Message != "macro UNUSED is never used" || got[1].Message != "symbol lonely is never used" {
		t.Fatalf("messages: %v", got)
	}
	expect(t, run(t, text, &fakeWS{incomplete: true}, CodeUnusedDefine), "")
	expect(t, run(t, text, nil, CodeUnusedDefine), "")
}

func TestDuplicateLabel(t *testing.T) {
	text := "loop:\n\tjmp loop\nloop:\n#ifdef A\nalt:\n#elif B\nalt:\n#else\nalt:\n#endif\n" +
		"\t.if X\ngx:\n\t.else\ngx:\n\t.endif\n#if C\nsame:\n#endif\n#if D\nsame:\n#endif\n" +
		"\t.macro m\nml:\n\t.endm\nml:\n"
	got := run(t, text, empty(), CodeDuplicateLabel)
	expect(t, got, "duplicate-label@2 duplicate-label@19")
	f := got[0]
	if f.Severity != lint.Error || !strings.Contains(f.Message, "already defined at line 1") ||
		len(f.Related) != 1 || f.Related[0].Range.Start.Line != 0 {
		t.Fatalf("finding = %+v", f)
	}
}

func TestDuplicateGlobal(t *testing.T) {
	other := protocol.Location{URI: "file:///w/b.S", Range: protocol.Range{Start: protocol.Position{Line: 4}}}
	ws := &fakeWS{globals: map[string][]protocol.Location{"init": {other}}}
	text := "\t.globl init, solo\ninit:\nsolo:\n\tret\ninit_private:\n"
	got := run(t, text, ws, CodeDuplicateDefinition)
	expect(t, got, "duplicate-definition@1")
	if got[0].Message != "global symbol init is also defined in b.S line 5" || len(got[0].Related) != 1 {
		t.Fatalf("finding = %+v", got[0])
	}
	expect(t, run(t, text, nil, CodeDuplicateDefinition), "")
}

func TestLocalLabels(t *testing.T) {
	text := "\tjz 1f\n\tjmp 2b\n2:\n\tjmp 2b\n\tjmp 3f\n\t.long 4b\n" +
		"\t.macro m\n\tjmp 7f\n7:\n\tjmp 8b\n\t.endm\n\tjmp 7f\n\tm\n7:\n"
	got := run(t, text, empty(), CodeLocalLabelUnresolved)
	expect(t, got, "local-label-unresolved@0 local-label-unresolved@1 local-label-unresolved@4 local-label-unresolved@5")
	if got[0].Message != "1f: no local label 1: follows" || got[1].Message != "2b: no local label 2: precedes it" {
		t.Fatalf("messages: %v", got)
	}
	if got[0].Severity != lint.Error {
		t.Fatalf("severity %v", got[0].Severity)
	}
}

func TestLocalLabelFar(t *testing.T) {
	text := "1:\n\tret\n\t.globl g\ng:\n\tjmp 1b\n" + // crosses g
		"h:\n2:\n\tjmp 2b\n" + // h is private: no boundary
		"\tjmp 3f\n\t.type fn, @function\nfn:\n3:\n" + // forward across fn
		"\t.globl k\nk: 4: jmp 4b\n" + // same statement, label before N
		"\tjmp 5f\n.Lx:\n5:\n" + // .L labels never bound
		"\t.globl q\n6: q: nop\n\tjmp 6b\n" // q after 6 on the target's statement
	got := run(t, text, empty(), CodeLocalLabelFar)
	expect(t, got, "local-label-far@4 local-label-far@8 local-label-far@19")
	if !strings.Contains(got[0].Message, "crosses the global label g (line 4)") {
		t.Fatalf("message = %q", got[0].Message)
	}
}

func TestGloblUndefined(t *testing.T) {
	text := "\t.globl stage2_entyr, stage2_entry, imported, viaset, viadef, nowhere\nstage2_entry:\n\tret\n\t.set viaset, 1\n#define viadef real\n"
	ws := &fakeWS{defined: map[string]bool{"imported": true}}
	got := run(t, text, ws, CodeGloblUndefined)
	expect(t, got, "globl-undefined@0 globl-undefined@0")
	if len(got[0].Fixes) != 1 || got[0].Fixes[0].Title != "Change to stage2_entry" || len(got[1].Fixes) != 0 {
		t.Fatalf("fixes: %+v / %+v", got[0].Fixes, got[1].Fixes)
	}
	expect(t, run(t, text, nil, CodeGloblUndefined), "")
	expect(t, run(t, text, &fakeWS{incomplete: true}, CodeGloblUndefined), "")
}

func TestExternUnused(t *testing.T) {
	text := "\t.extern panic\n\t.extern puts\n\t.extern a, b\n\t.extern fromws\n\tcall puts\n\tcall a\n"
	ws := &fakeWS{referenced: map[string]bool{"fromws": true}}
	got := run(t, text, ws, CodeExternUnused)
	expect(t, got, "extern-unused@0 extern-unused@2")
	if len(got[0].Fixes) != 1 || len(got[1].Fixes) != 0 {
		t.Fatalf("fixes: %+v", got)
	}
	e := got[0].Fixes[0].Edits[0]
	if e.NewText != "" || e.Range.Start.Line != 0 || e.Range.End.Line != 1 || e.Range.End.Character != 0 {
		t.Fatalf("edit = %+v", e)
	}
	// Without a workspace only the document counts.
	expect(t, run(t, text, nil, CodeExternUnused), "extern-unused@0 extern-unused@2 extern-unused@3")
}

func TestLinkerScriptSkipped(t *testing.T) {
	text := "SECTIONS {\n.image : {\n}\n}\nlbl:\n"
	expect(t, runURI(t, "file:///w/jmod.ld.S", text, empty(), nil, allCodes...), "")
	if !isLinkerScript("file:///x/a.lds") || isLinkerScript("file:///x/a.S") {
		t.Fatal("isLinkerScript")
	}
}

func TestHelpers(t *testing.T) {
	if distance("kitten", "sitting", 5) != 3 || distance("a", "abcdef", 3) != 3 || distance("abc", "abc", 3) != 0 {
		t.Fatal("distance")
	}
	if distance("abcdef", "uvwxyz", 2) != 2 {
		t.Fatal("distance early exit")
	}
	if !looksLikeMacro("SEL_CODE32") || looksLikeMacro("Sel") || looksLikeMacro("_1") {
		t.Fatal("looksLikeMacro")
	}
	if !exclusive([]frame{{1, 0}}, []frame{{1, 1}}) || exclusive([]frame{{1, 0}}, []frame{{2, 0}}) || exclusive(nil, nil) {
		t.Fatal("exclusive")
	}
	if ws := cWords(`a "b c" 'd' 12x e_1`); len(ws) != 2 || ws[1].word != "e_1" {
		t.Fatalf("cWords = %v", ws)
	}
}
