package asm

import (
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// checkProgram enforces what every rule relies on: statements in source
// order that never overlap, tokens that are a window of Program.Toks,
// operands whose offsets slice out their own text, and every instruction
// either resolved or reported as a macro call.
func checkProgram(t testing.TB, p *Program) {
	t.Helper()
	text := p.Doc.Text
	prevEnd := 0
	for i := range p.Stmts {
		s := &p.Stmts[i]
		if s.Start < prevEnd || s.Start > s.End || s.End > len(text) {
			t.Fatalf("stmt %d %q: span %d..%d after %d", i, s.Mnemonic, s.Start, s.End, prevEnd)
		}
		prevEnd = s.End
		if s.Line > s.EndLine {
			t.Fatalf("stmt %d: Line %d > EndLine %d", i, s.Line, s.EndLine)
		}
		if len(s.Toks) == 0 {
			t.Fatalf("stmt %d: no tokens", i)
		}
		first := (uintptr(unsafe.Pointer(&s.Toks[0])) - uintptr(unsafe.Pointer(&p.Toks[0]))) / unsafe.Sizeof(p.Toks[0])
		if int(first) >= len(p.Toks) || &p.Toks[first] != &s.Toks[0] || p.TokOffset(int(first)) != s.Start {
			t.Fatalf("stmt %d: Toks is not a window of Program.Toks starting at Start", i)
		}
		if s.Mode != Mode16 && s.Mode != Mode32 && s.Mode != Mode64 {
			t.Fatalf("stmt %d: mode %d", i, s.Mode)
		}
		if s.Section == "" {
			t.Fatalf("stmt %d: no section", i)
		}
		switch s.Kind {
		case Instr:
			if s.Mnemonic == "" && len(s.Prefixes) == 0 {
				t.Fatalf("stmt %d: instruction with neither mnemonic nor prefix", i)
			}
			if s.Mnemonic != "" && s.Inst == nil {
				t.Fatalf("stmt %d %q: unresolved instruction not marked MacroCall", i, s.Mnemonic)
			}
		case MacroCall:
			if s.Mnemonic == "" {
				t.Fatalf("stmt %d: macro call without a name", i)
			}
		case Directive, Cpp:
			if s.Name == "" {
				t.Fatalf("stmt %d: directive without a name", i)
			}
		}
		if s.MnemStart >= 0 && (s.MnemStart < s.Start || s.MnemStart >= s.End) {
			t.Fatalf("stmt %d: MnemStart %d outside %d..%d", i, s.MnemStart, s.Start, s.End)
		}
		for k, op := range s.Ops {
			if op.Start > op.End || op.Start < s.Start || op.End > s.End {
				t.Fatalf("stmt %d op %d %q: span %d..%d outside statement %d..%d", i, k, op.Text, op.Start, op.End, s.Start, s.End)
			}
			// Only the left edge is always trimmed: "' " and "'\ " are
			// character constants for a blank.
			if text[op.Start:op.End] != op.Text || strings.TrimLeft(op.Text, " \t") != op.Text ||
				(strings.TrimRight(op.Text, " \t") != op.Text && !strings.Contains(op.Text, "'")) {
				t.Fatalf("stmt %d op %d: Text %q does not match source %q", i, k, op.Text, text[op.Start:op.End])
			}
			switch op.Kind {
			case OpReg:
				if op.RegName == "" {
					t.Fatalf("stmt %d op %d %q: register without a name", i, k, op.Text)
				}
			case OpLabel:
				if op.Symbol == "" {
					t.Fatalf("stmt %d op %d %q: label without a symbol", i, k, op.Text)
				}
				p.LabelTarget(i, &s.Ops[k])
			case OpImm:
				if !strings.Contains(op.Text, "$") || !strings.Contains(op.Text, op.Imm) {
					t.Fatalf("stmt %d op %d %q: immediate without '$'", i, k, op.Text)
				}
			case OpMem:
				if op.Index == "" && op.Scale != 0 {
					t.Fatalf("stmt %d op %d %q: scale without index", i, k, op.Text)
				}
			}
		}
	}
	for name, i := range p.Labels {
		found := false
		for _, l := range p.Stmts[i].Labels {
			found = found || l == name
		}
		if !found {
			t.Fatalf("Labels[%q] = %d, which does not define it", name, i)
		}
	}
}

func parseFixture(t testing.TB, name string) *Program {
	t.Helper()
	src := benchdata.Fixture(t, name)
	return parseSrc(t, src, Config{InitialMode: Mode16})
}

func TestFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(benchdata.Dir(), "*.S"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			p := parseFixture(t, name)
			instrs := 0
			for _, s := range p.Stmts {
				if s.Kind == Instr && s.Inst != nil {
					instrs++
				}
			}
			if instrs == 0 {
				t.Error("no resolved instructions")
			}
		})
	}
}

// stmtOnLine returns the first statement on a one-based line.
func stmtOnLine(t *testing.T, p *Program, line int) Stmt {
	t.Helper()
	idx := p.StmtAt(line - 1)
	if len(idx) == 0 {
		t.Fatalf("no statement on line %d", line)
	}
	return p.Stmts[idx[0]]
}

