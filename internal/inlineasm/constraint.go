package inlineasm

import (
	"strconv"
	"strings"
)

// Constraint and clobber explanations for GCC's x86 machine description.
// The wording follows the GCC manual's "Machine Constraints" and "x86
// Operand Modifiers" sections, shortened to fit a hover line.

var constraintLetters = map[string]string{
	"r":  "any general register",
	"q":  "register with an addressable low byte (a/b/c/d in 32-bit code, any GPR in 64-bit)",
	"Q":  "register with an addressable high byte (a/b/c/d: %ah/%bh/%ch/%dh)",
	"a":  "register a (%al/%ax/%eax/%rax)",
	"b":  "register b (%bl/%bx/%ebx/%rbx)",
	"c":  "register c (%cl/%cx/%ecx/%rcx)",
	"d":  "register d (%dl/%dx/%edx/%rdx)",
	"S":  "register si (%si/%esi/%rsi)",
	"D":  "register di (%di/%edi/%rdi)",
	"A":  "a:d register pair (%edx:%eax; %rdx:%rax for 128-bit values in 64-bit code)",
	"U":  "call-clobbered integer register",
	"R":  "legacy register (a b c d si di bp sp, no REX needed)",
	"l":  "register usable as an index (any GPR except sp)",
	"f":  "x87 floating-point register",
	"t":  "top of x87 stack %st(0)",
	"u":  "second x87 register %st(1)",
	"x":  "SSE register (%xmm0-15)",
	"v":  "any EVEX-encodable SSE register (%xmm0-31)",
	"y":  "MMX register",
	"Yz": "first SSE register %xmm0",
	"Yk": "AVX-512 mask register usable as a predicate (%k1-k7)",
	"k":  "any AVX-512 mask register (%k0-k7)",
	"m":  "memory operand",
	"o":  "offsettable memory operand",
	"V":  "non-offsettable memory operand",
	"<":  "memory with autodecrement addressing (push)",
	">":  "memory with autoincrement addressing (pop)",
	"p":  "valid memory address (as for lea)",
	"g":  "general register, memory or integer immediate",
	"X":  "any operand",
	"i":  "integer immediate (may be symbolic)",
	"n":  "integer immediate with a known numeric value",
	"s":  "symbolic immediate (address of a symbol)",
	"E":  "floating-point immediate",
	"F":  "floating-point immediate",
	"G":  "standard x87 floating-point constant",
	"C":  "SSE constant zero",
	"I":  "integer constant 0..31 (32-bit shift count)",
	"J":  "integer constant 0..63 (64-bit shift count)",
	"K":  "signed 8-bit integer constant",
	"L":  "0xff or 0xffff (and-mask for zero extension)",
	"M":  "integer constant 0..3 (lea scale shift)",
	"N":  "unsigned 8-bit integer constant (in/out port)",
	"O":  "integer constant 0..127 (128-bit shift count)",
	"e":  "32-bit signed integer constant (sign-extended immediate)",
	"Z":  "32-bit unsigned integer constant (zero-extended immediate)",
}

// flagConditions are the suffixes valid after =@cc, i.e. the setcc/jcc
// condition codes.
var flagConditions = map[string]string{
	"a": "above (CF=0 and ZF=0)", "ae": "above or equal (CF=0)",
	"b": "below (CF=1)", "be": "below or equal (CF=1 or ZF=1)",
	"c": "carry (CF=1)", "e": "equal (ZF=1)", "z": "zero (ZF=1)",
	"g": "greater (ZF=0 and SF=OF)", "ge": "greater or equal (SF=OF)",
	"l": "less (SF!=OF)", "le": "less or equal (ZF=1 or SF!=OF)",
	"na": "not above", "nae": "not above or equal", "nb": "not below",
	"nbe": "not below or equal", "nc": "no carry (CF=0)", "ne": "not equal (ZF=0)",
	"ng": "not greater", "nge": "not greater or equal", "nl": "not less",
	"nle": "not less or equal", "no": "no overflow (OF=0)", "np": "parity odd (PF=0)",
	"ns": "not sign (SF=0)", "nz": "not zero (ZF=0)", "o": "overflow (OF=1)",
	"p": "parity even (PF=1)", "pe": "parity even (PF=1)", "po": "parity odd (PF=0)",
	"s": "sign (SF=1)",
}

