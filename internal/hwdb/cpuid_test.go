package hwdb

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

// isaSets is every ISA_SET name the instruction tables use, upper-cased:
// the merged index plus both raw data files, since a form system.json
// replaces (the XED long nop, FAT_NOP) is still a real ISA set.
func isaSets(t *testing.T) map[string]bool {
	t.Helper()
	if err := isa.DataError(); err != nil {
		t.Fatal(err)
	}
	sets := map[string]bool{}
	for _, in := range isa.Instructions() {
		for _, f := range in.Forms {
			if f.ISASet != "" {
				sets[strings.ToUpper(f.ISASet)] = true
			}
		}
	}
	for _, name := range []string{"../isa/data/xed.json", "../isa/data/system.json"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var insts []isa.Instruction
		if err := json.Unmarshal(raw, &insts); err != nil {
			t.Fatal(err)
		}
		for _, in := range insts {
			for _, f := range in.Forms {
				if f.ISASet != "" {
					sets[strings.ToUpper(f.ISASet)] = true
				}
			}
		}
	}
	return sets
}

func TestFeaturesForISASet(t *testing.T) {
	tests := []struct {
		set  string
		want string // Where() of each bit, joined
	}{
		{"AVX2", "CPUID.(EAX=07h,ECX=0):EBX[5]"},
		{"AVX2GATHER", "CPUID.(EAX=07h,ECX=0):EBX[5]"},
		{"BMI2", "CPUID.(EAX=07h,ECX=0):EBX[8]"},
		{"BMI1", "CPUID.(EAX=07h,ECX=0):EBX[3]"},
		{"MOVBE", "CPUID.01h:ECX[22]"},
		{"LZCNT", "CPUID.80000001h:ECX[5]"},
		{"POPCNT", "CPUID.01h:ECX[23]"},
		{"SSE4", "CPUID.01h:ECX[19]"},
		{"sse42", "CPUID.01h:ECX[20]"},
		{"CMPXCHG16B", "CPUID.01h:ECX[13]"},
		{"LAHF", "CPUID.80000001h:ECX[0]"},
		{"XSAVE", "CPUID.01h:ECX[26]"},
		{"RDRAND", "CPUID.01h:ECX[30]"},
		{"RDTSCP", "CPUID.80000001h:EDX[27]"},
		{"LONGMODE", "CPUID.80000001h:EDX[29]"},
		{"CMOV", "CPUID.01h:EDX[15]"},
		{"FCMOV", "CPUID.01h:EDX[0] CPUID.01h:EDX[15]"},
		{"FMA", "CPUID.01h:ECX[12] CPUID.01h:ECX[28]"},
		{"AVXAES", "CPUID.01h:ECX[25] CPUID.01h:ECX[28]"},
		{"AVX512F_512", "CPUID.(EAX=07h,ECX=0):EBX[16]"},
		{"AVX512F_128", "CPUID.(EAX=07h,ECX=0):EBX[16] CPUID.(EAX=07h,ECX=0):EBX[31]"},
		{"AVX512BW_256", "CPUID.(EAX=07h,ECX=0):EBX[30] CPUID.(EAX=07h,ECX=0):EBX[31]"},
		{"AVX512_VBMI2_512", "CPUID.(EAX=07h,ECX=0):ECX[6]"},
		{"AVX512_VBMI_512", "CPUID.(EAX=07h,ECX=0):ECX[1]"},
		{"AVX512_BF16_512", "CPUID.(EAX=07h,ECX=1):EAX[5]"},
		{"XSAVEC", "CPUID.(EAX=0Dh,ECX=1):EAX[1]"},
		{"3DNOW", "CPUID.80000001h:EDX[31]"},
		{"I386", ""},
		{"PENTIUMREAL", ""},
		{"", ""},
	}
	for _, tt := range tests {
		var got []string
		for _, f := range FeaturesForISASet(tt.set) {
			got = append(got, f.Where())
		}
		if strings.Join(got, " ") != tt.want {
			t.Errorf("FeaturesForISASet(%q) = %v; want %q", tt.set, got, tt.want)
		}
	}
}

func TestFeatureForMnemonic(t *testing.T) {
	tests := map[string]string{
		"rdtsc": "TSC", "RDMSR": "MSR", "wrmsr": "MSR", "cmpxchg8b": "CX8",
		"syscall": "SYSCALL", "prefetchw": "PREFETCHW", "fisttp": "SSE3",
	}
	for mn, want := range tests {
		f, ok := FeatureForMnemonic(mn)
		if !ok || f.Name != want {
			t.Errorf("FeatureForMnemonic(%s) = %q", mn, f.Name)
		}
	}
	if _, ok := FeatureForMnemonic("mov"); ok {
		t.Error("mov guarded")
	}
}