func TestFixtureLines(t *testing.T) {
	pm := parseFixture(t, "pmode.S")
	if s := stmtOnLine(t, pm, 107); !s.IsFar() || len(s.Ops) != 2 || s.Ops[1].Imm != "pmode_32" {
		t.Errorf("pmode.S:107 %+v", s)
	}
	if s := stmtOnLine(t, pm, 66); s.Ops[1].Seg != "es" || !s.Ops[1].DispVal.Known || s.Ops[1].DispVal.V != 0x510 {
		t.Errorf("pmode.S:66 %+v", s.Ops)
	}
	if s := stmtOnLine(t, pm, 22); s.Name != ".section" || pm.Stmts[pm.StmtAt(27)[0]].Section != ".text.stage2" {
		t.Errorf("pmode.S section %+v", s)
	}
	if s := stmtOnLine(t, pm, 21); s.Mode != Mode16 {
		t.Errorf("pmode.S:21 mode %d", s.Mode)
	}
	idx := pm.StmtAt(50)
	if tgt, ok := pm.LabelTarget(idx[0], &pm.Stmts[idx[0]].Ops[0]); !ok || pm.Stmts[tgt].Line != 54 {
		t.Errorf("pmode.S:51 jnz 1f -> %d %v", tgt, ok)
	}

	s2 := parseFixture(t, "stage2.S")
	idx = s2.StmtAt(304)
	if s := s2.Stmts[idx[0]]; len(s.Prefixes) != 1 || s.Prefixes[0] != "addr32" || !s.IsCondJump() {
		t.Errorf("stage2.S:305 %+v", s)
	} else if _, ok := s2.LabelTarget(idx[0], &s.Ops[0]); !ok {
		t.Error("stage2.S:305 loop 2b did not resolve")
	}

	ne := parseFixture(t, "noe820.S")
	if s := stmtOnLine(t, ne, 99); s.Ops[0].Kind != OpMem || !s.Ops[0].Indirect || s.Ops[0].Seg != "cs" {
		t.Errorf("noe820.S:99 %+v", s.Ops)
	}

	th := parseFixture(t, "thunk.S")
	if s := stmtOnLine(t, th, 49); !s.InMacroDef || s.Ops[0].Kind != OpExpr {
		t.Errorf("thunk.S:49 %+v", s)
	}
}

func FuzzParse(f *testing.F) {
	for _, src := range benchdata.Fixtures(f) {
		f.Add(src)
	}
	for _, s := range []string{
		"", "#", "#define X \\\n", "/*", "$", "%st(", "1:", "a: b = c", "{vex", "\\",
		"jmp 1f\n1:", "movl (%eax,%ebx,4), %ecx", "vaddps {sae}, %zmm1{%k1}{z}, (%rax){1to16}",
		".macro m\n1: jmp 1b\n.endm\nm\njmp 1b", ".rept 3\n.set x, x+1\n.endr", "movl $'\\", "movl $(',%eax",
		"ljmp *%cs:(a+(b-c))", "nop /* a\n\n b */ hlt", ",,,", "jmp * $", "AAA $0{Z}", ".section\"\"\n0", "movb $' , %al", "movl ,", "{z}", "jmp {sae}",
	} {
		f.Add(s)
	}
	cfg := Config{InitialMode: Mode16, Macros: map[string]string{"A": "B", "B": "(A)", "C": "4"}}
	f.Fuzz(func(t *testing.T, src string) {
		doc := newDoc(src)
		p := Parse(doc, lexer.Tokenize(src), cfg)
		checkProgram(t, p)
		for line := 0; line <= doc.LineCount(); line++ {
			for _, i := range p.StmtAt(line) {
				if p.Stmts[i].Line > line || p.Stmts[i].EndLine < line {
					t.Fatalf("StmtAt(%d) returned %d covering %d..%d", line, i, p.Stmts[i].Line, p.Stmts[i].EndLine)
				}
			}
		}
	})
}

// BenchmarkParseFixtures parses every falcon-bios fixture, tokens
// supplied, to compare with BenchmarkTokenizeFixtures in internal/lexer.
func BenchmarkParseFixtures(b *testing.B) {
	fx := benchdata.Fixtures(b)
	docs := make([]*docstore.Document, len(fx))
	toks := make([][]lexer.Token, len(fx))
	total := 0
	for i, src := range fx {
		docs[i] = newDoc(src)
		toks[i] = lexer.Tokenize(src)
		total += len(src)
	}
	b.SetBytes(int64(total))
	b.ReportAllocs()
	for b.Loop() {
		for i := range docs {
			Parse(docs[i], toks[i], Config{InitialMode: Mode16})
		}
	}
}

// BenchmarkParseLarge parses a 20k-line document, tokens supplied: the
// cost a lint run adds on top of the lexing the snapshot already did.
func BenchmarkParseLarge(b *testing.B) {
	src := benchdata.Large(b, 20000)
	doc := newDoc(src)
	toks := lexer.Tokenize(src)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		Parse(doc, toks, Config{InitialMode: Mode16})
	}
}

// BenchmarkTokenizeLarge is the lexer on the same document, so the ratio
// can be read off one run of this package's benchmarks.
func BenchmarkTokenizeLarge(b *testing.B) {
	src := benchdata.Large(b, 20000)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		lexer.Tokenize(src)
	}
}