// ConstraintHover explains a GCC x86 operand constraint in one short line,
// e.g. "=a" -> "output (write-only): register a (%al/%ax/%eax/%rax)". The
// argument may include its surrounding quotes. An empty result means the
// string is empty.
func ConstraintHover(c string) string {
	c = unquote(c)
	if c == "" {
		return ""
	}
	role := "input"
	var notes []string
	// Modifiers come first in practice, but GCC accepts = + & % anywhere in
	// the first alternative, so collect them wherever they appear.
	body := make([]byte, 0, len(c))
	for i := 0; i < len(c); i++ {
		switch c[i] {
		case '=':
			role = "output (write-only)"
		case '+':
			role = "output (read-write)"
		case '&':
			notes = appendOnce(notes, "early-clobber: written before all inputs are read, so it never shares a register with an input")
		case '%':
			notes = appendOnce(notes, "commutative with the next operand")
		default:
			body = append(body, c[i])
		}
	}
	b := string(body)
	if strings.HasPrefix(b, "@cc") {
		cond := b[3:]
		desc, ok := flagConditions[cond]
		if !ok {
			desc = "unknown condition"
		}
		return "flag output: condition " + cond + ", " + desc + ", result 0/1 as by set" + cond
	}
	alts := strings.Split(b, ",")
	descs := make([]string, 0, len(alts))
	for _, a := range alts {
		descs = append(descs, describeAlternative(a))
	}
	out := role + ": "
	if len(descs) == 1 {
		out += descs[0]
	} else {
		out += "alternatives: " + strings.Join(descs, " | ")
	}
	if len(notes) > 0 {
		out += "; " + strings.Join(notes, "; ")
	}
	return out
}

