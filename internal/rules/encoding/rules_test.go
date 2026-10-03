package encoding

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// ruleCase is one snippet and the findings it must produce: "" for none,
// else the code of the single finding expected on the snippet's last line
// (earlier lines set the stage: .code16, a label).
type ruleCase struct {
	text string
	mode asm.Mode
	want string
	msg  string // a substring the message must contain
	fix  string // the text after applying the first fix; "" for no fix
}

func runCases(t *testing.T, lang lint.Lang, cases []ruleCase) {
	t.Helper()
	for _, tc := range cases {
		mode := tc.mode
		if mode == 0 {
			mode = asm.Mode64
		}
		text := tc.text + "\n"
		got := lintModeSet(t, text, mode, lang)
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("%q (mode %d): want no findings, got %v", tc.text, mode, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Code != tc.want {
			t.Errorf("%q (mode %d): want one %s, got %v", tc.text, mode, tc.want, got)
			continue
		}
		f := got[0]
		if last := strings.Count(tc.text, "\n"); f.Range.Start.Line != last {
			t.Errorf("%q: finding on line %d, want %d", tc.text, f.Range.Start.Line, last)
		}
		if tc.msg != "" && !strings.Contains(f.Message, tc.msg) {
			t.Errorf("%q: message %q lacks %q", tc.text, f.Message, tc.msg)
		}
		switch {
		// An ambiguous size offers one fix per width; those are checked by
		// TestAmbiguousSizeFixes rather than as a single expected text.
		case tc.fix == "" && len(f.Fixes) != 0 && tc.want != CodeSizeAmbiguous:
			t.Errorf("%q: unexpected fix %+v", tc.text, f.Fixes)
		case tc.fix != "" && len(f.Fixes) != 1:
			t.Errorf("%q: want a fix, got %+v", tc.text, f.Fixes)
		case tc.fix != "":
			if got := applyFix(t, text, f.Fixes[0]); got != tc.fix+"\n" {
				t.Errorf("%q: fix gives %q, want %q", tc.text, got, tc.fix+"\n")
			}
		}
	}
}

// lintModeSet lints with the target mode marked as configured, so the
// mode-dependent checks apply from the first line.
func lintModeSet(t *testing.T, text string, mode asm.Mode, lang lint.Lang) []lint.Finding {
	t.Helper()
	return lint.Run(lint.Input{
		Prog: parse(text, mode), Lang: lang, Rules: rules(t),
		Target: lint.Target{Mode: mode, Sources: map[string]string{"mode": "test"}},
	})
}

func TestNoForm(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tlgdt %eax", want: CodeNoForm, msg: "lgdt needs a memory operand here (m16&32)"},
		{text: "\tlgdt gdt_desc", want: ""},
		{text: "\tlgdtl (%eax)", mode: 32, want: ""},
		{text: "\tlea %eax, %ebx", want: CodeNoForm, msg: "needs a memory operand"},
		{text: "\tmovq $1, %xmm0", want: CodeNoForm, msg: "no form of movq takes (imm, xmm)"},
		{text: "\tinc", want: CodeNoForm, msg: "inc takes operands"},
		{text: "\trol #8, %d0", want: ""}, // m68k: '#8' is no comment there
		{text: "\tcpuid %eax", want: ""},  // implicit operands spelled out: left to GAS
		{text: "\tbswap %eax, %ebx", want: CodeNoForm, msg: "bswap does not take 2 operands"},
		{text: "\tmov (%eax), (%ebx)", mode: 32, want: CodeNoForm, msg: "no form of mov takes (m, m)"},
		{text: "\tmovsd", mode: 32, want: ""},
		{text: "\taad", mode: 32, want: ""},
		{text: "\tvmrun", want: ""},
		{text: "\tjmp *%eax", mode: 32, want: ""},
		{text: "\tfld %st(1)", want: ""},
		{text: "\tmovsb (%esi), %es:(%edi)", mode: 32, want: ""},
		{text: "\tmov \\arg, %eax", want: ""},
		{text: "\tmov TMP, (%rax)", want: ""},
		{text: "\tadd $1, %ebx, %ecx", want: ""},                   // APX new data destination
		{text: "\tadd $1, %ebx, %ecx", mode: 32, want: CodeNoForm}, // no APX outside 64-bit
		{text: "\tsete %ebx", want: ""},
		{text: "\tblendvps %xmm0, %xmm1, %xmm2", want: ""},
		{text: "\tmovq %ds, %rax", want: ""},
		{text: "\tmovq %rax, %rbx", want: ""},
		{text: "\tmov %eax, %ds", mode: 32, want: ""},
		{text: "\tshl %eax", mode: 32, want: ""},
		{text: "\timul $3, %eax", mode: 32, want: ""},
		{text: "\txchg (%ebx), %eax", mode: 32, want: ""},
		{text: "\ttest (%ebx), %eax", mode: 32, want: ""},
		{text: "\tmovd %rax, %xmm0", want: ""},
		{text: "\tlar %ebx, %ebx", mode: 32, want: ""},
	})
}

