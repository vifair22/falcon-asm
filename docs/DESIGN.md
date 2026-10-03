# falcon-asm design

A language server for GNU as (GAS) x86 assembly in AT&T syntax, written for
the `.S` files in airies-boot (falcon-bios real mode, protected mode and long
mode code), usable from any LSP client: VS Code (thin client in
`editors/vscode`), CLion (LSP4IJ), Neovim.

Scope of the MVP: hover, semantic-token highlighting, a minimal TextMate base
grammar. Phase 2: `.code16/.code32/.code64` mode tracking, x86-64-v3 ISA
diagnostics, go-to-definition for labels and `#define` constants.

## Constraints

- Go, stdlib only. No modules in `go.mod`. The LSP protocol subset is written
  by hand in `internal/jsonrpc` and `internal/protocol`.
- Pure ASCII in every file (source, comments, strings, docs). No em-dashes,
  smart quotes, arrows or box drawing. Check with `ascii-check --diff`.
- Comments explain intent and reasoning, never restate the code.
- Errors: explicit returns, wrapped with `fmt.Errorf("...: %w", err)`.
- Tests: stdlib `testing`, table-driven, run under `-race`. Coverage floor
  80% of statements across the module.
- Positions on the wire are LSP positions: zero-based line, character offset
  in UTF-16 code units. Everything internal uses byte offsets; conversion
  happens once, in `internal/docstore`.

## Layout

```
cmd/falcon-asm/        main: flags (--version, --stdio accepted and ignored), runs server on stdin/stdout
internal/jsonrpc/      Content-Length framing, message types, a Conn that reads requests/notifications and writes responses/notifications
internal/protocol/     LSP type subset (only what we use)
internal/docstore/     open documents, incremental sync, UTF-16 <-> byte conversion
internal/lexer/        GAS + cpp tokenizer (pure function, no ISA knowledge)
internal/isa/          instruction, register and directive tables; mnemonic resolution
internal/isa/data/     embedded JSON: system.json (hand written), xed.json (generated)
internal/hover/        token at position -> short markdown
internal/semtok/       lexer tokens + isa resolution -> LSP semantic tokens
internal/server/       method dispatch, lifecycle, wires the above together
tools/xedgen/          generator: XED datafiles + asm-lsp x86 data -> internal/isa/data/xed.json
editors/vscode/        thin VS Code client + base TextMate grammar
testdata/falcon-bios/  real .S files from airies-boot, used as fixtures
```

## Package contracts

### internal/jsonrpc

```go
type Message struct {
    JSONRPC string           `json:"jsonrpc"`
    ID      *json.RawMessage `json:"id,omitempty"`
    Method  string           `json:"method,omitempty"`
    Params  json.RawMessage  `json:"params,omitempty"`
    Result  json.RawMessage  `json:"result,omitempty"`
    Error   *Error           `json:"error,omitempty"`
}
func Read(r *bufio.Reader) (*Message, error)   // io.EOF on clean end of stream
func Write(w io.Writer, m *Message) error       // safe for concurrent use via Conn
```

A response with a null result must serialize `"result":null` (hover with
nothing to show returns null, and clients reject a response that has neither
result nor error).

### internal/docstore

```go
type Store struct{ ... }                          // safe for concurrent use
func New() *Store
func (s *Store) Open(uri string, version int, text string)
func (s *Store) Change(uri string, version int, changes []protocol.TextDocumentContentChangeEvent) error
func (s *Store) Close(uri string)
func (s *Store) Get(uri string) (*Document, bool) // immutable snapshot

type Document struct {
    URI     string
    Version int
    Text    string
}
func (d *Document) Offset(p protocol.Position) (int, error) // UTF-16 position -> byte offset
func (d *Document) Position(off int) protocol.Position       // byte offset -> UTF-16 position
```

Sync kind is incremental (2). A change event without a range replaces the
whole text.

### internal/lexer

```go
type Kind int
const (
    Mnemonic     // first word of an instruction statement (after any labels / prefixes)
    Prefix       // rep, repe, repz, repne, repnz, lock, data16, addr32, rex64 ... and segment-override prefixes written as mnemonics (cs, ds, es, fs, gs, ss)
    Register     // %eax, %cr0, %xmm3, %st(1) (whole token including '%')
    Immediate    // $0x92, $sym, $(1<<4)  the '$' and the expression's first atom
    Number       // bare numeric literal not preceded by '$' (displacements, directive args)
    LabelDef     // foo:  (token excludes the colon)
    LocalLabelDef// 1:
    LabelRef     // identifier used as an operand or directive argument
    LocalLabelRef// 1f, 1b
    Directive    // .code16, .globl, .section  (whole token including '.')
    CppDirective // #include, #define, #if ... (the '#word' part only)
    CppArg       // the rest of a cpp line (header name, macro body); one token
    Comment      // /* ... */ (may span lines: emit one token per line), // ..., and '#' comments
    String       // "..." with escapes
    Identifier   // any other bare word (macro use, symbol)
    Punct        // , ( ) : * + - etc. Emitted only where useful; may be skipped
)
type Token struct {
    Kind   Kind
    Line   int    // zero based
    Col    int    // byte column within the line
    Len    int    // bytes; never spans a newline
    Text   string
}
func Tokenize(src string) []Token
```

