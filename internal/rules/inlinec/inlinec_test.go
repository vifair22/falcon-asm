package inlinec

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

var allCodes = []string{CodeOperandRef, CodeMissingClobber, CodeMissingMemory, CodeEarlyClobber,
	CodeOperandWidth, CodeOperandWidthTD, CodeHardcodedReg, CodeMissingVolatile}

type env struct {
	mode int
	cc   string
}

var x64 = env{mode: 64, cc: "sysv"}

func ruleSet(t *testing.T, codes ...string) []*lint.Rule {
	t.Helper()
	if len(codes) == 0 {
		codes = allCodes
	}
	var out []*lint.Rule
	for _, c := range codes {
		r, ok := lint.Lookup(c)
		if !ok {
			t.Fatalf("rule %s not registered", c)
		}
		out = append(out, r)
	}
	return out
}

// lintC lints a C snippet with the given rules, as the project runner's
// LangC pass does.
func lintC(t *testing.T, src string, e env, codes ...string) []lint.Finding {
	t.Helper()
	doc := &docstore.Document{URI: "file:///t.c", Text: src}
	var b lint.Batch
	b.Add(lint.Input{
		Prog: &asm.Program{Doc: doc}, Lang: lint.LangC, Inline: inlineasm.Extract(src),
		Target: lint.Target{Mode: asm.Mode(e.mode), CC: e.cc}, Rules: ruleSet(t, codes...),
	})
	return b.Finish(nil, lint.FileConfig{})
}

// apply applies a fix's edits to src.
func apply(t *testing.T, src string, fx lint.Fix) string {
	t.Helper()
	doc := &docstore.Document{Text: src}
	type off struct {
		s, e int
		text string
	}
	var es []off
	for _, e := range fx.Edits {
		s, err1 := doc.Offset(e.Range.Start)
		en, err2 := doc.Offset(e.Range.End)
		if err1 != nil || err2 != nil {
			t.Fatalf("bad edit range %+v", e.Range)
		}
		es = append(es, off{s, en, e.NewText})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].s > es[j].s })
	for _, e := range es {
		src = src[:e.s] + e.text + src[e.e:]
	}
	return src
}

// spanText is the C text a finding covers.
func spanText(src string, f lint.Finding) string {
	doc := &docstore.Document{Text: src}
	s, _ := doc.Offset(f.Range.Start)
	e, _ := doc.Offset(f.Range.End)
	return src[s:e]
}

type ruleCase struct {
	name string
	src  string
	env  env
	// want lists "text|message fragment" per finding, in order; the text
	// is what the finding's range covers.
	want []string
	// fixed is the source after applying the first fix of the first
	// finding; "" when no fix is expected. The fixed source must lint
	// clean for the rule.
	fixed string
}

func runCases(t *testing.T, code string, cases []ruleCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.env
			if e.mode == 0 {
				e = x64
			}
			got := lintC(t, tc.src, e, code)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d findings, want %d:\n%v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				text, frag, _ := strings.Cut(w, "|")
				if s := spanText(tc.src, got[i]); s != text {
					t.Errorf("finding %d covers %q, want %q (%s)", i, s, text, got[i].Message)
				}
				if !strings.Contains(got[i].Message, frag) {
					t.Errorf("finding %d message %q lacks %q", i, got[i].Message, frag)
				}
			}
			if tc.fixed == "" {
				if len(got) > 0 && len(got[0].Fixes) > 0 {
					t.Fatalf("unexpected fix %+v", got[0].Fixes)
				}
				return
			}
			if len(got) == 0 || len(got[0].Fixes) == 0 {
				t.Fatalf("no fix offered: %v", got)
			}
			fixed := apply(t, tc.src, got[0].Fixes[0])
			if fixed != tc.fixed {
				t.Fatalf("fixed source:\n%s\nwant:\n%s", fixed, tc.fixed)
			}
			if again := lintC(t, fixed, e, code); len(again) != 0 {
				t.Fatalf("findings remain after the fix: %v", again)
			}
		})
	}
}

func TestOperandRef(t *testing.T) {
	runCases(t, CodeOperandRef, []ruleCase{
		{name: "in range", src: `void f(void){ asm("movl %1, %0" : "=r"(o) : "r"(i)); }`},
		{name: "out of range", src: `void f(void){ asm("movl %2, %0" : "=r"(o) : "r"(i)); }`,
			want: []string{"%2|refers to operand 2, but the statement has 2 operands"}},
		{name: "unknown name", src: `void f(void){ asm("movl %[x], %0" : "=r"(o) : [y] "r"(i)); }`,
			want: []string{"%[x]|names no operand (named: y)"}},
		{name: "no names", src: `void f(void){ asm("movl %[x], %0" : "=r"(o) : "r"(i)); }`,
			want: []string{"%[x]|no operand has a name"}},
		{name: "l outside goto", src: `void f(void){ asm("jmp %l0" : : "r"(x)); }`,
			want: []string{"%l0|not an asm goto"}},
		{name: "l on operand", src: `void f(void){ asm goto("jmp %l0" : : "r"(x) : : out); out: ; }`,
			want: []string{"%l0|operand 0 is an input"}},
		{name: "goto label ok", src: `void f(void){ asm goto("jmp %l1; jmp %l[out]" : : "r"(x) : : out); out: ; }`},
		{name: "label past end", src: `void f(void){ asm goto("jmp %l2" : : "r"(x) : : out); out: ; }`,
			want: []string{"%l2|statement has 2 operands"}},
		{name: "unknown modifier", src: `void f(void){ asm("movl %m0, %%eax" : : "r"(x) : "eax"); }`,
			want: []string{"%m0|'m' is not an x86 operand modifier"}},
		{name: "known modifiers", src: `void f(void){ asm("movl %k0, %%eax; movb %b0, %%al; movb %h0, %%ah; movq %q0, %%rax" : : "Q"(x) : "rax"); }`},
		{name: "single percent register", src: `void f(void){ asm("movq %r8, %0" : "=r"(o)); }`,
			want:  []string{"%r8|a register is written %%r8"},
			fixed: `void f(void){ asm("movq %%r8, %0" : "=r"(o)); }`},
		{name: "basic asm skipped", src: `void f(void){ asm("movl %2, %eax"); }`},
	})
}

