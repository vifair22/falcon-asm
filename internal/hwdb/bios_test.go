package hwdb

import (
	"strings"
	"testing"
)

func TestBIOSLookup(t *testing.T) {
	d := Default()
	tests := []struct {
		vec, ah, al uint64
		alKnown     bool
		key, name   string
	}{
		{0x10, 0x0E, 0, false, "INT 10h AH=0Eh", "Teletype output"},
		{0x10, 0x4F, 0x02, true, "INT 10h AX=4F02h", "VBE set mode"},
		{0x11, 0x00, 0, false, "INT 11h", "Get equipment list"},
		{0x12, 0x99, 0, false, "INT 12h", "Get conventional memory size"},
		{0x13, 0x42, 0, false, "INT 13h AH=42h", "Extended read"},
		{0x13, 0x08, 0, false, "INT 13h AH=08h", "Get drive parameters"},
		{0x13, 0x41, 0, false, "INT 13h AH=41h", "Check extensions present"},
		{0x13, 0x4B, 1, true, "INT 13h AH=4Bh", "Bootable CD-ROM emulation status / terminate"},
		{0x14, 0x01, 0, false, "INT 14h AH=01h", "Send character"},
		{0x15, 0x24, 0x01, true, "INT 15h AX=2401h", "Enable A20 gate"},
		{0x15, 0xE8, 0x20, true, "INT 15h AX=E820h", "Query system address map"},
		{0x15, 0xE8, 0x01, true, "INT 15h AX=E801h", "Get memory size for >64M configurations"},
		{0x15, 0x86, 0, false, "INT 15h AH=86h", "Wait"},
		{0x15, 0x87, 0, false, "INT 15h AH=87h", "Move extended memory block"},
		{0x15, 0x88, 0, false, "INT 15h AH=88h", "Get extended memory size"},
		{0x15, 0xC0, 0, false, "INT 15h AH=C0h", "Get system configuration"},
		{0x15, 0x53, 0x07, true, "INT 15h AX=5307h", "APM set power state"},
		{0x16, 0x00, 0, false, "INT 16h AH=00h", "Read keystroke"},
		{0x16, 0x12, 0, false, "INT 16h AH=12h", "Get extended shift flags"},
		{0x17, 0x02, 0, false, "INT 17h AH=02h", "Get printer status"},
		{0x18, 0x00, 0, false, "INT 18h", "Boot failure / ROM BASIC"},
		{0x19, 0x00, 0, false, "INT 19h", "Bootstrap loader"},
		{0x1A, 0x02, 0, false, "INT 1Ah AH=02h", "Read RTC time"},
		{0x1A, 0xB1, 0x01, true, "INT 1Ah AX=B101h", "PCI BIOS installation check"},
		{0x1A, 0xB1, 0x0A, true, "INT 1Ah AX=B10Ah", "Read PCI configuration dword"},
	}
	for _, tt := range tests {
		s, ok := d.LookupBIOS(tt.vec, tt.ah, tt.al, tt.alKnown)
		if !ok || s.Key() != tt.key || s.Name != tt.name {
			var got string
			if s != nil {
				got = s.Title()
			}
			t.Errorf("LookupBIOS(%#x, %#x, %#x, %v) = %q; want %s: %s", tt.vec, tt.ah, tt.al, tt.alKnown, got, tt.key, tt.name)
		}
	}
	// AL unknown under an AL-selected group: no single service.
	if _, ok := d.LookupBIOS(0x15, 0xE8, 0, false); ok {
		t.Error("INT 15h AH=E8h without AL resolved to one service")
	}
	if _, ok := d.LookupBIOS(0x13, 0x77, 0, false); ok {
		t.Error("unknown INT 13h function resolved")
	}
	// Tables interface.
	name, detail, ok := d.BIOS(0x13, 0x42)
	if !ok || name != "INT 13h AH=42h: Extended read" || !strings.Contains(detail, "DS:SI = disk address packet") {
		t.Errorf("BIOS(13h, 42h) = %q, %q", name, detail)
	}
	name, _, ok = d.BIOS(0x15, 0xE8)
	if !ok || !strings.Contains(name, "AL=20h Query system address map") || !strings.Contains(name, "AL=01h") {
		t.Errorf("BIOS(15h, E8h) = %q", name)
	}
	if _, _, ok := d.BIOS(0x21, 0x09); ok {
		t.Error("INT 21h is DOS, not BIOS")
	}
}

