package hwdb

import (
	"strings"
	"testing"
)

func problems(d *Decoded) string { return strings.Join(d.Problems, "; ") }
func notes(d *Decoded) string    { return strings.Join(d.Notes, "; ") }

// GDT entries from airies-boot: falcon-bios/src/start/pmode.S and
// falcon-bios/src/jump/rthunk.S. The long-mode entries are the canonical
// flat code64 and the invalid L+D combination.
func TestDecodeDescriptorFixtures(t *testing.T) {
	tests := []struct {
		v       uint64
		summary string
		prob    string
	}{
		{0, "null descriptor", ""},
		{0x00CF9A000000FFFF, "code32 (code readable) base=0x0 limit=0xfffff (4 GiB) DPL=0", ""},
		{0x00CF92000000FFFF, "data32 (data writable) base=0x0 limit=0xfffff (4 GiB) DPL=0", ""},
		{0x008F9A000000FFFF, "code16 (code readable) base=0x0 limit=0xfffff (4 GiB) DPL=0", ""},
		{0x008F92000000FFFF, "data16 (data writable) base=0x0 limit=0xfffff (4 GiB) DPL=0", ""},
		{0x00009A000000FFFF, "code16 (code readable) base=0x0 limit=0xffff (64 KiB) DPL=0", ""},
		{0x000092000000FFFF, "data16 (data writable) base=0x0 limit=0xffff (64 KiB) DPL=0", ""},
		{0x00AF9A000000FFFF, "code64 (code readable) base=0x0 limit=0xfffff (4 GiB) DPL=0", ""},
		{0x00AFFA000000FFFF, "code64 (code readable) base=0x0 limit=0xfffff (4 GiB) DPL=3", ""},
		{0x00EF9A000000FFFF, "code64 (code readable) base=0x0 limit=0xfffff (4 GiB) DPL=0", "L and D both set"},
		{0x00CF9B000000FFFF, "code32 (code readable accessed)", ""},
		{0x00CF96000000FFFF, "data32 (data expand-down writable)", ""},
		{0x00CF98000000FFFF, "code32 (code execute-only)", ""},
		{0x00CF9E000000FFFF, "code32 (code conforming readable)", ""},
		{0x00CF7A000000FFFF, "not-present", ""},
		{0x12CF9A345678FFFF, "base=0x12345678", ""},
		{0x00008900A0000067, "system (32-bit TSS (available)) base=0xa000 limit=0x67", ""},
		{0x00008900A0000010, "system (32-bit TSS (available))", "below the minimum 0x67"},
		{0x00008200B0000FFF, "system (LDT)", ""},
		{0x0000880000000000, "reserved system type 8", "system type 8 is reserved"},
	}
	for _, tt := range tests {
		d := DecodeDescriptor(tt.v, nil, false)
		if !strings.Contains(d.Summary, tt.summary) {
			t.Errorf("%#016x: summary %q, want %q", tt.v, d.Summary, tt.summary)
		}
		if (tt.prob == "") != (len(d.Problems) == 0) || !strings.Contains(problems(d), tt.prob) {
			t.Errorf("%#016x: problems %q, want %q", tt.v, problems(d), tt.prob)
		}
	}
	// Notes.
	if d := DecodeDescriptor(0x00CF9A000000FFFF, nil, false); !strings.Contains(notes(d), "accessed bit clear") {
		t.Errorf("accessed note: %q", notes(d))
	}
	if d := DecodeDescriptor(0x00EF92000000FFFF, nil, false); !strings.Contains(notes(d), "L is ignored") {
		t.Errorf("data L note: %q", notes(d))
	}
	if d := DecodeDescriptor(0x00CF7A000000FFFF, nil, false); !strings.Contains(notes(d), "#NP") {
		t.Errorf("not-present note: %q", notes(d))
	}
	if d := DecodeDescriptor(0x00CF98000000FFFF, nil, false); !strings.Contains(notes(d), "execute-only") {
		t.Errorf("execute-only note: %q", notes(d))
	}
	// A 64-bit TSS, as 8 bytes in long mode and as 16 bytes.
	tss := uint64(0x00008900A0000067)
	if d := DecodeDescriptor(tss, nil, true); !strings.Contains(notes(d), "16 bytes") {
		t.Errorf("long-mode 8-byte TSS note: %q", notes(d))
	}
	hi := uint64(0xFFFF8000)
	d := DecodeDescriptor(tss, &hi, true)
	if !strings.Contains(d.Summary, "64-bit TSS (available)") || !strings.Contains(d.Summary, "base=0xffff80000000a000") {
		t.Errorf("16-byte TSS: %q", d.Summary)
	}
	bad := uint64(0x00000100_00000000) // type bits set in the upper half
	if d := DecodeDescriptor(tss, &bad, true); !strings.Contains(problems(d), "upper quadword") {
		t.Errorf("16-byte TSS upper type: %q", problems(d))
	}
	// Fields round-trip through ParseDescriptor.
	p := ParseDescriptor(0x00CF9A000000FFFF, nil)
	if !p.G || !p.DB || p.L || !p.P || !p.S || !p.Code || p.Bytes != 0xFFFFFFFF || p.Type != 0xA {
		t.Errorf("ParseDescriptor = %+v", p)
	}
}

