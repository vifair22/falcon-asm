package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlob(t *testing.T) {
	tests := []struct {
		pat  string
		path string
		want bool
	}{
		{"*.S", "boot.S", true},
		{"*.S", "src/boot/mbr.S", true},
		{"*.S", "src/boot/mbr.s", false},
		{"src/*.S", "src/a.S", true},
		{"src/*.S", "src/x/a.S", false},
		{"src/**/*.S", "src/a.S", true},
		{"src/**/*.S", "src/x/y/a.S", true},
		{"src/**", "src/x/y/a.S", true},
		{"**/legacy/*.S", "a/b/legacy/x.S", true},
		{"legacy", "legacy/boot/mbr.S", true},
		{"legacy/", "legacy/boot/mbr.S", true},
		{"legacy", "src/legacy/mbr.S", true},
		{"/legacy", "src/legacy/mbr.S", false},
		{"./src/a.S", "src/a.S", true},
		{"src/a.S", "src/a.S", true},
		{"src/a.S", "other/src/a.S", false},
		{"/", "anything/at/all.S", true},
		{"s?c/[ab].S", "src/b.S", true},
		{"*.S", "../outside.S", false},
		{"*.S", "", false},
		{"**/**/x.S", "x.S", true},
	}
	for _, tt := range tests {
		g, err := CompileGlob(tt.pat)
		if err != nil {
			t.Fatalf("CompileGlob(%q): %v", tt.pat, err)
		}
		if got := g.Match(tt.path); got != tt.want {
			t.Errorf("%q matching %q = %v", tt.pat, tt.path, got)
		}
		if g.String() != tt.pat {
			t.Errorf("String = %q", g.String())
		}
	}
	for _, bad := range []string{"", "  ", "a//b", "[x"} {
		if _, err := CompileGlob(bad); err == nil {
			t.Errorf("CompileGlob(%q) accepted", bad)
		}
	}
	if (Glob{}).Match("a") {
		t.Error("zero Glob matched")
	}
}

const fullConfig = `{
  "$schema": "ignored",
  "rules": { "x-reg": "error", "x-lvl": "off" },
  "overrides": [
    { "paths": ["legacy/**", "*.inc"], "rules": { "x-reg": "hint" }, "target": { "mode": 16, "cc": "custom" } },
    { "paths": ["legacy/mbr.S"], "target": { "level": "none", "cpu": "I386" } }
  ],
  "target": { "cpu": "pentium", "level": "v3", "cc": "sysv" },
  "tables": { "ports": [ { "port": 146, "name": "A20" } ] }
}`

func TestParseConfig(t *testing.T) {
	dir := t.TempDir()
	c := ParseConfig(filepath.Join(dir, ConfigFile), []byte(fullConfig))
	if len(c.Problems) != 0 {
		t.Fatalf("problems: %+v", c.Problems)
	}
	if c.Rules["x-reg"] != Error || c.Rules["x-lvl"] != Off || len(c.Overrides) != 2 {
		t.Fatalf("config = %+v", c)
	}
	if c.Target != (TargetConfig{CPU: "pentium", Level: "v3", CC: "sysv"}) {
		t.Fatalf("target = %+v", c.Target)
	}
	if !strings.Contains(string(c.Tables), `"A20"`) {
		t.Fatalf("tables = %s", c.Tables)
	}

	top := c.For(filepath.Join(dir, "src", "a.S"))
	if top.Rules["x-reg"] != Error || top.Target.Level != "v3" || top.Target.Mode != 0 {
		t.Fatalf("top-level file = %+v", top)
	}
	legacy := c.For(filepath.Join(dir, "legacy", "mbr.S"))
	want := TargetConfig{CPU: "i386", Level: "none", CC: "custom", Mode: 16}
	if legacy.Rules["x-reg"] != Hint || legacy.Rules["x-lvl"] != Off || legacy.Target != want {
		t.Fatalf("legacy file = %+v", legacy)
	}
	// The override must not leak into the configuration's own map.
	if c.Rules["x-reg"] != Error {
		t.Fatal("override mutated the top-level rules")
	}
	if inc := c.For(filepath.Join(dir, "x", "defs.inc")); inc.Target.Mode != 16 {
		t.Fatalf("*.inc = %+v", inc)
	}
	if out := c.For(filepath.Join(filepath.Dir(dir), "elsewhere.S")); out.Rules["x-reg"] != Error || out.Target.Mode != 0 {
		t.Fatalf("file outside the config dir = %+v", out)
	}
	// Relative paths resolve against the working directory.
	rel := ParseConfig("", []byte(`{"overrides":[{"paths":["a/*.S"],"rules":{"x-lvl":"error"}}]}`))
	if rel.For("a/x.S").Rules["x-lvl"] != Error {
		t.Fatal("relative config dir")
	}

	var nilCfg *Config
	if fc := nilCfg.For("/x.S"); fc.Rules != nil || fc.SeverityOf(testRule("x-y")) != Warning {
		t.Fatalf("nil config = %+v", fc)
	}
	if fc := (FileConfig{Rules: map[string]Severity{"x-y": Off}}); fc.SeverityOf(testRule("x-y")) != Off {
		t.Fatal("configured severity ignored")
	}
}

