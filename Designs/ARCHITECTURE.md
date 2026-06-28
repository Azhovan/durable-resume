# durable-resume v2 — Architecture

This document is the single authoritative design reference for **durable-resume**
(the `dr` command). It describes what the code in `main.go`, `cmd/`, and
`download/` actually does today, citing the real types and functions. There is no
aspirational content here: every behavior below is backed by a function in the
tree.

---

## 1. Overview & goals

`dr` is a **correct, resumable, segmented HTTP/HTTPS file downloader**. Given a
URL it:

- probes the remote once to learn the size and whether it honors byte ranges;
- if ranges are honored and the size is known, splits the file into disjoint byte
  chunks and downloads them concurrently into a single pre-allocated destination
  file via `os.File.WriteAt`;
- if ranges are not honored (or the size is unknown), falls back to a single
  sequential stream;
- persists durable resume state in a `<output>.dr.json` sidecar so an interrupted
  segmented download can be continued later from non-zero offsets;
- verifies the result by on-disk size and, optionally, a `sha256` checksum;
- removes the sidecar on success and retains both sidecar and partial file on
  failure/interruption.

Design priorities, in order: **correctness** (never report a sparse/short file as
success), **durable resumability**, then throughput via bounded concurrency.

Only `http` and `https` schemes are accepted; anything else is rejected with
`download.ErrUnsupportedScheme` (validated in both `cmd.validateURL` and
`download.validateScheme`).

---

## 2. Package layout

```
main.go            entrypoint; ldflag version vars; calls cmd.NewRootCmd(...).Execute()
cmd/root.go        cobra CLI: flag parsing, URL validation, header/checksum parsing,
                   signal handling (SIGINT/SIGTERM), builds download.Options, calls download.Run
download/          the engine
```

### `main.go`
Holds the `Version`/`Revision`/`Date` ldflag vars (defaulting to `dev`/`none`/
`unknown` for `go run`) and a `main()` that runs the root command, printing
`dr: <err>` to stderr and exiting 1 on error.

### `cmd/root.go`
`NewRootCmd(version, revision, date)` builds the single `dr <url> [flags]` cobra
command (`cobra.ExactArgs(1)`, `SilenceUsage`/`SilenceErrors` on). Responsibilities:

- `validateURL` — non-empty + `http`/`https` scheme, else `ErrNoURL` /
  `ErrUnsupportedScheme`.
- `parseHeaders` — repeatable `-H "Key: Value"` into an `http.Header` (errors on
  missing colon / empty key).
- `parseChecksum` — `"sha256:<hex>"` into `download.Checksum`; empty string yields
  the zero `Checksum`; rejects non-`sha256` algos and non-64-hex-char digests.
- `defaultOutputName` — derives the output filename from the URL path, falling
  back to `"download"` for empty/slash-terminated/`.`/`..` paths.
- Builds `download.Options` and installs `signal.NotifyContext(...,
  SIGINT, SIGTERM)` so Ctrl-C cancels the derived context, then calls
  `download.Run(ctx, opts)`.

Flags: `-o/--output`, `-c/--concurrency` (default `DefaultConcurrency=4`),
`--resume` (default **true**; `--resume=false` to disable), `--checksum`,
`--timeout` (default `DefaultTimeout=30s`), `--retries` (default
`DefaultRetries=3`), `-H/--header`, `-q/--quiet`, `-v/--verbose`.

### `download/` — file responsibilities

| File | Responsibility |
|------|----------------|
| `download.go` | Public `Run` + `Options`/`Checksum`; sentinel errors; orchestrates probe → strategy → resume → plan → pool → verify → cleanup. Contains `runSingleStream`, `runSegmentedDownload`, `validateScheme`, `partialFileUsable`, `httpClient`, `vlogf`. |
| `probe.go` | `remoteInfo` type + `probe`: a single `GET Range: bytes=0-0` interpreting 206/200, with a `headFallback` for other statuses. Header parsing helpers (`parseContentLength`, `parseContentRangeTotal`), `newRequest`, `copyHeaders`, `drainAndClose`. |
| `chunk.go` | `chunk` type, `ceilDiv`, `planChunks` (disjoint contiguous plan), and `fetchChunk` (ranged GET → `WriteAt` at disjoint offsets, with over-/under-length detection). |
| `pool.go` | `runSegmented` (bounded worker pool, periodic + final state flush, first-fatal-cancels-siblings), `classifyChunkError`, and `runSingle` (sequential stream). |
| `state.go` | `State` / `ChunkState` sidecar types, `statePath`, `LoadState`, `newState`, atomic `Save`, `Matches`, `MarkProgress`, `toChunks`, `completedBytes`, `Remove`. |
| `retry.go` | `newRetry` (exponential backoff + full jitter), `backoff`, `httpStatusError`, and the `isRetryable` classifier. |
| `progress.go` | `Progress` reporter: atomic counter, single render goroutine, stdlib `isTTY`, byte/rate/ETA formatting. |
| `verify.go` | `verifySize` and `verifyChecksum` (streaming sha256). |
| `*_test.go`, `regression_test.go` | Deterministic `httptest`-based tests; `regression_test.go` is the v1-defect guard suite. |

