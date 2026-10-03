package boot

import (
	"strings"
	"testing"
)

// loads=<label> measures to the end label, or to the end of the section.
func TestStageLabelSize(t *testing.T) {
	dir := t.TempDir()
	base := "\t.code16\n_start:\n\t# falcon-asm: loads=STAGE\n\txorw %ax, %ax\n\tmovw %ax, %es\n\tmovw $0x0202, %ax\n\tmovw $0x7E00, %bx\n" +
		"\tint $0x13\n\tljmp $0, $0x7E00\n\t. = _start + 510\n\t.byte 0x55, 0xAA\n"
	cases := []struct{ stage, tail, msg string }{
		{"s2", "s2:\n\t.fill 1100, 1, 0\ns2_end:\n\t.fill 100, 1, 0\n", "s2 is 1100 bytes"},
		{"s2_start", "s2_start:\n\t.fill 1200, 1, 0\ns2_end:\n", "s2_start is 1200 bytes"},
		{"s2", "s2:\n\t.fill 1300, 1, 0\n", "s2 is 1300 bytes"},
	}
	for _, c := range cases {
		text := strings.Replace(base, "STAGE", c.stage, 1) + c.tail
		mustHave(t, lintAssembled(t, dir, "s.S", text, nil, loaderCodes...), "loader-sector-count", c.msg)
	}
	// An ordinary label with an unlisted offset (a different section with
	// no bytes): the size is unknown.
	text := strings.Replace(base, "STAGE", "s3", 1) + "\t.section .other, \"a\"\ns3:\n"
	mustHave(t, lintAssembled(t, dir, "s.S", text, nil, loaderCodes...), "loader-sector-count-unproven", "size of s3")
}

// The DAP read through lea, and a packet that is not constants.
func TestDAPVariants(t *testing.T) {
	dir := t.TempDir()
	src := "\t.code16\n_start:\n\t# falcon-asm: loads=s2\n\tleaw dap, %si\n\tmovb $0x42, %ah\n\tint $0x13\n\tljmp $0, $0x7E00\n" +
		"dap:\t.byte 0x10, 0\n\t.word 1, 0x7E00, 0\n\t.quad 1\n\t. = _start + 510\n\t.byte 0x55, 0xAA\ns2:\n\t.fill 1000, 1, 0\n"
	mustHave(t, lintAssembled(t, dir, "d.S", src, nil, loaderCodes...), "loader-sector-count", "reads 1 sectors")
	sym := strings.Replace(src, ".word 1, 0x7E00, 0", ".word 1, s2, 0", 1)
	mustHave(t, lintAssembled(t, dir, "d.S", sym, nil, loaderCodes...), "loader-sector-count-unproven", "not laid out as constants")
	code := strings.Replace(src, "dap:\t.byte 0x10, 0\n", "dap:\tnop\n\t.byte 0x10, 0\n", 1)
	mustHave(t, lintAssembled(t, dir, "d.S", code, nil, loaderCodes...), "loader-sector-count-unproven", "not laid out as constants")
}

// The walk after a PE write stops quietly at data, another section or a
// macro, and the reload walk at anything it does not follow.
func TestModeSwitchWalkEdges(t *testing.T) {
	pre := "\t.code16\n\tcli\n\tlgdt gd\n\tmovl %cr0, %eax\n\torl $1, %eax\n\tmovl %eax, %cr0\n"
	quiet := []string{
		pre + "\t.byte 0xEA\n",
		pre + "\t.section .other, \"ax\"\n\tnop\n",
		pre + "\t.balign 4\n\t.globl x\nx:\n\tljmpl *ptr\n",
		pre + "\tljmpl $8, $pm\n\t.code32\npm:\n\t.byte 0\n",
		pre + "\tljmpl $8, $pm\n\t.code32\npm:\n\tmovw $0x10, %ax\n\tmovw %ax, %ds\n\tmovw %ax, %es\n\tmovw %ax, %ss\n\tmovl (0x500), %eax\n\tpushl %eax\n",
		pre + "\tljmpl $8, $pm\n\t.code32\npm:\n\tleal (%esi), %eax\n\tjmp elsewhere\n",
		pre + "\tljmpl $8, $pm\n\t.code32\npm:\n\tmovw $0x10, %ax\n\tmovw %ax, %ds\n\tlss stk, %esp\n\tpushl %eax\n\tcall f\n",
	}
	for _, q := range quiet {
		if got := lintText(t, q, opts{}, "mode-switch-order"); len(got) != 0 {
			t.Errorf("%q: %v", q, got)
		}
	}
	// String instructions use DS and ES implicitly.
	str := pre + "\tljmpl $8, $pm\n\t.code32\npm:\n\tmovw $0x10, %ax\n\tmovw %ax, %ds\n\tstosb\n"
	mustHave(t, lintText(t, str, opts{}, "mode-switch-order"), "mode-switch-order", "%es is used before")
	// Falling into data after clearing PE in 16-bit code: nothing to say.
	clr := "\t.code16\n\tmovl %cr0, %eax\n\tandl $0xFFFFFFFE, %eax\n\tmovl %eax, %cr0\n\tljmp $0, $rm\nrm:\n\t.byte 0\n"
	if got := lintText(t, clr, opts{}, "mode-switch-order"); len(got) != 0 {
		t.Errorf("clear: %v", got)
	}
}

// addr32 makes a 16-bit register form 32-bit addressing.
func TestReachAddr32(t *testing.T) {
	src := "\t.code16\n\tmovw $0xFFFF, %ax\n\tmovw %ax, %ds\n\tmovl $0x20000, %ebx\n\taddr32 movb 0x10, %al\n"
	_ = lintText(t, src, opts{}, "real-mode-reach")
	p := parse(t, "file:///a.S", "\taddr32 movb (%bx), %al\n", 16, nil)
	if !hasPrefix(&p.Stmts[0], "addr32") || hasPrefix(&p.Stmts[0], "data16") {
		t.Fatalf("prefixes = %v", p.Stmts[0].Prefixes)
	}
}
