package perf

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// run lints text with one rule; the mode defaults to 64-bit.
func run(t *testing.T, code, text string, tgt lint.Target) []lint.Finding {
	t.Helper()
	r, ok := lint.Lookup(code)
	if !ok {
		t.Fatalf("%s not registered", code)
	}
	if tgt.Mode == 0 {
		tgt.Mode = asm.Mode64
	}
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	p := asm.Parse(doc, lexer.Tokenize(text), asm.Config{InitialMode: tgt.Mode})
	return lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: tgt, Rules: []*lint.Rule{r}})
}

// applyFix applies the first fix of f to text.
func applyFix(t *testing.T, text string, f lint.Finding) string {
	t.Helper()
	if len(f.Fixes) == 0 {
		t.Fatalf("no fix: %v", f)
	}
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	edits := f.Fixes[0].Edits
	// Apply back to front so earlier offsets stay valid.
	for k := len(edits) - 1; k >= 0; k-- {
		e := edits[k]
		s, en := offset(doc, e.Range.Start), offset(doc, e.Range.End)
		text = text[:s] + e.NewText + text[en:]
		doc = &docstore.Document{URI: doc.URI, Text: text}
	}
	return text
}

func offset(d *docstore.Document, p protocol.Position) int {
	line, off := 0, 0
	for off < len(d.Text) && line < p.Line {
		if d.Text[off] == '\n' {
			line++
		}
		off++
	}
	return off + p.Character
}

type tc struct {
	name, text string
	tgt        lint.Target
	want       int    // findings expected
	msg        string // substring of the first message
	fixed      string // the text after the first fix, "" to skip
}

func check(t *testing.T, code string, cases []tc) {
	t.Helper()
	for _, c := range cases {
		got := run(t, code, c.text, c.tgt)
		if len(got) != c.want {
			t.Errorf("%s/%s: want %d findings, got %v", code, c.name, c.want, got)
			continue
		}
		if c.want == 0 {
			continue
		}
		if c.msg != "" && !strings.Contains(got[0].Message, c.msg) {
			t.Errorf("%s/%s: message %q lacks %q", code, c.name, got[0].Message, c.msg)
		}
		if c.fixed != "" {
			if out := applyFix(t, c.text, got[0]); out != c.fixed {
				t.Errorf("%s/%s: fixed text\n%q\nwant\n%q", code, c.name, out, c.fixed)
			}
		}
	}
}

func TestRegistered(t *testing.T) {
	sev := map[string]lint.Severity{
		CodeVzeroupper: lint.Warning, CodeFalseDep: lint.Info, CodePartialFlag: lint.Info,
		CodeIncNoCarry: lint.Warning, CodeSlowLoop: lint.Info, CodeSlowEnter: lint.Info,
		CodeXchgMem: lint.Info, CodeBtMem: lint.Info, CodeLea3: lint.Info, CodeDivConst: lint.Hint,
	}
	for code, want := range sev {
		r, ok := lint.Lookup(code)
		if !ok {
			t.Fatalf("%s missing", code)
		}
		if r.Default != want || r.Section != 8 || r.Doc.Why == "" || r.Doc.Bad == "" || r.Doc.Good == "" {
			t.Errorf("%s: %+v", code, r)
		}
	}
}

// TestDocExamples runs every rule's own Bad and Good examples, with the
// target the example's comment names.
func TestDocExamples(t *testing.T) {
	for _, code := range []string{CodeVzeroupper, CodeFalseDep, CodePartialFlag, CodeIncNoCarry,
		CodeSlowLoop, CodeSlowEnter, CodeXchgMem, CodeBtMem, CodeLea3, CodeDivConst} {
		r, _ := lint.Lookup(code)
		tgt := lint.Target{}
		if strings.Contains(r.Doc.Bad, "skylake") {
			tgt.CPU = "skylake"
		}
		if got := run(t, code, r.Doc.Bad+"\n", tgt); len(got) != 1 {
			t.Errorf("%s Bad: %v", code, got)
		}
		if got := run(t, code, r.Doc.Good+"\n", tgt); len(got) != 0 {
			t.Errorf("%s Good: %v", code, got)
		}
	}
}

