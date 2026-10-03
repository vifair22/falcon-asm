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
	"time"
)

const fixtureDir = "../../testdata/falcon-bios"

func TestPercentile(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 20; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	cases := []struct {
		p    float64
		want time.Duration
	}{{50, 10 * time.Millisecond}, {95, 19 * time.Millisecond}, {99, 20 * time.Millisecond}, {0, time.Millisecond}}
	for _, c := range cases {
		if got := percentile(d, c.p); got != c.want {
			t.Errorf("p%v = %v, want %v", c.p, got, c.want)
		}
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty = %v", got)
	}
}

func TestRecorderReport(t *testing.T) {
	r := newRecorder()
	r.add("b", 2*time.Millisecond, 10)
	r.add("a", 1500*time.Microsecond, 20)
	r.add("b", 4*time.Millisecond, 30)
	r.value("rss", "kB", 42)
	rep := r.report(Env{CPU: "x"})
	if len(rep.Latencies) != 2 || rep.Latencies[0].Name != "b" || rep.Latencies[1].Name != "a" {
		t.Fatalf("order: %+v", rep.Latencies)
	}
	b := rep.Latencies[0]
	if b.Count != 2 || b.P50 != 2 || b.Max != 4 || b.Mean != 3 || b.Bytes != 20 {
		t.Errorf("b = %+v", b)
	}
	if rep.Latencies[1].P99 != 1.5 {
		t.Errorf("a = %+v", rep.Latencies[1])
	}
	if len(rep.Values) != 1 || rep.Values[0].Value != 42 {
		t.Errorf("values = %+v", rep.Values)
	}
	if empty := newRecorder().report(Env{}); empty.Values == nil {
		t.Error("values must encode as [] not null")
	}
}

func TestTableJSONCompare(t *testing.T) {
	old := Report{
		Latencies: []Latency{{Name: "m", Count: 1, P50: 2, P95: 4, Max: 8}, {Name: "gone", Count: 1}},
		Values:    []Value{{Name: "rss", Unit: "kB", Value: 100}},
	}
	cur := Report{
		Latencies: []Latency{{Name: "m", Count: 1, P50: 1, P95: 4, Max: 0}, {Name: "fresh", Count: 1}},
		Values:    []Value{{Name: "rss", Unit: "kB", Value: 50}, {Name: "new", Unit: "x", Value: 1}},
	}
	var buf bytes.Buffer
	if err := writeTable(&buf, cur); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "fresh") || !strings.Contains(buf.String(), "rss") {
		t.Errorf("table:\n%s", buf.String())
	}
	dir := t.TempDir()
	po, pn := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	if err := writeJSON(po, old); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(pn, cur); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if code := run([]string{"-compare", po, pn}, &buf, &buf); code != 0 {
		t.Fatalf("compare exit %d: %s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"-50.0%", "+0.0%", "-100.0%", "only in the new report: fresh, new"} {
		if !strings.Contains(out, want) {
			t.Errorf("compare output lacks %q:\n%s", want, out)
		}
	}
	if code := run([]string{"-compare", po}, &buf, &buf); code != 2 {
		t.Errorf("one file: exit %d", code)
	}
	if code := run([]string{"-compare", po, filepath.Join(dir, "missing.json")}, &buf, &buf); code != 1 {
		t.Errorf("missing file: exit %d", code)
	}
	if err := os.WriteFile(pn, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readReport(pn); err == nil {
		t.Error("bad JSON accepted")
	}
	if err := writeJSON(filepath.Join(dir, "no", "such", "dir.json"), cur); err == nil {
		t.Error("write into a missing directory succeeded")
	}
}

func TestDelta(t *testing.T) {
	for _, c := range []struct {
		old, cur float64
		want     string
	}{{0, 0, "0%"}, {0, 1, "new"}, {2, 3, "+50.0%"}, {4, 1, "-75.0%"}} {
		if got := delta(c.old, c.cur); got != c.want {
			t.Errorf("delta(%v, %v) = %q, want %q", c.old, c.cur, got, c.want)
		}
	}
}

func TestRenameLabels(t *testing.T) {
	src := "start:\n\tjmp start\n\tjmp started\n.Lx: call .Lx\n1:\tjmp 1b\nfoo.start: ret\n"
	got := renameLabels(src, "_c1")
	want := "start_c1:\n\tjmp start_c1\n\tjmp started\n.Lx_c1: call .Lx_c1\n1:\tjmp 1b\nfoo.start_c1: ret\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if got := renameLabels("\tnop\n", "_x"); got != "\tnop\n" {
		t.Errorf("no labels: %q", got)
	}
}