// airies-boot falcon-bios/src/start/idt.S builds 32-bit interrupt gates
// with type word 0x8E00 and selector SEL_CODE32 (0x08).
func TestDecodeGate(t *testing.T) {
	gate := func(off uint64, sel uint64, typeWord uint64) uint64 {
		return off&0xFFFF | sel<<16 | typeWord<<32 | (off>>16&0xFFFF)<<48
	}
	tests := []struct {
		lo      uint64
		hi      *uint64
		summary string
		prob    string
	}{
		{gate(0x12345, 0x08, 0x8E00), nil, "32-bit interrupt gate -> 0x8:0x12345 DPL=0", ""},
		{gate(0x1000, 0x08, 0x8F00), nil, "32-bit trap gate", ""},
		{gate(0x1000, 0x08, 0xEE00), nil, "DPL=3", ""},
		{gate(0x1000, 0x08, 0x8C00), nil, "32-bit call gate", ""},
		{gate(0, 0x28, 0x8500), nil, "task gate -> TSS selector 0x28", ""},
		{gate(0x1000, 0x08, 0x0E00), nil, "not-present", ""},
		{gate(0x1000, 0x08, 0x8600), nil, "16-bit interrupt gate", ""},
		{gate(0x1000, 0x00, 0x8E00), nil, "32-bit interrupt gate", "null code selector"},
		{gate(0x1000, 0x08, 0x9E00), nil, "32-bit interrupt gate", "S bit set"},
		{gate(0x1000, 0x08, 0x8E20), nil, "32-bit interrupt gate", "bits 39:37"},
		{gate(0x1000, 0x08, 0x8900), nil, "not a gate", "not a gate type"},
		{0, nil, "empty gate", ""},
		{gate(0x1000, 0x08, 0x8E00), u64(0xFFFFFFFF), "64-bit interrupt gate -> 0x8:0xffffffff00001000", ""},
		{gate(0x1000, 0x08, 0x8E01), u64(0), "IST=1", ""},
		{gate(0x1000, 0x08, 0x8E00), u64(1 << 40), "64-bit interrupt gate", "63:32 are reserved"},
		{gate(0x1000, 0x08, 0x8E00), u64(0x00010000), "64-bit interrupt gate", "not canonical"},
		{gate(0x1000, 0x08, 0x8600), u64(0), "not a gate", "not a gate type"},
	}
	for _, tt := range tests {
		d := DecodeGate(tt.lo, tt.hi)
		if !strings.Contains(d.Summary, tt.summary) {
			t.Errorf("%#x: summary %q, want %q", tt.lo, d.Summary, tt.summary)
		}
		if (tt.prob == "") != (len(d.Problems) == 0) || !strings.Contains(problems(d), tt.prob) {
			t.Errorf("%#x: problems %q, want %q", tt.lo, problems(d), tt.prob)
		}
	}
}

