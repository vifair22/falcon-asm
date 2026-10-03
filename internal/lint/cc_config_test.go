package lint

import "testing"

// TestConfigCCSpellings: every spelling the convention model accepts is
// a valid target.cc, regparm(N) included.
func TestConfigCCSpellings(t *testing.T) {
	for _, cc := range []string{"sysv", "ms", "cdecl", "stdcall", "fastcall", "regparm(2)", "regparm", "custom", "none", "win64"} {
		c := ParseConfig("cfg.json", []byte(`{"target": {"cc": "`+cc+`"}}`))
		if len(c.Problems) != 0 {
			t.Errorf("%s: %+v", cc, c.Problems)
		}
	}
}
