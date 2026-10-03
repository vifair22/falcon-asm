package encoding

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func TestPrecedenceSafe(t *testing.T) {
	for expr, want := range map[string]bool{
		"1": true, "-1": true, "0x80 | 0x7f": true, "(1 << 8) | 3": true, "2 * 3 + 1": true,
		"1 + 2 << 3": false, "a | b & c": false, "a << b * c": false, "1 < 2": false,
		"!1": false, "1 == 1": false, "a ^ b | c": false, "4 - -1": true, "~1 + 2": true,
	} {
		if got := precedenceSafe(expr); got != want {
			t.Errorf("precedenceSafe(%q) = %v", expr, got)
		}
	}
}

func TestRegHelpers(t *testing.T) {
	for name, want := range map[string]string{"al": "a", "rsp": "sp", "r8": "r8", "r12d": "r12", "r9l": "r9", "rip": "", "r8x": "", "xmm0": ""} {
		if got := family(name); got != want {
			t.Errorf("family(%q) = %q", name, got)
		}
	}
	for name, want := range map[string]bool{"sil": true, "r8w": true, "xmm9": true, "xmm1": false, "eax": false, "ds": false} {
		r, _ := isa.LookupRegister(name)
		if got := needsREX(r); got != want {
			t.Errorf("needsREX(%q) = %v", name, got)
		}
	}
	if needsREX(nil) || suffixFor(12) != "" || suffixWidth("x") != 0 || bcstCount("1tox") != 0 || bcstCount("1to8") != 8 {
		t.Error("helper edge cases")
	}
	for s, want := range map[string]bool{"x0": true, "sp": true, "R8": true, "AX": true, "foo": false, "x": false, "eax": false, "s1x": false} {
		if got := foreignReg(s); got != want {
			t.Errorf("foreignReg(%q) = %v", s, got)
		}
	}
	if hex(-16) != "-0x10" || joinInts([]int{8}) != "8" || joinInts([]int{8, 16}) != "8 or 16" {
		t.Error("formatting")
	}
}

func TestDescribe(t *testing.T) {
	text := "\tmov %eax, %ds\n\tmov %cr0, %dr0\n\tmov %mm0, %k1\n\tmov %st(1), %xmm0\n\tmov (%rax,%xmm1), $1\n"
	p := parse(text, asm.Mode64)
	var parts []string
	for i := range p.Stmts {
		for j := range p.Stmts[i].Ops {
			op := &p.Stmts[i].Ops[j]
			o := operand{op: op, kind: op.Kind, reg: op.Reg}
			if op.Index == "xmm1" {
				o.vsib = 128
			}
			parts = append(parts, describe(&o))
		}
	}
	if got := strings.Join(parts, " "); got != "r32 sreg cr dr mm k st(i) xmm vsib imm" {
		t.Errorf("describe = %q", got)
	}
	if describe(&operand{kind: asm.OpLabel}) != "?" {
		t.Error("label")
	}
}

func TestFormList(t *testing.T) {
	inst, _, _ := isa.Lookup("mov")
	c := formsOf(inst, false)
	if got := c.list(5); !strings.HasPrefix(got, "it takes 1 or 2 operands") && !strings.HasPrefix(got, "it takes 2 operands") {
		t.Errorf("list(5) = %q", got)
	}
	if got := c.list(2); !strings.HasSuffix(got, "...") {
		t.Errorf("list(2) = %q", got)
	}
	// Patterns the compiler does not understand are counted, not guessed.
	for _, p := range []string{"*r/m32", "$imm16, $imm16", "(%si), %es:(%di)", "r/m12", "imm7", "vm16x", "mfoo", "m12abc", "bogus"} {
		if _, ok := compileForm(p, false); ok {
			t.Errorf("compileForm(%q) understood", p)
		}
	}
	if _, ok := compileForm("st(0), st(i)", false); !ok {
		t.Error("x87 pattern")
	}
}

// An x87 instruction, a register unknown to the tables and statements
// without a mnemonic are skipped without panicking.
func TestOddStatements(t *testing.T) {
	for _, text := range []string{
		"\tfldl (%eax)\n", "\tmov %foo, %eax\n", "\trep\n", "\tlock\n", "label:\n", "\tmov %eax{%1}, %ebx\n",
		"\tmov %es:(%eax), %ebx\n\tmov %xx:(%eax), %ebx\n", "\tjmp *%eax\n", "\ttmm\n", "\tldtilecfg (%rax)\n\ttilezero %tmm1\n",
		"\tbndmov %bnd1, %bnd2\n", "\tfxch %st(1)\n",
	} {
		_ = lintModeSet(t, text, asm.Mode64, lint.LangAsm)
	}
	// A program with no document reports nothing.
	if got := lint.Run(lint.Input{Prog: &asm.Program{}, Lang: lint.LangAsm, Rules: rules(t)}); len(got) != 0 {
		t.Errorf("empty program: %v", got)
	}
}
