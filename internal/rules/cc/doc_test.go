package cc

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// TestDocExamples runs every rule's documented bad example (which must be
// reported) and good example (which must not), so docs/RULES.md cannot
// drift from what the rules do.
func TestDocExamples(t *testing.T) {
	for _, code := range allCodes {
		r, _ := lint.Lookup(code)
		mode := asm.Mode32
		if strings.Contains(r.Doc.Bad, "%r") || strings.Contains(r.Doc.Bad, "cc=sysv") {
			mode = asm.Mode64
		}
		if got := run(t, r.Doc.Bad, mode, "", code); len(got) == 0 {
			t.Errorf("%s: bad example not reported:\n%s", code, r.Doc.Bad)
		}
		if got := run(t, r.Doc.Good, mode, "", code); len(got) != 0 {
			t.Errorf("%s: good example reported: %v\n%s", code, got, r.Doc.Good)
		}
	}
}
