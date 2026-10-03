package hwdb

import (
	"fmt"
	"sort"
	"strings"
)

// CPUIDLeaf is one CPUID leaf (and subleaf, for leaves that take ECX).
type CPUIDLeaf struct {
	Leaf   Num      `json:"leaf"`
	Sub    *Num     `json:"sub,omitempty"`
	Name   string   `json:"name"`
	Vendor string   `json:"vendor,omitempty"`
	Detail string   `json:"detail,omitempty"`
	EAX    []Field  `json:"eax,omitempty"`
	EBX    []Field  `json:"ebx,omitempty"`
	ECX    []Field  `json:"ecx,omitempty"`
	EDX    []Field  `json:"edx,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

// Regs returns the output registers in eax, ebx, ecx, edx order.
func (l *CPUIDLeaf) Regs() [4][]Field {
	return [4][]Field{l.EAX, l.EBX, l.ECX, l.EDX}
}

// Key is "CPUID.(EAX=07h,ECX=0)" in the SDM's notation.
func (l *CPUIDLeaf) Key() string {
	if l.Sub != nil {
		return fmt.Sprintf("CPUID.(EAX=%02Xh,ECX=%d)", uint64(l.Leaf), uint64(*l.Sub))
	}
	return fmt.Sprintf("CPUID.%02Xh", uint64(l.Leaf))
}

// FeatureBit locates one CPUID feature flag.
type FeatureBit struct {
	Name    string
	Leaf    uint32
	Subleaf uint32
	HasSub  bool
	Reg     string // "eax", "ebx", "ecx", "edx"
	Bit     int
	Level   int // x86-64 psABI level that requires it, 0 = none
	Field   *Field
}

// Where is "CPUID.(EAX=07h,ECX=0):EBX[5]".
func (f FeatureBit) Where() string {
	if f.HasSub {
		return fmt.Sprintf("CPUID.(EAX=%02Xh,ECX=%d):%s[%d]", f.Leaf, f.Subleaf, strings.ToUpper(f.Reg), f.Bit)
	}
	return fmt.Sprintf("CPUID.%02Xh:%s[%d]", f.Leaf, strings.ToUpper(f.Reg), f.Bit)
}

type cpuidFile struct {
	Leaves []CPUIDLeaf `json:"leaves"`
}

type cpuidTable struct {
	leaves   []CPUIDLeaf
	byKey    map[[2]int64]*CPUIDLeaf // {leaf, subleaf or -1}
	byName   map[string]FeatureBit   // upper-case feature name
	isaExact map[string][]FeatureBit // upper-case ISA set
	isaPre   []isaPrefix             // "AVX512F_*" patterns
	byInsn   map[string]FeatureBit
}

type isaPrefix struct {
	prefix string
	bit    FeatureBit
}

func loadCPUID() (*cpuidTable, error) {
	var f cpuidFile
	if err := readJSON("cpuid.json", &f); err != nil {
		return emptyCPUID(), err
	}
	t, err := buildCPUID(f, "cpuid.json")
	if err != nil {
		return emptyCPUID(), err
	}
	return t, nil
}

func emptyCPUID() *cpuidTable {
	return &cpuidTable{byKey: map[[2]int64]*CPUIDLeaf{}, byName: map[string]FeatureBit{},
		isaExact: map[string][]FeatureBit{}, byInsn: map[string]FeatureBit{}}
}

func buildCPUID(f cpuidFile, file string) (*cpuidTable, error) {
	t := emptyCPUID()
	t.leaves = f.Leaves
	regNames := [4]string{"eax", "ebx", "ecx", "edx"}
	for i := range t.leaves {
		l := &t.leaves[i]
		path := fmt.Sprintf("%s: leaves[%d]", file, i)
		k := [2]int64{int64(l.Leaf), -1}
		if l.Sub != nil {
			k[1] = int64(*l.Sub)
		}
		if _, dup := t.byKey[k]; dup {
			return nil, fmt.Errorf("%s: duplicate %s", path, l.Key())
		}
		t.byKey[k] = l
		regs := [4]*[]Field{&l.EAX, &l.EBX, &l.ECX, &l.EDX}
		for ri, rp := range regs {
			if _, err := finishFields(*rp, 32, path+"."+regNames[ri]); err != nil {
				return nil, err
			}
			for fi := range *rp {
				fl := &(*rp)[fi]
				if fl.Width() != 1 {
					continue
				}
				fb := FeatureBit{Name: fl.Name, Leaf: uint32(l.Leaf), Reg: regNames[ri], Bit: fl.Lo, Level: fl.Level, Field: fl}
				if l.Sub != nil {
					fb.Subleaf, fb.HasSub = uint32(*l.Sub), true
				}
				up := strings.ToUpper(fl.Name)
				if _, dup := t.byName[up]; dup {
					return nil, fmt.Errorf("%s.%s: feature %s named twice", path, regNames[ri], fl.Name)
				}
				t.byName[up] = fb
				for _, s := range fl.ISA {
					us := strings.ToUpper(s)
					if p, ok := strings.CutSuffix(us, "*"); ok {
						t.isaPre = append(t.isaPre, isaPrefix{p, fb})
						continue
					}
					t.isaExact[us] = append(t.isaExact[us], fb)
				}
				for _, in := range fl.Insns {
					if _, dup := t.byInsn[in]; dup {
						return nil, fmt.Errorf("%s: instruction %s guarded twice", path, in)
					}
					t.byInsn[in] = fb
				}
			}
		}
	}
	// Longest prefix first, so AVX512_VBMI2_* is not taken by AVX512_VBMI_*.
	sort.SliceStable(t.isaPre, func(i, j int) bool { return len(t.isaPre[i].prefix) > len(t.isaPre[j].prefix) })
	return t, nil
}

// CPUID returns the leaf, or the leaf without a subleaf when the leaf has no
// subleaf entries.
func CPUID(leaf, subleaf uint64) (*CPUIDLeaf, bool) {
	t := cpuidTab()
	if l, ok := t.byKey[[2]int64{int64(leaf), int64(subleaf)}]; ok {
		return l, true
	}
	l, ok := t.byKey[[2]int64{int64(leaf), -1}]
	return l, ok
}

// CPUIDLeaves lists every leaf in table order.
func CPUIDLeaves() []*CPUIDLeaf {
	t := cpuidTab()
	out := make([]*CPUIDLeaf, len(t.leaves))
	for i := range t.leaves {
		out[i] = &t.leaves[i]
	}
	return out
}

// Feature finds a feature flag by name ("AVX2", "LM", "SSE4_1").
func Feature(name string) (FeatureBit, bool) {
	f, ok := cpuidTab().byName[strings.ToUpper(name)]
	return f, ok
}

// FeaturesForISASet returns the CPUID bits that guard an internal/isa ISA
// set. Most sets have one; a few need two (AVX_GFNI needs GFNI and AVX,
// AVXAES needs AES and AVX), in which case every bit is returned and all must
// be set. Sets that no single bit guards (I386, LONGMODE's string forms,
// PENTIUMREAL) return none: use FeatureForMnemonic for those.
func FeaturesForISASet(isaSet string) []FeatureBit {
	t := cpuidTab()
	s := strings.ToUpper(strings.TrimSpace(isaSet))
	if fs, ok := t.isaExact[s]; ok {
		return fs
	}
	for _, p := range t.isaPre {
		if strings.HasPrefix(s, p.prefix) {
			out := []FeatureBit{p.bit}
			// The 128- and 256-bit forms of an AVX-512 set also need VL.
			if strings.HasPrefix(s, "AVX512") && (strings.HasSuffix(s, "_128") || strings.HasSuffix(s, "_256") || strings.HasSuffix(s, "_128N")) {
				if vl, ok := t.byName["AVX512VL"]; ok {
					out = append(out, vl)
				}
			}
			return out
		}
	}
	return nil
}

// FeatureForMnemonic returns the CPUID bit guarding an instruction whose ISA
// set is shared with instructions guarded by other bits: rdtsc (TSC),
// rdmsr/wrmsr (MSR), cmpxchg8b (CX8), syscall (SYSCALL), cpuid itself has
// none (its own presence is tested through EFLAGS.ID).
func FeatureForMnemonic(mnemonic string) (FeatureBit, bool) {
	f, ok := cpuidTab().byInsn[strings.ToLower(mnemonic)]
	return f, ok
}

// DecodeCPUID names the set bits and fields of one output register.
func DecodeCPUID(leaf, subleaf uint64, reg string, value uint64) ([]FieldValue, bool) {
	l, ok := CPUID(leaf, subleaf)
	if !ok {
		return nil, false
	}
	var fs []Field
	switch strings.ToLower(reg) {
	case "eax":
		fs = l.EAX
	case "ebx":
		fs = l.EBX
	case "ecx":
		fs = l.ECX
	case "edx":
		fs = l.EDX
	default:
		return nil, false
	}
	return setFields(fs, value), true
}
