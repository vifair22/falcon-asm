// Command lspbench measures falcon-asm the way an editor uses it: it starts
// the real server binary, speaks LSP over its stdio, replays scripted
// sessions and reports per-request latency percentiles, cold start times
// and peak resident memory. See docs/BENCHMARKS.md.
//
// Usage:
//
//	lspbench [-server build/bin/falcon-asm] [-scenarios all] [-json out.json]
//	lspbench -compare old.json new.json
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// scenarioNames in run order. "tree" runs only with -root; "cli" lints
// the root only with -root, a generated workspace always.
var scenarioNames = []string{"cold", "fixtures", "features", "diagnostics", "typing", "workspace", "tree", "cli"}

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, " ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// run is main with its environment passed in, for tests.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lspbench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config{log: stderr}
	var serverArgs listFlag
	fs.StringVar(&cfg.bin, "server", "build/bin/falcon-asm", "server binary to benchmark")
	fs.Var(&serverArgs, "server-arg", "extra server argument, repeatable; {scenario} expands to the scenario name")
	fs.StringVar(&cfg.fixtures, "fixtures", "testdata/falcon-bios", "directory of *.S fixtures")
	fs.StringVar(&cfg.root, "root", "", "a real tree with compile_commands.json for the tree scenario (read only)")
	scenarios := fs.String("scenarios", "all", "comma-separated: "+strings.Join(scenarioNames, ",")+", or all")
	sizes := fs.String("sizes", "20000,100000", "typing scenario file sizes in lines")
	fs.IntVar(&cfg.edits, "edits", 500, "single-character edits per typing phase")
	fs.IntVar(&cfg.wsFiles, "ws-files", 500, "files in the generated workspace")
	fs.IntVar(&cfg.coldRuns, "cold-runs", 10, "server starts for the cold scenario")
	fs.IntVar(&cfg.rounds, "rounds", 3, "repetitions of the per-file request sweeps")
	fs.DurationVar(&cfg.timeout, "timeout", 2*time.Minute, "longest wait for any one reply")
	fs.DurationVar(&cfg.settle, "settle", 3*time.Second, "quiet period after opening the tree before saves")
	fs.IntVar(&cfg.diagEdits, "diag-edits", 5, "edits per file (fixtures) and per phase (large files) in the diagnostics scenario")
	fs.DurationVar(&cfg.debounce, "debounce", 150*time.Millisecond, "the server's diagnostics debounce, subtracted in edit->publish-minus-debounce")
	jsonOut := fs.String("json", "", "also write the report as JSON to this file")
	doCompare := fs.Bool("compare", false, "compare two JSON reports given as arguments and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *doCompare {
		return runCompare(fs.Args(), stdout, stderr)
	}
	cfg.serverArgs = serverArgs
	var err error
	if cfg.sizes, err = parseSizes(*sizes); err != nil {
		fmt.Fprintf(stderr, "lspbench: %v\n", err)
		return 2
	}
	want, err := selectScenarios(*scenarios, cfg.root != "")
	if err != nil {
		fmt.Fprintf(stderr, "lspbench: %v\n", err)
		return 2
	}
	rep, err := benchmark(cfg, want)
	if err != nil {
		fmt.Fprintf(stderr, "lspbench: %v\n", err)
		return 1
	}
	if err := writeTable(stdout, rep); err != nil {
		fmt.Fprintf(stderr, "lspbench: %v\n", err)
		return 1
	}
	if *jsonOut != "" {
		if err := writeJSON(*jsonOut, rep); err != nil {
			fmt.Fprintf(stderr, "lspbench: %v\n", err)
			return 1
		}
	}
	return 0
}

func runCompare(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "lspbench: -compare takes two report files: old.json new.json")
		return 2
	}
	old, err := readReport(args[0])
	if err == nil {
		var cur Report
		if cur, err = readReport(args[1]); err == nil {
			err = compare(stdout, old, cur)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "lspbench: %v\n", err)
		return 1
	}
	return 0
}

func parseSizes(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bad -sizes entry %q", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// selectScenarios resolves the -scenarios flag. "all" includes the tree
// scenario only when a root was given, since it needs a real project.
func selectScenarios(s string, haveRoot bool) (map[string]bool, error) {
	out := map[string]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "all" {
			for _, n := range scenarioNames {
				out[n] = n != "tree" || haveRoot
			}
			continue
		}
		known := false
		for _, n := range scenarioNames {
			known = known || n == f
		}
		if !known {
			return nil, fmt.Errorf("unknown scenario %q", f)
		}
		if f == "tree" && !haveRoot {
			return nil, errors.New("the tree scenario needs -root")
		}
		out[f] = true
	}
	return out, nil
}

// benchmark runs the selected scenarios in a fixed order.
func benchmark(cfg config, want map[string]bool) (Report, error) {
	fx, err := loadFixtures(cfg.fixtures)
	if err != nil {
		return Report{}, err
	}
	tmp, err := os.MkdirTemp("", "lspbench-")
	if err != nil {
		return Report{}, fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	b := &bench{cfg: cfg, fx: fx, rec: newRecorder(), tmp: tmp}
	steps := map[string]func() error{
		"cold":        b.coldScenario,
		"fixtures":    b.fixturesScenario,
		"features":    b.featuresScenario,
		"diagnostics": b.diagnosticsScenario,
		"workspace":   b.workspaceScenario,
		"tree":        b.treeScenario,
		"cli":         b.cliScenario,
		"typing": func() error {
			for _, n := range cfg.sizes {
				if err := b.typingScenario(n); err != nil {
					return err
				}
			}
			return nil
		},
	}
	for _, name := range scenarioNames {
		if !want[name] {
			continue
		}
		fmt.Fprintf(cfg.log, "lspbench: %s\n", name)
		if err := steps[name](); err != nil {
			return Report{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	return b.rec.report(environment(cfg.bin)), nil
}

// environment describes the machine and build the numbers belong to.
func environment(bin string) Env {
	env := Env{GoVersion: runtime.Version(), Date: time.Now().UTC().Format(time.RFC3339)}
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
				env.CPU = strings.TrimSpace(v)
				break
			}
		}
	}
	if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		env.Kernel = strings.TrimSpace(string(raw))
	}
	if out, err := exec.Command(bin, "--version").Output(); err == nil {
		env.Server = strings.TrimSpace(string(out))
	}
	return env
}
