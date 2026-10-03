package inlineasm

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// checkMap enforces the Map contract on every statement: defined over
// 0..len(Asm), never decreasing, inside the source, and pointing at a byte
// that plausibly produced the template byte (the same byte, the backslash
// of an escape, or the '%' of a %-escape).
func checkMap(t *testing.T, src string, stmts []Stmt) {
	t.Helper()
	for k, st := range stmts {
		if len(st.offs) != len(st.Asm)+1 {
			t.Fatalf("stmt %d: %d offsets for %d template bytes", k, len(st.offs), len(st.Asm))
		}
		prev := -1
		for i := 0; i <= len(st.Asm); i++ {
			c := st.Map(i)
			if c < 0 || c > len(src) {
				t.Fatalf("stmt %d: Map(%d) = %d out of [0,%d]", k, i, c, len(src))
			}
			if c < prev {
				t.Fatalf("stmt %d: Map(%d) = %d < Map(%d) = %d", k, i, c, i-1, prev)
			}
			prev = c
			if i < len(st.Asm) && c < len(src) && src[c] != st.Asm[i] && src[c] != '\\' && src[c] != '%' {
				t.Fatalf("stmt %d: Asm[%d] = %q maps to %q", k, i, st.Asm[i], src[c])
			}
		}
		if st.Map(-5) != st.Map(0) || st.Map(len(st.Asm)+9) != st.Map(len(st.Asm)) {
			t.Fatalf("stmt %d: Map does not clamp", k)
		}
		if st.Start < 0 || st.End > len(src) || st.Start > st.End {
			t.Fatalf("stmt %d: bad span %d..%d", k, st.Start, st.End)
		}
		for _, r := range st.refs {
			if r.Offset < 0 || r.Offset+r.Len > len(st.Asm) || st.Asm[r.Offset] != '%' {
				t.Fatalf("stmt %d: bad ref %+v in %q", k, r, st.Asm)
			}
		}
	}
}

func extract(t *testing.T, src string) []Stmt {
	t.Helper()
	s := Extract(src)
	checkMap(t, src, s)
	return s
}

func one(t *testing.T, src string) Stmt {
	t.Helper()
	s := extract(t, src)
	if len(s) != 1 {
		t.Fatalf("got %d statements, want 1 in %q", len(s), src)
	}
	return s[0]
}

func constraints(ops []Operand) []string {
	var out []string
	for _, o := range ops {
		out = append(out, o.Name+"|"+o.Constraint+"|"+o.Expr)
	}
	return out
}

func TestFixtures(t *testing.T) {
	type want struct {
		asm      string
		outs     []string
		ins      []string
		clobbers []string
	}
	cases := []struct {
		file string
		want []want
	}{
		{"crypto.c", []want{
			{asm: "xgetbv", outs: []string{"|=a|lo", "|=d|hi"}, ins: []string{"|c|0u"}},
		}},
		{"xstate.c", []want{
			{asm: "mov %cr4, %0", outs: []string{"|=r|v"}},
			{asm: "mov %0, %cr4", ins: []string{"|r|v"}, clobbers: []string{"memory"}},
			{asm: "xgetbv", outs: []string{"|=a|lo", "|=d|hi"}, ins: []string{"|c|0u"}},
			{asm: "xsetbv", ins: []string{"|a|(uint32_t)v", "|d|(uint32_t)(v >> 32)", "|c|0u"}, clobbers: []string{"memory"}},
			{asm: "vzeroall", clobbers: append([]string{"memory"}, xmmList(0, 15)...)},
			{asm: "vzeroall", clobbers: []string{"memory"}},
		}},
		{"argon2_sse.h", []want{
			{asm: pxorList(0, 7), clobbers: xmmList(0, 7)},
			{asm: pxorList(8, 15), clobbers: xmmList(8, 15)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			got := extract(t, src)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements, want %d", len(got), len(tc.want))
			}
			for k, w := range tc.want {
				st := got[k]
				if st.Asm != w.asm {
					t.Errorf("stmt %d Asm = %q, want %q", k, st.Asm, w.asm)
				}
				if g := constraints(st.Outputs); !reflect.DeepEqual(g, w.outs) {
					t.Errorf("stmt %d outputs = %q, want %q", k, g, w.outs)
				}
				if g := constraints(st.Inputs); !reflect.DeepEqual(g, w.ins) {
					t.Errorf("stmt %d inputs = %q, want %q", k, g, w.ins)
				}
				if !reflect.DeepEqual(st.Clobbers, w.clobbers) {
					t.Errorf("stmt %d clobbers = %q, want %q", k, st.Clobbers, w.clobbers)
				}
				if st.Basic || st.Partial || st.Unterminated || !st.Volatile || st.TopLevel {
					t.Errorf("stmt %d flags: %+v", k, st)
				}
				if !strings.HasPrefix(src[st.Start:], "__asm__") || src[st.End-1] != ')' {
					t.Errorf("stmt %d span %q", k, src[st.Start:st.End])
				}
				for i, sp := range st.ClobberSpans {
					if src[sp.Start:sp.End] != `"`+st.Clobbers[i]+`"` {
						t.Errorf("clobber span %q", src[sp.Start:sp.End])
					}
				}
			}
		})
	}
}

