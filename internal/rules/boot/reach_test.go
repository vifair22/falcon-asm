package boot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

var reachCodes = []string{"real-mode-reach", "real-mode-layout"}

var allCodes = append(append(append([]string{}, modeCodes...), loaderCodes...), reachCodes...)

// Every rule of the package is quiet at Warning on the airies-boot files
// (the fixtures are verbatim copies); the information findings are the
// ones the real tree gets.
func TestAiriesFixturesPinned(t *testing.T) {
	m := headerMacros(t)
	want := map[string]string{
		"aptramp.S": "mode-switch-a20",
		"boot.S":    "loader-sector-count-unproven",
		"boot_fd.S": "loader-sector-count-unproven",
	}
	dir := t.TempDir()
	for _, f := range []string{"pmode.S", "thunk.S", "aptramp.S", "rthunk.S", "stage2.S", "boot.S", "boot_fd.S"} {
		text := strings.ReplaceAll(fixture(t, f), "#include <start/biosdefs.h>", "")
		var got []string
		for _, g := range lintAssembled(t, dir, f, text, m, allCodes...) {
			if g.Severity <= 2 {
				t.Errorf("%s: %s", f, g)
			}
			got = append(got, g.Code)
		}
		if strings.Join(got, " ") != want[f] {
			t.Errorf("%s: findings %v, want %q", f, got, want[f])
		}
	}
}

func TestRealModeReach(t *testing.T) {
	cases := []struct {
		name, text, msg string
	}{
		{"beyond the HMA", "\t.code16\n\tmovw $0xFFFF, %ax\n\tmovw %ax, %es\n\tmovl $0x20000, %edi\n\tmovb %es:(%edi), %al\n", "linear address 0x11fff0"},
		{"16-bit wrap", "\t.code16\n\txorw %ax, %ax\n\tmovw %ax, %ds\n\tmovw $0xFFF0, %bx\n\tmovw 0x20(%bx), %ax\n", "wraps to 0x10"},
		{"stack segment by bp", "\t.code16\n\tmovw $0xFFFF, %ax\n\tmovw %ax, %ss\n\tmovl $0x10000, %ebp\n\tmovl (%ebp), %eax\n", "%ss 0xffff"},
		{"index scaled", "\t.code16\n\txorw %ax, %ax\n\tmovw %ax, %ds\n\tmovw $0x8000, %si\n\tmovw $0x1000, %bx\n\tmovw (%bx,%si), %ax\n\tmovl $0x10, %ecx\n\tmovl $0xFFFFFF0, %ebx\n\tmovl (%ebx,%ecx,4), %eax\n", "beyond what real mode reaches"},
	}
	for _, c := range cases {
		mustHave(t, lintText(t, c.text, opts{}, reachCodes...), "real-mode-reach", c.msg)
	}
	quiet := []string{
		// Unknown segment, unknown register, symbolic displacement.
		"\t.code16\n\tmovl $0x20000, %edi\n\tmovb (%edi), %al\n",
		"\t.code16\n\txorw %ax, %ax\n\tmovw %ax, %ds\n\tmovb (%di), %al\n",
		"\t.code16\n\txorw %ax, %ax\n\tmovw %ax, %ds\n\tmovb foo, %al\nfoo:\t.byte 0\n",
		// The top of the HMA itself, and lea which reads nothing.
		"\t.code16\n\tmovw $0xFFFF, %ax\n\tmovw %ax, %ds\n\tmovw $0xFFFF, %bx\n\tmovb (%bx), %al\n\tleaw 2(%bx), %si\n",
		// 32-bit code is not real mode.
		"\t.code32\n\tmovw $0xFFFF, %ax\n\tmovw %ax, %ds\n\tmovl $0x20000, %edi\n\tmovb (%edi), %al\n",
		// A file that clears PE may be in unreal mode on purpose.
		"\t.code16\n\tmovl %cr0, %eax\n\tandl $0xFFFFFFFE, %eax\n\tmovl %eax, %cr0\n\tljmp $0, $1f\n1:\tmovw $0xFFFF, %ax\n\tmovw %ax, %es\n\tmovl $0x20000, %edi\n\tmovb %es:(%edi), %al\n",
	}
	for _, q := range quiet {
		if got := lintText(t, q, opts{}, reachCodes...); len(got) != 0 {
			t.Errorf("%q: %v", q, got)
		}
	}
	// 16-bit protected mode (the thunk's thunk_16) is not real mode: its
	// data selectors are not segments.
	pm := "\t.code32\n\tljmp $0x18, $pm16\n\t.code16\npm16:\n\tmovw $0xFFFF, %ax\n\tmovw %ax, %ds\n\tmovl $0x20000, %edi\n\tmovb (%edi), %al\n"
	if got := lintText(t, pm, opts{}, reachCodes...); len(got) != 0 {
		t.Errorf("16-bit protected mode: %v", got)
	}
}

