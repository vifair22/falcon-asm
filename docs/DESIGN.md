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
tools/lspbench/        end-to-end LSP benchmark (added in the performance pass)
internal/benchdata/    benchmark inputs shared by the packages' benchmarks
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

### Phase 2 and 3 integration

Recorded when the feature packages were wired into the server:

- **Server and engine.** `server.Options` takes an `Engine` interface
  (`internal/server/engine.go`) instead of the MVP's `Hover` and
  `SemanticTokens` functions; `NopEngine` is the default. The server owns
  the protocol, lifecycle and document text and hands the engine immutable
  snapshots; `internal/engine` owns everything else. `Session` carries the
  root URI (rootUri, else `workspaceFolders[0]`), the raw
  initializationOptions (the engine owns their schema) and a goroutine-safe
  `Publish`. An engine error wrapping `*server.RequestFailedError` (a
  refused rename) is answered with RequestFailed (-32803) and its message;
  any other error is InternalError. Empty results of definition,
  references, documentSymbol, foldingRange, inlayHint and completion are
  answered `null`.
- **Capabilities** as sent:
  `{"textDocumentSync":{"openClose":true,"change":2,"save":{"includeText":false}},
  "hoverProvider":true,"semanticTokensProvider":{"documentSelector":[{"language":"falcon-asm"}],
  "legend":{...},"full":true,"range":false},"definitionProvider":true,
  "referencesProvider":true,"renameProvider":{"prepareProvider":true},
  "documentSymbolProvider":true,"foldingRangeProvider":true,
  "inlayHintProvider":true,"completionProvider":{"triggerCharacters":["%",".","$"]}}`.
  The semantic tokens selector is sent in the static capability (the
  registration-options shape); the engine also answers an empty stream for
  C documents, for clients that ignore the selector.
- **Per-document state.** Language comes from the didOpen language id
  (`falcon-asm` is assembly; `c`, `cpp` and the other C family ids are C);
  any other id falls back to the extension (`.c`, `.h`, `.cc`, `.cpp` ...
  are C, everything else, including `.S`, `.s`, `.asm`, is assembly).
  Derived data lives in an immutable snapshot per (version, configuration
  generation): tokens, analysis, inline asm statements and diagnostics are
  each computed once, lazily, and shared by every request. The generation
  bumps when a toolchain run brings a new macro table.
- **Additive feature APIs** so one token stream serves a version:
  `analysis.AnalyzeTokens`, `semtok.EncodeWith(doc, Options{Tokens,
  IsMacro, Deprecated})` and `hover.HoverWith(doc, pos, Options{Tokens,
  Macro})`. Deprecated painting is keyed on the start positions of the
  `mode-instruction`/`mode-register` diagnostics, so the paint and the
  warning cannot disagree. A cpp macro in mnemonic position (a
  function-like macro used as a statement) now paints as `macro` rather
  than `keyword`. `symbols.DocumentSymbols`, `FoldingRanges`, the index and
  completion still tokenize on their own.
- **Macro hover.** In-file `#define`s (analysis symbols) win over the
  toolchain table, since they reflect the text being edited; the table
  supplies headers, `-D` and builtins. The hover shows `#define NAME value`,
  `Value: hex ... - dec ...` whenever the body evaluates (negative values
  are shown signed in both), and `Defined in file line N` from `Macro.Def`.
  The evaluator is `internal/constexpr`: C integer literals (with u/l
  suffixes) and character constants, `+ - * / % << >> & | ^ ~ !`, unary
  minus and plus, parentheses, 64-bit two's complement; comparisons, `?:`,
  casts and function-like macros are rejected, expansion depth is capped at
  32 and division by zero is an error. Local label hover now uses
  `symbols.ResolveLocal`.
- **Configuration.** Compile database: `initializationOptions.compileCommands`
  (a file or a directory, relative to the root; loaded by the new
  `toolchain.LoadDBFile`), else `LoadDB(root)`, else `FindDB(file)`, cached
  per directory. A file has toolchain features when the database yields
  flags for it, exact or borrowed. Mode: `Flags.Mode`, 64 without flags.
  Level: the in-file comment (analysis), else `initializationOptions.level`,
  else `Flags.Level`. A missing database and a missing compiler are each
  logged once to stderr and never surfaced.
- **Toolchain runs** start on didOpen and didSave of an assembly file with
  flags (unless `toolchain` is false) on their own goroutine: `Macros`, then
  `Assemble`. A per-document run sequence drops a result that finishes
  after a newer run started (the Runner also cancels it). `ErrNotAssembly`
  (headers and `.inc` files, which borrow flags) keeps the macro table and
  is not logged. Shutdown cancels the runs and waits up to 2 s; the server
  also shuts the engine down when the stream ends without shutdown.
