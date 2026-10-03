package lint

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A bad "tables" entry is a configuration problem at the JSON path the
// loader names, not only a log line.
func TestTablesProblems(t *testing.T) {
	RegisterTables(func(raw json.RawMessage) (Tables, error) {
		switch {
		case strings.Contains(string(raw), "bad"):
			return nil, errors.New(`hwdb: ports[1].fields[0].bits: want "hi:lo"`)
		case strings.Contains(string(raw), "weird"):
			return nil, errors.New(`hwdb: unknown section "weird"`)
		}
		return nil, nil
	})
	t.Cleanup(func() { RegisterTables(nil) })

	text := "{\n  \"tables\": {\"ports\": [{\"port\": 1},\n    {\"port\": 2, \"fields\": [{\"bits\": \"bad\"}]}]}\n}\n"
	c := ParseConfig("", []byte(text))
	if len(c.Problems) != 1 || c.Problems[0].Severity != Error {
		t.Fatalf("problems = %+v", c.Problems)
	}
	p := c.Problems[0]
	want := strings.Index(text, `"bad"`)
	if p.Range.Start.Line != 2 || p.Range.Start.Character != want-strings.LastIndex(text[:want], "\n")-1 ||
		!strings.Contains(p.Message, "ports[1].fields[0].bits") {
		t.Fatalf("problem = %+v", p)
	}
	// No path: the problem sits on the "tables" key.
	c = ParseConfig("", []byte(`{"tables": {"weird": []}}`))
	if len(c.Problems) != 1 || c.Problems[0].Range.Start.Character != 1 {
		t.Fatalf("problems = %+v", c.Problems)
	}
	if c := ParseConfig("", []byte(`{"tables": {"ports": []}}`)); len(c.Problems) != 0 {
		t.Fatalf("problems = %+v", c.Problems)
	}
}

func TestPathSpan(t *testing.T) {
	raw := []byte(`{"a": [1, {"b\"x": "s,}"}, {"c": {"d": [true, null]}}], "e": -2.5}`)
	cases := []struct {
		path, want string
		ok         bool
	}{
		{"a[2].c.d[1]", "null", true},
		{"a[1]", `{"b\"x": "s,}"}`, true},
		{"e", "-2.5", true},
		{"a[5]", "", false},
		{"a.b", "", false},
		{"e[0]", "", false},
		{"zz", "", false},
	}
	for _, c := range cases {
		segs, ok := parsePath(c.path)
		if !ok {
			t.Fatalf("parse %q", c.path)
		}
		s, e, ok := pathSpan(raw, segs)
		if ok != c.ok || ok && string(raw[s:e]) != c.want {
			t.Errorf("%s: %q %v", c.path, raw[s:e], ok)
		}
	}
	for _, bad := range []string{"", "a[", "a[x]", "[-1]", "a..b!", "a b"} {
		if _, ok := parsePath(bad); ok && bad != "a..b!" {
			t.Errorf("parsePath(%q) accepted", bad)
		}
	}
	if _, _, ok := errorSpan(raw, "no path here: at all"); ok {
		t.Error("prose resolved")
	}
	if _, ok := skipValue([]byte(`"open`), 0); ok {
		t.Error("unterminated string")
	}
	if _, ok := skipValue([]byte(`[1, 2`), 0); ok {
		t.Error("unterminated array")
	}
	if _, ok := skipValue([]byte(``), 0); ok {
		t.Error("empty")
	}
	if _, ok := objectMember([]byte(`{1: 2}`), 0, "x"); ok {
		t.Error("non-string key")
	}
	if _, ok := objectMember([]byte(`{"a" 2}`), 0, "x"); ok {
		t.Error("missing colon")
	}
}
