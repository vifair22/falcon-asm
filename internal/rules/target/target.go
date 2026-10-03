// Package target holds the rules of roadmap section 2 (mode, CPU and ISA
// target): instructions and registers that cannot be encoded in the
// .code16/32/64 mode in effect, and instruction forms above the x86-64
// level the file targets.
//
// The checks themselves live in internal/analysis, which predates the lint
// engine and also feeds mode tracking, symbols and the deprecated painting
// of semantic tokens. These rules run that analysis once per document
// (shared through the run's memo, or seeded by a caller that already has
// it) and report its findings under their registered codes, so the
// diagnostics, the painting and the engine's other users cannot disagree.
package target

import (
	"git.airies.net/vifair22/falcon-asm/internal/analysis"
	"git.airies.net/vifair22/falcon-asm/internal/asm"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lint"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
	"git.airies.net/vifair22/falcon-asm/internal/rules/flowrules"
)

const analysisKey = "target/analysis"

// AnalysisConfig is the analysis configuration a lint target implies. The
// engine builds its own analysis from the same function, which is what
// makes a seeded result (Seed) interchangeable with the one these rules
// would compute.
func AnalysisConfig(t lint.Target, macros map[string]string) analysis.Config {
	cfg := analysis.Config{Level: isa.Level(t.Level), LevelSource: t.Sources["level"], Macros: macros}
	// An initial mode nobody set stays zero, so messages say "default"
	// rather than sending the reader looking for a compile flag.
	if src := t.Sources["mode"]; src != "" {
		cfg.InitialMode = analysis.Mode(t.Mode)
		cfg.InitialModeSource = src
	}
	return cfg
}

// Seed hands the rules an analysis the caller already computed for the
// same document and AnalysisConfig.
func Seed(m *lint.Memo, r analysis.Result) { m.Set(analysisKey, r) }

func result(c *lint.Context) (analysis.Result, bool) {
	if c.Prog == nil || c.Prog.Doc == nil {
		return analysis.Result{}, false
	}
	v := c.Memo(analysisKey, func() any {
		return analysis.AnalyzeTokens(c.Prog.Doc, c.Prog.Toks, AnalysisConfig(c.Target, c.Prog.Macros))
	})
	r, ok := v.(analysis.Result)
	return r, ok
}

// reportCode re-emits the analysis diagnostics carrying code, adding fix
// when it offers one.
func reportCode(c *lint.Context, code string, fix func(analysis.Result, protocol.Diagnostic) []lint.Fix) {
	r, ok := result(c)
	if !ok {
		return
	}
	for _, d := range r.Diagnostics {
		if d.Code != code {
			continue
		}
		var fixes []lint.Fix
		if fix != nil {
			fixes = fix(r, d)
		}
		c.Report(d.Range, d.Message, fixes...)
	}
}

func init() {
	lint.Register(&lint.Rule{
		Code:    analysis.CodeModeInstruction,
		Section: 2,
		Default: lint.Warning,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "Instruction not valid in the .code16/.code32/.code64 mode in effect.",
			Why: "gas assembles for the mode the last .codeNN selected (or the -m flag), and some " +
				"instructions only exist in some modes: pusha and the BCD instructions are gone in " +
				"64-bit code, swapgs and iretq exist only there. Boot code switches modes several " +
				"times in one file, and an instruction moved across a .code32 or .code64 line is " +
				"either rejected or, worse, encodes as something else. The message names the " +
				"directive that set the mode.",
			Bad:  "\t.code64\n\tpusha",
			Good: "\t.code32\n\tpusha",
		},
		Check: func(c *lint.Context) { reportCode(c, analysis.CodeModeInstruction, nil) },
	})
	lint.Register(&lint.Rule{
		Code:    analysis.CodeModeRegister,
		Section: 2,
		Default: lint.Warning,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "Register not encodable in the mode in effect (%rax, %r8d, %sil, %xmm8 outside 64-bit code).",
			Why: "The 64-bit registers, r8-r15, the uniform byte registers and xmm8 and up need a REX " +
				"prefix, which only exists in 64-bit mode. In 16- or 32-bit code gas rejects them, and " +
				"a copy-paste from long-mode code into the real-mode entry is the usual way they get " +
				"there. A quick fix offers the register's legacy name where one exists.",
			Bad:  "\t.code32\n\tmovl $1, %rax",
			Good: "\t.code32\n\tmovl $1, %eax",
		},
		Check: func(c *lint.Context) { reportCode(c, analysis.CodeModeRegister, registerFix(c)) },
	})
	lint.Register(&lint.Rule{
		Code:    analysis.CodeLevel,
		Section: 2,
		Default: lint.Warning,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "Instruction form above the x86-64 level the file targets (v1-v4).",
			Why: "A target level (from -march=x86-64-vN, the falcon-asm.level setting, .falcon-asm.json " +
				"or a `falcon-asm: level=vN` comment) promises the code runs on every CPU of that level. " +
				"An AVX2 or BMI2 instruction in a v1 file assembles fine and then faults with #UD on " +
				"the first machine without the extension. The operand registers select the form " +
				"family, so `vpaddd %ymm...` is judged as AVX2 even though the xmm form is AVX.",
			Bad:  "# falcon-asm: level=v2\n\tandn %eax, %ebx, %ecx",
			Good: "# falcon-asm: level=v3\n\tandn %eax, %ebx, %ecx",
		},
		Check: func(c *lint.Context) { reportCode(c, analysis.CodeLevel, nil) },
	})
	lint.Register(&lint.Rule{
		Code:    analysis.CodeLevelOutside,
		Section: 2,
		Default: lint.Info,
		Langs:   lint.LangAsm | lint.LangInline,
		Doc: lint.Doc{
			Summary: "Instruction outside the x86-64 level scheme (AES, SHA, VMX ...) in a file with a level target, unless feature-unguarded judges it.",
			Why: "The psABI levels do not cover every extension: AES-NI, PCLMULQDQ, SHA, VMX and the " +
				"newer AVX-512 subsets are separate CPUID bits on top of any level. A level target " +
				"therefore says nothing about them, and code using one needs its own CPUID check. " +
				"Reported as information, since the instruction may well be guarded. In assembly, an " +
				"instruction whose CPUID bit feature-unguarded can name is left to that rule while it is " +
				"on: it reports the same missing promise only when no CPUID test guards the instruction. " +
				"What remains here is inline asm and extensions without a named bit.",
			Bad:  "# falcon-asm: level=v3\n\tptwrite %eax",
			Good: "# falcon-asm: level=v3\n\t# CPUID.(EAX=14h,ECX=0):EBX.PTWRITE checked by the caller\n\tptwrite %eax # falcon-asm: ignore=level-outside",
		},
		Check: checkLevelOutside,
	})
}