func TestGenLarge(t *testing.T) {
	fx, err := loadFixtures(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	text := genLarge(fx, 5000)
	if n := strings.Count(text, "\n"); n < 5000 || n > 5000+600 {
		t.Errorf("lines = %d", n)
	}
	if text != genLarge(fx, 5000) {
		t.Error("not deterministic")
	}
	// Every symbolic label is defined once across the copies.
	seen := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		for name := range labelNames(line) {
			seen[name]++
		}
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("label %s defined %d times", name, n)
		}
	}
	if _, err := loadFixtures(t.TempDir()); err == nil {
		t.Error("empty fixture dir accepted")
	}
}

func TestGenWorkspace(t *testing.T) {
	fx := []fixture{{name: "a.S", text: "x:\n\tjmp x\n"}}
	dir := t.TempDir()
	if err := genWorkspace(dir, fx, 3); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, wsFileName(2)))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if got := lines[globalCallLine][globalCallCol:]; got != globalLabel {
		t.Errorf("call line %q", lines[globalCallLine])
	}
	if !strings.Contains(string(raw), "x_w2:") {
		t.Errorf("labels not renamed:\n%s", raw)
	}
	raw0, _ := os.ReadFile(filepath.Join(dir, wsFileName(0)))
	if l := strings.Split(string(raw0), "\n")[globalDefLine]; l != globalLabel+":" {
		t.Errorf("def line %q", l)
	}
	if err := genWorkspace(filepath.Join(dir, "missing"), fx, 1); err == nil {
		t.Error("write into a missing directory succeeded")
	}
}