- **Diagnostics.** Published per URI: analysis (or, for C, every complete
  inline asm statement analyzed with the C file's mode and level, ranges
  mapped back through `Stmt.Map`), plus the last run's gas diagnostics for
  the file, plus inactive regions (Hint, `Unnecessary`, source
  `falcon-asm`, "inactive preprocessor region"), plus gas diagnostics other
  files' runs attributed to it. Included files that are not open get
  publishes without a version and are cleared when the owner's next run no
  longer reports them or the owner closes. Analysis publishes are debounced
  150 ms per URI; a publish re-checks under a send lock that its snapshot is
  still current, so a stale version is never sent after a newer one. The
  level comment of a C file applies to its inline asm; Partial statements
  are skipped.
- **Listing hints** are shown only when the listing describes the current
  buffer: the run records the document version it was started for, and only
  if the file on disk equalled that buffer (which also covers a client
  restoring an unsaved buffer at open). Any change hides the hints until the
  next save's run lands. This replaces the dirty flag of the plan with the
  same effect. With assembler errors gas prints no addresses, so there are
  no hints until the errors are fixed. A location move that pads nothing
  shows `pads 0 bytes`. Tooltips give the section and the line's true size
  (distance to the next offset, or the section end, not the possibly
  truncated byte dump).
- **Navigation and completion.** The index gets assembly documents only,
  with includes resolved from `#include` and `.include` lines through
  `Flags.ResolveInclude` (relative to the file without flags). Definition
  falls back to the toolchain's `Macro.Def` for names the index does not
  know (command-line or nested-header macros). Completion macros are the
  index's plus toolchain macros that have a definition location, which
  leaves out the compiler's few hundred builtins.
- **Open items for the perf pass:** `$/cancelRequest` is still ignored;
  `Index.Update` reparses on every didChange, synchronously; FindDB runs
  under the engine lock on first open in a directory. All three were
  resolved in the performance pass, below.
- Lint housekeeping done with the integration: the unused
  `symbols.equDirectives` was removed and three tests gained a `return`
  after `t.Fatal` to satisfy staticcheck's SA5011.

### Performance pass

Recorded with the performance pass; the measurements, and what was tried
and dropped, are in `docs/BENCHMARKS.md`.

- **Reading is decoupled from handling.** `Server.Run` starts a reader
  goroutine that fills a queue (`internal/server/inbox.go`); the handler
  still takes messages strictly in arrival order, so ordering is as
  before. What the queue adds is knowledge of what waits behind the
  message in hand. `$/cancelRequest` is honoured for a request still
  queued: it is answered RequestCancelled (-32800) without running.
  initialize and shutdown always run; a cancel for a request already
  answered or in progress is dropped (the LSP allows finishing it). A
  `semanticTokens/full` or `/full/delta` request with a newer didChange for
  its document already queued is answered ContentModified (-32801): its
  result would describe text the editor no longer shows, and the client
  asks again. Other requests are never skipped. `jsonrpc.Conn.Serve` is
  kept for other callers but the server no longer uses it.
- **Semantic tokens deltas.** The capability is now
  `"full":{"delta":true}`; every result carries a `resultId` (a counter);
  `textDocument/semanticTokens/full/delta` answers with one edit covering
  the tokens between the common prefix and suffix of the previous and
  current streams (aligned to whole tokens), or with the full stream when
  `previousResultId` is not the last result sent for that document. The
  server keeps one previous result per open document and drops it on
  didClose. `protocol.SemanticTokensOptions.Full` is `any` (true, or
  `SemanticTokensFullOptions`).
- **Replies are framed by hand.** `jsonrpc.Conn.Reply` appends
  `{"jsonrpc":"2.0","id":...,"result":...}` into one buffer with the
  header written into reserved space in front, instead of marshaling a
  `Message` (which re-validated and copied the encoded result). A result
  implementing `jsonrpc.JSONAppender` encodes itself into that buffer;
  `protocol.SemanticTokens` does, with `strconv.AppendUint`.
- **The symbol index is lazy.** `Index.UpdateLazy(uri, load)` stores a
  loader that the first query needing the document calls once; `Update`
  wraps it. The engine's loader returns its snapshot's parse
  (`symbols.Parse(uri, text, tokens)`, a shared `*symbols.File`) and
  include list, so a didChange computes nothing for the index, and the
  index, `File.DocumentSymbols` and `FoldingRangesTokens` all reuse the
  snapshot's one token stream. Included headers are read at that first
  query instead of at the change. Parsed files keep line offsets instead
  of a `[]string` of lines.