// checkLevelOutside reports what the analysis found, except instructions
// feature-unguarded decides about: it reports the same missing promise
// when no CPUID test guards the instruction and stays quiet when one does,
// so a second, vaguer note on the same mnemonic would only be noise.
func checkLevelOutside(c *lint.Context) {
	r, ok := result(c)
	if !ok {
		return
	}
	for _, d := range r.Diagnostics {
		if d.Code != analysis.CodeLevelOutside || judged(c, d.Range.Start) {
			continue
		}
		c.Report(d.Range, d.Message)
	}
}

// judged reports whether flowrules.FeatureJudged holds for the
// instruction starting at pos.
func judged(c *lint.Context, pos protocol.Position) bool {
	if c.Prog == nil || c.Prog.Doc == nil {
		return false
	}
	off, err := c.Prog.Doc.Offset(pos)
	if err != nil {
		return false
	}
	for _, i := range c.Prog.StmtAt(pos.Line) {
		st := &c.Prog.Stmts[i]
		if st.Kind == asm.Instr && st.MnemStart <= off && off < st.MnemStart+len(st.Mnemonic) {
			return flowrules.FeatureJudged(c, i)
		}
	}
	return false
}

// legacyNames are the registers a 64-bit general register's low half is
// called in 32- and 16-bit code.
var legacyNames = map[string][2]string{
	"rax": {"eax", "ax"}, "rbx": {"ebx", "bx"}, "rcx": {"ecx", "cx"}, "rdx": {"edx", "dx"},
	"rsi": {"esi", "si"}, "rdi": {"edi", "di"}, "rbp": {"ebp", "bp"}, "rsp": {"esp", "sp"},
	"rip": {"eip", "ip"},
}

// registerFix offers the mode's natural name for a 64-bit general
// register. The edit replaces only the name after '%', so it also applies
// inside an extended inline asm template, where the C source spells the
// register "%%rax".
func registerFix(c *lint.Context) func(analysis.Result, protocol.Diagnostic) []lint.Fix {
	return func(r analysis.Result, d protocol.Diagnostic) []lint.Fix {
		doc := c.Prog.Doc
		start, err1 := doc.Offset(d.Range.Start)
		end, err2 := doc.Offset(d.Range.End)
		if err1 != nil || err2 != nil || end-start < 2 || doc.Text[start] != '%' {
			return nil
		}
		names, ok := legacyNames[doc.Text[start+1:end]]
		if !ok || d.Range.Start.Line >= len(r.ModeAtLine) {
			return nil
		}
		name := names[0]
		if r.ModeAtLine[d.Range.Start.Line] == analysis.Mode16 {
			name = names[1]
		}
		return []lint.Fix{lint.NewFix("Use %"+name+" (the "+r.ModeAtLine[d.Range.Start.Line].String()+" register)",
			c.ReplaceRange(start+1, end, name))}
	}
}
