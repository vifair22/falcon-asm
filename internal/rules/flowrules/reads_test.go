package flowrules

import (
	"sort"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

func TestReadsOf(t *testing.T) {
	cases := []struct {
		mode asm.Mode
		src  string
		want string // register parts read, sorted
	}{
		{asm.Mode16, "movw %ax, %bx", "ax"},
		{asm.Mode16, "addw %ax, %bx", "ax bx"},
		{asm.Mode16, "xorw %ax, %ax", ""},
		{asm.Mode16, "movw (%bx,%si), %ax", "bx si"},
		{asm.Mode16, "popw %ax", ""},
		{asm.Mode16, "pushw %ax", "ax"},
		{asm.Mode16, "cmpb %ah, %al", "ah al"},
		{asm.Mode16, "rep movsb", "cx di si"},
		{asm.Mode32, "rep stosl", "eax ecx edi"},
		{asm.Mode16, "stosb", "al di"},
		{asm.Mode16, "lodsw", "si"},
		{asm.Mode16, "scasw", "ax di"},
		{asm.Mode16, "insb", "di dx"},
		{asm.Mode16, "outsb", "dx si"},
		{asm.Mode16, "loop 1f\n1:", "cx"},
		{asm.Mode16, "jcxz 1f\n1:", "cx"},
		{asm.Mode32, "jecxz 1f\n1:", "ecx"},
		{asm.Mode64, "jrcxz 1f\n1:", "rcx"},
		{asm.Mode16, "mulb %bl", "al bl"},
		{asm.Mode16, "divb %bl", "ax bl"},
		{asm.Mode16, "divw %bx", "ax bx dx"},
		{asm.Mode32, "imull %ecx", "eax ecx"},
		{asm.Mode32, "imull $3, %ecx, %eax", "ecx"},
		{asm.Mode16, "cbw", "al"},
		{asm.Mode16, "cwd", "ax"},
		{asm.Mode32, "cltd", "eax"},
		{asm.Mode16, "xlat", "al bx"},
		{asm.Mode32, "rdmsr", "ecx"},
		{asm.Mode32, "wrmsr", "eax ecx edx"},
		{asm.Mode16, "sahf", "ah"},
		{asm.Mode16, "leave", "bp"},
		{asm.Mode16, "outb %al, %dx", "al dx"},
		{asm.Mode16, "inb %dx, %al", "dx"},
		{asm.Mode16, "leaw 2(%bx), %ax", "bx"},
		{asm.Mode64, "movq (%rip), %rax", ""},
		{asm.Mode16, "movw %ax, %ds", "ax"},
		{asm.Mode16, "addr32 rep movsb", "ecx edi esi"},
		{asm.Mode32, "addr16 lodsb", "si"},
		{asm.Mode64, "addr32 stosb", "al edi"},
	}
	for _, c := range cases {
		p := prog("\t"+c.src+"\n", c.mode)
		var names []string
		for _, u := range readsOf(p, 0) {
			names = append(names, u.name())
		}
		sort.Strings(names)
		if got := strings.Join(names, " "); got != c.want {
			t.Errorf("%s: reads %q, want %q", c.src, got, c.want)
		}
	}
	// Not an instruction.
	if got := readsOf(prog("x:\n", asm.Mode16), 0); len(got) != 0 {
		t.Errorf("label reads %v", got)
	}
	// High-byte names and widths.
	u := regRead{r: flow.RDX, lo: 8, hi: 15}
	if u.name() != "dh" || u.width() != 8 || u.bits() != 0xFF00 {
		t.Errorf("dh: %s %d %#x", u.name(), u.width(), u.bits())
	}
	if u := (regRead{r: flow.RCX, lo: 8, hi: 15}); u.name() != "ch" {
		t.Errorf("ch: %s", u.name())
	}
	if u := (regRead{r: flow.RBX, lo: 8, hi: 15}); u.name() != "bh" {
		t.Errorf("bh: %s", u.name())
	}
	if u := (regRead{r: flow.RAX, lo: 0, hi: 63}); u.bits() != ^uint64(0) {
		t.Errorf("rax bits %#x", u.bits())
	}
}

func TestHelpers(t *testing.T) {
	if bitRange(0, 63) != ^uint64(0) || bitRange(8, 15) != 0xFF00 {
		t.Error("bitRange")
	}
	for _, s := range []string{"510", "0x1FE", "_start + 510", " _start+0x1fe "} {
		if !endsWith510(s) {
			t.Errorf("endsWith510(%q)", s)
		}
	}
	if endsWith510("0x0510") || endsWith510("440") {
		t.Error("endsWith510 false positive")
	}
	for _, c := range []struct {
		text string
		want bool
	}{
		{"\t.org 510\n", true},
		{"\t.set ., 0x1fe\n", true},
		{"\t.word 0xAA55\n", true},
		{"\t.byte 0x55, 0xAA\n", true},
		{"\t.byte 0x55\n", false},
		{"\tmovw $0x5678, %es:(0x0510)\n", false},
	} {
		if got := bootLayout(prog(c.text, asm.Mode16)); got != c.want {
			t.Errorf("bootLayout(%q) = %v", c.text, got)
		}
	}
	if cpuidReg("EBX") != flow.RBX || cpuidReg("eax") != flow.RAX || cpuidReg("ecx") != flow.RCX || cpuidReg("x") != flow.NoReg {
		t.Error("cpuidReg")
	}
	if sectionName("") != ".text" || sectionName(".boot") != ".boot" {
		t.Error("sectionName")
	}
	if hex2(0x1A) != "1A" {
		t.Error("hex2")
	}
	if !isExec(nil, ".text.stage1") || isExec(nil, ".data") || isExec(map[string]bool{".text": false}, ".text") {
		t.Error("isExec")
	}
}