func TestMissingClobber(t *testing.T) {
	runCases(t, CodeMissingClobber, []ruleCase{
		{name: "cpuid", src: `void f(void){ unsigned a, d; asm volatile("cpuid" : "=a"(a), "=d"(d) : "a"(0)); }`,
			want: []string{
				"cpuid|cpuid implicitly writes %rbx, but it is neither an output nor in the clobber list",
				"cpuid|cpuid implicitly writes %rcx",
			},
			fixed: `void f(void){ unsigned a, d; asm volatile("cpuid" : "=a"(a), "=d"(d) : "a"(0) : "rbx", "rcx"); }`},
		{name: "cpuid clobbered any spelling", src: `void f(void){ asm volatile("cpuid" : "=a"(a), "=d"(d) : "a"(0) : "%ebx", "cx"); }`},
		{name: "cpuid 32-bit names", env: env{mode: 32},
			src:   `void f(void){ asm volatile("cpuid" : "=a"(a), "=b"(b), "=d"(d) : "a"(0)); }`,
			want:  []string{"cpuid|writes %ecx"},
			fixed: `void f(void){ asm volatile("cpuid" : "=a"(a), "=b"(b), "=d"(d) : "a"(0) : "ecx"); }`},
		{name: "rdtsc outputs only", src: "void f(void){ asm volatile(\"rdtsc\" : \"=a\"(lo)\n\t); }",
			want:  []string{"rdtsc|implicitly writes %rdx"},
			fixed: "void f(void){ asm volatile(\"rdtsc\" : \"=a\"(lo) : : \"rdx\"\n\t); }"},
		{name: "rdtsc trailing line comment", src: "void f(void){ asm volatile(\"rdtsc\" : \"=a\"(lo) // lo\n\t); }",
			want:  []string{"rdtsc|%rdx"},
			fixed: "void f(void){ asm volatile(\"rdtsc\" : \"=a\"(lo) // lo\n\t : : \"rdx\"); }"},
		{name: "explicit write, style of existing clobbers",
			src:   `void f(void){ asm volatile("movl $1, %%eax; movl $2, %%esi" : : : "memory", "ecx"); }`,
			want:  []string{"%%eax|movl writes %rax", "%%esi|movl writes %rsi"},
			fixed: `void f(void){ asm volatile("movl $1, %%eax; movl $2, %%esi" : : : "memory", "ecx", "eax", "esi"); }`},
		{name: "empty clobber section", src: `void f(void){ asm volatile("xorl %%ebx, %%ebx" : "=a"(a) : "a"(0) : ); }`,
			want:  []string{"%%ebx|%rbx"},
			fixed: `void f(void){ asm volatile("xorl %%ebx, %%ebx" : "=a"(a) : "a"(0) : "rbx" ); }`},
		{name: "goto with labels", src: `void f(void){ asm goto("xorl %%edx, %%edx; jmp %l[out]" : : : : out); out: ; }`,
			want:  []string{"%%edx|%rdx"},
			fixed: `void f(void){ asm goto("xorl %%edx, %%edx; jmp %l[out]" : : : "rdx" : out); out: ; }`},
		{name: "input register modified", src: `void f(void){ asm volatile("rep stosb" : : "D"(p), "c"(n), "a"(0) : "memory"); }`,
			want: []string{"stosb|which is input operand %1", "stosb|which is input operand %0"}},
		{name: "rep movs in/out", src: `void f(void){ asm volatile("rep movsb" : "+D"(d), "+S"(s), "+c"(n) : : "memory"); }`},
		{name: "push pop balanced", src: `void f(void){ asm volatile("push %%rbx; cpuid; movl %%ebx, %1; pop %%rbx" : "=a"(a), "=r"(b), "=c"(c), "=d"(d) : "a"(0)); }`},
		{name: "pop without push", src: `void f(void){ asm volatile("pop %%rbx" : : : "memory"); }`,
			want: []string{"%%rbx|pop writes %rbx"}, fixed: `void f(void){ asm volatile("pop %%rbx" : : : "memory", "rbx"); }`},
		{name: "pic xchg", env: env{mode: 32},
			src: `void f(void){ asm volatile("xchgl %%ebx, %1; cpuid; xchgl %%ebx, %1" : "=a"(a), "=r"(b), "=c"(c), "=d"(d) : "a"(0)); }`},
		{name: "mov save xchg restore",
			src: `void f(void){ asm volatile("movq %%rbx, %q1; cpuid; xchgq %%rbx, %q1" : "=a"(a), "=&r"(b), "=c"(c), "=d"(d) : "a"(0)); }`},
		{name: "register pair swapped back", src: `void f(void){ asm volatile("xchg %%rsi, %%rdi; xchg %%rsi, %%rdi" ::: "memory"); }`},
		{name: "save overwritten", src: `void f(void){ asm volatile("movq %%rbx, %%r8; cpuid; movq $0, %%r8; movq %%r8, %%rbx" : "=a"(a), "=c"(c), "=d"(d) : "a"(0) : "r8"); }`,
			want: []string{"cpuid|%rbx"}, fixed: `void f(void){ asm volatile("movq %%rbx, %%r8; cpuid; movq $0, %%r8; movq %%r8, %%rbx" : "=a"(a), "=c"(c), "=d"(d) : "a"(0) : "r8", "rbx"); }`},
		{name: "leaves the template", src: `void f(void){ asm volatile("xorl %%ebx, %%ebx; jmp *%0" : : "r"(entry)); }`},
		{name: "local jump does not leave", src: `void f(void){ asm volatile("xorl %%ebx, %%ebx; jmp 1f; 1:" : : : "memory"); }`,
			want: []string{"%%ebx|%rbx"}, fixed: `void f(void){ asm volatile("xorl %%ebx, %%ebx; jmp 1f; 1:" : : : "memory", "rbx"); }`},
		{name: "unknown instruction", src: `void f(void){ asm volatile(".byte 0x0f, 0xa2" : : : "memory"); }`},
		{name: "syscall", src: `void f(void){ asm volatile("syscall" : "=a"(r) : "a"(n) : "memory"); }`,
			want:  []string{"syscall|%r11", "syscall|%rcx"},
			fixed: `void f(void){ asm volatile("syscall" : "=a"(r) : "a"(n) : "memory", "rcx", "r11"); }`},
		{name: "mul widths", src: `void f(void){ asm("mull %2" : "=a"(lo), "=d"(hi) : "r"(x), "a"(y)); asm("mulb %1" : "=a"(lo) : "q"(x), "a"(y)); asm("mul %1" : "=a"(lo) : "r"(x), "a"(y)); }`},
		{name: "mull without rdx", src: `void f(void){ asm("mull %1" : "=a"(lo) : "r"(x), "a"(y)); }`,
			want: []string{"mull|implicitly writes %rdx"}, fixed: `void f(void){ asm("mull %1" : "=a"(lo) : "r"(x), "a"(y) : "rdx"); }`},
		{name: "r8 in 32-bit code", env: env{mode: 32}, src: `void f(void){ asm volatile("xorl %%r8d, %%r8d" ::: "memory"); }`},
		{name: "vzeroall 32-bit", env: env{mode: 32}, src: `void f(void){ asm volatile("vzeroall" ::: "xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6"); }`,
			want:  []string{"vzeroall|%xmm7"},
			fixed: `void f(void){ asm volatile("vzeroall" ::: "xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6", "xmm7"); }`},
		{name: "ymm write is xmm family", src: `void f(void){ asm volatile("vpxor %%ymm3, %%ymm3, %%ymm3" ::: "xmm3"); }`},
		{name: "inactive branch", src: "void f(void){\n#if defined(__x86_64__)\n asm volatile(\"cpuid\" : \"+a\"(a), \"=b\"(b), \"+c\"(c), \"=d\"(d));\n#else\n asm volatile(\"cpuid\" : \"+a\"(a));\n#endif\n}"},
		{name: "string ops", src: `void f(void){ asm volatile("lodsb; scasb; cmpsb; outsb; insb" ::: "memory"); }`,
			want:  []string{"lodsb|%rax", "lodsb|%rsi", "scasb|%rdi"},
			fixed: `void f(void){ asm volatile("lodsb; scasb; cmpsb; outsb; insb" ::: "memory", "rax", "rsi", "rdi"); }`},
		{name: "partial template", src: `void f(void){ asm volatile("cpuid" MACRO : "=a"(a)); }`},
	})
}

