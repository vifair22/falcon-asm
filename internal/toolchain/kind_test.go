package toolchain

import "testing"

func TestIsLinkerScript(t *testing.T) {
	for p, want := range map[string]bool{
		"bios.ld": true, "x/boot.lds": true, "jmod.ld.S": true, "a.lds.s": true,
		"boot.S": false, "ld.S": false, "pmode.s": false, "gold.c": false, "x.ld.c": false,
	} {
		if got := IsLinkerScript(p); got != want {
			t.Errorf("IsLinkerScript(%q) = %v, want %v", p, got, want)
		}
	}
}