func u64(v uint64) *uint64 { return &v }

func TestDecodeRegisters(t *testing.T) {
	tests := []struct {
		kind    string
		v       uint64
		summary string
		prob    string
	}{
		// airies-boot pmode.S: CR0 |= 1 (PE) from the reset value 0x10 (ET).
		{"cr0", 0x11, "CR0: PE ET", ""},
		// thunk.S clears PE: 0x10.
		{"cr0", 0x10, "CR0: ET", ""},
		{"cr0", 0x80000011, "CR0: PE ET PG", ""},
		{"cr0", 0x80000010, "PG", "PG set without PE"},
		{"cr0", 0x20000011, "NW", "NW set without CD"},
		{"cr0", 0x60000011, "CD", ""},
		{"cr0", 0x40, "", "reserved bits 0x40"},
		// pmode.S: CR4 |= OSFXSR | OSXMMEXCPT.
		{"cr4", 1<<9 | 1<<10, "CR4: OSFXSR OSXMMEXCPT", ""},
		{"cr4", 0x20 | 1<<12 | 1<<17 | 1<<18 | 1<<20 | 1<<21, "PAE LA57 PCIDE OSXSAVE SMEP SMAP", ""},
		{"cr4", 1 << 15, "", "reserved bits 0x8000"},
		{"cr3", 0x1000, "CR3 (4/5-level paging, CR4.PCIDE = 0): BASE=0x1", ""},
		{"cr3", 0x1018, "PWT PCD", ""},
		{"cr3", 1 << 63, "", "reserved bits"},
		{"cr3-pcid", 1<<63 | 0x2000 | 5, "PCID=0x5 BASE=0x2 NOFLUSH", ""},
		{"cr3-pae", 0x1020, "PDPT=0x81", ""},
		{"cr3-32", 0x5000, "BASE=0x5", ""},
		{"cr3-32", 1 << 32, "", "reserved bits"},
		{"efer", 0xD01, "IA32_EFER: SCE LME LMA NXE", ""},
		{"efer", 0x2, "", "reserved bits 0x2"},
		{"xcr0", 0x7, "XCR0: X87 SSE AVX", ""},
		{"xcr0", 0xE7, "OPMASK ZMM_HI256 HI16_ZMM", ""},
		{"xcr0", 0x6, "", "X87 (bit 0) clear"},
		{"xcr0", 0x5, "", "AVX without SSE"},
		{"xcr0", 0x27, "", "must be set together"},
		{"xcr0", 0xE3, "", "AVX-512 state without AVX"},
		{"xcr0", 0xB, "", "BNDREG and BNDCSR"},
		{"xcr0", 1<<17 | 3, "", "XTILECFG and XTILEDATA"},
		{"xcr0", 1<<8 | 3, "", "reserved bits 0x100"},
		{"rflags", 0x202, "RFLAGS: ONE IF", ""},
		{"rflags", 0x3002, "IOPL=0x3", ""},
		{"eflags", 0x1_00000202, "ONE IF", ""},
		{"rflags", 0x8, "", "reserved bits 0x8"},
		{"selector", 0x08, "INDEX=0x1", ""},
		{"selector", 0x1B, "RPL=0x3 INDEX=0x3", ""},
		{"selector", 0x0F, "RPL=0x3 TI INDEX=0x1", ""},
		{"selector", 0x10000, "", "reserved bits"},
	}
	for _, tt := range tests {
		d, ok := Decode(tt.kind, tt.v)
		if !ok {
			t.Errorf("Decode(%s) unknown", tt.kind)
			continue
		}
		if !strings.Contains(d.Summary, tt.summary) {
			t.Errorf("%s %#x: summary %q, want %q", tt.kind, tt.v, d.Summary, tt.summary)
		}
		if (tt.prob == "") != (len(d.Problems) == 0) || !strings.Contains(problems(d), tt.prob) {
			t.Errorf("%s %#x: problems %q, want %q", tt.kind, tt.v, problems(d), tt.prob)
		}
	}
	if d, _ := Decode("cr0", 0); d.Summary != "CR0: (no bits set)" {
		t.Errorf("zero CR0: %q", d.Summary)
	}
	if d, _ := Decode("rflags", 0); !strings.Contains(notes(d), "bit 1") {
		t.Errorf("rflags bit 1 note: %q", notes(d))
	}
	if d, _ := Decode("selector", 0); !strings.Contains(notes(d), "null selector") {
		t.Errorf("null selector note: %q", notes(d))
	}
	if d, _ := Decode("cr0", 0x40000011); !strings.Contains(notes(d), "caches disabled") {
		t.Errorf("CD note: %q", notes(d))
	}
	if _, ok := Decode("cr9", 0); ok {
		t.Error("cr9 decoded")
	}
	if _, ok := Register("CR4"); !ok {
		t.Error("Register(CR4)")
	}
	if d := decodeReg("nope", 1, nil); d.Summary != "nope" {
		t.Error("decodeReg of an unknown layout")
	}
	kinds := strings.Join(DecodeKinds(), ",")
	for _, k := range []string{"cr0", "cr4", "efer", "xcr0", "pci-config", "descriptor", "gate64", "pml4e", "pte32", "selector"} {
		if !strings.Contains(kinds, k) {
			t.Errorf("DecodeKinds missing %s", k)
		}
	}
	// Every kind decodes without panicking.
	for _, k := range DecodeKinds() {
		if _, ok := Decode(k, 0xFFFFFFFFFFFFFFFF); !ok {
			t.Errorf("Decode(%s) failed", k)
		}
	}
}