Rules that matter for `.S` files:

- `#` at the start of a line (after optional whitespace) followed by a cpp
  directive word (`include define undef if ifdef ifndef elif else endif error
  warning pragma line`) is a CppDirective. A `#` elsewhere, or followed by
  anything else, starts a comment running to end of line.
- `/* */` comments may span lines. `//` comments run to end of line (cpp
  strips them before as sees them).
- `;` separates statements on one line. Each statement's first non-label word
  is the Mnemonic (or a Prefix followed by a Mnemonic).
- A statement whose first word starts with `.` is a Directive statement; its
  arguments are LabelRef / Number / String / Identifier.
- `1f` / `1b` style operands are LocalLabelRef.
- Lines inside a `#define` that continue with a trailing backslash belong to
  the CppArg.
- `\arg` macro parameter references inside `.macro` bodies are Identifier.

### internal/isa

```go
type Level int  // 0 unknown, 1..4 = x86-64-v1..v4 (from ISA set); levels above
                // the baseline only make sense for 64-bit code but are reported regardless

type Form struct {
    Operands string // AT&T operand order, e.g. "imm8, r/m8" ; "" for no operands
    ISASet   string // XED ISA_SET, e.g. "BMI2", "AVX512F_512", "I386"
    Level    Level
}

type Instruction struct {
    Name     string   // canonical lowercase mnemonic, e.g. "mov", "lgdt", "iret"
    Summary  string   // one line, e.g. "Load Global Descriptor Table Register"
    Category string   // XED CATEGORY or hand value: "SYSTEM", "IO", "DATAXFER", ...
    CPL      int      // 0 if privileged, 3 otherwise
    Modes    string   // which of 16/32/64 it is valid in, e.g. "16,32" ; "" = all
    FlagsRead, FlagsWritten, FlagsUndefined string // e.g. "CF ZF", "" when none
    Forms    []Form
    Aliases  []string // other spellings that resolve here (AT&T: iretl, ljmp, lret, movzbl ...)
}

type Register struct {
    Name    string // without '%', lowercase
    Class   string // "gpr", "segment", "control", "debug", "x87", "mmx", "xmm", "ymm", "zmm", "mask", "flags", "ip"
    Width   int    // bits
    Summary string // "Accumulator, low 8 bits of AX" ...
    Modes   string // "64" for r8-r15, rax..., sil/dil/spl/bpl, xmm8+ ; "" = all
    Bits    string // optional one-line bit layout for control regs, e.g. "PE(0) MP(1) EM(2) TS(3) ET(4) NE(5) WP(16) AM(18) NW(29) CD(30) PG(31)"
}

type Directive struct {
    Name    string // with leading '.'
    Summary string
    Syntax  string // ".section name[, \"flags\"[, @type]]"
}

// Lookup resolves an AT&T mnemonic. Exact match (including aliases) wins;
// only then is a single trailing size suffix (b, w, l, q) stripped and the
// base looked up. setb, jb, movsb, cwtl, scasb resolve exactly and are never
// split. suffix is "" when matched exactly.
func Lookup(word string) (inst *Instruction, suffix string, ok bool)
func LookupRegister(name string) (*Register, bool)   // name with or without '%'
func LookupDirective(name string) (*Directive, bool)
func IsPrefix(word string) bool
```

Data sources and precedence: `data/system.json` (hand written, authoritative
for system, privileged, I/O, string and control-transfer instructions) wins
over `data/xed.json` (generated) for any name present in both. Both files use
the JSON encoding of `[]Instruction`. Registers and directives are Go tables
in `registers.go` and `directives.go`.

x86-64 micro-architecture levels (from the psABI), keyed on XED ISA_SET
families:

