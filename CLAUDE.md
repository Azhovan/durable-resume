# CLAUDE.md

Guidance for AI agents and contributors working in this repo. Read this before making changes.

## Project summary

`durable-resume` (binary `dr`) is a single-binary CLI that downloads one URL into a local
file, correctly and resumably. It probes the remote for size and range support, splits the
file into disjoint byte chunks, downloads not-yet-complete chunks concurrently via
`os.File.WriteAt` into a single pre-allocated destination, and verifies size (and optional
sha256) before finishing. Progress and resume state survive interruption: a durable JSON
sidecar tracks per-chunk progress so a later run resumes instead of restarting, and the
sidecar is removed only on verified success. The codebase deliberately depends on almost
nothing (cobra + testify) and keeps all I/O and concurrency in the stdlib.

## Repo layout

- `main.go` — entrypoint; injects `Version`/`Revision`/`Date` via `-ldflags` and calls `cmd.NewRootCmd(...).Execute()`.
- `cmd/` — cobra CLI (`root.go`): flag parsing, URL/header/checksum validation, SIGINT/SIGTERM context, builds `download.Options`, calls `download.Run`.
- `download/` — the engine (one concern per file):
  - `download.go` — `Run`, `Options`, sentinel errors, strategy selection, segmented orchestration, verify+cleanup.
  - `probe.go` — `probe`: ranged `GET bytes=0-0` then HEAD fallback; learns size/ranges/etag/last-modified.
  - `chunk.go` — chunk plan (`planChunks`) and per-chunk fetch (`fetchChunk`, `WriteAt`).
  - `pool.go` — bounded worker pool (`runSegmented`), single-stream path (`runSingle`), periodic state flush.
  - `state.go` — the resume sidecar `State` (`<output>.dr.json`): load/save/match/mark-progress/remove.
  - `retry.go` — exponential backoff + jitter and `isRetryable` classification.
  - `progress.go` — concurrency-safe live progress + stdlib TTY detection (`isTTY`).
  - `verify.go` — `verifySize`, `verifyChecksum`.
  - `*_test.go` — unit + regression tests, all using `net/http/httptest`.
- `Designs/ARCHITECTURE.md` — the single authoritative design reference for v2 (flow, types, sidecar schema, resume rules, retry classification). The `download.go` package comment is the most concise full-flow description.

## Build / test / lint

- `make build` — cross-builds `dr` with version ldflags and `-trimpath`.
- `make test` — `go test ./... -race`. Always run with `-race`; the engine is concurrent.
- `go vet ./...` (also `make vet`).
- Formatting: code must be `gofmt` AND `gofumpt` clean. CI runs both; `make fmt` only runs `gofmt`, so run `gofumpt -l .` yourself (`go install mvdan.cc/gofumpt@latest`).
- CI: `.github/workflows/lint.yml` runs `go vet`, `gofmt`, and `gofumpt`. `.github/workflows/test.yml` runs `go test -v ./...`. Go 1.22 (lint job uses 1.23); module is `go 1.22`.

## Hard constraints (MUST keep)

- **No new dependencies.** Only `github.com/spf13/cobra` and `github.com/stretchr/testify` (test only) are allowed. Everything else is stdlib. Do not add a TTY/terminal library, an HTTP client library, a progress-bar library, etc.
- **Stdlib-only TTY detection.** `isTTY` uses `f.Stat()` + `os.ModeCharDevice`. Do not introduce `golang.org/x/term` or `isatty`.
- **http/https only.** `validateScheme`/`validateURL` reject every other scheme with `ErrUnsupportedScheme`. No ftp/file/etc.
- **Wrap errors with `%w` and reuse exported sentinels.** New failure modes that callers/tests may branch on should use (or add to) the sentinels in `download.go`: `ErrNoURL`, `ErrUnsupportedScheme`, `ErrRemoteChanged`, `ErrSizeMismatch`, `ErrChecksumMismatch`, `ErrRangeNot206`, `ErrChunkFailed`. Keep the `download:`/`verify:` message prefixes.
- **Deterministic tests.** Use `net/http/httptest` — NEVER hit the real network. No flaky `time.Sleep` races; inject the retry rng (`newRetry(..., rng)`) and use tiny base backoff. The injectable seams exist for this: `Options.Client`, `Options.Out`, the retry `rng`.
- **Segmented writes are lock-free by construction.** Every chunk writes via `WriteAt` at disjoint absolute offsets, so file writes need no mutex. `fetchChunk` caps reads to the bytes still owed (`want`) and fails fatally on an over-length 206 so a misbehaving server can never spill into a neighbour's region. Do not add a write lock, and do not let any path write past `ch.end`.
- **`gofmt`/`gofumpt` clean** before commit (see above).
- **Sidecar removed only on success.** `st.Remove(sp)` runs only after a clean download + size/checksum verify. On any failure or interrupt, retain both the sidecar and the partial file so the next run can resume.

