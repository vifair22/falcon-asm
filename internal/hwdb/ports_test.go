package hwdb

import (
	"strings"
	"testing"
)

func TestPortLookup(t *testing.T) {
	d := Default()
	tests := []struct {
		port  uint64
		write bool
		name  string // PortFor's hit name
		all   string // Port()'s combined name, "" = not checked
	}{
		{0x92, true, "System control port A", "System control port A"},
		{0x64, false, "KBC status", "KBC status (read) / KBC command (write)"},
		{0x64, true, "KBC command", ""},
		{0x60, true, "KBC data", ""},
		{0x20, true, "PIC1 command", ""},
		{0xA1, false, "PIC2 data", ""},
		{0x43, true, "PIT control word", ""},
		{0x70, true, "CMOS index", ""},
		{0x71, false, "CMOS data", ""},
		{0x80, true, "POST code", ""},
		{0x3F8, false, "COM1 RBR", "COM1 RBR (read) / COM1 THR (write) / COM1 DLL (DLAB=1)"},
		{0x3F8, true, "COM1 THR", ""},
		{0x2FD, false, "COM2 LSR", ""},
		{0x3EB, true, "COM3 LCR", ""},
		{0x2E9, true, "COM4 IER", ""},
		{0x379, false, "LPT1 STATUS", ""},
		{0x1F7, false, "IDE0 STATUS", "IDE0 STATUS (read) / IDE0 COMMAND (write)"},
		{0x177, true, "IDE1 COMMAND", ""},
		{0x1F0, false, "IDE0 DATA", ""},
		{0x3F6, true, "IDE0 DEVICE CONTROL", ""},
		{0x376, false, "IDE1 ALT STATUS", ""},
		{0x3D4, true, "VGA CRTC index", ""},
		{0x3C5, true, "VGA sequencer data", ""},
		{0x3DA, false, "VGA input status 1", ""},
		{0xCF8, true, "PCI CONFIG_ADDRESS", ""},
		{0xCF9, true, "Reset control", ""},
		{0xCFE, false, "PCI CONFIG_DATA", ""},
		{0x04, true, "DMA1 address/count", ""},
		{0xC6, true, "DMA2 address/count", ""},
		{0x8F, true, "DMA page channel 4", ""},
		{0xE9, true, "Bochs/QEMU debug port", ""},
		{0x402, true, "QEMU/OVMF debug console", ""},
		{0x3F2, true, "FDC digital output", ""},
		{0x3F7, false, "FDC digital input", ""},
	}
	for _, tt := range tests {
		h, ok := d.PortFor(tt.port, tt.write)
		if !ok || h.Name() != tt.name {
			t.Errorf("PortFor(%#x, %v) = %q, %v; want %q", tt.port, tt.write, h.Name(), ok, tt.name)
		}
		if tt.all != "" {
			name, _, ok := d.Port(tt.port)
			if !ok || name != tt.all {
				t.Errorf("Port(%#x) = %q; want %q", tt.port, name, tt.all)
			}
		}
	}
	for _, p := range []uint64{0x1, 0x3F3, 0x500, 0xFFFF, 0x10000} {
		if _, _, ok := d.Port(p); ok && p != 0x1 {
			t.Errorf("Port(%#x) unexpectedly known", p)
		}
	}
	// 0x01 is in the DMA1 range entry.
	if _, _, ok := d.Port(0x01); !ok {
		t.Error("0x01 (DMA1 range) unknown")
	}
	// A read of a write-only register finds nothing.
	if _, ok := d.PortFor(0x80, false); ok {
		t.Error("PortFor(0x80, read) found a register")
	}
	// The DLAB entry is only chosen when nothing unconditional matches.
	if h, ok := d.PortFor(0x3F9, true); !ok || h.Reg.Cond != "" {
		t.Errorf("PortFor(0x3F9) = %v", h.Name())
	}
}

func TestPortDetails(t *testing.T) {
	d := Default()
	h, _ := d.PortFor(0xCF8, true)
	if !h.Reg.AllowsWidth(32) || h.Reg.AllowsWidth(8) {
		t.Error("0xCF8 widths")
	}
	h, _ = d.PortFor(0x92, true)
	if !h.Reg.AllowsWidth(8) || h.Reg.AllowsWidth(16) {
		t.Error("0x92 widths")
	}
	if !h.Reg.Readable() || !h.Reg.Writable() {
		t.Error("0x92 is read/write")
	}
	h, _ = d.PortFor(0x64, false)
	if h.Reg.Writable() {
		t.Error("0x64 status is read-only")
	}

	idx := []struct {
		port, v uint64
		want    string
	}{
		{0x70, 0x0F, "shutdown status"},
		{0x70, 0x8F, "shutdown status"}, // NMI-disable bit ignored
		{0x70, 0x0B, "status B"},
		{0x64, 0xD1, "write output port"},
		{0x64, 0xFE, "pulse reset line"},
		{0x3D4, 0x0E, "cursor location high"},
		{0x3C4, 0x02, "map mask"},
		{0x1F7, 0xEC, "IDENTIFY DEVICE"},
		{0x177, 0x24, "READ SECTORS EXT"},
		{0x3FA, 0xC7, "enable FIFOs"},
		{0xCF9, 0x06, "hard reset"},
	}
	for _, tt := range idx {
		n, ok := d.PortIndex(tt.port, tt.v)
		if !ok || !strings.Contains(n, tt.want) {
			t.Errorf("PortIndex(%#x, %#x) = %q", tt.port, tt.v, n)
		}
	}
	if _, ok := d.PortIndex(0x70, 0x7F); ok {
		t.Error("CMOS 0x7F named")
	}
	if _, ok := d.PortIndex(0x500, 1); ok {
		t.Error("unknown port named")
	}
}

// The control word fields decode the PIT setup falcon-bios uses.
func TestPITControlWord(t *testing.T) {
	h, _ := Default().PortFor(0x43, true)
	got := setFields(h.Reg.Fields, 0xB6)
	var texts []string
	for _, f := range got {
		texts = append(texts, f.Text)
	}
	s := strings.Join(texts, " ")
	for _, w := range []string{"MODE=3 square wave", "ACCESS=lobyte/hibyte", "COUNTER=counter 2"} {
		if !strings.Contains(s, w) {
			t.Errorf("0xB6 decodes to %q, missing %q", s, w)
		}
	}
}
