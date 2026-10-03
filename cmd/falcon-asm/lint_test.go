package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/jsonrpc"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

const badAsm = "\t.code32\n\tmovl $1, %rax\n\t.code64\n\tpusha\n"

// tree writes files under a fresh directory, makes it the working
// directory, and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	if _, found := lint.FindConfig(filepath.Dir(dir)); found {
		t.Skip("a .falcon-asm.json exists above the temp dir")
	}
	return dir
}

func lintRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"lint"}, args...), strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestLintText(t *testing.T) {
	tree(t, map[string]string{
		"a.S":                   badAsm,
		"ok.s":                  "\tnop\n",
		"c/inline.c":            "void f(void) { __asm__(\"pusha\"); }\n",
		"build/gen.S":           badAsm, // skipped: build output
		"node_modules/x/y.S":    badAsm,
		"notes.txt":             "pusha\n",
		".falcon-asm.json":      `{"overrides": [{"paths": ["c/**"], "target": {"mode": 64}}]}`,
		"sub/.falcon-asm.json":  `{"rules": {"mode-instruction": "off"}}`,
		"sub/quiet.S":           badAsm,
		"sub/deeper/also.S":     "\tpusha\n",
		"other/unicode.S":       "\t.code32\n\t# \u00e9\u00e9\n\tmovl $1, /* \u00e9 */ %rax\n",
		"other/suppressed.S":    "\t.code64\n\tpusha # falcon-asm: ignore=mode-instruction\n",
		"other/stale-ignore.S":  "\tnop # falcon-asm: ignore=mode-register\n",
		"other/c-off/skipped.c": "void f(void) { __asm__(\"pusha\"); }\n",
	})
	code, out, errs := lintRun(t)
	want := "" +
		"a.S:2:11: warning: %rax is only valid in 64-bit code (current mode: 32-bit, set by .code32 at line 1) [mode-register]\n" +
		"a.S:4:2: warning: pusha is only valid in 16/32-bit code (current mode: 64-bit, set by .code64 at line 3) [mode-instruction]\n" +
		"c/inline.c:1:25: warning: pusha is only valid in 16/32-bit code (current mode: 64-bit, from .falcon-asm.json target.mode) [mode-instruction]\n" +
		"other/c-off/skipped.c:1:25: warning: pusha is only valid in 16/32-bit code (current mode: 64-bit, default) [mode-instruction]\n" +
		"other/stale-ignore.S:1:27: info: suppression of mode-register matched no finding [unused-suppression]\n" +
		"other/unicode.S:3:20: warning: %rax is only valid in 64-bit code (current mode: 32-bit, set by .code32 at line 1) [mode-register]\n" +
		"sub/quiet.S:2:11: warning: %rax is only valid in 64-bit code (current mode: 32-bit, set by .code32 at line 1) [mode-register]\n"
	if code != 1 || out != want {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n%s\nstderr: %s", code, out, want, errs)
	}
	if !strings.Contains(errs, "7 problems (6 warnings, 1 info) in 9 files linted") {
		t.Fatalf("stderr = %q", errs)
	}

	// The threshold decides the exit status, not what is printed.
	if code, out, _ := lintRun(t, "--severity", "error"); code != 0 || out != want {
		t.Fatalf("--severity error: exit %d", code)
	}
	// Without C, directories contribute assembly only; flags may follow
	// paths, and "--" ends them.
	code, out, _ = lintRun(t, "other", "--c=false", "--", "ok.s")
	if code != 1 || strings.Contains(out, ".c:") || !strings.Contains(out, "other/unicode.S") {
		t.Fatalf("--c=false: exit %d\n%s", code, out)
	}
	// An explicitly named file is linted whatever it is called.
	if code, out, _ := lintRun(t, "notes.txt", "ok.s"); code != 1 || !strings.HasPrefix(out, "notes.txt:1:1: warning: pusha") {
		t.Fatalf("explicit files: exit %d %q", code, out)
	}
	code, _, errs = lintRun(t, "ok.s")
	if code != 0 || !strings.Contains(errs, "1 file linted, no problems") {
		t.Fatalf("clean: exit %d %q", code, errs)
	}
	// Without a compile database the listing rules could not run, and the
	// summary says so.
	assertQualifiedClean(t, errs)
}