func TestSizeMismatch(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tmovw %eax, %bx", want: CodeSizeMismatch, msg: "w suffix of movw means 16-bit operands, but %eax (32-bit)"},
		{text: "\tmovw %eax, %ebx", want: CodeSizeMismatch, fix: "\tmovl %eax, %ebx"},
		{text: "\tmov %eax, %bx", want: CodeSizeMismatch, msg: "%eax (32-bit) and %bx (16-bit) differ in size"},
		{text: "\tmovb (%eax), %ecx", mode: 32, want: CodeSizeMismatch, fix: "\tmovl (%eax), %ecx"},
		{text: "\tmovzbl %ax, %ecx", mode: 32, want: CodeSizeMismatch, msg: "no form of movzbl takes these operand sizes"},
		{text: "\tinw %dx, %al", mode: 32, want: CodeSizeMismatch, fix: "\tinb %dx, %al"},
		{text: "\tmovzbl %al, %ecx", mode: 32, want: ""},
		{text: "\tshll %cl, %eax", mode: 32, want: ""},
		{text: "\tinl %dx, %eax", mode: 32, want: ""},
		{text: "\tcrc32b %al, %ecx", mode: 32, want: ""},
		{text: "\tcvtsi2sdl (%eax), %xmm0", mode: 32, want: ""},
		{text: "\tcvttsd2siq %xmm0, %rax", want: ""},
		{text: "\tmovl %eax, %cr0", mode: 32, want: ""},
		{text: "\tmovl %cr0, %rax", want: CodeSizeMismatch, fix: "\tmovq %cr0, %rax"},
		{text: "\tpushl %ds", mode: 32, want: ""},
		{text: "\tmovl $1, %rax", mode: 32, want: ""}, // mode-register's finding
		{text: "\tmovw %ds, %ax", mode: 16, want: ""},
		{text: "\tmovw %ds, %eax", mode: 32, want: CodeSizeMismatch, fix: "\tmovl %ds, %eax"},
		{text: "\tlgdtw (%eax)", mode: 32, want: ""},
	})
}

func TestSizeAmbiguous(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\t.code16\n\tinc (%bx)", want: CodeSizeAmbiguous, msg: "incb, incw, incl"},
		{text: "\tinc (%rax)", want: CodeSizeAmbiguous, msg: "incb, incw, incl, incq"},
		{text: "\tincw (%bx)", mode: 16, want: ""},
		{text: "\tinc %ax", mode: 16, want: ""},
		{text: "\tmov $1, (%eax)", mode: 32, want: CodeSizeAmbiguous},
		{text: "\tmov %eax, (%ebx)", mode: 32, want: ""},
		{text: "\tpush (%eax)", mode: 32, want: ""},
		{text: "\tlgdt (%eax)", mode: 32, want: ""},
		{text: "\tmovzx (%eax), %ecx", mode: 32, want: CodeSizeAmbiguous, msg: "movzbl, movzwl"},
		{text: "\tmovzbl (%eax), %ecx", mode: 32, want: ""},
		{text: "\tin $0x60", mode: 32, want: CodeSizeAmbiguous, msg: "inb, inw or inl"},
		{text: "\tinb $0x60", mode: 32, want: ""},
		{text: "\tout %dx", mode: 32, want: CodeSizeAmbiguous},
		{text: "\tbt $1, (%eax)", mode: 32, want: CodeSizeAmbiguous},
		{text: "\tbt %eax, (%eax)", mode: 32, want: ""},
		{text: "\tvcvtpd2ps (%rax), %xmm0", want: CodeSizeAmbiguous, msg: "x/y suffix"},
		{text: "\tvaddps (%rax){1to16}, %zmm1, %zmm2", want: ""},
		{text: "\tsetz (%eax)", mode: 32, want: ""},
		{text: "\tnop (%eax)", mode: 32, want: CodeSizeAmbiguous},
	})
}