- v1: baseline (I86..I386, I486, PENTIUM, CMOV, CX8, FPU/X87, FXSR, MMX, SSE, SSE2, SCE/syscall, LONGMODE)
- v2: CMPXCHG16B, LAHF (LAHF_SAHF), POPCNT, SSE3, SSSE3, SSE4.1, SSE4.2
- v3: AVX, AVX2, BMI1, BMI2, F16C, FMA, LZCNT, MOVBE, XSAVE (OSXSAVE)
- v4: AVX512F, AVX512BW, AVX512CD, AVX512DQ, AVX512VL
- anything else (AES, PCLMUL, SHA, VMX, AVX512_VNNI, AMX ...) is Level 0:
  outside the level scheme, reported by its ISA set name.

### internal/hover

Short markdown, at most about six lines:

```
**lgdt** - Load Global Descriptor Table Register
`lgdt m16&32`  -  SYSTEM  -  CPL 0
Flags: none
```

For instructions with many forms, list at most four distinct operand
patterns and say how many more there are. Show the level as
`x86-64-v3 (BMI2)` when Level > 1. For an AT&T suffix match, note it:
`movw` -> `mov` with `(w = 16-bit)`. Registers show class, width, modes and
bit layout when present. Directives show syntax and summary.

### internal/semtok

Token types legend (order is the wire index): `keyword` (mnemonic),
`modifier` (prefix), `variable` (register), `number`, `label` (definition and
reference), `macro` (cpp directive, macro use), `comment`, `string`,
`operator` (suffix), `type` (directive), `parameter` (immediate `$`).
Modifiers legend: `declaration` (label definitions), `defaultLibrary`
(privileged instruction, CPL 0), `deprecated` (reserved for phase 2:
instruction or register invalid in the current mode).

A resolved suffixed mnemonic emits two tokens: the base as `keyword` and the
suffix as `operator`, so `movw` paints `mov` and `w` differently. An
unresolved first word stays one `keyword` token.

### internal/server

Capabilities advertised: `textDocumentSync` incremental, `hoverProvider`,
`semanticTokensProvider` with `full: true` and the legend above. Methods:
`initialize`, `initialized`, `shutdown`, `exit`, `textDocument/didOpen`,
`didChange`, `didClose`, `textDocument/hover`,
`textDocument/semanticTokens/full`. Unknown requests get MethodNotFound
(-32601); unknown notifications are ignored. `$/cancelRequest` is ignored.
Server logs go to stderr, never stdout.

## Testing

- Unit tests per package, table driven.
- `internal/server` has an end-to-end test that builds nothing: it runs the
  server over `io.Pipe` pairs and speaks real framed JSON-RPC.
- `cmd/falcon-asm` has one test that builds the binary and drives it over
  stdio, so the real entrypoint is covered.
- Fixtures: `testdata/falcon-bios/*.S`. Tests assert on real code, e.g.
  every mnemonic in `pmode.S` resolves, `#include <start/biosdefs.h>` is a
  CppDirective plus CppArg.

## Implementation notes (deviations from the contracts above)

Recorded at MVP so the contracts stay honest:

- **Sync capability** is sent as `{"openClose":true,"change":2}`, not the
  bare kind: some clients (vscode-languageclient among them) only send
  didOpen/didClose when `openClose` is explicit.
- **docstore** adds `Document.LineCount`, `Document.Line` and `UTF16Len`, so
  semtok converts byte columns without a second conversion routine. Lines
  end at `\n`, `\r\n` or a lone `\r`; out-of-range positions clamp.
- **isa.Instruction** has a `Notes` field (IOPL-sensitive, invalid in 64-bit
  mode, ...). JSON tags are lower camelCase. A form whose Level is 0 on load
  is filled from its ISASet, so neither data file has to carry levels.
  `isa.DataError()` reports an embedded-data parse failure; the server logs
  it and keeps running on whatever parsed.
- **The dword string instructions** are named `movsl`/`cmpsl`, because
  `movsd`/`cmpsd` are the SSE2 instructions. Far forms are merged into
  `jmp`/`call`/`ret`, with `ljmp`/`lcall`/`lret` as aliases.
- **semtok**: a LabelRef or `$NAME` immediate naming a `#define` seen earlier
  in the document is painted as a macro (the lexer cannot know). `#undef`
  removes the name. Macros from included headers stay labels until phase 2
  follows includes.
- **hover**: lines are separated by blank lines (a single newline is a soft
  break in markdown). Forms are ordered lowest x86-64 level first, and with
  an AT&T suffix only forms of that width, plus forms with no sized operand,
  are listed. Labels also say whether they are `.globl`; `1f`/`1b` resolve by
  token order; numbers show hex, decimal and binary.
- **Known lexer limit**: a lone `\r` is not a line break to the lexer, so in
  classic-Mac line endings the second line reads as an operand. Positions are
  still correct. Not worth fixing for `.S` files.
