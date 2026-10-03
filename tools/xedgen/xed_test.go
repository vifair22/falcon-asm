package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testWidths is a trimmed copy of the XED width tables, enough for the
// operand fixtures below.
func testWidths(t *testing.T) map[string]width {
	t.Helper()
	const table = `
b      u8    1
w      i16   2
d      i32   4
q      i64   8
dq     i32  16
qq     i32  32
v      int   2 4 8
z      int   2 4 4
y      int   4 4 8
asz    int   2 4 8
p      struct 4 6 6
s      struct 6 6 10
mskw   i1   64bits
zd     i32  512bits
zu32   u32  512bits
vv     var   0
mem32real f32 4
mfpxenv struct 512
mem14  struct 14
`
	w := map[string]width{}
	if err := parseWidths(strings.NewReader(table), w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestParseWidths(t *testing.T) {
	w := testWidths(t)
	tests := []struct {
		name   string
		osz    int
		bits   int
		varies bool
	}{
		{"b", 64, 8, false},
		{"v", 16, 16, true},
		{"v", 32, 32, true},
		{"v", 64, 64, true},
		{"z", 64, 32, true},
		{"mskw", 32, 64, false},
		{"zd", 32, 512, false},
		{"vv", 32, 0, false},
	}
	for _, tc := range tests {
		got := w[tc.name]
		if b := got.bitsFor(tc.osz); b != tc.bits {
			t.Errorf("%s@%d = %d, want %d", tc.name, tc.osz, b, tc.bits)
		}
		if got.varies() != tc.varies {
			t.Errorf("%s varies = %v", tc.name, got.varies())
		}
	}
	if (width{}).bitsFor(32) != 0 {
		t.Error("empty width should be 0")
	}
	if err := parseWidths(strings.NewReader("x int abc\n"), map[string]width{}); err == nil {
		t.Error("expected error for bad byte width")
	}
	if err := parseWidths(strings.NewReader("x int zzbits\n"), map[string]width{}); err == nil {
		t.Error("expected error for bad bit width")
	}
}

func TestParseCfg(t *testing.T) {
	const cfg = `# comment
dec-instructions : foo-isa.txt
 add:dec-spine:spine.txt:2
widths:w.txt   # trailing
garbage
: nofile
`
	got, err := parseCfg(strings.NewReader(cfg), "d")
	if err != nil {
		t.Fatal(err)
	}
	want := []cfgEntry{
		{"dec-instructions", filepath.Join("d", "foo-isa.txt")},
		{"dec-spine", filepath.Join("d", "spine.txt")},
		{"widths", filepath.Join("d", "w.txt")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

const isaFixture = `#BEGIN_LEGAL
#END_LEGAL
INSTRUCTIONS()::

UDELETE: OLD_ONE
DELETE : GONE

{
ICLASS    : ADD
CPL       : 3
CATEGORY  : BINARY
EXTENSION : BASE
ISA_SET   : I86
FLAGS     : MUST [ of-mod sf-mod zf-mod af-mod pf-mod cf-mod ]
ATTRIBUTES: SCALABLE # a comment
PATTERN   : 0x00 MOD[mm] MOD!=3 REG[rrr] RM[nnn] MODRM()
OPERANDS  : MEM0:rw:b REG0=GPR8_R():r
IFORM     : ADD_MEMb_GPR8
PATTERN   : 0x00 MOD[0b11] MOD=3 REG[rrr] RM[nnn]
OPERANDS  : REG0=GPR8_B():rw \
            REG1=GPR8_R():r
}
{
ICLASS    : LGDT
CPL       : 0
CATEGORY  : SYSTEM
EXTENSION : BASE
UNAME     : OLD_ONE
PATTERN   : 0x0F 0x01 MOD[mm] MOD!=3 REG[0b010] RM[nnn] not64 MODRM()
OPERANDS  : MEM0:r:s
}
{
ICLASS    : GONE
CPL       : 3
CATEGORY  : MISC
EXTENSION : BASE
PATTERN   : 0x0F 0x0B
OPERANDS  :
}
{
ICLASS    : PDEP
CPL       : 3
CATEGORY  : BMI2
EXTENSION : BMI2
PATTERN   : VV1 0xF5 VF2 V0F38 W1 mode64 MOD[0b11] MOD=3 REG[rrr] RM[nnn]
OPERANDS  : REG0=VGPRy_R():w REG1=VGPRy_N():r REG2=VGPRy_B():r
}
`

func TestParseISA(t *testing.T) {
	pf, err := parseISA(strings.NewReader(isaFixture), "fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pf.UDeletes, []string{"OLD_ONE"}) || !reflect.DeepEqual(pf.Deletes, []string{"GONE"}) {
		t.Errorf("deletes: %v %v", pf.UDeletes, pf.Deletes)
	}
	if len(pf.Records) != 5 {
		t.Fatalf("got %d records, want 5 (two ADD patterns flattened)", len(pf.Records))
	}
	add := pf.Records[1]
	if add.IClass != "ADD" || add.Category != "BINARY" || add.ISASet != "I86" || add.CPL != 3 {
		t.Errorf("ADD header: %+v", add)
	}
	if !reflect.DeepEqual(add.Operands, []string{"REG0=GPR8_B():rw", "REG1=GPR8_R():r"}) {
		t.Errorf("continuation not joined: %q", add.Operands)
	}
	if pf.Records[0].IForm != "ADD_MEMb_GPR8" || !add.hasAttr("scalable") {
		t.Errorf("iform/attrs: %q %v", pf.Records[0].IForm, add.Attributes)
	}
	if !add.hasToken("MOD=3") || add.hasToken("MOD!=3") {
		t.Error("hasToken")
	}
	lgdt := pf.Records[2]
	if lgdt.CPL != 0 || lgdt.UName != "OLD_ONE" || lgdt.ISASet != "BASE" {
		t.Errorf("LGDT: %+v (ISA_SET should default to EXTENSION)", lgdt)
	}

	kept := applyDeletes(pf.Records, map[string]bool{"OLD_ONE": true}, map[string]bool{"GONE": true})
	var names []string
	for _, r := range kept {
		names = append(names, r.IClass)
	}
	if !reflect.DeepEqual(names, []string{"ADD", "ADD", "PDEP"}) {
		t.Errorf("after deletes: %v", names)
	}
}

func TestParseISAErrors(t *testing.T) {
	bad := map[string]string{
		"unterminated":       "{\nICLASS: X\nPATTERN: 0x90\n",
		"operands first":     "{\nICLASS: X\nOPERANDS: REG0=GPR8_R():r\n}\n",
		"bad cpl":            "{\nICLASS: X\nCPL: three\n}\n",
		"unterminated cont.": "{\nICLASS: X\nPATTERN: 0x90 \\",
	}
	for name, src := range bad {
		if _, err := parseISA(strings.NewReader(src), name); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// writeFixtureTree lays out a miniature XED datafiles directory: a root
// files.cfg plus one extension directory, so loadXED's walk, cfg parsing
// and cross-file deletes are exercised without the real clone.
func writeFixtureTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "datafiles")
	ext := filepath.Join(root, "ext")
	for _, d := range []string{root, ext} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "files.cfg"):          "dec-instructions : base-isa.txt\nenc-instructions : base-isa.txt\nwidths : w.txt\n",
		filepath.Join(root, "w.txt"):              "b u8 1\nv int 2 4 8\nz int 2 4 4\ns struct 6 6 10\n",
		filepath.Join(root, "base-isa.txt"):       isaFixture,
		filepath.Join(ext, "files.cfg"):           "dec-instructions: ext-isa.txt\n",
		filepath.Join(ext, "ext-isa.txt"):         "INSTRUCTIONS()::\nUDELETE: NORM_X\n{\nICLASS: POPCNT\nCPL: 3\nCATEGORY: SSE\nEXTENSION: SSE4\nISA_SET: POPCNT\nPATTERN: 0x0F 0xB8 f3_refining_prefix MOD[0b11] MOD=3 REG[rrr] RM[nnn]\nOPERANDS: REG0=GPRv_R():w REG1=GPRv_B():r\n}\n",
		filepath.Join(root, "not-referenced.txt"): "{\nICLASS: NEVER\n}\n",
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadXED(t *testing.T) {
	root := writeFixtureTree(t)
	db, err := loadXED(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range db.Records {
		names = append(names, r.IClass)
	}
	// LGDT carries UNAME OLD_ONE (deleted in base) and GONE is deleted by
	// iclass; the extension adds POPCNT after the base records.
	want := []string{"ADD", "ADD", "PDEP", "POPCNT"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("records %v, want %v", names, want)
	}
	if db.Widths["v"].bitsFor(64) != 64 {
		t.Error("widths not loaded")
	}
	if _, err := loadXED(filepath.Join(root, "missing")); err == nil {
		t.Error("expected error for missing directory")
	}
}