func TestREXHighByte(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tmovb %ah, %sil", want: CodeREXHighByte, msg: "(%sil)"},
		{text: "\tmovb %ah, (%r8)", want: CodeREXHighByte, msg: "%r8 in the address"},
		{text: "\tmovzbq %ah, %rax", want: CodeREXHighByte, msg: "64-bit operand"},
		{text: "\tmovzbl %ah, %r8d", want: CodeREXHighByte},
		{text: "\tmovb %ah, (%rax)", want: ""},
		{text: "\tmovzbl %ah, %eax", want: ""},
		{text: "\tmovb %ah, %al", want: ""},
		{text: "\t.code32\n\tmovb %ah, %sil", want: ""}, // the mode rule's business
	})
}

func TestAddr16(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tmov (%si,%di), %ax", mode: 16, want: CodeAddr16, msg: "base (%bx or %bp)"},
		{text: "\tmov (%bx,%bp), %ax", mode: 16, want: CodeAddr16},
		{text: "\tmov (%ax), %bx", mode: 16, want: CodeAddr16, msg: "%ax cannot address memory"},
		{text: "\tmov (%sp), %bx", mode: 16, want: CodeAddr16},
		{text: "\tmov (%bx,%si,2), %ax", mode: 16, want: CodeAddr16, msg: "no scaled index"},
		{text: "\tmov (%si,%bx), %ax", mode: 16, want: CodeAddr16, fix: "\tmov (%bx,%si), %ax"},
		{text: "\tmov 4(%di,%bp,1), %ax", mode: 16, want: CodeAddr16, fix: "\tmov 4(%bp,%di), %ax"},
		{text: "\tmov (,%si), %ax", mode: 16, want: CodeAddr16, fix: "\tmov (%si), %ax"},
		{text: "\tmov (,%ax), %bx", mode: 16, want: CodeAddr16},
		{text: "\taddr32 mov (%bx), %ax", mode: 16, want: CodeAddr16, msg: "addr32"},
		{text: "\tmov (%bx), %ax", want: CodeAddr16, msg: "64-bit code"},
		{text: "\tmov (%bx,%si), %ax", mode: 16, want: ""},
		{text: "\tmov 4(%bp,%di), %ax", mode: 16, want: ""},
		{text: "\tmov (%si), %ax", mode: 16, want: ""},
		{text: "\tmov (%eax), %bx", mode: 16, want: ""},
		{text: "\tmov (%bx), %ax", mode: 32, want: ""},
		{text: "\tinb (%dx), %al", mode: 16, want: ""},
	})
}

func TestAddrReg(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tmovl (%eax,%esp), %ebx", mode: 32, want: CodeAddrReg, fix: "\tmovl (%esp,%eax), %ebx"},
		{text: "\tmovl (%eax,%esp,2), %ebx", mode: 32, want: CodeAddrReg, msg: "cannot be an index"},
		{text: "\tmovq (,%rsp,1), %rbx", want: CodeAddrReg},
		{text: "\tmovl (%eip), %eax", mode: 32, want: CodeAddrReg, msg: "only in 64-bit code"},
		{text: "\tmovl (%rip), %eax", mode: 32, want: ""}, // mode-register reports %rip
		{text: "\tmovl (%rip,%rax), %eax", want: CodeAddrReg, msg: "takes no index"},
		{text: "\tmovl (%rax,%rip), %eax", want: CodeAddrReg},
		{text: "\tmovl (%ip), %eax", want: CodeAddrReg},
		{text: "\tmovl (%rax,%ebx), %eax", want: CodeAddrReg, msg: "differ in width"},
		{text: "\tmovl (%eax,%bx), %eax", mode: 32, want: CodeAddrReg},
		{text: "\tmovl (%ds), %eax", mode: 32, want: CodeAddrReg, msg: "cannot be a base"},
		{text: "\tmovl (%al), %eax", mode: 32, want: CodeAddrReg},
		{text: "\tmovl (%eax,%al), %eax", mode: 32, want: CodeAddrReg},
		{text: "\tmovl (%eax,%cr0), %eax", mode: 32, want: CodeAddrReg},
		{text: "\tmovl (%eax,%xmm1), %eax", want: CodeAddrReg, msg: "gather and scatter"},
		{text: "\tmovl (%eax,%ebx,3), %eax", mode: 32, want: CodeAddrReg, msg: "scale 3"},
		{text: "\tmovl (%esp,%eax,2), %eax", mode: 32, want: ""},
		{text: "\tmovl 0x10(%rip), %eax", want: ""},
		{text: "\tmovl (%eip), %eax", want: ""},
		{text: "\tvpgatherdd %xmm0, (%rax,%xmm1,4), %xmm2", want: ""},
		{text: "\tmovl (%1), %eax", want: ""},
	})
}

