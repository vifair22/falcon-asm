# falcon-asm benchmarks

How falcon-asm's performance is measured, what the numbers were before and
after the performance pass, which optimisations were made (and which were
tried and dropped), and where the time goes now.

Everything here is reproducible from the repository: the tools are
`make bench` (Go microbenchmarks) and `make lspbench` (the real server
binary driven over stdio by `tools/lspbench`). The raw outputs of both runs
are committed under `docs/bench/`.

## Environment

| | |
|---|---|
| CPU | AMD Ryzen 9 5900XT 16-Core Processor (16 cores, 32 threads) |
| Memory | 32 GB |
| Kernel | Linux 7.2.7-cachyos-gentoo-dist-mono (Gentoo) |
| Go | go version go1.26.7-X:nodwarf5 linux/amd64 |
| Server build | `make server` (release: `-trimpath -ldflags '-s -w'`) |
| Date | 2026-10-03 |

The machine was otherwise idle. Numbers from another machine are only
comparable with each other; `-compare` and benchstat exist for that.

## Method

Two layers, because they answer different questions.

**Microbenchmarks** (`go test -bench`) time one function on fixed input:
the lexer, the analysis, the line index, the engine's edit path. They are
precise, allocation-aware and good for attributing a change to a function.
Inputs come from `internal/benchdata`: the 14 falcon-bios fixtures, and
large documents made by concatenating them (20k and 100k lines).

**End to end** (`tools/lspbench`) starts `build/bin/falcon-asm`, speaks LSP
over its stdin/stdout like an editor, and times every request from the
moment its frame is written to the moment the reply's frame is fully read
(before the client decodes it, so the client's own JSON work is not charged
to the server). That includes framing, JSON in both directions, the queue,
the engine and the pipe, which is what a user waits for. It reports count,
p50, p95, p99 and max per request kind (nearest-rank percentiles, so every
reported value was actually observed), mean reply size, and peak resident
memory (`VmHWM` from `/proc/<pid>/status`, read just before shutdown).

Generated inputs are deterministic. Large files concatenate the fixtures in
name order, appending `_c<N>` to every label a copy defines, so labels stay
unique and navigation queries have a single answer.

## How to run

```sh
make bench                         # all microbenchmarks -> build/bench/bench.txt
make bench BENCH=Change BENCH_COUNT=10   # one family, enough runs for benchstat
make lspbench                      # end to end -> build/bench/lspbench.{txt,json}
make lspbench LSPBENCH_ARGS="-root ../airies-boot"        # plus the real tree
make lspbench LSPBENCH_ARGS="-scenarios typing -sizes 20000 -edits 200"
make lspbench LSPBENCH_ARGS="-scenarios features,diagnostics,cli -root ../airies-boot"
```

Knobs: `BENCH` (a `-bench` regexp), `BENCH_COUNT`, `BENCH_TIME`,
`LSPBENCH_ARGS`. CI runs `make bench BENCH_TIME=200ms` as the non-blocking
`bench:go` job and keeps `build/bench/` as an artifact.

Comparing runs:

```sh
build/bin/lspbench -compare docs/bench/baseline-lspbench.json build/bench/lspbench.json
go run golang.org/x/perf/cmd/benchstat@latest docs/bench/baseline-bench.txt build/bench/bench.txt
```

benchstat is fetched and run, never added to `go.mod`; the module stays
stdlib-only. Use `BENCH_COUNT=6` or more for its confidence intervals.

### lspbench flags

