package flowrules

import (
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
)

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