func TestSreg(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tmov %ax, %cs", mode: 16, want: CodeSreg, msg: "#UD"},
		{text: "\tpop %cs", mode: 16, want: CodeSreg},
		{text: "\tlds %eax, %ebx", mode: 32, want: CodeSreg, msg: "far pointer from memory"},
		{text: "\tlds (%eax), %ebx", mode: 32, want: ""},
		{text: "\tpush %ds", want: CodeSreg, msg: "64-bit code"},
		{text: "\tpop %es", want: CodeSreg},
		{text: "\tpush %fs", want: ""},
		{text: "\tpush %ds", mode: 32, want: ""},
		{text: "\tmov %ds, %es", mode: 16, want: CodeSreg, msg: "segment-to-segment"},
		{text: "\tmov $0x10, %ds", mode: 32, want: CodeSreg, msg: "immediate"},
		{text: "\tmov %al, %ds", mode: 32, want: CodeSreg, msg: "not byte registers"},
		{text: "\tmov %ds, %al", mode: 32, want: CodeSreg},
		{text: "\tmovl %ds, (%eax)", mode: 32, want: CodeSreg, fix: "\tmovw %ds, (%eax)"},
		{text: "\tmov %ds, (%eax)", mode: 32, want: ""},
		{text: "\tmovl %ds, %eax", mode: 32, want: ""},
		{text: "\tmov %ax, %ss", mode: 16, want: ""},
	})
}

func TestFixedReg(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tin %dx, %bl", mode: 32, want: CodeFixedReg, msg: "through %al, %ax or %eax only, not %bl"},
		{text: "\tinb %dx, %ah", mode: 32, want: CodeFixedReg},
		{text: "\tout %al, %cx", mode: 32, want: CodeFixedReg, msg: "port is an immediate"},
		{text: "\toutb %al, %edx", mode: 32, want: CodeFixedReg, fix: "\toutb %al, %dx"},
		{text: "\toutb %al, (%bx)", mode: 16, want: CodeFixedReg},
		{text: "\tin %bx", mode: 16, want: CodeFixedReg},
		{text: "\tshl %bl, %eax", mode: 32, want: CodeFixedReg, msg: "immediate or %cl"},
		{text: "\tshll %ecx, %eax", mode: 32, want: CodeFixedReg, fix: "\tshll %cl, %eax"},
		{text: "\tshldl %ebx, %eax, %edx", mode: 32, want: CodeFixedReg},
		{text: "\tshll (%eax), %eax", mode: 32, want: CodeFixedReg},
		{text: "\tin %dx, %al", mode: 32, want: ""},
		{text: "\tinb (%dx), %al", mode: 32, want: ""},
		{text: "\toutl %eax, $0x80", mode: 32, want: ""},
		{text: "\tshl %cl, %eax", mode: 32, want: ""},
		{text: "\tshrdl $4, %eax, %edx", mode: 32, want: ""},
	})
}