---

## 3. End-to-end data flow

```mermaid
flowchart TD
    A[dr <url> flags] --> B[cmd: validateURL + parse headers/checksum]
    B --> C[download.Run]
    C --> D{validateScheme http/https}
    D -->|invalid| ERR1[ErrUnsupportedScheme]
    D -->|ok| E[probe: GET Range bytes=0-0]
    E -->|206 + Content-Range total| F[remoteInfo: size, acceptRanges=true]
    E -->|200 + Content-Length| F2[remoteInfo: acceptRanges=false]
    E -->|405/other| HF[headFallback HEAD: Content-Length + Accept-Ranges]
    F --> G{info.streamable?<br/>acceptRanges && size>0}
    F2 --> G
    HF --> G
    G -->|no| SS[runSingleStream]
    G -->|yes| SEG[runSegmentedDownload]

    SS --> SS1[open dst O_CREATE\|O_WRONLY]
    SS1 --> SS2[retry: runSingle truncate-to-0 then sequential WriteAt from 0]
    SS2 --> SS3[verifySize skip-if-unknown + verifyChecksum]
    SS3 --> OK[success - no sidecar]

    SEG --> R1{opts.Resume?}
    R1 -->|yes| R2[LoadState <output>.dr.json]
    R2 --> R3{saved != nil?}
    R3 -->|Matches=false| R4[size differs? ErrRemoteChanged / etag differs? ErrRemoteChanged / no validator? discard]
    R3 -->|partial file missing or wrong size| R5[discard sidecar]
    R3 -->|ok| R6[st=saved; chunks=toChunks; resumed=true]
    R1 -->|no| P
    R4 -->|discard| P
    R5 --> P
    R6 --> OPEN
    P[planChunks + newState] --> OPEN

    OPEN[open dst] --> TR{resumed?}
    TR -->|no| TRU[dst.Truncate size - pre-allocate]
    TR -->|yes| NOTRU[no truncate - on-disk size already validated]
    TRU --> POOL
    NOTRU --> POOL

    POOL[runSegmented: bounded worker pool<br/>fetchChunk -> WriteAt at disjoint offsets<br/>MarkProgress + periodic Save] --> DONE{dlErr?}
    DONE -->|err| RETAIN[retain sidecar + partial file - return err]
    DONE -->|nil| V1[check st.completedBytes == size]
    V1 --> V2[verifySize + close dst + verifyChecksum]
    V2 --> RM[st.Remove sidecar]
    RM --> OK
```

Step-by-step (`download.Run` → `runSegmentedDownload`):

1. **Validate** — `opts.URL` non-empty, `validateScheme` enforces http/https.
   `concurrency` is clamped to `>=1`. `httpClient(opts)` returns the injected
   client or `&http.Client{Timeout: opts.Timeout}`.
2. **Probe** — `probe(ctx, client, url, header)` returns a `remoteInfo`
   `{size, acceptRanges, etag, lastModified}` (see §4 details below). Probe never
   hard-fails on a streamable-but-non-206 status; only transport errors propagate.
3. **Strategy select** — `info.streamable()` is `acceptRanges && size > 0`. If
   false → `runSingleStream`; otherwise → `runSegmentedDownload`.
4. **Resume-state load/validate** (segmented only, when `opts.Resume`) —
   `LoadState(statePath(output))` → see §5.
5. **Chunk plan** — fresh runs call `planChunks(info.size, concurrency)` and
   `newState(...)`; resumed runs reconstruct chunks via `saved.toChunks()`.
