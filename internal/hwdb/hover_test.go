package hwdb

import (
	"strings"
	"testing"
)

func pureASCII(t *testing.T, what, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			t.Errorf("%s: non-ASCII byte at %d", what, i)
			return
		}
	}
}

func contains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	pureASCII(t, what, got)
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in:\n%s", what, w, got)
		}
	}
}

func TestPortHover(t *testing.T) {
	d := Default()
	contains(t, "0x92", d.PortHover(0x92, 8, true),
		"**0x92** - System control port A (fast A20 and reset)", "chipset - 8-bit - read/write",
		"`0` FAST\\_RESET", "`1` A20", "Notes:", "andb $0xFE, %al")
	contains(t, "0x92 word", d.PortHover(0x92, 16, true), "Warning: 16-bit access; the register is 8-bit")
	contains(t, "0x64 in", d.PortHover(0x64, 8, false), "KBC status", "`1` IBF", "Also at this port: KBC command (write)")
	contains(t, "0x64 out", d.PortHover(0x64, 8, true), "KBC command", "`0xD1` write output port", "Also at this port: KBC status (read)")
	contains(t, "0x3F8", d.PortHover(0x3F8, 8, true), "COM1 THR", "at base 0x3F8 + 0", "COM1 DLL (read/write, DLAB=1)")
	contains(t, "0x80 read", d.PortHover(0x80, 8, false), "write-only register")
	contains(t, "0x3DA write", d.PortHover(0x3DA, 8, true), "VGA feature control")
	contains(t, "0x2FD write", d.PortHover(0x2FD, 8, true), "read-only register")
	contains(t, "0xCF8", d.PortHover(0xCF8, 32, true), "32-bit", "`31` ENABLE", "`23:16` BUS")
	contains(t, "0xCF9", d.PortHover(0xCF9, 8, true), "Values: `0x02` arm hard reset", "_Confidence: chipset register")
	contains(t, "0xF4", d.PortHover(0xF4, 8, true), "_Confidence: emulator only")
	contains(t, "0x3F9 DLAB", d.PortHover(0x3F9, 0, false), "COM1 IER")
	contains(t, "0x70", d.PortHover(0x70, 8, true), "Registers: `0x00` seconds", "`0x0F` shutdown status")
	if s := d.PortHover(0x500, 8, true); s != "" {
		t.Errorf("unknown port hover %q", s)
	}
}

func TestMSRHover(t *testing.T) {
	d := Default()
	contains(t, "EFER", d.MSRHover(0xC0000080, nil),
		"**MSR 0xC0000080** - IA32\\_EFER (EFER)", "`8` LME", "`10` LMA - long mode active (set by the CPU) (read-only)",
		"Reserved (must be 0): `0x2FE`", "Notes:")
	v := uint64(0x503)
	contains(t, "EFER value", d.MSRHover(0xC0000080, &v), "Value `0x503`", "Warning: reserved bits 0x2 set")
	contains(t, "MTRRCAP", d.MSRHover(0xFE, nil), "read-only")
	contains(t, "VM_CR", d.MSRHover(0xC0010114, nil), "Amd only")
	contains(t, "PHYSBASE2", d.MSRHover(0x204, nil), "IA32\\_MTRR\\_PHYSBASE2", "(0 UC, 1 WC, 4 WT, 5 WP, 6 WB)")
	contains(t, "PRED_CMD", d.MSRHover(0x49, nil), "write-only")
	contains(t, "SYSCFG", d.MSRHover(0xC0010010, nil), "_Confidence")
	if s := d.MSRHover(0x5, nil); s != "" {
		t.Errorf("unknown MSR hover %q", s)
	}
}