func TestVzeroupper(t *testing.T) {
	check(t, CodeVzeroupper, []tc{
		{name: "ret", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tret\n", want: 1, msg: "ymm3",
			fixed: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tvzeroupper\n\tret\n"},
		{name: "call", text: "f:\n\tvaddps %ymm9, %ymm9, %ymm9\n\tcall memcpy\n\tret\n", want: 1, msg: "call"},
		{name: "tail jump", text: "f:\n\tvmovdqu (%rdi), %ymm9\n\tjmp memset\n", want: 1, msg: "jump out"},
		{name: "labelled ret", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n1:\tret\n", want: 1,
			fixed: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n1:\tvzeroupper; ret\n"},
		{name: "same line", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3; ret\n", want: 1,
			fixed: "f:\n\tvaddps %ymm1, %ymm2, %ymm3; vzeroupper; ret\n"},
		{name: "prefixed ret: no fix", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\trep ret\n", want: 1},
		{name: "zmm", text: "f:\n\tvpaddd %zmm1, %zmm2, %zmm5\n\tret\n", want: 1, msg: "ymm5"},
		// Clean: vzeroupper, 128-bit VEX, legacy SSE only, unknown entry.
		{name: "vzeroupper", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tvzeroupper\n\tret\n"},
		{name: "vex128 clears", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tvaddps %xmm1, %xmm2, %xmm3\n\tret\n"},
		{name: "sse only", text: "f:\n\taddps %xmm1, %xmm2\n\tret\n"},
		{name: "nothing", text: "f:\n\tret\n"},
		// ymm0 alone at ret: a __m256 return value.
		{name: "ymm0 return", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm0\n\tret\n"},
		// ymm arguments set up before the call.
		{name: "ymm args", text: "f:\n\tvmovaps %ymm5, %ymm0\n\tcall g256\n\tvzeroupper\n\tret\n"},
		{name: "ymm tail args", text: "f:\n\tvmovaps %ymm5, %ymm1\n\tjmp g256\n"},
		// Only one path dirty: the join is unknown, no warning.
		{name: "one path", text: "f:\n\ttestl %edi, %edi\n\tjz 1f\n\tvaddps %ymm1, %ymm2, %ymm3\n\tvzeroupper\n1:\tret\n"},
		// AMD targets have no transition penalty.
		{name: "znver", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tret\n", tgt: lint.Target{CPU: "znver3"}},
		{name: "far ret", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tlret\n"},
		{name: "indirect jump", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tjmp *%rax\n"},
		{name: "local jump", text: "f:\n\tvaddps %ymm1, %ymm2, %ymm3\n\tjmp 1f\n1:\tvzeroupper\n\tret\n"},
	})
}

func TestVzeroupperCallee(t *testing.T) {
	check(t, CodeVzeroupper, []tc{
		// In-document callee that uses ymm itself.
		{name: "ymm callee", text: "f:\n\tvaddps %ymm11, %ymm12, %ymm13\n\tcall g\n\tvzeroupper\n\tret\n" +
			"g:\n\tvmulps %ymm3, %ymm3, %ymm4\n\tvzeroupper\n\tret\n"},
		// In-document callee with no SSE and no calls out.
		{name: "scalar callee", text: "f:\n\tvaddps %ymm11, %ymm12, %ymm13\n\tcall g\n\tvzeroupper\n\tret\n" +
			"g:\n\tleal 1(%rdi), %eax\n\tret\n"},
		// In-document callee running legacy SSE: the penalty is real.
		{name: "sse callee", text: "f:\n\tvaddps %ymm11, %ymm12, %ymm13\n\tcall g\n\tvzeroupper\n\tret\n" +
			"g:\n\taddps %xmm1, %xmm2\n\tret\n", want: 1, msg: "call"},
		// In-document callee that calls out.
		{name: "leaving callee", text: "f:\n\tvaddps %ymm11, %ymm12, %ymm13\n\tcall g\n\tvzeroupper\n\tret\n" +
			"g:\n\tcall h\n\tret\n", want: 1},
		{name: "jumping callee", text: "f:\n\tvaddps %ymm11, %ymm12, %ymm13\n\tcall g\n\tvzeroupper\n\tret\n" +
			"g:\n\tjmp h\n", want: 1},
	})
}

func TestFalseDep(t *testing.T) {
	check(t, CodeFalseDep, []tc{
		{name: "popcnt", text: "f:\n\tpopcntq (%rdi), %rax\n\tret\n", want: 1, msg: "Ice Lake",
			fixed: "f:\n\txorl %eax, %eax\n\tpopcntq (%rdi), %rax\n\tret\n"},
		{name: "lzcnt r8", text: "f:\n\tlzcntq %rsi, %r8\n\tret\n", want: 1, msg: "Cannon Lake",
			fixed: "f:\n\txorl %r8d, %r8d\n\tlzcntq %rsi, %r8\n\tret\n"},
		{name: "tzcnt 32", text: "f:\n\ttzcntl %esi, %ecx\n\tret\n", want: 1, tgt: lint.Target{CPU: "haswell"}},
		{name: "level target", text: "f:\n\tpopcntl %esi, %ecx\n\tret\n", want: 1, tgt: lint.Target{CPU: "x86-64-v3"}},
		{name: "unknown cpu", text: "f:\n\tpopcntl %esi, %ecx\n\tret\n", want: 1, tgt: lint.Target{CPU: "native"}},
		{name: "snb popcnt", text: "f:\n\tpopcntl %esi, %ecx\n\tret\n", want: 1, tgt: lint.Target{CPU: "sandybridge"}},
		{name: "merged def", text: "f:\n\ttestl %edi, %edi\n\tjz 1f\n\txorl %eax, %eax\n1:\tpopcntl %esi, %eax\n\tret\n", want: 1},
		// Clean.
		{name: "xor first", text: "f:\n\txorl %eax, %eax\n\tpopcntq (%rdi), %rax\n\tret\n"},
		{name: "sub first", text: "f:\n\tsubl %eax, %eax\n\tpopcntq (%rdi), %rax\n\tret\n"},
		{name: "mov imm first", text: "f:\n\tmovl $0, %eax\n\tpopcntq (%rdi), %rax\n\tret\n"},
		{name: "dst is source", text: "f:\n\tpopcntq %rax, %rax\n\tret\n"},
		{name: "dst in address", text: "f:\n\tpopcntq (%rax), %rax\n\tret\n"},
		{name: "16-bit dst", text: "f:\n\tpopcntw %si, %ax\n\tret\n"},
		{name: "icelake popcnt", text: "f:\n\tpopcntl %esi, %ecx\n\tret\n", tgt: lint.Target{CPU: "icelake"}},
		{name: "snb lzcnt is bsr", text: "f:\n\tlzcntl %esi, %ecx\n\tret\n", tgt: lint.Target{CPU: "sandybridge"}},
		{name: "zen", text: "f:\n\tpopcntl %esi, %ecx\n\tret\n", tgt: lint.Target{CPU: "znver2"}},
		{name: "unreachable", text: "f:\n\tret\n\tpopcntl %esi, %ecx\n"},
		{name: "mov reg is not a breaker", text: "f:\n\tmovl %edx, %ecx\n\tpopcntl %esi, %ecx\n\tret\n", want: 1},
	})
}

func TestPartialFlags(t *testing.T) {
	check(t, CodePartialFlag, []tc{
		{name: "adc after dec", text: "f:\n\tclc\n\tdecl %ecx\n\tadcl $0, %eax\n\tret\n", want: 1, msg: "decl on line 3"},
		{name: "jc not adjacent", text: "f:\n\tclc\n\tincl %eax\n\tmovl %eax, %edx\n\tjc 1f\n1:\tret\n", want: 1},
		{name: "cmp inc jb idiom", text: "f:\n\tcmpl %eax, %ebx\n\tincl %ecx\n\tjb 1f\n1:\tret\n", want: 1},
		// Clean.
		{name: "add", text: "f:\n\tclc\n\tsubl $1, %ecx\n\tadcl $0, %eax\n\tret\n"},
		{name: "zf reader", text: "f:\n\tdecl %ecx\n\tjnz f\n\tret\n"},
		{name: "pushf", text: "f:\n\tincb %ah\n\tpushfq\n\tret\n"},
		// The adjacent jcc case belongs to inc-no-carry.
		{name: "stale carry", text: "f:\n\tincl %eax\n\tjc 1f\n1:\tret\n"},
	})
}

func TestIncNoCarry(t *testing.T) {
	check(t, CodeIncNoCarry, []tc{
		{name: "inc jc", text: "f:\n\tincl %eax\n\tjc 1f\n1:\tret\n", want: 1, msg: "use add $1"},
		{name: "dec setc", text: "f:\n\tmovl $1, %eax\n\tdecl %eax\n\tsetc %dl\n\tret\n", want: 1, msg: "use sub $1"},
		{name: "dec cmovb", text: "f:\n\tdecl %eax\n\tcmovbl %ecx, %edx\n\tret\n", want: 1},
		{name: "dec jbe", text: "f:\n\tdecl %eax\n\tjbe 1f\n1:\tret\n", want: 1},
		// Clean.
		{name: "cmp inc jb", text: "f:\n\tcmpl %eax, %ebx\n\tincl %ecx\n\tjb 1f\n1:\tret\n"},
		{name: "adc chain", text: "f:\n\tincl %ecx\n\tadcl $0, %eax\n\tret\n"},
		{name: "not adjacent", text: "f:\n\tincl %eax\n\tmovl %eax, %edx\n\tjc 1f\n1:\tret\n"},
		{name: "add", text: "f:\n\taddl $1, %eax\n\tjc 1f\n1:\tret\n"},
		{name: "jz", text: "f:\n\tincl %eax\n\tjz 1f\n1:\tret\n"},
	})
}

func TestSlowForms(t *testing.T) {
	check(t, CodeSlowLoop, []tc{
		{name: "loop32", text: "1:\tnop\n\tloop 1b\n", tgt: lint.Target{Mode: asm.Mode32}, want: 1},
		{name: "loopnz", text: "1:\tnop\n\tloopnz 1b\n", want: 1, msg: "loopnz"},
		{name: "16-bit", text: "1:\tnop\n\tloop 1b\n", tgt: lint.Target{Mode: asm.Mode16}},
		{name: "in macro body", text: ".macro m\n1:\tloop 1b\n.endm\n"},
	})
	check(t, CodeSlowEnter, []tc{
		{name: "enter64", text: "f:\n\tenter $32, $0\n\tleave\n\tret\n", want: 1,
			fixed: "f:\n\tpushq %rbp; movq %rsp, %rbp; leaq -32(%rsp), %rsp\n\tleave\n\tret\n"},
		{name: "enter64 zero", text: "\tenter $0, $0\n", want: 1, fixed: "\tpushq %rbp; movq %rsp, %rbp\n"},
		{name: "level", text: "\tenter $8, $2\n", tgt: lint.Target{Mode: asm.Mode16}, want: 1, msg: "nesting level 2"},
		{name: "symbolic size: no fix", text: "\tenter $FRAME, $0\n", want: 1},
		{name: "enter32", text: "\tenter $8, $0\n", tgt: lint.Target{Mode: asm.Mode32}},
		{name: "unknown level", text: "\tenter $8, $LVL\n"},
	})
	check(t, CodeXchgMem, []tc{
		{name: "mem dst", text: "\txchgl %eax, (%rdi)\n", want: 1},
		{name: "mem src", text: "\txchgl (%rdi), %eax\n", want: 1},
		{name: "regs", text: "\txchgl %eax, %ebx\n"},
	})
	check(t, CodeBtMem, []tc{
		{name: "bt", text: "\tbtl %ecx, (%rdi)\n", want: 1},
		{name: "bts", text: "\tbtsq %rcx, bitmap(%rip)\n", want: 1},
		{name: "imm", text: "\tbtl $3, (%rdi)\n"},
		{name: "reg", text: "\tbtl %ecx, %eax\n"},
		{name: "other", text: "\tmovl %ecx, (%rdi)\n"},
	})
	sky := lint.Target{CPU: "skylake"}
	check(t, CodeLea3, []tc{
		{name: "three", text: "\tleaq 8(%rdi,%rsi,4), %rax\n", tgt: sky, want: 1, msg: "skylake"},
		{name: "alias", text: "\tleaq 8(%rdi,%rsi,4), %rax\n", tgt: lint.Target{CPU: "core-avx2"}, want: 1},
		{name: "two", text: "\tleaq (%rdi,%rsi,4), %rax\n", tgt: sky},
		{name: "zero disp", text: "\tleaq 0(%rdi,%rsi,4), %rax\n", tgt: sky},
		{name: "no index", text: "\tleaq 8(%rdi), %rax\n", tgt: sky},
		{name: "no cpu", text: "\tleaq 8(%rdi,%rsi,4), %rax\n"},
		{name: "zen", text: "\tleaq 8(%rdi,%rsi,4), %rax\n", tgt: lint.Target{CPU: "znver4"}},
	})
	check(t, CodeDivConst, []tc{
		{name: "pow2", text: "f:\n\tmovl $16, %ecx\n\tdivl %ecx\n\tret\n", want: 1, msg: "shift right by 4 (and mask with 15"},
		{name: "idiv pow2", text: "f:\n\tmovl $8, %ecx\n\tcltd\n\tidivl %ecx\n\tret\n", want: 1, msg: "arithmetic shift by 3"},
		{name: "ten", text: "f:\n\tmovl $10, %ecx\n\tdivl %ecx\n\tret\n", want: 1, msg: "reciprocal"},
		{name: "byte", text: "f:\n\tmovb $4, %cl\n\tdivb %cl\n\tret\n", want: 1},
		{name: "16-bit pow2", text: "f:\n\tmovw $2, %bx\n\tdivw %bx\n\tret\n", tgt: lint.Target{Mode: asm.Mode16}, want: 1},
		{name: "16-bit ten", text: "f:\n\tmovw $10, %bx\n\tdivw %bx\n\tret\n", tgt: lint.Target{Mode: asm.Mode16}},
		{name: "unknown", text: "f:\n\tdivl %ecx\n\tret\n"},
		{name: "zero", text: "f:\n\txorl %ecx, %ecx\n\tdivl %ecx\n\tret\n"},
		{name: "negative", text: "f:\n\tmovl $-4, %ecx\n\tidivl %ecx\n\tret\n"},
		{name: "memory", text: "f:\n\tdivl (%rdi)\n\tret\n"},
	})
}

func TestNegative(t *testing.T) {
	if negative(1, 0) || negative(1, 65) || !negative(0x80, 8) || negative(0x7F, 8) {
		t.Error("negative")
	}
}

// TestNoFlow covers the rules' guard when the dataflow is unavailable:
// a context with no program never reaches the analysis.
func TestNoFlow(t *testing.T) {
	for _, code := range []string{CodeVzeroupper, CodeFalseDep, CodePartialFlag, CodeIncNoCarry, CodeDivConst,
		CodeSlowLoop, CodeSlowEnter, CodeXchgMem, CodeBtMem, CodeLea3} {
		r, _ := lint.Lookup(code)
		got := lint.Run(lint.Input{Lang: lint.LangAsm, Target: lint.Target{CPU: "skylake"}, Rules: []*lint.Rule{r}})
		if len(got) != 0 {
			t.Errorf("%s: %v", code, got)
		}
	}
}
