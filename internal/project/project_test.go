package project

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"

	_ "git.airies.net/vifair22/falcon-asm/internal/rules/all"
)

func TestFileKinds(t *testing.T) {
	for p, want := range map[string][2]bool{
		"a.S": {true, false}, "b.s": {true, false}, "c.c": {false, true}, "d.H": {false, true},
		"e.cpp": {false, true}, "f.asm": {false, false}, "README": {false, false},
	} {
		if IsAsm(p) != want[0] || IsC(p) != want[1] {
			t.Errorf("%s: asm %v c %v", p, IsAsm(p), IsC(p))
		}
	}
}

func TestResolveTarget(t *testing.T) {
	flags := toolchain.Flags{Compiler: "/usr/bin/gcc", Mode: 32, Level: 2, LevelSource: "-march=x86-64-v2"}
	comment := LevelComment{Level: 4, Source: "comment", OK: true}
	tests := []struct {
		name    string
		in      TargetInputs
		comment LevelComment
		want    lint.Target
	}{
		{"nothing", TargetInputs{}, LevelComment{},
			lint.Target{Mode: 64, Sources: map[string]string{}}},
		{"flags", TargetInputs{Flags: flags, HasFlags: true}, LevelComment{},
			lint.Target{Mode: 32, Level: 2, CC: "cdecl", Sources: map[string]string{"mode": "compile flags", "level": "-march=x86-64-v2", "cc": "inferred from compile flags"}}},
		{"setting beats flags", TargetInputs{Flags: flags, HasFlags: true, SettingLevel: 3, SettingSource: "setting"}, LevelComment{},
			lint.Target{Mode: 32, Level: 3, CC: "cdecl", Sources: map[string]string{"mode": "compile flags", "level": "setting", "cc": "inferred from compile flags"}}},
		{"config beats setting", TargetInputs{Flags: flags, HasFlags: true, SettingLevel: 3, Config: lint.TargetConfig{Level: "v1", Mode: 16, CPU: "i386", CC: "none"}}, LevelComment{},
			lint.Target{Mode: 16, Level: 1, CPU: "i386", Sources: map[string]string{"mode": ".falcon-asm.json target.mode", "level": ".falcon-asm.json target.level v1", "cpu": ".falcon-asm.json target.cpu", "cc": ".falcon-asm.json target.cc"}}},
		{"comment beats all", TargetInputs{Config: lint.TargetConfig{Level: "v1", CC: "ms"}}, comment,
			lint.Target{Mode: 64, Level: 4, CC: "ms", Sources: map[string]string{"level": "comment", "cc": ".falcon-asm.json target.cc"}}},
		{"config none", TargetInputs{Flags: flags, HasFlags: true, Config: lint.TargetConfig{Level: "none"}}, LevelComment{},
			lint.Target{Mode: 32, CC: "cdecl", Sources: map[string]string{"mode": "compile flags", "level": ".falcon-asm.json target.level none", "cc": "inferred from compile flags"}}},
		{"march cpu, mingw", TargetInputs{HasFlags: true, Flags: toolchain.Flags{Compiler: "x86_64-w64-mingw32-gcc", Mode: 64, Args: []string{"-march=x86-64-v2", "-march=Haswell"}}}, LevelComment{},
			lint.Target{Mode: 64, CPU: "haswell", CC: "ms", Sources: map[string]string{"mode": "compile flags", "cpu": "-march=haswell", "cc": "inferred from compile flags"}}},
		{"sysv, native", TargetInputs{HasFlags: true, Flags: toolchain.Flags{Compiler: "gcc", Mode: 64, Args: []string{"-march=native"}}}, LevelComment{},
			lint.Target{Mode: 64, CC: "sysv", Sources: map[string]string{"mode": "compile flags", "cc": "inferred from compile flags"}}},
		{"16-bit", TargetInputs{HasFlags: true, Flags: toolchain.Flags{Compiler: "gcc", Mode: 16}}, LevelComment{},
			lint.Target{Mode: 16, Sources: map[string]string{"mode": "compile flags"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTarget(tt.in, tt.comment)
			if got.Mode != tt.want.Mode || got.Level != tt.want.Level || got.CPU != tt.want.CPU || got.CC != tt.want.CC || len(got.Sources) != len(tt.want.Sources) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
			for k, v := range tt.want.Sources {
				if got.Sources[k] != v {
					t.Fatalf("source %s = %q, want %q", k, got.Sources[k], v)
				}
			}
		})
	}
}

func TestLevelComments(t *testing.T) {
	c := AsmLevelComment(lexer.Tokenize("\tnop\n# falcon-asm: level=v2\n"))
	if !c.OK || c.Level != isa.Level(2) || c.Source != "falcon-asm: level comment at line 2" {
		t.Fatalf("asm = %+v", c)
	}
	if c := AsmLevelComment(nil); c.OK {
		t.Fatal("no comment")
	}
	if c := CLevelComment("// falcon-asm: level=v3\nint x;\n"); !c.OK || c.Level != 3 {
		t.Fatalf("C = %+v", c)
	}
	if c := CLevelComment("int x;\n"); c.OK {
		t.Fatal("no C comment")
	}
}

func TestLintAsm(t *testing.T) {
	text := "\t.code32\n\tpushl %rax\n\tpushl %rbx /* falcon-asm: ignore=mode-register */\n"
	doc := &docstore.Document{URI: "file:///a.S", Text: text}
	res := Lint(Doc{Doc: doc, Target: lint.Target{Mode: 64}})
	if len(res.Findings) != 1 || res.Findings[0].Code != "mode-register" || res.Findings[0].Range.Start.Line != 1 || res.Suppressions == nil {
		t.Fatalf("findings = %v", res.Findings)
	}
	off := Lint(Doc{Doc: doc, Toks: lexer.Tokenize(text), Config: lint.FileConfig{Rules: map[string]lint.Severity{"mode-register": lint.Off}}})
	if len(off.Findings) != 0 {
		t.Fatalf("rule off: %v", off.Findings)
	}
}

func TestLintC(t *testing.T) {
	src := "" +
		"void f(void) {\n" + // 0
		"\t__asm__ volatile (\"pushq %%rax\\n\\t\" \"popq %%rbx\" ::: \"memory\");\n" + // 1
		"\t__asm__ (\"push %rax\"); // falcon-asm: ignore=mode-register\n" + // 2
		"\t__asm__ (FOO \"pushq %rax\");\n" + // 3: partial, skipped
		"\t__asm__ (\"push %r\\x63x\");\n" + // 4: escape inside the name: no fix
		"}\n"
	doc := &docstore.Document{URI: "file:///x.c", Text: src}
	res := Lint(Doc{Doc: doc, C: true, Target: lint.Target{Mode: 32, Sources: map[string]string{"mode": "compile flags"}}})
	var got []string
	for _, f := range res.Findings {
		line := strings.Split(src, "\n")[f.Range.Start.Line]
		got = append(got, f.Code+"@"+line[f.Range.Start.Character:f.Range.End.Character])
	}
	// pushq %rax; popq %rbx really does clobber rbx undeclared.
	want := "mode-register@%%rax asm-missing-clobber@%%rbx mode-register@%%rbx mode-register@%r\\x63x"
	if strings.Join(got, " ") != want {
		t.Fatalf("findings = %q\nwant %q", got, want)
	}
	// The register fix replaces only the name, which reads the same in
	// the template and in the C source...
	fx := res.Findings[0].Fixes
	if len(fx) != 1 || fx[0].Edits[0].NewText != "eax" || fx[0].Edits[0].Range.Start != (protocol.Position{Line: 1, Character: 28}) {
		t.Fatalf("fix = %+v", fx)
	}
	// ...but not when the C source spells the template differently.
	if len(res.Findings[3].Fixes) != 0 {
		t.Fatalf("fix through an escape = %+v", res.Findings[2].Fixes)
	}
	// Statements passed in are used as they are.
	if res := Lint(Doc{Doc: doc, C: true, Inline: []inlineasm.Stmt{}, Target: lint.Target{Mode: 32}}); len(res.Findings) != 0 {
		t.Fatalf("with no statements: %v", res.Findings)
	}
}

func TestMapping(t *testing.T) {
	src := "asm(\"mov %eax, %ebx\");\n"
	cdoc := &docstore.Document{Text: src}
	st := &inlineasm.Extract(src)[0]
	vdoc := &docstore.Document{Text: st.Asm}
	if _, ok := MapRange(cdoc, vdoc, st, protocol.Range{Start: protocol.Position{Line: -1}}); ok {
		t.Fatal("out-of-range position mapped")
	}
	r, ok := MapRange(cdoc, vdoc, st, protocol.Range{Start: protocol.Position{Character: 4}, End: protocol.Position{Character: 8}})
	if !ok || src[r.Start.Character:r.End.Character] != "%eax" {
		t.Fatalf("mapped %+v", r)
	}
	edit := func(c1, c2 int, text string) (protocol.TextEdit, bool) {
		return mapEdit(cdoc, vdoc, st, protocol.TextEdit{Range: protocol.Range{Start: protocol.Position{Character: c1}, End: protocol.Position{Character: c2}}, NewText: text})
	}
	if e, ok := edit(0, 0, "cld; "); !ok || e.Range.Start.Character != 5 {
		t.Fatalf("insert = %+v %v", e, ok)
	}
	for _, bad := range []string{"a\nb", "%eax", "\"", "\\"} {
		if _, ok := edit(5, 8, bad); ok {
			t.Errorf("edit with %q mapped", bad)
		}
	}
	if _, ok := mapEdit(cdoc, vdoc, st, protocol.TextEdit{Range: protocol.Range{Start: protocol.Position{Line: -1}}}); ok {
		t.Fatal("out-of-range edit mapped")
	}
}