func xmmList(a, b int) []string {
	var out []string
	for i := a; i <= b; i++ {
		out = append(out, "xmm"+itoa(i))
	}
	return out
}

func pxorList(a, b int) string {
	var parts []string
	for i := a; i <= b; i++ {
		parts = append(parts, "pxor %xmm"+itoa(i)+", %xmm"+itoa(i))
	}
	return strings.Join(parts, "\n\t")
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// TestMapPointsAtSource checks the mapping on the multi-line fixture by
// value: the second literal's first byte must map to its own source column.
func TestMapPointsAtSource(t *testing.T) {
	b, err := os.ReadFile("testdata/argon2_sse.h")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	st := extract(t, src)[0]
	i := strings.Index(st.Asm, "pxor %xmm2")
	c := st.Map(i)
	if !strings.HasPrefix(src[c:], "pxor %%xmm2") {
		t.Fatalf("Map(%d) -> %q", i, src[c:c+12])
	}
	// "%xmm2": the '%' maps to the first of "%%", "x" to its own byte.
	if src[st.Map(i+5)] != '%' || src[st.Map(i+6)] != 'x' || st.Map(i+6) != st.Map(i+5)+2 {
		t.Fatalf("%%%% collapse mapping wrong")
	}
	if a, ok := st.AsmOffset(c + 7); !ok || a != i+6 {
		t.Fatalf("AsmOffset = %d,%v want %d", a, ok, i+6)
	}
	if a, ok := st.AsmOffset(c + 6); !ok || a != i+5 {
		t.Fatalf("AsmOffset on second %% = %d,%v want %d", a, ok, i+5)
	}
	nl := strings.IndexByte(st.Asm, '\n')
	if src[st.Map(nl)] != '\\' || src[st.Map(nl)+1] != 'n' {
		t.Fatalf("escape maps to %q", src[st.Map(nl):st.Map(nl)+2])
	}
	if _, ok := st.AsmOffset(st.Start); ok {
		t.Fatalf("AsmOffset outside template succeeded")
	}
	if st.Range.Start != (protocol.Position{Line: 7, Character: 4}) {
		t.Fatalf("Range.Start = %+v", st.Range.Start)
	}
	if st.Range.End.Line != 12 {
		t.Fatalf("Range.End = %+v", st.Range.End)
	}
}

func TestSynthetic(t *testing.T) {
	cases := []struct {
		name, src string
		n         int // number of statements
		asm       string
		check     func(t *testing.T, s Stmt)
	}{
		{name: "basic", src: `void f(void){ asm("nop"); }`, n: 1, asm: "nop",
			check: func(t *testing.T, s Stmt) {
				if !s.Basic || s.TopLevel || len(OperandRefs(s)) != 0 {
					t.Errorf("%+v", s)
				}
			}},
		{name: "basic keeps percent", src: `void f(void){ __asm("movl %%eax, %ebx; {a|b}"); }`, n: 1, asm: "movl %%eax, %ebx; {a|b}"},
		{name: "toplevel", src: "asm(\".globl foo\\n\"\n    \"foo: ret\");\nint x;", n: 1, asm: ".globl foo\nfoo: ret",
			check: func(t *testing.T, s Stmt) {
				if !s.TopLevel || !s.Basic {
					t.Errorf("flags %+v", s)
				}
			}},
		{name: "toplevel after decl", src: "int x;\n__asm__(\"nop\");", n: 1, asm: "nop"},
		{name: "extended empty lists", src: `void f(void){ asm volatile("rep; movsb %%al" ::); }`, n: 1, asm: "rep; movsb %al",
			check: func(t *testing.T, s Stmt) {
				if s.Basic || len(s.Outputs)+len(s.Inputs)+len(s.Clobbers) != 0 {
					t.Errorf("%+v", s)
				}
			}},
		{name: "qualifiers", src: `void f(void){ __asm__ __volatile__ __inline__ goto("jmp %l0" :::: out); out: ; }`, n: 1, asm: "jmp %l0",
			check: func(t *testing.T, s Stmt) {
				if !s.Volatile || !s.Inline || !s.Goto || len(s.Labels) != 1 || s.Labels[0].Name != "out" || s.Labels[0].Index != 0 {
					t.Errorf("%+v", s)
				}
				r := OperandRefs(s)
				if len(r) != 1 || r[0].Modifier != 'l' || r[0].Index != 0 || r[0].Len != 3 {
					t.Errorf("refs %+v", r)
				}
				if h := s.RefHover(r[0]); !strings.Contains(h, "goto label out") || !strings.Contains(h, "modifier l") {
					t.Errorf("hover %q", h)
				}
			}},
		{name: "asm goto named label", src: `int f(int x){ asm goto("btl %1, %0\n\tjc %l[yes]" : : "r"(x), "r"(3) : "cc" : yes, no); return 0; yes: return 1; no: return 2; }`,
			n: 1, asm: "btl %1, %0\n\tjc %l[yes]",
			check: func(t *testing.T, s Stmt) {
				r := OperandRefs(s)
				if len(r) != 3 || r[2].Name != "yes" || r[2].Index != 2 || r[2].Modifier != 'l' {
					t.Errorf("refs %+v", r)
				}
				if len(s.Labels) != 2 || s.Labels[1].Index != 3 {
					t.Errorf("labels %+v", s.Labels)
				}
			}},
		{name: "named operands", src: `void f(int *p){ int r; asm("movl %[src], %k[dst]" : [dst] "=r" (r) : [src] "m" (*p)); }`,
			n: 1, asm: "movl %[src], %k[dst]",
			check: func(t *testing.T, s Stmt) {
				r := OperandRefs(s)
				if len(r) != 2 || r[0].Name != "src" || r[0].Index != 1 || r[1].Name != "dst" || r[1].Index != 0 || r[1].Modifier != 'k' || r[1].Len != 7 {
					t.Errorf("refs %+v", r)
				}
				if s.Outputs[0].Name != "dst" || s.Inputs[0].Expr != "*p" {
					t.Errorf("ops %+v %+v", s.Outputs, s.Inputs)
				}
			}},
		{name: "unknown name", src: `void f(void){ asm("%[nope]" : : "r"(1)); }`, n: 1, asm: "%[nope]",
			check: func(t *testing.T, s Stmt) {
				r := OperandRefs(s)
				if len(r) != 1 || r[0].Index != -1 {
					t.Errorf("refs %+v", r)
				}
				if h := s.RefHover(r[0]); !strings.Contains(h, "no operand named") {
					t.Errorf("hover %q", h)
				}
			}},
		{name: "mask register not a ref", src: `void f(void){ asm("kmovw %%k0, %0" : "=r"(x)); }`, n: 1, asm: "kmovw %k0, %0",
			check: func(t *testing.T, s Stmt) {
				r := OperandRefs(s)
				if len(r) != 1 || r[0].Offset != 11 {
					t.Errorf("refs %+v", r)
				}
			}},
		{name: "macro in template", src: `#define REG "%%eax"
void f(void){ asm("mov " REG ", %0" "x" : "=r"(x) : : "memory"); }`, n: 1, asm: "mov ",
			check: func(t *testing.T, s Stmt) {
				if !s.Partial || len(s.Outputs) != 1 || s.Clobbers[0] != "memory" || len(s.Template) != 1 {
					t.Errorf("%+v", s)
				}
			}},
		{name: "macro call in template", src: `void f(void){ asm volatile(STR(nop) : : ); }`, n: 1, asm: "",
			check: func(t *testing.T, s Stmt) {
				if !s.Partial || s.Basic {
					t.Errorf("%+v", s)
				}
			}},
		{name: "wide literal is partial", src: `void f(void){ asm(L"nop"); }`, n: 1, asm: "",
			check: func(t *testing.T, s Stmt) {
				if !s.Partial {
					t.Errorf("not partial")
				}
			}},
		{name: "concat with comments", src: "void f(void){ asm(\"a\" /* c1 */ \"b\" // c2 \"zz\"\n  \"c\"\n#if 0\n#endif\n \"d\"); }", n: 1, asm: "abcd"},
		{name: "escapes", src: `void f(void){ asm("\"q\\\101\x41\x4142\t\a\b\f\v\r\e\?\'\z\u00e9\U0001F600\x\uZZ\0"); }`, n: 1,
			asm: "\"q\\AAB\t\a\b\f\v\r\x1b?'z\u00e9\U0001F600xu" + "ZZ\x00"},
		{name: "line splice in literal", src: "void f(void){ asm(\"ab\\\ncd\\\r\nef\"); }", n: 1, asm: "abcdef"},
		{name: "nested parens", src: `void f(void){ asm("" : "=r"(a[(i+1)*(j)]) : "r"(g(h(1), (2)))); }`, n: 1, asm: "",
			check: func(t *testing.T, s Stmt) {
				if s.Outputs[0].Expr != "a[(i+1)*(j)]" || s.Inputs[0].Expr != "g(h(1), (2))" {
					t.Errorf("%q", constraints(append(s.Outputs, s.Inputs...)))
				}
			}},
		{name: "strings in expr", src: `void f(void){ asm("" : : "r"(strlen(")),:")), "i"(':')); }`, n: 1, asm: "",
			check: func(t *testing.T, s Stmt) {
				if len(s.Inputs) != 2 || s.Inputs[0].Expr != `strlen(")),:")` || s.Inputs[1].Expr != "':'" || len(s.Clobbers) != 0 {
					t.Errorf("%q", constraints(s.Inputs))
				}
			}},
		{name: "concat constraint", src: `void f(void){ asm("" : "=" "r"(x)); }`, n: 1, asm: "",
			check: func(t *testing.T, s Stmt) {
				if s.Outputs[0].Constraint != "=r" || s.Partial {
					t.Errorf("%+v", s.Outputs[0])
				}
			}},
		{name: "macro constraint and clobber", src: `void f(void){ asm("" : C(x) : : CLOB, "cc" X); }`, n: 1, asm: "",
			check: func(t *testing.T, s Stmt) {
				if !s.Partial || len(s.Clobbers) != 1 || s.Clobbers[0] != "cc" {
					t.Errorf("%+v", s)
				}
			}},
		{name: "dialects", src: `void f(void){ asm("{movl|mov} %0, %1 %{%|%} {x%}|y%}}" : "=r"(a) : "r"(b)); }`, n: 1, asm: "movl %0, %1 {|} x}",
			check: func(t *testing.T, s Stmt) {
				if len(OperandRefs(s)) != 2 {
					t.Errorf("refs")
				}
			}},
		{name: "percent forms", src: `void f(void){ asm("%= %* %% %5x %" : ); }`, n: 1, asm: "%= %* % %5x %",
			check: func(t *testing.T, s Stmt) {
				r := OperandRefs(s)
				if len(r) != 1 || r[0].Index != 5 {
					t.Errorf("refs %+v", r)
				}
				if h := s.RefHover(r[0]); !strings.Contains(h, "does not exist") {
					t.Errorf("hover %q", h)
				}
			}},
		{name: "two statements", src: `void f(void){ asm("a"); if (x) asm("b"); else asm("c"); do asm("d"); while(0); }`, n: 4, asm: "a"},
		{name: "in comment", src: "/* asm(\"x\"); */ // asm(\"y\");\nint z;", n: 0},
		{name: "line comment continued", src: "// note \\\nasm(\"x\");\nint z;", n: 0},
		{name: "in string", src: `char *s = "asm(\"x\")"; char c = 'a'; char d = '"';`, n: 0},
		{name: "in define", src: "#define BARRIER() asm volatile(\"\" ::: \\\n \"memory\")\n#define X /* asm(\"a\")\n asm(\"b\") */ 1\nint q;", n: 0},
		{name: "include", src: "#include \"asm.h\"\n#error don't\nint x;", n: 0},
		{name: "member", src: `void f(void){ x.asm("a"); p->asm("b"); y = s.__asm__; }`, n: 0},
		{name: "plain identifier", src: `struct s { int asm; }; int asm = 3; void f(void){ asm = asm + 1; }`, n: 0},
		{name: "asm label", src: "extern int foo(int) __asm__(\"bar\");\nregister int r asm(\"eax\");\nint a[2] asm(\"b\");\nvoid g(void){ register long x __asm__(\"rax\"); }", n: 0},
		{name: "identifier with asm suffix", src: `void f(void){ my_asm("x"); asm_x("y"); }`, n: 0},
		{name: "unterminated statement", src: `void f(void){ asm volatile("mov %0, %%eax" : "=r"(x)`, n: 1, asm: "mov %0, %eax",
			check: func(t *testing.T, s Stmt) {
				if !s.Unterminated || len(s.Outputs) != 1 {
					t.Errorf("%+v", s)
				}
			}},
		{name: "unterminated literal", src: "void f(void){ asm(\"mov %eax\n int x; asm(\"nop\"); }", n: 2, asm: "mov %eax",
			check: func(t *testing.T, s Stmt) {
				if !s.Unterminated {
					t.Errorf("%+v", s)
				}
			}},
		{name: "unterminated at semicolon", src: `void f(void){ asm("nop" ; asm("hlt"); }`, n: 2, asm: "nop"},
		{name: "operand list cut by semicolon", src: `void f(void){ asm("nop" : "=r"(x ; asm("hlt"); }`, n: 2, asm: "nop"},
		{name: "operand list cut by brace", src: `void f(void){ asm("nop" : "=r"(x) }`, n: 1, asm: "nop",
			check: func(t *testing.T, s Stmt) {
				if !s.Unterminated {
					t.Errorf("%+v", s)
				}
			}},
		{name: "no paren", src: `void f(void){ asm volatile; }`, n: 0},
		{name: "raw string", src: `void f(){ asm(R"x(mov %eax, "%ebx")x"); }`, n: 1, asm: `mov %eax, "%ebx"`},
		{name: "raw string bad delim", src: `void f(){ asm(R"x y(nop)x y"); }`, n: 1},
		{name: "u8 literal", src: `void f(){ asm(u8"nop"); }`, n: 1, asm: "nop"},
		{name: "char literals around", src: `void f(){ char a = '\'', b = L'x'; asm("nop"); }`, n: 1, asm: "nop"},
		{name: "numbers", src: `void f(){ x = 1e+5 + 0x1p-3 + .5 + 1'000; asm("nop"); }`, n: 1, asm: "nop"},
		{name: "splice outside literal", src: "void f(){ x = 1; \\\n asm(\"nop\"); }", n: 1, asm: "nop"},
		{name: "crlf and utf16", src: "/* \u00e9\U0001F600 */ void f(){\r\n\u00e9; asm(\"nop\"); }", n: 1, asm: "nop",
			check: func(t *testing.T, s Stmt) {
				if s.Range.Start != (protocol.Position{Line: 1, Character: 3}) {
					t.Errorf("range %+v", s.Range)
				}
			}},
		{name: "operand without paren", src: `void f(){ asm("" : "=r" : "r"(1)); }`, n: 1,
			check: func(t *testing.T, s Stmt) {
				if s.Outputs[0].Expr != "" || s.Inputs[0].Expr != "1" {
					t.Errorf("%+v", s)
				}
			}},
		{name: "operand junk before paren", src: `void f(){ asm("" : "=r" junk (x)); }`, n: 1,
			check: func(t *testing.T, s Stmt) {
				if !s.Partial || s.Outputs[0].Expr != "x" {
					t.Errorf("%+v", s)
				}
			}},
		{name: "unclosed expr", src: `void f(){ asm("" : "=r"(x`, n: 1,
			check: func(t *testing.T, s Stmt) {
				if s.Outputs[0].Expr != "x" || !s.Unterminated {
					t.Errorf("%+v", s.Outputs)
				}
			}},
		{name: "label junk", src: `void f(){ asm goto("" :::: 1, done); }`, n: 1,
			check: func(t *testing.T, s Stmt) {
				if len(s.Labels) != 1 {
					t.Errorf("%+v", s.Labels)
				}
			}},
		{name: "stray close brace", src: `} asm("nop");`, n: 1, asm: "nop"},
		{name: "keyword at file start", src: `asm("nop");`, n: 1, asm: "nop"},
		{name: "after number", src: `int x = 1 asm("nop");`, n: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, tc.src)
			if len(got) != tc.n {
				t.Fatalf("got %d statements, want %d: %+v", len(got), tc.n, got)
			}
			if tc.n == 0 {
				return
			}
			if tc.asm != "" && got[0].Asm != tc.asm {
				t.Errorf("Asm = %q, want %q", got[0].Asm, tc.asm)
			}
			if tc.check != nil {
				tc.check(t, got[0])
			}
		})
	}
}

