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
  - `completion.go` — the single sanctioned `completion [bash|zsh|fish|powershell]` helper subcommand plus per-flag/positional completion wiring (`registerCompletions`, called at the end of `NewRootCmd`).
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
- **Wrap errors with `%w` and reuse exported sentinels.** New failure modes that callers/tests may branch on should use (or add to) the sentinels in `download.go`: `ErrNoURL`, `ErrUnsupportedScheme`, `ErrRemoteChanged`, `ErrSizeMismatch`, `ErrChecksumMismatch`, `ErrRangeNot206`, `ErrChunkFailed`, `ErrInvalidProxy`, `ErrAllSourcesFailed`. Keep the `download:`/`verify:` message prefixes.
- **Deterministic tests.** Use `net/http/httptest` — NEVER hit the real network. No flaky `time.Sleep` races; inject the retry rng (`newRetry(..., rng)`) and use tiny base backoff. The injectable seams exist for this: `Options.Client`, `Options.Out`, the retry `rng`.
- **Segmented writes are lock-free by construction.** Every chunk writes via `WriteAt` at disjoint absolute offsets, so file writes need no mutex. `fetchChunk` caps reads to the bytes still owed (`want`) and fails fatally on an over-length 206 so a misbehaving server can never spill into a neighbour's region. Do not add a write lock, and do not let any path write past `ch.end`.
- **`gofmt`/`gofumpt` clean** before commit (see above).
- **Sidecar removed only on success.** `st.Remove(sp)` runs only after a clean download + size/checksum verify. On any failure or interrupt, retain both the sidecar and the partial file so the next run can resume.
- **`completion` is the ONE sanctioned subcommand.** dr's identity is "binary `dr`, no subcommands", with the single conventional exception `dr completion [bash|zsh|fish|powershell]` (gh/kubectl precedent). cobra v1.8.0 does NOT auto-add a completion command for a no-subcommand root (`InitDefaultCompletionCmd` early-returns on `!c.HasSubCommands()`), so it is added explicitly in `cmd/completion.go` via `registerCompletions(cmd)` at the END of `NewRootCmd` (after all flags). It does NOT change root `Args` (still `cobra.ArbitraryArgs`), `RunE`, or download dispatch — `dr <url>` is byte-for-byte unchanged. Path flags `-o`/`-i` complete files (registered func returning `ShellCompDirectiveDefault`, NOT `MarkFlagFilename`); every value flag (`concurrency`/`timeout`/`retries`/`header`/`mirror`/`limit-rate`/`proxy`) and the positional URLs use `cobra.NoFileCompletions`; `--checksum` hints `sha256:`. No new deps (cobra's own generators). Do NOT "fix" this by deleting the subcommand or adding others.

## Download-engine invariants

- **HTTP client / proxy.** `httpClient(opts)` returns an injected `opts.Client` verbatim (the test seam; `Options.Proxy` ignored). Otherwise it clones `http.DefaultTransport` (preserving pooling/TLS/HTTP-2) and sets `transport.Proxy`: with an explicit `Options.Proxy` it is `http.ProxyURL(parsed)` which OVERRIDES the env and ignores `NO_PROXY`; otherwise it stays `http.ProxyFromEnvironment` (honors `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` + lowercase). Accepted proxy schemes: http, https, socks5, socks5h (dialed natively by net/http; no new dep). The download URL itself stays http/https (`validateScheme`/`validateURL` unchanged). cmd validates `--proxy` (`cmd.parseProxy`) before any download; `download.parseProxyURL` re-validates inside `httpClient` returning `ErrInvalidProxy`. One client feeds the probe and every strategy.
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
- **Mirror failover (Phase 1, sequential).** `opts.sources()` = `[opts.URL (primary), opts.Mirrors...]` (empty mirror entries skipped). `Run` validates every source's scheme, builds ONE client + ONE shared aggregate limiter, then: stdout -> `runStdoutSources`; file -> a single PRIMARY probe to resolve the output name and check skip-if-complete ONCE, then `runSources` (the OUTER failover loop). `runSources` reuses the primary probe for `source[0]`, and `attempt` probes each subsequent source and dispatches the per-source strategy (`runSingleStream`/`runSegmentedDownload`), threading `source` and a `multi` flag. A per-source NON-ctx error advances to the next source; a ctx cancel/deadline (checked via BOTH `ctxCanceled(ctx)` and `isCtxErr(err)`) aborts ALL sources immediately and NEVER burns a mirror — including on the primary probe in `Run`. Output naming and the sidecar `State.URL` are keyed on the PRIMARY (`resolveOutputPath`/`newState` use `opts.URL`), so failover never renames the output. Resume-across-mirrors reuses the SAME `.part` + sidecar via the unchanged `State.Matches` + `partialFileUsable` gate: a mirror with matching size+validator RESUMES; a mismatch DISCARDS + restarts fresh — but the discard-on-mismatch branch is gated on `multi` (len(sources)>1); single-source keeps the exact `ErrRemoteChanged` semantics. `.part`/sidecar are RETAINED on every per-source failure. ALL sources exhausted -> `fmt.Errorf("%w: %w", ErrAllSourcesFailed, errors.Join(perSourceErrs...))` (so `errors.Is` matches the aggregate AND each wrapped per-source sentinel); single-source returns its lone error UNWRAPPED. stdout failover has NO resume and may only fail over while zero bytes were emitted (`runStdoutStream` returns the byte count). **Single-source (no `--mirror`) is byte-for-byte identical to before.** cmd: repeatable `--mirror/-m` (StringArray), validated http/https, rejected with batch (>1 positional or `-i`).

