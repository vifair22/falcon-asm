// Package analysis is the pure, document-local pass behind phase 2: it
// tracks the processor mode (.code16/.code32/.code64) line by line, reports
// instructions and registers that cannot be encoded in the mode in effect,
// checks instruction forms against an x86-64 micro-architecture level
// target, and lists the symbols the document defines.
//
// It never runs a tool and never reports an unknown mnemonic: a word the
// tables do not know is far more often a macro than a typo, and the
// assembler (internal/toolchain) is the authority on real errors. Every
// diagnostic here is one the assembler would either miss (level targets) or
// report with less context (mode).
package analysis

import (
	"fmt"
	"strconv"
	"strings"

	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
	"git.airies.net/vifair22/falcon-asm/internal/protocol"
)

// Source is the Diagnostic.Source of everything this package reports, so a
// client can tell analysis findings from assembler output.
const Source = "falcon-asm"

// Diagnostic codes. They are stable identifiers a user can grep for or a
// client can filter on; the message carries the specifics.
const (
	CodeModeInstruction = "mode-instruction"
	CodeModeRegister    = "mode-register"
	CodeLevel           = "level"
	CodeLevelOutside    = "level-outside"
)

// Mode is the processor operating mode the assembler encodes for.
type Mode int

// Modes as GAS selects them with .code16/.code32/.code64.
const (
	Mode16 Mode = 16
	Mode32 Mode = 32
	Mode64 Mode = 64
)

func (m Mode) String() string { return fmt.Sprintf("%d-bit", int(m)) }

func (m Mode) valid() bool { return m == Mode16 || m == Mode32 || m == Mode64 }

// Config is what the caller knows about the document from outside it,
// normally from the compile database.
type Config struct {
	InitialMode Mode // mode at the top of the file; 0 means 64, as with no -m flag
	// InitialModeSource names where a non-zero InitialMode came from, for
	// messages ("compile flags", ".falcon-asm.json target.mode"); empty
	// means compile flags, which was the only source before it existed.
	InitialModeSource string
	Level             isa.Level         // target level; 0 = no target and no level diagnostics
	LevelSource       string            // where Level came from, for messages, e.g. "-march=x86-64-v3"; may be empty
	Macros            map[string]string // cpp macros from the toolchain; nil when unavailable
}

// Result is everything Analyze derives from one document snapshot.
type Result struct {
	Diagnostics []protocol.Diagnostic
	ModeAtLine  []Mode   // one entry per document line: the mode in effect at its start
	Symbols     []Symbol // definitions in this document, in source order
}

// SymbolKind classifies a Symbol.
type SymbolKind int

// Symbol kinds.
const (
	Label      SymbolKind = iota // name:
	LocalLabel                   // 1:
	Macro                        // #define
	AsmMacro                     // .macro
	Equ                          // .set / .equ / .equiv / .eqv / name = expr
	Section                      // .section, .text, .data, .bss ...
)

var symbolKindNames = [...]string{
	Label:      "Label",
	LocalLabel: "LocalLabel",
	Macro:      "Macro",
	AsmMacro:   "AsmMacro",
	Equ:        "Equ",
	Section:    "Section",
}

func (k SymbolKind) String() string {
	if k >= 0 && int(k) < len(symbolKindNames) {
		return symbolKindNames[k]
	}
	return "SymbolKind(?)"
}

// Symbol is one definition. SelectionRange covers the name; Range covers
// the whole construct (a #define with its continuations, a .macro through
// its .endm, a section up to the next section switch, a label's line), and
// always contains SelectionRange.
type Symbol struct {
	Name           string
	Kind           SymbolKind
	Range          protocol.Range
	SelectionRange protocol.Range
	Global         bool   // named by .globl/.global anywhere in the document
	Detail         string // macro body, .set value, .macro parameters, section flags, or a label's section
}

// Analyze runs every document-local check. It is pure and safe to call
// concurrently on different (or the same) documents.
//
// An in-file "falcon-asm: level=..." comment overrides cfg.Level, so the
// precedence in DESIGN holds whatever the caller resolved.
func Analyze(doc *docstore.Document, cfg Config) Result {
	return AnalyzeTokens(doc, lexer.Tokenize(doc.Text), cfg)
}