func TestMissingClobberSingleFix(t *testing.T) {
	src := `void f(void){ asm volatile("cpuid" : "=a"(a), "=d"(d) : "a"(0)); }`
	got := lintC(t, src, x64, CodeMissingClobber)
	if len(got) != 2 || len(got[1].Fixes) != 2 || got[1].Fixes[1].Title != `Add clobber "rcx"` {
		t.Fatalf("findings = %+v", got)
	}
	want := `void f(void){ asm volatile("cpuid" : "=a"(a), "=d"(d) : "a"(0) : "rcx"); }`
	if s := apply(t, src, got[1].Fixes[1]); s != want {
		t.Fatalf("single fix = %s", s)
	}
}

func TestImplicitWriteTable(t *testing.T) {
	cases := map[string][]string{
		"cpuid": {"rax", "rbx", "rcx", "rdx"}, "rdtsc": {"rax", "rdx"}, "rdtscp": {"rax", "rcx", "rdx"},
		"rdmsr": {"rax", "rdx"}, "xgetbv": {"rax", "rdx"}, "rdpmc": {"rax", "rdx"},
		"cltd": {"rdx"}, "cqto": {"rdx"}, "cwtl": {"rax"}, "cltq": {"rax"}, "inb $0x60": {"rax"},
		"inb %dx, %al": {"rax"}, "lodsl": {"rax", "rsi"}, "stosq": {"rdi"}, "rep stosq": {"rcx", "rdi"},
		"movsb": {"rdi", "rsi"}, "movsl": {"rdi", "rsi"}, "movsd": {"rdi", "rsi"}, "cmpsd": {"rdi", "rsi"},
		"repe cmpsb": {"rcx", "rdi", "rsi"}, "scasb": {"rdi"}, "insw": {"rdi"}, "outsb": {"rsi"},
		"1: loop 1b": {"rcx"}, "xlatb": {"rax"}, "syscall": {"r11", "rcx"},
		"cmpxchgl %ecx, (%rdi)": {"rax"}, "cmpxchg8b (%rdi)": {"rax", "rdx"}, "cmpxchg16b (%rdi)": {"rax", "rdx"},
		"leave": {"rbp"}, "enter $16, $0": {"rbp"}, "popal": {"rax", "rbp", "rbx", "rcx", "rdi", "rdx", "rsi"},
		"mulq %rcx": {"rax", "rdx"}, "divb %cl": {"rax"}, "imull %ecx": {"rax", "rdx"}, "imull %ecx, %edx": {"rdx"},
		"lahf": {"rax"}, "xchg %eax, %ebx": {"rax", "rbx"}, "xaddl %eax, %ebx": {"rax", "rbx"},
		"pcmpistri $0, %xmm1, %xmm2": {"rcx"}, "pcmpistrm $0, %xmm1, %xmm2": {"xmm0"},
		"cmpl %eax, %ebx": nil, "testl %eax, %eax": nil, "outb %al, %dx": nil, "pushq %rax": nil,
		"btl $1, %eax": nil, "btsl $1, %eax": {"rax"}, "setc %al": {"rax"}, "popq %rcx": {"rcx"},
		"fstpl (%rax)": nil, "fnstsw %ax": {"rax"}, "fldl (%rax)": nil, "nopl 0(%rax)": nil,
		"wrmsr": nil, "xsetbv": nil, "rdrand %rax": {"rax"}, "movq %rax, %cr3": nil,
	}
	for text, want := range cases {
		insns := parseTemplate(text, nil, 0, 64)
		m := &stmtModel{st: &inlineasm.Stmt{}, mode: 64, insns: insns}
		f := m.facts()
		got := sortedFams(f.written)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q writes %v, want %v", text, got, want)
		}
	}
	m := &stmtModel{st: &inlineasm.Stmt{}, mode: 32, insns: parseTemplate("vzeroupper", nil, 0, 32)}
	if got := sortedFams(m.facts().written); len(got) != 8 {
		t.Errorf("vzeroupper in 32-bit code writes %v", got)
	}
}