func TestImmRange(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\toutb %al, $0x300", mode: 32, want: CodeImmRange, msg: "through %dx"},
		{text: "\tint $256", mode: 32, want: CodeImmRange, msg: "interrupt vector 0x100"},
		{text: "\tint $-1", mode: 32, want: CodeImmRange},
		{text: "\tret $70000", mode: 32, want: CodeImmRange, msg: "0xffff bytes"},
		{text: "\tret $-32768", mode: 32, want: ""},
		{text: "\tmovb $300, %al", mode: 32, want: CodeImmRange, msg: "-0x80-0xff"},
		{text: "\tmovb $-129, %al", mode: 32, want: CodeImmRange},
		{text: "\tmovb $255, %al", mode: 32, want: ""},
		{text: "\tmovw $70000, %ax", mode: 32, want: CodeImmRange},
		{text: "\tmovl $0x100000000, %eax", mode: 32, want: CodeImmRange},
		{text: "\tmovl $0xffffffff, %eax", mode: 32, want: ""},
		{text: "\taddq $0xffffffff, %rax", want: CodeImmRange, msg: "sign-extended imm32"},
		{text: "\tmovq $0xffffffff, (%rax)", want: CodeImmRange},
		{text: "\tmovq $0xffffffff, %rax", want: ""}, // GAS uses movabs
		{text: "\tmovq $-1, %rax", want: ""},
		{text: "\tpushq $0xffffffff", want: CodeImmRange},
		{text: "\tpushl $0xffffffff", mode: 32, want: ""},
		{text: "\tenter $0, $256", mode: 32, want: CodeImmRange},
		{text: "\tenter $65535, $255", mode: 32, want: ""},
		{text: "\tshl $-1, %eax", mode: 32, want: CodeImmRange},
		{text: "\tshl $3, %eax", mode: 32, want: ""},
		{text: "\tmovb $FOO, %al", mode: 32, want: ""},
		{text: "#define BIG 0x1ff\n\tmovb $BIG, %al", mode: 32, want: CodeImmRange},
		{text: "\tmovb $1 + 2 << 7, %al", mode: 32, want: ""}, // GAS and C disagree: not judged
		{text: "\tmovb $(1 << 8), %al", mode: 32, want: CodeImmRange},
	})
}

func TestDataRange(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\t.byte 300", want: CodeDataRange, msg: "truncates it to 0x2c"},
		{text: "\t.byte -129", want: CodeDataRange, msg: "without a warning"},
		{text: "\t.byte 0x55, 0xaa, 256", want: CodeDataRange},
		{text: "\t.word 0x10000", want: CodeDataRange},
		{text: "\t.long 0x100000000", want: CodeDataRange},
		{text: "\t.byte 255, -128, 'A', label, 1f-0b", want: ""},
		{text: "\t.word 0xffff, -32768", want: ""},
		{text: "\t.quad 0xffffffffffffffff", want: ""},
		{text: "\t.byte 2 + 1 << 8", want: ""},
		{text: "#define SIG 0xaa55\n\t.byte SIG", want: CodeDataRange},
		{text: "\tmov r0, sp\n\t.word 0x12345678", want: ""}, // ARM: .word is 32 bits
	})
}

func TestPrefix(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tlock addl %eax, %ebx", mode: 32, want: CodePrefix, msg: "memory destination"},
		{text: "\tlock movl %eax, (%ebx)", mode: 32, want: CodePrefix, msg: "not mov"},
		{text: "\tlock; addl %eax, (%ebx)", mode: 32, want: ""},
		{text: "\tlock xchg (%ebx), %eax", mode: 32, want: ""},
		{text: "\tlock cmpxchg8b (%eax)", mode: 32, want: ""},
		{text: "\trep addl %eax, %ebx", mode: 32, want: CodePrefix, msg: "repeats only string"},
		{text: "\trep ret", mode: 32, want: ""},
		{text: "\trepz ret", mode: 32, want: ""},
		{text: "\trep nop", mode: 32, want: ""},
		{text: "\trep bsf %eax, %ebx", mode: 32, want: ""},
		{text: "\trep movsb", mode: 32, want: ""},
		{text: "\trep; movsd", mode: 32, want: ""},
		{text: "\trep xstore", mode: 32, want: ""},
		{text: "\trep rep movsb", mode: 32, want: CodePrefix, msg: "repeated"},
		{text: "\trep repne movsb", mode: 32, want: CodePrefix, msg: "conflicts"},
		{text: "\tlock rep movsb", mode: 32, want: CodePrefix},
		{text: "\tcs ds lodsb", mode: 32, want: CodePrefix},
		{text: "\tes mov %ds:(%eax), %eax", mode: 32, want: CodePrefix, msg: "GAS keeps the prefix"},
		{text: "\tes mov %es:(%eax), %eax", mode: 32, want: ""},
		{text: "\taddr32 movl (%eax), %eax", mode: 32, want: CodePrefix, msg: "redundant"},
		{text: "\tdata16 movw %ax, %bx", mode: 32, want: CodePrefix, msg: "w suffix"},
		{text: "\tdata32 movl %eax, %ebx", mode: 16, want: CodePrefix, msg: "l suffix"},
		{text: "\tdata16 lgdt (%eax)", mode: 32, want: ""},
		{text: "\taddr32 movl (%eax), %eax", mode: 16, want: ""},
		{text: "\taddr32 loop 1b", mode: 16, want: ""},
		{text: "\tlock addl \\x, \\y", mode: 32, want: ""},
		{text: "\txacquire lock addl $1, (%eax)", mode: 32, want: ""},
	})
}

