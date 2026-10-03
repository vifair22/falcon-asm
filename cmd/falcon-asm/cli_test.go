package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// misalignedGDT trips table-alignment, a listing rule: the GDT's offset
// is only known from the assembler's listing.
const misalignedGDT = "\t.balign 8\n\t.byte 0\ngdt:\n\t.quad 0, 0x00cf9a000000ffff\ngdt_desc:\n" +
	"\t.word . - gdt - 1\n\t.long gdt\n\tlgdt gdt_desc\n"

// writeCCDB writes a compile_commands.json in dir with one gcc entry per
// file.
func writeCCDB(t *testing.T, dir string, files ...string) {
	t.Helper()
	var entries []map[string]any
	for _, f := range files {
		entries = append(entries, map[string]any{"directory": dir, "file": f, "arguments": []string{"gcc", "-c", f}})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func needGCC(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("no gcc")
	}
}

// The assembler runs by default: a listing rule's finding is reported
// without --toolchain, and --no-toolchain says the rule was skipped rather
// than calling the file clean.
func TestLintToolchainDefault(t *testing.T) {
	needGCC(t)
	dir := tree(t, map[string]string{"g.S": misalignedGDT})
	writeCCDB(t, dir, "g.S")

	code, out, errs := lintRun(t, "--severity", "info", "g.S")
	if code != 1 || !strings.Contains(out, "[table-alignment]") || strings.Contains(errs, "skipped") {
		t.Fatalf("default: exit %d\nstdout %s\nstderr %s", code, out, errs)
	}
	// --toolchain is the default spelled out.
	if code, out2, _ := lintRun(t, "--toolchain", "--severity", "info", "g.S"); code != 1 || out2 != out {
		t.Fatalf("--toolchain: exit %d\n%s", code, out2)
	}

	for _, args := range [][]string{{"--no-toolchain"}, {"--toolchain=false"}} {
		code, out, errs = lintRun(t, append(args, "--severity", "info", "g.S")...)
		if code != 0 || strings.Contains(out, "table-alignment") {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
		if !strings.Contains(errs, "note: ") || !strings.Contains(errs, "1 with --no-toolchain): g.S; drop --no-toolchain") {
			t.Fatalf("%v: no skipped note:\n%s", args, errs)
		}
		assertQualifiedClean(t, errs)
	}
}

// assertQualifiedClean fails unless the summary says "no problems" with the
// skipped-rules qualifier: a run that skipped rules must never read clean.
func assertQualifiedClean(t *testing.T, errs string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(errs), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "no problems (") || !strings.Contains(last, "listing rule") || !strings.HasSuffix(last, ")") {
		t.Fatalf("summary %q is not qualified", last)
	}
	if !strings.HasPrefix(lines[len(lines)-2], "note: ") {
		t.Fatalf("the note does not precede the summary:\n%s", errs)
	}
}

// A file without a compile command (not even a neighbour's to borrow)
// cannot be assembled: the note names it and the reason, JSON stays an array, json-full and SARIF carry the
// skip, and the exit status is untouched.
func TestLintSkippedNoCompileCommand(t *testing.T) {
	needGCC(t)
	dir := tree(t, map[string]string{"g.S": misalignedGDT, "other/lone.S": "\tnop\n", "c.c": "void f(void) { __asm__(\"nop\"); }\n"})
	writeCCDB(t, dir, "g.S")

	code, out, errs := lintRun(t, "other/lone.S", "c.c")
	if code != 0 || out != "" {
		t.Fatalf("exit %d %q", code, out)
	}
	if !strings.Contains(errs, "listing rules skipped on 1 file (no assembler listing: 1 without a compile command): other/lone.S;") ||
		!strings.Contains(errs, "compile_commands.json entry") {
		t.Fatalf("stderr:\n%s", errs)
	}
	assertQualifiedClean(t, errs)
	// A file beside one with an entry borrows it, as in the editor, and
	// is assembled.
	if err := os.WriteFile(filepath.Join(dir, "near.S"), []byte("\tnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := lintRun(t, "near.S"); code != 0 || errs != "1 file linted, no problems\n" {
		t.Fatalf("borrowed entry: exit %d %q", code, errs)
	}
	if err := os.Remove(filepath.Join(dir, "near.S")); err != nil {
		t.Fatal(err)
	}

	// With findings, the problem summary carries the qualifier too.
	code, _, errs = lintRun(t, "--severity", "info", ".")
	if code != 1 || !strings.Contains(errs, "in 3 files linted (") || !strings.Contains(errs, "skipped on 1 file)") {
		t.Fatalf("exit %d:\n%s", code, errs)
	}

	code, out, errs = lintRun(t, "--format", "json", "other/lone.S")
	var arr []jsonItem
	if code != 0 || json.Unmarshal([]byte(out), &arr) != nil || !strings.Contains(errs, "note: ") {
		t.Fatalf("json: exit %d %q %q", code, out, errs)
	}

	_, out, _ = lintRun(t, "--format", "json-full", "--severity", "info", ".")
	var full jsonFull
	if err := json.Unmarshal([]byte(out), &full); err != nil {
		t.Fatalf("json-full: %v\n%s", err, out)
	}
	if full.Files != 3 || len(full.Findings) == 0 || len(full.Skipped) != 1 || full.Skipped[0].File != "other/lone.S" ||
		full.Skipped[0].Reason != "no-compile-command" || len(full.Skipped[0].Rules) == 0 {
		t.Fatalf("json-full = %+v", full)
	}

	_, out, _ = lintRun(t, "--format", "sarif", ".")
	var log sarifLog
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatal(err)
	}
	inv := log.Runs[0].Invocations
	if len(inv) != 1 || !inv[0].ExecutionSuccessful || len(inv[0].ToolExecutionNotifications) != 1 {
		t.Fatalf("sarif invocations = %+v", inv)
	}
	n := inv[0].ToolExecutionNotifications[0]
	if n.Descriptor.ID != sarifSkippedID || n.Locations[0].PhysicalLocation.ArtifactLocation.URI != "other/lone.S" {
		t.Fatalf("notification = %+v", n)
	}

	// A configuration that turns every listing rule off has nothing to
	// skip.
	var off []string
	for _, c := range listingRulesAll(t) {
		off = append(off, `"`+c+`": "off"`)
	}
	if err := os.WriteFile(filepath.Join(dir, ".falcon-asm.json"), []byte(`{"rules": {`+strings.Join(off, ", ")+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := lintRun(t, "other/lone.S"); code != 0 || strings.Contains(errs, "skipped") || !strings.HasSuffix(errs, "no problems\n") {
		t.Fatalf("all listing rules off: exit %d %q", code, errs)
	}
}

func listingRulesAll(t *testing.T) []string {
	t.Helper()
	// The zero configuration leaves every rule at its default.
	codes := listingRules(lint.FileConfig{})
	if len(codes) == 0 {
		t.Fatal("no listing rules registered")
	}
	return codes
}

// A compiler that is missing or a file that fails to assemble are both
// reported as skipped, with their own reasons.
func TestLintSkippedFailures(t *testing.T) {
	needGCC(t)
	dir := tree(t, map[string]string{"bad.S": "#include \"missing.h\"\n\tnop\n", "nocc.S": "\tnop\n"})
	db := `[{"directory": "` + dir + `", "file": "bad.S", "arguments": ["gcc", "-c", "bad.S"]},` +
		`{"directory": "` + dir + `", "file": "nocc.S", "arguments": ["no-such-cc-for-falcon", "-c", "nocc.S"]}]`
	if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(db), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := lintRun(t, ".")
	if code != 1 || !strings.Contains(out, "bad.S:1:") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(errs, "on 2 files (no assembler listing: 1 whose compiler was not found, 1 failed to assemble): bad.S, nocc.S;") {
		t.Fatalf("stderr:\n%s", errs)
	}
}

// The skip note names a handful of files and counts the rest.
func TestSkipNoteTruncates(t *testing.T) {
	var skipped []skippedFile
	files := make([]lintFile, maxNamed+2)
	for i := range files {
		files[i].display = string(rune('a'+i)) + ".S"
		skipped = append(skipped, skippedFile{file: &files[i], listingSkip: listingSkip{reason: skipNoEntry, rules: []string{"x", "y"}}})
	}
	sum := summarizeSkips(skipped)
	if got := sum.short(); got != "2 listing rules skipped on 7 files" {
		t.Fatalf("short = %q", got)
	}
	if note := sum.note(); !strings.Contains(note, "e.S and 2 more;") || strings.Contains(note, "f.S") {
		t.Fatalf("note = %q", note)
	}
}

// helpRun runs the binary's entrypoint and wants help on stdout, status 0.
func helpRun(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("%v: exit %d, stderr %q", args, code, stderr.String())
	}
	return stdout.String()
}

// flagsIn fails for every flag of fs that text does not document. The
// expectation comes from the flag set the parser uses, so a new flag
// cannot be left out of the help.
func flagsIn(t *testing.T, what, text string, fs *flag.FlagSet) {
	t.Helper()
	n := 0
	fs.VisitAll(func(f *flag.Flag) {
		n++
		if !strings.Contains(text, "  -"+f.Name) || !strings.Contains(text, f.Usage) {
			t.Errorf("%s help does not document -%s", what, f.Name)
		}
	})
	if n == 0 {
		t.Fatalf("%s: empty flag set", what)
	}
}

func TestHelp(t *testing.T) {
	var o lintOptions
	var sev string
	var noTC bool
	lintFlags := lintFlagSet(&o, &sev, &noTC)
	for _, args := range [][]string{{"lint", "--help"}, {"lint", "-h"}, {"help", "lint"}, {"lint", "src", "--fix", "-help"}} {
		text := helpRun(t, args...)
		flagsIn(t, strings.Join(args, " "), text, lintFlags)
		for _, want := range []string{"usage: falcon-asm lint", "Exit status:", "  0  ", "  1  ", "  2  ", "Examples:",
			"--severity hint", "--changed", "--fix", "--format sarif", "--jobs", "--no-toolchain", "--compile-commands", "--config", "--c=false",
			"listing rules skipped"} {
			if !strings.Contains(text, want) {
				t.Errorf("%v: help lacks %q", args, want)
			}
		}
	}

	var v bool
	serverFlags := serverFlagSet(&v)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"help", "server"}} {
		text := helpRun(t, args...)
		flagsIn(t, strings.Join(args, " "), text, serverFlags)
		for _, want := range []string{"usage: falcon-asm [command]", "\n  lint ", "\n  rules ", "\n  version ", "\n  help ",
			"language server", "help lint"} {
			if !strings.Contains(text, want) {
				t.Errorf("%v: help lacks %q", args, want)
			}
		}
	}

	var md bool
	rulesFlags := rulesFlagSet(&md)
	for _, args := range [][]string{{"rules", "--help"}, {"help", "rules"}} {
		text := helpRun(t, args...)
		flagsIn(t, strings.Join(args, " "), text, rulesFlags)
		if !strings.Contains(text, "usage: falcon-asm rules") || !strings.Contains(text, "Examples:") {
			t.Errorf("%v: %q", args, text)
		}
	}

	// A help flag after "--" is a path, not a request for help.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"lint", "--", "-h"}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "-h") {
		t.Fatalf("lint -- -h: exit %d %q", code, stderr.String())
	}
}

// Usage errors still exit 2 with a pointer to the help on stderr.
func TestUsageErrorsPointAtHelp(t *testing.T) {
	for _, args := range [][]string{{"lint", "--nope"}, {"rules", "--nope"}, {"--nope"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--help") || stdout.Len() != 0 {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
}