6. **Open + pre-allocate** — `os.OpenFile(output, O_CREATE|O_WRONLY, 0o644)`. Fresh
   runs `dst.Truncate(info.size)` to pre-allocate; resumed runs do **not** truncate
   (the on-disk size was already validated).
7. **Worker pool** — `runSegmented(...)` runs the bounded pool writing via
   `WriteAt` at disjoint offsets, flushing state periodically and finally.
8. **Verify** — for `info.size > 0`, `st.completedBytes()` must equal `info.size`
   (the bytes-actually-written check), then `verifySize(dst, info.size)`, then
   `verifyChecksum(output, opts.Checksum)`.
9. **Cleanup** — on success `st.Remove(statePath)` deletes the sidecar; on failure
   the sidecar + partial file are retained.

---

## 4. Probe details (`probe.go`)

`probe` issues a single `GET` with `Range: bytes=0-0` and branches on status:

- **206 Partial Content** — the canonical streamable case. The total size is read
  from the `Content-Range` header (`parseContentRangeTotal` extracts the value
  after the final `/`; `*` or malformed ⇒ size stays `-1`). Sets
  `acceptRanges = true`.
- **200 OK** — server ignored the range; full body would follow. Size comes from
  `Content-Length` (`parseContentLength`, `-1` if missing/malformed),
  `acceptRanges = false`.
- **anything else (405, etc.)** — `headFallback` issues a `HEAD`, reading
  `Content-Length` for size and `Accept-Ranges: bytes` for range support, and
  overriding `etag`/`lastModified` if the HEAD provides them. This never
  hard-fails; only transport errors propagate.

`remoteInfo.size` is `-1` when unknown. `streamable()` requires both
`acceptRanges` and `size > 0`.

---

## 5. Durable resume design

### Sidecar schema — `<output>.dr.json`

Path is `statePath(output) = output + ".dr.json"`. Serialized from the `State`
struct in `state.go`. The real JSON fields:

```json
{
  "url": "https://example.com/file.bin",
  "size": 215040,
  "etag": "\"rt-v1\"",
  "last_modified": "Wed, 21 Oct 2015 07:28:00 GMT",
  "concurrency": 4,
  "chunks": [
    { "index": 0, "start": 0,      "end": 53759,  "done": 53760 },
    { "index": 1, "start": 53760,  "end": 107519, "done": 12288 },
    { "index": 2, "start": 107520, "end": 161279, "done": 0 },
    { "index": 3, "start": 161280, "end": 215039, "done": 0 }
  ]
}
```

- `etag` and `last_modified` are `omitempty`.
- `State.mu sync.Mutex` is unexported, so `encoding/json` ignores it; it guards
  `Chunks` against concurrent `MarkProgress`/`Save`.
- `ChunkState`: `Index`/`Start`/`End` are immutable for the life of the download;
  `Done` is the per-chunk resume cursor (bytes already written for that chunk).
  `Start`/`End` use HTTP Range semantics (both inclusive).

### Atomic save

`State.Save(path)` snapshots the marshalable fields **under `mu`**, then writes
durably and atomically:

1. `json.MarshalIndent` under the lock.
2. Write to `path + ".tmp"`, `f.Sync()`, `f.Close()` (each step removes the temp
   file on error).
3. `os.Rename(tmp, path)` — the atomic publish.

This guarantees a reader (a later `LoadState`) never sees a half-written sidecar.

### `Matches()` validation rules

`State.Matches(info remoteInfo)` decides whether the remote is unchanged enough to
trust the cursors:

1. `s.Size != info.size` ⇒ `false` (size is the first gate).
2. Else, if **both** ETags are present ⇒ result is `s.ETag == info.etag`.
3. Else, if **both** Last-Modified values are present ⇒ result is the equality.
4. Else (no usable validator on either side) ⇒ `false` (resume is unsafe).

`runSegmentedDownload` uses this and then *disambiguates* a `false` result so the
user gets the right message:
- size differs ⇒ `ErrRemoteChanged` reported as a size change;
- a validator differs ⇒ `ErrRemoteChanged` reported as an ETag change;
- no usable validator ⇒ the sidecar is **discarded** and the download restarts
  fresh (not an error). (Tested by `TestRunResumeSizeChangedNoValidator` and
  `TestRunResumeNoValidatorMatchingSizeRestartsFresh`.)

### On-disk partial-file revalidation

