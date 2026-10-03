package boot

import (
	"testing"
)

var modeCodes = []string{"mode-switch-order", "mode-switch-a20"}

func TestAiriesModeSwitchClean(t *testing.T) {
	m := headerMacros(t)
	for _, f := range []string{"pmode.S", "thunk.S", "aptramp.S", "rthunk.S", "stage2.S", "boot.S"} {
		got := lintText(t, fixture(t, f), opts{macros: m}, modeCodes...)
		for _, g := range got {
			t.Logf("%s: %s", f, g)
		}
	}
}

func TestPmodeMutations(t *testing.T) {
	m := headerMacros(t)
	base := fixture(t, "pmode.S")
	cases := []struct {
		name, from, to, code, msg string
	}{
		{"lgdt after the CR0 write", "\tlgdt    gdt_desc\n\tmovl    %cr0, %eax\n\torl     $1, %eax             /* CR0.PE */\n\tmovl    %eax, %cr0\n",
			"\tmovl    %cr0, %eax\n\torl     $1, %eax             /* CR0.PE */\n\tmovl    %eax, %cr0\n\tlgdt    gdt_desc\n",
			"mode-switch-order", "lgdt comes after the CR0.PE write"},
		{"far jump removed", "\tljmpl   $SEL_CODE32, $pmode_32\n", "", "mode-switch-order", "no far jump after setting CR0.PE"},
		{"near jump first", "\tljmpl   $SEL_CODE32, $pmode_32\n", "\tjmp 9f\n9:\tljmpl   $SEL_CODE32, $pmode_32\n", "mode-switch-order", "is `jmp`"},
		{"work before the jump", "\tmovl    %eax, %cr0\n\tljmpl", "\tmovl    %eax, %cr0\n\tnop\n\tnop\n\tnop\n\tnop\n\tljmpl", "mode-switch-order", "4 instructions run between"},
		{"sti", "\tlgdt    gdt_desc\n", "\tsti\n\tlgdt    gdt_desc\n", "mode-switch-order", "interrupts enabled"},
		{"ds used before reload", "pmode_32:\n", "pmode_32:\n\tmovl    (0x500), %ecx\n", "mode-switch-order", "%ds is used before %ds is reloaded"},
		{"stack before ss", "\tmovw    %ax, %ss\n\tmovw    %ax, %fs\n", "\tpushl   %eax\n\tmovw    %ax, %fs\n", "mode-switch-order", "the stack is used before %ss"},
		{"no A20", "\tmovw    $0x2401, %ax\n\tint     $0x15\n", "", "", ""},
	}
	for _, c := range cases {
		if !contains(base, c.from) {
			t.Fatalf("%s: %q not in fixture", c.name, c.from)
		}
		text := replace(base, c.from, c.to)
		got := lintText(t, text, opts{macros: m}, modeCodes...)
		if c.code == "" {
			continue
		}
		mustHave(t, got, c.code, c.msg)
	}
	// Removing both A20 paths leaves the file with no A20 at all.
	text := replace(replace(base, "\tmovw    $0x2401, %ax\n\tint     $0x15\n", ""), "\toutb    %al, $0x92\n", "")
	mustHave(t, lintText(t, text, opts{macros: m}, modeCodes...), "mode-switch-a20", "nothing in this file enables A20")
}

const (
	paeBlock = "\tmovl    %cr4, %eax\n\torl     $0x20, %eax          /* CR4.PAE */\n\tmovl    %eax, %cr4\n"
	cr3Block = "\tmovl    $pml4, %eax\n\tmovl    %eax, %cr3\n"
	lmeBlock = "\tmovl    $0xC0000080, %ecx\n\trdmsr\n\torl     $0x100, %eax         /* EFER.LME */\n\twrmsr\n"
	pgBlock  = "\tmovl    %cr0, %eax\n\torl     $0x80000000, %eax    /* CR0.PG */\n\tmovl    %eax, %cr0\n"
)

func TestLongModeSequence(t *testing.T) {
	base := fixture(t, "longmode.S")
	if got := lintText(t, base, opts{}, modeCodes...); len(got) != 0 {
		t.Fatalf("clean sequence: %v", got)
	}
	move := func(block string) string {
		// The block moves from its place to right after the PG write.
		s := replace(base, block, "")
		return replace(s, pgBlock, pgBlock+block)
	}
	cases := []struct {
		name, text, msg string
	}{
		{"PG before PAE", move(paeBlock), "CR4.PAE is set at line"},
		{"PG before CR3", move(cr3Block), "CR3 is loaded at line"},
		{"PG before LME", move(lmeBlock), "EFER.LME is set at line"},
		{"LME before PAE", replace(replace(base, paeBlock, ""), lmeBlock, lmeBlock+paeBlock), "after EFER.LME"},
		{"no far jump", replace(base, "\tljmp    $0x08, $lm64\n", ""), "no far jump after enabling long mode"},
		{"PAE known clear", replace(base, paeBlock, "\txorl    %eax, %eax\n\tmovl    %eax, %cr4\n"), "CR4.PAE is known clear"},
	}
	for _, c := range cases {
		mustHave(t, lintText(t, c.text, opts{}, modeCodes...), "mode-switch-order", c.msg)
	}
	// 32-bit paging (no LME anywhere) only needs CR3.
	paging := replace(replace(base, lmeBlock, ""), paeBlock, "")
	paging = replace(paging, "\tljmp    $0x08, $lm64\n", "\tljmp    $0x08, $pg32\n\t.code32\npg32:\n\thlt\n")
	if got := lintText(t, paging, opts{}, modeCodes...); len(got) != 0 {
		t.Fatalf("32-bit paging: %v", got)
	}
}

func TestThunkMutations(t *testing.T) {
	m := headerMacros(t)
	base := fixture(t, "thunk.S")
	if got := lintText(t, base, opts{macros: m}, modeCodes...); len(got) != 0 {
		t.Fatalf("thunk.S: %v", got)
	}
	cases := []struct {
		name, from, to, msg string
	}{
		{"PE cleared in 32-bit code", "\tcli\n\tljmp    $SEL_CODE16, $thunk_16\n",
			"\tcli\n\tmovl    %cr0, %eax\n\tandl    $0xFFFFFFFE, %eax\n\tmovl    %eax, %cr0\n\tljmp    $SEL_CODE16, $thunk_16\n",
			"CR0.PE is cleared in .code32 code"},
		{"real-mode target is 32-bit", "\tljmp    $0, $thunk_real\n", "\tljmp    $0, $thunk_back32\n", "lands in .code32 code"},
		{"ds not reloaded in real mode", "thunk_real:\n\txorw    %ax, %ax\n\tmovw    %ax, %ds\n", "thunk_real:\n\txorw    %ax, %ax\n",
			"%ds is used before %ds is reloaded after the return to real mode"},
		{"lgdt after the ascent's PE write", "\tlgdt    gdt_desc\n\n\tmovl    %cr0, %eax\n\torl     $1, %eax             /* CR0.PE */\n\tmovl    %eax, %cr0\n",
			"\n\tmovl    %cr0, %eax\n\torl     $1, %eax             /* CR0.PE */\n\tmovl    %eax, %cr0\n\tlgdt    gdt_desc\n", "lgdt comes after"},
	}
	for _, c := range cases {
		if !contains(base, c.from) {
			t.Fatalf("%s: %q not in fixture", c.name, c.from)
		}
		mustHave(t, lintText(t, replace(base, c.from, c.to), opts{macros: m}, modeCodes...), "mode-switch-order", c.msg)
	}
}
