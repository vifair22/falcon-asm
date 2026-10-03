// Package inlinec holds the rules of roadmap section 9 (inline asm in C):
// operand references, missing register and memory clobbers, missing
// early-clobber markers, C type widths against instruction sizes,
// hard-coded registers an operand may also be given, and side-effecting
// asm the compiler may delete.
//
// Every rule here runs on the C document (lint.LangC), because each one
// needs the template and the operand lists together. They share a model of
// each extended asm statement (model.go), built once per document: the
// template's instructions with their operands classified, the constraint
// of every operand, and the facts derived from both (analysis.go). The
// model only knows what it can prove; an instruction it does not know, a
// template cut short by a macro, or a statement in a preprocessor branch
// the target cannot compile makes the rules stay quiet rather than guess.
package inlinec

import (
	"fmt"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/inlineasm"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
)

// Rule codes.
const (
	CodeOperandRef      = "asm-operand-ref"
	CodeMissingClobber  = "asm-missing-clobber"
	CodeMissingMemory   = "asm-missing-memory-clobber"
	CodeEarlyClobber    = "asm-early-clobber"
	CodeOperandWidth    = "asm-operand-width"
	CodeOperandWidthTD  = "asm-operand-width-typedef"
	CodeHardcodedReg    = "asm-hardcoded-reg"
	CodeMissingVolatile = "asm-missing-volatile"
)

// reportAsm reports over the template bytes [start, end) of the statement,
// mapped into the C document.
func reportAsm(c *lint.Context, m *stmtModel, start, end int, msg string, fixes ...lint.Fix) {
	cs, ce := asmToC(m.st, start, end)
	c.ReportRange(cs, ce, msg, fixes...)
}

func asmToC(st *inlineasm.Stmt, start, end int) (int, int) {
	cs := st.Map(start)
	ce := cs
	if end > start {
		ce = st.Map(end-1) + 1
	}
	return cs, ce
}

// fixFrom turns a C edit into a quick fix.
func fixFrom(c *lint.Context, title string, e cEdit) lint.Fix {
	return lint.NewFix(title, c.ReplaceRange(e.start, e.end, e.text))
}

func docText(c *lint.Context) string {
	if c.Prog == nil || c.Prog.Doc == nil {
		return ""
	}
	return c.Prog.Doc.Text
}

// hasClobber reports whether the clobber list names fam (in any spelling)
// or, for fam "memory", the memory clobber.
func hasClobber(st *inlineasm.Stmt, fam string) bool {
	for _, cl := range st.Clobbers {
		cl = strings.TrimSpace(cl)
		if cl == fam {
			return true
		}
		if f, _, ok := family(cl); ok && f == fam {
			return true
		}
	}
	return false
}

// boundOutput returns the output operand whose fixed constraint names fam,
// or -1.
func (m *stmtModel) boundOutput(fam string) int {
	for i := range m.ops {
		if m.ops[i].out && m.ops[i].fixed[fam] {
			return i
		}
	}
	return -1
}

// boundInput returns the input operand whose fixed constraint names fam,
// or -1.
func (m *stmtModel) boundInput(fam string) int {
	for i := range m.ops {
		if !m.ops[i].out && m.ops[i].fixed[fam] {
			return i
		}
	}
	return -1
}

// opName renders operand i as the template would reference it.
func (m *stmtModel) opName(i int) string {
	if op, ok := m.st.Operand(i); ok && op.Name != "" {
		return fmt.Sprintf("%%[%s]", op.Name)
	}
	return fmt.Sprintf("%%%d", i)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = `"` + n + `"`
	}
	return strings.Join(q, ", ")
}