func TestParseLD(t *testing.T) {
	src := `/* comment */ SECTIONS {
	. = 0x7C00;
	.text : { *(.text.stage1) *(.text.stage2) *(.text*) }
	.bss (NOLOAD) : { *(.bss*) }
	.hi 0x100000 : AT(0x8000) { KEEP(*(.text.stage3)) }
	PROVIDE(x = .);
	. = 0x200000;
	.data : { *(.data.hi) }
	/DISCARD/ : { *(.comment) }
}`
	got := parseLD(src)
	want := map[string]uint64{".text.stage1": 0x7C00, ".text.stage3": 0x100000, ".data.hi": 0x200000}
	if len(got) != len(want) {
		t.Fatalf("parseLD = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#x, want %#x", k, got[k], v)
		}
	}
	if parseLD("MEMORY { }") != nil {
		t.Fatal("no SECTIONS")
	}
	if len(parseLD("SECTIONS { .a : { *(.x) ")) != 0 {
		t.Fatal("unterminated")
	}
}

// Scripts are found beside the file and above it; two scripts placing a
// section differently give it no address.
func TestSectionAddresses(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "src", "start")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(sub, "a.ld"), "SECTIONS { .t 0x1000 : { *(.text.a) } .u 0x3000 : { *(.text.c) } }")
	write(filepath.Join(root, "b.lds"), "SECTIONS { .t 0x2000 : { *(.text.b) } .u 0x4000 : { *(.text.c) } }")
	p := parse(t, toolchain.PathToURI(filepath.Join(sub, "x.S")), "\tnop\n", asm.Mode16, nil)
	got := sectionAddresses(p)
	if got[".text.a"] != 0x1000 || got[".text.b"] != 0x2000 {
		t.Fatalf("addresses = %v", got)
	}
	if _, ok := got[".text.c"]; ok {
		t.Fatal("conflicting placement kept")
	}
	// Cached: the same map comes back for an unchanged script.
	if a, b := loadLD(filepath.Join(sub, "a.ld")), loadLD(filepath.Join(sub, "a.ld")); a[".text.a"] != b[".text.a"] {
		t.Fatal("cache")
	}
	if sectionAddresses(parse(t, "untitled:1", "\tnop\n", asm.Mode16, nil)) != nil {
		t.Fatal("untitled document")
	}
}

// Real-mode code past the end of its 64 KiB segment.
func TestSegmentCross(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rm.ld"), []byte("SECTIONS { .text 0xF000 : { *(.text.rm) } }"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "\t.code16\n\t.section .text.rm, \"ax\"\n\t.globl _start\n_start:\n\tljmp $0, $entry\nentry:\n\tnop\n\t.fill 0x1000, 1, 0x90\nlast:\n\tnop\n\thlt\n"
	got := lintAssembled(t, dir, "rm.S", src, nil, "real-mode-layout")
	mustHave(t, got, "real-mode-layout", "past 0xFFFF, IP wraps")
	// Shorter, it ends below 0x10000.
	ok := strings.Replace(src, ".fill 0x1000, 1", ".fill 0x100, 1", 1)
	if got := lintAssembled(t, dir, "rm2.S", ok, nil, "real-mode-layout"); len(got) != 0 {
		t.Fatalf("fits: %v", got)
	}
	// Longer than a segment whatever the addresses.
	long := "\t.code16\n\t.section .text.big, \"ax\"\nbig:\n\tnop\n\t.fill 0x10010, 1, 0x90\n\tnop\n\thlt\n"
	mustHave(t, lintAssembled(t, dir, "big.S", long, nil, "real-mode-layout"), "real-mode-layout", "more than one 64 KiB code segment")
}
