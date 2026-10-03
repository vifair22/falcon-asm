package flowrules

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/flow"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

func init() {
	lint.Register(&lint.Rule{
		Code:      CodeSelfModifying,
		Section:   sectionDataflow,
		Default:   lint.Info,
		Langs:     langs,
		NeedsFlow: true,
		Doc: lint.Doc{
			Summary: "Store into an instruction in an executable section (self-modifying code).",
			Why: "Patching an instruction (the immediate of a far jump, an opcode) works in real mode and " +
				"is an old boot loader trick, but it is invisible in the source, it breaks the moment the " +
				"code is placed in ROM or a write-protected page, and on processors since the i486 a write " +
				"close ahead of the executing instruction may or may not be seen without a jump in " +
				"between. The rule reports a store whose memory operand names a label of code (an " +
				"instruction, not a data directive) in .text or a section with the x flag, as Information: " +
				"it is sometimes the point. Variables kept in .text (a data label) are not code and are not " +
				"reported.",
			Bad:  "\tmovw %ax, patch+1\npatch:\n\tmovw $0, %bx",
			Good: "\tmovw %ax, value\n\tmovw value, %bx\n\t.section .data\nvalue:\n\t.word 0",
		},
		Check: checkSMC,
	})
}

// execSections maps every section the document declares to whether it is
// executable: an explicit flags string with x, else the conventional
// .text names.
func execSections(p *asm.Program) map[string]bool {
	out := map[string]bool{}
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Directive || st.Name != ".section" || len(st.Args) == 0 {
			continue
		}
		name := strings.TrimSpace(st.Args[0])
		if len(st.Args) > 1 {
			flags := strings.Trim(strings.TrimSpace(st.Args[1]), "\"")
			out[name] = strings.ContainsRune(flags, 'x')
		}
	}
	return out
}

func isExec(sections map[string]bool, name string) bool {
	if x, ok := sections[name]; ok {
		return x
	}
	return name == "" || name == ".text" || strings.HasPrefix(name, ".text.") || name == ".init" || name == ".fini"
}

func checkSMC(c *lint.Context) {
	f := factsOf(c)
	if f == nil {
		return
	}
	p, g, res := f.p, f.g, f.res
	var sections map[string]bool
	for i := range p.Stmts {
		st := &p.Stmts[i]
		if st.Kind != asm.Instr || st.Inst == nil || len(st.Ops) == 0 || !res.Reachable(i) {
			continue
		}
		k := len(st.Ops) - 1
		op := &st.Ops[k]
		if op.Kind != asm.OpMem || !flow.DestWritten(st) || st.Inst.Name == "lea" || op.Disp == "" || op.Base != "" || op.Index != "" {
			continue
		}
		t, name := codeLabel(p, i, op.Disp)
		if t < 0 || g.BlockOf[t] < 0 {
			continue
		}
		if sections == nil {
			sections = execSections(p)
		}
		if !isExec(sections, p.Stmts[t].Section) {
			continue
		}
		c.ReportOp(i, k, fmt.Sprintf("%s writes into the instruction at %s (line %d) in executable section %s: self-modifying code",
			st.Mnemonic, name, lineOf(p, t), sectionName(p.Stmts[t].Section)))
	}
}

func sectionName(s string) string {
	if s == "" {
		return ".text"
	}
	return s
}

// codeLabel resolves the symbol a displacement starts with ("patch",
// "patch+1", "1b") to the statement defining it, -1 when it names no label
// of this document. The target counts only when it is code (the CFG gave
// it a block), never a data label.
func codeLabel(p *asm.Program, i int, disp string) (int, string) {
	sym := disp
	if k := strings.IndexAny(sym, "+-*/() "); k >= 0 {
		sym = sym[:k]
	}
	sym = strings.TrimSpace(sym)
	if sym == "" {
		return -1, ""
	}
	if sym[0] >= '0' && sym[0] <= '9' {
		if t, ok := p.LocalTarget(i, sym); ok {
			return t, sym
		}
		return -1, ""
	}
	if t, ok := p.Labels[sym]; ok {
		return t, sym
	}
	return -1, ""
}
