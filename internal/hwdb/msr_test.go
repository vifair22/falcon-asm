package hwdb

import (
	"strings"
	"testing"
)

func TestMSRLookup(t *testing.T) {
	d := Default()
	tests := []struct {
		idx  uint64
		name string
	}{
		{0x10, "IA32_TIME_STAMP_COUNTER"},
		{0x1B, "IA32_APIC_BASE"},
		{0x3A, "IA32_FEATURE_CONTROL"},
		{0x48, "IA32_SPEC_CTRL"},
		{0x49, "IA32_PRED_CMD"},
		{0xFE, "IA32_MTRRCAP"},
		{0x174, "IA32_SYSENTER_CS"},
		{0x176, "IA32_SYSENTER_EIP"},
		{0x1A0, "IA32_MISC_ENABLE"},
		{0x1D9, "IA32_DEBUGCTL"},
		{0x200, "IA32_MTRR_PHYSBASE0"},
		{0x207, "IA32_MTRR_PHYSMASK3"},
		{0x21E, "IA32_MTRR_PHYSBASE15"},
		{0x250, "IA32_MTRR_FIX64K_00000"},
		{0x26D, "IA32_MTRR_FIX4K_E8000"},
		{0x277, "IA32_PAT"},
		{0x2FF, "IA32_MTRR_DEF_TYPE"},
		{0x80F, "IA32_X2APIC_SIVR"},
		{0x830, "IA32_X2APIC_ICR"},
		{0x813, "IA32_X2APIC_ISR3"},
		{0x8A0, "x2APIC register"},
		{0xDA0, "IA32_XSS"},
		{0xC0000080, "IA32_EFER"},
		{0xC0000081, "IA32_STAR"},
		{0xC0000082, "IA32_LSTAR"},
		{0xC0000084, "IA32_FMASK"},
		{0xC0000100, "IA32_FS_BASE"},
		{0xC0000102, "IA32_KERNEL_GS_BASE"},
		{0xC0000103, "IA32_TSC_AUX"},
		{0xC0010010, "SYSCFG"},
		{0xC0010114, "VM_CR"},
		{0xC0010117, "VM_HSAVE_PA"},
	}
	for _, tt := range tests {
		name, _, ok := d.MSR(tt.idx)
		if !ok || name != tt.name {
			t.Errorf("MSR(%#x) = %q, %v; want %q", tt.idx, name, ok, tt.name)
		}
	}
	for _, idx := range []uint64{0x0, 0x220, 0x900, 0xC0000085, 0xFFFFFFFF} {
		if _, _, ok := d.MSR(idx); ok {
			t.Errorf("MSR(%#x) unexpectedly known", idx)
		}
	}
	// like: copies the fixed-range fields.
	m, _ := d.LookupMSR(0x26F)
	if len(m.Fields) != 8 || m.Fields[7].Lo != 56 {
		t.Errorf("0x26F fields = %+v", m.Fields)
	}
	if r, ok := d.MSRReserved(0xC0000080); !ok || r != 0x2FE {
		t.Errorf("EFER reserved = %#x", r)
	}
	if _, ok := d.MSRReserved(0x5); ok {
		t.Error("unknown MSR has a reserved mask")
	}
}

func TestCheckMSRWrite(t *testing.T) {
	d := Default()
	tests := []struct {
		idx, v uint64
		want   string // "" = no problems
	}{
		{0xC0000080, 0x501, ""},                    // SCE | LME | LMA
		{0xC0000080, 0xD01, ""},                    // + NXE
		{0xC0000080, 0x2, "reserved bits 0x2"},     // bit 1
		{0xC0000080, 0x200, "reserved bits 0x200"}, // bit 9
		{0x277, 0x0007040600070406, ""},            // power-on PAT
		{0x277, 0x0007040600070402, "reserved memory type"},
		{0x277, 0x08, "reserved bits"},
		{0x2FF, 0xC06, ""},                     // E | FE | WB
		{0x2FF, 0xC03, "reserved memory type"}, // type 3
		{0x2FF, 0x100, "reserved bits"},
		{0x200, 0x80000006, ""},
		{0x201, 0xFFFFF800, ""},
		{0x201, 0x400, "reserved bits"},
		{0xFE, 0, "read-only"},
		{0x1B, 0xFEE00900, ""},
		{0x1B, 0xFEE00A00, "reserved bits 0x200"},
		{0xC0000084, 1 << 32, "reserved bits"},
		{0x80B, 1, "reserved bits"},
		{0x12345, 0xFFFF, ""}, // unknown: nothing
	}
	for _, tt := range tests {
		got := strings.Join(d.CheckMSRWrite(tt.idx, tt.v), "; ")
		if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
			t.Errorf("CheckMSRWrite(%#x, %#x) = %q; want %q", tt.idx, tt.v, got, tt.want)
		}
	}
}

func TestDecodeMSR(t *testing.T) {
	d := Default()
	dec, ok := d.DecodeMSR(0xC0000080, 0xD01)
	if !ok || dec.Summary != "IA32_EFER: SCE LME LMA NXE" {
		t.Errorf("EFER summary = %q", dec.Summary)
	}
	dec, _ = d.DecodeMSR(0x277, 0x0007040600070406)
	if !strings.Contains(dec.Summary, "PA0=WB") || !strings.Contains(dec.Summary, "PA3=UC") || !strings.Contains(dec.Summary, "PA2=UC-") {
		t.Errorf("PAT summary = %q", dec.Summary)
	}
	// Decoding a read-only MSR's value is not a write.
	dec, _ = d.DecodeMSR(0xFE, 0x508)
	if len(dec.Problems) != 0 || !strings.Contains(dec.Summary, "VCNT=0x8") {
		t.Errorf("MTRRCAP = %q %v", dec.Summary, dec.Problems)
	}
	dec, _ = d.DecodeMSR(0x830, 0x000000010000C500|6)
	if !strings.Contains(dec.Summary, "DELIVERY=INIT") || !strings.Contains(dec.Summary, "DEST=0x1") {
		t.Errorf("ICR = %q", dec.Summary)
	}
	if _, ok := d.DecodeMSR(0x5, 0); ok {
		t.Error("unknown MSR decoded")
	}
}
