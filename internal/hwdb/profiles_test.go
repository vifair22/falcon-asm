package hwdb

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

func TestProfileSupports(t *testing.T) {
	tests := []struct {
		profile string
		has     []string
		lacks   []string
	}{
		// cpuid on a 386, cmov before P6, rdtsc on a 486 (roadmap item 16).
		{"i386", []string{"I386", "I286REAL", "X87", ""}, []string{"I486REAL", "PENTIUMREAL", "CMOV", "MMX", "SSE", "LONGMODE"}},
		{"i486", []string{"I486REAL", "I386"}, []string{"PENTIUMREAL", "CMOV", "I486"}},
		{"pentium", []string{"PENTIUMREAL", "I486"}, []string{"CMOV", "PENTIUMMMX"}},
		{"pentium-mmx", []string{"PENTIUMMMX", "RDPMC"}, []string{"CMOV"}},
		{"p6", []string{"CMOV", "FCMOV", "FCOMI", "PPRO"}, []string{"PENTIUMMMX", "SEP", "SSE"}},
		{"i686", []string{"CMOV"}, []string{"SSE"}},
		{"pentium2", []string{"PENTIUMMMX", "SEP", "FXSAVE"}, []string{"SSE"}},
		{"pentium3", []string{"SSE", "SSEMXCSR"}, []string{"SSE2"}},
		{"pentium4", []string{"SSE2", "CLFSH"}, []string{"SSE3", "LONGMODE"}},
		{"core2", []string{"SSSE3", "LONGMODE", "CMPXCHG16B", "LAHF"}, []string{"SSE4", "POPCNT", "VTX"}},
		{"nehalem", []string{"SSE42", "POPCNT", "RDTSCP"}, []string{"AES", "AVX"}},
		{"sandybridge", []string{"AVX", "XSAVE", "AES"}, []string{"AVX2", "F16C"}},
		{"haswell", []string{"AVX2", "BMI2", "MOVBE", "FMA", "LZCNT", "BMI1", "F16C"}, []string{"RTM", "AVX512F_512", "ADOX_ADCX"}},
		{"skylake", []string{"CLFLUSHOPT", "XSAVES", "RDSEED"}, []string{"AVX512F_512", "SGX"}},
		{"skylake-avx512", []string{"AVX512F_512", "AVX512BW_128", "CLWB"}, []string{"AVX512_VBMI_512"}},
		{"icelake", []string{"AVX512_VBMI2_256", "GFNI", "VAES", "RDPID", "SHA"}, []string{"CLWB", "AMX_TILE"}},
		{"icelake-server", []string{"CLWB", "WBNOINVD"}, []string{"AMX_TILE"}},
		{"x86-64", []string{"SSE2", "LONGMODE", "CMOV"}, []string{"SSE3", "POPCNT", "SEP", "CLFSH"}},
		{"x86-64-v2", []string{"SSE42", "POPCNT", "LAHF", "CMPXCHG16B"}, []string{"AVX"}},
		{"x86-64-v3", []string{"AVX2", "MOVBE", "XSAVE"}, []string{"AVX512F_512", "AES"}},
		{"x86-64-v4", []string{"AVX512F_512", "AVX512DQ_SCALAR", "AVX512BW_KOPD"}, []string{"AVX512_VBMI_512"}},
		{"k6", []string{"PENTIUMMMX", "PENTIUMREAL"}, []string{"CMOV", "3DNOW"}},
		{"k6-3", []string{"3DNOW"}, []string{"CMOV"}},
		{"athlon", []string{"CMOV", "3DNOW", "SEP"}, []string{"SSE", "LONGMODE"}},
		{"athlon-xp", []string{"SSE", "FXSAVE"}, []string{"SSE2"}},
		{"k8", []string{"SSE2", "LONGMODE", "3DNOW"}, []string{"SSE3", "LAHF", "CMPXCHG16B"}},
		{"opteron-sse3", []string{"SSE3"}, []string{"SSSE3"}},
		{"znver1", []string{"AVX2", "SHA", "CLZERO", "SSE4A", "SVM"}, []string{"CLWB", "AVX512F_512", "FMA4", "3DNOW"}},
		{"znver2", []string{"CLWB", "RDPID"}, []string{"VAES"}},
		{"znver3", []string{"VAES", "VPCLMULQDQ", "INVPCID"}, []string{"AVX512F_512"}},
		{"znver4", []string{"AVX512F_512", "AVX512_BF16_256", "AVX512_VNNI_512", "GFNI"}, []string{"AVX512_FP16_512", "AVX_VNNI"}},
	}
	for _, tt := range tests {
		p, ok := CPUProfile(tt.profile)
		if !ok {
			t.Errorf("profile %s missing", tt.profile)
			continue
		}
		for _, s := range tt.has {
			if !p.Supports(s) {
				t.Errorf("%s lacks %s", tt.profile, s)
			}
		}
		for _, s := range tt.lacks {
			if p.Supports(s) {
				t.Errorf("%s has %s", tt.profile, s)
			}
		}
	}
	if p, _ := CPUProfile("core2"); !p.LongMode() {
		t.Error("core2 not long mode")
	}
	if p, _ := CPUProfile("pentium4"); p.LongMode() {
		t.Error("pentium4 long mode")
	}
}