Even when `Matches` passes, the cursors are only trustworthy if the partial data
file on disk is consistent with them. `partialFileUsable(output, saved.Size)`:

- returns `false` if `size <= 0` (never the segmented/pre-allocated path);
- `os.Stat`s the output; returns `false` if it is missing;
- returns `fi.Size() == size` — the file must still be at the pre-allocated size.

If this check fails the sidecar is discarded and the run restarts fresh. This is
the guard that prevents resuming against a deleted or truncated file and emitting a
sparse, zero-holed file as "success". Regression coverage:
`TestRunResumeMissingFileRestartsFresh` and
`TestRunResumeTruncatedFileRestartsFresh`.

### Sidecar lifecycle: created / flushed / removed / retained

- **Created/flushed**: only on the segmented path. `runSegmented` runs a ticker
  every `stateFlushInterval` (1s) calling `st.Save(statePath)`, and always does one
  **final** `Save` after the workers finish (or are cancelled). The single-stream
  path never creates a sidecar (verified by `TestRunSingleStreamNoLength`).
- **Removed**: on full success, after verification, via `st.Remove(statePath)`
  (a missing file is not an error).
- **Retained**: on any failure or interruption, `runSegmentedDownload` returns the
  download error without removing anything — the periodic/final flush has already
  persisted progress, so a later run resumes from the cursors.

---

## 6. Concurrency & cancellation model (`pool.go`)

`runSegmented` is the bounded worker pool:

- **Bounded semaphore**: `sem := make(chan struct{}, concurrency)`. Each dispatched
  chunk acquires a slot (`sem <- struct{}{}`) and releases it on goroutine exit
  (`defer func(){ <-sem }()`), capping in-flight fetches at `concurrency`.
- **Derived cancel context**: `ctx, cancel := context.WithCancel(ctx)`. The
  external context (wired to SIGINT/SIGTERM in `cmd`) plus this derived cancel let
  the first fatal error tear down siblings.
- **First-fatal-cancels-siblings**: `errOnce sync.Once` + `firstErr`. The `fail`
  closure records the first error and calls `cancel()` exactly once; all in-flight
  workers observe `ctx.Done()` and stop. Dispatch also checks `ctx.Err()` before
  acquiring a slot and on the `sem`/`ctx.Done()` select, so cancellation halts new
  work promptly.
- **State flush goroutine**: a separate goroutine ticks every
  `stateFlushInterval` and exits on `ctx.Done()`; after `wg.Wait()` the pool calls
  `cancel()`, waits for the flush goroutine (`flushWG.Wait()`), then does a final
  `st.Save`. A final-save error is only surfaced if no download error preceded it.
- **Per-write bookkeeping**: each worker's `onWrite(n)` calls both the caller's
  `onBytes` (progress) and `st.MarkProgress(ch.index, n)` (resume cursor). Already-
  complete chunks (`!ch.remaining().todo`) are skipped at dispatch — this is what
  makes resume cheap.
- **Error wrapping**: chunk failures pass through `classifyChunkError`, which wraps
  with `ErrChunkFailed` **except** for `context.Canceled`/`context.DeadlineExceeded`,
  which propagate unwrapped so callers can `errors.Is` them (see
  `TestClassifyChunkError`).

**Why `WriteAt` at disjoint offsets needs no file lock**: `planChunks` produces
contiguous, **non-overlapping** byte ranges, and `fetchChunk` writes strictly
within `[ch.start, ch.end]` (the over-length guard, below, refuses any surplus).
Because every worker's `WriteAt` targets a region no other worker touches, the
writes commute and never interleave on the same bytes — a single shared `*os.File`
is safe with no mutex. The only shared mutable structure that *does* need a lock is
`State`, guarded by `State.mu`.

---

## 7. Retry & error classification (`retry.go`)

`newRetry(maxRetries, base, rng)` returns a `retryFunc` that:

- checks `ctx.Err()` before each attempt;
- runs `op()`, returning immediately on success;
- returns immediately if `!isRetryable(err)` (fatal);
- otherwise, up to `maxRetries` times, sleeps `backoff(attempt, base, max, rng())`
  on a timer that also selects on `ctx.Done()` (so a cancel during backoff returns
  `ctx.Err()`).

`backoff` is **exponential with full jitter**: the window is `base * 2^attempt`
clamped to `defaultBackoffMax` (30s) with overflow guards, and the actual sleep is
`window * r` where `r` is a `[0,1)` jitter sample. `rng` is injectable so tests are
deterministic (e.g. `fastRetry` in the test helpers).