func TestStatementOrder(t *testing.T) {
	src := `void f(void){ asm("a"); if (x) asm("b"); else asm("c"); do asm("d"); while(0); }`
	var asms []string
	for _, s := range extract(t, src) {
		asms = append(asms, s.Asm)
	}
	if !reflect.DeepEqual(asms, []string{"a", "b", "c", "d"}) {
		t.Fatalf("%q", asms)
	}
}

func TestFindAndOperand(t *testing.T) {
	src := `void f(void){ asm("a"); asm("b %0 %1" : "=r"(x) : "r"(y)); }`
	stmts := extract(t, src)
	off := strings.Index(src, `"b`)
	s := Find(stmts, off)
	if s == nil || s.Asm != "b %0 %1" {
		t.Fatalf("Find = %+v", s)
	}
	if Find(stmts, 0) != nil {
		t.Fatalf("Find outside")
	}
	if op, ok := s.Operand(1); !ok || op.Expr != "y" {
		t.Fatalf("Operand(1) = %+v", op)
	}
	if _, ok := s.Operand(2); ok {
		t.Fatal("Operand(2)")
	}
	if _, ok := s.Operand(-1); ok {
		t.Fatal("Operand(-1)")
	}
}

func TestHoverAt(t *testing.T) {
	src := `int f(int v){ asm goto("mov %k[in], %%eax\n\tjz %l2" : [o] "=&r"(o) : [in] "r"(v) : "memory", "ecx" : out); out: return 0; }`
	s := one(t, src)
	at := func(needle string, delta int) (string, bool) {
		i := strings.Index(src, needle)
		if i < 0 {
			t.Fatalf("needle %q", needle)
		}
		return s.HoverAt(i + delta)
	}
	cases := []struct {
		needle string
		delta  int
		want   string // substring; "" means no hover
	}{
		{"%k[in]", 1, "operand 1 [in]"},
		{"%k[in]", 0, "modifier k: 32-bit"},
		{"%%eax", 1, ""},
		{"%l2", 2, "goto label out"},
		{`"=&r"`, 2, "early-clobber"},
		{"[o]", 1, "operand 0 [o]"},
		{`"memory"`, 3, "memory:"},
		{`"ecx"`, 1, "register %ecx is clobbered"},
		{": out", 3, "goto label out, referenced as %l2"},
		{"(v)", 1, ""},
	}
	for _, c := range cases {
		h, ok := at(c.needle, c.delta)
		if c.want == "" {
			if ok {
				t.Errorf("%s+%d: unexpected hover %q", c.needle, c.delta, h)
			}
			continue
		}
		if !ok || !strings.Contains(h, c.want) {
			t.Errorf("%s+%d: hover %q, want %q", c.needle, c.delta, h, c.want)
		}
	}
}

