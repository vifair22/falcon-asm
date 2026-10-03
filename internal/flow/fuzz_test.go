package flow

import (
	"strings"
	"testing"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/benchdata"
)

// extraLines are statements the fixtures lack but the analysis has paths
// for: structure the fuzzer should be able to splice anywhere.
var extraLines = []string{
	".if X", ".else", ".endif", ".rept 3", ".endr", "#ifdef Y", "#else", "#endif",
	".section .data", ".previous", ".text", ".code64", ".code32", ".code16",
	"1:", "1: jmp 1b", "jne 1f", "jmp *%eax", "call 1f", "ret $4", "iret", "ud2",
	"hlt", "enter $16, $0", "leave", "pushal", "popal", "pushfq", "popfq",
	"rep stosb", "repne scasb", "loop 1b", "xchgl %eax, %ebx", "movq %rsp, %rbp",
	"movq %rbp, %rsp", "andq $-16, %rsp", "movl %esp, save", "movl save, %esp",
	"vmovdqa %ymm0, %ymm1", "vzeroupper", "int $0x13", "lmsw %ax", "M 1, 2",
	".macro M a, b", ".endm", ".byte 0xea", ". = 0x1fe", "ljmp $8, $1f",
	"lea 8(%rsp), %rsp", "shlq %cl, %rax", "sarl $33, %eax", "rorw $17, %ax",
	"cqto", "cbtw", "movsbq %al, %rax", "movzwl %ax, %eax", "lss stk, %sp",
}

func fuzzLines(tb testing.TB) []string {
	var lines []string
	for _, src := range benchdata.Fixtures(tb) {
		for _, l := range strings.Split(src, "\n") {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "/*") && !strings.HasPrefix(t, "*") {
				lines = append(lines, l)
			}
		}
	}
	return append(lines, extraLines...)
}

// FuzzFlow builds programs from fixture lines chosen by the input and
// checks the graph's invariants and that the analysis terminates and
// answers every query without panicking.
func FuzzFlow(f *testing.F) {
	lines := fuzzLines(f)
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{1, 0xff, 0x10, 0x20, 0x30, 0x40, 0x50})
	f.Add([]byte{2, 0x9a, 0x01, 0x9b, 0x01, 0x9c, 0x01, 0x9d, 0x01})
	f.Add([]byte("random bytes that pick lines"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		mode := []asm.Mode{asm.Mode16, asm.Mode32, asm.Mode64}[int(data[0])%3]
		var b strings.Builder
		for k := 1; k+1 < len(data) && k < 400; k += 2 {
			idx := int(data[k])<<8 | int(data[k+1])
			b.WriteString(lines[idx%len(lines)])
			b.WriteByte('\n')
		}
		g := Build(parse(t, b.String(), mode))
		checkCFG(t, g)
		cfg := Config{}
		if data[0]&4 != 0 {
			cfg.CallClobbers = []Reg{RAX, RCX, RDX, Vec(0)}
			cfg.IntClobbers = func(uint64) ([]Reg, bool) { return []Reg{RAX}, true }
			cfg.IntEffect = func(_ uint64, st *State) { st.IF = Set }
		}
		r := Analyze(g, cfg)
		for i := range r.In {
			s := r.After(i)
			if s.Reachable != r.In[i].Reachable {
				t.Fatalf("After changed reachability at %d", i)
			}
			r.ValueAt(i, "eax")
			r.ValueAt(i, "ah")
			for k := range s.regs {
				if s.regs[k].v&^s.regs[k].mask != 0 {
					t.Fatalf("stmt %d: value bits outside the mask in %s", i, Reg(k))
				}
			}
		}
	})
}