func TestConfigProblems(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		sev   Severity
		msg   string
		line  int
		fatal bool
	}{
		{"syntax", "{\n  \"rules\": {,}\n}", Error, "invalid JSON", 1, true},
		{"truncated", "{\"rules\": {", Error, "invalid JSON", 0, true},
		{"trailing", "{} {}", Error, "invalid JSON", 0, true},
		{"not object", "[1, 2]", Error, "must be a JSON object", 0, true},
		{"unknown key", "{\n\"rulez\": {}}", Warning, `unknown key "rulez"`, 1, false},
		{"unknown code", "{\"rules\": {\n  \"no-such-rule\": \"off\"}}", Warning, `unknown rule code "no-such-rule"`, 1, false},
		{"bad severity", "{\"rules\": {\"x-lvl\": \"loud\"}}", Error, `invalid severity "loud"`, 0, true},
		{"severity type", "{\"rules\": {\"x-lvl\": 3}}", Error, "must be a string", 0, true},
		{"severity object", "{\"rules\": {\"x-lvl\": {}}}", Error, "must be a string", 0, true},
		{"rules type", "{\"rules\": []}", Error, "rules must be an object", 0, true},
		{"rules scalar", "{\"rules\": 5}", Error, "rules must be an object", 0, true},
		{"overrides type", "{\"overrides\": {}}", Error, "overrides must be an array", 0, true},
		{"no paths", "{\"overrides\": [{\"rules\": {}}]}", Warning, "no \"paths\"", 0, false},
		{"bad glob", "{\"overrides\": [{\"paths\": [\"[x\"]}]}", Error, "syntax error in pattern", 0, true},
		{"paths type", "{\"overrides\": [{\"paths\": \"*.S\"}]}", Error, "paths must be an array", 0, true},
		{"override key", "{\"overrides\": [{\"paths\": [], \"when\": 1}]}", Warning, `unknown override key "when"`, 0, false},
		{"bad level", "{\"target\": {\"level\": \"v9\"}}", Error, `invalid level "v9"`, 0, true},
		{"bad mode", "{\"target\": {\"mode\": 8}}", Error, "mode must be 16, 32 or 64", 0, true},
		{"mode type", "{\"target\": {\"mode\": [64]}}", Error, "mode must be 16, 32 or 64", 0, true},
		{"cc", "{\"target\": {\"cc\": \"pascal\"}}", Warning, `unknown calling convention "pascal"`, 0, false},
		{"target key", "{\"target\": {\"arch\": \"x\"}}", Warning, `unknown target key "arch"`, 0, false},
		{"cpu type", "{\"target\": {\"cpu\": 386}}", Error, "target.cpu must be a string", 0, true},
		{"tables bad", "{\"tables\": [}", Error, "invalid JSON", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ParseConfig("cfg.json", []byte(tt.text))
			if len(c.Problems) == 0 {
				t.Fatal("no problem reported")
			}
			p := c.Problems[0]
			if p.Severity != tt.sev || !strings.Contains(p.Message, tt.msg) || p.Range.Start.Line != tt.line {
				t.Fatalf("problem = %+v", c.Problems)
			}
			if c.Fatal() != tt.fatal {
				t.Fatalf("Fatal = %v", c.Fatal())
			}
		})
	}
	// The unknown code's range covers the key itself.
	c := ParseConfig("", []byte(`{"rules": {"nope": "off", "x-lvl": "error"}}`))
	if r := c.Problems[0].Range; r.Start.Character != 11 || r.End.Character != 17 || c.Rules["x-lvl"] != Error {
		t.Fatalf("range = %+v, rules %+v", r, c.Rules)
	}
	// Escaped keys still locate their opening quote.
	c = ParseConfig("", []byte(`{"ru\u006ces": {}, "x\"y": 1}`))
	if len(c.Problems) != 1 || c.Problems[0].Range.Start.Character != 19 {
		t.Fatalf("escaped key problem = %+v", c.Problems)
	}
	if c := ParseConfig("", []byte("  \n")); len(c.Problems) != 0 || c.Rules != nil {
		t.Fatalf("empty file = %+v", c)
	}
	if c := ParseConfig("", []byte(`{"target":{"mode":32,"level":"x86-64"}}`)); c.Target.Mode != 32 || c.Target.Level != "x86-64" || len(c.Problems) != 0 {
		t.Fatalf("mode 32 = %+v", c)
	}
	if c := ParseConfig("", []byte(`{"target":{"mode":true, "x": null, "y": false}}`)); len(c.Problems) != 3 {
		t.Fatalf("scalar kinds = %+v", c.Problems)
	}
}

func TestLoadAndFindConfig(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindConfig(sub); ok {
		// A config above the temp dir would make this test meaningless.
		t.Skip("a .falcon-asm.json exists above the temp dir")
	}
	p := filepath.Join(root, "a", ConfigFile)
	if err := os.WriteFile(p, []byte(`{"rules":{"x-lvl":"error"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := FindConfig(sub)
	if !ok || got != p {
		t.Fatalf("FindConfig = %q %v", got, ok)
	}
	c, err := LoadConfig(got)
	if err != nil || c.Rules["x-lvl"] != Error || c.Dir != filepath.Join(root, "a") {
		t.Fatalf("LoadConfig = %+v %v", c, err)
	}
	if _, err := LoadConfig(filepath.Join(root, "missing.json")); err == nil {
		t.Fatal("missing file loaded")
	}
	// A directory with the config's name is not a config.
	if err := os.Mkdir(filepath.Join(sub, ConfigFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := FindConfig(sub); got != p {
		t.Fatalf("directory taken for a config: %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]int{"v1": 1, "V4": 4, "x86-64-v2": 2, "x86-64": 1, "none": 0} {
		if got, ok := ParseLevel(in); !ok || got != want {
			t.Errorf("ParseLevel(%q) = %d %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "v5", "v", "haswell"} {
		if _, ok := ParseLevel(bad); ok {
			t.Errorf("ParseLevel(%q) accepted", bad)
		}
	}
}
