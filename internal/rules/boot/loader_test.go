package boot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/toolchain"
)

var loaderCodes = []string{"loader-sector-count", "loader-sector-count-unproven"}

// The documented examples, with a real listing.
func TestDocExamples(t *testing.T) {
	dir := t.TempDir()
	for _, code := range []string{"mode-switch-order", "mode-switch-a20", "loader-sector-count", "loader-sector-count-unproven", "real-mode-reach", "real-mode-layout"} {
		r, _ := lint.Lookup(code)
		if code == "real-mode-layout" {
			if err := os.WriteFile(filepath.Join(dir, "link.ld"), []byte("SECTIONS { .lo 0x7C00 : { *(.text.lo) } .hi 0x200000 : { *(.data.hi) } }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		bad := lintAssembled(t, dir, code+"-bad.S", r.Doc.Bad, nil, code)
		good := lintAssembled(t, dir, code+"-good.S", r.Doc.Good, nil, code)
		if len(bad) == 0 {
			t.Errorf("%s: Bad example not reported", code)
		}
		if len(good) != 0 {
			t.Errorf("%s: Good example reported: %v", code, good)
		}
	}
}

// airies-boot's stage 1 loaders read stage 2 in a loop with a count the
// installer writes into the sector: information, never a warning.
func TestAiriesLoaders(t *testing.T) {
	m := headerMacros(t)
	dir := t.TempDir()
	for _, name := range []string{"boot.S", "boot_fd.S"} {
		got := lintAssembled(t, dir, name, strings.ReplaceAll(fixture(t, name), "#include <start/biosdefs.h>", ""), m, loaderCodes...)
		if len(got) != 1 || got[0].Code != "loader-sector-count-unproven" || !strings.Contains(got[0].Message, "0x7e00") ||
			!strings.Contains(got[0].Message, "loop") {
			t.Errorf("%s: %v", name, got)
		}
	}
	// With the stage named and STAGE2_SECTORS standing in for the count
	// from memory, the fit is checked: stage 2 here is 2 KiB.
	stage := "\t.code16\n\t.globl stage2_start\nstage2_start:\n\t.fill 2048, 1, 0x90\nstage2_end:\n"
	if err := os.WriteFile(filepath.Join(dir, "stage2.bin"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = stage
	text := strings.ReplaceAll(fixture(t, "boot.S"), "#include <start/biosdefs.h>", "/* falcon-asm: loads=stage2.bin */")
	if got := lintAssembled(t, dir, "boot.S", text, m, loaderCodes...); len(got) != 0 {
		t.Errorf("annotated, fits: %v", got)
	}
	small := map[string]string{}
	for k, v := range m {
		small[k] = v
	}
	small["STAGE2_SECTORS"] = "3"
	got := lintAssembled(t, dir, "boot.S", text, small, loaderCodes...)
	mustHave(t, got, "loader-sector-count", "reads 3 sectors (1536 bytes), but stage2.bin is 2048 bytes (4 sectors)")
	// sectors= overrides the macro.
	text2 := strings.Replace(text, "loads=stage2.bin", "loads=stage2.bin sectors=2", 1)
	mustHave(t, lintAssembled(t, dir, "boot.S", text2, m, loaderCodes...), "loader-sector-count", "reads 2 sectors")
}

const chsLoader = "\t.code16\n\t.globl _start\n_start:\n\txorw %ax, %ax\n\tmovw %ax, %es\n" +
	"retry:\n\tmovw $0x02CNT, %ax\n\tmovw $0x0002, %cx\n\txorw %dx, %dx\n\tmovw $0x7E00, %bx\n\tint $0x13\n\tjc retry\n" +
	"\tljmp $0, $0x7E00\n\t. = _start + 510\n\t.byte 0x55, 0xAA\nstage2:\n\t.fill SIZE, 1, 0x90\n"

func chs(sectors, size int) string {
	s := strings.Replace(chsLoader, "CNT", twoDigits(sectors), 1)
	return strings.Replace(s, "SIZE", itoa(size), 1)
}

func twoDigits(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// A CHS read in a retry loop (back to the read only when CF is set) still
// has its count: the stage after the boot sector is the rest of the
// section.
func TestCHSRetryLoop(t *testing.T) {
	dir := t.TempDir()
	if got := lintAssembled(t, dir, "ok.S", chs(3, 1500), nil, loaderCodes...); len(got) != 0 {
		t.Fatalf("fits: %v", got)
	}
	got := lintAssembled(t, dir, "short.S", chs(2, 1500), nil, loaderCodes...)
	f := mustHave(t, got, "loader-sector-count", "reads 2 sectors (1024 bytes), but the rest of section .text (from offset 0x200) is 1500 bytes (3 sectors)")
	if len(f.Related) == 0 {
		t.Fatal("no related location for the count")
	}
}

// A disk address packet laid out as constants: its count word is the
// count; written at run time, it is not.
func TestDAPLoader(t *testing.T) {
	dir := t.TempDir()
	src := "\t.code16\n\t.globl _start\n_start:\n\t# falcon-asm: loads=stage2_start\n\tmovw $dap, %si\n\tmovb $0x42, %ah\n\tint $0x13\n" +
		"\tljmp $0, $0x7E00\ndap:\t.byte 0x10, 0\n\t.word COUNT, 0x7E00, 0\n\t.quad 1\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n" +
		"stage2_start:\n\t.fill 1100, 1, 0x90\nstage2_end:\n\t.fill 300, 1, 0\n"
	if got := lintAssembled(t, dir, "dap.S", strings.Replace(src, "COUNT", "3", 1), nil, loaderCodes...); len(got) != 0 {
		t.Fatalf("fits: %v", got)
	}
	got := lintAssembled(t, dir, "dap2.S", strings.Replace(src, "COUNT", "2", 1), nil, loaderCodes...)
	mustHave(t, got, "loader-sector-count", "but stage2_start is 1100 bytes")
	// The packet's count written at run time: unknown.
	rt := strings.Replace(strings.Replace(src, "COUNT", "3", 1), "\tmovb $0x42, %ah\n", "\tmovw %cx, 2(%si)\n\tmovb $0x42, %ah\n", 1)
	got = lintAssembled(t, dir, "dap3.S", rt, nil, loaderCodes...)
	mustHave(t, got, "loader-sector-count-unproven", "written at run time")
	// SI not a packet.
	nosi := strings.Replace(strings.Replace(src, "COUNT", "3", 1), "\tmovw $dap, %si\n", "", 1)
	mustHave(t, lintAssembled(t, dir, "dap4.S", nosi, nil, loaderCodes...), "loader-sector-count-unproven", "DS:SI does not name")
}

// A loader whose stage is an assembly source of its own, measured by
// assembling it (the toolchain call is stubbed here; the real one needs a
// compile database).
func TestStageFromSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stage2.S"), []byte("\tnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := assembleFile
	t.Cleanup(func() { assembleFile = old })
	calls := 0
	assembleFile = func(path string) (map[string]uint64, error) {
		calls++
		return map[string]uint64{".text": 1400, ".data": 200, ".bss": 4096, ".symtab": 99, ".rela.text": 12}, nil
	}
	src := strings.Replace(chs(3, 10), "\t.globl _start\n", "\t.globl _start\n\t# falcon-asm: loads=stage2.S\n", 1)
	got := lintAssembled(t, dir, "ld.S", src, nil, loaderCodes...)
	mustHave(t, got, "loader-sector-count", "stage2.S is 1600 bytes")
	lintAssembled(t, dir, "ld.S", src, nil, loaderCodes...)
	if calls != 1 {
		t.Fatalf("assembled %d times; the size is cached", calls)
	}
	assembleFile = func(string) (map[string]uint64, error) { return nil, errors.New("no compile flags") }
	if err := os.WriteFile(filepath.Join(dir, "stage3.S"), []byte("\tnop\n\tnop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src = strings.Replace(src, "loads=stage2.S", "loads=stage3.S", 1)
	mustHave(t, lintAssembled(t, dir, "ld.S", src, nil, loaderCodes...), "loader-sector-count-unproven", "could not be assembled")
	src = strings.Replace(src, "loads=stage3.S", "loads=missing.bin", 1)
	mustHave(t, lintAssembled(t, dir, "ld.S", src, nil, loaderCodes...), "loader-sector-count-unproven", "neither a label")
}

func TestLoaderQuiet(t *testing.T) {
	dir := t.TempDir()
	// Not a boot sector, not annotated: a disk read is not a stage load.
	plain := "\t.code16\n\tmovw $0x0201, %ax\n\tint $0x13\n\tret\n"
	if got := lintAssembled(t, dir, "p.S", plain, nil, loaderCodes...); len(got) != 0 {
		t.Fatal(got)
	}
	// A boot sector chaining to another boot sector at 0x7C00.
	chain := "\t.code16\n_start:\n\tmovw $0x0201, %ax\n\tmovw $0x7C00, %bx\n\tint $0x13\n\tljmp $0, $0x7C00\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n"
	if got := lintAssembled(t, dir, "c.S", chain, nil, loaderCodes...); len(got) != 0 {
		t.Fatal(got)
	}
	// No listing: the rules do not run.
	if got := lintText(t, chs(2, 1500), opts{}, loaderCodes...); len(got) != 0 {
		t.Fatal(got)
	}
	if loadedSize(map[string]uint64{".text": 5, ".note.GNU-stack": 0, ".comment": 9}) != 5 {
		t.Fatal("loadedSize")
	}
	_ = toolchain.PathToURI
}
