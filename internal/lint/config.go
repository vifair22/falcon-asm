package lint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/callconv"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// ConfigFile is the per-project configuration file name.
const ConfigFile = ".falcon-asm.json"

// Config is a parsed .falcon-asm.json:
//
//	{
//	  "rules":     { "<code>": "off" | "hint" | "info" | "warning" | "error" },
//	  "overrides": [ { "paths": ["glob", ...], "rules": {...}, "target": {...} } ],
//	  "target":    { "cpu": "", "level": "v3", "cc": "sysv", "mode": 16 },
//	  "tables":    { "ports": [...], "msrs": [...], "bios": [...], "helpers": [...] }
//	}
//
// Overrides apply in order after the top level, so a later block wins.
// "tables" is kept raw for internal/hwdb, which owns its schema.
type Config struct {
	Path      string // the file it came from; "" for the empty configuration
	Dir       string // override globs are relative to this directory
	Rules     map[string]Severity
	Overrides []Override
	Target    TargetConfig
	Tables    json.RawMessage
	// Problems found while parsing, positioned in the configuration file.
	// Error problems mean the file (or a part of it) was not used.
	Problems []Problem
}

// Override is one entry of "overrides".
type Override struct {
	Paths  []Glob
	Rules  map[string]Severity
	Target TargetConfig
}

// TargetConfig is a "target" object. Empty fields leave the value to the
// next source (compile flags, settings). Level is kept as written and
// checked at load; Mode is 16, 32, 64 or 0 for unset.
type TargetConfig struct {
	CPU   string
	Level string
	CC    string
	Mode  int
}

// merge lays o over t, field by field.
func (t TargetConfig) merge(o TargetConfig) TargetConfig {
	if o.CPU != "" {
		t.CPU = o.CPU
	}
	if o.Level != "" {
		t.Level = o.Level
	}
	if o.CC != "" {
		t.CC = o.CC
	}
	if o.Mode != 0 {
		t.Mode = o.Mode
	}
	return t
}

// FileConfig is the configuration resolved for one file.
type FileConfig struct {
	Rules  map[string]Severity // configured severities; absent = rule default
	Target TargetConfig
	Tables json.RawMessage
}

// SeverityOf is the severity r runs at for this file.
func (f FileConfig) SeverityOf(r *Rule) Severity {
	if s, ok := f.Rules[r.Code]; ok {
		return s
	}
	return r.Default
}

// Problem is a configuration diagnostic.
type Problem struct {
	Range    protocol.Range
	Severity Severity // Error or Warning
	Message  string
}

// Fatal reports whether any problem is an error.
func (c *Config) Fatal() bool {
	for _, p := range c.Problems {
		if p.Severity == Error {
			return true
		}
	}
	return false
}

// For resolves the configuration of the file at path (absolute or
// relative to the working directory). A nil Config resolves to defaults.
func (c *Config) For(path string) FileConfig {
	if c == nil {
		return FileConfig{}
	}
	fc := FileConfig{Target: c.Target, Tables: c.Tables}
	rel := ""
	if len(c.Overrides) > 0 {
		rel = c.relPath(path)
	}
	var matched []*Override
	for i := range c.Overrides {
		o := &c.Overrides[i]
		for _, g := range o.Paths {
			if rel != "" && g.Match(rel) {
				matched = append(matched, o)
				break
			}
		}
	}
	if len(matched) == 0 {
		fc.Rules = c.Rules
		return fc
	}
	fc.Rules = make(map[string]Severity, len(c.Rules))
	for k, v := range c.Rules {
		fc.Rules[k] = v
	}
	for _, o := range matched {
		for k, v := range o.Rules {
			fc.Rules[k] = v
		}
		fc.Target = fc.Target.merge(o.Target)
	}
	return fc
}

