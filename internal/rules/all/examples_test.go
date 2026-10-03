package all_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/project"
	_ "git.airies.net/vifair22/falcon-asm/internal/rules/all"
	"git.airies.net/vifair22/falcon-asm/internal/symbols"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

// exampleCPU names the CPU profile an example is written against, for the
// rules that only judge code relative to a CPU target and whose example
// does not say so itself.
var exampleCPU = map[string]string{
	"lea-three-component": "skylake",
}

// Examples carry their context the way the documentation reads:
//
//	# .falcon-asm.json: {"target": {"cpu": "pentium"}}   project config
//	# link.ld: SECTIONS { ... }   or   /* boot.ld: ... */  a file beside it
//	# a.S ... # b.S ...                                    several files
var (
	configLine = regexp.MustCompile(`^\s*(?:#|/\*)\s*\.falcon-asm\.json:\s*(\{.*\})\s*(?:\*/)?\s*$`)
	sideFile   = regexp.MustCompile(`^\s*(?:#|/\*)\s*([\w.-]+\.(?:ld|lds)):\s*(.*?)\s*(?:\*/)?\s*$`)
	fileHeader = regexp.MustCompile(`^\s*#\s*([\w.-]+\.[Ss])\s*$`)
)

// exampleFiles splits an example into its files and side context.
func exampleFiles(src string) (files [][2]string, side map[string]string, cpu string) {
	side = map[string]string{}
	var name string
	var cur []string
	flush := func() {
		if name != "" || len(cur) > 0 {
			if name == "" {
				name = "example.S"
			}
			files = append(files, [2]string{name, strings.Join(cur, "\n") + "\n"})
		}
		cur = nil
	}
	for _, line := range strings.Split(src, "\n") {
		if m := configLine.FindStringSubmatch(line); m != nil {
			var cfg struct {
				Target struct {
					CPU string `json:"cpu"`
				} `json:"target"`
			}
			if json.Unmarshal([]byte(m[1]), &cfg) == nil {
				cpu = cfg.Target.CPU
			}
			continue
		}
		if m := sideFile.FindStringSubmatch(line); m != nil {
			side[m[1]] = m[2] + "\n"
			continue
		}
		if m := fileHeader.FindStringSubmatch(line); m != nil {
			flush()
			name = m[1]
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return files, side, cpu
}

// lintExample lints one documentation example the way the editor would:
// written to a file in its own directory (the workspace), with the symbol
// index over that directory, and with an assembler listing when the rule
// needs one and gcc is available.
func lintExample(t *testing.T, r *lint.Rule, src string, c bool, mode asm.Mode) map[string]bool {
	t.Helper()
	dir := t.TempDir()
	// A marker makes dir the project root, so rules that look for linker
	// scripts beside the source stop here.
	if err := os.WriteFile(filepath.Join(dir, ".falcon-asm.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var files [][2]string
	side := map[string]string{}
	cpu := exampleCPU[r.Code]
	if c {
		files = [][2]string{{"example.c", src}}
	} else {
		var exCPU string
		files, side, exCPU = exampleFiles(src)
		if exCPU != "" {
			cpu = exCPU
		}
	}
	for name, text := range side {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix := symbols.NewIndex(nil)
	ix.AddRoot(dir)
	var paths []string
	for _, f := range files {
		path := filepath.Join(dir, f[0])
		if err := os.WriteFile(path, []byte(f[1]), 0o644); err != nil {
			t.Fatal(err)
		}
		ix.Update(toolchain.PathToURI(path), f[1], nil)
		paths = append(paths, path)
	}
	tables, _ := lint.LoadTables(nil)
	got := map[string]bool{}
	for i, path := range paths {
		uri := toolchain.PathToURI(path)
		d := project.Doc{
			Doc:       &docstore.Document{URI: uri, Text: files[i][1]},
			C:         c,
			Target:    lint.Target{Mode: mode, CPU: cpu, Sources: map[string]string{"mode": "example"}},
			Tables:    tables,
			Workspace: ix.LintView(uri),
		}
		// A rule that is off by default documents what it reports once a
		// project turns it on, so its examples run with it on.
		if r.Default == lint.Off {
			d.Config = lint.FileConfig{Rules: map[string]lint.Severity{r.Code: lint.Info}}
		}
		if r.NeedsListing && !c {
			d.Listing = assemble(t, path, mode)
		}
		for _, f := range project.Lint(d).Findings {
			got[f.Code] = true
		}
	}
	return got
}

// assemble produces the listing a listing rule needs; nil (and the rule
// quiet) when gcc is not installed.
func assemble(t *testing.T, path string, mode asm.Mode) *toolchain.AsmResult {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		return nil
	}
	flag := map[asm.Mode]string{16: "-m16", 32: "-m32", 64: "-m64"}[mode]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := (&toolchain.Runner{}).Assemble(ctx, path, toolchain.Flags{
		Compiler: "gcc", Args: []string{flag}, Dir: filepath.Dir(path), File: path, Exact: true, Mode: int(mode),
	})
	if err != nil {
		t.Logf("assemble %s: %v", path, err)
		return nil
	}
	return res
}

// Every rule's documented bad example must trigger it and its good example
// must not, in at least one processor mode. This keeps docs/RULES.md honest
// and gives each rule a fixture that reads like the documentation.
func TestRuleExamples(t *testing.T) {
	for _, r := range lint.Rules() {
		r := r
		if r.NeedsListing {
			if _, err := exec.LookPath("gcc"); err != nil {
				t.Logf("skipping %s: needs gcc for a listing", r.Code)
				continue
			}
		}
		t.Run(r.Code, func(t *testing.T) {
			c := r.Langs&lint.LangAsm == 0
			ok := false
			for _, m := range []asm.Mode{64, 32, 16} {
				if lintExample(t, r, r.Doc.Bad, c, m)[r.Code] && !lintExample(t, r, r.Doc.Good, c, m)[r.Code] {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("bad example does not trigger %s (or good example does) in any mode\nbad:\n%s\ngood:\n%s",
					r.Code, strings.TrimSpace(r.Doc.Bad), strings.TrimSpace(r.Doc.Good))
			}
		})
	}
}