- **Completion** gets the document's cached tokens
  (`completion.Context.Tokens`) to decide whether the cursor line can be
  lexed on its own (no block comment open at its start), and the mode from
  `analysis.ModeAtLine`, a modes-only run of the analysis' own statement
  walk, instead of the full analysis. The snapshot computes that table
  once per version (`snapshot.modeAt`).
- **docstore** patches the line index on a ranged edit (starts before the
  edit kept, the edit rescanned, later starts shifted) instead of
  rebuilding it from the whole text.
- **Engine**: FindDB runs outside the engine lock; only the per-directory
  cache is read and filled under it (a `*toolchain.DB` is read-only once
  loaded). The analysis caches each instruction's form selection per
  operand family (`sync.Map`, since publishes analyze concurrently).
- **Startup**: the instruction tables load in a goroutine started by
  `main`, beside the initialize handshake; `isa.DataError` is still logged
  if they fail to parse.
- **Workspace scan** reads and parses files on a GOMAXPROCS worker pool.
- **Profiling and benchmarking**: the server accepts `--cpuprofile=path`
  and `--memprofile=path`, kept out of `-help` (development only).
  `tools/lspbench` drives the built binary end to end; `internal/benchdata`
  supplies the inputs every package's benchmarks share; `make bench` and
  `make lspbench` run them, and CI runs `make bench` as a non-blocking job.

## Phase 2 and 3

Phase 2: mode tracking, x86-64 level diagnostics, toolchain integration
(compile database, macros, assembler errors, listing hints), macro value
hover, outline, go-to-definition, extension PATH fallback. Phase 3: inline
asm in C, completion, inactive `#if` regions, folding, references and rename.

### Target mode and level

Each assembly document has an initial mode and an optional target level:

- Initial mode: from compile flags (`-m16` -> 16, `-m32` -> 32, `-m64` or
  none -> 64; `-m16`/`.code16gcc` count as 16). `.code16`, `.code16gcc`,
  `.code32`, `.code64` switch it for the following lines, in token order.
  `#if` branches are not evaluated for mode (documented limit).
- Target level, highest precedence first: a comment anywhere in the file
  `falcon-asm: level=v1|v2|v3|v4` (inside `/* */`, `#` or `//`); else
  `-march=x86-64-vN` (or `-march=x86-64` = v1, `-march=native` = none) from
  the compile database; else none. With no level, no level diagnostics.

### internal/analysis (pure, no exec)

```go
type Mode int // 16, 32, 64
type Config struct {
    InitialMode Mode
    Level       isa.Level // 0 = no target
    Macros      map[string]string // from toolchain; nil when unavailable
}
type Result struct {
    Diagnostics []protocol.Diagnostic
    ModeAtLine  []Mode            // per line, the mode in effect at its start
    Symbols     []Symbol          // definitions in this document
}
type Symbol struct {
    Name string; Kind SymbolKind // Label, LocalLabel, Macro (#define), AsmMacro (.macro), Equ (.set/.equ/=), Section
    Range, SelectionRange protocol.Range; Global bool; Detail string
}
func Analyze(doc *docstore.Document, cfg Config) Result
func LevelOverride(src string) (isa.Level, bool) // the in-file comment
```

Diagnostics (source `falcon-asm`):
- Instruction invalid in the current mode (isa Modes), Warning. E.g.
  `pusha` in 64-bit code, `iretq`/`swapgs` outside it.
- Register invalid in the current mode (isa Register.Modes), Warning: `%rax`,
  `%r8d`, `%sil`, `%xmm8+` outside 64-bit code. Also paints the token with
  the `deprecated` modifier (semtok consults ModeAtLine).
- Form above target level, Warning: the operand registers select the form
  family (any `%zmm`, `%k`, `{k}`/`{z}` or `%xmm16+`/`%ymm16+` operand means
  the AVX-512 forms; any `%ymm` means a VEX ymm form); the minimum level
  among the matching forms is compared to the target. Level-0 ISA sets
  (AES, VMX ...) are reported as Information only when a level target is set
  ("outside the x86-64 level scheme: requires AES").
- Unknown mnemonic is NOT a diagnostic (macros, cpp); the assembler reports
  real errors.

### internal/symbols (pure)

Workspace symbol index over open documents plus files reachable through
`#include` (resolved with the toolchain include paths; read from disk) and
other `.S`/`.s` files under the workspace root (scanned lazily on first
request, cached by mtime).