func TestMissingMemory(t *testing.T) {
	runCases(t, CodeMissingMemory, []ruleCase{
		{name: "store through pointer", src: `void f(void){ asm volatile("movl %1, (%0)" : : "r"(p), "r"(v)); }`,
			want:  []string{"(%0)|movl stores to (%0), which is not a memory operand"},
			fixed: `void f(void){ asm volatile("movl %1, (%0)" : : "r"(p), "r"(v) : "memory"); }`},
		{name: "memory output describes it", src: `void f(void){ asm volatile("movl %2, (%1)" : "=m"(*p) : "r"(p), "r"(v)); }`},
		{name: "described operand", src: `void f(void){ asm volatile("incl %0" : "+m"(x)); }`},
		{name: "store to input", src: `void f(void){ asm volatile("incl %0" : : "m"(x)); }`,
			want:  []string{"%0|stores to input operand %0"},
			fixed: `void f(void){ asm volatile("incl %0" : : "m"(x) : "memory"); }`},
		{name: "rep stos", src: `void f(void){ asm volatile("rep stosb" : "+D"(p), "+c"(n) : "a"(0)); }`,
			want:  []string{"stosb|stosb stores through %rdi"},
			fixed: `void f(void){ asm volatile("rep stosb" : "+D"(p), "+c"(n) : "a"(0) : "memory"); }`},
		{name: "rep stos 32-bit", env: env{mode: 32}, src: `void f(void){ asm volatile("rep stosb" : "+D"(p), "+c"(n) : "a"(0)); }`,
			want:  []string{"stosb|through %edi"},
			fixed: `void f(void){ asm volatile("rep stosb" : "+D"(p), "+c"(n) : "a"(0) : "memory"); }`},
		{name: "stack is not memory", src: `void f(void){ asm volatile("movl %%eax, 4(%%rsp)" ::: "eax"); }`},
		{name: "xsave", src: `void f(void){ asm volatile("xsave (%0)" : : "r"(buf), "a"(-1), "d"(-1)); }`,
			want:  []string{"(%0)|xsave stores to (%0)"},
			fixed: `void f(void){ asm volatile("xsave (%0)" : : "r"(buf), "a"(-1), "d"(-1) : "memory"); }`},
		{name: "already clobbered", src: `void f(void){ asm volatile("movl %1, (%0)" : : "r"(p), "r"(v) : "memory"); }`},
		{name: "global symbol", src: `void f(void){ asm volatile("movl $1, counter(%%rip)" :); }`,
			want:  []string{"counter(%%rip)|stores to counter(%rip)"},
			fixed: `void f(void){ asm volatile("movl $1, counter(%%rip)" : : : "memory"); }`},
		{name: "loads only", src: `void f(void){ asm volatile("movl (%1), %0" : "=r"(v) : "r"(p)); }`},
	})
}

func TestEarlyClobber(t *testing.T) {
	runCases(t, CodeEarlyClobber, []ruleCase{
		{name: "classic", src: `void f(void){ asm("movl %1, %0\n\taddl %2, %0" : "=r"(s) : "r"(a), "r"(b)); }`,
			want:  []string{`"=r"|output %0 is written by movl before input %2 is read by addl`},
			fixed: `void f(void){ asm("movl %1, %0\n\taddl %2, %0" : "=&r"(s) : "r"(a), "r"(b)); }`},
		{name: "already early", src: `void f(void){ asm("movl %1, %0\n\taddl %2, %0" : "=&r"(s) : "r"(a), "r"(b)); }`},
		{name: "read write", src: `void f(void){ asm("movl %1, %0\n\taddl %2, %0" : "+r"(s) : "r"(a), "r"(b)); }`},
		{name: "tied input", src: `void f(void){ asm("addl %2, %0\n\taddl %1, %0" : "=r"(s) : "0"(a), "r"(b)); }`},
		{name: "same instruction", src: `void f(void){ asm("leal (%1,%2), %0" : "=r"(s) : "r"(a), "r"(b)); }`},
		{name: "immediate input", src: `void f(void){ asm("movl $0, %0\n\taddl %1, %0" : "=r"(s) : "i"(4)); }`},
		{name: "memory input address", src: `void f(void){ asm("movl $0, %0\n\taddl %1, %0" : "=r"(s) : "m"(x)); }`,
			want:  []string{`"=r"|before input %1 is read`},
			fixed: `void f(void){ asm("movl $0, %0\n\taddl %1, %0" : "=&r"(s) : "m"(x)); }`},
		{name: "fixed output implicit write", src: `void f(void){ asm("rdtsc\n\taddl %1, %%eax" : "=a"(lo) : "r"(x) : "rdx"); }`,
			want:  []string{`"=a"|output %0 is written by rdtsc before input %1`},
			fixed: `void f(void){ asm("rdtsc\n\taddl %1, %%eax" : "=&a"(lo) : "r"(x) : "rdx"); }`},
		{name: "fixed against fixed", src: `void f(void){ asm("cpuid\n\tmovl %4, %%esi" : "=a"(a), "=b"(b), "=c"(c), "=d"(d) : "S"(x)); }`},
		{name: "classes differ", src: `void f(void){ asm("movd %1, %0\n\tpaddd %2, %0" : "=x"(v) : "r"(a), "r"(b)); }`},
		{name: "named operands", src: `void f(void){ asm("movl %[a], %[s]\n\taddl %[b], %[s]" : [s] "=r"(s) : [a] "r"(a), [b] "r"(b)); }`,
			want:  []string{`"=r"|output %[s] is written by movl before input %[b]`},
			fixed: `void f(void){ asm("movl %[a], %[s]\n\taddl %[b], %[s]" : [s] "=&r"(s) : [a] "r"(a), [b] "r"(b)); }`},
		{name: "jump out between", src: `void f(void){ asm("movl %1, %0\n\tjmp 1f\n\taddl %2, %0\n1:" : "=r"(s) : "r"(a), "r"(b)); }`},
		{name: "copy of the input", src: `void f(void){ asm("movl %1, %0\n\taddl %1, %0" : "=r"(s) : "r"(a)); }`},
		{name: "copy then other write", src: `void f(void){ asm("movl %1, %0\n\tshll $1, %0\n\taddl %1, %0" : "=r"(s) : "r"(a)); }`,
			want:  []string{`"=r"|written by movl before input %1 is read by addl`},
			fixed: `void f(void){ asm("movl %1, %0\n\tshll $1, %0\n\taddl %1, %0" : "=&r"(s) : "r"(a)); }`},
		{name: "write only output read as pure dest", src: `void f(void){ asm("movl %1, %0\n\tmovl %0, %2" : "=r"(s) : "r"(a), "r"(b)); }`},
	})
}