// A prefix on its own line applies to the next instruction and is
// reported where it is written.
func TestPrefixAlone(t *testing.T) {
	got := lintModeSet(t, "\tlock\n\taddl $1, %eax\n1:\tlock\n2:\taddl $1, %eax\n", asm.Mode32, lint.LangAsm)
	if summary(got) != CodePrefix+"@0" {
		t.Fatalf("findings = %v", got)
	}
}

func TestAVXDeco(t *testing.T) {
	runCases(t, lint.LangAsm, []ruleCase{
		{text: "\tvaddps %zmm1, %zmm2, %zmm3{%k0}", want: CodeAVXDeco, msg: "%k0 cannot be a write mask"},
		{text: "\tvaddps %zmm1, %zmm2, %zmm3{z}", want: CodeAVXDeco, msg: "needs a write mask"},
		{text: "\tvmovaps %zmm0, (%rax){%k1}{z}", want: CodeAVXDeco, fix: "\tvmovaps %zmm0, (%rax){%k1}"},
		{text: "\tvaddps %xmm0{1to4}, %xmm1, %xmm2", want: CodeAVXDeco, msg: "memory operand"},
		{text: "\tvaddps (%rax){1to8}, %zmm1, %zmm2", want: CodeAVXDeco, fix: "\tvaddps (%rax){1to16}, %zmm1, %zmm2"},
		{text: "\tvaddps %xmm1, %ymm2, %ymm3", want: CodeAVXDeco, msg: "does not mix vector widths"},
		{text: "\tvpgatherdd (%rax,%zmm1,4), %zmm2", want: CodeAVXDeco, msg: "completion mask"},
		{text: "\tvaddps %zmm1{%k1}, %zmm2, %zmm3", want: CodeAVXDeco, msg: "destination operand"},
		{text: "\tvcmpps $0, %zmm1, %zmm2, %k1{%k2}{z}", want: CodeAVXDeco, msg: "zeroing-masking"},
		{text: "\tvaddps %zmm1, %zmm2, %zmm3{%k1}{z}", want: ""},
		{text: "\tvaddps (%rax){1to16}, %zmm1, %zmm2", want: ""},
		{text: "\tvaddpd (%rax){1to8}, %zmm1, %zmm2", want: ""},
		{text: "\tvextracti128 $1, %ymm0, %xmm1", want: ""},
		{text: "\tvinserti128 $1, %xmm0, %ymm1, %ymm2", want: ""},
		{text: "\tvcvtps2pd %xmm0, %ymm1", want: ""},
		{text: "\tvpgatherdd (%rax,%zmm1,4), %zmm2{%k1}", want: ""},
		{text: "\tvbroadcastss %xmm0, %zmm1{%k1}{z}", want: ""},
		{text: "\tvaddps {rn-sae}, %zmm1, %zmm2, %zmm3", want: ""},
		{text: "\tkmovw %k0, %k1", want: ""},
	})
}

// Inline asm templates: operand references (%0, %b1, %k0) are never
// judged, real registers (written %%eax in C, %eax in the template) are.
func TestInline(t *testing.T) {
	runCases(t, lint.LangInline, []ruleCase{
		{text: "inb %w1, %b0", want: ""},
		{text: "outb %b0, %w1", want: ""},
		{text: "movl %1, %0", want: ""},
		{text: "rdrand %0", want: ""},
		{text: "mov %k0, %eax", want: ""},
		{text: "lock; xaddl %0, %1", want: ""},
		{text: "movw %eax, %bx", want: CodeSizeMismatch},
		{text: "int $256", want: CodeImmRange},
		{text: "mov x0, sp", want: ""},
	})
}