// relPath is path relative to the configuration's directory, slash
// separated; "" when it lies outside.
func (c *Config) relPath(p string) string {
	if c.Dir == "" {
		return filepath.ToSlash(filepath.Clean(p))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(c.Dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// LoadConfig reads and parses a configuration file. The error is for the
// read only; content problems are in Config.Problems.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseConfig(path, data), nil
}

// FindConfig walks up from dir to the nearest directory holding a
// configuration file and returns its path.
func FindConfig(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		p := filepath.Join(dir, ConfigFile)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// ParseConfig parses configuration text. It never fails: a syntax error is
// an Error problem and leaves the defaults, and an unknown key or rule code
// is a Warning, so a typo in one entry never costs the rest of the file.
func ParseConfig(path string, data []byte) *Config {
	c := &Config{Path: path}
	if path != "" {
		if abs, err := filepath.Abs(path); err == nil {
			c.Dir = filepath.Dir(abs)
		}
	}
	p := &configParser{c: c, doc: &docstore.Document{Text: string(data)}, dec: json.NewDecoder(bytes.NewReader(data))}
	p.dec.UseNumber()
	if len(bytes.TrimSpace(data)) == 0 {
		return c
	}
	if err := p.top(); err != nil {
		var se *json.SyntaxError
		off := int(p.dec.InputOffset())
		if errors.As(err, &se) {
			off = int(se.Offset)
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			off = len(data)
		}
		// A file that does not parse is not used at all: half a
		// configuration applied would be harder to reason about than
		// none.
		*c = Config{Path: c.Path, Dir: c.Dir}
		p.problemAt(off, off, Error, "invalid JSON: %v", err)
		c.Problems = append(c.Problems, p.problems...)
		return c
	}
	c.Problems = p.problems
	return c
}

type configParser struct {
	c        *Config
	doc      *docstore.Document
	dec      *json.Decoder
	problems []Problem
}

func (p *configParser) problemAt(start, end int, sev Severity, format string, args ...any) {
	start = max(0, min(start, len(p.doc.Text)))
	end = max(start, min(end, len(p.doc.Text)))
	p.problems = append(p.problems, Problem{
		Range:    protocol.Range{Start: p.doc.Position(start), End: p.doc.Position(end)},
		Severity: sev,
		Message:  fmt.Sprintf(format, args...),
	})
}

// span is a token's byte range: the decoder reports the offset after the
// token, and the start is found by scanning back over its text.
type span struct{ start, end int }

func (p *configParser) token() (json.Token, span, error) {
	t, err := p.dec.Token()
	end := int(p.dec.InputOffset())
	if err != nil {
		return nil, span{end, end}, err
	}
	start := end - 1
	switch v := t.(type) {
	case string:
		// Walk back to the opening quote: escapes make the source longer
		// than the decoded string.
		start = end - 2
		for start > 0 && !(p.doc.Text[start] == '"' && p.doc.Text[start-1] != '\\') {
			start--
		}
	case json.Number:
		start = end - len(v.String())
	case bool:
		start = end - len(fmt.Sprint(v))
	case nil:
		start = end - len("null")
	}
	return t, span{max(0, start), end}, nil
}

// skip consumes one value, returning its span. Objects and arrays are
// skipped through their closing delimiter.
func (p *configParser) skip() (span, error) {
	t, sp, err := p.token()
	if err != nil {
		return sp, err
	}
	d, ok := t.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return sp, nil
	}
	depth := 1
	for depth > 0 {
		t, end, err := p.token()
		if err != nil {
			return sp, err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
		sp.end = end.end
	}
	return sp, nil
}

// object walks an object, calling field for each key. The opening brace
// must be next; if something else is, it is reported and skipped.
func (p *configParser) object(what string, field func(key string, ks span) error) error {
	t, sp, err := p.token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		end := sp.end
		if ok && d == '[' {
			if err := p.finishSkip(); err != nil {
				return err
			}
			end = int(p.dec.InputOffset())
		}
		p.problemAt(sp.start, end, Error, "%s must be an object", what)
		return nil
	}
	for p.dec.More() {
		kt, ks, err := p.token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		if err := field(key, ks); err != nil {
			return err
		}
	}
	_, _, err = p.token() // '}'
	return err
}

// finishSkip consumes the rest of an array or object whose opening
// delimiter was just read.
func (p *configParser) finishSkip() error {
	depth := 1
	for depth > 0 {
		t, _, err := p.token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// array walks an array, calling elem for each element.
func (p *configParser) array(what string, elem func() error) error {
	if !p.peekDelim('[') {
		sp, err := p.skip()
		if err != nil {
			return err
		}
		p.problemAt(sp.start, sp.end, Error, "%s must be an array", what)
		return nil
	}
	if _, _, err := p.token(); err != nil {
		return err
	}
	for p.dec.More() {
		if err := elem(); err != nil {
			return err
		}
	}
	_, _, err := p.token() // ']'
	return err
}

// peekDelim reports whether the next non-space byte is d.
func (p *configParser) peekDelim(d byte) bool {
	off := int(p.dec.InputOffset())
	for off < len(p.doc.Text) {
		switch c := p.doc.Text[off]; c {
		case ' ', '\t', '\r', '\n', ':', ',':
			off++
		default:
			return c == d
		}
	}
	return false
}

// str reads a string value, reporting anything else.
func (p *configParser) str(what string) (string, span, bool, error) {
	if p.peekDelim('{') || p.peekDelim('[') {
		sp, err := p.skip()
		if err == nil {
			p.problemAt(sp.start, sp.end, Error, "%s must be a string", what)
		}
		return "", sp, false, err
	}
	t, sp, err := p.token()
	if err != nil {
		return "", sp, false, err
	}
	s, ok := t.(string)
	if !ok {
		p.problemAt(sp.start, sp.end, Error, "%s must be a string", what)
	}
	return s, sp, ok, nil
}

func (p *configParser) top() error {
	if !p.peekDelim('{') {
		sp, err := p.skip()
		if err != nil {
			return err
		}
		p.problemAt(sp.start, sp.end, Error, "the configuration must be a JSON object")
		return nil
	}
	err := p.object("configuration", func(key string, ks span) error {
		switch key {
		case "rules":
			m, err := p.rules("rules")
			p.c.Rules = m
			return err
		case "overrides":
			return p.array("overrides", p.override)
		case "target":
			t, err := p.target("target")
			p.c.Target = t
			return err
		case "tables":
			var raw json.RawMessage
			if err := p.dec.Decode(&raw); err != nil {
				return err
			}
			p.c.Tables = raw
			p.checkTables(raw, int(p.dec.InputOffset())-len(raw), ks)
			return nil
		case "$schema":
			_, err := p.skip()
			return err
		}
		p.problemAt(ks.start, ks.end, Warning, "unknown key %q (known: rules, overrides, target, tables)", key)
		_, err := p.skip()
		return err
	})
	if err != nil {
		return err
	}
	// Anything after the object is a syntax error the decoder only sees
	// when asked for another token.
	if _, err := p.dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("unexpected content after the configuration object")
		}
		return err
	}
	return nil
}

func (p *configParser) rules(what string) (map[string]Severity, error) {
	m := map[string]Severity{}
	err := p.object(what, func(code string, ks span) error {
		v, vs, ok, err := p.str(fmt.Sprintf("the severity of %q", code))
		if err != nil || !ok {
			return err
		}
		sev, valid := ParseSeverity(v)
		if !valid {
			p.problemAt(vs.start, vs.end, Error, "invalid severity %q for %s (want off, hint, info, warning or error)", v, code)
			return nil
		}
		if _, known := Lookup(code); !known {
			p.problemAt(ks.start, ks.end, Warning, "unknown rule code %q", code)
			return nil
		}
		m[code] = sev
		return nil
	})
	return m, err
}

func (p *configParser) override() error {
	var o Override
	seenPaths := false
	start := int(p.dec.InputOffset())
	err := p.object("an override", func(key string, ks span) error {
		switch key {
		case "paths":
			seenPaths = true
			return p.array("paths", func() error {
				s, sp, ok, err := p.str("a path pattern")
				if err != nil || !ok {
					return err
				}
				g, gerr := CompileGlob(s)
				if gerr != nil {
					p.problemAt(sp.start, sp.end, Error, "%v", gerr)
					return nil
				}
				o.Paths = append(o.Paths, g)
				return nil
			})
		case "rules":
			m, err := p.rules("rules")
			o.Rules = m
			return err
		case "target":
			t, err := p.target("target")
			o.Target = t
			return err
		}
		p.problemAt(ks.start, ks.end, Warning, "unknown override key %q (known: paths, rules, target)", key)
		_, err := p.skip()
		return err
	})
	if err != nil {
		return err
	}
	if !seenPaths {
		p.problemAt(start, int(p.dec.InputOffset()), Warning, "override has no \"paths\" and applies to nothing")
	}
	p.c.Overrides = append(p.c.Overrides, o)
	return nil
}

func (p *configParser) target(what string) (TargetConfig, error) {
	var t TargetConfig
	err := p.object(what, func(key string, ks span) error {
		switch key {
		case "cpu", "level", "cc":
			v, vs, ok, err := p.str(what + "." + key)
			if err != nil || !ok {
				return err
			}
			v = strings.ToLower(strings.TrimSpace(v))
			switch key {
			case "cpu":
				t.CPU = v
			case "level":
				if _, valid := ParseLevel(v); !valid && v != "" {
					p.problemAt(vs.start, vs.end, Error, "invalid level %q (want v1..v4, x86-64-vN, x86-64 or none)", v)
					return nil
				}
				t.Level = v
			case "cc":
				// The convention model owns the spellings, regparm(N)
				// included.
				if v != "" && !callconv.Valid(v) {
					p.problemAt(vs.start, vs.end, Warning, "unknown calling convention %q (known: %s)", v, callconv.Names)
				}
				t.CC = v
			}
			return nil
		case "mode":
			if p.peekDelim('{') || p.peekDelim('[') {
				vs, err := p.skip()
				if err == nil {
					p.problemAt(vs.start, vs.end, Error, "%s.mode must be 16, 32 or 64", what)
				}
				return err
			}
			t2, vs, err := p.token()
			if err != nil {
				return err
			}
			n, _ := t2.(json.Number)
			switch n.String() {
			case "16", "32", "64":
				t.Mode = int(n[0]-'0')*10 + int(n[1]-'0')
			default:
				p.problemAt(vs.start, vs.end, Error, "%s.mode must be 16, 32 or 64", what)
			}
			return nil
		}
		p.problemAt(ks.start, ks.end, Warning, "unknown target key %q (known: cpu, level, cc, mode)", key)
		_, err := p.skip()
		return err
	})
	return t, err
}

// ParseLevel reads an x86-64 level as configuration and comments spell it:
// "v1".."v4", "x86-64-vN", "x86-64" (v1) or "none" (0, an explicit "no
// target"). ok is false for anything else.
func ParseLevel(v string) (int, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "none":
		return 0, true
	case "x86-64":
		return 1, true
	}
	v = strings.TrimPrefix(v, "x86-64-")
	if len(v) == 2 && v[0] == 'v' && v[1] >= '1' && v[1] <= '4' {
		return int(v[1] - '0'), true
	}
	return 0, false
}
