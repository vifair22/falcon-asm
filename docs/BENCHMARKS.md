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
| `-scenarios` | `all` | comma list of `cold,fixtures,typing,workspace,tree`; `all` includes `tree` only with `-root` |
| `-fixtures` | `testdata/falcon-bios` | fixture directory |
| `-root` | | a real project with `compile_commands.json`, for `tree` (only read) |
| `-sizes` | `20000,100000` | typing scenario file sizes in lines |
| `-edits` | `500` | edits per typing phase |
| `-ws-files` | `500` | files in the generated workspace |
| `-cold-runs` | `10` | server starts in `cold` |
| `-rounds` | `3` | repetitions of the per-file sweeps |
| `-timeout` | `2m` | longest wait for one reply |
| `-settle` | `3s` | quiet period after opening the tree |
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

**tree**: a real project with the toolchain on (here
`/home/vifair/git/home/airies-boot`, 18 assembly sources from its
`compile_commands.json`). Opens them all, waits until no diagnostics have
been published for 3 s, then 3 rounds of `didSave` per file, timing save to
the toolchain's diagnostics publish, followed by the listing inlay hints.

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
