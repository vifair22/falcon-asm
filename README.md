# falcon-asm

A language server for GNU as (GAS) x86 assembly in AT&T syntax. Written for
the cpp-preprocessed `.S` files in airies-boot (falcon-bios real mode,
protected mode and long mode code), but it knows nothing project-specific and
works on any GAS x86 source.

What it does:

- **Hover** on mnemonics, registers, directives, prefixes, labels and numbers.
  Short and to the point: summary, operand forms, ISA set and x86-64 level
  (`x86-64-v3 (BMI2)`), privilege (`CPL 0`), mode restrictions (`64-bit only`)
  and flags read/written. AT&T size suffixes resolve (`movw` = `mov` + `w`)
  without mis-splitting real mnemonics (`setb`, `movsb`, `cwtl`). A `#define`d
  constant shows its definition and, when it is a constant expression over
  numbers and other macros, its value in hex and decimal (`SEL_CODE32` ->
  `0x8`), plus the header it comes from.
- **Semantic highlighting**: mnemonic vs. size suffix, prefixes, registers,
  immediates, label definitions and references, directives, cpp directives
  and macros (including those from included headers, once the toolchain has
  run). Privileged (CPL 0) instructions carry the `defaultLibrary` modifier;
  instructions and registers invalid in the current `.code16/32/64` mode
  carry `deprecated`.
- **Diagnostics**: instructions and registers invalid in the current mode,
  instruction forms above a target x86-64 level (`-march=x86-64-v3`, a
  `falcon-asm: level=v3` comment, or the editor setting), and the real
  assembler's errors and warnings from the project's own compiler, run on
  open and save in the background. Errors inside included files are reported
  on those files too. `#if 0` bodies are faded.
- **Listing hints**: after each instruction or data line, its offset in the
  section and its encoded bytes (`+0x1a3  b8 01 24`); on `.org` and `. = expr`
  lines, how many bytes the move pads (`pads 70 bytes`), which is the space
  left in a boot sector at `. = _start + 510`.
- **Navigation**: go to definition, find references and rename for labels,
  `.set`/`.equ` symbols, `.macro` and `#define` names, across open files,
  their `#include`s and the `.S`/`.s` files of the workspace; local `1f`/`1b`
  labels resolve the way gas does. Outline and folding ranges.
- **Completion**: registers valid in the current mode after `%`, directives
  after `.`, mnemonics and prefixes at statement start, labels and macros in
  operands.
- **Inline asm in C/C++**: hover inside `asm` statements (instructions and
  registers in the template, `%0`/`%[name]` operands, constraints such as
  `"=r"`, clobbers) and mode/level diagnostics using the C file's own
  compile flags. Everything else in C files is left to clangd.
- Full coverage of the system instructions a boot loader lives on (`lgdt`,
  `ljmp`, `iret`, `inb`/`outb`, `rdmsr`, `rep movsb`, ...), which general
  purpose x86 tooling tends to miss.

The toolchain features read `compile_commands.json` (at the workspace root,
in `build/`, or found walking up from the file) for each file's compiler and
flags. Without a database, or without the compiler installed, they are
silently off and everything else still works.

It is a single static binary with no runtime dependencies and speaks LSP over
stdio, so any LSP client can drive it. A thin VS Code client lives in
`editors/vscode`.

## Building

Requires Go (see `go.mod` for the version). The module is stdlib-only.

```sh
make            # build/bin/falcon-asm
make test       # go test -race ./...
make coverage   # coverage profile + 80% statement gate
make analyze    # go vet, gofmt check, golangci-lint if installed
make check      # ascii-check over the tree
make bench      # Go microbenchmarks into build/bench/bench.txt
make lspbench   # end-to-end LSP benchmark into build/bench/lspbench.{txt,json}
make rules-doc  # regenerate docs/RULES.md from the rule registry (a test checks it)
make install    # installs falcon-asm into ~/.local/bin (PREFIX=... to change)
```

`make VARIANT=debug` builds without stripping or `-trimpath`. The version
string is `release_version` plus build stamp and variant, e.g.
`0.1.0_20261003.0518.release`; `falcon-asm --version` prints it.

## Benchmarking

`make lspbench` starts the built server and drives it over stdio the way an
editor does (`tools/lspbench`): the falcon-bios fixtures, typing into 20k-
and 100k-line files, a 500-file workspace and process cold start, with
per-request p50/p95/p99/max and peak memory; the linter's paths
(edit-to-diagnostics latency, code actions, code lenses, register hints,
hardware hovers and their length, workspace symbols, the call hierarchy);
and `falcon-asm lint` wall time and peak memory. `-root` adds a real
project with the toolchain on; `-json` and `-compare old.json new.json`
track changes. `make bench` runs the Go microbenchmarks. The method,
scenarios, the before and after numbers of the performance pass and of the
linter pass, and the remaining hot spots are in
[docs/BENCHMARKS.md](docs/BENCHMARKS.md).

## Linting

The same binary lints without an editor, for CI and pre-commit hooks:

```sh
falcon-asm lint                      # everything under ., gcc-style output
falcon-asm lint --format sarif src   # SARIF 2.1.0 for code scanning
falcon-asm lint --fix                # apply quick fixes in place
falcon-asm lint --changed            # only files changed against git HEAD
falcon-asm rules [code]              # the rule list, or one rule's documentation
```