// airies-boot falcon-bios/src/hw/pci.h builds
// 0x80000000 | bus << 16 | (dev & 0x1F) << 11 | (fn & 7) << 8 | (reg & 0xFC).
func TestDecodePCIAddress(t *testing.T) {
	addr := func(bus, dev, fn, reg uint64) uint64 {
		return 0x80000000 | bus<<16 | (dev&0x1F)<<11 | (fn&7)<<8 | reg&0xFC
	}
	tests := []struct {
		v       uint64
		summary string
		prob    string
	}{
		{addr(0, 0, 0, 0), "PCI 00:00.0 offset 0x00", ""},
		{addr(0, 0x1F, 3, 0x40), "PCI 00:1f.3 offset 0x40", ""},
		{addr(2, 3, 1, 0x10), "PCI 02:03.1 offset 0x10", ""},
		{addr(0xFF, 0x1F, 7, 0xFC), "PCI ff:1f.7 offset 0xfc", ""},
		{0x00001808, "PCI 00:03.0 offset 0x08", "enable bit 31 clear"},
		{addr(0, 1, 0, 0) | 2, "PCI 00:01.0", "bits 1:0 set"},
		{addr(0, 1, 0, 0x10) | 1<<24, "(extended 0x110)", ""},
		{addr(0, 1, 0, 0) | 1<<29, "", "reserved bits 0x20000000"},
	}
	for _, tt := range tests {
		d, _ := Decode("pci-config", tt.v)
		if !strings.Contains(d.Summary, tt.summary) {
			t.Errorf("%#x: summary %q, want %q", tt.v, d.Summary, tt.summary)
		}
		if (tt.prob == "") != (len(d.Problems) == 0) || !strings.Contains(problems(d), tt.prob) {
			t.Errorf("%#x: problems %q, want %q", tt.v, problems(d), tt.prob)
		}
	}
}