`isRetryable(err)` classification, in order:

| Condition | Verdict |
|-----------|---------|
| `context.Canceled` / `context.DeadlineExceeded` | fatal |
| `ErrRemoteChanged` | fatal |
| carries `*httpStatusError`: code `429` or `5xx` | retryable |
| carries `*httpStatusError`: any other status (e.g. `200`, `4xx`, `416`) | fatal |
| `ErrRangeNot206` with no status code (e.g. over-length 206) | fatal |
| `io.ErrShortWrite` (local write failure, ENOSPC) | fatal |
| `io.ErrUnexpectedEOF` (mid-stream truncation) | retryable |
| `net.Error` (transport/timeout) | retryable |
| anything else (unknown) | retryable |

The HTTP-status check is deliberately **before** the `ErrRangeNot206` check, so a
non-206 chunk response (which wraps both `ErrRangeNot206` *and* a `httpStatusError`
via `%w: %w`) is classified by its status code: 5xx/429 retry, 200/4xx fail fast.
See `TestIsRetryableExtra`, `TestFetchChunkMidStream200NotRetried`,
`TestFetchChunk416NotRetried`, `TestFetchChunkShortReadIsRetryable`,
`TestFetchChunkOverLength206IsFatal`.

**Sentinel errors** (`download.go`), all wrapped with `%w` so callers use
`errors.Is`:

```
ErrNoURL, ErrUnsupportedScheme, ErrRemoteChanged, ErrSizeMismatch,
ErrChecksumMismatch, ErrRangeNot206, ErrChunkFailed
```

`httpStatusError` (a struct carrying `code`) is matched with `errors.As` to read
the status for classification.

---

## 8. Progress (`progress.go`)

`Progress` is a concurrency-safe reporter:

- **Atomic counter**: `done atomic.Int64`. `Add(n)` increments (called from every
  worker write); `Seed(n)` stores an initial value used when resuming
  (`prog.Seed(st.completedBytes())`).
- **Single render goroutine**: `Start(ctx)` launches one goroutine that redraws on
  a `renderInterval` (200ms) ticker, selecting on `ctx.Done()` and an internal
  `stop` channel. `Stop()` closes `stop` (idempotent), waits, and prints a final
  line with a trailing newline. All other code only mutates the atomic counter, so
  there is exactly one writer to the output stream.
- **Stdlib TTY detection**: `isTTY(f)` calls `f.Stat()` and checks
  `info.Mode()&os.ModeCharDevice != 0` — no third-party terminal library.
- **Suppression rules**: `active()` gates rendering on `!quiet && out != nil &&
  isTTY(out)`. When inactive, `Start`/`Stop` are no-ops and the counter still runs
  but nothing is drawn — so piping to a file or `--quiet` produces no progress
  noise. Rendering shows percent / done / total / rate / ETA when `total > 0`, and
  just done + rate when the size is unknown (`percent` and `eta` guard against
  divide-by-zero).

---

## 9. Correctness decisions / invariants

- **Single pre-allocated file, no temp-merge.** Fresh segmented runs
  `dst.Truncate(info.size)` once and every chunk writes in place via `WriteAt`.
  There is no per-chunk temp file and no merge step (the v1 `MergeFiles` is gone).
  This removes the entire class of merge-ordering/merge-corruption bugs.
- **Bytes-written is the real integrity gate.** Because the file is pre-allocated,
  its on-disk size always equals `info.size` regardless of how many bytes actually
  arrived. So the meaningful check is `st.completedBytes() == info.size`; a short
  server response leaves a zero hole that is reported as `ErrSizeMismatch` instead
  of being accepted.
- **Integer `ceilDiv`.** `planChunks` uses `ceilDiv(a, b) = (a + b - 1) / b` (with
  `b == 0 ⇒ 0`) — pure integer arithmetic, no float rounding. It also clamps the
  chunk count to `min(concurrency, size)` so no chunk is empty, and chunks are
  contiguous and disjoint.
- **Fixed 32 KiB buffer.** `copyBufferSize = 32 * 1024` is the read buffer in
  `fetchChunk`, `runSingle`, and `verifyChecksum`. Buffers are **never** sized to a
  chunk, so memory is bounded regardless of file or chunk size.