## Download-engine invariants

- **Strategy selection.** `remoteInfo.streamable()` == `acceptRanges && size > 0`. Streamable -> `runSegmentedDownload` (concurrent chunks). Non-streamable (no ranges or unknown size) -> `runSingleStream` (one sequential stream, no sidecar, size verify skipped when size unknown).
- **Probe contract.** `probe` issues `GET Range: bytes=0-0`: `206` -> size from `Content-Range` total + `acceptRanges=true`; `200` -> size from `Content-Length`, no ranges; anything else -> HEAD fallback (size from `Content-Length`, ranges from `Accept-Ranges: bytes`). Only transport errors propagate; a non-206 is not a hard failure.
- **Resume sidecar contract** (`<output>.dr.json`):
  - Resume only if `State.Matches(info)`: `Size` must match, then `ETag` if both present, else `Last-Modified` if both present; **no usable validator => not a match** (resume is unsafe). On size mismatch return `ErrRemoteChanged` (size variant); on validator mismatch return `ErrRemoteChanged` (etag variant).
  - Even when the sidecar matches, the on-disk file must still exist at exactly the pre-allocated `Size` (`partialFileUsable`); otherwise discard the sidecar and start fresh (trusting stale done-cursors would leave zero-filled holes).
  - A missing OR corrupt (unmarshal-failing) sidecar is treated as absent (`LoadState` returns `(nil, nil)`) so a clean restart is always possible.
  - Fresh runs pre-allocate the destination with `Truncate(size)`; resume runs must NOT truncate.
  - `State.Save` is atomic (temp file + `fsync` + `os.Rename`) and concurrency-safe (`State.mu` guards `Chunks`). The pool flushes periodically (`stateFlushInterval`) and once more on exit.
  - Segmented success requires `st.completedBytes() == info.size` (a short server response is reported as `ErrSizeMismatch`), since the pre-allocated file's on-disk size alone cannot detect zero holes.
- **Retry classification** (`isRetryable`): retry on `429`, `5xx`, transient `net.Error`, and mid-stream `io.ErrUnexpectedEOF`; fail fast on `4xx` (non-429), `ErrRemoteChanged`, `ErrRangeNot206` (carrying no status), `io.ErrShortWrite` (local write failure), and context cancel/deadline. HTTP status is checked via `httpStatusError` before the `ErrRangeNot206` sentinel so a non-206 chunk response is classified by its real status code. Unknown errors are retryable.
- **Chunk fetch.** `fetchChunk` resumes from `start + done`, requires `206`, advances `ch.done` and calls `onBytes`/`MarkProgress` after each `WriteAt`, and surfaces a short read as retryable `io.ErrUnexpectedEOF` so the chunk resumes from its advanced cursor. Uses a fixed `copyBufferSize` buffer — never size a buffer to a chunk.

## More detail

Start with the `download` package comment in `download.go` (the end-to-end flow), then read
`Designs/ARCHITECTURE.md` for the full v2 design (flow diagram, sidecar schema, resume rules,
retry classification, and the v1 defects this rewrite fixed).
