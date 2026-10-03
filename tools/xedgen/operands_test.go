package main

import (
	"reflect"
	"strings"
	"testing"
)

func rec(iclass, isa, pattern, operands string, attrs ...string) *xedRecord {
	return &xedRecord{
		IClass:     iclass,
		ISASet:     isa,
		Extension:  isa,
		CPL:        3,
		Pattern:    strings.Fields(pattern),
		Operands:   strings.Fields(operands),
		Attributes: attrs,
	}
}

func TestParseOperand(t *testing.T) {
	w := testWidths(t)
	tests := []struct {
		in   string
		want xedOperand
	}{
		{"REG0=GPRv_R():w:v", xedOperand{Name: "REG0", Value: "GPRv_R()", Action: "w", Width: "v"}},
		{"MEM0:r:zd:u32:TXT=BCASTSTR", xedOperand{Name: "MEM0", Action: "r", Width: "zd", Elem: "u32", Txt: "BCASTSTR"}},
		{"IMM0:r:IMPL:b", xedOperand{Name: "IMM0", Action: "r", Width: "b", Vis: "IMPL"}},
		{"REG1=XED_REG_CL:r:IMPL", xedOperand{Name: "REG1", Value: "XED_REG_CL", Action: "r", Vis: "IMPL"}},
		{"REG2=MASK1():r:mskw:TXT=ZEROSTR", xedOperand{Name: "REG2", Value: "MASK1()", Action: "r", Width: "mskw", Txt: "ZEROSTR"}},
		{"EMX_BROADCAST_1TO4_32", xedOperand{Name: "EMX_BROADCAST_1TO4_32"}},
	}
	for _, tc := range tests {
		if got := parseOperand(tc.in, w); got != tc.want {
			t.Errorf("parseOperand(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// renderAll renders a record at every size operandSizes selects, in AT&T
// order, which is what ends up in the JSON before reg/mem fusion.
func renderAll(t *testing.T, r *xedRecord) []string {
	t.Helper()
	w := testWidths(t)
	var out []string
	for _, osz := range operandSizes(r, w) {
		ops, ok := renderRecord(r, w, osz)
		if !ok {
			continue
		}
		out = append(out, attOrder(r, ops))
	}
	return out
}

func TestRenderRecord(t *testing.T) {
	tests := []struct {
		name string
		rec  *xedRecord
		want []string
	}{
		{"gprv expands by operand size",
			rec("ADD", "I86", "0x01 MOD[0b11] MOD=3 REG[rrr] RM[nnn]", "REG0=GPRv_B():rw REG1=GPRv_R():r"),
			[]string{"r16, r16", "r32, r32", "r64, r64"}},
		{"imm z caps at 32",
			rec("ADD", "I86", "0x05", "REG0=OrAX():rw:IMPL IMM0:r:z"),
			[]string{"imm16, ax", "imm32, eax", "imm32, rax"}},
		{"not64 drops 64-bit size",
			rec("BOUND", "I186", "0x62 MOD[mm] MOD!=3 not64", "REG0=GPRv_R():r MEM0:r:v", "ATT_OPERAND_ORDER_EXCEPTION"),
			[]string{"r16, m16", "r32, m32"}},
		{"rexw selects 64 only",
			rec("MOVSXD", "LONGMODE", "0x63 MOD[mm] MOD!=3 mode64 rexw_prefix", "REG0=GPR64_R():w MEM0:r:d"),
			[]string{"m32, r64"}},
		{"vex W0 gpry is 32-bit",
			rec("PDEP", "BMI2", "VV1 0xF5 W0 MOD[0b11] MOD=3", "REG0=VGPRy_R():w REG1=VGPRy_N():r REG2=VGPRy_B():r"),
			// 16 and 32 both render as r32; buildForms deduplicates.
			[]string{"r32, r32, r32", "r32, r32, r32"}},
		{"suppressed operands are hidden",
			rec("CPUID", "I486REAL", "0x0F 0xA2", "REG0=XED_REG_EAX:rw:SUPP REG1=XED_REG_EBX:w:SUPP"),
			[]string{""}},
		{"in al, dx keeps implicit fixed registers",
			rec("IN", "I86", "0xEC", "REG0=XED_REG_AL:w:IMPL REG1=XED_REG_DX:r:IMPL"),
			[]string{"dx, al"}},
		{"shift by one renders literal 1",
			rec("SHL", "I86", "0xD0 MOD[0b11] MOD=3 ONE()", "REG0=GPR8_B():rw IMM0:r:b:IMPL"),
			[]string{"1, r8"}},
		{"avx512 mask and zeroing decorate the destination",
			rec("VPERMD", "AVX512F_512", "EVV 0x36 V66 V0F38 MOD[mm] MOD!=3 VL512 W0 ESIZE_32_BITS() NELEM_FULL()",
				"REG0=ZMM_R3():w:zu32 REG1=MASK1():r:mskw:TXT=ZEROSTR REG2=ZMM_N3():r:zu32 MEM0:r:vv:u32:TXT=BCASTSTR"),
			[]string{"m512/m32bcst, zmm, zmm{k}{z}"}},
		{"explicit broadcast record",
			rec("VADDPS", "AVX512F_128", "EVV 0x58 MOD[mm] MOD!=3 BCRC=1 VL128 ESIZE_32_BITS() NELEM_FULL()",
				"REG0=XMM_R3():w:dq REG1=MASK1():r:mskw REG2=XMM_N3():r:dq MEM0:r:vv:f32"),
			[]string{"m32bcst, xmm, xmm{k}"}},
		{"rounding control records are skipped",
			rec("VADDPS", "AVX512F_512", "EVV 0x58 MOD[0b11] MOD=3 BCRC=1 VL512",
				"REG0=ZMM_R3():w:zf32 REG1=MASK1():r:mskw REG2=ZMM_N3():r REG3=ZMM_B3():r"),
			nil},
		{"rounding by TXT hint is skipped",
			rec("VCVTSI2SS", "AVX512F_SCALAR", "EVV 0x2A MOD[0b11] MOD=3",
				"REG0=XMM_R3():w REG1=XMM_N3():r REG2=GPR32_B():r:d:TXT=ROUNDC"),
			nil},
		{"vsib gather",
			rec("VPGATHERDD", "AVX512F_256", "EVV 0x90 MOD[mm] MOD!=3 VL256 UISA_VMODRM_YMM()",
				"REG0=YMM_R3():w:qq:u32 REG1=MASKNOT0():rw:mskw MEM0:r:d:u32", "DWORD_INDICES"),
			[]string{"vm32y, ymm{k}"}},
		{"vsib qword indices",
			rec("VPGATHERQQ", "AVX2GATHER", "VV1 0x91 VL128 MOD[mm] MOD!=3 VMODRM_XMM()",
				"REG0=XMM_R():crw:dq MEM0:r:q REG1=XMM_N():rw:dq", "QWORD_INDICES"),
			[]string{"xmm, vm64x, xmm"}},
		{"x87 memory form hides st(0)",
			rec("FADD", "X87", "0xD8 MOD[mm] MOD!=3 REG[0b000]", "REG0=XED_REG_ST0:rw:IMPL MEM0:r:mem32real"),
			[]string{"m32fp"}},
		{"x87 register form keeps st(0)",
			rec("FADD", "X87", "0xD8 MOD[0b11] MOD=3", "REG0=XED_REG_ST0:rw:IMPL REG1=X87():r"),
			[]string{"st(i), st(0)"}},
		{"far pointer folds selector immediate",
			rec("JMP_FAR", "I86", "0xEA not64", "PTR:r:p IMM0:r:w"),
			[]string{"ptr16:16", "ptr16:32"}},
		{"far memory pointer",
			rec("JMP_FAR", "I86", "0xFF MOD[mm] MOD!=3 REG[0b101]", "MEM0:r:p"),
			[]string{"m16:16", "m16:32", "m16:32"}},
		{"special memory names",
			rec("LGDT", "I286REAL", "0x0F 0x01 not64", "MEM0:r:s REG0=XED_REG_GDTR:w:SUPP"),
			[]string{"m16&32"}},
		{"struct memory is sized in bytes",
			rec("FNSTENV", "X87", "0xD9 MOD[mm] MOD!=3", "MEM0:w:mem14"),
			[]string{"m14byte"}},
		{"misc register classes",
			rec("X", "Y", "0x00", "REG0=CR_R():w REG1=DR_R():r REG2=SEG():r REG3=BND_R():r REG4=TMM_R():r REG5=MMX_R():r REG6=MASK_R():r REG7=YMM_B():r REG8=XMM_SE():r REG9=WEIRD():r"),
			[]string{"weird, xmm, ymm, k, mm, tmm, bnd, sreg, dr, cr"}},
		{"relative branches and agen",
			rec("LEA", "I86", "0x8D MOD[mm] MOD!=3", "REG0=GPR32_R():w AGEN:r RELBR:r:b ABSBR:r:u64 REG1=XED_REG_INVALID:r"),
			[]string{"imm64, rel8, m, r32"}},
		{"accumulator by address size",
			rec("MONITOR", "MONITOR", "0x0F 0x01", "REG0=ArAX():r:IMPL REG1=OeAX():r:IMPL REG2=A_GPR_R():r"),
			[]string{"r16, ax, ax", "r32, eax, eax", "r64, eax, rax"}},
	}
	for _, tc := range tests {
		got := renderAll(t, tc.rec)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestVectorMemBits(t *testing.T) {
	tests := []struct {
		pattern string
		want    int
	}{
		{"VL512 NELEM_FULLMEM()", 512},
		{"VL256 NELEM_HALFMEM()", 128},
		{"VL512 NELEM_QUARTERMEM()", 128},
		{"VL512 NELEM_EIGHTHMEM()", 64},
		{"VL512 NELEM_MEM128()", 128},
		{"VL128 ESIZE_64_BITS() NELEM_SCALAR()", 64},
		{"ESIZE_32_BITS() NELEM_TUPLE2()", 64},
		{"ESIZE_32_BITS() NELEM_TUPLE4()", 128},
		{"ESIZE_32_BITS() NELEM_TUPLE8()", 256},
		{"VL128 NELEM_MOVDDUP()", 64},
		{"VL256 NELEM_MOVDDUP()", 256},
		{"VL128", 0},
	}
	for _, tc := range tests {
		r := rec("X", "Y", tc.pattern, "")
		if got := vectorMemBits(r, xedOperand{}); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.pattern, got, tc.want)
		}
	}
	if esize(rec("X", "Y", "", ""), xedOperand{Elem: "f16"}) != 16 {
		t.Error("esize from element type")
	}
	if esize(rec("X", "Y", "", ""), xedOperand{}) != 32 {
		t.Error("esize default")
	}
}

func TestRecordModes(t *testing.T) {
	tests := map[string]int{"mode64": 4, "not64": 3, "mode16": 1, "mode32": 2, "": 7}
	for p, want := range tests {
		if got := recordModes(rec("X", "Y", p, "")); got != want {
			t.Errorf("%q: %d, want %d", p, got, want)
		}
	}
}