```go
type Index struct{ ... } // safe for concurrent use
func NewIndex(read func(path string) (string, error)) *Index
func (ix *Index) Update(uri string, text string, includes []string)
func (ix *Index) Remove(uri string)
func (ix *Index) Definition(uri string, pos protocol.Position) []protocol.Location
func (ix *Index) References(uri string, pos protocol.Position, includeDecl bool) []protocol.Location
func (ix *Index) Rename(uri string, pos protocol.Position, newName string) (*protocol.WorkspaceEdit, error)
func DocumentSymbols(doc *docstore.Document) []protocol.DocumentSymbol // sections > labels, plus macros
func FoldingRanges(doc *docstore.Document) []protocol.FoldingRange     // block comments, runs of line comments, .macro/.endm, .rept/.endr, #if/#endif, .if/.endif
```

Local labels (`1:`) resolve within the document by token order (existing
hover logic, moved here and shared). Global labels resolve across the
index; a `.globl` label defined in another file is found. Rename refuses
local numeric labels and names that are mnemonics or registers.

### internal/toolchain (exec)

```go
type DB struct{ ... }
func LoadDB(root string) (*DB, error)          // compile_commands.json at root, root/build, or found walking up from a file
func (db *DB) Flags(file string) (Flags, bool)  // exact entry, else nearest entry in the same directory, else none
type Flags struct {
    Compiler  string   // argv[0]
    Args      []string // filtered: keeps -I -iquote -isystem -D -U -m* -march -std -include; drops -c -o -MMD -MF -MD -MT -W* except -Wa,
    Dir       string
    Mode      int      // 16/32/64 from -m flags
    Level     isa.Level
}
type Runner struct{ Timeout time.Duration } // default 10s; one run at a time per file, newer cancels older
func (r *Runner) Macros(ctx, file string, f Flags) (map[string]Macro, error) // gcc -E -dM, plus location by scanning includes
func (r *Runner) Assemble(ctx, file string, f Flags) (*AsmResult, error)
type AsmResult struct {
    Diagnostics map[string][]protocol.Diagnostic // keyed by file path (errors can be in includes)
    Listing     []ListingLine // per source line of the main file: offset, section, bytes
    Inactive    []protocol.Range // main-file lines removed by the preprocessor (#if 0 bodies)
}
```

`Assemble` preprocesses with the file's own flags (`-E` keeping line
markers) into a temp dir, maps preprocessed lines back to (file, line) from
the markers, assembles the result with `as --listing-lhs-width=... -al=...`
(the 32/64 choice from Mode: `--32`/`--64`) or through the compiler driver
with `-c -o tmp -Wa,-al=...`, and parses `file:line: Error|Warning: msg`.
Runs only on didOpen and didSave (never on change), reads the file from
disk, writes only into a private temp dir, and fails quietly (logs once)
when there is no database or no compiler. Missing toolchain is never an
error the user sees.

### Server additions

Capabilities: `definitionProvider`, `referencesProvider`, `renameProvider`
(with prepare), `documentSymbolProvider`, `foldingRangeProvider`,
`inlayHintProvider`, `completionProvider` (trigger chars `%`, `.`, `$`),
`textDocumentSync.save`. Semantic tokens are registered with a document
selector for the `falcon-asm` language only, so C files keep clangd's
tokens. Diagnostics published per file are the union of analysis and
toolchain results; toolchain results are kept until the next save.

Inlay hints (from the listing, main file only): at the end of an
instruction or data line, `+0x1a3  b8 01 24` (offset within section, up to
8 bytes then `...`). On a `.org` or `. = expr` line, the number of padding
bytes the location move inserts (`pads 37 bytes`), which is the boot-sector
budget for `. = _start + 510`.

Hover additions: an identifier or label reference that is a known macro
shows `#define NAME value` and, when the value is a constant expression
over numbers and other macros, its evaluated value in hex and decimal.

### internal/inlineasm (phase 3)

```go
type Stmt struct {
    Asm        string          // joined, unescaped template (%% -> %)
    Map        func(asmOff int) (cOff int) // template byte offset -> C byte offset
    Outputs, Inputs []Operand  // constraint, C expression, symbolic name
    Clobbers   []string
    Range      protocol.Range  // the whole asm statement in the C file
}
func Extract(csrc string) []Stmt
func ConstraintHover(c string) string // "=a" -> "output, %eax/%ax/%al (a register)"
```

C documents (`c`, `cpp`) get hover inside asm templates (instruction,
register, `%0`/`%[name]` -> operand and its constraint) and in the operand
lists (constraint meaning), plus level diagnostics using the C file's
`-march` and mode from the compile database. No semantic tokens for C; the
VS Code client contributes an injection grammar into C/C++ asm strings.

### internal/completion (phase 3)

Context from the token before the cursor: after `%` registers valid in the
current mode; after `.` at statement start directives; at mnemonic
position mnemonics (with prefix list) ranked by exact prefix; in operand
position labels and macros from the index. Items carry a one-line detail
(the hover summary).
