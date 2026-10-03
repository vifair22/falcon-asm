# falcon-asm for VS Code

A thin VS Code client for the `falcon-asm` language server: hover,
diagnostics and semantic highlighting for GNU as (GAS) x86 assembly in AT&T
syntax, in `.S` and `.s` files, plus hover, diagnostics and highlighting for
inline `asm` statements in C and C++.

The extension itself knows almost nothing about x86. It ships:

- a minimal TextMate grammar (`syntaxes/falcon-asm.tmLanguage.json`) that
  paints comments, cpp directives, strings, numbers, registers, labels,
  directives and the statement mnemonic the instant a file opens;
- an injection grammar (`syntaxes/falcon-asm-inline.tmLanguage.json`) that
  highlights the template strings of `asm` statements in C and C++ (see
  [Inline asm in C and C++](#inline-asm-in-c-and-c));
- a language client that starts the `falcon-asm` binary over stdio. The
  server's semantic tokens then repaint the file with real knowledge:
  prefixes vs mnemonics, AT&T suffixes (`movw` paints `mov` and `w`
  differently), privileged instructions, macro uses, label references.

Everything language-aware (instruction tables, hover text, token
classification) lives in the Go server in this repository. Upgrading the
server binary upgrades the extension's behaviour; the extension only needs
rebuilding when the grammar, settings or client code change.

## Install

Build and install the server first (from the repository root):

```sh
make install            # installs falcon-asm into ~/.local/bin
```

Then build and install the extension:

```sh
cd editors/vscode
npm ci && npm run package
code --install-extension falcon-asm-*.vsix
```

`make vscode` at the repository root runs `npm ci`, the tests and
`npm run package` in one go.

## Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `falcon-asm.serverPath` | `falcon-asm` | The server binary. A bare name is looked up on `PATH`; `~/` is expanded; a relative path is resolved against the first workspace folder. Left at the default, a few install locations are tried after `PATH` (below). |
| `falcon-asm.inlineAsm.enabled` | `true` | Offer C and C++ files to the server for hover and diagnostics inside `asm` statements. Off, the server only sees `falcon-asm` files. The highlighting grammar is unaffected. |
| `falcon-asm.toolchain.enabled` | `true` | Run the compiler and assembler from `compile_commands.json` on open and save: assembler diagnostics, macro values, listing hints. |
| `falcon-asm.toolchain.onType` | `false` | Also assemble the unsaved buffer after a 400 ms pause in typing, so assembler diagnostics, listing hints and size budget lenses follow the text without a save. The buffer is written to a hidden `.falcon-asm-<pid>-<n>-<file>` beside the source (so quoted includes resolve) and removed when the run ends; the extension hides that pattern from the explorer and the file watcher. Needs the toolchain. |
| `falcon-asm.inlayHints.listing` | `true` | Listing inlay hints: section offset and encoded bytes after each instruction line, padding size on `.org` lines. Needs the toolchain. |
| `falcon-asm.inlayHints.registers` | `false` | Register values the dataflow analysis tracks, as inlay hints at `int`, `in`/`out`, `rdmsr`/`wrmsr`, `cpuid`, `lgdt`, control register writes and far jumps: `ah=42h dl=80h`, `dx=3FDh`, `ecx=C0000080h (EFER)`. Values that differ by path are joined with `\|`. Does not need the toolchain. |
| `falcon-asm.level` | `""` | Target x86-64 level for level diagnostics: `v1` to `v4`. Empty defers to a `falcon-asm: level=vN` comment in the file, then `-march` from the compile database. |
| `falcon-asm.compileCommands` | `""` | `compile_commands.json` to use instead of discovering one. `~/` is expanded; a relative path is resolved against the first workspace folder. |
| `falcon-asm.trace.server` | `off` | `messages` or `verbose` logs the JSON-RPC traffic to the "falcon-asm" output channel. |

Every setting except `trace.server` restarts the server when changed: the
server reads its options once, at initialization. They reach it as
`initializationOptions`:

```json
{
  "toolchain": true,
  "toolchainOnType": false,
  "listingHints": true,
  "registerHints": false,
  "level": "",
  "compileCommands": ""
}
```

`toolchain` is `falcon-asm.toolchain.enabled`, `toolchainOnType` is
`falcon-asm.toolchain.onType`, `listingHints` is
`falcon-asm.inlayHints.listing`, `registerHints` is
`falcon-asm.inlayHints.registers`. `level` is always one of `""`, `"v1"` to
`"v4"` (anything else is sent as `""`). `compileCommands` is `""` (discover)
or the path with `~/` expanded, made absolute against the first workspace
folder when one is open. Every key is always present.

The extension also sets `editor.semanticHighlighting.enabled: true` for
`[falcon-asm]` files, since without semantic tokens only the base grammar
is visible.

VS Code started from a desktop launcher sees the session's `PATH`, not your
shell's, and that often lacks `~/.local/bin` (KDE Plasma, for one). So when
`falcon-asm.serverPath` is left at the default and `PATH` has no
`falcon-asm`, the extension also tries, in order: `~/.local/bin` (where
`make install` puts it), `$GOBIN`, `bin` under each `$GOPATH` entry, and
`~/go/bin` (where `go install` puts it). An explicit path or a different
name is used exactly as given, with no fallback.

If the binary still cannot be found, an error notification names where it
looked and offers **Open Settings**; set `falcon-asm.serverPath` to the
binary's full path.

Command palette: **falcon-asm: Restart Language Server** stops the server
and starts it again, re-reading `falcon-asm.serverPath`. Use it after
installing a new server build.

## Inline asm in C and C++

With `falcon-asm.inlineAsm.enabled` on (the default), the extension also
activates for `c` and `cpp` documents and hands them to the server, which
provides hover inside `asm` templates and operand lists and diagnostics for
the instructions in them. Every other feature is suppressed for C and C++
on the client side, so completion, semantic tokens, symbols, folding,
go-to-definition, references, rename and inlay hints from clangd (or
whichever C extension you use) are never shadowed or duplicated. The server
also scopes its semantic tokens to `falcon-asm` only; the client-side
filter is a second line of defence.

Highlighting comes from `syntaxes/falcon-asm-inline.tmLanguage.json`,
injected into `source.c` and `source.cpp`. It takes over each
`asm`/`__asm__`/`__asm` statement (with any of `volatile`, `__volatile__`,
`inline`, `goto`) from the C grammar and paints:

- in the template strings (before the first `:`): mnemonics at statement
  start (after the opening quote, `\n`, `\n\t` or `;`), prefixes,
  labels, directives, `%%reg` registers (and `%reg` in basic asm),
  `%0`/`%[name]`/`%k0`/`%l[label]`/`%=` operand references, `$`
  immediates, numbers and `1f`/`1b` local label references;
- after the first `:`: constraint and clobber strings as plain strings,
  `[name]` operand names, and the C operand expressions through the C
  grammar.

It has to own the whole statement because the bundled C/C++ grammars give
every string in an `asm` statement the same scopes
(`meta.asm.c > string.quoted.double.c > meta.embedded.assembly.c`; in C++
`meta.asm.cpp > string.quoted.double.cpp > meta.embedded.assembly`), so a
rule injected into those strings cannot tell a template from `"memory"` or
`"=r"`. Known limit: the opening parenthesis must be on the same line as
the `asm` keyword.

The grammar does not depend on the setting: turning
`falcon-asm.inlineAsm.enabled` off stops the server seeing C files but
keeps the highlighting.

## Conflicts with other assembly extensions

Other assembly extensions (x86 and x86_64 Assembly, ASM Code Lens, the
Arm/RISC-V ones, and so on) often claim `.s` and `.S` too, and VS Code
picks one language per file. If files open with a different language mode,
pin them in your settings:

```json
"files.associations": {
  "*.S": "falcon-asm",
  "*.s": "falcon-asm"
}
```

Scope the association to a workspace (`.vscode/settings.json`) if other
projects need a different assembler's syntax.

## Comments

**Toggle Line Comment** inserts `#`, the GAS comment character for x86.
In `.S` files run through cpp, a `#` followed by a cpp directive word
(`#include`, `#define`, `#if`...) is a directive, not a comment; the grammar
follows the same rule.

## Development

```sh
npm ci
npm run build       # typecheck (tsc --noEmit) and bundle to out/extension.js
npm run watch       # rebundle on change
npm test            # vitest: settings, path resolution, grammars, manifest
npm run package     # production bundle and falcon-asm-<version>.vsix
```

`src/server-command.ts` (server path resolution and the install-location
fallback) and `src/settings.ts` (document selector, initializationOptions,
which settings restart the server) have no `vscode` import, so they are
tested directly. `test/tm.ts` is a small TextMate interpreter used to
tokenize the falcon-bios fixtures under `testdata/falcon-bios/` and check
the base grammar's scopes. It also models injections, so
`test/inline-grammar.test.ts` runs the inline asm grammar against a stand-in
for the C grammar on lines from airies-boot's `falcon-core` (skipped when
that checkout is not next to this one), and, when a VS Code install is
found, checks the facts the injection relies on in the real bundled C/C++
grammars (`VSCODE_CPP_SYNTAXES` points at another install). It relies on
the grammars using only regex syntax that JS and Oniguruma read the same
way, which the tests enforce.

`@types/node` is not a dependency; the few Node signatures the code uses are
declared in `types/node-shim.d.ts`.

The semantic token legend the server advertises (see `docs/DESIGN.md`) is
mapped to TextMate scopes in `package.json` under `semanticTokenScopes`, so
any theme colors it. A test fails if the legend gains a type that is not
mapped there.

## License

GPL-3.0-or-later, the same as the rest of falcon-asm. `npm run package`
copies the repository's `LICENSE` into this directory for the `.vsix`.
