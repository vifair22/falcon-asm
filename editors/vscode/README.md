# falcon-asm for VS Code

A thin VS Code client for the `falcon-asm` language server: hover and
semantic highlighting for GNU as (GAS) x86 assembly in AT&T syntax, in `.S`
and `.s` files.

The extension itself knows almost nothing about x86. It ships:

- a minimal TextMate grammar (`syntaxes/falcon-asm.tmLanguage.json`) that
  paints comments, cpp directives, strings, numbers, registers, labels,
  directives and the statement mnemonic the instant a file opens;
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
| `falcon-asm.serverPath` | `falcon-asm` | The server binary. A bare name is looked up on `PATH`; `~/` is expanded; a relative path is resolved against the first workspace folder. Changing it restarts the server. |
| `falcon-asm.trace.server` | `off` | `messages` or `verbose` logs the JSON-RPC traffic to the "falcon-asm" output channel. |

The extension also sets `editor.semanticHighlighting.enabled: true` for
`[falcon-asm]` files, since without semantic tokens only the base grammar
is visible.

If the binary cannot be found, an error notification names the path it
looked for and offers **Open Settings**. Note that VS Code started from a
desktop launcher may not see the `PATH` of your shell; if `falcon-asm` is
on `PATH` in a terminal but not found by the editor, set
`falcon-asm.serverPath` to the full path (for example
`~/.local/bin/falcon-asm`).

Command palette: **falcon-asm: Restart Language Server** stops the server
and starts it again, re-reading `falcon-asm.serverPath`. Use it after
installing a new server build.

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
npm test            # vitest: settings resolution, grammar and manifest checks
npm run package     # production bundle and falcon-asm-<version>.vsix
```

`src/server-command.ts` holds the server path resolution and has no `vscode`
import, so it is tested directly. `test/tm.ts` is a small TextMate
interpreter used to tokenize the falcon-bios fixtures under
`testdata/falcon-bios/` and check the base grammar's scopes; it relies on
the grammar using only regex syntax that JS and Oniguruma read the same way,
which `test/grammar.test.ts` enforces.

`@types/node` is not a dependency; the few Node signatures the code uses are
declared in `types/node-shim.d.ts`.

The semantic token legend the server advertises (see `docs/DESIGN.md`) is
mapped to TextMate scopes in `package.json` under `semanticTokenScopes`, so
any theme colors it. A test fails if the legend gains a type that is not
mapped there.

## License

GPL-3.0-or-later, the same as the rest of falcon-asm. `npm run package`
copies the repository's `LICENSE` into this directory for the `.vsix`.