func TestFlags(t *testing.T) {
	if s, err := parseSizes("10, 20"); err != nil || len(s) != 2 || s[1] != 20 {
		t.Errorf("sizes = %v, %v", s, err)
	}
	if _, err := parseSizes("10,x"); err == nil {
		t.Error("bad size accepted")
	}
	all, err := selectScenarios("all", false)
	if err != nil || all["tree"] || !all["typing"] {
		t.Errorf("all = %v, %v", all, err)
	}
	if all, _ := selectScenarios("all", true); !all["tree"] {
		t.Error("all with root lacks tree")
	}
	if _, err := selectScenarios("nope", false); err == nil {
		t.Error("unknown scenario accepted")
	}
	if _, err := selectScenarios("tree", false); err == nil {
		t.Error("tree without root accepted")
	}
	var buf bytes.Buffer
	for _, args := range [][]string{{"-bogus"}, {"-sizes", "x"}, {"-scenarios", "nope"}} {
		if code := run(args, &buf, &buf); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code := run([]string{"-h"}, &buf, &buf); code != 0 {
		t.Errorf("-h: exit %d", code)
	}
	if code := run([]string{"-fixtures", t.TempDir()}, &buf, &buf); code != 1 {
		t.Errorf("no fixtures: exit %d", code)
	}
	var l listFlag
	_ = l.Set("a")
	_ = l.Set("b")
	if l.String() != "a b" {
		t.Errorf("listFlag = %q", l.String())
	}
}

func TestReadVMHWM(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "status")
	if err := os.WriteFile(p, []byte("Name:\tx\nVmHWM:\t  1234 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := readVMHWM(p); err != nil || n != 1234 {
		t.Errorf("got %d, %v", n, err)
	}
	if err := os.WriteFile(p, []byte("Name:\tx\nVmHWM:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readVMHWM(p); err == nil {
		t.Error("empty VmHWM accepted")
	}
	if _, err := readVMHWM(filepath.Join(dir, "none")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestReadFrame(t *testing.T) {
	for _, in := range []string{"Content-Length: x\r\n\r\n", "Content-Length: 5\r\n\r\nab", ""} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestTreeFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := treeFiles(dir); err == nil {
		t.Error("missing database accepted")
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "compile_commands.json"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[")
	if _, err := treeFiles(dir); err == nil {
		t.Error("bad JSON accepted")
	}
	write(`[{"directory":"` + dir + `","file":"c.c"}]`)
	if _, err := treeFiles(dir); err == nil {
		t.Error("database without assembly accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.S"), []byte("\tnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(`[{"directory":"` + dir + `","file":"a.S"},{"directory":"` + dir + `","file":"` + filepath.Join(dir, "a.S") + `"},{"directory":"/","file":"/no/such.S"}]`)
	files, err := treeFiles(dir)
	if err != nil || len(files) != 1 {
		t.Errorf("files = %v, %v", files, err)
	}
}

// buildServer compiles the real server once for the end-to-end tests.
func buildServer(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the server binary")
	}
	bin := filepath.Join(t.TempDir(), "falcon-asm")
	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/falcon-asm").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestRunAll drives every synthetic scenario at a small size against the
// real server, and the tree scenario when a compiler is installed.
func TestRunAll(t *testing.T) {
	bin := buildServer(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "r.json")
	args := []string{
		"-server", bin, "-fixtures", fixtureDir, "-sizes", "2000", "-edits", "40",
		"-ws-files", "20", "-cold-runs", "2", "-rounds", "1", "-json", out, "-timeout", "30s",
	}
	if _, err := exec.LookPath("gcc"); err == nil {
		root := filepath.Join(dir, "tree")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(root, "a.S")
		if err := os.WriteFile(src, []byte("\t.code16\n\tmovw\t$1, %ax\n\tbogus %ax\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		db := `[{"directory":"` + root + `","command":"gcc -m32 -c -o a.o a.S","file":"a.S"}]`
		if err := os.WriteFile(filepath.Join(root, "compile_commands.json"), []byte(db), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-root", root, "-settle", "300ms")
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, l := range rep.Latencies {
		names[l.Name] = true
	}
	for _, want := range []string{
		"cold/spawn->initialize", "cold/spawn->semanticTokens-20k", "fixtures/hover",
		"fixtures/semanticTokens/full", "fixtures/completion", "fixtures/definition",
		"fixtures/references", "fixtures/documentSymbol", "typing-2k/completion",
		"typing-2k/burst-drain", "typing-2k/semanticTokens/full/delta", "workspace/references-first", "workspace/definition",
	} {
		if !names[want] {
			t.Errorf("report lacks %s", want)
		}
	}
	if rep.Env.CPU == "" || rep.Env.Server == "" {
		t.Errorf("env = %+v", rep.Env)
	}
}

// TestServerFailures covers a server that cannot start or dies early.
func TestServerFailures(t *testing.T) {
	var buf bytes.Buffer
	missing := filepath.Join(t.TempDir(), "none")
	if code := run([]string{"-server", missing, "-fixtures", fixtureDir, "-scenarios", "fixtures"}, &buf, &buf); code != 1 {
		t.Errorf("missing server: exit %d", code)
	}
	// A "server" that exits at once ends the stream before initialize.
	c, err := spawn("true", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.initialize("", nil); err == nil {
		t.Error("initialize against a dead server succeeded")
	}
	if _, err := c.waitPublish("x", time.Time{}, time.Second); err == nil {
		t.Error("publish wait on a dead server succeeded")
	}
	_ = c.close()

	// A server that never answers times out.
	c, err = spawn("sleep", []string{"5"}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.call("initialize", nil); err == nil {
		t.Error("call to a silent server succeeded")
	}
	if _, err := c.waitPublish("x", time.Time{}, 50*time.Millisecond); err == nil {
		t.Error("publish wait on a silent server succeeded")
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
}