// Every ISA set a CPUID bit names exists in internal/isa (or is a prefix
// that matches one), so a typo cannot silently disconnect rule 17.
func TestCPUIDISASetsExist(t *testing.T) {
	sets := isaSets(t)
	for _, l := range cpuidTab().leaves {
		for _, fs := range l.Regs() {
			for _, f := range fs {
				for _, s := range f.ISA {
					us := strings.ToUpper(s)
					if p, ok := strings.CutSuffix(us, "*"); ok {
						found := false
						for have := range sets {
							if strings.HasPrefix(have, p) {
								found = true
							}
						}
						if !found {
							t.Errorf("%s %s: pattern %s matches no ISA set", l.Key(), f.Name, s)
						}
						continue
					}
					// MMX is the psABI name; the XED tables call it PENTIUMMMX.
					if !sets[us] && us != "MMX" {
						t.Errorf("%s %s: ISA set %s not in internal/isa", l.Key(), f.Name, s)
					}
				}
			}
		}
	}
}

// The level a CPUID bit carries agrees with internal/isa's level for the
// ISA sets it guards, and the psABI's own lists are complete.
func TestCPUIDLevels(t *testing.T) {
	for _, l := range cpuidTab().leaves {
		for _, fs := range l.Regs() {
			for _, f := range fs {
				for _, s := range f.ISA {
					lv := int(isa.LevelForISASet(strings.TrimSuffix(s, "*")))
					if strings.HasSuffix(s, "*") {
						lv = int(isa.LevelForISASet(strings.TrimSuffix(s, "*") + "512"))
					}
					if lv != 0 && f.Level != 0 && lv != f.Level && !multiGuard(s) {
						t.Errorf("%s %s: level %d, isa says %s is v%d", l.Key(), f.Name, f.Level, s, lv)
					}
				}
			}
		}
	}
	want := map[int][]string{
		1: {"CMOV", "CX8", "FPU", "FXSR", "MMX", "SYSCALL", "SSE", "SSE2"},
		2: {"CX16", "LAHF_LM", "POPCNT", "SSE3", "SSSE3", "SSE4_1", "SSE4_2"},
		3: {"AVX", "AVX2", "BMI1", "BMI2", "F16C", "FMA", "ABM", "MOVBE", "OSXSAVE"},
		4: {"AVX512F", "AVX512BW", "AVX512CD", "AVX512DQ", "AVX512VL"},
	}
	for lv, names := range want {
		for _, n := range names {
			f, ok := Feature(n)
			if !ok || f.Level != lv {
				t.Errorf("feature %s level = %d, want %d", n, f.Level, lv)
			}
		}
	}
}

// multiGuard sets are listed under two bits (FCMOV under FPU and CMOV),
// where only one of the bits carries the set's own level.
func multiGuard(s string) bool {
	switch s {
	case "FCMOV", "FCOMI", "F16C", "FMA":
		return true
	}
	return false
}

func TestCPUIDLookup(t *testing.T) {
	l, ok := CPUID(7, 0)
	if !ok || l.Name != "Structured extended features" {
		t.Errorf("leaf 7.0 = %v", l)
	}
	l, ok = CPUID(7, 1)
	if !ok || !strings.Contains(l.Name, "subleaf 1") {
		t.Errorf("leaf 7.1 = %v", l)
	}
	if _, ok := CPUID(7, 5); ok {
		t.Error("leaf 7.5 known")
	}
	// Leaves without subleaf entries ignore the subleaf.
	l, ok = CPUID(1, 99)
	if !ok || l.Key() != "CPUID.01h" {
		t.Errorf("leaf 1 = %v", l)
	}
	if _, ok := CPUID(0x40000000, 0); ok {
		t.Error("hypervisor leaf known")
	}
	if n := len(CPUIDLeaves()); n < 15 {
		t.Errorf("only %d leaves", n)
	}
	for _, name := range []string{"lm", "NX", "PAGE1GB", "INVARIANT_TSC", "X2APIC", "HYPERVISOR"} {
		if _, ok := Feature(name); !ok {
			t.Errorf("feature %s missing", name)
		}
	}
	// airies-boot pmode.S checks CPUID.1:EDX bits 24 and 25 (FXSR, SSE).
	fs, ok := DecodeCPUID(1, 0, "edx", 1<<24|1<<25)
	if !ok || len(fs) != 2 || fs[0].Name != "FXSR" || fs[1].Name != "SSE" {
		t.Errorf("DecodeCPUID(1, edx) = %+v", fs)
	}
	fs, _ = DecodeCPUID(1, 0, "EAX", 0x000906EA)
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.Text)
	}
	if got := strings.Join(parts, " "); got != "STEPPING=0xa MODEL=0xe FAMILY=0x6 TYPE=original OEM EXT_MODEL=0x9" {
		t.Errorf("leaf 1 EAX = %q", got)
	}
	for _, r := range []string{"ebx", "ecx"} {
		if _, ok := DecodeCPUID(1, 0, r, 0); !ok {
			t.Errorf("DecodeCPUID(1, %s) failed", r)
		}
	}
	if _, ok := DecodeCPUID(1, 0, "esi", 0); ok {
		t.Error("esi decoded")
	}
	if _, ok := DecodeCPUID(0x99, 0, "eax", 0); ok {
		t.Error("unknown leaf decoded")
	}
}
