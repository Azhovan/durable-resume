# dr — Reference

Deep reference detail relocated out of the [README](../README.md) to keep it skimmable. For engine internals (flow diagram, sidecar schema, retry classification, the v1 defects the rewrite fixed), see [Designs/ARCHITECTURE.md](../Designs/ARCHITECTURE.md). The authoritative, always-current flag list is `dr --help`.

## NDJSON record schema (`--json`)

`--json` emits one compact JSON object per download to stdout (NDJSON), implying `--quiet` for human output. Each record carries:

| field | type | notes |
|---|---|---|
| `url` | string | the requested (primary) URL |
| `output` | string | the resolved final path |
| `bytes` | int | bytes written to the destination |
| `size` | int | probed total size; `-1` when unknown |
| `sha256` | string | verified lowercase hex; present **only** with `--checksum` |
| `resumed` | bool | a matching resume sidecar was honored |
| `skipped` | bool | skip-if-complete short-circuit hit (`success` is then `true`) |
| `source` | string | the URL that actually served the bytes (a mirror after failover) |
| `success` | bool | whether the download succeeded |
| `error` | string | failure message; present **only** when `success` is `false` |

`size`, `bytes`, `resumed`, `skipped`, and `success` are always present; `sha256`, `source`, and `error` are omitted when empty.

**IO routing.** In `--json` mode stdout carries only the NDJSON records; all human output is suppressed and any residual diagnostics go to stderr, so the stream is never corrupted. Records are emitted for both success and failure — even an invalid URL yields a record with `success:false` and a non-empty `error`; no record is ever dropped. In a batch `dr` still exits non-zero if any URL failed (no human "N of M" tally is printed). `--json` cannot be combined with `-o -` (both write to stdout) and is rejected before any download starts. The multi-URL guards apply identically: with more than one URL, `-o` must name an existing directory and `--checksum` is rejected.

## `--limit-rate` grammar

The cap is an aggregate whole-download cap across all `-c/--concurrency` workers and applies to every path (segmented, single-stream, `-o -`). The grammar is `<number><unit>`, case-insensitive:

| Unit | Meaning | Example | Bytes/sec |
|---|---|---|---|
| *(none)* or `b` | bytes | `100000` | 100000 |
| `k` / `K` / `kib` | KiB (1024 bytes) | `500k` | 512000 |
| `m` / `M` / `mib` | MiB (1024² bytes) | `1M` | 1048576 |
| `g` / `G` / `gib` | GiB (1024³ bytes) | `2g` | 2147483648 |

Suffixes are 1024-based (matching `wget --limit-rate` and the binary units in the progress display). A fractional number is allowed (`1.5M` = 1572864 B/s). An empty value, `0`, or omitting the flag means unlimited — no limiter is allocated and throughput is byte-for-byte unchanged. A negative or unparseable value is rejected before any download starts. In a batch the cap is **per-download**: each URL is limited independently, matching `wget`'s per-invocation-per-file semantics.

## Mirror failover — full behavior

`-m/--mirror` supplies alternate URLs serving the same file as the primary. `dr` tries the primary, then each mirror in order, succeeding as soon as any source delivers a complete, verified file.

- **One file only.** `--mirror` requires exactly one positional URL; it cannot be combined with batch mode (multiple positional URLs or `-i`). Each mirror must be a valid `http`/`https` URL.
- **Resume across mirrors.** Failover reuses the same `<output>.part` staging file and sidecar. When the next mirror reports the same size *and* validator (`ETag`/`Last-Modified`) as the partial on disk, `dr` resumes from where the previous source left off; when they differ, the partial is discarded and a fresh download starts from that mirror.
- **What triggers failover.** Any per-source failure advances to the next mirror: a connection error, an HTTP 4xx/5xx, an exhausted-retry chunk failure, a size or range mismatch, or a checksum mismatch. Only when every source is exhausted does `dr` fail (reporting each source's error).
- **Cancellation never burns a mirror.** Ctrl-C / `SIGTERM` (a context cancel or timeout) aborts immediately without trying the next mirror; the `.part` and sidecar are retained so a later run can resume.
- **Checksum is a cross-mirror safety net.** `--checksum sha256:<hex>` is verified on the final assembled file regardless of which mirror(s) served the bytes, so a corrupt mirror is caught.
- **Output naming is keyed on the primary.** The output filename (and the sidecar's recorded URL) is derived from the primary URL, so failover never renames the output mid-flight.
- **stdout.** `-m` works with `-o -`, but a pipe cannot be rewound: failover only happens *before* any byte is emitted (typically a probe/connect failure). Once a source has written bytes to the pipe, a later error is returned rather than replaying a mirror (which would duplicate the leading bytes). There is no resume in stdout mode, and `--checksum` with `-o -` remains rejected.

## How resume works (internals)

On a segmented download, `dr` writes a sidecar named `<output>.part.dr.json` alongside the `.part` staging file. The sidecar records:

- the per-chunk byte cursors (how many bytes of each chunk are already on disk), and
- a remote validator: the total size plus the server's `ETag` and/or `Last-Modified`.

Re-running the same command reloads the sidecar and continues each unfinished chunk with an HTTP `Range` request, skipping bytes already written. Bytes land directly in the pre-allocated `.part` file via `WriteAt`, so there is no merge step. On success the `.part` is renamed onto the final path and the sidecar is removed; on interruption or failure both are retained. A stale `.part` from an unrelated earlier attempt is overwritten when a fresh download starts.

If the remote changed since the saved state (a different size, or a different `ETag`/`Last-Modified`), resuming is refused rather than silently producing a corrupt file. The failure surfaces as `download: ... remote changed since saved state; cannot resume`. Use `--resume=false` to discard the sidecar and start fresh.

## Proxy details

`dr` honors `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` (and lowercase forms) by default; when none are set, the connection is direct. `--proxy <url>` overrides the environment for that run, so the `NO_PROXY` bypass list is not consulted (an explicit `--proxy` always proxies). Accepted proxy schemes are `http`, `https`, `socks5`, `socks5h` (`socks5h` delegates DNS resolution to the proxy). The proxy URL is validated before any download starts; an unparseable URL, a missing host, or an unsupported scheme is rejected up front. The download URL itself is always restricted to `http`/`https` — only the proxy may use `socks5`/`socks5h`. The proxy is dialed by the Go standard library, so no extra dependency is added.
