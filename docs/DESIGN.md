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

### Linter performance and usability pass

Recorded with the pass that closed the linter work; numbers in
`docs/BENCHMARKS.md` ("Linter pass").

- **One parse and one dataflow per version.** The snapshot owns
  `program()` (the `asm.Parse` the rules use: target mode, toolchain
  macros), `flow()` (CFG and solution, `flow.Config{}`, a panic logged and
  turned into nils) and `hwIndex()` (built from both). The lint run gets
  the program through `project.Doc.Prog` and the other two through
  `lint.ProvideFlow` and `hw.Provide`, which register the snapshot's
  functions with the run's memo (`Memo.Provide`): the first rule that asks
  computes through them, so the run, hover, register hints and code lenses
  share one of each, and a run whose rules never ask builds none.
  `flow.Analyze` drops the analyzer's block states once solved.
- **Rules stay off the keystroke path.** Semantic tokens use the analysis,
  completion the modes table, hover the hardware index (below); none of
  them runs a rule. `textDocument/codeAction` with `context.triggerKind`
  Automatic (2, VS Code's light bulb after a cursor move or an edit) is
  answered `null` while the version has not been linted
  (`snapshot.lintDone`): the editor asks again when the diagnostics
  arrive. An invoked request, or one without a trigger kind, still lints
  on demand.
- **Hover builds the hardware index only when it can add something.**
  `snapshot.mayHaveHardware` looks at the token under the cursor: comments,
  label definitions, cpp lines, prefixes and strings never get hardware
  text, and a mnemonic only when `hw.MnemonicMayHover` says so (int,
  in/out, rdmsr/wrmsr, cpuid, xsetbv, and mov when the line names a
  control register).
- **Hardware hovers are trimmed** (`trimHardware`): Notes and Quirks
  lists, structure layout tables and confidence remarks are dropped, a
  paragraph an earlier section already showed is not repeated, and the
  rest is capped at 12 non-blank lines with a "(N more lines)" note. An
  int whose AH differs by path lists one title line per candidate service.
- **level-outside defers to feature-unguarded** for assembly
  instructions the latter judges (`flowrules.FeatureJudged`: rule on,
  flow available, reachable code in a function, a nameable CPUID bit),
  since both would report the same missing promise on one mnemonic.
- **Startup** loads the hardware tables (`hwdb.DataError`) on a goroutine
  of their own beside the instruction tables, so neither waits for the
  other and neither is parsed on a request.
- **The macro expansion hover** parses the version only when the word can
  be a macro (`snapshot.mayExpand`: a `.macro` of that name in the file,
  an assembly include, or a cpp macro), and then uses `program()`.
- `tools/lspbench` gained the `features`, `diagnostics` and `cli`
  scenarios, edit-to-publish timing in `workspace` and code lenses in
  `tree`; the client records the version of each publish.

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

Inlay hints (from the listing, main file only): after the code of an
instruction or data line, ahead of any trailing comment (which the hint
pushes right), `+0x1a3  b8 01 24` (offset within section, up to 8 bytes
then `...`). A line with no code of its own keeps the hint at its end. On a `.org` or `. = expr` line, the number of padding
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

## Lint engine

Recorded with the lint engine foundation (roadmap items 78, 80-83, 86).
Every check of the roadmap is a rule plugged into it.

### Packages

```
internal/lint/          registry, Context, runner, .falcon-asm.json, suppressions, RULES.md rendering
internal/rules/<area>/  rules, registered from init(); rules/target holds section 2
internal/rules/all/     imports every rule package; the engine and the CLI import it
internal/project/       shared by the engine and the CLI: target resolution, linting one document
```

`lint` depends on `asm`, `flow`, `toolchain` and `inlineasm`, never on
`hwdb`: the hardware tables reach rules through the `lint.Tables`
interface, built by a loader `internal/rules/hw` registers with
`lint.RegisterTables` (from there rather than from `hwdb`, so `hwdb` stays
free of the lint engine); the engine and the CLI call
`lint.LoadTables(fileConfig.Tables)`, and `ParseConfig` calls it too, so a
bad `tables` entry is a configuration problem at the JSON path the loader
names. With no loader registered the tables are nil and rules must treat
that as "nothing known". `lint.MSRChecker` is the optional part rules
type-assert for wrmsr checks.

### Registry

`lint.Register(*Rule)` panics on a code that is not lowercase kebab-case
(or is `all`), a duplicate, a section outside 1..12, a nil Check, an
invalid default severity, no language or an empty `Doc.Summary`.
`lint.Rules()` is sorted by section, then code; `lint.Lookup(code)` finds
one. A default of `Off` makes a rule opt-in.

### Context (what a rule sees)

`Prog`, `Target`, `Listing` (only for `NeedsListing` rules; such a rule is
skipped when there is no listing for the current text), `Tables`, plus
`Lang()`, `Config()`, `Inline()` (the C document's asm statements, for
`LangC` rules), `Severity()` and `Rule()`.

- Reporting: `Report(range, msg, fixes...)`, `ReportStmt(i, ...)`,
  `ReportOp(i, j, ...)`, `ReportRange(startByte, endByte, ...)`,
  `ReportFinding(f)` (for `Related`). Code and severity are always the
  running rule's. Out-of-range indices and offsets are ignored.
- `Flow()` builds the CFG and dataflow once per document, shared by every
  rule; `NeedsFlow` rules get it up front and are skipped when it could not
  be built (a panic in `flow`, logged once).
- `Memo(key, fn)` computes a value once per document run and shares it;
  a caller may seed values it already has (`Input.Memo`).
- Fix helpers return `protocol.TextEdit`s: `InsertBefore(stmt, text)`,
  `ReplaceRange(start, end, text)` (clamped), `InsertLineAbove(stmt, text)`
  (indented like the statement's line, keeping the file's line ending);
  `lint.NewFix(title, edits...)` bundles them.

### Runner

`lint.Run(Input)` lints one assembly program; `lint.Batch` collects several
runs over one document (a C file is one `LangInline` run per complete asm
statement plus one `LangC` run) and `Finish` applies suppressions once.
Per rule: skipped when its `Langs` miss the input's language, its resolved
severity is `Off`, or it needs a listing there is none of. A panicking
rule is recovered, logged once per process, and its findings for that
document dropped; the other rules still run. Findings are sorted by start,
end, code and message, and exact duplicates removed. `Input.MapRange` and
`MapEdit` carry findings and fixes from an inline asm template into the C
document; an edit is kept only when the C text under it reads exactly like
the template text and the new text needs no escaping (`"`, `\`, `%`, line
breaks), which is why rule fixes should edit names rather than whole
operands (`rax` -> `eax`, not `%rax` -> `%eax`).

### Configuration: .falcon-asm.json

```json
{
  "rules":     { "<code>": "off" | "hint" | "info" | "warning" | "error" },
  "overrides": [ { "paths": ["glob", ...], "rules": { ... }, "target": { ... } } ],
  "target":    { "cpu": "pentium", "level": "v3", "cc": "sysv", "mode": 16 },
  "tables":    { "ports": [...], "msrs": [...], "bios": [...], "helpers": [...] }
}
```

- Found by walking up from the file (the nearest wins), in the engine and
  the CLI alike; `--config` overrides it for the CLI. Overrides apply in
  order after the top level.
- Globs are relative to the configuration's directory, `path.Match` per
  segment, `**` for any number of directories; a pattern without a slash
  matches the base name at any depth, and a pattern matching a directory
  covers everything below it.
- `tables` is kept as raw JSON for `hwdb`.
- Problems are positioned in the file. Syntax errors, wrong types and
  invalid values (severity, level, mode, glob) are errors; unknown keys,
  rule codes and calling conventions are warnings and the entry is
  ignored. A file with a syntax error is not used at all. The engine
  publishes problems on the configuration's URI (code `config`); the CLI
  prints them and exits 2 on an error.
- An open `.falcon-asm.json` buffer is the live configuration: edits apply
  to every open document at once; closing the buffer rereads the file.

### Target resolution (internal/project)

Highest precedence first:

- mode: `target.mode`, then the `-m` flag, else 64
- level: the in-file `falcon-asm: level=` comment, then `target.level`,
  then the editor's `falcon-asm.level` setting, then `-march=x86-64-vN`
- cpu: `target.cpu`, then a `-march` naming a CPU (not native, not an
  x86-64 level)
- cc: `target.cc` (`none` clears it), then inferred from the flags: 64-bit
  `sysv` (`ms` for a mingw compiler), 32-bit `cdecl` (`regparm(N)` for
  `-mregparm=N`, `stdcall` for `-mrtd`; the last flag wins), 16-bit none.
  Spellings are `internal/callconv`'s, which the configuration validator
  also uses. Per-file and per-function overrides are comments, resolved by
  the convention rules (see "Calling conventions" below).

`Target.Sources` says where each value came from, for messages.

### Suppressions

Inside any comment (`/* */`, `#`, `//` in assembly; `//` and `/* */` in C,
so inline asm findings are silenced from the C source):

- `falcon-asm: ignore=<code>[,<code>...]: <reason>` - findings starting
  on this line
- `falcon-asm: ignore-next=<code>: <reason>` - the next line holding code
- `falcon-asm: ignore-file=<code>: <reason>`
- `falcon-asm: ignore-begin=<code>: <reason>` ... `falcon-asm:
  ignore-end=<code>` - inclusive; nested regions pair innermost first; an
  unclosed begin runs to the end of the file
- `all` matches every rule; several items may share one comment, and
  other `key=value` items (`level=`, `cc=`, `reached=`) and free text after
  them are ignored. A space is allowed after a comma in the code list
  only, so `ignore=a because` still ends the list at `a`.
- The reason is the text after a colon directly following the code list.
  It is optional for the engine; the marker and the item must share one
  line, because the lexer cuts a block comment at each newline.

`unused-suppression` (default Info) reports a suppression that matched
nothing, but only for codes whose rule ran on the document (a rule turned
off is not a reason to report its suppressions), `all`, unknown codes and
an `ignore-end` without a begin. `ignore-without-reason` (default Off,
the one rule that is) reports every suppression item without a reason,
one finding per item, never for `ignore-end`; a project that wants
suppressions to stay honest turns it on in `.falcon-asm.json`. The runner
implements both: the reason check runs first, so a suppression of
`ignore-without-reason` is marked used before unused suppressions are
judged. A suppression never hides its own report. The syntax paragraph is
`lint.SuppressionSyntax`, shared by the generated docs/RULES.md header and
the rule's own documentation.

### Engine wiring

- The snapshot (per version and configuration generation) computes the
  target once and the lint result once (`snapshot.lintResult`), sharing
  its tokens and seeding its analysis into the target rules
  (`target.Seed`), so the mode rules cost nothing beyond the analysis the
  hover and the semantic tokens already used. The analysis configuration
  is `target.AnalysisConfig` of the resolved target, which is what makes
  the seeded result interchangeable with the rules' own.
- A toolchain result now bumps the generation like new macros do, so rules
  needing a listing see it; the listing is passed only when it describes
  the buffer's version.
- Published per document: lint findings (`Code` = rule code, `Source`
  `falcon-asm`, `codeDescription.href` pointing at this rule's section of
  docs/RULES.md), gas diagnostics, inactive regions, other files' gas
  diagnostics and configuration problems. A gas diagnostic on a line where
  a lint finding of Warning or Error starts or ends is dropped (item 78).
- `textDocument/codeAction` (capability `{"codeActionKinds":["quickfix"]}`):
  for every finding touching the range, one action per fix (the first is
  preferred) and one "Ignore <code> on this line" action that appends to an
  `ignore=` already on the line or adds `/* falcon-asm: ignore=code */`
  (assembly) or `// falcon-asm: ignore=code` (C) at its end, before a
  trailing line-continuation backslash when there is one. Rule fixes come
  first. `unused-suppression` offers no ignore action.
- `internal/analysis` keeps its API: mode tracking, symbols and the
  semantic tokens' deprecated painting still use it; only publishing moved
  to lint. `analysis.Config.InitialModeSource` names where the initial mode
  came from in messages, and `analysis.LevelOverrideTokens` reads the level
  comment from tokens already in hand.

### CLI

`falcon-asm` with no command (or only `-stdio`/`-clientProcessId`) is the
language server; `lint`, `rules`, `version` and `help [command]` are the
other modes. `--help`/`-h` (checked before any other flag, up to `--`)
prints the mode's help on stdout with status 0: the top level lists the
modes and the server flags, `lint --help`/`help lint` every lint flag
with its default, the exit codes and examples, `rules --help` the rules
flags. Each help prints the same `flag.FlagSet` the parser uses
(`serverFlagSet`, `lintFlagSet`, `rulesFlagSet`), and the CLI test walks
those sets, so a flag cannot be added without appearing in the help. A
flag error prints the flag package's message and a one-line pointer to
`--help` on stderr, status 2; so does an unknown command or a positional
argument to the server.

`falcon-asm lint [flags] [paths...]` (paths default `.`; flags may follow
paths): walks directories for `.S`/`.s` and, with `--c` (default on),
`.c/.h/.cc/.cpp/.cxx/.hpp/.hh`, skipping `build`, `.git` and
`node_modules`; a file named explicitly is linted whatever its extension.
Files are linted on a pool of `--jobs` workers (default GOMAXPROCS), and
each worker runs its file's assembler, so the toolchain is as parallel as
the rules. Compile flags come from `--compile-commands`, else `./` or
`./build/`, else the nearest database above each file; within a database
a file without its own entry borrows one from its directory or top-level
subdirectory (`toolchain.DB.Flags`, the engine's lookup).

The toolchain is on by default (`--no-toolchain`, or `--toolchain=false`,
turns it off; `--toolchain` is accepted as a no-op): every assembly file
with flags gets its macros, its listing and gas's diagnostics,
deduplicated as in the editor. A clean result has to mean clean, so the
CLI accounts for every listing rule that did not run. For an assembly
file, `lintText` records a `listingSkip` (the enabled `NeedsListing` rules
for that file's configuration, and a reason) when there is no listing:
no compile command, `--no-toolchain`, the compiler not found
(`ErrNoToolchain`), not an assembler source (`ErrNotAssembly`, a file
named explicitly), or a failed run (an `Assemble` error, or a result with
an error diagnostic and no listed line, which leaves no addresses). A
failed assembly that still listed lines keeps its partial listing and is
not counted, which is what the editor does; its gas errors are reported
as findings anyway. A file whose configuration turns every listing rule
off records nothing. At the end, stderr gets one note (distinct rule
count, file count, counts per reason, up to five file names, and what to
do) before the summary, and the summary carries the qualifier: "N files
linted, no problems (R listing rules skipped on F files)". Skips never
change the exit status. Machine output: `json` stays an array of findings
for compatibility; `json-full` is `{"findings", "skipped": [{"file",
"reason", "rules"}], "files"}` with stable reason keys
(`no-compile-command`, `no-toolchain`, `compiler-not-found`,
`assembly-failed`, `not-assembly`); `sarif` adds
`runs[0].invocations[0]` with `executionSuccessful` and one
`toolExecutionNotifications` entry per skipped file (descriptor
`listing-rules-skipped`, level `note`, the file as location, reason and
rules in `properties`). The note is printed on stderr for every format.

`--fix` applies the first fix of every finding whose edits overlap no fix
already taken (never ignore comments), writes the file, and reports what
is left after linting it again. `--changed` keeps the files git reports
changed against HEAD plus untracked ones, and lints everything outside a
repository. Output: `text` (`file:line:col: severity: message [code]`,
byte columns, notes and summary on stderr), `json` (file, line, column,
LSP range, code, severity, source, message, fixes), `json-full` or
`sarif` (2.1.0, rule metadata from the registry, `SRCROOT`-relative
locations, UTF-16 columns, fixes). `--severity` (default warning) sets
the exit threshold: 0 below it, 1 at or above, 2 for usage and
configuration errors and unreadable files. `falcon-asm rules` lists the
rules, `falcon-asm rules <code>` prints one, `--markdown` prints
docs/RULES.md, which `make rules-doc` regenerates and
`internal/rules/all`'s test keeps current.

## Layout rules and editor features (items 47-49, 79, 85)

- **Optional engine features.** `internal/server/extra.go` defines
  `CodeLensEngine`, `WorkspaceSymbolEngine` and `CallHierarchyEngine`. The
  server advertises `codeLensProvider` (`{"resolveProvider":false}`),
  `workspaceSymbolProvider` and `callHierarchyProvider` only when the engine
  implements them, and dispatches `textDocument/codeLens`,
  `workspace/symbol`, `textDocument/prepareCallHierarchy` and
  `callHierarchy/incomingCalls`/`outgoingCalls` from there; other engines
  answer MethodNotFound. workspace/symbol answers `[]`, never null.
- **Size budgets** (`internal/rules/layout`, `layout.Budgets`). A stage is
  the region from a start symbol to a `.org sym + N` or `. = sym + N`; each
  (section, symbol) gets one lens, bounded by its outermost move, placed on
  the symbol's line: `stage1: 440 / 510 bytes (70 free)`. Used is the target
  minus the padding the move inserts (`AsmResult.PadBytes`), so it is exact.
  The name is the last component of a dotted section, else the symbol. A
  section budget comes from `falcon-asm: budget=<bytes>` (or
  `budget=<section>:<bytes>`) and is measured with `SectionSizes`. When gas
  failed, only listed bytes are known: a lower bound, shown (`at least ...`)
  only when already over. Lenses, like listing hints, exist only while the
  last run describes the buffer. `budget-exceeded` (Error) replaces gas's
  "attempt to move .org backwards" with the stage, its budget and the
  overflow; the Error on the same line drops gas's own (item 78).
  `org-overlap` (Error, no listing needed) catches targets that go
  backwards or coincide with data between, relative to the same symbol;
  budget-exceeded leaves those lines to it.
- **Alignment.** `table-alignment` (Info): GDT/IDT bases found through
  `lgdt`/`lidt` and the `.word`/`.long` pseudo-descriptor, 8 bytes (16 for a
  64-bit IDT); `page-table-alignment` (Error): tables traced from the
  register moved into `%cr3` (straight-line, explicit writers only) and then
  through entries `.quad tbl + flags`. A label's offset comes from the
  listing; a section's alignment is the largest `.align`/`.balign`/
  `.p2align` in it (a lower bound). Misaligned in a section aligned at least
  as much is certain; misaligned otherwise is table-alignment's "depends on
  section placement"; aligned offsets are quiet. `simd-alignment` (Error)
  covers movdqa/movaps/vmovdqa and the non-temporal moves with a label
  operand, certain cases only. TSS alignment is not checked.
- **section-overlap** reads `*.ld`/`*.lds` from the file's directory up to
  the project root (`.git` or `.falcon-asm.json`, at most 8 levels), cached
  by mtime. Only `. = <number>;` and literal output section addresses are
  used; anything unsupported stops the parse. A script applies only if it
  names one of the file's non-standard sections literally; sections are
  placed first-match like ld, sizes are this object's (a lower bound).
- **On-type gas** (`toolchainOnType`, default off; VS Code
  `falcon-asm.toolchain.onType`). A change restarts a 400 ms timer
  (`Config.OnTypeDelay`); the run writes the buffer exclusively to
  `.falcon-asm-<pid>-<n>-<base>` beside the source, so quoted includes
  resolve, assembles it (no macro run: their locations would name the
  copy), removes it, files the copy's diagnostics under the source and
  applies the listing to that version. Save and close cancel a pending or
  running on-type run; shutdown cancels all, and the copy is removed on
  every path. The extension hides `**/.falcon-asm-*-*` from the explorer
  and the watcher.
- **Workspace symbols and call hierarchy** live in `internal/symbols`
  (`wsymbols.go`, `callhier.go`). Symbols: labels (private ones too),
  `.set`, `#define`, `.macro`; ranked exact, prefix, substring,
  subsequence, globals first; capped at 500. A function is a call target or
  a `.globl` label; calls are `call`/`lcall`, tail calls are `jmp`/`ljmp` to
  another function, marked in the item detail; a call counts for an item
  only when its name resolves to that item's definition from the calling
  file. Code before a file's first function is a File item.

## Performance lints (items 58-61)

Rules live in `internal/rules/perf`. Performance advice must not drown
correctness findings, so everything defaults to Info (div-by-constant to
Hint) except `missing-vzeroupper` and `inc-no-carry`, which are Warnings:
the first because the psABI expects clean upper state at function
boundaries and the penalty is large, the second because a CF read right
after `inc`/`dec` reads a stale carry, which is usually a bug. Codes:

- 58 `missing-vzeroupper`: `call`, `ret` or a `jmp` to a label outside the
  document where `flow` proves some ymm/zmm upper half dirty on every path
  (`State.UpperDirty`). Quiet for an in-document callee that uses ymm/zmm
  or runs no legacy SSE and never leaves the document, when the call's
  block writes ymm0-ymm7 (vector arguments), at a `ret` where only ymm0 is
  dirty (a `__m256` return value), and for AMD CPU targets. The fix inserts
  `vzeroupper` (after the statement's label, inline when the line holds an
  earlier statement, never between a prefix and its mnemonic).
- 59 `false-dependency`, `partial-flags-stall`, `inc-no-carry`. The first
  applies to popcnt (Sandy Bridge to Skylake) and lzcnt/tzcnt (Haswell to
  Skylake), and to no CPU target, a level target or an unknown CPU; the
  last writer of the destination (`Value.Def`) being a zeroing idiom or an
  immediate move exempts it. The flag rules read `State.FlagDef`: a CF
  reader whose ZF comes from an `inc`/`dec`. `inc-no-carry` takes the
  jcc/setcc/cmovcc immediately after the inc/dec unless the instruction
  just before the inc/dec wrote CF (`cmp; inc; jb` is an idiom);
  everything else is the Info merge note. `pushf` and other readers of
  non-status flags are excluded: they save flags, they do not consume CF.
- 60 `slow-loop` (not in 16-bit code, and only in hot code: the loop
  instruction's block lies on a CFG cycle that leaves it by an edge other
  than its own backward branch, or its function is called from a block on
  a cycle, from a hot function, or recursively; `rules/perf/hot.go`. A
  global routine with no caller in the document counts as cold, which is
  what keeps setup code quiet; inline asm templates are always reported,
  since their loops are in the C), `slow-enter` (a nesting level in any
  mode, level 0 only in 64-bit code; the fix uses `lea` because `enter`
  leaves the flags alone), `xchg-mem-lock`, `bt-mem`, `lea-three-component`
  (only for the sandybridge..icelake-server profiles), `div-by-constant`
  (a divisor `flow` knows; powers of two in every mode, other constants
  only outside 16-bit code).

Item 61 (latency/throughput/uop hover) is declined. uops.info is the only
measured source of that breadth, and neither the site, its XML download
page nor the paper states a licence for the data (the paper carries the
ACM "not for redistribution" notice), so derived tables cannot be shipped
in a GPL project. A hand-written table was considered and rejected: the
numbers would be unverifiable recollection rather than measurement, and a
hover that is confidently wrong about latency is worse than none. The rule
docs above cite their sources in prose instead. Revisit if uops.info (or
another measured source) publishes a redistributable licence.

## Calling conventions (items 31-37, stack frames of 48)

- **Model** (`internal/callconv`, pure, depends only on `flow`). A `Conv`
  names its integer argument and result registers, the callee-saved
  (`Preserved`) and caller-saved (`Clobbered`) register families, red zone,
  shadow space, call alignment, whether the callee pops, and whether the
  x87 CW and MXCSR control bits are callee-saved. sysv, ms, cdecl,
  stdcall, fastcall, regparm(N), custom and none. A custom convention
  holds only what its comment declares (`clobbers=`, `preserves=`, `args=`,
  `returns=`, with `all` for "every other general register"); a register
  in neither set is unknown and never reported. `cc=custom` may be left
  out when a list is given: lists without `cc=` mean custom (no other
  convention takes them), and lists next to `cc=` naming another
  convention are a `cc-annotation` finding, since one of the two is wrong. The model also parses the
  `falcon-asm:` items it owns (`cc=`, the lists, `handler[=errcode|
  noerrcode|service]`, `vector=N`); other items and trailing free text are
  ignored, as the suppression parser does.
- **Resolution** (`internal/rules/cc`, once per document through `Memo`).
  An annotation on a label's line, or above it with only comments and
  declaration directives (.globl, .type, .align, ...) between, belongs to
  the function starting there; any other annotation with `cc=` sets the
  file default (the first wins). A function without its own annotation
  takes the file default, else `Target.CC`, but only when the convention
  fits its entry mode (sysv/ms 64-bit, the i386 ones 32-bit), and a 16-bit
  function only takes a comment's: real-mode helpers have no standard
  convention. `custom` from the configuration declares nothing and counts
  as none. External callees follow the default for the call's mode.
- **Dataflow.** The engine's shared solution makes every register unknown
  after a call it cannot see, which would make every callee-saved register
  look written by every external call. When a file has a convention the
  model runs `flow.Analyze` again with `CallClobbers` set to its
  caller-saved set (about 1% of lint time over arch/x86 of Linux). Calls to
  in-document functions use flow's summaries either way.
- **Reaching definitions per path.** flow keeps one `Def` per statement
  and meets disagreeing paths into `DefMerged`. The rules walk back from a
  merged definition through predecessor exit states (`Result.Out`) until
  each path names its writer, which is what "on some path" means for
  callee-saved, argument and result registers. A merge made inside a block
  (a pop of a slot whose pushed value was merged, an opaque statement) is
  unknown. A call whose callee preserves the register is looked through to
  the definition reaching the call; an `int` or a call to code with no
  convention is opaque, but a write before it still counts, since the
  entry value is gone either way. Restores the stack model cannot pair
  (a pop on an unknown stack, popa, leave) are restores; a load from a
  stack slot, or from the operand a store before it saved the register
  to (in the function or the owner of a shared epilogue), is a spill
  reload.
- **Reads.** `rules/cc/reads.go` under-approximates which general
  registers an instruction reads (operands, addresses, the fixed implicit
  inputs of string, multiply/divide, loop, MSR and sign-extension
  instructions); pusha is not counted. `rules/flowrules/reads.go` grew an
  equivalent model concurrently; the two should move into `flow`.
- **Returns.** A ret or lret is a return only at the entry depth: with
  something still pushed it is a jump (`push $sel; push $addr; lretq`), and
  an lret on an unknown stack is assumed to be one.
- **Handlers.** Found by a move of `$label` (or a relocation expression
  whose only added label it is) to an absolute address below 0x400 (IVT,
  vector = address / 4; vectors other than 00-0F, 1C and 70-77 are
  software services), by `mov $label`/`lea label` in a function that runs
  lidt (a table of addresses there gives vectors by index), or by
  `falcon-asm: handler`. Pushes of labels are not installs. Service
  handlers are exempt from handler-preserve, and `lret $n` (the flag-
  discarding INT hook idiom) from handler-ret.
- **Stack.** The 64-bit checks assume rsp is 8 past a 16-byte boundary at
  entry, so they run only for functions something calls or that are
  global (not `_start`/`start`, not a numeric local label: `call 1f`
  tricks). Depth comes from `State.Stack`, or `FrameDepth` for rbp-based
  operands; a realigned stack has an unknown depth and is skipped.