Exit status is 0 when nothing at or above `--severity` (default `warning`)
was found, 1 when something was, 2 on a usage or configuration error. Rules
are configured per project and per path in `.falcon-asm.json` and silenced
in the source with `falcon-asm: ignore=<code>` comments; the editor and the
CLI read the same file and report the same findings. Every rule is
documented, with examples, in [docs/RULES.md](docs/RULES.md).

```json
{
  "rules": { "level-outside": "off", "mode-register": "error" },
  "overrides": [ { "paths": ["legacy/**"], "target": { "mode": 16 } } ],
  "target": { "level": "v3" }
}
```

## Editor setup

The server must be on `PATH` (`make install`) or configured by absolute path.

### VS Code

```sh
make vscode     # npm ci, tests, packages editors/vscode/falcon-asm-*.vsix
code --install-extension editors/vscode/falcon-asm-*.vsix
```

The extension registers the `falcon-asm` language for `.S` and `.s` files.
If another assembly extension also claims those extensions, pin the
association in settings:

```json
"files.associations": { "*.S": "falcon-asm", "*.s": "falcon-asm" }
```

Settings: `falcon-asm.serverPath` (default `falcon-asm`, resolved via PATH),
`falcon-asm.toolchain.enabled` (run the compiler on open/save),
`falcon-asm.inlayHints.listing` (offset/bytes hints),
`falcon-asm.inlayHints.registers` (tracked register values, off by default), `falcon-asm.level`
(target level override, `v1`..`v4`), `falcon-asm.compileCommands` (explicit
database path), `falcon-asm.inlineAsm.enabled` (serve C/C++ files for inline
asm) and `falcon-asm.trace.server` (`off`, `messages`, `verbose`). Changing
any but the last restarts the server. Command: `falcon-asm: Restart Server`.
See `editors/vscode/README.md`.

### Other clients: initializationOptions

The server reads its options once, from `initializationOptions`. Every key
is optional:

```json
{ "toolchain": true, "listingHints": true, "registerHints": false, "level": "", "compileCommands": "" }
```

`level` is `""` (use the file comment or `-march`) or `v1`..`v4`;
`compileCommands` is a path to a `compile_commands.json` (or a directory
holding one), relative paths resolving against the workspace root.

Semantic tokens are registered with a document selector for the
`falcon-asm` language id only, so C files keep their own highlighting. A
client that honours static registration selectors must therefore open
assembly files with that language id (the CLion mapping below does); one
that ignores them gets tokens for any assembly file, and an empty stream for
C files.

### CLion / other JetBrains IDEs

Install the **LSP4IJ** plugin, then *Settings > Languages & Frameworks >
Language Servers > +*:

- Name: `falcon-asm`
- Command: `falcon-asm` (or the absolute path)
- Mappings > File name patterns: `*.S`, `*.s`, language id `falcon-asm`

Every feature works through LSP4IJ; the options described above go in the
server's initialization options as JSON.

### Neovim (0.11+)

```lua
vim.lsp.config('falcon_asm', {
  cmd = { 'falcon-asm' },
  filetypes = { 'asm' },
  root_markers = { '.git' },
})
vim.lsp.enable('falcon_asm')
```

Neovim maps `.S`/`.s` to the `asm` filetype by default.

## Layout

```
cmd/falcon-asm/      entry point: the server, `lint` and `rules`
internal/jsonrpc/    Content-Length framing and connection
internal/protocol/   the LSP types in use
internal/docstore/   open documents, incremental sync, UTF-16 positions
internal/lexer/      GAS + cpp tokenizer
internal/isa/        instruction, register and directive tables, mnemonic resolution
internal/analysis/   mode tracking, mode and level diagnostics, document symbols
internal/symbols/    workspace symbol index: definition, references, rename, outline, folding
internal/toolchain/  compile database, gcc -dM macros, assembling with a listing
internal/inlineasm/  asm statements in C: template extraction and mapping, operand hover
internal/completion/ completion items
internal/constexpr/  integer constant expressions for macro values
internal/hover/      hover content
internal/semtok/     semantic tokens
internal/lint/       rule registry, .falcon-asm.json, suppression comments, the runner
internal/rules/      the lint rules, one package per roadmap area; rules/all imports them
internal/project/    shared by server and CLI: file target resolution, linting a document
internal/engine/     per-document state, configuration, toolchain runs, feature wiring
internal/server/     method dispatch and lifecycle
internal/benchdata/  shared benchmark inputs built from the fixtures
tools/xedgen/        generator for internal/isa/data/xed.json
tools/lspbench/      end-to-end LSP benchmark driving the server binary
editors/vscode/      VS Code client, base TextMate grammar, inline asm injection grammar
testdata/falcon-bios real .S fixtures from airies-boot
```

`docs/DESIGN.md` has the package contracts and the reasoning behind them.

## Instruction data

`internal/isa/data/system.json` is hand written and covers the system,
privileged, I/O, string and control-transfer instructions; it wins over the
generated data for any name in both. `internal/isa/data/xed.json` is
generated by `tools/xedgen` from Intel XED and asm-lsp's x86 data and is
committed, so a normal build needs neither upstream. To regenerate, see
`tools/xedgen/README.md` and run `make xed-data`. Attribution is in `NOTICE`.

## License

GPL-3.0-or-later. See `LICENSE`. Third-party data attribution in `NOTICE`.