func init() {
	lint.Register(&lint.Rule{
		Code: CodeOperandRef, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "Operand reference out of range, unknown %[name], %l outside asm goto, or an unknown operand modifier.",
			Why: "GCC numbers the operands of an extended asm outputs first, then inputs, then (for asm goto) " +
				"labels. A %N past the end, a %[name] that names no operand, or a modifier letter the x86 " +
				"back end does not define is a compile error at best; at worst it is a register name written " +
				"with a single % (%r8 reads as operand 8 with modifier r), which the quick fix turns into %%r8.",
			Bad:  "asm(\"movl %2, %0\" : \"=r\"(out) : \"r\"(in));",
			Good: "asm(\"movl %1, %0\" : \"=r\"(out) : \"r\"(in));",
		},
		Check: checkOperandRefs,
	})
	lint.Register(&lint.Rule{
		Code: CodeMissingClobber, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "The template writes a register that is neither an output operand nor in the clobber list.",
			Why: "GCC assumes every register not named as an output or a clobber holds the same value after " +
				"the asm as before it, and keeps live values there. A template that writes %ebx (explicitly, or " +
				"implicitly through cpuid, rdtsc, rep movs, mul, syscall ...) silently corrupts whatever the " +
				"compiler had in it; the failure moves with optimisation level and inlining. Registers saved and " +
				"restored inside the template (push/pop, the PIC-safe xchg %ebx sequence) are not reported. A " +
				"register that is an input operand cannot be clobbered; the message says to make it an in/out " +
				"operand (\"+c\") instead. The quick fix adds the missing clobbers.",
			Bad:  "uint32_t lo, hi;\nasm volatile(\"cpuid\" : \"=a\"(lo), \"=d\"(hi) : \"a\"(0));",
			Good: "uint32_t lo, hi;\nasm volatile(\"cpuid\" : \"=a\"(lo), \"=d\"(hi) : \"a\"(0) : \"rbx\", \"rcx\");",
		},
		Check: checkMissingClobber,
	})
	lint.Register(&lint.Rule{
		Code: CodeMissingMemory, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "The template stores to memory that is not a memory operand, and \"memory\" is not clobbered.",
			Why: "Without a \"memory\" clobber or an \"=m\" output naming the object, GCC assumes the asm leaves " +
				"memory alone: it keeps values cached in registers across it and may move loads and stores past " +
				"it. A store through a pointer operand ((%0) with an \"r\" constraint), a rep stos/movs, or an " +
				"xsave into a buffer then goes unseen. Statements with a memory output operand are not reported, " +
				"since that operand usually names the object written. The quick fix adds \"memory\".",
			Bad:  "asm volatile(\"rep stosb\" : \"+D\"(p), \"+c\"(n) : \"a\"(0));",
			Good: "asm volatile(\"rep stosb\" : \"+D\"(p), \"+c\"(n) : \"a\"(0) : \"memory\");",
		},
		Check: checkMissingMemory,
	})
	lint.Register(&lint.Rule{
		Code: CodeEarlyClobber, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "An output is written before an input is read, but its constraint has no early-clobber '&'.",
			Why: "Without '&' GCC may give an output the same register as an input, on the assumption that " +
				"every input is consumed before any output is produced. A template that writes the output and " +
				"then reads another input reads its own result instead. The bug only appears when the register " +
				"allocator happens to share the register, so it comes and goes with unrelated changes. Outputs " +
				"tied to the input (\"0\") and read-write outputs (\"+r\") are not affected. The quick fix inserts " +
				"'&' after '='.",
			Bad:  "asm(\"movl %1, %0\\n\\taddl %2, %0\" : \"=r\"(sum) : \"r\"(a), \"r\"(b));",
			Good: "asm(\"movl %1, %0\\n\\taddl %2, %0\" : \"=&r\"(sum) : \"r\"(a), \"r\"(b));",
		},
		Check: checkEarlyClobber,
	})
	widthWhy := "GCC prints a register operand in the width of its C type: a uint8_t in \"r\" becomes %al, " +
		"a pointer %rax. A movl on it is then an assembler error, or worse, with a memory operand a 4-byte " +
		"access to a 1-byte object. A modifier that widens an input (%k0 on a uint8_t) reads upper bits the " +
		"compiler never defined, and one that narrows a pointer (%b0, %k0 in 64-bit code) truncates it. Types " +
		"come from a cast, a literal or the declaration of a plain identifier in the enclosing function; " +
		"anything else is not checked."
	lint.Register(&lint.Rule{
		Code: CodeOperandWidth, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "C operand width disagrees with the instruction's size suffix or the operand modifier.",
			Why:     widthWhy + " This rule covers standard C and <stdint.h> types; see asm-operand-width-typedef for typedefs.",
			Bad:     "uint8_t v = 1;\nasm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\");",
			Good:    "uint32_t v = 1;\nasm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\");",
		},
		Check: func(c *lint.Context) { checkOperandWidth(c, false) },
	})
	lint.Register(&lint.Rule{
		Code: CodeOperandWidthTD, Section: 9, Default: lint.Info, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "As asm-operand-width, for typedefs whose width is assumed by convention (UINT32, u8, DWORD, UINTN).",
			Why: widthWhy + " The width of these typedefs is a convention (Linux, EDK2, Windows) rather than " +
				"something visible here, so the finding is information only.",
			Bad:  "UINT8 v = 1;\nasm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\");",
			Good: "UINT32 v = 1;\nasm(\"movl %0, %%eax\" : : \"r\"(v) : \"eax\");",
		},
		Check: func(c *lint.Context) { checkOperandWidth(c, true) },
	})
	lint.Register(&lint.Rule{
		Code: CodeHardcodedReg, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "The template reads a fixed register that an \"r\"-class operand could also be allocated to.",
			Why: "A register named in the template (%%eax) that no constraint binds and no clobber reserves is " +
				"one GCC is free to use for an operand. Reading it gets whatever the compiler left there, which " +
				"may be one of the operands. Bind the value with a constraint (\"a\") or, when the asm sets the " +
				"register itself, clobber it. Writes are asm-missing-clobber's; %rsp and %rbp are not reported.",
			Bad:  "asm(\"movl %%ecx, %0\" : \"=r\"(v));",
			Good: "asm(\"movl %1, %0\" : \"=r\"(v) : \"c\"(x));",
		},
		Check: checkHardcodedReg,
	})
	lint.Register(&lint.Rule{
		Code: CodeMissingVolatile, Section: 9, Default: lint.Warning, Langs: lint.LangC,
		Doc: lint.Doc{
			Summary: "Asm with outputs and side effects is not volatile, so GCC may delete, merge or hoist it.",
			Why: "An extended asm with outputs and no volatile is treated as a pure function of its inputs: if " +
				"the outputs are unused it is deleted, two identical ones are merged, and it may be moved out of " +
				"a loop. That is wrong for port I/O, MSR and control register writes, cli/sti, invlpg, stores " +
				"to memory that is not an output, and instructions whose result differs per execution (rdtsc, " +
				"rdrand). Asm without outputs and basic asm are implicitly volatile and not reported. The quick " +
				"fix adds volatile.",
			Bad:  "uint8_t v;\nasm(\"inb %1, %0\" : \"=a\"(v) : \"Nd\"(port));",
			Good: "uint8_t v;\nasm volatile(\"inb %1, %0\" : \"=a\"(v) : \"Nd\"(port));",
		},
		Check: checkMissingVolatile,
	})
}
