package inlinec

import (
	"strconv"
	"strings"
)

// A register family is every name of one architectural register: %al, %ah,
// %ax, %eax and %rax are all "rax"; %xmm3, %ymm3 and %zmm3 are all "xmm3".
// Clobbers, fixed constraints and template writes are compared by family,
// because GCC's clobber of "eax" protects the whole of %rax and a write to
// %al destroys whatever the compiler kept in %rax.

var legacyFamilies = map[string]string{
	"al": "rax", "ah": "rax", "ax": "rax", "eax": "rax", "rax": "rax",
	"bl": "rbx", "bh": "rbx", "bx": "rbx", "ebx": "rbx", "rbx": "rbx",
	"cl": "rcx", "ch": "rcx", "cx": "rcx", "ecx": "rcx", "rcx": "rcx",
	"dl": "rdx", "dh": "rdx", "dx": "rdx", "edx": "rdx", "rdx": "rdx",
	"sil": "rsi", "si": "rsi", "esi": "rsi", "rsi": "rsi",
	"dil": "rdi", "di": "rdi", "edi": "rdi", "rdi": "rdi",
	"bpl": "rbp", "bp": "rbp", "ebp": "rbp", "rbp": "rbp",
	"spl": "rsp", "sp": "rsp", "esp": "rsp", "rsp": "rsp",
}

var legacyWidths = map[string]int{
	"al": 8, "ah": 8, "bl": 8, "bh": 8, "cl": 8, "ch": 8, "dl": 8, "dh": 8,
	"sil": 8, "dil": 8, "bpl": 8, "spl": 8,
	"ax": 16, "bx": 16, "cx": 16, "dx": 16, "si": 16, "di": 16, "bp": 16, "sp": 16,
}

// family maps a register name (with or without '%', any case) to its
// family and the width of the name in bits. ok is false for names that are
// not general-purpose, vector or mask registers: segment, control and x87
// registers are never allocated to operands, so no rule here cares about
// them.
func family(name string) (fam string, width int, ok bool) {
	r := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "%"))
	if f, ok := legacyFamilies[r]; ok {
		w := legacyWidths[r]
		switch {
		case w != 0:
		case r[0] == 'e':
			w = 32
		default:
			w = 64
		}
		return f, w, true
	}
	// r8..r15 with an optional b/w/d (or GCC's l) width suffix.
	if len(r) >= 2 && r[0] == 'r' {
		body, w := r[1:], 64
		switch body[len(body)-1] {
		case 'b', 'l':
			body, w = body[:len(body)-1], 8
		case 'w':
			body, w = body[:len(body)-1], 16
		case 'd':
			body, w = body[:len(body)-1], 32
		}
		if n, ok := smallNum(body); ok && n >= 8 && n <= 15 {
			return "r" + strconv.Itoa(n), w, true
		}
		return "", 0, false
	}
	for _, v := range []struct {
		pre string
		w   int
	}{{"xmm", 128}, {"ymm", 256}, {"zmm", 512}} {
		if n, ok := smallNum(strings.TrimPrefix(r, v.pre)); ok && strings.HasPrefix(r, v.pre) && n <= 31 {
			return "xmm" + strconv.Itoa(n), v.w, true
		}
	}
	if n, ok := smallNum(strings.TrimPrefix(r, "k")); ok && strings.HasPrefix(r, "k") && n <= 7 {
		return "k" + strconv.Itoa(n), 64, true
	}
	return "", 0, false
}

func smallNum(s string) (int, bool) {
	if s == "" || len(s) > 2 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// isGPR reports whether fam is a general-purpose register family.
func isGPR(fam string) bool {
	_, ok := legacyFamilies[fam]
	return ok || isREXOnly(fam)
}

func isXMM(fam string) bool  { return strings.HasPrefix(fam, "xmm") }
func isMask(fam string) bool { return len(fam) == 2 && fam[0] == 'k' }

// displayName is the name a message uses for a family in the given mode:
// %eax in 16/32-bit code (the 64-bit name does not exist there), %rax in
// 64-bit code.
func displayName(fam string, mode int) string {
	if mode != 64 {
		if _, ok := legacyFamilies[fam]; ok {
			return "e" + fam[1:]
		}
	}
	return fam
}

// clobberStyle describes how the statement already spells its register
// clobbers, so an added one matches: "%eax" or "eax", 32- or 64-bit names.
type clobberStyle struct {
	percent bool
	narrow  bool // e-names in 64-bit code
}

func styleOf(clobbers []string, mode int) clobberStyle {
	var s clobberStyle
	s.narrow = mode != 64
	for _, c := range clobbers {
		fam, w, ok := family(c)
		if !ok || !isGPR(fam) {
			continue
		}
		s.percent = strings.HasPrefix(strings.TrimSpace(c), "%")
		if _, legacy := legacyFamilies[fam]; legacy {
			s.narrow = w == 32
		}
		break
	}
	return s
}

// clobberName spells fam for a clobber list in style s.
func (s clobberStyle) clobberName(fam string) string {
	n := fam
	if _, legacy := legacyFamilies[fam]; legacy && s.narrow {
		n = "e" + fam[1:]
	}
	if s.percent {
		n = "%" + n
	}
	return n
}

// regSet is a small set of register families.
type regSet map[string]bool

func (s regSet) add(fams ...string) {
	for _, f := range fams {
		s[f] = true
	}
}

// isREXOnly reports r8..r15, which exist only in 64-bit code.
func isREXOnly(fam string) bool {
	return len(fam) >= 2 && fam[0] == 'r' && fam[1] >= '0' && fam[1] <= '9'
}

// existsIn reports whether the family can be named in the mode: r8-r15
// and xmm8 and up need REX (or EVEX for xmm16 and up), which exists only in
// 64-bit code. A write to one elsewhere is an assembler error, not a
// missing clobber.
func existsIn(fam string, mode int) bool {
	if mode == 64 {
		return true
	}
	if isXMM(fam) {
		n, _ := smallNum(fam[3:])
		return n < 8
	}
	return !isREXOnly(fam)
}