- **Structured-result output seam (`Options.Result *Result`).** `download/result.go` defines the exported `Result` (snake_case json tags: `url`, `output`, `bytes`, `size`, `sha256` omitempty, `resumed`, `skipped`, `source` omitempty; NO omitempty on `size`/`bytes`/`resumed`/`skipped`). When `Options.Result` is non-nil, `Run` populates it (best-effort, on success AND failure) via the single guarded helper `setResult(opts, mutate)`, which is a NO-OP when `Result == nil`. The HUMAN path (Result nil — every production caller and existing test) is byte-for-byte unchanged: `Run`'s signature stays `Run(ctx, opts) error`, and the pointer survives `Run`'s value-copy of `opts`. `sha256` is the VERIFIED expected hex echoed from `strings.ToLower(opts.Checksum.Hex)` ONLY after `verifyChecksum` returns nil (nil iff the file's digest equals it) — **do NOT change `verifyChecksum`'s signature** (it is called directly by `verify_test.go` and `skip_test.go`) and do NOT add always-on hashing. `runStdoutSources` is intentionally left unplumbed (stdout mode is rejected under `--json`). `source` is set in BOTH `runSources` branches (single-source `srcs[0]` and the multi-loop winning `src`).
- **`--json` IO routing (cmd).** `--json` emits NDJSON (one compact `json.Marshal`ed object + `\n` per download via `cmd.OutOrStdout()`; single URL = one line, batch = one per URL streamed). cmd owns `jsonRecord` (embeds `download.Result` + `success` bool + `error` omitempty), `recordFor`, `emitJSON`. STDOUT carries ONLY the NDJSON: cmd sets `base.Quiet = true` (kills progress + short-circuits `savedf`/`skippedf`/`vlogf`) and `base.Out = os.Stderr` before the single/batch split. Records are emitted for success AND failure, INCLUDING an invalid single URL: the single-URL `--json` branch does NOT early-return on `validateURL` failure (it seeds `res.URL`/`res.Size = -1`, sets `rerr = validateURL(...)`, runs only when valid, then ALWAYS emits one record), matching the batch path and the "record for every URL" contract. `--json` + `-o -` is REJECTED fail-fast in `RunE` (both own stdout). `--json` + `--quiet` is harmless (redundant). The multi-URL guards (`-o -`, `--checksum`, and non-directory `-o` rejection) are HOISTED into a single `len(urls) > 1` block that runs before BOTH the `--json` batch loop and the non-json batch path, so `--json` cannot bypass them (a plain-file `-o` with multiple URLs would otherwise collide every download onto one path). Single-URL failure returns the content-free `errJSONFailed` AFTER emitting the record (the record carries the real error); batch returns `errBatchFailed` iff any failed and never calls `writeSummary`. The non-json single + batch paths are byte-for-byte unchanged; `runFunc`'s type is unchanged so its test reassignments compile untouched.

## More detail

Start with the `download` package comment in `download.go` (the end-to-end flow), then read
`Designs/ARCHITECTURE.md` for the full v2 design (flow diagram, sidecar schema, resume rules,
retry classification, and the v1 defects this rewrite fixed).