// Bad and Good of every rule's documentation: the Bad example triggers
// the rule and the Good one does not.
func TestDocExamples(t *testing.T) {
	for _, r := range rules(t) {
		if r.NeedsListing {
			continue
		}
		for _, ex := range []struct {
			text string
			want bool
		}{{r.Doc.Bad, true}, {r.Doc.Good, false}} {
			hit := false
			for _, f := range lintModeSet(t, ex.text+"\n", asm.Mode64, lint.LangAsm) {
				hit = hit || f.Code == r.Code
			}
			if hit != ex.want {
				t.Errorf("%s: example %q: reported=%v, want %v", r.Code, ex.text, hit, ex.want)
			}
		}
	}
}

// None of the rules fires on real code: the falcon-bios fixtures and the
// large synthetic document built from them, in every mode.
func TestFixturesClean(t *testing.T) {
	texts := benchdata.Fixtures(t)
	texts = append(texts, benchdata.Large(t, 20000))
	for _, text := range texts {
		for _, mode := range []asm.Mode{asm.Mode16, asm.Mode32, asm.Mode64} {
			for _, lang := range []lint.Lang{lint.LangAsm} {
				if got := lintModeSet(t, text, mode, lang); len(got) != 0 {
					n := min(len(got), 5)
					t.Errorf("mode %d: %d findings, first %v", mode, len(got), got[:n])
				}
			}
		}
	}
}

// Statements the rules must leave alone: other architectures' assembly,
// macro bodies whose mode is not known, and code with no mode given.
func TestQuiet(t *testing.T) {
	quiet := []string{
		// ARM, MIPS, PowerPC and Go/Plan 9 syntax.
		"\tmov r0, sp\n\tstr r1, [r0]\n\tadd sp, sp, $8",
		"\tand $4, $4, 0x7c\n\tor $5, $5, $4",
		"\tor 3,3,4\n\tand 9,9,3",
		"\tMOVQ AX, BX\n\tADDQ $1, AX",
		// A file with x86 code and an ARM section behind #if.
		"\tmovl %eax, %ebx\n#ifdef __aarch64__\n\tmov x0, sp\n\tret x30\n#endif",
		// A .macro body may be expanded in any mode.
		"\t.macro m\n\tpush %ds\n\t.endm",
	}
	for _, text := range quiet {
		if got := lintModeSet(t, text+"\n", asm.Mode64, lint.LangAsm); len(got) != 0 {
			t.Errorf("%q: %v", text, got)
		}
	}
	// With no mode given anywhere, 64-bit is only a default: a 32-bit file
	// built with -m32 is not told its push %ds is invalid.
	if got := lintText(t, "\tpushl %ds\n\tpush %es\n", asm.Mode64, lint.LangAsm, nil); len(got) != 0 {
		t.Errorf("defaulted mode: %v", got)
	}
	if got := lintText(t, "\t.code64\n\tpush %es\n", asm.Mode64, lint.LangAsm, nil); summary(got) != CodeSreg+"@1" {
		t.Errorf(".code64: %v", got)
	}
}

// An ambiguous size has one valid repair per width; every one is offered
// so the author picks, and applying any of them clears the finding.
func TestAmbiguousSizeFixes(t *testing.T) {
	cases := []struct {
		text string
		mode asm.Mode
		want []string
	}{
		{"\t.code16\n\tinc (%bx)", 0, []string{"Use incb", "Use incw", "Use incl"}},
		{"\tmovzx (%eax), %ecx", 32, []string{"Use movzbl", "Use movzwl"}},
		{"\tin $0x60", 32, []string{"Use inb", "Use inw", "Use inl"}},
	}
	for _, tc := range cases {
		var f *lint.Finding
		fs := lintText(t, tc.text, tc.mode, lint.LangAsm, nil)
		for i := range fs {
			if fs[i].Code == CodeSizeAmbiguous {
				f = &fs[i]
			}
		}
		if f == nil {
			t.Fatalf("%q: no finding in %s", tc.text, summary(fs))
			return
		}
		var titles []string
		for _, fx := range f.Fixes {
			titles = append(titles, fx.Title)
		}
		if strings.Join(titles, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%q: fixes %q, want %q", tc.text, titles, tc.want)
		}
		for _, fx := range f.Fixes {
			fixed := applyFix(t, tc.text, fx)
			for _, g := range lintText(t, fixed, tc.mode, lint.LangAsm, nil) {
				if g.Code == CodeSizeAmbiguous {
					t.Errorf("%q after %q still ambiguous", fixed, fx.Title)
				}
			}
		}
	}
}
