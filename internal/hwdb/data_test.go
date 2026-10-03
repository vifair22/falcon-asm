package hwdb

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

func TestDataLoads(t *testing.T) {
	if err := DataError(); err != nil {
		t.Fatal(err)
	}
}

// The tables are hand-written; the project rule is pure ASCII everywhere.
func TestDataPureASCII(t *testing.T) {
	err := fs.WalkDir(dataFS, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := dataFS.ReadFile(path)
		if err != nil {
			return err
		}
		for i, b := range raw {
			if b >= 0x80 || (b < 0x20 && b != '\n' && b != '\t' && b != '\r') {
				line := strings.Count(string(raw[:i]), "\n") + 1
				t.Errorf("%s:%d: non-ASCII or control byte %#x", path, line, b)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDataSizes(t *testing.T) {
	d := Default()
	pt := d.portTab()
	mt := d.msrTab()
	bt := d.biosTab()
	ct := cpuidTab()
	nFeat := 0
	for _, l := range ct.leaves {
		for _, fs := range l.Regs() {
			nFeat += len(fs)
		}
	}
	t.Logf("ports: %d entries + %d blocks, %d port numbers; msrs: %d exact + %d ranges; bios: %d services, %d structs; cpuid: %d leaves, %d fields (%d named feature bits); profiles: %d + %d aliases; helpers: %d",
		len(pt.file.Ports), len(pt.file.Blocks), len(pt.byNum), len(mt.exact), len(mt.ranges),
		len(bt.byKey), len(bt.structs), len(ct.leaves), nFeat, len(ct.byName), len(Profiles()), len(ProfileAliases()), len(d.Helpers()))
	if len(pt.byNum) < 100 || len(mt.exact) < 50 || len(bt.byKey) < 70 || len(ct.byName) < 150 {
		t.Error("a table is smaller than expected; did a file fail to load?")
	}
}

// Reserved masks must never overlap a defined field: a bit cannot be both
// documented and must-be-zero. The builders enforce it; this test states
// the invariant directly on the loaded data.
func TestReservedMasksDisjoint(t *testing.T) {
	d := Default()
	check := func(name string, fs []Field, res uint64) {
		for i := range fs {
			if fs[i].Mask()&res != 0 {
				t.Errorf("%s: field %s overlaps reserved mask %#x", name, fs[i].Name, res)
			}
		}
	}
	for _, m := range d.msrTab().exact {
		check(m.Name, m.Fields, uint64(m.Reserved))
	}
	for _, m := range d.msrTab().ranges {
		check(m.Name, m.Fields, uint64(m.Reserved))
	}
	for _, r := range regTab().byName {
		check(r.Name, r.Fields, uint64(r.Reserved))
	}
}

func TestBitsInsideWidth(t *testing.T) {
	for _, l := range cpuidTab().leaves {
		for _, fs := range l.Regs() {
			for _, f := range fs {
				if f.Hi > 31 {
					t.Errorf("%s %s: bit %d beyond 32", l.Key(), f.Name, f.Hi)
				}
			}
		}
	}
	for _, r := range regTab().byName {
		for _, f := range r.Fields {
			if f.Hi >= r.Width {
				t.Errorf("%s %s: bit %d beyond width %d", r.Name, f.Name, f.Hi, r.Width)
			}
		}
	}
}

func TestParseBits(t *testing.T) {
	tests := []struct {
		in     string
		lo, hi int
		bad    bool
	}{
		{"0", 0, 0, false},
		{"63:0", 0, 63, false},
		{" 7 : 4 ", 4, 7, false},
		{"4:7", 0, 0, true},
		{"x", 0, 0, true},
		{"3:y", 0, 0, true},
	}
	for _, tt := range tests {
		lo, hi, err := parseBits(tt.in)
		if (err != nil) != tt.bad || (!tt.bad && (lo != tt.lo || hi != tt.hi)) {
			t.Errorf("parseBits(%q) = %d,%d,%v", tt.in, lo, hi, err)
		}
	}
}

func TestNumJSON(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
		bad  bool
	}{
		{`"0x3F8"`, 0x3F8, false},
		{`1016`, 1016, false},
		{`"0b101"`, 5, false},
		{`"0xFFFF_FFFF"`, 0xFFFFFFFF, false},
		{`"zz"`, 0, true},
		{`-1`, 0, true},
	}
	for _, tt := range tests {
		var n Num
		err := json.Unmarshal([]byte(tt.in), &n)
		if (err != nil) != tt.bad || (!tt.bad && uint64(n) != tt.want) {
			t.Errorf("Num(%s) = %#x, %v", tt.in, uint64(n), err)
		}
	}
}

func TestFieldHelpers(t *testing.T) {
	f := Field{Name: "X", Bits: "63:0"}
	if _, err := finishFields([]Field{f}, 64, "t"); err != nil {
		t.Fatal(err)
	}
	fs := []Field{{Name: "ALL", Bits: "63:0"}}
	if _, err := finishFields(fs, 64, "t"); err != nil {
		t.Fatal(err)
	}
	if fs[0].Mask() != ^uint64(0) || fs[0].Get(0x1234) != 0x1234 || fs[0].Width() != 64 {
		t.Error("full-width field helpers")
	}
	if widthMask(64) != ^uint64(0) || widthMask(8) != 0xFF {
		t.Error("widthMask")
	}
}

// Every builder rejects the mistakes a hand-written table is prone to; the
// error names the JSON path.
func TestFinishFieldsErrors(t *testing.T) {
	tests := []struct {
		name string
		fs   []Field
		want string
	}{
		{"empty name", []Field{{Bits: "0"}}, "name: empty"},
		{"bad bits", []Field{{Name: "A", Bits: "q"}}, "bad bit"},
		{"outside", []Field{{Name: "A", Bits: "8"}}, "outside a 8-bit"},
		{"overlap", []Field{{Name: "A", Bits: "3:0"}, {Name: "B", Bits: "2"}}, "overlaps"},
		{"value too wide", []Field{{Name: "A", Bits: "1:0", Values: []Enum{{V: 4, Name: "x"}}}}, "does not fit"},
	}
	for _, tt := range tests {
		_, err := finishFields(tt.fs, 8, "x.fields")
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "x.fields[") {
			t.Errorf("%s: err = %v, want %q with path", tt.name, err, tt.want)
		}
	}
	if err := resolveEnums([]Field{{Name: "A", Bits: "0", Enum: "nope"}}, nil, "p"); err == nil {
		t.Error("unknown enum accepted")
	}
}

func TestBuildPortsErrors(t *testing.T) {
	tests := []struct {
		name string
		f    portFile
		want string
	}{
		{"dup", portFile{Ports: []Port{{Port: 1, Name: "a"}, {Port: 1, Name: "b"}}}, "already defined"},
		{"rw vs r", portFile{Ports: []Port{{Port: 1, Name: "a"}, {Port: 1, Name: "b", Access: "r"}}}, "overlaps"},
		{"no name", portFile{Ports: []Port{{Port: 1}}}, "name: empty"},
		{"last before", portFile{Ports: []Port{{Port: 5, Last: 4, Name: "a"}}}, "before port"},
		{"too high", portFile{Ports: []Port{{Port: 0x10000, Name: "a"}}}, "64 KiB"},
		{"access", portFile{Ports: []Port{{Port: 1, Name: "a", Access: "x"}}}, "access"},
		{"width", portFile{Ports: []Port{{Port: 1, Name: "a", Widths: []int{12}}}}, "widths"},
		{"dup value", portFile{Ports: []Port{{Port: 1, Name: "a", Values: []Enum{{V: 1}, {V: 1}}}}}, "duplicate value"},
		{"wide value", portFile{Ports: []Port{{Port: 1, Name: "a", Values: []Enum{{V: 0x100}}}}}, "wider"},
		{"bad field", portFile{Ports: []Port{{Port: 1, Name: "a", Fields: []Field{{Name: "x", Bits: "9"}}}}}, "outside"},
		{"big range", portFile{Ports: []Port{{Port: 0, Last: 100, Name: "a"}}}, "use a block"},
		{"block span", portFile{Blocks: []PortBlock{{Name: "b"}}}, "span"},
		{"block offset", portFile{Blocks: []PortBlock{{Name: "b", Span: 2, Bases: []PortBase{{Base: 0x10}}, Regs: []Port{{Port: 2, Name: "r"}}}}}, "outside span"},
		{"block reg", portFile{Blocks: []PortBlock{{Name: "b", Span: 2, Bases: []PortBase{{Base: 0x10}}, Regs: []Port{{Port: 1}}}}}, "name: empty"},
		{"block clash", portFile{Ports: []Port{{Port: 0x11, Name: "a"}}, Blocks: []PortBlock{{Name: "b", Span: 2, Bases: []PortBase{{Base: 0x10}}, Regs: []Port{{Port: 1, Name: "r"}}}}}, "already defined"},
	}
	for _, tt := range tests {
		_, err := buildPorts(tt.f, "t")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestBuildMSRsErrors(t *testing.T) {
	tests := []struct {
		name string
		list []MSR
		want string
	}{
		{"dup", []MSR{{Index: 1, Name: "a"}, {Index: 1, Name: "b"}}, "already defined"},
		{"no name", []MSR{{Index: 1}}, "name: empty"},
		{"wide index", []MSR{{Index: 1 << 33, Name: "a"}}, "32-bit"},
		{"last", []MSR{{Index: 5, Last: 4, Name: "a"}}, "before index"},
		{"access", []MSR{{Index: 1, Name: "a", Access: "rw"}}, "access"},
		{"reserved overlap", []MSR{{Index: 1, Name: "a", Fields: []Field{{Name: "X", Bits: "0"}}, Reserved: 1}}, "overlaps defined"},
		{"like missing", []MSR{{Index: 1, Name: "a", Like: 9}}, "like"},
		{"bad enum", []MSR{{Index: 1, Name: "a", Fields: []Field{{Name: "X", Bits: "0", Enum: "q"}}}}, "unknown value list"},
		{"range clash", []MSR{{Index: 0x10, Last: 0x1F, Name: "a"}, {Index: 0x10, Last: 0x1F, Name: "b"}}, "range overlaps"},
		{"range partial", []MSR{{Index: 0x10, Last: 0x1F, Name: "a"}, {Index: 0x18, Last: 0x2F, Name: "b"}}, "range overlaps"},
	}
	for _, tt := range tests {
		_, err := buildMSRs(tt.list, nil, "t")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
	// Interleaved stride-2 runs and a nested run are fine.
	ok := []MSR{
		{Index: 0x200, Last: 0x20E, Stride: 2, Name: "B{n}"},
		{Index: 0x201, Last: 0x20F, Stride: 2, Name: "M{n}"},
		{Index: 0x800, Last: 0x8FF, Name: "x2"},
		{Index: 0x810, Last: 0x817, Name: "isr{n}"},
	}
	if _, err := buildMSRs(ok, nil, "t"); err != nil {
		t.Error(err)
	}
}

func TestBuildBIOSErrors(t *testing.T) {
	n := func(v uint64) *Num { x := Num(v); return &x }
	base := func() biosFile {
		return biosFile{Status: map[string][]Enum{"s": {{V: 0}}}, Structs: []BIOSStruct{{Name: "st", Sizes: []int{4}, Fields: []StructField{{Off: 0, Size: 4, Name: "a"}}}}}
	}
	tests := []struct {
		name string
		mod  func(f *biosFile)
		want string
	}{
		{"no name", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x10}} }, "name: empty"},
		{"wide vec", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x100, Name: "x"}} }, "bytes"},
		{"al no ah", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x10, AL: n(1), Name: "x"}} }, "without ah"},
		{"err", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x10, Name: "x", Err: "maybe"}} }, "convention"},
		{"status", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x10, Name: "x", Status: "q"}} }, "status table"},
		{"struct", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x10, Name: "x", Struct: []string{"q"}}} }, "unknown structure"},
		{"clobber", func(f *biosFile) { f.Services = []BIOSService{{Int: 0x10, Name: "x", Clobbers: []string{"zz"}}} }, "not a register"},
		{"field reg", func(f *biosFile) {
			f.Services = []BIOSService{{Int: 0x10, Name: "x", Fields: []Field{{Name: "A", Bits: "0"}}}}
		}, "need the register"},
		{"field bits", func(f *biosFile) {
			f.Services = []BIOSService{{Int: 0x10, Name: "x", Fields: []Field{{Name: "A", Bits: "40", Reg: "AX"}}}}
		}, "outside"},
		{"dup service", func(f *biosFile) {
			f.Services = []BIOSService{{Int: 0x10, AH: n(1), Name: "x"}, {Int: 0x10, AH: n(1), Name: "y"}}
		}, "already defined"},
		{"dup vector", func(f *biosFile) { f.Vectors = []BIOSVector{{Int: 1}, {Int: 1}} }, "duplicate INT"},
		{"dup status", func(f *biosFile) { f.Status["s"] = []Enum{{V: 1}, {V: 1}} }, "duplicate code"},
		{"dup struct", func(f *biosFile) { f.Structs = append(f.Structs, f.Structs[0]) }, "duplicate"},
		{"struct sizes", func(f *biosFile) { f.Structs[0].Sizes = nil }, "needs name and sizes"},
		{"struct overlap", func(f *biosFile) {
			f.Structs[0].Fields = []StructField{{Off: 0, Size: 2, Name: "a"}, {Off: 1, Size: 1, Name: "b"}}
		}, "overlaps"},
		{"struct past", func(f *biosFile) { f.Structs[0].Fields = []StructField{{Off: 2, Size: 4, Name: "a"}} }, "past"},
		{"struct const", func(f *biosFile) {
			f.Structs[0].Fields = []StructField{{Off: 0, Size: 1, Name: "a", Const: []Num{0x100}}}
		}, "does not fit"},
	}
	for _, tt := range tests {
		f := base()
		tt.mod(&f)
		_, err := buildBIOS(f, "t")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestBuildCPUIDErrors(t *testing.T) {
	tests := []struct {
		name string
		f    cpuidFile
		want string
	}{
		{"dup leaf", cpuidFile{Leaves: []CPUIDLeaf{{Leaf: 1, Name: "a"}, {Leaf: 1, Name: "b"}}}, "duplicate"},
		{"dup feature", cpuidFile{Leaves: []CPUIDLeaf{{Leaf: 1, Name: "a", EAX: []Field{{Name: "X", Bits: "0"}}, EBX: []Field{{Name: "X", Bits: "0"}}}}}, "named twice"},
		{"dup insn", cpuidFile{Leaves: []CPUIDLeaf{{Leaf: 1, Name: "a", EAX: []Field{{Name: "X", Bits: "0", Insns: []string{"i"}}, {Name: "Y", Bits: "1", Insns: []string{"i"}}}}}}, "guarded twice"},
		{"bad field", cpuidFile{Leaves: []CPUIDLeaf{{Leaf: 1, Name: "a", EDX: []Field{{Name: "X", Bits: "32"}}}}}, "outside"},
	}
	for _, tt := range tests {
		_, err := buildCPUID(tt.f, "t")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestBuildProfilesErrors(t *testing.T) {
	tests := []struct {
		name string
		f    profileFile
		want string
	}{
		{"no name", profileFile{Profiles: []Profile{{}}}, "name: empty"},
		{"dup", profileFile{Profiles: []Profile{{Name: "a"}, {Name: "a"}}}, "duplicate profile"},
		{"base later", profileFile{Profiles: []Profile{{Name: "a", Base: "b"}, {Name: "b"}}}, "not defined above"},
		{"dup add", profileFile{Profiles: []Profile{{Name: "a", Add: []string{"X", "x"}}}}, "already present"},
		{"dup prefix", profileFile{Profiles: []Profile{{Name: "a", Add: []string{"P_*"}}, {Name: "b", Base: "a", Add: []string{"P_*"}}}}, "already inherited"},
		{"alias target", profileFile{Profiles: []Profile{{Name: "a"}}, Aliases: map[string]string{"x": "q"}}, "unknown profile"},
		{"alias shadow", profileFile{Profiles: []Profile{{Name: "a"}, {Name: "b"}}, Aliases: map[string]string{"a": "b"}}, "shadows"},
	}
	for _, tt := range tests {
		_, err := buildProfiles(tt.f, "t")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestBuildRegsErrors(t *testing.T) {
	tests := []struct {
		name string
		f    regFile
		want string
	}{
		{"width", regFile{Regs: []RegLayout{{Name: "a", Width: 0}}}, "width"},
		{"reserved", regFile{Regs: []RegLayout{{Name: "a", Width: 8, Reserved: 1, Fields: []Field{{Name: "X", Bits: "0"}}}}}, "overlaps"},
		{"dup", regFile{Regs: []RegLayout{{Name: "a", Width: 8}, {Name: "a", Width: 8}}}, "duplicate"},
		{"field", regFile{Regs: []RegLayout{{Name: "a", Width: 8, Fields: []Field{{Name: "X", Bits: "8"}}}}}, "outside"},
	}
	for _, tt := range tests {
		_, err := buildRegs(tt.f, "t")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestReadJSONErrors(t *testing.T) {
	var v struct{}
	if err := readJSON("missing.json", &v); err == nil {
		t.Error("missing file accepted")
	}
	var p portFile
	if err := decodeStrict([]byte(`{"ports":[{"port":1,"name":"a","bogus":1}]}`), &p, "x"); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("unknown field: %v", err)
	}
}

// tables mirrors lint.Tables; the check lives here rather than importing
// internal/lint, which depends on packages being built alongside this one.
type tables interface {
	Port(port uint64) (name, detail string, ok bool)
	MSR(index uint64) (name, detail string, ok bool)
	BIOS(vector, ah uint64) (name, detail string, ok bool)
}

var _ tables = (*DB)(nil)