func TestOperandWidth(t *testing.T) {
	runCases(t, CodeOperandWidth, []ruleCase{
		{name: "uint8 into movl", src: "void f(void){ uint8_t v = 1; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%0|movl takes 32-bit operands, but %0 is printed as a 8-bit register ('v' is uint8_t)"}},
		{name: "uint32 fine", src: "void f(void){ uint32_t v = 1; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "width from register", src: "void f(void){ unsigned char v; asm(\"mov %0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%0|mov takes 32-bit operands"}},
		{name: "typedef is not this rule", src: "void f(void){ UINT8 v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "widening modifier on input", src: "void f(uint8_t v){ asm(\"movl %k0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%k0|widens a 8-bit input to 32 bits"}},
		{name: "widening modifier on output is fine", src: "void f(void){ uint8_t v; asm(\"movl $1, %k0\" : \"=r\"(v)); }"},
		{name: "pointer truncated", src: "void f(void){ char *p; asm(\"movb %b0, %%al\" : : \"r\"(p) : \"al\"); }",
			want: []string{"%b0|truncates a pointer to 8 bits ('p' is char *)"}},
		{name: "memory operand", src: "void f(void){ uint16_t w; asm(\"incl %0\" : \"+m\"(w)); }",
			want: []string{"%0|incl accesses 32 bits, but %0 is 16 bits ('w' is uint16_t)"}},
		{name: "cast", src: "void f(void){ asm(\"movl %0, %%eax\" : : \"r\"((uint64_t)x) : \"eax\"); }",
			want: []string{"%0|is printed as a 64-bit register ('(uint64_t)x' is uint64_t)"}},
		{name: "literal", src: "void f(void){ asm(\"movq %0, %%rax\" : : \"r\"(0u) : \"rax\"); }",
			want: []string{"%0|movq takes 64-bit operands, but %0 is printed as a 32-bit register"}},
		{name: "long under ms abi", env: env{mode: 64, cc: "ms"}, src: "void f(void){ unsigned long v; asm(\"movq %0, %%rax\" : : \"r\"(v) : \"rax\"); }",
			want: []string{"%0|32-bit register ('v' is unsigned long)"}},
		{name: "long under sysv", src: "void f(void){ unsigned long v; asm(\"movq %0, %%rax\" : : \"r\"(v) : \"rax\"); }"},
		{name: "long unknown abi", env: env{mode: 64}, src: "void f(void){ unsigned long v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "list declaration", src: "void f(void){ unsigned int a = 0, b = 0; asm(\"movq %0, %%rax\" : : \"r\"(b) : \"rax\"); }",
			want: []string{"%0|('b' is unsigned int)"}},
		{name: "shadowed with another width", src: "void f(void){ uint32_t v; { uint8_t v; } asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "unknown typedef", src: "void f(void){ mytype v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "expression", src: "void f(void){ asm(\"movl %0, %%eax\" : : \"r\"(s.v) : \"eax\"); }"},
		{name: "array", src: "void f(void){ uint8_t v[4]; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "shift count", src: "void f(void){ uint8_t n; uint32_t x; asm(\"shll %b1, %0\" : \"+r\"(x) : \"c\"(n)); }"},
		{name: "pointer movq ok", src: "void f(void){ void *p; asm(\"movq %0, %%rax\" : : \"r\"(p) : \"rax\"); }"},
		{name: "pointer in 32-bit code", env: env{mode: 32}, src: "void f(void){ void *p; asm(\"movq %0, %%mm0\" : : \"r\"(p)); asm(\"movl %0, %%eax\" : : \"r\"(p) : \"eax\"); }"},
		{name: "address of", src: "void f(void){ asm(\"movl %0, %%eax\" : : \"r\"(&x) : \"eax\"); }",
			want: []string{"%0|64-bit register ('&x' is void *)"}},
		{name: "in comment is not a declaration", src: "void f(void){ /* uint8_t v; */ uint32_t v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
		{name: "parameter", src: "void f(uint16_t port, uint64_t v){ asm(\"movl %1, %%eax\" : : \"r\"(port), \"r\"(v) : \"eax\"); }",
			want: []string{"%1|('v' is uint64_t)"}},
		{name: "short int", src: "void f(void){ short int v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%0|16-bit register"}},
		{name: "long long", src: "void f(void){ long long v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%0|64-bit register"}},
		{name: "port data", src: "void f(uint16_t port, uint16_t v){ asm volatile(\"outb %0, %1\" : : \"a\"(v), \"Nd\"(port)); }",
			want: []string{"%0|outb takes 8-bit operands, but %0 is printed as a 16-bit register ('v' is uint16_t)"}},
		{name: "port read", src: "void f(uint16_t port){ uint32_t v; asm volatile(\"inw %1, %0\" : \"=a\"(v) : \"Nd\"(port)); }",
			want: []string{"%0|inw takes 16-bit operands, but %0 is printed as a 32-bit register"}},
		{name: "port matches", src: "void f(uint16_t port, uint8_t v){ asm volatile(\"outb %0, %1; inb %1, %0\" : \"+a\"(v) : \"Nd\"(port)); }"},
		{name: "size_t", env: env{mode: 32}, src: "void f(void){ size_t v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
	})
}

func TestOperandWidthTypedef(t *testing.T) {
	runCases(t, CodeOperandWidthTD, []ruleCase{
		{name: "UINT8", src: "void f(void){ UINT8 v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%0|('v' is UINT8)"}},
		{name: "UINTN", src: "void f(void){ UINTN v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }",
			want: []string{"%0|64-bit register"}},
		{name: "standard type is not this rule", src: "void f(void){ uint8_t v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }"},
	})
	got := lintC(t, "void f(void){ u8 v; asm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\"); }", x64, CodeOperandWidthTD)
	if len(got) != 1 || got[0].Severity != lint.Info {
		t.Fatalf("typedef finding = %v", got)
	}
}

func TestHardcodedReg(t *testing.T) {
	runCases(t, CodeHardcodedReg, []ruleCase{
		{name: "unbound read", src: `void f(void){ asm("movl %%ecx, %0" : "=r"(v)); }`,
			want:  []string{`%%ecx|%rcx is read here, but no constraint binds it and no clobber reserves it; GCC may allocate %0 ("=r")`},
			fixed: `void f(void){ asm("movl %%ecx, %0" : "=r"(v) : : "rcx"); }`},
		{name: "bound", src: `void f(void){ asm("movl %%ecx, %0" : "=r"(v) : "c"(x)); }`},
		{name: "clobbered", src: `void f(void){ asm("movl %%ecx, %0" : "=r"(v) : : "ecx"); }`},
		{name: "written is the other rule", src: `void f(void){ asm("movl $1, %%ecx; movl %%ecx, %0" : "=r"(v)); }`},
		{name: "no free operand", src: `void f(void){ asm("movl %%ecx, %%edx" : : "a"(x) : "edx"); }`},
		{name: "stack and frame", src: `void f(void){ asm("movq %%rsp, %0; movq %%rbp, %1" : "=r"(a), "=r"(b)); }`},
		{name: "address base", src: `void f(void){ asm("movl (%%rsi), %0" : "=r"(v)); }`,
			want:  []string{"(%%rsi)|%rsi is read here"},
			fixed: `void f(void){ asm("movl (%%rsi), %0" : "=r"(v) : : "rsi"); }`},
		{name: "high byte only", env: env{mode: 32}, src: `void f(void){ asm("movl %%esi, %%eax; movb %h0, %%al" : "=Q"(v) : : "eax"); }`},
		{name: "xmm", src: `void f(void){ asm("movdqa %%xmm1, %0" : "=x"(v)); }`,
			want:  []string{"%%xmm1|%xmm1 is read here"},
			fixed: `void f(void){ asm("movdqa %%xmm1, %0" : "=x"(v) : : "xmm1"); }`},
	})
}

func TestMissingVolatile(t *testing.T) {
	runCases(t, CodeMissingVolatile, []ruleCase{
		{name: "port read", src: `void f(void){ asm("inb %1, %0" : "=a"(v) : "Nd"(port)); }`,
			want:  []string{"inb|asm has outputs but is not volatile, and inb does port I/O"},
			fixed: `void f(void){ asm volatile("inb %1, %0" : "=a"(v) : "Nd"(port)); }`},
		{name: "rdtsc gnu spelling", src: `void f(void){ __asm__("rdtsc" : "=a"(lo), "=d"(hi)); }`,
			want:  []string{"rdtsc|returns a different value every time"},
			fixed: `void f(void){ __asm__ __volatile__("rdtsc" : "=a"(lo), "=d"(hi)); }`},
		{name: "cpuid is pure", src: `void f(void){ asm("cpuid" : "=a"(a), "=b"(b), "=c"(c), "=d"(d) : "a"(0)); }`},
		{name: "no outputs", src: `void f(void){ asm("outb %0, %1" : : "a"(v), "Nd"(port)); }`},
		{name: "volatile", src: `void f(void){ asm volatile("inb %1, %0" : "=a"(v) : "Nd"(port)); }`},
		{name: "store", src: `void f(void){ asm("movl %1, (%2); movl $0, %0" : "=r"(d) : "r"(v), "r"(p) : "memory"); }`,
			want:  []string{"movl|movl stores to memory that is not an output"},
			fixed: `void f(void){ asm volatile("movl %1, (%2); movl $0, %0" : "=r"(d) : "r"(v), "r"(p) : "memory"); }`},
		{name: "control register", src: `void f(void){ __asm("mov %%cr0, %0; mov %0, %%cr0" : "=r"(x)); }`,
			want:  []string{"mov|writes a control register"},
			fixed: `void f(void){ __asm __volatile__("mov %%cr0, %0; mov %0, %%cr0" : "=r"(x)); }`},
		{name: "segment register", src: `void f(void){ asm("movw %%ax, %%ds; movl $0, %0" : "=r"(x)); }`,
			want: []string{"movw|loads a segment register"}, fixed: `void f(void){ asm volatile("movw %%ax, %%ds; movl $0, %0" : "=r"(x)); }`},
		{name: "debug register", src: `void f(void){ asm("mov %0, %%dr7" : "=r"(x)); }`,
			want: []string{"mov|writes a debug register"}, fixed: `void f(void){ asm volatile("mov %0, %%dr7" : "=r"(x)); }`},
		{name: "basic", src: `void f(void){ asm("cli"); }`},
	})
}

// TestAirieBootFixtures pins the inline asm of airies-boot (falcon-core,
// falcon-bios, falcon-efi, falcon-hook) as it stood when these rules were
// written: correct code, and no rule may report any of it.
func TestAiriesBootFixtures(t *testing.T) {
	cases := []struct {
		path string
		env  env
	}{
		{"../../inlineasm/testdata/crypto.c", env{mode: 64, cc: "ms"}},
		{"../../inlineasm/testdata/xstate.c", env{mode: 64, cc: "ms"}},
		{"../../inlineasm/testdata/argon2_sse.h", env{mode: 64, cc: "ms"}},
		{"testdata/bios32.c", env{mode: 32, cc: "cdecl"}},
		{"testdata/efi64.c", env{mode: 64, cc: "ms"}},
		{"testdata/efi64.c", env{mode: 64, cc: "sysv"}},
	}
	for _, tc := range cases {
		b, err := os.ReadFile(filepath.FromSlash(tc.path))
		if err != nil {
			t.Fatal(err)
		}
		if got := lintC(t, string(b), tc.env); len(got) != 0 {
			t.Errorf("%s (%d-bit): %v", tc.path, tc.env.mode, got)
		}
	}
	// The #else branch of xstate.c's xstate_return is the 32-bit build's:
	// linted as 32-bit code, its vzeroall without xmm clobbers is exactly
	// what the rule describes (falcon-bios gives the compiler no SSE
	// registers, which the rule cannot know), and the 64-bit branch is
	// skipped instead.
	b, _ := os.ReadFile(filepath.FromSlash("../../inlineasm/testdata/xstate.c"))
	got := lintC(t, string(b), env{mode: 32}, CodeMissingClobber)
	if len(got) != 8 || !strings.Contains(got[0].Message, "vzeroall implicitly writes %xmm0") {
		t.Fatalf("32-bit xstate.c: %v", got)
	}
}

func TestInactiveRanges(t *testing.T) {
	src := "a\n#if defined(__x86_64__)\nb64\n#elif defined __i386__\nb32\n#else\nbother\n#endif\n" +
		"#ifdef FOO\nfoo\n#else\nnotfoo\n#endif\n#ifndef __x86_64__\nn64\n#endif\n" +
		"#if 0 /* off */\nzero\n#elif 1\none\n#endif\n#if (!defined(__amd64__) || X > 1)\nmaybe\n#endif\n" +
		"#if defined(__i386__) && \\\n  defined(BAR)\ncont\n#endif\n/*\n#if 0\n*/\nincomment\n#elifdef __x86_64__\n#endif\n// tail\n"
	inactive := func(mode int, word string) bool {
		return inRanges(inactiveRanges(src, mode), strings.Index(src, "\n"+word+"\n")+1)
	}
	for _, c := range []struct {
		word       string
		in64, in32 bool
	}{
		{"a", false, false}, {"b64", false, true}, {"b32", true, false}, {"bother", true, true},
		{"foo", false, false}, {"notfoo", false, false}, {"n64", true, false},
		{"zero", true, true}, {"one", false, false}, {"maybe", false, false}, {"cont", true, false},
		{"incomment", false, false},
	} {
		if got := inactive(64, c.word); got != c.in64 {
			t.Errorf("%s in 64-bit: inactive=%v", c.word, got)
		}
		if got := inactive(32, c.word); got != c.in32 {
			t.Errorf("%s in 32-bit: inactive=%v", c.word, got)
		}
	}
	for _, e := range []string{"defined(", "1 +", "(1", "defined(1)", "0x1g", "!0", "__x86_64__"} {
		_ = evalCond(e, 64)
	}
	if evalCond("!0", 64) != triTrue || evalCond("__x86_64__", 64) != triTrue || evalCond("defined(", 64) != triUnknown {
		t.Fatal("evalCond basics")
	}
	if r := inactiveRanges("#else\n#endif\n#endif\n#if 0\nx", 64); len(r) != 1 || r[0][1] != len("#else\n#endif\n#endif\n#if 0\nx") {
		t.Fatalf("unbalanced: %v", r)
	}
}

func TestFamilies(t *testing.T) {
	for name, want := range map[string]string{
		"%al": "rax:8", "ah": "rax:8", "AX": "rax:16", "eax": "rax:32", "rax": "rax:64", "r8": "r8:64",
		"r9d": "r9:32", "r10w": "r10:16", "r11b": "r11:8", "r12l": "r12:8", "xmm3": "xmm3:128",
		"ymm15": "xmm15:256", "zmm31": "xmm31:512", "k7": "k7:64", "sil": "rsi:8", "spl": "rsp:8",
	} {
		f, w, ok := family(name)
		if !ok || f+":"+strconv.Itoa(w) != want {
			t.Errorf("family(%q) = %s:%d %v, want %s", name, f, w, ok, want)
		}
	}
	for _, bad := range []string{"cr0", "ds", "r7", "r16", "xmm32", "k8", "rip", "st", "", "r"} {
		if _, _, ok := family(bad); ok {
			t.Errorf("family(%q) accepted", bad)
		}
	}
	if displayName("rax", 32) != "eax" || displayName("r8", 64) != "r8" || displayName("xmm1", 32) != "xmm1" {
		t.Fatal("displayName")
	}
	if s := styleOf([]string{"memory", "%r8", "%eax"}, 64); !s.percent || s.clobberName("rbx") != "%rbx" {
		t.Fatalf("style = %+v", s)
	}
	if !existsIn("xmm7", 32) || existsIn("xmm8", 32) || existsIn("r8", 16) || !existsIn("rax", 16) {
		t.Fatal("existsIn")
	}
	if itoa(31) != "31" {
		t.Fatal("itoa")
	}
}

func TestConstraintParse(t *testing.T) {
	st := inlineasm.Extract(`void f(void){ asm("" : [o] "=&r"(a), "+Q"(b), "=@ccz"(c) : "[o]"(d), "1"(e), "Yz"(x), "Yk"(k), "#r,m"(y), "X"(z), "Wz"(w), "f"(q), "?*g"(g)); }`)[0]
	ops := analyzeOperands(&st)
	if !ops[0].early || !ops[0].out || !ops[1].rw || !ops[1].highQ || !ops[2].flag {
		t.Fatalf("outputs = %+v", ops[:3])
	}
	if ops[3].tiedTo != 0 || ops[4].tiedTo != 1 || !ops[4].highQ || !ops[5].fixed["xmm0"] || !ops[6].mask {
		t.Fatalf("inputs = %+v", ops[3:7])
	}
	if ops[7].regLike() || !ops[8].anyReg || !ops[9].anyReg || ops[10].regLike() || !ops[11].gpr {
		t.Fatalf("misc = %+v", ops[7:])
	}
	if ops[1].regOK("rsi", 64) || !ops[1].regOK("rcx", 64) || !ops[11].regOK("r9", 64) || ops[11].regOK("r9", 32) ||
		ops[11].regOK("rsp", 64) || !ops[5].regOK("xmm0", 64) || ops[5].regOK("xmm1", 64) {
		t.Fatal("regOK")
	}
	q := parseConstraint("q")
	if !q.regOK("rsi", 64) || q.regOK("rsi", 32) || !q.regOK("rdx", 32) {
		t.Fatal("q")
	}
	if p := parseConstraint("[unterminated"); p.regLike() {
		t.Fatal("unterminated name")
	}
	if tiedIndex(&st, "[nope]") != -1 || tiedIndex(&st, "[x") != -1 || tiedIndex(&st, "r") != -1 {
		t.Fatal("tiedIndex")
	}
	ci, cm, crm := parseConstraint("i"), parseConstraint("m"), parseConstraint("rm")
	if !ci.immOnly() || !cm.memOnly() || crm.memOnly() {
		t.Fatal("imm/mem only")
	}
}

func TestTypeInference(t *testing.T) {
	cases := []struct {
		expr, decls string
		mode        int
		cc          string
		want        int // 0: unknown
	}{
		{"x", "int x;", 64, "", 32},
		{"x", "const volatile unsigned char x;", 64, "", 8},
		{"x", "signed x;", 64, "", 32},
		{"x", "unsigned short int x;", 64, "", 16},
		{"x", "_Bool x;", 64, "", 8},
		{"x", "uintptr_t x;", 32, "", 32},
		{"x", "uintptr_t *x;", 64, "", 64},
		{"x", "float x;", 64, "", 0},
		{"x", "struct s x;", 64, "", 0},
		{"x", "int y; y = a * x;", 64, "", 0},
		{"x", "return x;", 64, "", 0},
		{"x", "f(a, x);", 64, "", 0},
		{"x", "", 64, "", 0},
		{"(x)", "uint16_t x;", 64, "", 16},
		{"(unsigned long long)x", "", 64, "", 64},
		{"(struct s *)x", "", 64, "", 0},
		{"(x) + 1", "", 64, "", 0},
		{"0", "", 64, "", 32},
		{"0x10ULL", "", 64, "", 64},
		{"0x80000000", "", 64, "", 0},
		{"0x80000000u", "", 64, "", 32},
		{"10l", "", 64, "", 0},
		{"1e3", "", 64, "", 0},
		{"x", "char *x, y;", 64, "", 64},
		{"y", "char *x, y;", 64, "", 8},
		{"y", "for (i = 0, y; ;);", 64, "", 0},
		{"x", "static const char *x = \"uint8_t x;\";", 64, "", 64},
		{"x", "#define T uint8_t x;\nuint32_t x;", 64, "", 32},
		{"x", "bool x;", 64, "", 8},
		{"x", "unsigned uint8_t x;", 64, "", 8},
		{"x", "long int x;", 32, "", 32},
		{"x", "long x;", 64, "ms", 32},
		{"x", "DWORD x;", 64, "", 32},
		{"x", "char c; int x;", 64, "", 32},
		{"x", "", 64, "", 0},
	}
	for _, c := range cases {
		src := "void f(void)\n{\n" + c.decls + "\n"
		got, ok := exprType(src, len(src), c.expr, c.mode, c.cc)
		w := 0
		if ok {
			w = got.width
		}
		if w != c.want {
			t.Errorf("%q with %q (%d-bit %s) = %d (%+v), want %d", c.expr, c.decls, c.mode, c.cc, w, got, c.want)
		}
	}
	// Outside any function the search starts at the top of the file.
	if got, ok := exprType("uint8_t g;\n", 11, "g", 64, ""); !ok || got.width != 8 {
		t.Fatalf("file scope = %+v %v", got, ok)
	}
	if functionStart("void f(int a) {") != 6 || functionStart("x = { 1 };") != 0 || functionStart("{ {") != 0 {
		t.Fatal("functionStart")
	}
}

func TestFixEdges(t *testing.T) {
	src := `asm("x")`
	st := inlineasm.Extract(src)[0]
	if _, ok := clobberEdit(src, &st, []string{"rax"}); ok {
		t.Fatal("basic asm got a clobber fix")
	}
	if _, ok := clobberEdit(src, &st, nil); ok {
		t.Fatal("no names")
	}
	src = `void f(void){ asm("x" : "=r"(a)`
	st = inlineasm.Extract(src)[0]
	if _, ok := clobberEdit(src, &st, []string{"rax"}); ok {
		t.Fatal("unterminated statement got a fix")
	}
	if _, ok := earlyClobberEdit(src, &inlineasm.Operand{ConstraintSpan: inlineasm.Span{Start: 0, End: 1}}); ok {
		t.Fatal("tiny span")
	}
	if _, ok := earlyClobberEdit(`"r"`, &inlineasm.Operand{ConstraintSpan: inlineasm.Span{Start: 0, End: 3}}); ok {
		t.Fatal("no '='")
	}
	if _, ok := volatileEdit("asmx", &inlineasm.Stmt{Start: 0}); ok {
		t.Fatal("not a keyword")
	}
	if _, ok := volatileEdit("", &inlineasm.Stmt{Start: 3}); ok {
		t.Fatal("out of range")
	}
}

// FuzzClobberEdit checks the clobber fixer on arbitrary C: it never
// panics, its edit stays inside the statement, and the edited statement
// parses with the new clobber in its list.
func FuzzClobberEdit(f *testing.F) {
	for _, s := range []string{
		`asm volatile("cpuid" : "=a"(a) : "a"(0));`,
		`asm("x" : "=a"(a));`,
		`asm("x" ::: "memory");`,
		`asm("x" : : : );`,
		"asm(\"x\" : \"=a\"(a) // c\n);",
		`asm goto("jmp %l0" : : : : out);`,
		`asm("x" : : "r"(a) /* c */ );`,
		`__asm__ __volatile__("x" : "=r"(a) : "r"(b) : "cc", "memory");`,
		// Found by fuzzing: an unterminated literal in an operand list, and
		// a second statement sharing its first byte with the end of another.
		"asm(:\"\n)0",
		"{asm()asm(:)",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		for _, st := range inlineasm.Extract(src) {
			e, ok := clobberEdit(src, &st, []string{"rbx"})
			if !ok {
				continue
			}
			if e.start < st.Start || e.end > st.End || e.start > e.end {
				t.Fatalf("edit %+v outside statement [%d,%d) of %q", e, st.Start, st.End, src)
			}
			out := src[:e.start] + e.text + src[e.end:]
			var again *inlineasm.Stmt
			for _, s2 := range inlineasm.Extract(out) {
				if s2.Start == st.Start {
					again = &s2
				}
			}
			if again == nil {
				t.Fatalf("statement lost after edit: %q", out)
				return
			}
			found := false
			for _, c := range again.Clobbers {
				found = found || c == "rbx"
			}
			if !found {
				t.Fatalf("clobber missing after edit: %q -> %q (%v)", src, out, again.Clobbers)
			}
		}
	})
}

// TestReportsMapToC checks findings land on the C text of the template.
func TestReportsMapToC(t *testing.T) {
	src := "void f(void){\n  asm volatile(\"nop\\n\\t\"\n               \"cpuid\" : \"=a\"(a) : : \"rbx\", \"rcx\");\n}"
	got := lintC(t, src, x64, CodeMissingClobber)
	if len(got) != 1 || spanText(src, got[0]) != "cpuid" || got[0].Range.Start.Line != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0].Range != (protocol.Range{Start: protocol.Position{Line: 2, Character: 16}, End: protocol.Position{Line: 2, Character: 21}}) {
		t.Fatalf("range %+v", got[0].Range)
	}
}

func TestRegistered(t *testing.T) {
	for _, r := range ruleSet(t) {
		if r.Section != 9 || r.Langs != lint.LangC || r.Doc.Bad == "" || r.Doc.Good == "" || r.Doc.Why == "" {
			t.Errorf("rule %s: %+v", r.Code, r)
		}
		// Every Bad example triggers its own rule and its Good one does
		// not, so the documentation cannot drift from the behaviour.
		bad := lintC(t, "void f(void){\n"+r.Doc.Bad+"\n}", x64, r.Code)
		good := lintC(t, "void f(void){\n"+r.Doc.Good+"\n}", x64, r.Code)
		if len(bad) == 0 || len(good) != 0 {
			t.Errorf("rule %s: bad example %v, good example %v", r.Code, bad, good)
		}
	}
}

// sortedFams sorts a register set, to compare it.
func sortedFams(s regSet) []string {
	out := make([]string, 0, len(s))
	for f := range s {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
