package all

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// docs/RULES.md is generated from the registry. This test is what makes
// "every rule is documented" hold: a rule added or changed without
// `make rules-doc` fails here.
func TestRulesDocUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "RULES.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != lint.Markdown() {
		t.Fatal("docs/RULES.md is stale: run `make rules-doc`")
	}
}

// Every rule documents why it exists and shows a bad and a good example
// (roadmap item 86); the registry only insists on a summary.
func TestRulesFullyDocumented(t *testing.T) {
	rules := lint.Rules()
	if len(rules) < 5 {
		t.Fatalf("only %d rules registered", len(rules))
	}
	for _, r := range rules {
		d := r.Doc
		if strings.TrimSpace(d.Why) == "" || strings.TrimSpace(d.Bad) == "" || strings.TrimSpace(d.Good) == "" {
			t.Errorf("rule %s: Why, Bad and Good are all required", r.Code)
		}
		if d.Bad == d.Good {
			t.Errorf("rule %s: Bad and Good are the same", r.Code)
		}
	}
}
