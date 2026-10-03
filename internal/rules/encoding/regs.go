package encoding

import (
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// family is the architectural register a general register name is a view
// of ("a" for al/ah/ax/eax/rax, "r8" for r8/r8d/r8w/r8b/r8l), "" for
// anything else.
func family(name string) string {
	if f, ok := legacyFamily[name]; ok {
		return f
	}
	if strings.HasPrefix(name, "r") && len(name) >= 2 && name[1] >= '0' && name[1] <= '9' {
		n := 1
		for n < len(name) && name[n] >= '0' && name[n] <= '9' {
			n++
		}
		switch name[n:] {
		case "", "d", "w", "b", "l":
			return name[:n]
		}
	}
	return ""
}

var legacyFamily = map[string]string{
	"al": "a", "ah": "a", "ax": "a", "eax": "a", "rax": "a",
	"bl": "b", "bh": "b", "bx": "b", "ebx": "b", "rbx": "b",
	"cl": "c", "ch": "c", "cx": "c", "ecx": "c", "rcx": "c",
	"dl": "d", "dh": "d", "dx": "d", "edx": "d", "rdx": "d",
	"sil": "si", "si": "si", "esi": "si", "rsi": "si",
	"dil": "di", "di": "di", "edi": "di", "rdi": "di",
	"bpl": "bp", "bp": "bp", "ebp": "bp", "rbp": "bp",
	"spl": "sp", "sp": "sp", "esp": "sp", "rsp": "sp",
}

func isHighByte(name string) bool {
	switch name {
	case "ah", "bh", "ch", "dh":
		return true
	}
	return false
}

// needsREX reports a register that can only be encoded with a REX prefix:
// r8-r15 in any width, the uniform byte registers, and xmm8-15/ymm8-15.
// (Registers 16 and up need EVEX, which cannot address the high bytes
// either.)
func needsREX(r *isa.Register) bool {
	if r == nil {
		return false
	}
	switch r.Class {
	case "gpr":
		switch r.Name {
		case "sil", "dil", "bpl", "spl":
			return true
		}
		f := family(r.Name)
		return strings.HasPrefix(f, "r") && len(f) > 1 && f[1] >= '0' && f[1] <= '9'
	case "xmm", "ymm", "zmm":
		return r.Modes == "64"
	}
	return false
}

// suffixFor is the AT&T suffix naming an operand width.
func suffixFor(w int) string {
	switch w {
	case 8:
		return "b"
	case 16:
		return "w"
	case 32:
		return "l"
	case 64:
		return "q"
	}
	return ""
}

func suffixWidth(s string) int {
	switch s {
	case "b":
		return 8
	case "w":
		return 16
	case "l":
		return 32
	case "q":
		return 64
	}
	return 0
}

func isSegName(s string) bool {
	switch s {
	case "cs", "ds", "es", "fs", "gs", "ss":
		return true
	}
	return false
}

func isMaskReg(s string) bool {
	return len(s) == 2 && s[0] == 'k' && s[1] >= '0' && s[1] <= '7'
}