func TestRefHoverUnknownModifier(t *testing.T) {
	s := one(t, `void f(){ asm("%j0" : "=r"(x)); }`)
	r := OperandRefs(s)
	if len(r) != 1 || !strings.Contains(s.RefHover(r[0]), "unknown modifier") {
		t.Fatalf("%+v", r)
	}
}

func TestConstraintHover(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"=a"`, "output (write-only): register a (%al/%ax/%eax/%rax)"},
		{"+r", "output (read-write): any general register"},
		{"r", "input: any general register"},
		{"=&r", "output (write-only): any general register; early-clobber"},
		{"%r", "input: any general register; commutative with the next operand"},
		{"rm", "input: any general register or memory operand"},
		{"=r,m", "output (write-only): alternatives: any general register | memory operand"},
		{"0", "input: same location as operand 0"},
		{"12", "input: same location as operand 12"},
		{"[x]", "input: same location as operand [x]"},
		{"[x", "unterminated operand name"},
		{"=@ccz", "flag output: condition z, zero (ZF=1), result 0/1 as by setz"},
		{"=@ccbogus", "unknown condition"},
		{"Yz", "first SSE register %xmm0"},
		{"Yk", "mask register usable as a predicate"},
		{"Bf", "machine-specific constraint Bf"},
		{"?r", "(slightly disparaged)"},
		{"!m", "(severely disparaged)"},
		{"*r", "input: any general register"},
		{"r#m", "input: any general register"},
		{"#", "no constraint"},
		{"=W", "unknown constraint 'W'"},
		{"=w", "unknown constraint 'w'"},
		{"", ""},
		{"N", "in/out port"},
		{"=&&r", "early-clobber"},
	}
	for _, c := range cases {
		got := ConstraintHover(c.in)
		if c.want == "" {
			if got != "" {
				t.Errorf("%q: %q", c.in, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("ConstraintHover(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if strings.Count(ConstraintHover("=&&r"), "early") != 1 {
		t.Error("duplicate note")
	}
	for k := range constraintLetters {
		if ConstraintHover(k) == "" || strings.Contains(ConstraintHover(k), "unknown") {
			t.Errorf("letter %q", k)
		}
	}
}

func TestClobberHover(t *testing.T) {
	known := []string{"memory", "cc", "dirflag", "fpsr", "flags", "redzone",
		"eax", "%rax", "ax", "al", "sil", "r8", "r15", "r8d", "r15w", "r9b",
		"xmm0", "xmm31", "ymm7", "zmm31", "mm7", "k7", "st", "st(7)", "rbp"}
	for _, c := range known {
		h := ClobberHover(`"` + c + `"`)
		if h == "" || strings.Contains(h, "unknown") {
			t.Errorf("ClobberHover(%q) = %q", c, h)
		}
	}
	unknown := []string{"bogus", "xmm32", "mm8", "k8", "r7", "r16", "r7d", "st(8)", "xmm", "xmmab", "r100", "eip", "rd"}
	for _, c := range unknown {
		if h := ClobberHover(c); !strings.Contains(h, "unknown") {
			t.Errorf("ClobberHover(%q) = %q, want unknown", c, h)
		}
	}
	if ClobberHover("") != "" {
		t.Error("empty")
	}
}

func TestModifierHover(t *testing.T) {
	for _, m := range "abhkqwxlc" {
		if ModifierHover(byte(m)) == "" {
			t.Errorf("modifier %c", m)
		}
	}
	if ModifierHover('j') != "" {
		t.Error("j")
	}
}

func TestOperandRefsCopy(t *testing.T) {
	s := one(t, `void f(){ asm("%0" : "=r"(x)); }`)
	r := OperandRefs(s)
	r[0].Index = 99
	if OperandRefs(s)[0].Index != 0 {
		t.Fatal("OperandRefs aliases internal state")
	}
}

func TestScanRefEdges(t *testing.T) {
	cases := []struct {
		in string
		n  int
	}{
		{"%", 0}, {"%k", 0}, {"%[abc", 0}, {"%[a\n]", 0}, {"%[ a ]", 6},
		{"%99999999999999", 15}, {"%k[x]", 5}, {"%kx", 0},
	}
	for _, c := range cases {
		if _, n := scanRef([]byte(c.in)); n != c.n {
			t.Errorf("scanRef(%q) n = %d, want %d", c.in, n, c.n)
		}
	}
	if r, _ := scanRef([]byte("%[ a ]")); r.Name != "a" {
		t.Errorf("name %q", r.Name)
	}
}

func TestLexEdges(t *testing.T) {
	// A string ending in a backslash at end of input is unterminated.
	toks := lexC(`"abc\`)
	if len(toks) != 1 || !toks[0].unterminated {
		t.Fatalf("%+v", toks)
	}
	toks = lexC(`"abc\"`)
	if len(toks) != 1 || !toks[0].unterminated {
		t.Fatalf("%+v", toks)
	}
	toks = lexC(`R"x(abc`)
	if len(toks) != 1 || !toks[0].unterminated || !toks[0].raw {
		t.Fatalf("%+v", toks)
	}
	if got := Extract("/* never closed asm(\"x\")"); len(got) != 0 {
		t.Fatal("comment")
	}
	if got := Extract("#define A \\\r\n asm(\"x\")\r\nasm(\"y\");"); len(got) != 1 || got[0].Asm != "y" {
		t.Fatalf("%+v", got)
	}
	if got := Extract("#if 1 // c \\\n asm(\"x\")\nint z;"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if got := Extract("asm(\"x\" \\\n);"); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func FuzzExtract(f *testing.F) {
	seeds := []string{
		`asm("nop");`,
		`void f(){ asm volatile("mov %[a], %k0\n\t{x|y}" : "=r"(x) : [a] "rm"(y) : "cc" : l); }`,
		`asm goto("%l0" :::: a, b`,
		"asm(\"\\x41\\101\\u00e9\\\n\" R\"(z)\"",
		`asm("%%%{%|%}{|}{" : );`,
		"#define X asm(\"a\")\n/*",
		`asm("a" M "b" : "=" M (x) ::: `,
	}
	for _, name := range []string{"crypto.c", "xstate.c", "argon2_sse.h"} {
		if b, err := os.ReadFile("testdata/" + name); err == nil {
			seeds = append(seeds, string(b))
		}
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		stmts := Extract(src)
		checkMap(t, src, stmts)
		for k := range stmts {
			s := &stmts[k]
			for _, r := range OperandRefs(*s) {
				_ = s.RefHover(r)
			}
			for off := s.Start; off <= s.End; off++ {
				if a, ok := s.AsmOffset(off); ok && (a < 0 || a > len(s.Asm)) {
					t.Fatalf("AsmOffset(%d) = %d", off, a)
				}
				_, _ = s.HoverAt(off)
			}
			for _, o := range append(append([]Operand(nil), s.Outputs...), s.Inputs...) {
				_ = ConstraintHover(o.Constraint)
			}
			for _, c := range s.Clobbers {
				_ = ClobberHover(c)
			}
		}
	})
}
