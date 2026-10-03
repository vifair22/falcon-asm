package benchdata

import (
	"strings"
	"testing"
)

func TestInputs(t *testing.T) {
	fx := Fixtures(t)
	if len(fx) < 10 {
		t.Fatalf("%d fixtures", len(fx))
	}
	if !strings.Contains(Fixture(t, "pmode.S"), "lgdt") {
		t.Error("pmode.S lacks lgdt")
	}
	if n := strings.Count(Large(t, 5000), "\n"); n < 5000 {
		t.Errorf("Large(5000) has %d lines", n)
	}
}
