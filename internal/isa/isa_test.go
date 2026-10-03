package isa

import (
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// fixtureXED stands in for the generated table so the resolution rules can
// be tested independently of whatever tools/xedgen has produced so far. It
// deliberately contains the names that make suffix stripping dangerous
// (setb, jb, set would be wrong; mov is the classic suffix base) and a
// collision with the system layer.
var fixtureXED = []Instruction{
	{Name: "MOV", Summary: "Move", CPL: 3, Forms: []Form{{Operands: "r/m32, r32", ISASet: "I86"}}},
	{Name: "setb", Summary: "Set Byte if Below", CPL: 3},
	{Name: "jb", Summary: "Jump if Below", CPL: 3, Aliases: []string{"jc", "jnae"}},
	{Name: "movsd", Summary: "Move Scalar Double", CPL: 3, Forms: []Form{{ISASet: "SSE2"}}},
	{Name: "in", Summary: "xed in, must be replaced", CPL: 3, Aliases: []string{"xedonlyalias"}},
	{Name: "inb", Summary: "xed name that a system alias shadows", CPL: 3},
	{Name: "vpaddd", Summary: "Add Packed Dword", CPL: 3, Forms: []Form{{ISASet: "AVX2"}, {ISASet: "AVX512F_512", Level: 4}}},
	{Name: "", Summary: "nameless entries are dropped"},
}

var fixtureSystem = []Instruction{
	{Name: "in", Summary: "Input from Port", CPL: 3, Aliases: []string{"INB", "inw", "inl"}},
	{Name: "iret", Summary: "Interrupt Return", CPL: 3, Aliases: []string{"iretw", "iretl", "iretd"}},
	{Name: "pushf", Summary: "Push FLAGS", CPL: 3, Aliases: []string{" "}}, // a blank alias must not make "" resolve
	{Name: "cwtl", Summary: "Convert Word to Long", CPL: 3},
	{Name: "movs", Summary: "Move String", CPL: 3, Aliases: []string{"movsb", "movsw", "movsl", "movsq"}},
	{Name: "lgdt", Summary: "Load GDTR", CPL: 0, Forms: []Form{{ISASet: "I286REAL"}}},
	{Name: "dup", Summary: "first", CPL: 3},
	{Name: "dup", Summary: "second wins", CPL: 3},
}

func TestIndexLookup(t *testing.T) {
	ix := buildIndex(fixtureXED, fixtureSystem)
	tests := []struct {
		word, name, suffix, summary string
		ok                          bool
	}{
		{word: "mov", name: "mov", suffix: ""},
		{word: "movw", name: "mov", suffix: "w"},
		{word: "MOVL", name: "mov", suffix: "l"},
		{word: "movq", name: "mov", suffix: "q"},
		{word: "movb", name: "mov", suffix: "b"},
		{word: "setb", name: "setb", suffix: ""},
		{word: "jb", name: "jb", suffix: ""},
		{word: "jc", name: "jb", suffix: ""},
		{word: "movsb", name: "movs", suffix: ""},
		{word: "movsl", name: "movs", suffix: ""},
		{word: "movsd", name: "movsd", suffix: ""},
		{word: "cwtl", name: "cwtl", suffix: ""},
		{word: "inb", name: "in", suffix: "", summary: "Input from Port"},
		{word: "in", name: "in", suffix: "", summary: "Input from Port"},
		{word: "iretl", name: "iret", suffix: ""},
		{word: "iretq", name: "iret", suffix: "q"},
		{word: "pushfl", name: "pushf", suffix: "l"},
		{word: "pushfw", name: "pushf", suffix: "w"},
		{word: "lgdtl", name: "lgdt", suffix: "l"},
		{word: "  LGDT ", name: "lgdt", suffix: ""},
		{word: "dup", name: "dup", summary: "second wins"},
		{word: "xedonlyalias", ok: false},
		{word: "movll", ok: false}, // only one suffix is ever stripped
		{word: "set", ok: false},
		{word: "b", ok: false},
		{word: "", ok: false},
		{word: "nosuch", ok: false},
	}
	for _, tt := range tests {
		if tt.name != "" {
			tt.ok = true
		}
		inst, suffix, ok := ix.lookup(tt.word)
		if ok != tt.ok {
			t.Errorf("lookup(%q) ok = %v, want %v", tt.word, ok, tt.ok)
			continue
		}
		if !ok {
			if inst != nil || suffix != "" {
				t.Errorf("lookup(%q) miss returned %v, %q", tt.word, inst, suffix)
			}
			continue
		}
		if inst.Name != tt.name || suffix != tt.suffix {
			t.Errorf("lookup(%q) = %q+%q, want %q+%q", tt.word, inst.Name, suffix, tt.name, tt.suffix)
		}
		if tt.summary != "" && inst.Summary != tt.summary {
			t.Errorf("lookup(%q) summary = %q, want %q", tt.word, inst.Summary, tt.summary)
		}
	}
}

func TestIndexLevelsFilled(t *testing.T) {
	ix := buildIndex(fixtureXED, fixtureSystem)
	inst, _, _ := ix.lookup("vpaddd")
	if got := []Level{inst.Forms[0].Level, inst.Forms[1].Level}; !reflect.DeepEqual(got, []Level{3, 4}) {
		t.Errorf("vpaddd levels = %v, want [3 4]", got)
	}
	// normalize must not write through to the caller's slice.
	if fixtureXED[6].Forms[0].Level != 0 {
		t.Error("buildIndex mutated its input")
	}
	if n := len(ix.all); n != 13 {
		t.Errorf("index holds %d instructions, want 13", n)
	}
}

func TestIndexSameLayerNameBeatsAlias(t *testing.T) {
	layer := []Instruction{
		{Name: "movs", Aliases: []string{"movsd"}},
		{Name: "movsd", Summary: "SSE2"},
	}
	for _, l := range [][]Instruction{layer, {layer[1], layer[0]}} {
		inst, _, _ := buildIndex(l).lookup("movsd")
		if inst == nil || inst.Summary != "SSE2" {
			t.Errorf("movsd resolved to %+v, want the SSE2 name", inst)
		}
	}
}

func TestEmbeddedData(t *testing.T) {
	if err := DataError(); err != nil {
		t.Fatalf("embedded data: %v", err)
	}
	// Resolution is checked against system.json alone: once xed.json is
	// generated it may legitimately add exact spellings (pushfq, say) that
	// change which rows match by suffix, and that is not this test's concern.
	sys, err := readTable(dataFS, systemFile)
	if err != nil {
		t.Fatal(err)
	}
	ix := buildIndex(sys)
	tests := []struct{ word, name, suffix string }{
		{"inb", "in", ""},
		{"outb", "out", ""},
		{"outl", "out", ""},
		{"iretl", "iret", ""},
		{"iretq", "iretq", ""},
		{"pushfl", "pushf", "l"},
		{"popfw", "popf", "w"},
		{"pushal", "pusha", "l"},
		{"ljmpl", "ljmp", "l"},
		{"lgdtl", "lgdt", "l"},
		{"sgdtl", "sgdt", "l"},
		{"lret", "lret", ""},
		{"retf", "lret", ""},
		{"lodsb", "lods", ""},
		{"stosw", "stos", ""},
		{"cwtl", "cwtl", ""},
		{"cwde", "cwtl", ""},
		{"cltq", "cltq", ""},
		{"movzbl", "movzbl", ""},
		{"movslq", "movslq", ""},
		{"scasb", "scas", ""},
		{"CLI", "cli", ""},
		{"fwait", "wait", ""},
	}
	for _, tt := range tests {
		inst, suffix, ok := ix.lookup(tt.word)
		if !ok || inst.Name != tt.name || suffix != tt.suffix {
			t.Errorf("Lookup(%q) = %v+%q (%v), want %q+%q", tt.word, inst, suffix, ok, tt.name, tt.suffix)
		}
	}
	// System names and aliases outrank anything generated.
	for _, w := range []string{"lgdt", "inb", "movsb", "iretl", "cwtl"} {
		want, _, _ := ix.lookup(w)
		if got, _, _ := Lookup(w); got == nil || got.Name != want.Name {
			t.Errorf("Lookup(%q) = %v, want the system entry %q", w, got, want.Name)
		}
	}
	if inst, _, _ := Lookup("lgdt"); inst.CPL != 0 || inst.Forms[0].Level != 1 {
		t.Errorf("lgdt CPL/level = %d/%d, want 0/1", inst.CPL, inst.Forms[0].Level)
	}
}

// TestSystemJSONValid holds the hand-written table to the rules the rest of
// the server relies on.
func TestSystemJSONValid(t *testing.T) {
	raw, err := fs.ReadFile(dataFS, systemFile)
	if err != nil {
		t.Fatal(err)
	}
	if i := strings.IndexFunc(string(raw), func(r rune) bool { return r > 0x7e || (r < 0x20 && r != '\n') }); i >= 0 {
		t.Errorf("system.json has a non-ASCII or control byte at offset %d", i)
	}
	var insts []Instruction
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&insts); err != nil {
		t.Fatal(err)
	}
	validModes := map[string]bool{"": true, "16": true, "32": true, "64": true, "16,32": true, "32,64": true}
	seen := map[string]string{}
	claim := func(key, owner string) {
		if key != strings.ToLower(key) || strings.TrimSpace(key) != key || key == "" {
			t.Errorf("%s: key %q must be non-empty, lowercase and trimmed", owner, key)
		}
		if prev, dup := seen[key]; dup {
			t.Errorf("%q claimed by both %s and %s", key, prev, owner)
		}
		seen[key] = owner
	}
	for _, in := range insts {
		if in.Name == "" || in.Summary == "" || in.Category == "" {
			t.Errorf("entry %+v lacks name, summary or category", in)
		}
		claim(in.Name, in.Name)
		for _, a := range in.Aliases {
			claim(a, in.Name)
		}
		if in.CPL != 0 && in.CPL != 3 {
			t.Errorf("%s: CPL %d", in.Name, in.CPL)
		}
		if !validModes[in.Modes] {
			t.Errorf("%s: modes %q", in.Name, in.Modes)
		}
		if len(in.Forms) == 0 {
			t.Errorf("%s: no forms", in.Name)
		}
		for _, f := range in.Forms {
			if f.ISASet == "" {
				t.Errorf("%s: form %q has no ISA set", in.Name, f.Operands)
			}
			if f.Level != 0 {
				t.Errorf("%s: level is derived from the ISA set and should not be written", in.Name)
			}
		}
		if IsPrefix(in.Name) {
			t.Errorf("%s is a prefix, not an instruction", in.Name)
		}
	}
	// The privileged set this BIOS code depends on being flagged.
	for _, name := range []string{"hlt", "lgdt", "lidt", "lldt", "ltr", "lmsw", "clts", "invd", "wbinvd",
		"invlpg", "invpcid", "rdmsr", "wrmsr", "swapgs", "sysret", "sysexit", "xsetbv", "rsm"} {
		if inst, _, ok := Lookup(name); !ok || inst.CPL != 0 {
			t.Errorf("%s should be CPL 0", name)
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in := []Instruction{{
		Name: "lgdt", Summary: "Load GDTR", Category: "SYSTEM", CPL: 0, Modes: "16,32",
		FlagsRead: "CF", FlagsWritten: "ZF", FlagsUndefined: "OF",
		Forms:   []Form{{Operands: "m16&32", ISASet: "I286REAL", Level: 1}},
		Aliases: []string{"lgdtx"}, Notes: "note",
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// CPL 0 is meaningful and must survive omitempty-style trimming.
	if !strings.Contains(string(raw), `"cpl":0`) {
		t.Errorf("cpl 0 not serialized: %s", raw)
	}
	var out []Instruction
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip:\n got %+v\nwant %+v", out, in)
	}
}

func TestReadTable(t *testing.T) {
	fsys := fstest.MapFS{
		"good.json": {Data: []byte(`[{"name":"x","summary":"y","cpl":3}]`)},
		"bad.json":  {Data: []byte(`{not json`)},
		"dir.json":  {Mode: fs.ModeDir},
	}
	got, err := readTable(fsys, "good.json")
	if err != nil || len(got) != 1 || got[0].Name != "x" {
		t.Errorf("good: %v, %v", got, err)
	}
	if got, err := readTable(fsys, "missing.json"); err != nil || got != nil {
		t.Errorf("missing: %v, %v; want nil, nil", got, err)
	}
	if _, err := readTable(fsys, "bad.json"); err == nil || !strings.Contains(err.Error(), "parse bad.json") {
		t.Errorf("bad: err = %v", err)
	}
	if _, err := readTable(fsys, "dir.json"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dir: err = %v, want a read error", err)
	}
}

func TestIsPrefix(t *testing.T) {
	yes := []string{"rep", "REPZ", "repne", "lock", "addr32", "data16", "cs", "es", "rex64",
		"rex.w", "rex.WRXB", "rex", "{vex3}", "notrack"}
	no := []string{"", "mov", "rex.", "rex.wz", "repx", "movsb", "%es"}
	for _, w := range yes {
		if !IsPrefix(w) {
			t.Errorf("IsPrefix(%q) = false", w)
		}
	}
	for _, w := range no {
		if IsPrefix(w) {
			t.Errorf("IsPrefix(%q) = true", w)
		}
	}
}