func TestDecodePTE(t *testing.T) {
	tests := []struct {
		kind    string
		v       uint64
		summary string
		prob    string
	}{
		{"pml4e", 0x2003, "PML4E -> table at 0x2000 RW", ""},
		{"pml4e", 0x2083, "", "reserved bits 0x80"},
		{"pml5e", 0x3007, "PML5E -> table at 0x3000 RW US", ""},
		{"pdpte", 0x83, "PDPTE -> 1 GiB page at 0x0 RW PS", ""},
		{"pdpte", 0x40000083, "1 GiB page at 0x40000000", ""},
		{"pdpte", 0x2083, "", "reserved bits 0x2000"},
		{"pdpte", 0x1083, "PAT", ""},
		{"pdpte", 0x3003, "PDPTE -> table at 0x3000", ""},
		{"pde", 0x200083, "PDE -> 2 MiB page at 0x200000 RW PS", ""},
		{"pde", 0x2000E3, "A D PS", ""},
		{"pde", 0x202083, "", "reserved bits 0x2000"},
		{"pde", 0x4003, "PDE -> table at 0x4000", ""},
		{"pte", 0xB8003, "PTE -> 4 KiB page at 0xb8000 RW", ""},
		{"pte", 0x8000000000001103, "G XD", ""},
		{"pte", 0x1083, "PAT", ""},
		{"pte", 0x1800000000001001, "PKEY=3", ""},
		{"pte", 0, "PTE: not present", ""},
		{"pae-pdpte", 0x5001, "PAE PDPTE -> table at 0x5000", ""},
		{"pae-pdpte", 0x5003, "", "reserved bits 0x2"},
		{"pae-pdpte", 0x8000000000005001, "", "reserved bits 0x8000000000000000"},
		{"pae-pde", 0x200083, "2 MiB page", ""},
		{"pae-pte", 0x7003, "PAE PTE -> 4 KiB page at 0x7000", ""},
		{"pde32", 0x400083, "PDE (32-bit) -> 4 MiB page at 0x400000", ""},
		{"pde32", 0x400083 | 3<<13, "4 MiB page at 0x300400000", ""},
		{"pde32", 0x600083, "", "reserved bits 0x200000"},
		{"pde32", 0x9003, "table at 0x9000", ""},
		{"pte32", 0xB8083, "PTE (32-bit) -> 4 KiB page at 0xb8000 RW PAT", ""},
	}
	for _, tt := range tests {
		d, _ := Decode(tt.kind, tt.v)
		if !strings.Contains(d.Summary, tt.summary) {
			t.Errorf("%s %#x: summary %q, want %q", tt.kind, tt.v, d.Summary, tt.summary)
		}
		if (tt.prob == "") != (len(d.Problems) == 0) || !strings.Contains(problems(d), tt.prob) {
			t.Errorf("%s %#x: problems %q, want %q", tt.kind, tt.v, problems(d), tt.prob)
		}
	}
	if d, _ := Decode("pte", 0x8000000000001001); !strings.Contains(notes(d), "EFER.NXE") {
		t.Errorf("XD note: %q", notes(d))
	}
	if d, _ := Decode("pde32", 0x400083); !strings.Contains(notes(d), "CR4.PSE") {
		t.Errorf("PSE note: %q", notes(d))
	}
	if d, _ := Decode("pte", 0x1000); !strings.Contains(notes(d), "available to software") {
		t.Errorf("not-present note: %q", notes(d))
	}
	if d := DecodePTE("pte64", 1); d.Summary != "pte64" {
		t.Error("unknown PTE kind")
	}
}

func TestSizeText(t *testing.T) {
	for n, want := range map[uint64]string{1 << 30: "1 GiB", 1 << 20: "1 MiB", 4096: "4 KiB", 100: "100 bytes", 1<<32 + 1: "4294967297 bytes"} {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d) = %q", n, got)
		}
	}
}
