package hw

import (
	"encoding/json"
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/hwdb"
)

func TestCHover(t *testing.T) {
	src := "#define PCI_DATA 0xCFC\n" +
		"#define MSR_EFER 0xC0000080 /* extended features */\n" +
		"#define LEAF(x) (x)\n" +
		"static inline void outb(uint16_t port, uint8_t v);\n" +
		"void f(void) {\n" +
		"\toutb(0x92, v);\n" + // 5
		"\tx = inl(PCI_DATA);\n" + // 6
		"\ty = rdmsr(MSR_EFER);\n" + // 7
		"\tcpuid_count(7, 0, r);\n" + // 8
		"\toutb((uint16_t)0x70, 0x0A);\n" + // 9
		"\twrmsr(MSR_EFER, 0x100);\n" + // 10
		"\toutb(port, v);\n" + // 11
		"\tnothing(0x92);\n" + // 12
		"\tinb(TOOL_PORT);\n" + // 13
		"\toutb(foo(1, \"a,b\"), 2);\n" + // 14
		"}\n"
	db := hwdb.Default()
	tool := func(name string) (string, bool) {
		if name == "TOOL_PORT" {
			return "0x3F8", true
		}
		return "", false
	}
	at := func(line int, sub string, delta int) string {
		ls := strings.Split(src, "\n")
		off := 0
		for i := 0; i < line; i++ {
			off += len(ls[i]) + 1
		}
		k := strings.Index(ls[line], sub)
		if k < 0 {
			t.Fatalf("%q not on line %d", sub, line)
		}
		md, _, _ := CHover(src, off+k+delta, db, tool)
		return md
	}
	cases := []struct {
		line  int
		sub   string
		delta int
		want  string
	}{
		{5, "outb", 1, "0x92"},
		{5, "0x92", 1, "0x92"},
		{6, "inl", 0, "0xCFC"},
		{6, "PCI_DATA", 2, "0xCFC"},
		{7, "rdmsr", 2, "EFER"},
		{8, "cpuid_count", 3, "07h"},
		{9, "outb", 0, "CMOS"},
		{10, "wrmsr", 0, "EFER"},
		{13, "inb", 0, "COM1"},
	}
	for _, c := range cases {
		if md := at(c.line, c.sub, c.delta); !strings.Contains(md, c.want) {
			t.Errorf("line %d %q: %q, want %q", c.line, c.sub, md, c.want)
		}
	}
	if md := at(9, "outb", 0); !strings.Contains(md, "selects") {
		t.Errorf("index write not named: %q", md)
	}
	for _, c := range []struct {
		line  int
		sub   string
		delta int
	}{{3, "outb", 0}, {11, "outb", 0}, {12, "nothing", 0}, {5, "v)", 0}, {14, "outb", 0}, {4, "void", 0}} {
		if md := at(c.line, c.sub, c.delta); md != "" {
			t.Errorf("line %d %q: unexpected %q", c.line, c.sub, md)
		}
	}
	if md, _, _ := CHover(src, -1, db, nil); md != "" {
		t.Fatal("negative offset")
	}
	// Project helper names (overrides).
	pdb, err := hwdb.Load(json.RawMessage(`{"helpers":[{"name":"port_out8","kind":"port","write":true,"value":1,"width":8},{"name":"outb","kind":"none"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	text := "void g(void) { port_out8(0x80, 1); outb(0x92, 1); }\n"
	if md, _, _ := CHover(text, strings.Index(text, "port_out8"), pdb, nil); !strings.Contains(md, "0x80") {
		t.Fatalf("override helper: %q", md)
	}
	if md, _, _ := CHover(text, strings.Index(text, "outb"), pdb, nil); md != "" {
		t.Fatalf("removed helper: %q", md)
	}
}

func TestCHelpers(t *testing.T) {
	if got := stripCasts("(unsigned long)(uint16_t) 0x10"); got != "0x10" {
		t.Fatalf("stripCasts = %q", got)
	}
	if got := stripCasts("(1 << 4)"); got != "(1 << 4)" {
		t.Fatalf("stripCasts = %q", got)
	}
	d := cDefines("#define A 1\n# define B (2) // two\n#define C(x) x\n#define D \\\n 3\n#defineE 4\n#define\n")
	if d["A"] != "1" || d["B"] != "(2)" || len(d) != 2 {
		t.Fatalf("defines = %v", d)
	}
	if _, ok := splitArgs("f(a, b", 1); ok {
		t.Fatal("unterminated")
	}
	if _, ok := splitArgs("f(a]", 1); ok {
		t.Fatal("mismatched")
	}
	if a, ok := splitArgs("f()", 1); !ok || len(a) != 0 {
		t.Fatalf("empty = %v", a)
	}
}
