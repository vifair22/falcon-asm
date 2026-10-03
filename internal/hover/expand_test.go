package hover

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func TestHoverExpansion(t *testing.T) {
	doc := &docstore.Document{URI: "file:///t.S", Text: "\tmymac 1\n\tmovl %eax, %ebx\n"}
	var asked [2]int
	opts := Options{Expansion: func(line, col int) (string, bool) {
		asked = [2]int{line, col}
		return "\tnop\n", true
	}}
	h, err := HoverWith(doc, protocol.Position{Line: 0, Character: 3}, opts)
	if err != nil || h == nil {
		t.Fatalf("hover = %v, %v", h, err)
	}
	if want := "**mymac** - macro\n\nExpands to:\n```asm\n\tnop\n```"; h.Contents.Value != want {
		t.Fatalf("hover = %q", h.Contents.Value)
	}
	if asked != [2]int{0, 1} {
		t.Fatalf("asked at %v", asked)
	}
	// Instructions are explained, never expanded.
	if h, _ := HoverWith(doc, protocol.Position{Line: 1, Character: 2}, opts); h == nil || strings.Contains(h.Contents.Value, "Expands") {
		t.Fatalf("instruction hover = %v", h)
	}
	// No expansion: a bare macro word has no hover.
	none := Options{Expansion: func(int, int) (string, bool) { return "", false }}
	if h, _ := HoverWith(doc, protocol.Position{Line: 0, Character: 3}, none); h != nil {
		t.Fatalf("hover = %v", h)
	}
}

func TestExpansionBlockCap(t *testing.T) {
	long := strings.Repeat("\tnop\n", MaxExpansionLines+3)
	got := expansionBlock(long)
	if strings.Count(got, "nop") != MaxExpansionLines || !strings.HasSuffix(got, "(3 more lines)") {
		t.Fatalf("block = %q", got)
	}
	if got := expansionBlock(strings.Repeat("\tnop\n", MaxExpansionLines+1)); !strings.HasSuffix(got, "(1 more line)") {
		t.Fatalf("singular = %q", got)
	}
	if got := expansionBlock(".ascii \"```\""); !strings.Contains(got, "````asm") {
		t.Fatalf("fence = %q", got)
	}
}
