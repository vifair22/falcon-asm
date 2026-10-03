package isa

import "strings"

// levelBySet maps XED ISA_SET names to psABI x86-64 levels. When XED gives an
// instruction no explicit ISA_SET it uses the EXTENSION name, which is why
// plain extension names (AVX, BMI1, SSE3, XSAVE ...) appear here too.
//
// v1 is the set every x86-64 processor implements. Besides the families the
// psABI lists (CMOV, CX8, FPU, FXSR, MMX, SSE, SSE2, SCE), it takes the
// pre-x86-64 base sets (I86 .. PENTIUMREAL, PPRO) and encodings that were
// always present on that hardware without a CPUID bit of their own: the
// long NOPs, PAUSE (a rep nop that older parts execute as nop) and RDPMC
// (architectural since the Pentium Pro, gated by CR4.PCE rather than CPUID).
// Anything with its own feature bit that the psABI does not name, such as
// CLFSH, SEP, RDTSCP or MONITOR, is left at 0 rather than guessed at.
var levelBySet = map[string]Level{
	// v1
	"I86": 1, "I186": 1, "I286REAL": 1, "I286PROTECTED": 1, "I386": 1,
	"I486": 1, "I486REAL": 1, "PENTIUMREAL": 1, "PENTIUMMMX": 1,
	"PPRO": 1, "PPRO_UD0_SHORT": 1, "PPRO_UD0_LONG": 1,
	"FAT_NOP": 1, "PREFETCH_NOP": 1, "PAUSE": 1, "RDPMC": 1,
	"CMOV": 1, "FCMOV": 1, "FCOMI": 1, "X87": 1,
	"MMX": 1, "SSE": 1, "SSEMXCSR": 1, "SSE_PREFETCH": 1,
	"SSE2": 1, "SSE2MMX": 1, "FXSAVE": 1, "FXSAVE64": 1, "LONGMODE": 1,

	// v2
	"CMPXCHG16B": 2, "LAHF": 2, "POPCNT": 2,
	"SSE3": 2, "SSE3X87": 2, "SSSE3": 2, "SSSE3MMX": 2,
	"SSE4": 2, "SSE42": 2,

	// v3
	"AVX": 3, "AVX2": 3, "AVX2GATHER": 3, "BMI1": 3, "BMI2": 3,
	"F16C": 3, "FMA": 3, "LZCNT": 3, "MOVBE": 3, "XSAVE": 3,
}

// v4Families are the AVX-512 subsets the psABI requires for x86-64-v4. XED
// splits each by vector length and kind (AVX512F_128, AVX512F_512,
// AVX512F_SCALAR, AVX512BW_KOPD ...); AVX512VL has no set of its own and is
// implied by the _128 and _256 variants. The newer AVX512_* sets (note the
// underscore: AVX512_VNNI_128, AVX512_BF16_512 ...) are outside the scheme.
var v4Families = []string{"AVX512F", "AVX512BW", "AVX512CD", "AVX512DQ"}

// LevelForISASet maps an XED ISA_SET (or EXTENSION) name to its x86-64
// micro-architecture level, or 0 when the set is unknown or outside the
// level scheme. Matching is case-insensitive.
func LevelForISASet(isaSet string) Level {
	s := strings.ToUpper(strings.TrimSpace(isaSet))
	if l, ok := levelBySet[s]; ok {
		return l
	}
	for _, fam := range v4Families {
		if s == fam || strings.HasPrefix(s, fam+"_") {
			return 4
		}
	}
	return 0
}