func TestBIOSConventions(t *testing.T) {
	d := Default()
	s, _ := d.LookupBIOS(0x13, 0x08, 0, false)
	v, _ := d.BIOSVectorInfo(0x13)
	cl := strings.Join(s.AllClobbers(v), ",")
	for _, r := range []string{"ah", "es", "di", "dl", "dh", "ch", "cl", "bl", "ax"} {
		if !strings.Contains(","+cl+",", ","+r+",") {
			t.Errorf("INT 13h AH=08h clobbers %q, missing %s", cl, r)
		}
	}
	if s.Err != ErrCFAH || s.Status != "int13" {
		t.Errorf("INT 13h AH=08h error convention %q/%q", s.Err, s.Status)
	}
	s, _ = d.LookupBIOS(0x10, 0x0E, 0, false)
	v, _ = d.BIOSVectorInfo(0x10)
	if cl := s.AllClobbers(v); len(cl) != 1 || cl[0] != "bp" {
		t.Errorf("INT 10h AH=0Eh clobbers %v", cl)
	}
	s, _ = d.LookupBIOS(0x15, 0xE8, 0x20, true)
	cl = strings.Join(s.AllClobbers(nil), ",")
	for _, r := range []string{"eax", "ebx", "ecx", "edx"} {
		if !strings.Contains(cl, r) {
			t.Errorf("E820 clobbers %q, missing %s", cl, r)
		}
	}
	if s.Err != ErrCF || len(s.Struct) != 1 || s.Struct[0] != "e820" {
		t.Errorf("E820 err/struct = %q %v", s.Err, s.Struct)
	}
	s, _ = d.LookupBIOS(0x16, 0x01, 0, false)
	if s.Err != ErrZF {
		t.Errorf("INT 16h AH=01h err = %q", s.Err)
	}
	s, _ = d.LookupBIOS(0x15, 0x86, 0, false)
	if s.IF == "" {
		t.Error("INT 15h AH=86h has no IF note")
	}
	for code, want := range map[uint64]string{0x00: "success", 0x09: "64 KiB", 0x80: "timeout", 0xAA: "not ready"} {
		if n, ok := d.BIOSStatus("int13", code); !ok || !strings.Contains(n, want) {
			t.Errorf("int13 status %#x = %q", code, n)
		}
	}
	if _, ok := d.BIOSStatus("int13", 0x99); ok {
		t.Error("unknown status named")
	}
	if _, ok := d.BIOSVectorInfo(0x21); ok {
		t.Error("INT 21h vector known")
	}
	if got := splitRegs("CX:DX, bogus"); strings.Join(got, ",") != "cx,dx" {
		t.Errorf("splitRegs = %v", got)
	}
}

func TestBIOSStructs(t *testing.T) {
	d := Default()
	want := []string{"cdrom-spec", "dap", "e820", "edd-params", "move-gdt", "sysconf", "vbe-info", "vbe-mode-info"}
	if got := strings.Join(d.Structs(), ","); got != strings.Join(want, ",") {
		t.Errorf("Structs = %s", got)
	}
	dap, ok := d.Struct("dap")
	if !ok {
		t.Fatal("no dap")
	}
	// airies-boot falcon-bios/src/start/boot.S builds its DAP as
	// .byte 16, .byte 0, .word 0, .word 0, .word 0, .quad 0.
	boot := []byte{16, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if p := dap.CheckStruct(boot); len(p) != 0 {
		t.Errorf("boot.S DAP: %v", p)
	}
	bad := append([]byte(nil), boot...)
	bad[0], bad[1] = 0x12, 1
	p := strings.Join(dap.CheckStruct(bad), "; ")
	if !strings.Contains(p, "dap.size (offset 0) is 0x12, want 0x10 or 0x18") || !strings.Contains(p, "dap.reserved") {
		t.Errorf("bad DAP: %s", p)
	}
	p = strings.Join(dap.CheckStruct(boot[:12]), "; ")
	if !strings.Contains(p, "is 12 bytes") {
		t.Errorf("short DAP: %s", p)
	}
	e, _ := d.Struct("e820")
	if len(e.Sizes) != 2 || e.Fields[3].Off != 20 || e.Fields[3].Since != 24 {
		t.Errorf("e820 layout %+v", e)
	}
	ep, _ := d.Struct("edd-params")
	if ep.Fields[len(ep.Fields)-1].Off != 56 {
		t.Error("edd-params device path offset")
	}
	if _, ok := d.Struct("nope"); ok {
		t.Error("unknown struct")
	}
}