// AnalyzeTokens is Analyze over a token stream the caller already has, so a
// server that also paints and hovers the same document version tokenizes it
// once. toks must be lexer.Tokenize(doc.Text) and is not modified.
func AnalyzeTokens(doc *docstore.Document, toks []lexer.Token, cfg Config) Result {
	// A zero mode means no -m flag was seen; that is worth saying in a
	// message, because "from compile flags" would send the reader looking
	// for a flag that does not exist.
	initialFromFlags := cfg.InitialMode.valid()
	if !initialFromFlags {
		cfg.InitialMode = Mode64
	}
	if lvl, line, ok := levelOverride(toks); ok {
		cfg.Level = lvl
		cfg.LevelSource = fmt.Sprintf("falcon-asm: level comment at line %d", line+1)
	}
	a := newAnalyzer(doc, toks, cfg, initialFromFlags)
	a.collectMacroNames()
	a.run()
	return Result{
		Diagnostics: a.diags,
		ModeAtLine:  a.modeAtLine(),
		Symbols:     a.finishSymbols(),
	}
}

// ModeAtLine is AnalyzeTokens(doc, toks, cfg).ModeAtLine computed on its
// own. It walks the statements exactly as the full analysis does, so the
// tables cannot disagree, but skips every check and symbol: completion needs
// the mode on every keystroke, long before the debounced diagnostics want
// the rest, and on a large document the rest is most of the cost.
func ModeAtLine(doc *docstore.Document, toks []lexer.Token, cfg Config) []Mode {
	if !cfg.InitialMode.valid() {
		cfg.InitialMode = Mode64
	}
	a := newAnalyzer(doc, toks, cfg, false)
	a.modesOnly = true
	a.run()
	return a.modeAtLine()
}

// modeState remembers how the current mode was set, because "current mode:
// 16-bit" alone sends the reader hunting for the directive.
type modeState struct {
	mode Mode
	dir  string // the .codeNN directive, "" for the initial mode
	line int    // zero-based token line of that directive
}

func (s modeState) origin(fromFlags bool, source string) string {
	switch {
	case s.dir != "":
		return fmt.Sprintf("set by %s at line %d", s.dir, s.line+1)
	case fromFlags && source != "":
		return "from " + source
	case fromFlags:
		return "from compile flags"
	default:
		return "default"
	}
}

// modeSwitch records where a .codeNN took effect, by byte offset so the
// per-line table can be built in docstore's line numbering (which also
// honours a lone '\r', unlike the lexer).
type modeSwitch struct {
	off  int
	mode Mode
}

type analyzer struct {
	doc      *docstore.Document
	cfg      Config
	toks     []lexer.Token
	nlStarts []int // byte offset of each lexer line (lines split at '\n' only)

	initialFromFlags bool
	modesOnly        bool // track modes and .macro nesting only; see ModeAtLine
	mode             modeState
	switches         []modeSwitch
	macroDepth       int

	// Names that make a statement a macro invocation rather than an
	// instruction: cpp macros (toolchain and in-file) and .macro names.
	cppNames map[string]bool
	asmNames map[string]bool

	diags   []protocol.Diagnostic
	syms    []Symbol
	globals map[string]bool
	openMac []int // indexes into syms of .macro definitions awaiting .endm
	openSec int   // index into syms of the section in effect, -1 when none
	secName string
}

