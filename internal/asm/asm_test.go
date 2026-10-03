package asm

import (
	"reflect"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

func newDoc(src string) *docstore.Document {
	s := docstore.New()
	s.Open("file:///t.S", 1, src)
	d, _ := s.Get("file:///t.S")
	return d
}

func parseSrc(t testing.TB, src string, cfg Config) *Program {
	t.Helper()
	p := Parse(newDoc(src), lexer.Tokenize(src), cfg)
	checkProgram(t, p)
	return p
}

// firstInstr returns the first statement that is not a directive, label or
// cpp line.
func firstInstr(t *testing.T, p *Program) Stmt {
	t.Helper()
	for _, s := range p.Stmts {
		if s.Kind == Instr || s.Kind == MacroCall {
			return s
		}
	}
	t.Fatalf("no instruction in %q", p.Doc.Text)
	return Stmt{}
}

// bare drops the fields a table cannot spell conveniently; positions are
// checked by checkProgram, Reg by TestRegisterLookup.
func bare(op Operand) Operand {
	op.Start, op.End, op.Reg, op.sufBits = 0, 0, nil, 0
	return op
}

func known(v int64) Value { return Value{Known: true, V: v} }

func TestOperands(t *testing.T) {
	macros := map[string]string{"SEL": "0x08", "SCALE": "8", "PORT": "(0x3F8 + 5)"}
	tests := []struct {
		name string
		src  string
		want []Operand
	}{
		{"registers", "movl %eax, %EBX", []Operand{
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
			{Kind: OpReg, Text: "%EBX", RegName: "ebx"},
		}},
		{"st index", "fld %st(1)", []Operand{{Kind: OpReg, Text: "%st(1)", RegName: "st(1)"}}},
		{"st spaced", "fld %st (1)", []Operand{{Kind: OpReg, Text: "%st (1)", RegName: "st(1)"}}},
		{"base only", "movw (%bx), %ax", []Operand{
			{Kind: OpMem, Text: "(%bx)", Base: "bx", RegName: "bx"},
			{Kind: OpReg, Text: "%ax", RegName: "ax"},
		}},
		{"index scale no base", "movl 8(,%esi,4), %eax", []Operand{
			{Kind: OpMem, Text: "8(,%esi,4)", Disp: "8", DispVal: known(8), Index: "esi", Scale: 4},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"segment base", "movw %es:(%di), %ax", []Operand{
			{Kind: OpMem, Text: "%es:(%di)", Seg: "es", Base: "di", RegName: "di"},
			{Kind: OpReg, Text: "%ax", RegName: "ax"},
		}},
		{"base index no scale", "movw 0x10(%bp,%si), %ax", []Operand{
			{Kind: OpMem, Text: "0x10(%bp,%si)", Disp: "0x10", DispVal: known(16), Base: "bp", Index: "si", Scale: 1, RegName: "bp"},
			{Kind: OpReg, Text: "%ax", RegName: "ax"},
		}},
		{"index without scale", "movl (,%eax), %ebx", []Operand{
			{Kind: OpMem, Text: "(,%eax)", Index: "eax", Scale: 1},
			{Kind: OpReg, Text: "%ebx", RegName: "ebx"},
		}},
		{"base trailing comma", "movl (%eax,), %ebx", []Operand{
			{Kind: OpMem, Text: "(%eax,)", Base: "eax", RegName: "eax"},
			{Kind: OpReg, Text: "%ebx", RegName: "ebx"},
		}},
		{"macro scale", "movl (%eax,%ebx,SCALE), %ecx", []Operand{
			{Kind: OpMem, Text: "(%eax,%ebx,SCALE)", Base: "eax", Index: "ebx", Scale: 8, RegName: "eax"},
			{Kind: OpReg, Text: "%ecx", RegName: "ecx"},
		}},
		{"unknown scale", "movl (%eax,%ebx,x), %ecx", []Operand{
			{Kind: OpMem, Text: "(%eax,%ebx,x)", Base: "eax", Index: "ebx", RegName: "eax"},
			{Kind: OpReg, Text: "%ecx", RegName: "ecx"},
		}},
		{"bare symbol", "movl foo, %eax", []Operand{
			{Kind: OpMem, Text: "foo", Disp: "foo"},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"bare number", "movl 0x500, %eax", []Operand{
			{Kind: OpMem, Text: "0x500", Disp: "0x500", DispVal: known(0x500)},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"rip relative", "leaq foo+4(%rip), %rax", []Operand{
			{Kind: OpMem, Text: "foo+4(%rip)", Disp: "foo+4", Base: "rip", RIPRel: true, RegName: "rip"},
			{Kind: OpReg, Text: "%rax", RegName: "rax"},
		}},
		{"negative disp", "movl -4(%ebp), %eax", []Operand{
			{Kind: OpMem, Text: "-4(%ebp)", Disp: "-4", DispVal: known(-4), Base: "ebp", RegName: "ebp"},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"paren disp with segment", "movw $0x5678, %es:(0x0510)", []Operand{
			{Kind: OpImm, Text: "$0x5678", Imm: "0x5678", ImmVal: known(0x5678)},
			{Kind: OpMem, Text: "%es:(0x0510)", Seg: "es", Disp: "(0x0510)", DispVal: known(0x510)},
		}},
		{"segment disp", "movl %fs:0x28, %eax", []Operand{
			{Kind: OpMem, Text: "%fs:0x28", Seg: "fs", Disp: "0x28", DispVal: known(0x28)},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"macro disp", "movb %dh, PORT(%si)", []Operand{
			{Kind: OpReg, Text: "%dh", RegName: "dh"},
			{Kind: OpMem, Text: "PORT(%si)", Disp: "PORT", DispVal: known(0x3FD), Base: "si", RegName: "si"},
		}},
		{"immediates", "movl $0x10, %eax", []Operand{
			{Kind: OpImm, Text: "$0x10", Imm: "0x10", ImmVal: known(16)},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"char open", "movb $'a, %al", []Operand{
			{Kind: OpImm, Text: "$'a", Imm: "'a", ImmVal: known('a')},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"char space", "movb $' ', %al", []Operand{
			{Kind: OpImm, Text: "$' '", Imm: "' '", ImmVal: known(' ')},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"char escape", `movb $'\r', %al`, []Operand{
			{Kind: OpImm, Text: `$'\r'`, Imm: `'\r'`, ImmVal: known('\r')},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"char expr", "addb $('A' - '9' - 1), %al", []Operand{
			{Kind: OpImm, Text: "$('A' - '9' - 1)", Imm: "('A' - '9' - 1)", ImmVal: known('A' - '9' - 1)},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"open char expr", "addb $('a + 1), %al", []Operand{
			{Kind: OpImm, Text: "$('a + 1)", Imm: "('a + 1)", ImmVal: known('a' + 1)},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"bad char expr", `addb $('\q + 1), %al`, []Operand{
			{Kind: OpImm, Text: `$('\q + 1)`, Imm: `('\q + 1)`},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"negative imm", "andl $-16, %esp", []Operand{
			{Kind: OpImm, Text: "$-16", Imm: "-16", ImmVal: known(-16)},
			{Kind: OpReg, Text: "%esp", RegName: "esp"},
		}},
		{"expr imm", "andl $((1 << 24) | 2), %edx", []Operand{
			{Kind: OpImm, Text: "$((1 << 24) | 2)", Imm: "((1 << 24) | 2)", ImmVal: known(1<<24 | 2)},
			{Kind: OpReg, Text: "%edx", RegName: "edx"},
		}},
		{"binary octal", "movl $0b101, %eax; movl $017, %eax", []Operand{
			{Kind: OpImm, Text: "$0b101", Imm: "0b101", ImmVal: known(5)},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"macro imm", "pushl $SEL", []Operand{{Kind: OpImm, Text: "$SEL", Imm: "SEL", ImmVal: known(8)}}},
		{"macro expr imm", "outb %al, $PORT", []Operand{
			{Kind: OpReg, Text: "%al", RegName: "al"},
			{Kind: OpImm, Text: "$PORT", Imm: "PORT", ImmVal: known(0x3FD)},
		}},
		{"label imm", "movl $msg, %esi", []Operand{
			{Kind: OpImm, Text: "$msg", Imm: "msg"},
			{Kind: OpReg, Text: "%esi", RegName: "esi"},
		}},
		{"space constant", "movb $' , %al", []Operand{
			{Kind: OpImm, Text: "$' ", Imm: "' ", ImmVal: known(' ')},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"space constant expr", "movb $1+' , %al", []Operand{
			{Kind: OpImm, Text: "$1+' ", Imm: "1+' ", ImmVal: known(33)},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"spaced dollar", "pushl $ 5", []Operand{{Kind: OpImm, Text: "$ 5", Imm: "5", ImmVal: known(5)}}},
		{"local jump", "jmp 1f", []Operand{{Kind: OpLabel, Text: "1f", Symbol: "1f", Disp: "1f"}}},
		{"call", "call foo", []Operand{{Kind: OpLabel, Text: "foo", Symbol: "foo", Disp: "foo"}}},
		{"call plt", "call foo@PLT", []Operand{{Kind: OpLabel, Text: "foo@PLT", Symbol: "foo", Disp: "foo@PLT"}}},
		{"jump dot", "jmp .", []Operand{{Kind: OpLabel, Text: ".", Symbol: ".", Disp: "."}}},
		{"jump expr", "jmp foo+4", []Operand{{Kind: OpLabel, Text: "foo+4", Symbol: "foo+4", Disp: "foo+4"}}},
		{"jump absolute", "jmp 0x7c00", []Operand{{Kind: OpLabel, Text: "0x7c00", Symbol: "0x7c00", Disp: "0x7c00", DispVal: known(0x7c00)}}},
		{"loop", "addr32 loop 2b", []Operand{{Kind: OpLabel, Text: "2b", Symbol: "2b", Disp: "2b"}}},
		{"xbegin", "xbegin 1f", []Operand{{Kind: OpLabel, Text: "1f", Symbol: "1f", Disp: "1f"}}},
		{"indirect reg", "jmp *%eax", []Operand{{Kind: OpReg, Text: "*%eax", RegName: "eax", Indirect: true}}},
		{"indirect mem", "jmp *(%bx)", []Operand{{Kind: OpMem, Text: "*(%bx)", Base: "bx", RegName: "bx", Indirect: true}}},
		{"indirect symbol", "jmp *pmode_entry", []Operand{{Kind: OpMem, Text: "*pmode_entry", Disp: "pmode_entry", Indirect: true}}},
		{"indirect rip", "call *foo(%rip)", []Operand{{Kind: OpMem, Text: "*foo(%rip)", Disp: "foo", Base: "rip", RegName: "rip", RIPRel: true, Indirect: true}}},
		{"far immediate", "ljmp $SEL, $label", []Operand{
			{Kind: OpImm, Text: "$SEL", Imm: "SEL", ImmVal: known(8)},
			{Kind: OpImm, Text: "$label", Imm: "label"},
		}},
		{"far indirect paren disp", "ljmp *%cs:(RELOC+(old15-_start))", []Operand{
			{Kind: OpMem, Text: "*%cs:(RELOC+(old15-_start))", Seg: "cs", Disp: "(RELOC+(old15-_start))", Indirect: true},
		}},
		{"jump through base", "jmp (%eax)", []Operand{{Kind: OpMem, Text: "(%eax)", Base: "eax", RegName: "eax"}}},
		{"mask zeroing", "vaddps %zmm1, %zmm2, %zmm3{%k1}{z}", []Operand{
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1"},
			{Kind: OpReg, Text: "%zmm2", RegName: "zmm2"},
			{Kind: OpReg, Text: "%zmm3{%k1}{z}", RegName: "zmm3", Mask: "k1", Zeroing: true},
		}},
		{"broadcast", "vaddps (%rax){1to16}, %zmm1, %zmm2", []Operand{
			{Kind: OpMem, Text: "(%rax){1to16}", Base: "rax", RegName: "rax", Broadcast: "1to16"},
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1"},
			{Kind: OpReg, Text: "%zmm2", RegName: "zmm2"},
		}},
		{"rounding first", "vaddps {rn-sae}, %zmm1, %zmm2, %zmm3", []Operand{
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1", Rounding: "rn-sae"},
			{Kind: OpReg, Text: "%zmm2", RegName: "zmm2"},
			{Kind: OpReg, Text: "%zmm3", RegName: "zmm3"},
		}},
		{"rounding last", "vaddps %zmm1, %zmm2, %zmm3, {rz-sae}", []Operand{
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1"},
			{Kind: OpReg, Text: "%zmm2", RegName: "zmm2"},
			{Kind: OpReg, Text: "%zmm3", RegName: "zmm3", Rounding: "rz-sae"},
		}},
		{"two decorations alone", "vcmpps {sae}, {%k2}, %zmm1, %zmm2", []Operand{
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1", Rounding: "sae", Mask: "k2"},
			{Kind: OpReg, Text: "%zmm2", RegName: "zmm2"},
		}},
		{"masked memory dest", "vmovaps %zmm0, (%rax){%k1}", []Operand{
			{Kind: OpReg, Text: "%zmm0", RegName: "zmm0"},
			{Kind: OpMem, Text: "(%rax){%k1}", Base: "rax", RegName: "rax", Mask: "k1"},
		}},
		{"unknown decoration", "vaddps %zmm1, %zmm2{foo}", []Operand{
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1"},
			{Kind: OpExpr, Text: "%zmm2{foo}"},
		}},
		{"empty decoration", "vaddps %zmm1, %zmm2{}", []Operand{
			{Kind: OpReg, Text: "%zmm1", RegName: "zmm1"},
			{Kind: OpExpr, Text: "%zmm2{}"},
		}},
		{"block comment inside", "movl /* a */ %eax, %ebx", []Operand{
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
			{Kind: OpReg, Text: "%ebx", RegName: "ebx"},
		}},
		{"trailing comma", "movl %eax,", []Operand{
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
			{Kind: OpExpr},
		}},
		{"leading comma", "movl ,%eax", []Operand{
			{Kind: OpExpr},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"register expression", "movl (%eax+4), %ebx", []Operand{
			{Kind: OpExpr, Text: "(%eax+4)"},
			{Kind: OpReg, Text: "%ebx", RegName: "ebx"},
		}},
		{"register plus", "movl %eax+1, %ebx", []Operand{
			{Kind: OpExpr, Text: "%eax+1"},
			{Kind: OpReg, Text: "%ebx", RegName: "ebx"},
		}},
		{"too many parts", "movl (%eax,%ebx,4,5), %ecx", []Operand{
			{Kind: OpExpr, Text: "(%eax,%ebx,4,5)"},
			{Kind: OpReg, Text: "%ecx", RegName: "ecx"},
		}},
		{"nested group", "movl (%eax,(%ebx)), %ecx", []Operand{
			{Kind: OpExpr, Text: "(%eax,(%ebx))"},
			{Kind: OpReg, Text: "%ecx", RegName: "ecx"},
		}},
		{"index not register", "movl (%eax,4), %ecx", []Operand{
			{Kind: OpExpr, Text: "(%eax,4)"},
			{Kind: OpReg, Text: "%ecx", RegName: "ecx"},
		}},
		{"segment only", "movw %es:, %ax", []Operand{
			{Kind: OpExpr, Text: "%es:"},
			{Kind: OpReg, Text: "%ax", RegName: "ax"},
		}},
		{"string operand", `movl "s", %eax`, []Operand{
			{Kind: OpExpr, Text: `"s"`},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"star alone", "jmp *", []Operand{{Kind: OpExpr, Text: "*", Indirect: true}}},
		{"macro param", `movl \src, %eax`, []Operand{
			{Kind: OpExpr, Text: `\src`},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"macro param imm", `movb $\ch, %al`, []Operand{
			{Kind: OpExpr, Text: `$\ch`},
			{Kind: OpReg, Text: "%al", RegName: "al"},
		}},
		{"late immediate", "movl 4+$x, %eax", []Operand{
			{Kind: OpExpr, Text: "4+$x"},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"lone backslash", `movl \ , %eax`, []Operand{
			{Kind: OpExpr, Text: `\`},
			{Kind: OpReg, Text: "%eax", RegName: "eax"},
		}},
		{"macro call args", "MYMAC %eax, (%ebx)", []Operand{
			{Kind: OpExpr, Text: "%eax"},
			{Kind: OpExpr, Text: "(%ebx)"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := ".macro MYMAC a, b\n.endm\n" + tt.src
			p := parseSrc(t, src, Config{Macros: macros})
			s := firstInstr(t, p)
			if len(s.Ops) != len(tt.want) {
				t.Fatalf("%q: %d operands %+v, want %d", tt.src, len(s.Ops), s.Ops, len(tt.want))
			}
			for i := range tt.want {
				if got := bare(s.Ops[i]); got != tt.want[i] {
					t.Errorf("%q operand %d\n got  %+v\n want %+v", tt.src, i, got, tt.want[i])
				}
			}
		})
	}
}

func TestRegisterLookup(t *testing.T) {
	p := parseSrc(t, "movl %eax, %foo; fld %st(2)", Config{})
	ops := p.Stmts[0].Ops
	if ops[0].Reg == nil || ops[0].Reg.Name != "eax" || ops[0].Width() != 32 {
		t.Errorf("eax: %+v", ops[0])
	}
	if ops[1].Reg != nil || ops[1].Width() != 0 {
		t.Errorf("unknown register resolved: %+v", ops[1])
	}
	if st := p.Stmts[1].Ops[0]; st.Reg == nil {
		t.Errorf("st(2) did not resolve: %+v", st)
	}
}

func TestWidth(t *testing.T) {
	tests := []struct {
		src  string
		want []int
	}{
		{"movl (%eax), %ebx", []int{32, 32}},
		{"movb $1, (%bx)", []int{0, 8}},
		{"movw %ax, 4(%si)", []int{16, 16}},
		{"addq %rax, foo", []int{64, 64}},
		{"movq %rax, foo", []int{64, 0}}, // SSE movq: exact, no suffix
		{"mov (%eax), %ebx", []int{0, 32}},
		{"fldl (%eax)", []int{0}},
		{"jmp 1f", []int{0}},
	}
	for _, tt := range tests {
		s := parseSrc(t, tt.src, Config{}).Stmts[0]
		var got []int
		for _, op := range s.Ops {
			got = append(got, op.Width())
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: widths %v, want %v", tt.src, got, tt.want)
		}
	}
}

// stmtSummary is the part of a statement the splitting tests compare.
type stmtSummary struct {
	Kind     StmtKind
	Line     int
	EndLine  int
	Labels   []string
	Prefixes []string
	Mnemonic string
	Name     string
	Args     []string
	NOps     int
	InMacro  bool
}

func summarize(p *Program) []stmtSummary {
	out := make([]stmtSummary, 0, len(p.Stmts))
	for _, s := range p.Stmts {
		out = append(out, stmtSummary{
			Kind: s.Kind, Line: s.Line, EndLine: s.EndLine, Labels: s.Labels,
			Prefixes: s.Prefixes, Mnemonic: s.Mnemonic, Name: s.Name, Args: s.Args,
			NOps: len(s.Ops), InMacro: s.InMacroDef,
		})
	}
	return out
}

func TestStatements(t *testing.T) {
	ss := func(xs ...string) []string { return xs }
	tests := []struct {
		name string
		src  string
		cfg  Config
		want []stmtSummary
	}{
		{"labels", "a: b: nop\n1:\n", Config{}, []stmtSummary{
			{Kind: Instr, Labels: ss("a", "b"), Mnemonic: "nop"},
			{Kind: Label, Line: 1, EndLine: 1, Labels: ss("1")},
		}},
		{"separators", "cli; hlt ; jmp 1b\n;;\n", Config{}, []stmtSummary{
			{Kind: Instr, Mnemonic: "cli"},
			{Kind: Instr, Mnemonic: "hlt"},
			{Kind: Instr, Mnemonic: "jmp", NOps: 1},
		}},
		{"cpp continuation", "#define M(a) \\\n\tmovl a, %eax ; \\\n\tret\nnop", Config{}, []stmtSummary{
			{Kind: Cpp, EndLine: 2, Name: "#define", Args: ss("M(a) \\", "movl a, %eax ; \\", "ret")},
			{Kind: Instr, Line: 3, EndLine: 3, Mnemonic: "nop"},
		}},
		{"cpp spaced", "  #  define X 1\n#endif /* x */\n#unknownword\n", Config{}, []stmtSummary{
			{Kind: Cpp, Name: "#define", Args: ss("X 1")},
			{Kind: Cpp, Line: 1, EndLine: 1, Name: "#endif"},
		}},
		{"block comment joins", "movl %eax, /* x\n y */ %ebx\nnop", Config{}, []stmtSummary{
			{Kind: Instr, EndLine: 1, Mnemonic: "movl", NOps: 2},
			{Kind: Instr, Line: 2, EndLine: 2, Mnemonic: "nop"},
		}},
		{"block comment blank line", "movl %eax, /* x\n\n y */ %ebx", Config{}, []stmtSummary{
			{Kind: Instr, EndLine: 2, Mnemonic: "movl", NOps: 2},
		}},
		{"trailing block comment", "nop /* a\n b */\nhlt\n/* c */ cli", Config{}, []stmtSummary{
			{Kind: Instr, Mnemonic: "nop"},
			{Kind: Instr, Line: 2, EndLine: 2, Mnemonic: "hlt"},
			{Kind: Instr, Line: 3, EndLine: 3, Mnemonic: "cli"},
		}},
		{"comment before statement", "/* a\n b */ nop\n// x\nhlt # y", Config{}, []stmtSummary{
			{Kind: Instr, Line: 1, EndLine: 1, Mnemonic: "nop"},
			{Kind: Instr, Line: 3, EndLine: 3, Mnemonic: "hlt"},
		}},
		{"single line block then line", "nop /* a */\nhlt", Config{}, []stmtSummary{
			{Kind: Instr, Mnemonic: "nop"},
			{Kind: Instr, Line: 1, EndLine: 1, Mnemonic: "hlt"},
		}},
		{"assignments", "FOO = 0x10 + BAR\n. = _start + 510\n\\x = 1", Config{}, []stmtSummary{
			{Kind: Directive, Name: ".set", Args: ss("FOO", "0x10 + BAR")},
			{Kind: Directive, Line: 1, EndLine: 1, Name: ".set", Args: ss(".", "_start + 510")},
			{Kind: Directive, Line: 2, EndLine: 2, Name: ".set", Args: ss(`\x`, "1")},
		}},
		{"set equ", ".equ X, 5\n.set vec, vec + 1\n.EQUIV Y, 2", Config{}, []stmtSummary{
			{Kind: Directive, Name: ".set", Args: ss("X", "5")},
			{Kind: Directive, Line: 1, EndLine: 1, Name: ".set", Args: ss("vec", "vec + 1")},
			{Kind: Directive, Line: 2, EndLine: 2, Name: ".equiv", Args: ss("Y", "2")},
		}},
		{"directive args", ".section .text.stage2, \"ax\"\n.byte 1,,(2, 3)\n.text", Config{}, []stmtSummary{
			{Kind: Directive, Name: ".section", Args: ss(".text.stage2", `"ax"`)},
			{Kind: Directive, Line: 1, EndLine: 1, Name: ".byte", Args: ss("1", "", "(2, 3)")},
			{Kind: Directive, Line: 2, EndLine: 2, Name: ".text"},
		}},
		{"prefixes", "rep movsw\nlock addr32 data16 incl (%eax)\n{vex} vpaddd %xmm1, %xmm2, %xmm3", Config{}, []stmtSummary{
			{Kind: Instr, Prefixes: ss("rep"), Mnemonic: "movsw"},
			{Kind: Instr, Line: 1, EndLine: 1, Prefixes: ss("lock", "addr32", "data16"), Mnemonic: "incl", NOps: 1},
			{Kind: Instr, Line: 2, EndLine: 2, Prefixes: ss("{vex}"), Mnemonic: "vpaddd", NOps: 3},
		}},
		{"prefix alone", "rep; movsb\nes\ncs; nop", Config{}, []stmtSummary{
			{Kind: Instr, Prefixes: ss("rep")},
			{Kind: Instr, Mnemonic: "movsb"},
			{Kind: Instr, Line: 1, EndLine: 1, Prefixes: ss("es")},
			{Kind: Instr, Line: 2, EndLine: 2, Prefixes: ss("cs")},
			{Kind: Instr, Line: 2, EndLine: 2, Mnemonic: "nop"},
		}},
		{"garbage", "5 x\nfoo: (x)", Config{}, []stmtSummary{
			{Kind: Empty},
			{Kind: Empty, Line: 1, EndLine: 1, Labels: ss("foo")},
		}},
		{"macro calls", "#define OUTP(x) x\nOUTP(1)\nfrobnicate %eax\nSAVE_ALL\nMOVL %EAX, %EBX", Config{Macros: map[string]string{"SAVE_ALL": ""}}, []stmtSummary{
			{Kind: Cpp, Name: "#define", Args: ss("OUTP(x) x")},
			{Kind: MacroCall, Line: 1, EndLine: 1, Mnemonic: "outp", NOps: 1},
			{Kind: MacroCall, Line: 2, EndLine: 2, Mnemonic: "frobnicate", NOps: 1},
			{Kind: MacroCall, Line: 3, EndLine: 3, Mnemonic: "save_all"},
			{Kind: Instr, Line: 4, EndLine: 4, Mnemonic: "movl", NOps: 2},
		}},
		{"macro definition", ".macro MARK32 ch\n\tmovb $\\ch, %al\n9903:\tnop\n\\op %eax\n.endm\nMARK32 'A'", Config{}, []stmtSummary{
			{Kind: Directive, Name: ".macro", Args: ss("MARK32 ch")},
			{Kind: Instr, Line: 1, EndLine: 1, Mnemonic: "movb", NOps: 2, InMacro: true},
			{Kind: Instr, Line: 2, EndLine: 2, Labels: ss("9903"), Mnemonic: "nop", InMacro: true},
			{Kind: MacroCall, Line: 3, EndLine: 3, Mnemonic: `\op`, NOps: 1, InMacro: true},
			{Kind: Directive, Line: 4, EndLine: 4, Name: ".endm", InMacro: true},
			{Kind: MacroCall, Line: 5, EndLine: 5, Mnemonic: "mark32", NOps: 1},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(parseSrc(t, tt.src, tt.cfg))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%q\n got  %+v\n want %+v", tt.src, got, tt.want)
			}
		})
	}
}

func TestInstructionResolution(t *testing.T) {
	p := parseSrc(t, "movl %eax, %ebx\nmovsb\nlretq\nfrob", Config{})
	tests := []struct {
		inst, suffix string
	}{{"mov", "l"}, {"movs", ""}, {"lret", "q"}, {"", ""}}
	for i, tt := range tests {
		s := p.Stmts[i]
		name := ""
		if s.Inst != nil {
			name = s.Inst.Name
		}
		if name != tt.inst || s.Suffix != tt.suffix {
			t.Errorf("stmt %d %q: inst %q suffix %q, want %q %q", i, s.Mnemonic, name, s.Suffix, tt.inst, tt.suffix)
		}
	}
	if p.Stmts[0].MnemStart != 0 || p.Stmts[1].MnemStart != 16 {
		t.Errorf("MnemStart %d %d", p.Stmts[0].MnemStart, p.Stmts[1].MnemStart)
	}
}

func modes(p *Program) []Mode {
	var out []Mode
	for _, s := range p.Stmts {
		out = append(out, s.Mode)
	}
	return out
}

func TestModes(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		initial Mode
		want    []Mode
	}{
		{"default 64", "nop", 0, []Mode{64}},
		{"bad initial", "nop", 17, []Mode{64}},
		{"initial 16", "nop", 16, []Mode{16}},
		{"switches", ".code16\nmovw %ax, %bx\n.code32; movl %eax, %ebx\n.code64\nret\n.code16gcc\nnop", 32,
			[]Mode{32, 16, 16, 32, 32, 64, 64, 16}},
		{"macro body ignored", ".macro m\n.code64\n.endm\nnop", 16, []Mode{16, 16, 16, 16}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modes(parseSrc(t, tt.src, Config{InitialMode: tt.initial}))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("modes %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSections(t *testing.T) {
	src := strings.Join([]string{
		"nop",                    // .text
		".data",                  // .text (applies from the next statement)
		".byte 1",                // .data
		".section .foo, \"ax\"",  // .data
		"nop",                    // .foo
		".previous",              // .foo
		"nop",                    // .data
		".pushsection .bar",      // .data
		"nop",                    // .bar
		".previous",              // .bar
		"nop",                    // .data (previous within the pushed level)
		".popsection",            // .data
		"nop",                    // .data
		".bss",                   // .data
		".text 1",                // .bss
		"nop",                    // .text
		".section \"quoted\"",    // .text
		"nop",                    // quoted
		".section",               // quoted: no name, no change
		".section \"\"",          // quoted: empty name, no change
		".pushsection",           // quoted
		".popsection",            // quoted: nothing pushed
		".macro m\n.data\n.endm", // quoted throughout
		"nop",                    // quoted
	}, "\n")
	want := []string{
		".text", ".text", ".data", ".data", ".foo", ".foo", ".data", ".data", ".bar", ".bar",
		".data", ".data", ".data", ".data", ".bss", ".text", ".text", "quoted", "quoted",
		"quoted", "quoted", "quoted", "quoted", "quoted", "quoted", "quoted",
	}
	p := parseSrc(t, src, Config{})
	var got []string
	for _, s := range p.Stmts {
		got = append(got, s.Section)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sections\n got  %v\n want %v", got, want)
	}
}

func TestLabelsMap(t *testing.T) {
	src := "start: nop\n1: nop\nstart: hlt\n.macro m\ninner: nop\n.endm\n\\x: nop\nend:"
	p := parseSrc(t, src, Config{})
	want := map[string]int{"start": 0, "end": 7}
	if !reflect.DeepEqual(p.Labels, want) {
		t.Errorf("Labels %v, want %v", p.Labels, want)
	}
}

func TestLocalTarget(t *testing.T) {
	src := strings.Join([]string{
		"1: nop",        // 0
		"jmp 1b",        // 1
		"jmp 1f",        // 2
		"2: jmp 2b",     // 3
		"1: jmp 1f; 1:", // 4, 5
		".macro m",      // 6
		"3: nop",        // 7
		"jmp 3b",        // 8
		"jmp 1b",        // 9
		".endm",         // 10
		"jmp 3b",        // 11
		"3: nop",        // 12
		"jmp 3f",        // 13
		"m",             // 14
		"3: nop",        // 15
		"jmp 3b",        // 16
		"jmp 9f",        // 17
		"m; jmp 3b",     // 18, 19
	}, "\n")
	p := parseSrc(t, src, Config{})
	tests := []struct {
		stmt int
		ref  string
		want int
		ok   bool
	}{
		{1, "1b", 0, true},
		{2, "1f", 4, true},
		{3, "2b", 3, true},
		{4, "1f", 5, true},
		{4, "1b", 4, true},
		{8, "3b", 7, true},
		{9, "1b", -1, false},  // leaves the macro body
		{11, "3b", -1, false}, // the body's 3: is not code here
		{13, "3f", -1, false}, // m redefines 3: before the 3: below
		{16, "3b", 15, true},
		{17, "9f", -1, false},
		{19, "3b", -1, false}, // m on the same line, before the jump
		{14, "3b", 12, true},  // a macro call's own arguments see what precedes it
		{0, "x", -1, false},
		{0, "1x", -1, false},
		{0, "ab", -1, false},
		{-1, "1b", -1, false},
		{99, "1b", -1, false},
	}
	for _, tt := range tests {
		got, ok := p.LocalTarget(tt.stmt, tt.ref)
		if got != tt.want || ok != tt.ok {
			t.Errorf("LocalTarget(%d, %q) = %d %v, want %d %v", tt.stmt, tt.ref, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLabelTarget(t *testing.T) {
	p := parseSrc(t, "foo: nop\n1: jmp foo\njmp 1b\ncall bar\njmp *%eax\njmp foo+4", Config{})
	tests := []struct {
		stmt, want int
		ok         bool
	}{{1, 0, true}, {2, 1, true}, {3, -1, false}, {4, -1, false}, {5, -1, false}}
	for _, tt := range tests {
		got, ok := p.LabelTarget(tt.stmt, &p.Stmts[tt.stmt].Ops[0])
		if got != tt.want || ok != tt.ok {
			t.Errorf("LabelTarget(%d) = %d %v, want %d %v", tt.stmt, got, ok, tt.want, tt.ok)
		}
	}
}

func TestStmtAtAndRange(t *testing.T) {
	src := "nop; hlt\n#define X \\\n 1\n\n/*\u00e9*/ ret"
	p := parseSrc(t, src, Config{})
	tests := []struct {
		line int
		want []int
	}{{0, []int{0, 1}}, {1, []int{2}}, {2, []int{2}}, {3, nil}, {4, []int{3}}, {9, nil}}
	for _, tt := range tests {
		if got := p.StmtAt(tt.line); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("StmtAt(%d) = %v, want %v", tt.line, got, tt.want)
		}
	}
	s := p.Stmts[3]
	r := p.Range(s.MnemStart, s.End)
	want := protocol.Range{Start: protocol.Position{Line: 4, Character: 6}, End: protocol.Position{Line: 4, Character: 9}}
	if r != want {
		t.Errorf("Range = %+v, want %+v", r, want)
	}
	last := len(p.Toks) - 1
	if off := p.TokOffset(last); src[off:off+3] != "ret" {
		t.Errorf("TokOffset = %d", off)
	}
}

func TestPredicates(t *testing.T) {
	type flags struct{ br, call, ret, iret, cond, uncond, far bool }
	tests := []struct {
		src  string
		want flags
	}{
		{"jmp 1f", flags{br: true, uncond: true}},
		{"jnz 1f", flags{br: true, cond: true}},
		{"loop 1b", flags{br: true, cond: true}},
		{"jecxz 1f", flags{br: true, cond: true}},
		{"xbegin 1f", flags{}},
		{"xend", flags{}},
		{"call foo", flags{br: true, call: true}},
		{"lcall $1, $2", flags{br: true, call: true, far: true}},
		{"ljmp $8, $foo", flags{br: true, uncond: true, far: true}},
		{"jmp $8, $foo", flags{br: true, uncond: true, far: true}},
		{"call $8, $foo", flags{br: true, call: true, far: true}},
		{"ljmp *(%bx)", flags{br: true, uncond: true, far: true}},
		{"ret", flags{ret: true}},
		{"lretl", flags{ret: true, far: true}},
		{"iret", flags{iret: true}},
		{"iretq", flags{iret: true}},
		{"nop", flags{}},
		{"frob 1f", flags{}},
	}
	for _, tt := range tests {
		s := parseSrc(t, tt.src, Config{}).Stmts[0]
		got := flags{s.IsBranch(), s.IsCall(), s.IsRet(), s.IsIret(), s.IsCondJump(), s.IsUncondJump(), s.IsFar()}
		if got != tt.want {
			t.Errorf("%q: %+v, want %+v", tt.src, got, tt.want)
		}
	}
}

func TestFlags(t *testing.T) {
	if got := ParseFlags("CF ZF, (OF) C1 junk"); got != FlagCF|FlagZF|FlagOF {
		t.Errorf("ParseFlags = %b", got)
	}
	if got := ParseFlags("RFLAGS masked by IA32_FMASK; RF cleared"); got != FlagsAll {
		t.Errorf("ParseFlags(syscall) = %b", got)
	}
	tests := []struct {
		src           string
		reads, writes bool
		read, written Flags
	}{
		{"cmpl $1, %eax", false, true, 0, FlagsStatus},
		{"jz 1f", true, false, FlagZF, 0},
		{"movl %eax, %ebx", false, false, 0, 0},
		{"cld", false, true, 0, FlagDF},
		{"frob", false, false, 0, 0},
	}
	for _, tt := range tests {
		s := parseSrc(t, tt.src, Config{}).Stmts[0]
		if s.ReadsFlags() != tt.reads || s.WritesFlags() != tt.writes {
			t.Errorf("%q: reads %v writes %v", tt.src, s.ReadsFlags(), s.WritesFlags())
		}
		if s.FlagsRead() != tt.read || s.FlagsWritten() != tt.written {
			t.Errorf("%q: read %b written %b, want %b %b", tt.src, s.FlagsRead(), s.FlagsWritten(), tt.read, tt.written)
		}
	}
}

func immVal(t *testing.T, p *Program, i int) Value {
	t.Helper()
	return p.Stmts[i].Ops[0].ImmVal
}

func TestSymbolValues(t *testing.T) {
	src := strings.Join([]string{
		".set A, 4",                   // 0
		"pushl $A",                    // 1: 4
		".set A, A + 1",               // 2
		"pushl $A",                    // 3: 5
		"B = A * 2",                   // 4
		"pushl $B",                    // 5: 10
		".rept 2",                     // 6
		"pushl $vec",                  // 7: unknown, vec changes per iteration
		".set vec, vec + 1",           // 8
		".endr",                       // 9
		"pushl $vec",                  // 10: unknown
		".if 1",                       // 11
		".set C, 1",                   // 12
		".endif",                      // 13
		"pushl $C",                    // 14: unknown
		".eqv D, 3",                   // 15
		"pushl $D",                    // 16: unknown
		".equ E, label",               // 17
		"pushl $E",                    // 18: unknown
		".set M, 5",                   // 19
		"pushl $M",                    // 20: the cpp macro wins: 7
		"pushl $CYC",                  // 21: unknown, no hang
		"pushl $FN",                   // 22: function-like, unknown
		"pushl $LONG",                 // 23: continuation body, 3
		".macro mm\n.set F, 1\n.endm", // 24-26
		"pushl $F",                    // 27: unknown
		".rept 1\n.rept 1\n.endr\n.set G, 1\n.endr", // 28-32
		"pushl $G",   // 33: unknown
		"pushl $NEG", // 34: -1
	}, "\n")
	cfg := Config{Macros: map[string]string{"M": "7", "CYC": "CYC2", "CYC2": "(CYC)", "NEG": "-(1)"}}
	src = "#define FN(x) x\n#define LONG 1 + \\\n 2\n" + src
	p := parseSrc(t, src, cfg)
	var vals []Value
	for _, s := range p.Stmts {
		if s.Mnemonic == "pushl" {
			vals = append(vals, s.Ops[0].ImmVal)
		}
	}
	want := []Value{known(4), known(5), known(10), {}, {}, {}, {}, {}, known(7), {}, {}, known(3), {}, {}, known(-1)}
	if !reflect.DeepEqual(vals, want) {
		t.Errorf("values\n got  %v\n want %v", vals, want)
	}
	if p.Macros["LONG"] != "1 + 2" || p.Macros["M"] != "7" {
		t.Errorf("Macros = %v", p.Macros)
	}
	_ = immVal
}

func TestMacrosMap(t *testing.T) {
	tc := map[string]string{"A": "1"}
	if p := parseSrc(t, "nop", Config{Macros: tc}); !reflect.DeepEqual(p.Macros, tc) {
		t.Errorf("toolchain only: %v", p.Macros)
	}
	p := parseSrc(t, "#define B 2\n#define B 3\n#define\n#define 9\n#define C 1 /* why */ + 2\n#define D /* x */ 4\n#define E \\\n\\\n 5\nnop", Config{})
	if !reflect.DeepEqual(p.Macros, map[string]string{"B": "2", "C": "1 + 2", "D": "4", "E": "5"}) {
		t.Errorf("in-file only: %v", p.Macros)
	}
	p = parseSrc(t, "#define A 5", Config{Macros: tc})
	if !reflect.DeepEqual(p.Macros, map[string]string{"A": "5"}) {
		t.Errorf("merged: %v", p.Macros)
	}
	if tc["A"] != "1" {
		t.Error("Config.Macros was modified")
	}
}

func TestParseNumber(t *testing.T) {
	tests := []struct {
		s  string
		v  int64
		ok bool
	}{
		{"10", 10, true}, {"0x1F", 31, true}, {"0X1f", 31, true}, {"0b101", 5, true},
		{"0B11", 3, true}, {"017", 15, true}, {"0", 0, true}, {"1f", 0, false},
		{"0b", 0, false}, {"08", 0, false}, {"0xFFFFFFFFFFFFFFFF", -1, true}, {"1.5", 0, false},
		{"0x10000000000000000", 0, false}, {"18446744073709551615", -1, true}, {"18446744073709551616", 0, false},
		{"0xg", 0, false}, {"0b2", 0, false}, {"1b", 0, false},
	}
	for _, tt := range tests {
		v, ok := parseNumber(tt.s)
		if v != tt.v || ok != tt.ok {
			t.Errorf("parseNumber(%q) = %d %v, want %d %v", tt.s, v, ok, tt.v, tt.ok)
		}
	}
}

func TestParseChar(t *testing.T) {
	tests := []struct {
		s  string
		v  int64
		ok bool
	}{
		{"'a", 'a', true}, {"'a'", 'a', true}, {"'''", '\'', true}, {`'\''`, '\'', true},
		{`'\n`, 10, true}, {`'\r'`, 13, true}, {`'\t'`, 9, true}, {`'\b'`, 8, true},
		{`'\f'`, 12, true}, {`'\v'`, 11, true}, {`'\a'`, 7, true}, {`'\\'`, '\\', true},
		{`'\"'`, '"', true}, {`'\012'`, 10, true}, {`'\0'`, 0, true}, {`'\x41'`, 0x41, true},
		{`'\x'`, 0, false}, {`'\0123'`, 0, false}, {`'\q'`, 0, false}, {`'\`, 0, false},
		{"'", 0, false}, {"'ab'", 0, false}, {"'\u00e9'", 0xe9, true}, {"'\xff'", 0, false},
	}
	for _, tt := range tests {
		v, ok := parseChar(tt.s)
		if v != tt.v || ok != tt.ok {
			t.Errorf("parseChar(%q) = %d %v, want %d %v", tt.s, v, ok, tt.v, tt.ok)
		}
	}
}

func TestEvalText(t *testing.T) {
	p := &parser{cfg: Config{Macros: map[string]string{"X": "3", "UL": "(0x10UL)"}}}
	p.lookupFn = p.lookup
	tests := []struct {
		s    string
		want Value
	}{
		{"", Value{}}, {"  ", Value{}}, {"X * 2", known(6)}, {"-X", known(-3)}, {"-y", Value{}},
		{"'a' + '\\n'", known('a' + 10)}, {"'\\x41 + 1", known(0x42)}, {"1 +", Value{}},
		{"'", Value{}}, {"a.b", Value{}}, {"(X + y)", Value{}}, {"(1f + 2)", Value{}},
		{"(UL + 1)", known(17)}, {"(X - $)", Value{}},
	}
	for _, tt := range tests {
		if got := p.evalText(tt.s); got != tt.want {
			t.Errorf("evalText(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
	if got := p.evalAtom(""); got != (Value{}) {
		t.Errorf("evalAtom empty = %v", got)
	}
	if got := p.evalAtom("'"); got != (Value{}) {
		t.Errorf("evalAtom quote = %v", got)
	}
}

func TestSectionName(t *testing.T) {
	for in, want := range map[string]string{`"x y"`: "x y", ".text 1": ".text", ".foo": ".foo"} {
		if got := sectionName(in); got != want {
			t.Errorf("sectionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmptyDocument(t *testing.T) {
	p := parseSrc(t, "", Config{})
	if len(p.Stmts) != 0 || p.StmtAt(0) != nil {
		t.Errorf("stmts %v", p.Stmts)
	}
	p = parseSrc(t, "/* only\n a comment", Config{})
	if len(p.Stmts) != 0 {
		t.Errorf("stmts %v", p.Stmts)
	}
}

func TestIsGPRMove(t *testing.T) {
	cases := map[string]bool{
		"\tmovl $1, %eax":    true,
		"\tmovq $tbl, %rax":  true,
		"\tmovq %rax, %cr3":  true,
		"\tmovabs $1, %rax":  true,
		"\tmovq %xmm0, %rax": false,
		"\tmovq %mm1, %mm2":  false,
		"\tmovd %eax, %xmm0": false,
		"\taddl $1, %eax":    false,
	}
	for src, want := range cases {
		p := Parse(&docstore.Document{URI: "file:///t.S", Text: src}, lexer.Tokenize(src), Config{InitialMode: Mode64})
		var got bool
		for _, s := range p.Stmts {
			if s.Kind == Instr {
				got = s.IsGPRMove()
			}
		}
		if got != want {
			t.Errorf("%q: IsGPRMove = %v, want %v", src, got, want)
		}
	}
}