- **Over-/under-length protection in `fetchChunk`.** Reads are capped to `want`
  (bytes still owed for the range). A server returning more than the range is a
  fatal `ErrRangeNot206` (the surplus is refused so it can never spill into a
  neighbouring chunk's region and race an unlocked `WriteAt`). A clean EOF that
  delivered fewer bytes is a retryable `io.ErrUnexpectedEOF`, and the `done` cursor
  has already advanced so the retry resumes mid-chunk.
- **Single-stream rejects anomalous 206.** `runSingle` sends no Range header, so
  only `200` is valid; a `206` there is rejected via `httpStatusError` rather than
  written as a complete file (`TestRunSingleStreamRejects206`). `runSingle`
  truncates `dst` to 0 each attempt so a retry is idempotent; the single-stream
  caller `Seed(0)`s progress so retried bytes are not double-counted.
- **http/https only**, enforced twice (`cmd.validateURL`, `download.validateScheme`).
- **Corrupt sidecar = absent.** `LoadState` returns `(nil, nil)` for both a missing
  file and an unparseable one, so a damaged sidecar yields a clean restart rather
  than an error.

---

## 10. Appendix — Defects fixed from v1

This rewrite eliminates eight concrete v1 bugs:

1. **Missing resume.** v1 advertised resume but never honored a sidecar. v2 has a
   real `<output>.dr.json` with per-chunk `done` cursors, `Matches`/
   `partialFileUsable` validation, and a resume path that issues Range requests
   from non-zero offsets (`TestRunInterruptThenResumeRoundTrip`).
2. **Broken `MergeFiles`.** v1 downloaded chunks to temp files and merged them,
   with a faulty merge. v2 deletes the whole merge concept: one pre-allocated file
   written in place via `WriteAt` at disjoint offsets.
3. **No-range corruption.** v1 could write a ranged/partial body as if it were the
   whole file. v2 validates 206 (`fetchChunk` ⇒ `ErrRangeNot206`) on the segmented
   path and rejects an anomalous 206 on the single-stream path.
4. **Float ceil no-op.** v1's chunk-count "ceiling" used float math that rounded to
   a no-op. v2 uses integer `ceilDiv` and clamps to `min(concurrency, size)`.
5. **Progress double-count.** v1 counted retried/overlapping bytes twice. v2 uses a
   single `atomic.Int64`, `Seed`s the resume baseline, and resets to 0 before each
   single-stream retry so bytes are counted once.
6. **Probe hard-fail.** v1 treated a non-206 probe as a fatal error. v2's `probe`
   handles 206/200 and falls back to `HEAD` for other statuses; only transport
   errors propagate.
7. **Huge per-segment buffers.** v1 allocated a buffer per segment proportional to
   segment size. v2 uses a fixed 32 KiB `copyBufferSize` everywhere.
8. **Fake FTP.** v1 pretended to support FTP. v2 supports **only** http/https,
   enforced in both `cmd` and `download`, with `ErrUnsupportedScheme` for anything
   else.

---

## 11. Testing strategy

- **Deterministic `httptest`.** Tests stand up `httptest.NewServer` handlers that
  emulate real server behaviors — ranged 206 responses (`rangedServer`),
  connection-close-framed short reads, over-length 206, plain 200, 200-with-no-
  Content-Length, 416, mid-download cancellation — so behavior is reproducible with
  no network. `baseOpts` builds a quiet `Options` pointed at the test server's
  client; `parseRange`/`parseRangeHeader` parse Range headers in handlers.
- **Table-driven.** Classification and pure-logic tests (e.g. `TestIsRetryableExtra`,
  `TestClassifyChunkError`, chunk planning) use table/subtest structure with
  `t.Parallel()`.
- **Deterministic retry.** `fastRetry` injects a fixed/zero jitter source so backoff
  timing does not flake tests.
- **Regression suite.** `regression_test.go` pins each v1 defect: missing-file and
  truncated-file resume restarts, the full interrupt→resume round trip (asserting
  non-zero-offset Range requests on the second run), size-changed and no-validator
  resume branches, fast-fail on 200/416, retryable short reads, fatal over-length
  206, and single-stream 206 rejection.
- **`-race`.** The suite is intended to run under `go test -race ./...` to validate
  the concurrency model — concurrent `WriteAt` at disjoint offsets plus `State.mu`
  guarding the shared `State` and the atomic progress counter.
