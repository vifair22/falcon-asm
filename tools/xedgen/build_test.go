package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/isa"
)

func TestCanonicalName(t *testing.T) {
	tests := []struct {
		r    *xedRecord
		want string
		ok   bool
	}{
		{rec("ADD", "I86", "", ""), "ADD", true},
		{rec("ADD_LOCK", "I86", "", ""), "", false},
		{rec("REP_MOVSB", "I86", "", ""), "", false},
		{rec("REPNE_SCASB", "I86", "", ""), "", false},
		{rec("REP_XSTORE", "VIA_PADLOCK_RNG", "", ""), "XSTORE", true},
		{rec("MOVSD_XMM", "SSE2", "", ""), "MOVSD", true},
		{rec("MOVSD", "I386", "", ""), "MOVSL", true},
		{rec("JMP_FAR", "I86", "", ""), "JMP", true},
		{rec("SYSRET64", "LONGMODE", "", ""), "SYSRET", true},
		{rec("PREFETCH_RESERVED", "PREFETCH_NOP", "", ""), "", false},
		{rec("SHL", "I86", "", "", "UNDOCUMENTED"), "", false},
		{rec("FFREEP", "X87", "", "", "UNDOCUMENTED"), "FFREEP", true},
		{rec("", "I86", "", ""), "", false},
	}
	for _, tc := range tests {
		got, ok := canonicalName(tc.r)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q,%v want %q,%v", tc.r.IClass, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseFlags(t *testing.T) {
	r, w, u := map[string]bool{}, map[string]bool{}, map[string]bool{}
	parseFlags("IMM1 MUST [ OF-MOD CF-TST CF-MOD ], IMMx MUST [ of-u fc1-mod fc2-u ]", r, w, u)
	parseFlags("READONLY [ iopl-tst junk ]", r, w, u)
	parseFlags("MUST [ unterminated", r, w, u)
	if got := joinFlags(r); got != "CF IOPL" {
		t.Errorf("read %q", got)
	}
	if got := joinFlags(w); got != "CF OF C1" {
		t.Errorf("written %q", got)
	}
	if got := joinFlags(u); got != "OF C2" {
		t.Errorf("undef %q", got)
	}
}

func TestModesString(t *testing.T) {
	tests := map[int]string{7: "", 0: "", 4: "64", 3: "16,32", 1: "16", 6: "32,64"}
	for in, want := range tests {
		if got := modesString(in); got != want {
			t.Errorf("modesString(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestLevelForISASet pins the level mapping the generated data depends on.
// The mapping itself lives in internal/isa; this guards against a change
// there silently relabelling the forms the hover reports.
func TestLevelForISASet(t *testing.T) {
	tests := map[string]isa.Level{
		"I86": 1, "SSE2": 1, "LONGMODE": 1, "POPCNT": 2, "SSE42": 2, "LAHF": 2,
		"BMI2": 3, "AVX2": 3, "FMA": 3, "MOVBE": 3, "LZCNT": 3,
		"AVX512F_512": 4, "AVX512BW_KOPD": 4, "AVX512DQ_SCALAR": 4, "AVX512F_128N": 4,
		"AVX512_VNNI_128": 0, "AES": 0, "VTX": 0, "AMX_TILE": 0, "SHA": 0, "": 0,
	}
	for set, want := range tests {
		if got := isa.LevelForISASet(set); got != want {
			t.Errorf("LevelForISASet(%q) = %d, want %d", set, got, want)
		}
	}
}

func TestBuildFormsFusion(t *testing.T) {
	w := testWidths(t)
	recs := []*xedRecord{
		// MOV 88 /r and 8A /r: two encodings of r8,r8, each with its own
		// memory form. Keyed fusion must pair each reg form with the memory
		// form of the same opcode.
		rec("MOV", "I86", "0x88 MOD[mm] MOD!=3 REG[rrr] RM[nnn] MODRM()", "MEM0:w:b REG0=GPR8_R():r"),
		rec("MOV", "I86", "0x88 MOD[0b11] MOD=3 REG[rrr] RM[nnn]", "REG0=GPR8_B():w REG1=GPR8_R():r"),
		rec("MOV", "I86", "0x8A MOD[mm] MOD!=3 REG[rrr] RM[nnn] MODRM()", "REG0=GPR8_R():w MEM0:r:b"),
		rec("MOV", "I86", "0x8A MOD[0b11] MOD=3 REG[rrr] RM[nnn]", "REG0=GPR8_R():w REG1=GPR8_B():r"),
	}
	got := buildForms(recs, w)
	want := []Form{
		{Operands: "r8, r/m8", ISASet: "I86", Level: 1},
		{Operands: "r/m8, r8", ISASet: "I86", Level: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mov: got %+v", got)
	}

	// MOVZX-style: memory and register variants are separate encodings
	// with different mode constraints; the keyless pass fuses them.
	recs = []*xedRecord{
		rec("MOVZX", "I386", "0x0F 0xB7 MOD[mm] MOD!=3 mode64 norexw_prefix", "REG0=GPR32_R():w MEM0:r:w"),
		rec("MOVZX", "I386", "0x0F 0xB7 MOD[0b11] MOD=3", "REG0=GPR32_R():w REG1=GPR16_B():r"),
	}
	if got := buildForms(recs, w); len(got) != 1 || got[0].Operands != "r/m16, r32" {
		t.Errorf("movzx: got %+v", got)
	}

	// EVEX: reg form + memory form with an explicit broadcast variant
	// become one "zmm/m512/m32bcst" form; the store-direction masked form
	// keeps the register's decoration.
	recs = []*xedRecord{
		rec("VADDPS", "AVX512F_512", "EVV 0x58 MOD[0b11] MOD=3 BCRC=0 VL512", "REG0=ZMM_R3():w REG1=MASK1():r:mskw:TXT=ZEROSTR REG2=ZMM_N3():r REG3=ZMM_B3():r"),
		rec("VADDPS", "AVX512F_512", "EVV 0x58 MOD[mm] MOD!=3 BCRC=0 VL512 NELEM_FULLMEM()", "REG0=ZMM_R3():w REG1=MASK1():r:mskw:TXT=ZEROSTR REG2=ZMM_N3():r MEM0:r:vv:f32"),
		rec("VADDPS", "AVX512F_512", "EVV 0x58 MOD[mm] MOD!=3 BCRC=1 VL512 ESIZE_32_BITS() NELEM_FULL()", "REG0=ZMM_R3():w REG1=MASK1():r:mskw:TXT=ZEROSTR REG2=ZMM_N3():r MEM0:r:vv:f32"),
		rec("VMOVAPS", "AVX512F_512", "EVV 0x29 MOD[0b11] MOD=3 VL512", "REG0=ZMM_B3():w REG1=MASK1():r:mskw:TXT=ZEROSTR REG2=ZMM_R3():r"),
		rec("VMOVAPS", "AVX512F_512", "EVV 0x29 MOD[mm] MOD!=3 VL512 NELEM_FULLMEM()", "MEM0:w:vv:f32 REG0=MASK1():r:mskw REG1=ZMM_R3():r"),
	}
	got = buildForms(recs, w)
	var ops []string
	for _, f := range got {
		ops = append(ops, f.Operands)
	}
	wantOps := []string{"zmm/m512/m32bcst, zmm, zmm{k}{z}", "zmm, zmm/m512{k}{z}"}
	if !reflect.DeepEqual(ops, wantOps) {
		t.Errorf("evex: got %q, want %q", ops, wantOps)
	}

	// A register-only second encoding is subsumed by the fused form.
	recs = []*xedRecord{
		rec("PEXTRW", "SSE4", "0x0F 0x3A 0x15 MOD[mm] MOD!=3", "MEM0:w:w REG0=XMM_R():r IMM0:r:b"),
		rec("PEXTRW", "SSE4", "0x0F 0x3A 0x15 MOD[0b11] MOD=3", "REG0=GPR32_B():w REG1=XMM_R():r IMM0:r:b"),
		rec("PEXTRW", "SSE4", "0x0F 0xC5 MOD[0b11] MOD=3", "REG0=GPR32_R():w REG1=XMM_B():r IMM0:r:b"),
	}
	if got := buildForms(recs, w); len(got) != 1 || got[0].Operands != "imm8, xmm, r32/m16" {
		t.Errorf("pextrw: got %+v", got)
	}
}

func TestComponents(t *testing.T) {
	got := components("r/m8")
	if !reflect.DeepEqual(got, map[string]bool{"r8": true, "m8": true}) {
		t.Errorf("r/m8: %v", got)
	}
	if !subset(components("m16"), components("r32/m16")) || subset(components("r16"), components("r32/m16")) {
		t.Error("subset")
	}
}

const xmlFixture = `<?xml version='1.0' encoding='utf-8'?>
<InstructionSet name="x86-64">
  <Instruction name="CWDE" summary="Convert Word to Doubleword">
    <InstructionForm gas-name="cwtl"><Operand type="eax"/></InstructionForm>
  </Instruction>
  <Instruction name="ADD" summary="Add">
    <InstructionForm gas-name="addb"/>
    <InstructionForm gas-name="addl"/>
    <InstructionForm gas-name="addl"/>
  </Instruction>
  <Instruction name="VCVTPD2DQ" summary="Convert &#x201C;Packed&#x201D; Double &#x2014; Dword">
    <InstructionForm gas-name="vcvtpd2dqx"/>
    <InstructionForm gas-name="vcvtpd2dq"/>
  </Instruction>
  <Instruction name="MOVZX" summary="Move with Zero-Extend">
    <InstructionForm gas-name="movzbl"/>
  </Instruction>
  <Instruction name="NOFORMS" summary="">
    <InstructionForm/>
  </Instruction>
</InstructionSet>
`

const htmlFixture = `<html><body><table>
<tr><td><a href='/x86/add'>ADD</a></td><td>Add (ignored, XML wins)</td></tr>
<tr><td><a href='/x86/lgdt:lidt'>LGDT</a></td><td>Load Global/Interrupt Descriptor Table Register</td></tr>
<tr><td><a href='/x86/jcc'>Jcc</a></td><td>Jump if Condition Is Met</td></tr>
<tr><td><a href='/x86/loop'>LOOPcc</a></td><td>Loop According to ECX Counter</td></tr>
<tr><td><a href='/x86/movs'>MOVS</a></td><td>Move Data From String to String</td></tr>
<tr><td><a href='/x86/fcmovcc'>FCMOVcc</a></td><td>Floating-Point Conditional Move</td></tr>
<tr><td><a href='/x86/x'>XEON</a></td><td>Xeon Phi&#8482; &amp; friends</td></tr>
<tr><td><a href='/x86/empty'></a></td><td>nothing</td></tr>
</table></body></html>`

func testDocs(t *testing.T) *asmDocs {
	t.Helper()
	d := newAsmDocs()
	if err := d.readOpcodesXML(strings.NewReader(xmlFixture)); err != nil {
		t.Fatal(err)
	}
	if err := d.readIndexHTML(strings.NewReader(htmlFixture)); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAsmDocs(t *testing.T) {
	d := testDocs(t)
	tests := map[string]string{
		"ADD":       "Add",
		"LGDT":      "Load Global/Interrupt Descriptor Table Register",
		"JNBE":      "Jump if Condition Is Met",
		"VCVTPD2DQ": `Convert "Packed" Double - Dword`,
		"XEON":      "Xeon Phi & friends",
	}
	for name, want := range tests {
		if got, ok := d.lookup(name); !ok || got != want {
			t.Errorf("lookup(%s) = %q,%v want %q", name, got, ok, want)
		}
	}
	if _, ok := d.lookup("JMPABS"); ok {
		t.Error("JMPABS must not match the Jcc family")
	}
	if !reflect.DeepEqual(d.GasNames["ADD"], []string{"addb", "addl"}) {
		t.Errorf("gas names deduplicated: %v", d.GasNames["ADD"])
	}
	if err := newAsmDocs().readOpcodesXML(strings.NewReader("<a><b></a>")); err == nil {
		t.Error("expected xml error")
	}
}

func TestCleanText(t *testing.T) {
	tests := map[string]string{
		"  a\u2019s  \u201cq\u201d \u2014 x\u2026 ": `a's "q" - x...`,
		"Intel\u00ae 64 \u4e2d":                     "Intel 64",
		"2\u00d73 \u2192 y":                         "2x3 -> y",
	}
	for in, want := range tests {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAliasesFor(t *testing.T) {
	d := testDocs(t)
	names := map[string]bool{"ja": false, "jc": true, "pushf": true, "pushfd": true, "lods": false}
	tests := []struct {
		name, upper string
		want        []string
	}{
		{"cwde", "CWDE", []string{"cwtl"}},
		{"add", "ADD", nil},
		{"vcvtpd2dq", "VCVTPD2DQ", []string{"vcvtpd2dqx"}},
		{"movzx", "MOVZX", nil},
		{"jnbe", "JNBE", []string{"ja"}},
		// jc is an instruction name in this fixture, so it is not an alias.
		{"jb", "JB", []string{"jnae"}},
		{"setnz", "SETNZ", []string{"setne"}},
		{"cmovz", "CMOVZ", []string{"cmove"}},
		{"fcmovb", "FCMOVB", nil},
		{"jmp", "JMP", []string{"ljmp"}},
		// pushfl is pushf plus a suffix, which Lookup splits on its own.
		{"pushfd", "PUSHFD", nil},
		{"lodsd", "LODSD", []string{"lodsl"}},
	}
	for _, tc := range tests {
		if got := aliasesFor(tc.name, tc.upper, d, names); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("aliasesFor(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSummaryCandidates(t *testing.T) {
	tests := map[string][]string{
		"MOVSL":    {"MOVS"},
		"XSAVE64":  {"XSAVE64", "XSAVE"},
		"LOOPE":    {"LOOPE", "LOOPCC"},
		"JCXZ":     {"JCXZ", "JCC"},
		"UD1":      {"UD1", "UD"},
		"FCMOVNBE": {"FCMOVNBE", "FCMOVCC"},
		"SCASQ":    {"SCASQ", "SCAS"},
		"ADD":      {"ADD"},
	}
	for in, want := range tests {
		if got := summaryCandidates(in); !reflect.DeepEqual(got, want) {
			t.Errorf("summaryCandidates(%s) = %v, want %v", in, got, want)
		}
	}
}

func TestBuildJoin(t *testing.T) {
	w := testWidths(t)
	db := &xedDB{Widths: w, Records: []*xedRecord{
		rec("ADD", "I86", "0x00 MOD[0b11] MOD=3", "REG0=GPR8_B():rw REG1=GPR8_R():r"),
		rec("ADD", "APX_F", "EVV 0x00 MAP4 mode64", "REG0=GPR8_N():w REG1=GPR8_B():r REG2=GPR8_R():r"),
		rec("ADD_LOCK", "I86", "0x00 MOD[mm]", "MEM0:rw:b REG0=GPR8_R():r"),
		rec("PUSH2", "APX_F", "EVV 0xFF MAP4 mode64", "REG0=GPR64_N():r REG1=GPR64_B():r"),
		rec("MOV_CR", "I86", "0x0F 0x22 MOD[0b11] MOD=3", "REG0=CR_R():w REG1=GPR32_B():r"),
		rec("MOV", "I86", "0x88 MOD[0b11] MOD=3", "REG0=GPR8_B():w REG1=GPR8_R():r"),
		rec("IRETQ", "LONGMODE", "0xCF mode64 rexw_prefix", ""),
		rec("PUSHA", "I186", "0x60 mode16", ""),
		rec("PUSHA", "I186", "0x60 mode32 66_prefix", ""),
		rec("ZZTOP", "FUTURE_THING", "0x0F 0x0A", "REG0=GPR32_R():w"),
		rec("NOP", "I86", "0b1001_0 SRM[0b000]", ""),
		rec("NOP", "FAT_NOP", "0x0F 0x1F MOD[mm] MOD!=3 REG[rrr]", "MEM0:r:v"),
		rec("NOP", "FAT_NOP", "0x0F 0x1F MOD[0b11] MOD=3 REG[rrr]", "REG0=GPRv_B():r"),
		rec("NOP", "FAT_NOP", "0x0F 0x19 MOD[0b11] MOD=3", "REG0=GPRv_B():r REG1=GPRv_R():r"),
	}}
	db.Records[0].Flags = "MUST [ cf-mod af-u ]"
	db.Records[4].CPL = 0
	db.Records[6].CPL = 0
	d := testDocs(t)
	d.Summary["IRETQ"] = "Interrupt Return"
	d.Summary["PUSHA"] = "Push All"
	d.Summary["MOV"] = "Move"
	d.Summary["NOP"] = "No Operation"
	insts, st := build(db, d)

	byName := map[string]Instruction{}
	var order []string
	for _, in := range insts {
		byName[in.Name] = in
		order = append(order, in.Name)
	}
	wantOrder := []string{"add", "iretq", "mov", "nop", "push2", "pusha", "zztop"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order %v, want %v", order, wantOrder)
	}
	add := byName["add"]
	if len(add.Forms) != 1 || add.Forms[0].ISASet != "I86" || add.FlagsWritten != "CF" || add.FlagsUndefined != "AF" || add.Modes != "" {
		t.Errorf("add (APX promotion should be dropped): %+v", add)
	}
	if p := byName["push2"]; len(p.Forms) != 1 || p.Modes != "64" || p.Summary != "Push Two 64-bit Registers" {
		t.Errorf("push2 (APX-only keeps its form): %+v", p)
	}
	if m := byName["mov"]; m.CPL != 3 || len(m.Forms) != 2 {
		t.Errorf("mov: CPL must stay 3 when MOV_CR is merged: %+v", m)
	}
	if i := byName["iretq"]; i.CPL != 0 || i.Modes != "64" {
		t.Errorf("iretq: %+v", i)
	}
	if p := byName["pusha"]; p.Modes != "16,32" {
		t.Errorf("pusha modes %q", p.Modes)
	}
	if n := byName["nop"]; len(n.Forms) != 4 || n.Forms[1].Operands != "r/m16" {
		t.Errorf("nop: %+v", n.Forms)
	}
	if z := byName["zztop"]; z.Summary != "ZZTOP instruction (FUTURE_THING)" {
		t.Errorf("fallback summary %q", z.Summary)
	}
	if !reflect.DeepEqual(st.Fallback, []string{"zztop"}) {
		t.Errorf("fallbacks %v", st.Fallback)
	}

	out, err := encode(insts)
	if err != nil {
		t.Fatal(err)
	}
	var back []Instruction
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(back, insts) {
		t.Error("JSON round trip mismatch")
	}
	if bytes.Count(out, []byte("\n")) != len(insts)+2 {
		t.Error("expected one instruction per line")
	}
	if _, err := encode([]Instruction{{Name: "x", Summary: "caf\u00e9"}}); err == nil {
		t.Error("encode must reject non-ASCII")
	}
}

// writeAsmLSPTree lays out the three asm-lsp files the generator reads.
func writeAsmLSPTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "asm-lsp")
	files := map[string]string{
		"docs_store/opcodes/x86_64.xml":                   xmlFixture,
		"docs_store/opcodes/x86.xml":                      `<InstructionSet name="x86"><Instruction name="POPCNT" summary="Bit Population Count"/></InstructionSet>`,
		"docs_store/instr_info_cache/x86_instr_docs.html": htmlFixture,
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunEndToEnd(t *testing.T) {
	xedRoot := filepath.Dir(writeFixtureTree(t)) // parent of datafiles, like a clone
	asmRoot := writeAsmLSPTree(t)
	outDir := t.TempDir()
	out := filepath.Join(outDir, "data", "xed.json")
	fb := filepath.Join(outDir, "fallbacks.txt")
	var log bytes.Buffer
	err := run([]string{"-xed", xedRoot, "-asmlsp", asmRoot, "-out", out, "-fallbacks", fb}, &log)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var insts []Instruction
	if err := json.Unmarshal(data, &insts); err != nil {
		t.Fatal(err)
	}
	if len(insts) != 3 || insts[0].Name != "add" || insts[2].Name != "popcnt" {
		t.Fatalf("unexpected output: %s", data)
	}
	if insts[2].Summary != "Bit Population Count" || insts[2].Forms[0].Level != 2 {
		t.Errorf("popcnt: %+v", insts[2])
	}
	fbData, _ := os.ReadFile(fb)
	if strings.TrimSpace(string(fbData)) != "pdep" {
		t.Errorf("fallbacks file: %q", fbData)
	}
	if !strings.Contains(log.String(), "3 instructions") {
		t.Errorf("log: %s", log.String())
	}

	bad := [][]string{
		{"-nosuchflag"},
		{"-xed", filepath.Join(outDir, "missing"), "-asmlsp", asmRoot, "-out", out},
		{"-xed", xedRoot, "-asmlsp", filepath.Join(outDir, "missing"), "-out", out},
		{"-xed", xedRoot, "-asmlsp", asmRoot, "-out", filepath.Join(fb, "x", "y.json")},
	}
	for _, args := range bad {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Errorf("run(%v): expected error", args)
		}
	}
}
