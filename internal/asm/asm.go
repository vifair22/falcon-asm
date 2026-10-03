// Package asm is the structured program model every lint rule works on: the
// document split into statements, each instruction resolved against the ISA
// tables, its AT&T operands parsed into registers, memory references,
// immediates and labels, and the processor mode and section in effect.
//
// The lexer knows token kinds; this package knows what a statement means.
// Rules never re-lex or re-parse: they read a Program.
package asm

import (
	"git.airies.net/vifair22/falcon-asm/internal/docstore"
	"git.airies.net/vifair22/falcon-asm/internal/isa"
	"git.airies.net/vifair22/falcon-asm/internal/lexer"
)

// Mode is the processor mode a statement is assembled for (16, 32, 64).
type Mode int

// Modes as GAS selects them with .code16/.code32/.code64. .code16gcc is
// Mode16: it changes only how GAS sizes some stack operations.
const (
	Mode16 Mode = 16
	Mode32 Mode = 32
	Mode64 Mode = 64
)

// StmtKind classifies a statement.
type StmtKind int

// Statement kinds. A line holding only a label definition is a Label
// statement; a label followed by an instruction is one Instr statement whose
// Labels lists the label.
const (
	Empty StmtKind = iota
	Instr
	Directive
	Label
	Cpp
	MacroCall // first word is a .macro, #define or unknown word
)

// OpKind classifies an operand.
type OpKind int

// Operand kinds.
const (
	OpReg   OpKind = iota // %eax
	OpMem                 // seg:disp(base,index,scale), also bare symbol/number memory refs
	OpImm                 // $expr
	OpLabel               // branch target or bare symbol in a jump/call
	OpExpr                // anything the parser could not classify (macro args)
)

// Value is a constant the parser could evaluate (numbers, char literals,
// known macros, simple expressions); Known is false otherwise.
type Value struct {
	Known bool
	V     int64
}

// Operand is one parsed AT&T operand. Byte offsets are into Doc.Text.
type Operand struct {
	Kind     OpKind
	Text     string // the operand's source text, trimmed
	Start    int    // byte offset of the first character
	End      int    // byte offset one past the last character
	Indirect bool   // leading '*' (jmp *%eax, ljmp *(%bx))

	// OpReg
	Reg *isa.Register // nil when the name is not a known register

	// OpReg and OpMem: the register name as written, without '%'
	RegName string

	// OpMem
	Seg     string // segment override without '%', "" when none
	Disp    string // displacement expression text, "" when none
	DispVal Value
	Base    string // base register without '%', "" when none
	Index   string // index register without '%', "" when none
	Scale   int    // 1, 2, 4, 8; 0 when no index
	RIPRel  bool   // (%rip) base

	// OpImm: the expression after '$'
	Imm    string
	ImmVal Value

	// OpLabel: the symbol name
	Symbol string

	// AVX-512 decorations following the operand: {%k1}, {z}, {1to16}, {sae}, {rn-sae}
	Mask      string // "k1" etc, "" when none
	Zeroing   bool
	Broadcast string // "1to16" etc
	Rounding  string // "sae", "rn-sae", ...

	// sufBits is the memory operand size the statement's AT&T suffix
	// implies, so Width can answer without the statement in hand.
	sufBits int16
}

// Stmt is one statement: a line may hold several separated by ';'.
type Stmt struct {
	Kind   StmtKind
	Line   int      // zero-based line of the first token
	Start  int      // byte offset of the first token (labels included)
	End    int      // byte offset one past the last token
	Labels []string // labels defined at the start of this statement ("1" for local 1:)

	// Instr and MacroCall
	Prefixes  []string         // rep, lock, data16, addr32, segment prefixes written as words, {vex}...
	Mnemonic  string           // as written, lowercase
	MnemStart int              // byte offset of the mnemonic token
	Inst      *isa.Instruction // nil when unresolved
	Suffix    string           // AT&T size suffix resolved by isa.Lookup ("" when exact)
	Ops       []Operand        // AT&T order: sources first, destination last

	// Directive
	Name string   // ".code16", ".byte" ...
	Args []string // comma-separated argument texts, trimmed

	Mode    Mode          // mode in effect for this statement
	Section string        // section in effect (".text" default)
	Toks    []lexer.Token // the statement's tokens (sub-slice of Program.Toks)

	// EndLine is the zero-based line of the last token: a cpp directive
	// with continuations, or a statement straddling a block comment, ends
	// below Line.
	EndLine int
	// InMacroDef marks statements inside a .macro definition (through its
	// .endm). They are parsed as written, but they are not code at this
	// place: the body is assembled where the macro is invoked.
	InMacroDef bool
}

// Program is a parsed document.
type Program struct {
	Doc    *docstore.Document
	Toks   []lexer.Token
	Stmts  []Stmt
	Labels map[string]int    // global label name -> index of the statement defining it
	Macros map[string]string // macro values the parse used (toolchain + in-file #define); read only, may be shared with Config.Macros

	nl        []int               // byte offset of each lexer line
	macLocals map[string][]string // in-file .macro name -> local labels its body defines
}

// Config is what Parse needs beyond the text.
type Config struct {
	InitialMode Mode              // 16/32/64; 0 = 64
	Macros      map[string]string // known cpp macro values (name -> body), may be nil
}

// Parse builds the program model. toks must be lexer.Tokenize(doc.Text).
// Implemented in parse.go.
func Parse(doc *docstore.Document, toks []lexer.Token, cfg Config) *Program {
	return parse(doc, toks, cfg)
}
