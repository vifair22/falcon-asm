package cpu

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func run(t *testing.T, text string, tgt lint.Target) []lint.Finding {
	t.Helper()
	r, ok := lint.Lookup(Code)
	if !ok {
		t.Fatal("not registered")
	}
	doc := &docstore.Document{URI: "file:///t.S", Text: text}
	toks := lexer.Tokenize(text)
	if tgt.Mode == 0 {
		tgt.Mode = asm.Mode32
	}
	p := asm.Parse(doc, toks, asm.Config{InitialMode: tgt.Mode})
	return lint.Run(lint.Input{Prog: p, Lang: lint.LangAsm, Target: tgt, Rules: []*lint.Rule{r}})
}

func TestCPUUnsupported(t *testing.T) {
	cases := []struct {
		cpu, text, want string
	}{
		{"pentium", "\tcmovel %ebx, %eax\n", "first in pentiumpro"},
		{"i386", "\tcpuid\n", "I486REAL"},
		{"i486", "\trdtsc\n", "PENTIUMREAL"},
		{"i586", "\tud2\n", "PPRO"},
		{"pentium", "\tmovd %eax, %mm0\n", "MMX"},
		{"k6", "\tsyscall\n", "LONGMODE"},
		{"pentium3", "\t.code64\n\tswapgs\n", "LONGMODE"},
	}
	for _, c := range cases {
		got := run(t, c.text, lint.Target{CPU: c.cpu, Sources: map[string]string{"cpu": "-march=" + c.cpu}})
		if len(got) != 1 || !strings.Contains(got[0].Message, c.want) {
			t.Errorf("%s %q: %v", c.cpu, c.text, got)
		}
	}
	clean := []struct{ cpu, text string }{
		{"pentium", "\tcpuid\n\trdtsc\n\tlock xaddl %eax, (%ebx)\n\tmovl %cr4, %eax\n\tinb $0x92, %al\n"},
		{"pentiumpro", "\tcmovel %ebx, %eax\n"},
		{"k6-2", "\tsyscall\n"},             // AMD's legacy-mode SYSCALL
		{"athlon-xp", "\tsysret\n"},         // inherited from athlon
		{"", "\tcmovel %ebx, %eax\n"},       // no CPU target
		{"native", "\tcmovel %ebx, %eax\n"}, // unknown profile
	}
	for _, c := range clean {
		if got := run(t, c.text, lint.Target{CPU: c.cpu}); len(got) != 0 {
			t.Errorf("%s %q: %v", c.cpu, c.text, got)
		}
	}
	// Intel never had 32-bit SYSCALL; AMD's 64-bit form still needs long mode.
	if got := run(t, "\t.code64\n\tsyscall\n", lint.Target{CPU: "k6-2"}); len(got) != 1 {
		t.Errorf("64-bit syscall on k6-2: %v", got)
	}
	// A level profile with a level target too: the level rules speak.
	if got := run(t, "\tandn %eax, %ebx, %ecx\n", lint.Target{CPU: "x86-64-v2", Level: 2}); len(got) != 0 {
		t.Errorf("duplicate of the level rule: %v", got)
	}
	if got := run(t, "\tandn %eax, %ebx, %ecx\n", lint.Target{CPU: "x86-64-v2"}); len(got) != 1 {
		t.Errorf("level profile alone: %v", got)
	}
	// The message names where the target came from.
	got := run(t, "\tcmovel %ebx, %eax\n", lint.Target{CPU: "pentium"})
	if len(got) != 1 || !strings.Contains(got[0].Message, "(target)") || got[0].Range.Start.Character != 1 {
		t.Errorf("default source: %v", got)
	}
}

func TestProfile(t *testing.T) {
	if p, ok := Profile("i686"); !ok || p.Name != "pentiumpro" {
		t.Fatalf("i686 = %v", p)
	}
	if _, ok := Profile(""); ok {
		t.Fatal("empty")
	}
	if _, ok := Profile("nonesuch"); ok {
		t.Fatal("unknown")
	}
	if got := firstWith(mustProfile(t, "k6"), "3DNOW"); got != "k6-2" {
		t.Fatalf("k6 line: %q", got)
	}
	if got := firstWith(mustProfile(t, "pentium"), "NOSUCHSET"); got != "" {
		t.Fatalf("nothing has it: %q", got)
	}
}

func mustProfile(t *testing.T, n string) *hwdb.Profile {
	t.Helper()
	p, ok := Profile(n)
	if !ok {
		t.Fatal(n)
	}
	return p
}