func TestProfileNames(t *testing.T) {
	names := Profiles()
	want := []string{"i386", "i486", "pentium", "pentium-mmx", "pentiumpro", "pentium2", "pentium3", "pentium4",
		"core2", "nehalem", "sandybridge", "haswell", "skylake", "icelake", "x86-64", "x86-64-v2", "x86-64-v3",
		"x86-64-v4", "k6", "athlon", "k8", "znver1", "znver2", "znver3", "znver4"}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("profile %s missing", w)
		}
	}
	march := map[string]string{
		"i586": "pentium", "i686": "pentiumpro", "corei7": "nehalem", "core-avx2": "haswell",
		"HASWELL": "haswell", "x86-64-v1": "x86-64", "opteron": "k8", "athlon-4": "athlon-xp",
	}
	for m, want := range march {
		if got, ok := ProfileForMarch(m); !ok || got != want {
			t.Errorf("ProfileForMarch(%s) = %q", m, got)
		}
	}
	for _, m := range []string{"native", "", "z80"} {
		if _, ok := ProfileForMarch(m); ok {
			t.Errorf("ProfileForMarch(%q) resolved", m)
		}
	}
	if len(ProfileAliases()) < 10 {
		t.Error("too few aliases")
	}
	p, _ := CPUProfile("x86-64-v4")
	sets := p.Sets()
	if len(sets) < 40 || !strings.Contains(strings.Join(sets, ","), "AVX512F_*") {
		t.Errorf("Sets() = %v", sets)
	}
}

// The x86-64-vN profiles agree with internal/isa's level table: a profile
// supports a set exactly when its level is 1..N.
func TestLevelProfilesMatchISA(t *testing.T) {
	sets := isaSets(t)
	for n := 1; n <= 4; n++ {
		name := "x86-64"
		if n > 1 {
			name += "-v" + string(rune('0'+n))
		}
		p, _ := CPUProfile(name)
		for s := range sets {
			lv := int(isa.LevelForISASet(s))
			want := lv >= 1 && lv <= n
			if p.Supports(s) != want {
				t.Errorf("%s supports %s = %v, isa level %d", name, s, p.Supports(s), lv)
			}
		}
	}
}

// Every 64-bit CPU implements x86-64 v1; the generations that implement a
// higher level implement all of it.
func TestProfileSupersets(t *testing.T) {
	sets := isaSets(t)
	sup := map[string]string{
		"core2": "x86-64", "nehalem": "x86-64-v2", "haswell": "x86-64-v3", "skylake-avx512": "x86-64-v4",
		"icelake": "x86-64-v4", "k8": "x86-64", "znver1": "x86-64-v3", "znver4": "x86-64-v4",
	}
	for big, small := range sup {
		b, _ := CPUProfile(big)
		s, _ := CPUProfile(small)
		for set := range sets {
			if s.Supports(set) && !b.Supports(set) {
				t.Errorf("%s lacks %s from %s", big, set, small)
			}
		}
	}
	// Every set a profile names exists in internal/isa ("MMX" is the
	// psABI's name for XED's PENTIUMMMX).
	for _, n := range Profiles() {
		p, _ := CPUProfile(n)
		for _, s := range p.Sets() {
			if pre, ok := strings.CutSuffix(s, "*"); ok {
				found := false
				for have := range sets {
					found = found || strings.HasPrefix(have, pre)
				}
				if !found {
					t.Errorf("%s: pattern %s matches nothing", n, s)
				}
				continue
			}
			if !sets[s] && s != "MMX" {
				t.Errorf("%s: ISA set %s not in internal/isa", n, s)
			}
		}
	}
}