func TestBIOSHover(t *testing.T) {
	d := Default()
	contains(t, "INT 13h AH=42h", d.BIOSHover(0x13, 0x42, 0, false),
		"**INT 13h AH=42h** - Extended read (Disk services)", "Since: EDD 1.0", "`DS:SI` disk address packet",
		"Errors: CF set on error, AH = status", "| 0 | 1 | size |", "Quirks:", "127 (7Fh)")
	contains(t, "E820", d.BIOSHover(0x15, 0xE8, 0x20, true),
		"INT 15h AX=E820h", "`EDX` 534D4150h ('SMAP')", "may destroy edx on some BIOSes", "Errors: CF set on error")
	contains(t, "E8 group", d.BIOSHover(0x15, 0xE8, 0, false), "AL selects the function", "`AX=E820h` Query system address map")
	contains(t, "INT 10h teletype", d.BIOSHover(0x10, 0x0E, 0, false), "may destroy bp on some BIOSes")
	contains(t, "INT 13h AH=08h", d.BIOSHover(0x13, 0x08, 0, false), "Registers: destroys AX, BL, ES, DI")
	contains(t, "INT 15h AH=86h", d.BIOSHover(0x15, 0x86, 0, false), "Interrupts: enables interrupts")
	contains(t, "INT 11h", d.BIOSHover(0x11, 0, 0, false), "Result bits:", "AX `1` X87")
	contains(t, "VBE", d.BIOSHover(0x10, 0x4F, 0x01, true), "AX != 004Fh", "VBE mode information")
	contains(t, "INT 14h", d.BIOSHover(0x14, 0x00, 0, false), "AH bit 7 set = timeout")
	contains(t, "INT 13h AH=4Bh", d.BIOSHover(0x13, 0x4B, 0, false), "_Confidence")
	contains(t, "unknown function", d.BIOSHover(0x13, 0x77, 0, false), "**INT 13h** - Disk services", "AH=77h is not in the table", "Quirks:")
	if s := d.BIOSHover(0x21, 0x09, 0, false); s != "" {
		t.Errorf("DOS hover %q", s)
	}
	contains(t, "dap struct", d.StructHover("dap"), "(16 or 24 bytes)", "| 16 | 8 | buffer64 |", "Notes:")
	if d.StructHover("nope") != "" {
		t.Error("unknown struct hover")
	}
}

func TestCPUIDHover(t *testing.T) {
	d := Default()
	contains(t, "leaf 7", d.CPUIDHover(7, 0), "**CPUID.(EAX=07h,ECX=0)** - Structured extended features", "`5` AVX2 (v3)", "`16` AVX512F (v4)")
	contains(t, "leaf 1", d.CPUIDHover(1, 0), "`23` POPCNT (v2)", "`22` MOVBE (v3)", "EDX: `0` FPU,")
	contains(t, "leaf 0", d.CPUIDHover(0, 0), "Notes:", "EFLAGS bit 21")
	contains(t, "ext", d.CPUIDHover(0x80000001, 0), "`29` LM", "`5` ABM (v3)")
	if d.CPUIDHover(0x99, 0) != "" {
		t.Error("unknown leaf hover")
	}
}

func TestDecodeHover(t *testing.T) {
	d := Default()
	contains(t, "gdt", d.DecodeHover("descriptor", 0x00CF9A000000FFFF), "**code32 (code readable)", "`0xCF9A000000FFFF`", "Notes:")
	contains(t, "bad cr0", d.DecodeHover("cr0", 0x80000000), "Warning:", "PG set without PE")
	if d.DecodeHover("cr9", 0) != "" {
		t.Error("unknown kind hover")
	}
}

func TestMarkdownHelpers(t *testing.T) {
	if got := mdEscape("a_b*c"); got != "a\\_b\\*c" {
		t.Errorf("mdEscape = %q", got)
	}
	if got := mdText("use `a_b` then x_y"); got != "use `a_b` then x\\_y" {
		t.Errorf("mdText = %q", got)
	}
	if got := mdText("odd ` tick_x"); got != "odd \\` tick\\_x" {
		t.Errorf("mdText odd = %q", got)
	}
	if got := mdCode("a`b"); got != "``a`b``" {
		t.Errorf("mdCode = %q", got)
	}
	if mdList(nil) != "" || notesBlock("x", nil) != "" || valuesLine("", nil) != "" {
		t.Error("empty helpers")
	}
	if got := valuesLine("", []Enum{{V: 1, Name: "one"}}); got != "Values: `0x01` one" {
		t.Errorf("valuesLine = %q", got)
	}
	if got := dedupe([]string{"BP", "bp", "si"}); len(got) != 2 {
		t.Errorf("dedupe = %v", got)
	}
}