func appendOnce(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func unquote(c string) string {
	if len(c) >= 2 && c[0] == '"' && c[len(c)-1] == '"' {
		return c[1 : len(c)-1]
	}
	return c
}

// describeAlternative explains one comma-separated alternative: a sequence
// of letters, multi-letter codes, matching digits and [name] references,
// any of which may be chosen by the compiler.
func describeAlternative(a string) string {
	var parts []string
	disparage := ""
	for i := 0; i < len(a); {
		c := a[i]
		switch {
		case c == '#':
			// The rest of the alternative only matters to register
			// preferencing; the manual says it is ignored as a constraint.
			i = len(a)
			continue
		case c == '*' || c == ' ' || c == '\t':
			// '*' only hides the next letter from register preferencing;
			// that letter is still a constraint.
			i++
			continue
		case c == '?':
			disparage = " (slightly disparaged)"
		case c == '!':
			disparage = " (severely disparaged)"
		case isDigit(c):
			j := i
			for j < len(a) && isDigit(a[j]) {
				j++
			}
			n, _ := strconv.Atoi(a[i:j])
			parts = append(parts, "same location as operand "+strconv.Itoa(n))
			i = j
			continue
		case c == '[':
			j := strings.IndexByte(a[i:], ']')
			if j < 0 {
				parts = append(parts, "unterminated operand name")
				i = len(a)
				continue
			}
			parts = append(parts, "same location as operand ["+a[i+1:i+j]+"]")
			i += j + 1
			continue
		case (c == 'Y' || c == 'B' || c == 'W' || c == 'j') && i+1 < len(a):
			code := a[i : i+2]
			if d, ok := constraintLetters[code]; ok {
				parts = append(parts, d)
			} else {
				parts = append(parts, "machine-specific constraint "+code)
			}
			i += 2
			continue
		default:
			if d, ok := constraintLetters[string(c)]; ok {
				parts = append(parts, d)
			} else {
				parts = append(parts, "unknown constraint '"+string(c)+"'")
			}
		}
		i++
	}
	if len(parts) == 0 {
		return "no constraint" + disparage
	}
	return strings.Join(parts, " or ") + disparage
}

// ClobberHover explains one clobber list entry.
func ClobberHover(c string) string {
	c = unquote(c)
	switch c {
	case "memory":
		return "memory: the asm may read or write memory not named in its operands; cached values are flushed and memory accesses are not moved across it"
	case "cc":
		return "cc: the asm modifies the flags register (on x86 GCC assumes this for every asm anyway)"
	case "dirflag":
		return "dirflag: the asm changes the direction flag (DF)"
	case "fpsr":
		return "fpsr: the asm changes the x87 status word"
	case "flags":
		return "flags: the asm modifies the flags register"
	case "redzone":
		return "redzone: the asm writes below the stack pointer (the 128-byte x86-64 red zone)"
	case "":
		return ""
	}
	r := strings.TrimPrefix(c, "%")
	if isRegisterName(r) {
		return "register %" + r + " is clobbered: the compiler keeps no value in it across the asm and never assigns it to an operand"
	}
	return "unknown clobber \"" + c + "\""
}

// isRegisterName recognises the x86 register spellings GCC accepts in a
// clobber list. The set is small enough to check structurally rather than
// with a table.
func isRegisterName(r string) bool {
	switch r {
	case "ax", "bx", "cx", "dx", "si", "di", "bp", "sp",
		"al", "bl", "cl", "dl", "ah", "bh", "ch", "dh", "sil", "dil", "bpl", "spl",
		"st", "argp", "frame":
		return true
	}
	if len(r) == 3 && (r[0] == 'e' || r[0] == 'r') {
		switch r[1:] {
		case "ax", "bx", "cx", "dx", "si", "di", "bp", "sp":
			return true
		}
	}
	if n, ok := numSuffix(r, "st("); ok && strings.HasSuffix(r, ")") {
		return n <= 7
	}
	for _, pre := range []struct {
		p   string
		max int
	}{{"xmm", 31}, {"ymm", 31}, {"zmm", 31}, {"mm", 7}, {"k", 7}, {"r", 15}} {
		if n, ok := numSuffix(r, pre.p); ok {
			return n <= pre.max && (pre.p != "r" || n >= 8)
		}
	}
	// r8d r8w r8b
	if len(r) >= 3 && r[0] == 'r' && strchr("dwb", r[len(r)-1]) {
		if n, ok := numSuffix(r[:len(r)-1], "r"); ok {
			return n >= 8 && n <= 15
		}
	}
	return false
}

// numSuffix parses r as prefix followed by decimal digits (and, for "st(",
// a closing parenthesis which the caller checks).
func numSuffix(r, prefix string) (int, bool) {
	if !strings.HasPrefix(r, prefix) {
		return 0, false
	}
	d := strings.TrimSuffix(r[len(prefix):], ")")
	if d == "" || len(d) > 2 {
		return 0, false
	}
	for k := 0; k < len(d); k++ {
		if !isDigit(d[k]) {
			return 0, false
		}
	}
	n, _ := strconv.Atoi(d)
	return n, true
}

var modifiers = map[byte]string{
	'a': "print as an address (memory reference without brackets)",
	'A': "print as an absolute memory reference (*) for jmp/call",
	'b': "8-bit register name (%al)",
	'B': "print the b opcode suffix",
	'c': "constant without $ or other punctuation",
	'd': "duplicate the register operand (AVX)",
	'E': "address with a 64-bit register (x32)",
	'g': "512-bit register name (%zmm)",
	'h': "high 8-bit register name (%ah)",
	'H': "memory reference plus 8 bytes",
	'k': "32-bit register name (%eax)",
	'l': "label name without $ (asm goto)",
	'L': "print the l opcode suffix",
	'n': "negated constant without punctuation",
	'p': "raw symbol name without syntax prefixes",
	'P': "symbol with PLT suffix / no $",
	'q': "64-bit register name (%rax)",
	'Q': "print the q opcode suffix",
	'R': "embedded rounding and SAE",
	'r': "SAE only",
	's': "shift-double count followed by a separator",
	'S': "print the s opcode suffix",
	't': "256-bit register name (%ymm)",
	'T': "print the t opcode suffix",
	'V': "full integer register name without %",
	'w': "16-bit register name (%ax)",
	'W': "print the w opcode suffix",
	'x': "128-bit register name (%xmm)",
	'y': "print st(0) instead of st",
	'z': "opcode suffix for the operand size",
	'Z': "opcode suffix for the operand size (x87 rules)",
}

// ModifierHover explains an x86 operand modifier letter, "" when unknown.
func ModifierHover(m byte) string { return modifiers[m] }