func TestLintJSONAndSARIF(t *testing.T) {
	tree(t, map[string]string{"a.S": badAsm})
	code, out, _ := lintRun(t, "--format=json")
	var items []struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
		Code     string `json:"code"`
		Severity string `json:"severity"`
		Source   string `json:"source"`
		Fixes    []struct {
			Title string `json:"title"`
			Edits []struct {
				NewText string `json:"newText"`
			} `json:"edits"`
		} `json:"fixes"`
	}
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if code != 1 || len(items) != 2 || items[0].File != "a.S" || items[0].Line != 2 || items[0].Column != 11 ||
		items[0].Severity != "warning" || items[0].Source != "falcon-asm" || items[0].Fixes[0].Edits[0].NewText != "eax" {
		t.Fatalf("json items = %+v", items)
	}
	if _, out, _ := lintRun(t, "--format=json", "--severity=hint", "a.S", "--config", "/dev/null"); !strings.HasPrefix(out, "[") {
		t.Fatalf("json with an empty config = %q", out)
	}

	code, out, _ = lintRun(t, "--format", "sarif")
	var log struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				RuleIndex int    `json:"ruleIndex"`
				Level     string `json:"level"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI       string `json:"uri"`
							URIBaseID string `json:"uriBaseId"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine, StartColumn int
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				Fixes []json.RawMessage `json:"fixes"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	if code != 1 || log.Version != "2.1.0" || len(log.Runs) != 1 || len(log.Runs[0].Results) != 2 {
		t.Fatalf("sarif = %s", out)
	}
	run0 := log.Runs[0]
	for _, r := range run0.Results {
		if run0.Tool.Driver.Rules[r.RuleIndex].ID != r.RuleID || r.Level != "warning" {
			t.Fatalf("result %+v does not index its rule", r)
		}
	}
	loc := run0.Results[0].Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "a.S" || loc.ArtifactLocation.URIBaseID != "SRCROOT" || loc.Region.StartLine != 2 || loc.Region.StartColumn != 11 || len(run0.Results[0].Fixes) != 1 {
		t.Fatalf("location = %+v", loc)
	}
}

func TestLintFix(t *testing.T) {
	dir := tree(t, map[string]string{
		"a.S":              badAsm,
		"b.c":              "void f(void) { __asm__(\"pushl %%rax\" ::: \"memory\"); } // falcon-asm: level=v1\n",
		".falcon-asm.json": `{"target": {"mode": 32}}`,
	})
	code, out, errs := lintRun(t, "--fix")
	if code != 1 || !strings.Contains(errs, "a.S: applied 1 fix\n") || !strings.Contains(errs, "b.c: applied 1 fix\n") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errs)
	}
	// The mode-instruction finding has no fix and remains.
	if out != "a.S:4:2: warning: pusha is only valid in 16/32-bit code (current mode: 64-bit, set by .code64 at line 3) [mode-instruction]\n" {
		t.Fatalf("after fix: %q", out)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.S"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.c"))
	if string(a) != strings.Replace(badAsm, "%rax", "%eax", 1) || !strings.Contains(string(b), `"pushl %%eax"`) {
		t.Fatalf("fixed files:\n%s\n%s", a, b)
	}
	// Nothing left to fix: nothing written.
	if _, _, errs := lintRun(t, "--fix"); strings.Contains(errs, "applied") {
		t.Fatalf("second --fix: %q", errs)
	}
}

func rangeOf(s, e int) protocol.Range {
	return protocol.Range{Start: protocol.Position{Character: s}, End: protocol.Position{Character: e}}
}

func edit(s, e int, text string) protocol.TextEdit {
	return protocol.TextEdit{Range: rangeOf(s, e), NewText: text}
}

func TestApplyFixesConflicts(t *testing.T) {
	text := "abcdef"
	mk := func(s, e int, nt string) item {
		return item{Finding: lint.Finding{Fixes: []lint.Fix{{Edits: []protocol.TextEdit{edit(s, e, nt)}}}}}
	}
	items := []item{mk(1, 3, "X"), mk(2, 4, "Y"), mk(4, 4, "Z"), mk(4, 4, "W"), mk(5, 6, "V"), {}}
	got, n := applyFixes(text, items)
	if got != "aXdZeV" || n != 3 {
		t.Fatalf("applyFixes = %q, %d", got, n)
	}
	bad := item{Finding: lint.Finding{Fixes: []lint.Fix{{Edits: []protocol.TextEdit{{Range: rangeOf(-1, 0)}}}}}}
	if got, n := applyFixes(text, []item{bad}); got != text || n != 0 {
		t.Fatalf("invalid edit applied: %q", got)
	}
}

func TestLintUsageErrors(t *testing.T) {
	tree(t, map[string]string{
		"a.S":                     badAsm,
		"broken/.falcon-asm.json": `{"rules": `,
		"broken/b.S":              "\tnop\n",
		"warned/.falcon-asm.json": `{"rules": {"no-such-rule": "off"}}`,
		"warned/w.S":              "\tnop\n",
	})
	tests := []struct {
		name   string
		args   []string
		code   int
		stderr string
	}{
		{"format", []string{"--format=xml"}, 2, `unknown --format "xml"`},
		{"severity", []string{"--severity=loud"}, 2, `unknown --severity "loud"`},
		{"severity off", []string{"--severity=off"}, 2, `unknown --severity "off"`},
		{"flag", []string{"--nope"}, 2, "flag provided but not defined"},
		{"missing path", []string{"missing.S"}, 2, "missing.S"},
		{"config read", []string{"--config", "missing.json", "a.S"}, 2, "read missing.json"},
		{"config broken", []string{"broken"}, 2, "broken/.falcon-asm.json:1:11: error: invalid JSON"},
		{"explicit broken", []string{"--config", "broken/.falcon-asm.json", "a.S"}, 2, "invalid JSON"},
		{"config warning", []string{"warned"}, 0, `warned/.falcon-asm.json:1:12: warning: unknown rule code "no-such-rule"`},
		{"ccdb", []string{"--compile-commands", "nope.json", "a.S"}, 2, "--compile-commands"},
		{"jobs", []string{"--jobs=0", "warned"}, 0, "1 file linted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errs := lintRun(t, tt.args...)
			if code != tt.code || !strings.Contains(errs, tt.stderr) {
				t.Fatalf("exit %d, stderr %q; want %d and %q", code, errs, tt.code, tt.stderr)
			}
		})
	}
}

func TestLintChanged(t *testing.T) {
	tree(t, map[string]string{"a.S": badAsm})
	// The temp dir is not a repository: --changed falls back to all.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if _, err := gitOutput(".", "rev-parse", "--show-toplevel"); err == nil {
		t.Skip("the temp dir is inside a git repository")
	}
	code, out, errs := lintRun(t, "--changed")
	if code != 1 || !strings.Contains(errs, "not a git repository") || !strings.Contains(out, "a.S") {
		t.Fatalf("exit %d %q %q", code, out, errs)
	}
	files := []lintFile{{abs: "/r/a.S"}, {abs: "/r/b.S"}}
	if got := filterChanged(files, map[string]bool{"/r/b.S": true}); len(got) != 1 || got[0].abs != "/r/b.S" {
		t.Fatalf("filterChanged = %+v", got)
	}
}

// changedFiles inside this repository only reads it.
func TestChangedFilesInRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if _, err := gitOutput(".", "rev-parse", "HEAD"); err != nil {
		t.Skip("not inside a repository with a commit")
	}
	set, ok := changedFiles()
	if !ok {
		t.Fatal("changedFiles failed inside a repository")
	}
	for p := range set {
		if !filepath.IsAbs(p) {
			t.Fatalf("relative path %q", p)
		}
	}
}

// --toolchain assembles with the database's compiler; gas's diagnostic on
// a line falcon-asm already flags is dropped, the others are kept.
func TestLintToolchain(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("no gcc")
	}
	dir := tree(t, map[string]string{
		"t.S": "\t.code32\n\tmovl $1, %rax\n\tbogus_insn\n",
	})
	db := `[{"directory": "` + dir + `", "file": "t.S", "arguments": ["gcc", "-m32", "-c", "t.S"]}]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(db), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := lintRun(t, "--toolchain", "t.S")
	if code != 1 || !strings.Contains(out, "t.S:2:11: warning:") || !strings.Contains(out, "t.S:3:") || !strings.Contains(out, "[gas]") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if strings.Count(out, "t.S:2:") != 1 {
		t.Fatalf("gas not deduplicated on line 2:\n%s", out)
	}
	// The same through an explicit database, in SARIF: gas gets a rule.
	code, out, _ = lintRun(t, "--toolchain", "--compile-commands", "compile_commands.json", "--format", "sarif", "t.S")
	if code != 1 || !strings.Contains(out, `"ruleId": "gas"`) {
		t.Fatalf("sarif with gas: %s", out)
	}
	// A compiler that is not installed is noted once, and linting goes on.
	db = `[{"directory": "` + dir + `", "file": "t.S", "arguments": ["no-such-cc-for-falcon", "-c", "t.S"]},` +
		`{"directory": "` + dir + `", "file": "u.S", "arguments": ["no-such-cc-for-falcon", "-c", "u.S"]}]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(db), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "u.S"), []byte("\tnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs = lintRun(t, "--toolchain", "--jobs=1", "t.S", "u.S")
	if code != 1 || strings.Count(errs, "gas diagnostics are off") != 1 || strings.Contains(out, "[gas]") {
		t.Fatalf("missing compiler: exit %d\n%s\n%s", code, out, errs)
	}
}

// A file outside the working directory is named by its absolute path, and
// its SARIF location is a file URI rather than relative to SRCROOT.
func TestLintOutsideWorkingDir(t *testing.T) {
	other := t.TempDir()
	p := filepath.Join(other, "far.S")
	if err := os.WriteFile(p, []byte(badAsm), 0o644); err != nil {
		t.Fatal(err)
	}
	tree(t, map[string]string{})
	code, out, _ := lintRun(t, p)
	if code != 1 || !strings.HasPrefix(out, p+":2:11:") {
		t.Fatalf("text: %q", out)
	}
	_, out, _ = lintRun(t, "--format=sarif", p)
	if !strings.Contains(out, `"uri": "file://`+p+`"`) {
		t.Fatalf("sarif: %s", out)
	}
}

func TestRulesCommand(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run([]string{"rules"}, nil, &out, &errs); code != 0 || !strings.Contains(out.String(), "mode-register") || !strings.HasPrefix(out.String(), "CODE") {
		t.Fatalf("list: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"rules", "mode-register", "level"}, nil, &out, &errs); code != 0 ||
		!strings.Contains(out.String(), "mode-register (section 2") || !strings.Contains(out.String(), "\nlevel (section 2") {
		t.Fatalf("docs: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"rules", "--markdown"}, nil, &out, &errs); code != 0 || out.String() != lint.Markdown() {
		t.Fatal("markdown")
	}
	if code := run([]string{"rules", "nope"}, nil, &out, &errs); code != 2 || !strings.Contains(errs.String(), `unknown rule "nope"`) {
		t.Fatalf("unknown: %d %q", code, errs.String())
	}
	out.Reset()
	if code := run([]string{"rules", "-h"}, nil, &out, &errs); code != 0 || !strings.Contains(out.String(), "usage: falcon-asm rules") {
		t.Fatal("help")
	}
	if code := run([]string{"rules", "--bad"}, nil, &out, &errs); code != 2 {
		t.Fatal("bad flag")
	}
}

// TestCodeActionSession drives the real server and engine over stdio: a
// document with a fixable finding gets its quick fixes.
func TestCodeActionSession(t *testing.T) {
	tree(t, map[string]string{})
	open := `{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":"file:///nowhere/x.S","languageId":"falcon-asm","version":1,"text":"\t.code32\n\tpushl %rax\n"}}}`
	action := `{"jsonrpc":"2.0","id":3,"method":"textDocument/codeAction","params":{"textDocument":{"uri":"file:///nowhere/x.S"},"range":{"start":{"line":1,"character":8},"end":{"line":1,"character":8}},"context":{"diagnostics":[]}}}`
	in := frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":null,"rootUri":null,"capabilities":{},"initializationOptions":{"toolchain":false}}}`) +
		frame(`{"jsonrpc":"2.0","method":"initialized","params":{}}`) +
		frame(open) + frame(action) +
		frame(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`) +
		frame(`{"jsonrpc":"2.0","method":"exit"}`)
	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader(in), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	r := bufio.NewReader(&stdout)
	for {
		m, err := jsonrpc.Read(r)
		if err != nil {
			t.Fatalf("no codeAction response: %v", err)
		}
		if m.ID == nil || string(*m.ID) != "3" {
			continue
		}
		var acts []struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		}
		if err := json.Unmarshal(m.Result, &acts); err != nil {
			t.Fatal(err)
		}
		if len(acts) != 2 || acts[0].Title != "Use %eax (the 32-bit register)" || acts[1].Title != "Ignore mode-register on this line" || acts[0].Kind != "quickfix" {
			t.Fatalf("actions = %+v", acts)
		}
		return
	}
}
