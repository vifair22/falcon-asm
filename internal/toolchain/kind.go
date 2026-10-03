package toolchain

import (
	"path/filepath"
	"strings"
)

// IsLinkerScript reports a linker script: *.ld and *.lds, and the
// preprocessed forms *.ld.S and *.lds.S that a build runs through cpp. The
// latter end in .S like assembly, and assembling one as code produces a
// page of nonsense errors, so every caller that decides what to assemble or
// lint asks here first.
func IsLinkerScript(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	ext := filepath.Ext(b)
	if ext == ".ld" || ext == ".lds" {
		return true
	}
	stem := strings.TrimSuffix(b, ext)
	return ext == ".s" && (strings.HasSuffix(stem, ".ld") || strings.HasSuffix(stem, ".lds"))
}