| Flag | Default | Meaning |
|---|---|---|
| `-server` | `build/bin/falcon-asm` | server binary |
| `-server-arg` | | extra server argument, repeatable; `{scenario}` expands to the scenario name |
| `-scenarios` | `all` | comma list of `cold,fixtures,features,diagnostics,typing,workspace,tree,cli`; `all` includes `tree` only with `-root` (and `cli` lints the root only with it) |
| `-fixtures` | `testdata/falcon-bios` | fixture directory |
| `-root` | | a real project with `compile_commands.json`, for `tree` (only read) |
| `-sizes` | `20000,100000` | typing scenario file sizes in lines |
| `-edits` | `500` | edits per typing phase |
| `-ws-files` | `500` | files in the generated workspace |
| `-cold-runs` | `10` | server starts in `cold` |
| `-rounds` | `3` | repetitions of the per-file sweeps |
| `-timeout` | `2m` | longest wait for one reply |
| `-settle` | `3s` | quiet period after opening the tree |
| `-diag-edits` | `5` | edits per fixture, and per phase on the large files, in `diagnostics` (and in `workspace`'s edit timing) |
| `-debounce` | `150ms` | the server's diagnostics debounce, subtracted in the `edit->publish-minus-debounce` metrics |
| `-json` | | also write the report as JSON |
| `-compare old new` | | print the deltas between two JSON reports |

### Profiling

The server accepts two flags that are deliberately left out of `-help`:
`--cpuprofile=path` and `--memprofile=path`. The CPU profile covers the
whole session; the heap profile is written at exit after a GC. With
lspbench, one profile per scenario:

```sh
build/bin/lspbench -scenarios typing -sizes 100000 \
    -server-arg=--cpuprofile=build/prof/{scenario}.cpu \
    -server-arg=--memprofile=build/prof/{scenario}.mem
go tool pprof -top build/bin/falcon-asm build/prof/typing-100k.cpu
```

Microbenchmarks profile the usual way (`go test -bench X -cpuprofile`).

## Scenarios

**cold**: 10 fresh processes. `spawn->initialize` is process creation to
the initialize reply. `spawn->semanticTokens-20k` is process creation to the
first semantic tokens of a 20k-line file opened right after initialize,
which includes loading the instruction tables.

**fixtures**: the size of file the server is written for. Opens all 14
falcon-bios fixtures (toolchain off, root at the fixture directory),
requests semantic tokens for each (3 rounds), hovers every mnemonic and
register of `pmode.S`, asks for completion inside every mnemonic and
register of `pmode.S` and `stage2.S` (up to 300 each), definition on every
label reference and references on every label definition of both, then
documentSymbol, foldingRange and inlayHint on every fixture (3 rounds). One
unrecorded semantic tokens request first, so the table load is not charged
here.

**typing-20k, typing-100k**: what an editor sends while someone types into
the middle of a large file. 500 single-character `didChange` edits typing
ordinary instruction lines; after each word character or trigger, a
completion request; every 10th edit a full semantic tokens request, and 5
edits later a delta request when the server offers deltas; every 25th edit
documentSymbol and foldingRange. Each request waits for its reply, so its
latency includes the edits before it. `open+semanticTokens` is didOpen to
the first tokens.

Then the **burst**: the same 500 edits sent without waiting, each followed
by a semantic tokens request, and each new request cancelling the previous
one with `$/cancelRequest`, which is what VS Code does. `burst-drain` is the
time from the first edit to the last reply: how long highlighting lags
behind the keyboard. `burst-answered`/`burst-cancelled` count replies.

**workspace**: 500 generated `.S` files (fixture copies), file 0 defining a
`.globl` label every other file calls. Root at the directory, two files
open. `references-first` is the first query, which walks and parses the
whole workspace; then 60 each of references (501 locations), definition and
operand completion.

**features** (added with the linter): the 14 fixtures with register
hints on, opened and waited until their diagnostics are published, as a
user reads a file. Hover on every mnemonic, register, immediate, number
and label operand of `boot.S`, `pmode.S` and `stage2.S` (hardware text
included; the report also counts hover lengths), a code action for every
published diagnostic and a light-bulb request (`triggerKind` 2, empty
context) on every fifth line, code lenses, inlay hints with register
hints, seven `workspace/symbol` queries, `prepareCallHierarchy` on every
label definition of those three files plus incoming and outgoing calls of
each item, and `hover-after-edit`: a register operand hovered right after
an edit, before the version's lint, so it pays for the version's parse,
dataflow and hardware index.

**diagnostics** (added with the linter): edit to `publishDiagnostics` of
that version, which includes the 150 ms debounce and every lint rule; the
`-minus-debounce` series is the server's own work plus the pipe. On each
fixture (one server, 5 edits each), then on the 20k- and 100k-line files
(one server each): open to first publish, 5 edits to publish, then 5
edits each followed, just after the debounce, by a completion
(`completion-during-lint`: a keystroke while the lint of the previous
version runs), then 5 edits each followed at once by a light-bulb code
action (`codeAction-after-edit`), then 5 by a register hover
(`hover-after-edit`). Peak RSS per server.

**cli** (added with the linter): `falcon-asm lint --format json` run 3
times over the root (`-root`), again with `--toolchain`, and over a
generated 500-file workspace, timing the process and reading its peak RSS
(`getrusage` maxrss). Exit status 1 (findings) is expected.

**tree**: a real project with the toolchain on (here
`/home/vifair/git/home/airies-boot`, 18 assembly sources from its
`compile_commands.json`). Opens them all, waits until no diagnostics have
been published for 3 s, then 3 rounds of `didSave` per file, timing save to
the toolchain's diagnostics publish, followed by the listing inlay hints
and (since the linter pass) the size budget code lenses.

**workspace** gained, with the linter, `edit->publish`: 5 edits to the
second open file, each waited out to its diagnostics, whose symbol rules
consult the whole 500-file workspace.

## Results: end to end

Baseline: `docs/bench/baseline-lspbench.json`. Final:
`docs/bench/final-lspbench.json`. Full table with p95 and max:
`docs/bench/compare-lspbench.txt`. Times in milliseconds.

| Metric | Base p50 | Final p50 | Base p95 | Final p95 | Change (p50) |
|---|---:|---:|---:|---:|---:|
| cold/spawn->initialize | 11.487 | 1.581 | 13.275 | 2.211 | -86% |
| cold/spawn->semanticTokens-20k | 48.936 | 23.213 | 51.593 | 23.519 | -53% |
| fixtures/semanticTokens/full | 0.167 | 0.076 | 0.406 | 0.217 | -55% |
| fixtures/definition | 0.027 | 0.033 | 0.104 | 0.103 | +6 us |
| fixtures/references | 0.032 | 0.037 | 0.074 | 0.059 | +5 us |
| fixtures/hover | 0.029 | 0.029 | 0.043 | 0.055 | 0 |
| fixtures/completion | 0.142 | 0.066 | 0.638 | 0.195 | -54% |
| fixtures/documentSymbol | 0.098 | 0.040 | 0.249 | 0.069 | -59% |
| fixtures/foldingRange | 0.044 | 0.027 | 0.114 | 0.046 | -39% |
| fixtures/inlayHint | 0.020 | 0.025 | 0.049 | 0.034 | +5 us |
| typing-20k/open+semanticTokens | 35.341 | 20.803 | | | -41% |
| typing-20k/completion | 12.541 | 2.936 | 26.727 | 5.435 | -77% |
| typing-20k/semanticTokens/full | 16.055 | 7.307 | 31.655 | 8.960 | -54% |
| typing-20k/semanticTokens/full/delta | (none) | 5.987 | | 7.987 | new |
| typing-20k/documentSymbol | 9.473 | 3.747 | 22.651 | 4.159 | -60% |
| typing-20k/foldingRange | 3.160 | 0.479 | 4.572 | 0.595 | -85% |
| typing-20k/burst-drain | 12479.389 | 53.792 | | | -99.6% |
| typing-100k/open+semanticTokens | 174.863 | 92.337 | | | -47% |
| typing-100k/completion | 56.996 | 12.975 | 120.065 | 23.404 | -77% |
| typing-100k/semanticTokens/full | 74.625 | 34.517 | 153.408 | 43.799 | -54% |
| typing-100k/semanticTokens/full/delta | (none) | 28.265 | | 39.221 | new |
| typing-100k/documentSymbol | 48.615 | 21.025 | 116.309 | 23.986 | -57% |
| typing-100k/foldingRange | 12.348 | 1.900 | 17.760 | 2.209 | -85% |
| typing-100k/burst-drain | 59272.536 | 216.253 | | | -99.6% |
| workspace/references-first | 34.043 | 13.487 | | | -60% |
| workspace/references | 0.927 | 0.362 | 1.272 | 0.496 | -61% |
| workspace/definition | 0.117 | 0.129 | 0.171 | 0.202 | +12 us |
| workspace/completion | 0.736 | 0.785 | 0.961 | 1.065 | +49 us |
| tree/didSave->diagnostics | 15.562 | 15.754 | 17.305 | 19.400 | 0 (gcc) |
| tree/inlayHint | 0.388 | 0.262 | 1.556 | 1.014 | -32% |

| Value | Base | Final | Change |
|---|---:|---:|---:|
| cold/peak-rss (kB) | 25872 | 19900 | -23% |
| fixtures/peak-rss (kB) | 23400 | 21284 | -9% |
| typing-20k/peak-rss (kB) | 69596 | 35772 | -49% |
| typing-100k/peak-rss (kB) | 323468 | 108656 | -66% |
| workspace/peak-rss (kB) | 67144 | 70352 | +5% |
| tree/peak-rss (kB) | 34900 | 30364 | -13% |
| typing-20k burst requests computed | 500 | 1 | 499 cancelled |
| typing-100k burst requests computed | 500 | 1 | 499 cancelled |
| typing-*/semanticTokens delta reply (bytes) | 471052 / 2348763 (full) | 129 | |

Reading the table:

- At fixture size every request is well under a millisecond and mostly
  pipe round trip. The few that moved up by 5 to 12 microseconds did so
  because the symbol index now parses a document on the first query that
  needs it instead of on every edit (see below), and builds the set of open
  documents per query. That is the intended trade.
- In the typing scenarios a full semantic tokens request now pays for the
  analysis itself: the completion before it used to compute (and cache) the
  analysis for the mode table and now does not. Per keystroke the sum is
  lower, and the frequent request, completion, is 4 to 5 times faster.
- `tree/didSave->diagnostics` is the compiler and assembler running; the
  server's share is negligible and unchanged.

## Results: microbenchmarks

Baseline: `docs/bench/baseline-bench.txt`. Final: `docs/bench/final-bench.txt`.
benchstat output: `docs/bench/compare-bench.txt` (5 runs each; benchstat's
box-drawing bars, plus-minus, infinity and footnote marks transliterated to
ASCII, since every committed file is pure ASCII). Medians:

| Benchmark | Base | Final | Change |
|---|---:|---:|---:|
| engine Change/20k (didChange inside the engine) | 3.33 ms, 8.8 MB | 0.40 us, 352 B | -99.99% |
| engine Change/100k | 15.8 ms, 45 MB | 0.36 us, 352 B | -99.99% |
| engine Snapshot/20k (tokens + analysis) | 5.61 ms | 4.43 ms | -21% |
| engine Snapshot/100k | 27.4 ms | 21.3 ms | -22% |
| engine EditThenQuery/semanticTokens/20k | 12.19 ms | 6.88 ms | -44% |
| engine EditThenQuery/semanticTokens/100k | 59.6 ms | 33.0 ms | -45% |
| engine EditThenQuery/completion/20k | 11.16 ms | 2.29 ms | -79% |
| engine EditThenQuery/completion/100k | 52.6 ms | 10.4 ms | -80% |
| engine EditThenQuery/documentSymbol/100k | 52.1 ms | 19.4 ms | -63% |
| engine EditThenQuery/foldingRange/100k | 40.5 ms | 7.8 ms | -81% |
| engine EditThenQuery/hover/100k | 48.8 ms | 21.1 ms | -57% |
| docstore Change/20k | 668 us, 1.21 MB | 90 us, 0.73 MB | -86% |
| docstore Change/100k | 3.09 ms, 6.6 MB | 0.34 ms, 3.5 MB | -89% |
| analysis AnalyzeTokens (20k, level v3) | 12.8 ms, 16.2 MB, 64403 allocs | 3.32 ms, 1.2 MB, 390 allocs | -74% |
| lexer TokenizeFixtures | 292 us, 1.01 MB | 231 us, 0.60 MB | -21% |
| semtok EncodeFixtures | 826 us | 723 us | -12% |
| symbols IndexUpdate (20k) | 3.15 ms | 214 ns | parse deferred to query |
| symbols DocumentSymbols (20k) | 3.91 ms | 3.40 ms | -13% |
| symbols FoldingRanges (20k) | 2.10 ms | 1.71 ms | -18% |
| symbols References | 20.9 us | 21.5 us | +3% |
| jsonrpc ReplyLarge (94k-integer struct) | 4.30 ms | 1.23 ms | -71% |
| completion Complete/* (no document tokens) | 1.12-1.53 ms | 0.88-1.30 ms | -15 to -22% |
| isa Load (both tables, cold) | 8.16 ms | 8.13 ms | unchanged |
| hover Hover (pmode.S, every mnemonic/register) | 434 us | 440 us | +1% |

Benchmarks added during the pass have no baseline row in benchstat; their
before numbers were measured on the baseline code before each change:
`jsonrpc ReplySemanticTokens` 4.3 ms and 1.15 MB through the generic path,
0.43 ms and 288 KB final; `protocol MarshalSemanticTokens` 2.77 ms, 2.06 ms
final (encoding/json still re-scans a MarshalJSON result, which is why the
reply path bypasses it); `lexer TokenizeLarge` 1.71 ms and 6.9 MB, 1.38 ms
and 3.4 MB final.

## Optimisations

In the order they were made; each was measured on its own before the next.

1. **Instruction tables load in the background.** `main` parsed both
   embedded JSON tables before starting the read loop, so the initialize
   reply waited about 9 ms for data it does not need. The load now starts in
   a goroutine at launch; a request that needs the tables earlier waits on
   the same `sync.Once`. cold/spawn->initialize 11.0 -> 1.5 ms (p50, 30
   starts); spawn->semanticTokens-20k 48.1 -> 39.8 ms.

2. **Replies are framed by hand, and semantic tokens encode themselves.**
   `Conn.Reply` marshaled the result, then marshaled the `Message`, which
   re-validated and copied the raw result; encoding/json also re-scans every
   `MarshalJSON` output. Now the reply body is appended directly into a
   buffer with room reserved for the header, and a result implementing
   `jsonrpc.JSONAppender` (`protocol.SemanticTokens`) writes its integers
   with `strconv.AppendUint`. Reply of a 20k-line document's tokens (94k
   integers): 4.3 ms and 1.15 MB -> 0.43 ms and 288 KB. Generic large reply:
   4.30 -> 1.23 ms.

3. **The document line index is patched, not rebuilt.** `docstore.Change`
   rescanned the entire text for line starts on every keystroke. Lines
   before the edit keep their starts, the edited span (one byte either side,
   for `\r\n` pairs) is rescanned, and later starts are shifted. A property
   test checks the patched index against a rebuild over thousands of random
   edits with every line ending. Change on 20k lines 668 -> 90 us; on 100k
   3.09 -> 0.34 ms. What remains is copying the string, which an immutable
   `Text string` requires.

4. **The symbol index parses lazily and shares the engine's tokens.**
   Every didChange re-lexed and re-parsed the whole file for the index,
   synchronously, and resolved its includes (which forced the engine's own
   tokenization too), although index queries are rare next to keystrokes.
   `Index.UpdateLazy` now stores a loader; the first query that needs the
   document calls it once. The loader returns the snapshot's own parse
   (`symbols.Parse` over the snapshot's cached token stream), which
   documentSymbol also uses; foldingRange takes the cached tokens
   (`FoldingRangesTokens`). `symbols.file` keeps line offsets instead of a
   `[]string` of lines, which removed 100k string headers per parse from
   the heap the collector scans. Engine didChange 3.33 ms -> 0.4 us (20k),
   15.8 ms -> 0.36 us (100k); edit plus semantic tokens on 100k 60.5 ->
   40.2 ms at that point.

5. **The analysis stopped allocating per instruction.** `selectForms`
   copied the matching forms of every instruction (mov has dozens) to pick
   the cheapest; `parseOverride` lower-cased every comment looking for the
   `falcon-asm: level=` tag; `modeIn` formatted and split strings per
   operand. Now a predicate plus a running minimum, a case-insensitive
   search that allocates nothing, and an in-place walk. AnalyzeTokens (20k,
   level v3) 12.8 ms and 17 MB -> 8.4 ms and 1.55 MB.

6. **Form selection is cached per instruction.** The predicate still
   case-folded every form's operands on every instruction. Instructions are
   immutable once loaded, so the best and first form per operand family are
   computed once per instruction (`sync.Map`, since debounced publishes
   analyze concurrently). 8.4 -> 3.8 ms.

7. **Token slices are sized to what the lexer produces.** `Tokenize`
   reserved `len(src)/4` tokens; the fixtures average 10 to 13 bytes per
   token, so it reserved and zeroed three times what it used. Now
   `len(src)/8`. TokenizeLarge 1.71 -> 1.66 ms and 6.9 -> 3.4 MB per pass;
   the memory halving is the point (zeroing and GC), the time is a bonus.

8. **Requests are read ahead; cancelled and stale ones are skipped.** The
   server read and handled on one goroutine, so `$/cancelRequest` always
   arrived after the request it named had been answered, and a client typing
   into a large file queued one full semantic tokens computation per
   keystroke. A reader goroutine now fills a queue (`internal/server/inbox.go`);
   the handler still processes strictly in order. A request cancelled while
   queued is answered `RequestCancelled` without running (initialize and
   shutdown always run). A semantic tokens request (full or delta) with a
   newer didChange for its document already queued is answered
   `ContentModified`, since its result would describe text the editor no
   longer shows; the client asks again. burst-drain 12.5 s -> 62 ms (20k)
   and 59.3 s -> 234 ms (100k); 499 of 500 burst requests are now never
   computed; peak RSS at 100k 323 -> 104 MB.

9. **Completion needs neither the full analysis nor a prefix re-lex.**
   Completion asked the analysis for the mode at the cursor, which computed
   every diagnostic and symbol of the document on every keystroke. A
   modes-only walk (`analysis.ModeAtLine`) runs the same statement
   splitting with every check and symbol switched off, so the tables cannot
   disagree; a test holds them equal on every fixture. Completion also
   re-lexed everything before the cursor whenever any `/*` appeared earlier
   in the file (in real files: always); with the document's cached tokens
   (`completion.Context.Tokens`) it can tell whether a block comment is
   actually open at the cursor line, and otherwise lexes just that line. A
   test compares answers with and without the tokens at every byte of the
   fixtures. Edit plus completion on 100k 26.4 -> 12.3 ms, on 20k 2.8 ms.

10. **The workspace scan reads and parses in parallel.** The first
    cross-file query walks the root and parses every `.S`; files are
    independent, so a worker pool of GOMAXPROCS does it. references-first
    over 500 files 34 -> 13 ms (with GOMAXPROCS=1 it measures 53 to 66 ms,
    so the parallelism, not noise, is the gain).

11. **The lexer and analysis find newlines with IndexByte.** Block
    comments are now scanned a line at a time with `strings.IndexByte` and
    `strings.Index` (vectorized in the runtime) instead of byte by byte;
    the same for `lineEnd` and the analysis' line table. Output is
    identical to the old lexer on the fixtures and on 300k random inputs
    (a one-off differential test) and the fuzz target ran clean.
    TokenizeLarge 1.66 -> 1.42 ms, TokenizeFixtures 280 -> 245 us.

12. **Semantic tokens deltas.** The server now advertises
    `"full":{"delta":true}`, sends a `resultId` with every result and
    answers `textDocument/semanticTokens/full/delta` with one edit spanning
    the changed tokens (common prefix and suffix, aligned to whole
    tokens), or the full stream when the base is unknown. It keeps one
    previous result per open document. Reply size after an edit: 471 KB
    (20k) and 2.35 MB (100k) -> 129 bytes; in the same run a delta request's
    p50 is 6.4 ms against a full request's 7.6 ms (20k), and 29.2 against
    35.0 ms (100k), the encoding and transfer saved. The larger win is on
    the client, which no longer parses megabytes of JSON per keystroke;
    lspbench does not measure the client.

13. **Register lookup without allocation.** `isa.LookupRegister` squeezed
    blanks out of every name with `strings.Fields` and `Join` to accept
    `%st (1)`; now only when there is a blank. AnalyzeTokens 3.76 -> 3.36
    ms, 17340 -> 390 allocations.

14. **FindDB runs outside the engine lock.** On the first open in a
    directory without a configured database, `FindDB` walked up the tree
    and parsed whatever `compile_commands.json` it found while holding the
    lock that toolchain results and debounced publishes also need. Parsing
    a generated 3.5 MB database takes 54 ms, all of which was lock hold
    time. Now only the cache read and fill are locked (`DB` is read-only
    once loaded). Not visible in lspbench, whose scenarios configure a root
    database; it removes a stall, not request latency.

Combined: a CPU profile of the baseline server through the 100k typing
scenario with 200 edits per phase sampled 55.3 s of CPU; the final server
through the same scenario with 500 edits per phase sampled 10.7 s.

## Tried and dropped

| Change | Measured | Why dropped |
|---|---|---|
| `slices.BinarySearch` instead of `sort.Search` in `docstore.Position` | BenchmarkPosition 7.49 -> 7.12 us, p=0.15 (not significant) | no measurable gain |
| GOGC=200 | typing-20k completion p50 3.17 -> 2.78 ms, semanticTokens 7.55 -> 6.97 ms; 100k semanticTokens 34.65 -> 34.54 ms; peak RSS 100k 114.5 -> 169.1 MB, 20k 35.1 -> 44.8 MB | 3 to 12% latency for up to 48% more memory |
| GOGC=400 | typing-20k completion 3.17 -> 2.67 ms, semanticTokens 7.55 -> 6.83 ms; 100k semanticTokens 34.65 -> 33.39 ms; peak RSS 100k 114.5 -> 279.1 MB, 20k 35.1 -> 70.1 MB | same trade, worse |
| Index snapshot holding open documents in a slice instead of two maps | References 21.97 -> 22.64 us (n=10, p=0.14) | no measurable gain |

Considered and not done:

- **Generated Go tables or a binary encoding for the instruction data.**
  The load is 8.2 ms, under the 20 ms threshold set for this pass, and
  since optimisation 1 it overlaps the handshake. A request arriving within
  about 8 ms of process start still waits for it.
- **A hand-written JSON envelope parser.** Opening a 20k-line document
  spends about 3.4 ms in `jsonrpc.Read` (validating and copying the params)
  and 2.9 ms decoding them again in the handler, per 550 KB of text. Saving
  a pass means not validating params at read time, which would turn
  malformed params from ParseError into InvalidParams. Not worth the
  protocol change for a once-per-open cost.

## Remaining hot spots

- **Whole-document recomputation per version on very large files.** Every
  new version re-lexes (about 7 ms at 100k lines) and, for semantic tokens,
  hover and diagnostics, re-analyzes (about 15 ms at 100k lines of 16-bit
  code analyzed as 64-bit, which is diagnostic-heavy) and re-encodes (about
  8 ms). Incremental lexing (reusing the previous version's tokens outside
  the edited lines) would be the next step; it needs the token stream to
  stop carrying absolute line numbers or a cheap way to shift them.
- **Hover on a large file** computes the full analysis to look up in-file
  `#define`s (21 ms at 100k lines after an edit). A defines-only lookup
  would cut it to the lexing cost.
- **didOpen JSON**: about 6 ms per 550 KB of document text, see above.
- **GC**: the token slice holds a string per token, so a 100k-line
  document's tokens are megabytes of pointers the collector scans while
  the version is live. A token without `Text` (offsets only) would remove
  that, at the cost of touching every consumer of the lexer.
- **The instruction table load** (8.2 ms, JSON) on a request that arrives
  within the first few milliseconds of the process.
- At fixture size there is nothing left worth chasing: every request is
  under 0.35 ms at p95, most of it the pipe round trip, and the toolchain
  run on save (about 16 ms) is gcc and as.

## Linter pass

The linter (about 100 rules over `internal/asm`, the dataflow in
`internal/flow`, the rule engine, the hardware tables, hardware hovers,
code lenses, register hints, call hierarchy and code actions) landed after
the performance pass. This pass measured it against the performance
pass's final numbers (caaab5f), fixed what had regressed or did not meet
the interactivity targets, and reviewed what a user sees.

### Environment and method

Same machine as above (AMD Ryzen 9 5900XT, 32 GB, Linux
7.2.7-cachyos-gentoo-dist-mono, go1.26.7), 2026-10-03, load average below
2 throughout. Three server binaries: **caaab5f** (the performance pass,
rebuilt from `git archive caaab5f`), **head** (2ac496c, the linter before
this pass) and **linter** (this pass).

- End to end: `lspbench` (this pass's version) ran each binary twice,
  interleaved (caaab5f, head, linter, caaab5f, head, linter); caaab5f
  only runs the scenarios it supports. The tables below are the means of
  the two runs (`docs/bench/linter-run{1,2}-{caaab5f,head,linter}.json`,
  aggregated in `docs/bench/linter-compare-lspbench.txt`). A last run of
  the final binary, after the word-index change (optimisation 8 below),
  is `docs/bench/linter-lspbench.{json,txt}`.
- Microbenchmarks: the test binaries of caaab5f and of this pass were
  built once and run alternately, one count per round, 6 rounds
  (`docs/bench/linter-bench-caaab5f.txt`, `docs/bench/linter-bench.txt`);
  the engine benchmarks the linter added, head against this pass the same
  way (`docs/bench/linter-bench-engine-head.txt`,
  `docs/bench/linter-bench-engine.txt`). benchstat output, transliterated
  to ASCII: `docs/bench/linter-compare-bench.txt`.

### Pre-existing metrics

End to end, p50 and p95 in milliseconds; "final" is the committed caaab5f
result, "caaab5f" its rerun in this session.

| Metric | final p50 | caaab5f p50 | head p50 | linter p50 | caaab5f p95 | linter p95 |
|---|---:|---:|---:|---:|---:|---:|
| cold/spawn->initialize | 1.581 | 1.579 | 1.813 | 1.831 | 1.907 | 2.006 |
| cold/spawn->semanticTokens-20k | 23.213 | 22.413 | 22.719 | 22.665 | 23.928 | 23.291 |
| fixtures/semanticTokens/full | 0.076 | 0.074 | 0.070 | 0.078 | 0.228 | 0.179 |
| fixtures/definition | 0.033 | 0.031 | 0.031 | 0.030 | 0.064 | 0.076 |
| fixtures/references | 0.037 | 0.032 | 0.035 | 0.033 | 0.051 | 0.049 |
| fixtures/hover | 0.029 | 0.029 | 0.031 | 0.032 | 0.056 | 0.074 |
| fixtures/completion | 0.066 | 0.058 | 0.058 | 0.058 | 0.172 | 0.170 |
| fixtures/documentSymbol | 0.040 | 0.033 | 0.036 | 0.035 | 0.059 | 0.054 |
| fixtures/foldingRange | 0.027 | 0.023 | 0.027 | 0.027 | 0.065 | 0.038 |
| fixtures/inlayHint | 0.025 | 0.021 | 0.025 | 0.024 | 0.038 | 0.030 |
| typing-20k/open+semanticTokens | 20.803 | 20.576 | 20.819 | 20.611 | | |
| typing-20k/completion | 2.936 | 3.071 | 3.059 | 3.086 | 5.550 | 5.489 |
| typing-20k/semanticTokens/full | 7.307 | 7.382 | 7.155 | 7.187 | 9.314 | 9.033 |
| typing-20k/semanticTokens/full/delta | 5.987 | 6.143 | 6.224 | 6.089 | 8.210 | 8.038 |
| typing-20k/documentSymbol | 3.747 | 3.808 | 3.508 | 3.447 | 4.386 | 4.041 |
| typing-20k/foldingRange | 0.479 | 0.504 | 0.475 | 0.488 | 0.563 | 0.549 |
| typing-20k/burst-drain | 53.792 | 68.782 | 58.370 | 59.645 | | |
| typing-100k/open+semanticTokens | 92.337 | 92.380 | 89.196 | 91.064 | | |
| typing-100k/completion | 12.975 | 12.744 | 12.991 | 12.825 | 23.323 | 23.023 |
| typing-100k/semanticTokens/full | 34.517 | 33.858 | 33.252 | 33.776 | 43.025 | 43.052 |
| typing-100k/semanticTokens/full/delta | 28.265 | 27.997 | 28.047 | 27.832 | 37.621 | 36.677 |
| typing-100k/documentSymbol | 21.025 | 20.791 | 20.553 | 20.622 | 22.437 | 22.040 |
| typing-100k/foldingRange | 1.900 | 1.916 | 1.905 | 1.873 | 2.268 | 2.063 |
| typing-100k/burst-drain | 216.253 | 221.608 | 221.971 | 202.388 | | |
| workspace/references-first | 13.487 | 12.970 | 12.308 | 14.181 | | |
| workspace/references | 0.362 | 0.382 | 0.351 | 0.363 | 0.450 | 0.454 |
| workspace/definition | 0.129 | 0.137 | 0.136 | 0.138 | 0.179 | 0.178 |
| workspace/completion | 0.785 | 0.815 | 0.807 | 0.811 | 0.945 | 0.944 |
| tree/didSave->diagnostics | 15.754 | 14.809 | 15.910 | 15.574 | 16.537 | 18.294 |
| tree/inlayHint | 0.262 | 0.260 | 0.230 | 0.232 | 0.789 | 0.827 |

| Peak RSS (kB) | final | caaab5f | head | linter |
|---|---:|---:|---:|---:|
| cold | 19900 | 20162 | 22812 | 23890 |
| fixtures | 21284 | 20486 | 24810 | 24762 |
| typing-20k | 35772 | 35960 | 37130 | 40922 |
| typing-100k | 108656 | 106854 | 117270 | 114596 |
| workspace | 70352 | 69360 | 70728 | 74760 |
| tree | 30364 | 30486 | 71642 | 70182 |

Microbenchmarks, caaab5f against this pass (6 interleaved rounds): every
pre-existing benchmark is within noise (benchstat `~`) or within a few
percent either way. The significant ones: engine Snapshot/20k -2.1%,
Snapshot/100k -2.4%, EditThenQuery/semanticTokens/20k -3.2%, Change/100k
-3.5%; EditThenQuery/documentSymbol/100k +2.3% (not significant head
against this pass), EditThenQuery/hover/100k +2.0% (20.7 -> 21.1 ms),
jsonrpc ReplySemanticTokens +2.0%, symbols IndexUpdate +4.4% (213 -> 223
ns), DocumentSymbols +3.6% and FoldingRanges +3.0% (the symbol parse now
also records `.include` names and their kind, for the include graph the
symbol rules walk; no end-to-end metric moved).

Reading the tables: what remains above caaab5f is the cost of carrying the
linter, not work on the keystroke path.

- **Static footprint.** The binary grew from 4.5 to 7.1 MB, package
  initialisation (rule registry, the rules' lookup tables) from about 0.25
  to 0.4 ms, and the hardware tables (2.5 ms to parse, about 2 MB) now
  load at startup on their own goroutine (optimisation 7). That is the
  +0.25 ms on spawn->initialize and the 4 to 5 MB on every peak RSS,
  typing and workspace included: an idle server's VmHWM is 12.6 MB for
  caaab5f and 17.4 MB now. typing-100k is +7% (114.6 against 106.9 MB,
  run-to-run spread about 5 MB); the lint never runs inside the typing
  scenario (its edits come faster than the debounce).
- **tree/peak-rss** (30 -> 70 MB) is the open of 18 files: 18 debounced
  lint runs, 18 toolchain listings and the first scan of the workspace's
  600 C, header and linker-script files for the symbol rules' word index
  (about 25 MB of the difference; with the workspace view switched off the
  peak is 46 MB). It is transient: the heap after the scenario is about
  10 MB.
- **workspace/references-first** varies by 2 ms between runs of the same
  binary (it is one sample); the scan now also resolves includes.

### New scenarios

| Metric | head p50 | linter p50 | head p95 | linter p95 |
|---|---:|---:|---:|---:|
| features/hover (every operand and mnemonic, 3 files) | 0.027 | 0.028 | 0.043 | 0.044 |
| features/hover-after-edit (register, before lint) | 0.329 | 0.310 | 0.681 | 0.577 |
| features/codeAction (each published finding) | 0.033 | 0.036 | 0.054 | 0.051 |
| features/codeAction-cursor (light bulb) | 0.021 | 0.021 | 0.028 | 0.029 |
| features/codeLens | 0.017 | 0.019 | 0.024 | 0.029 |
| features/inlayHint-registers | 0.024 | 0.026 | 0.340 | 0.051 |
| features/workspaceSymbol | 0.043 | 0.045 | 0.262 | 0.231 |
| features/prepareCallHierarchy | 0.193 | 0.218 | 0.405 | 0.344 |
| features/incomingCalls | 0.161 | 0.155 | 0.231 | 0.299 |
| features/outgoingCalls | 0.157 | 0.156 | 0.226 | 0.235 |
| diagnostics/edit->publish-minus-debounce (fixtures) | 1.107 | 1.030 | 1.953 | 1.708 |
| diagnostics-20k/edit->publish-minus-debounce | 44.548 | 37.494 | 46.151 | 39.692 |
| diagnostics-20k/open->publish | 215.308 | 205.473 | | |
| diagnostics-20k/completion-during-lint | 1.187 | 1.605 | 2.148 | 3.125 |
| diagnostics-20k/codeAction-after-edit | 39.862 | 0.310 | 41.382 | 0.375 |
| diagnostics-20k/hover-after-edit | 17.468 | 15.866 | 20.957 | 16.296 |
| diagnostics-100k/edit->publish-minus-debounce | 247.144 | 200.138 | 252.894 | 213.060 |
| diagnostics-100k/completion-during-lint | 12.271 | 12.294 | 13.965 | 14.116 |
| diagnostics-100k/codeAction-after-edit | 222.448 | 1.154 | 227.764 | 1.337 |
| diagnostics-100k/hover-after-edit | 103.760 | 93.044 | 115.579 | 97.852 |
| workspace/edit->publish-minus-debounce (500 files) | 1.948 | 2.010 | 2.218 | 2.436 |
| tree/codeLens | 0.107 | 0.044 | 0.252 | 0.096 |
| cli/lint-tree (airies-boot, 600 files) | 72.198 | 77.101 | 78.171 | 83.042 |
| cli/lint-tree-toolchain | 128.387 | 123.232 | 130.927 | 139.528 |
| cli/lint-ws500 | 215.308 | 217.105 | 215.493 | 219.634 |

caaab5f's workspace/edit->publish-minus-debounce, for comparison, is 0.51
ms: its publish was the analysis alone.

| Value | head | linter |
|---|---:|---:|
| features: hovers with content / longest (lines) / over 12 lines | 870 / 55 / 35 | 870 / 19 / 22 |
| diagnostics (fixtures) peak RSS (kB) | 24914 | 30430 |
| diagnostics-20k peak RSS (kB) | 147068 | 78964 |
| diagnostics-100k peak RSS (kB) | 613340 | 336688 |
| features peak RSS (kB) | 38374 | 37758 |
| cli/lint-tree peak RSS (kB) | 90160 | 84534 |
| cli/lint-tree-toolchain peak RSS (kB) | 106406 | 103286 |
| cli/lint-ws500 peak RSS (kB) | 206502 | 205950 |

Against the targets of this pass: at fixture size every request's p95 is
under 0.6 ms (hover 0.074, the slowest the first hover of a new version at
0.58), and edit to diagnostics is 1.0 ms p50, 1.7 ms p95 beyond the 150
ms debounce (2.0 ms in the 500-file workspace). At 20k lines typing is
unchanged (semantic tokens and completion within 2% and 5% of caaab5f,
both inside its own rerun's spread) and edit to diagnostics is 37.5 ms p50,
39.7 ms p95 beyond the debounce.

Engine microbenchmarks the linter added, head against this pass: one
version's diagnostics from scratch (tokens, analysis, parse, flow, every
rule) Diagnostics/20k 35.7 -> 28.8 ms (-19%), Diagnostics/100k 213.7 ->
173.8 ms (-19%), allocations -25%; a keystroke followed by a hover
EditThenQuery/hover/20k 21.6 -> 4.8 ms and /100k 128.6 -> 21.0 ms (-78%,
-84%, back to caaab5f); RegisterHintsCold 340 -> 305 us.

### What was found and fixed

In the order they were made; each measured on its own.

1. **One parse, one dataflow and one hardware index per version.** The
   lint run parsed the document and ran the dataflow for its rules; the
   hardware hover and register hints parsed it and ran the dataflow
   again for their index; code lenses parsed it once per request; the
   macro expansion hover parsed it a fourth time. The snapshot now owns
   `program()`, `flow()` and `hwIndex()`, and the lint run takes them
   through `project.Doc.Prog`, `lint.ProvideFlow` and `hw.Provide` (a
   provider registered with the run's memo, so nothing is built that no
   rule asks for). With the next two items: Diagnostics/20k 35.3 -> 29.1
   ms; diagnostics-20k peak RSS 147 -> 79 MB, diagnostics-100k 613 ->
   337 MB, because hover and lint no longer each hold a parse and a flow
   solution of the same version.
2. **SIPI trampolines** were searched with a regexp over every immediate
   of every instruction, three times per lint (three rules ask): 10% of
   lint CPU at 20k lines. Now a `>>` pre-check and once per index.
3. **Line maps instead of hash maps.** The calling convention rules'
   annotation scan and the hardware index's comment-line scan built a
   map entry per token's line, and lowered every comment to look for the
   marker: about 12% of lint CPU together. Now line-indexed slices and a
   case-insensitive search at the comment's colons.
4. **Light-bulb code actions stay off the keystroke path.** VS Code asks
   for code actions after every cursor move and edit; for a version not
   yet linted that request ran every rule, ahead of the debounce, on the
   request queue: 39.9 ms at 20k lines, 222 ms at 100k. A request with
   `triggerKind` Automatic is now answered `null` until the version's
   lint exists (the editor asks again when the diagnostics arrive); an
   invoked request still lints on demand. 0.31 ms and 1.15 ms.
5. **Hover asks for the hardware index only when it can use it.** A hover
   on `add`, a label, a comment or a mov between general registers built
   the version's parse, dataflow and index. `mayHaveHardware` decides from
   the token under the cursor (`hw.MnemonicMayHover` for mnemonics). The
   hover output over every token of the 14 fixtures is byte-identical
   with and without the check.
6. **The macro expansion hover parses only when there can be a macro.**
   An unknown first word (a typo, a half-typed mnemonic, which is what
   typing produces) made hover parse the whole version to look for a gas
   macro invocation. `mayExpand` answers from the tokens unless the file
   defines a `.macro` of that name, includes assembly, or the word is a
   cpp macro. EditThenQuery/hover 8.4 -> 4.4 ms (20k), 40.5 -> 22.3 ms
   (100k), together with item 5 21.6 -> 4.8 and 128.6 -> 21.0 ms.
7. **The hardware tables load at startup**, on a goroutine of their own
   beside the instruction tables, instead of on the first request that
   needs one (a BIOS, port or CPUID table costs 0.4 to 0.6 ms to parse).
   fixtures/hover p95 0.17-0.19 -> 0.06-0.09 ms over four runs each. On
   the instruction tables' goroutine they delayed the first semantic
   tokens enough to show in fixtures/semanticTokens p95 (0.25 -> 0.35
   ms), which is why they have their own.
8. **The workspace word index** (identifiers of the C sources and linker
   scripts, for the symbol rules) built a set per file and cloned each
   word; now one set per worker, cleared per file, and one string per
   file the words are sliced from. tree peak RSS 70.4 -> 66.6 MB (mean of
   three runs each, spread about 5 MB).
9. **flow drops its block states** once solved: the result kept the
   analyzer for `After`, and with it one State (888 bytes) per block,
   about 13 MB at 100k lines, for as long as the version is cached.

Benchmark fixes: BenchmarkFlowHover used a bare `docstore.Document`,
whose line index is rebuilt on every position conversion, and measured
that (84.8 us per hover); through a store, as the server does, a warm
register hover is 1.6 us.

### Usability review and changes

Every hover over every token of the 14 fixtures, the CLI over airies-boot
(with and without `--toolchain`) and over the fixtures, `falcon-asm rules`,
and every rule's documented bad example linted with all rules on in each
mode were read as a user would.

- **Hardware hovers were reference cards.** An `int $0x13` whose AH
  differs by path appended both services in full: 55 lines, with BIOS
  quirk lists and a disk address packet table; a `$0xCF9` feeding two
  writes repeated the port card twice. Now the hardware part keeps what
  is specific to the operand, drops Notes/Quirks lists, structure tables
  and confidence remarks, never repeats a paragraph, and is capped at 12
  lines with "(N more lines)"; a path-dependent int lists one title line
  per candidate service (each in full on its own `movb $..., %ah`).
  Longest hover 55 -> 19 lines (instruction part included), hovers over 12
  lines 35 -> 22 of 870.
- **Duplicate diagnostics.** `level-outside` and `feature-unguarded`
  reported the same mnemonic for the same reason ("aesenc is outside the
  x86-64 level scheme: requires AES" beside "aesenc needs AES ... the
  target does not guarantee it"). level-outside now leaves to
  feature-unguarded every assembly instruction the latter judges (it also
  knows when a CPUID test guards it); it still reports inline asm and
  extensions without a named CPUID bit (its documented example is now
  `ptwrite`). No other pair of rules, or rule and gas, reported one
  problem twice in the examples or on airies-boot.
- **The light bulb** no longer waits for a full lint after each keystroke
  (item 4 above).
- Checked and left as they are: every message is one line; applying each
  offered fix, and each "Ignore ... on this line" action, to the example
  it belongs to and linting again introduces no new warning or error; the
  CLI prints `file:line:col: severity: message [code]` with a summary on
  stderr (`18 problems (18 info) in 600 files linted` on airies-boot),
  exits 0 below `--severity` (default warning); `falcon-asm rules` is a
  readable table. Info noise on airies-boot is 18 findings in 600 files,
  15 of them `unused-label` (9 on one table of report strings in
  rigboot.S, each genuinely unreferenced).
- Tests that failed under `go test -count=2` (they mutated process-global
  registries without restoring them) were fixed; `lint.RegisterTables`
  now returns the loader it replaces.

### Remaining hot spots

- **Lint of very large files.** At 100k lines one version's lint is 200
  ms beyond the debounce and the server peaks at 337 MB (head: 613). It
  never blocks typing (completion during the lint: 12.3 ms, the same as
  without it), but the memory is real: the dataflow keeps one 888-byte
  State per statement (61 MB at 100k lines), the program parse 38 MB, the
  tokens 33 MB, all cached per version so hover can reuse them. A cursor
  API over block entry states (replaying a block on demand) would cut the
  first; it changes every rule that reads `Result.In`.
- **First hover of a version on a register operand** builds the parse,
  dataflow and hardware index: 0.3 ms at fixture size, 16 ms at 20k
  lines, 93 ms at 100k, once per version (the debounced lint builds the
  same, so it is not extra work, only earlier). Register inlay hints, when
  switched on, pay the same on the first request of a version.
- **Encoding form matching** is about 13% of a lint at 20k lines (each
  instruction against every form of its mnemonic).
- **CLI parallelism and memory**: `lint` over the 500-file workspace takes
  0.17 s with 32 jobs at 175 to 205 MB peak, 0.17 s with 16 at 128 MB and
  0.36 s with 4 at 89 MB. The default stays GOMAXPROCS; `--jobs` is the
  knob for memory-constrained CI.