func newAnalyzer(doc *docstore.Document, toks []lexer.Token, cfg Config, fromFlags bool) *analyzer {
	a := &analyzer{
		doc:              doc,
		cfg:              cfg,
		toks:             toks,
		nlStarts:         make([]int, 1, strings.Count(doc.Text, "\n")+1),
		initialFromFlags: fromFlags,
		mode:             modeState{mode: cfg.InitialMode},
		cppNames:         map[string]bool{},
		asmNames:         map[string]bool{},
		globals:          map[string]bool{},
		openSec:          -1,
	}
	for i := 0; ; {
		j := strings.IndexByte(doc.Text[i:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
		a.nlStarts = append(a.nlStarts, i)
	}
	return a
}

// collectMacroNames fills the names that make a statement a macro call.
func (a *analyzer) collectMacroNames() {
	for name := range a.cfg.Macros {
		a.cppNames[name] = true
	}
	// Collect in-file macro names up front: a .macro may be invoked above
	// its definition when the file is included in pieces, and a misfire
	// here would be a false diagnostic.
	for i, t := range a.toks {
		switch {
		case t.Kind == lexer.CppDirective && cppWord(t.Text) == "define":
			if i+1 < len(a.toks) && a.toks[i+1].Kind == lexer.CppArg && a.toks[i+1].Line == t.Line {
				if name := leadingIdent(a.toks[i+1].Text); name != "" {
					a.cppNames[name] = true
				}
			}
		case t.Kind == lexer.Directive && strings.EqualFold(t.Text, ".macro"):
			if i+1 < len(a.toks) && a.toks[i+1].Line == t.Line && a.toks[i+1].Kind == lexer.Identifier {
				a.asmNames[strings.ToLower(a.toks[i+1].Text)] = true
			}
		}
	}
}

func (a *analyzer) off(t lexer.Token) int {
	if t.Line < len(a.nlStarts) {
		return a.nlStarts[t.Line] + t.Col
	}
	return len(a.doc.Text)
}

func (a *analyzer) rangeOf(first, last lexer.Token) protocol.Range {
	return protocol.Range{
		Start: a.doc.Position(a.off(first)),
		End:   a.doc.Position(a.off(last) + last.Len),
	}
}

func (a *analyzer) tokRange(t lexer.Token) protocol.Range { return a.rangeOf(t, t) }

// lineEndPos is the end of the content of the lexer line holding t.
func (a *analyzer) lineEndPos(t lexer.Token) protocol.Position {
	end := len(a.doc.Text)
	if t.Line+1 < len(a.nlStarts) {
		end = a.nlStarts[t.Line+1] - 1
	}
	return a.doc.Position(end)
}

func (a *analyzer) report(t lexer.Token, sev protocol.DiagnosticSeverity, code, msg string) {
	a.diags = append(a.diags, protocol.Diagnostic{
		Range:    a.tokRange(t),
		Severity: sev,
		Code:     code,
		Source:   Source,
		Message:  msg,
	})
}

// run walks the document one statement at a time. Statements end at a line
// break or ';'; comments are dropped because they carry nothing a check
// needs and block comments may sit inside a statement.
func (a *analyzer) run() {
	var stmt []lexer.Token
	line := -1
	inCpp := false // collecting a cpp directive and its continuation lines
	flush := func() {
		if len(stmt) > 0 {
			a.statement(stmt)
		}
		stmt = stmt[:0]
	}
	for _, t := range a.toks {
		if inCpp {
			if t.Kind == lexer.CppArg || t.Kind == lexer.Comment {
				if t.Kind == lexer.CppArg {
					stmt = append(stmt, t)
				}
				continue
			}
			inCpp = false
			flush()
			line = t.Line
		}
		if t.Line != line {
			flush()
			line = t.Line
		}
		switch {
		case t.Kind == lexer.Comment:
		case t.Kind == lexer.Punct && t.Text == ";":
			flush()
		case t.Kind == lexer.CppDirective:
			flush()
			stmt = append(stmt, t)
			inCpp = true
		default:
			stmt = append(stmt, t)
		}
	}
	flush()
}

func (a *analyzer) statement(stmt []lexer.Token) {
	if stmt[0].Kind == lexer.CppDirective {
		if !a.modesOnly {
			a.cppStatement(stmt)
		}
		return
	}
	if !a.modesOnly {
		a.labels(stmt)
	}
	for i, t := range stmt {
		switch t.Kind {
		case lexer.Directive:
			a.directive(stmt, i)
			return
		case lexer.Mnemonic:
			if a.macroDepth == 0 && !a.modesOnly {
				a.instruction(stmt, i)
			}
			return
		}
	}
}

// directive handles the directives that change state or define symbols.
// Inside a .macro body only nesting is tracked: what a body does to the
// mode or the symbol table happens where it is expanded, not where it is
// written.
func (a *analyzer) directive(stmt []lexer.Token, i int) {
	d := stmt[i]
	name := strings.ToLower(d.Text)
	switch name {
	case ".macro":
		a.macroDepth++
		if a.macroDepth == 1 && !a.modesOnly {
			a.defineAsmMacro(stmt, i)
		}
		return
	case ".endm", ".endmacro":
		if a.macroDepth > 0 {
			a.macroDepth--
			if a.macroDepth == 0 && !a.modesOnly {
				a.endAsmMacro(d)
			}
		}
		return
	}
	if a.macroDepth > 0 {
		return
	}
	switch name {
	case ".code16", ".code16gcc":
		a.setMode(Mode16, d)
		return
	case ".code32":
		a.setMode(Mode32, d)
		return
	case ".code64":
		a.setMode(Mode64, d)
		return
	}
	if a.modesOnly {
		return
	}
	switch name {
	case ".globl", ".global":
		for _, t := range stmt[i+1:] {
			if t.Kind == lexer.LabelRef {
				a.globals[t.Text] = true
			}
		}
	case ".set", ".equ", ".equiv", ".eqv":
		for j := i + 1; j < len(stmt); j++ {
			if stmt[j].Kind == lexer.LabelDef {
				a.addEqu(stmt, j)
				break
			}
		}
	case ".section", ".pushsection":
		if i+1 < len(stmt) {
			a.openSection(stmt[i+1], d, stmt, i+2)
		}
	case ".text", ".data", ".bss":
		a.openSection(d, d, stmt, i+1)
	case ".previous", ".popsection":
		// The section being returned to is not tracked; ending the open
		// range keeps the outline from claiming lines it cannot place.
		a.closeSection(d)
		a.secName = ""
	}
}

func (a *analyzer) setMode(m Mode, d lexer.Token) {
	a.mode = modeState{mode: m, dir: strings.ToLower(d.Text), line: d.Line}
	a.switches = append(a.switches, modeSwitch{off: a.off(d), mode: m})
}

// modeAtLine builds the per-line table in docstore line numbers. A switch
// applies from the line after its directive, so a line opening with
// .code32 still reports the mode it starts in.
func (a *analyzer) modeAtLine() []Mode {
	n := a.doc.LineCount()
	out := make([]Mode, n)
	cur := a.cfg.InitialMode
	si := 0
	for line := 0; line < n; line++ {
		for si < len(a.switches) && a.doc.Position(a.switches[si].off).Line < line {
			cur = a.switches[si].mode
			si++
		}
		out[line] = cur
	}
	return out
}

// isMacroCall reports whether the mnemonic-position word is a macro the
// document or toolchain knows about. Such a statement is skipped whole: its
// operands are macro arguments, not instruction operands.
func (a *analyzer) isMacroCall(word string) bool {
	return a.cppNames[word] || a.asmNames[strings.ToLower(word)]
}

func (a *analyzer) instruction(stmt []lexer.Token, mi int) {
	m := stmt[mi]
	if a.isMacroCall(m.Text) {
		return
	}
	inst, _, ok := isa.Lookup(m.Text)
	if !ok {
		// Unknown words are most often macros from an .include the
		// lexer cannot see; checking their operands would guess.
		return
	}
	word := strings.ToLower(m.Text)
	if inst.Modes != "" && !modeIn(a.mode.mode, inst.Modes) {
		a.report(m, protocol.SeverityWarning, CodeModeInstruction, fmt.Sprintf(
			"%s is only valid in %s code (current mode: %s, %s)",
			word, modesText(inst.Modes), a.mode.mode, a.mode.origin(a.initialFromFlags, a.cfg.InitialModeSource)))
	}
	for _, t := range stmt[mi+1:] {
		if t.Kind != lexer.Register {
			continue
		}
		r, ok := isa.LookupRegister(t.Text)
		if !ok || r.Modes == "" || modeIn(a.mode.mode, r.Modes) {
			continue
		}
		a.report(t, protocol.SeverityWarning, CodeModeRegister, fmt.Sprintf(
			"%%%s is only valid in %s code (current mode: %s, %s)",
			r.Name, modesText(r.Modes), a.mode.mode, a.mode.origin(a.initialFromFlags, a.cfg.InitialModeSource)))
	}
	if a.cfg.Level > 0 {
		a.levelCheck(stmt, mi, inst, word)
	}
}

// modeIn reports whether m is listed in an isa Modes string such as "64"
// or "16,32".
// It runs for every instruction and register operand, so it walks the
// string in place rather than formatting and splitting.
func modeIn(m Mode, modes string) bool {
	for modes != "" {
		p, rest, _ := strings.Cut(modes, ",")
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && Mode(n) == m {
			return true
		}
		modes = rest
	}
	return false
}

// modesText renders "16,32" as "16/32-bit".
func modesText(modes string) string {
	parts := strings.Split(modes, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, "/") + "-bit"
}
