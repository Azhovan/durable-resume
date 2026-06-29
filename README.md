# Durable Resume

A reliable file downloader with durable resume, parallel segmented downloading, and optional integrity verification.

## Key Features

- **Simple CLI**: Just `dr <URL>` - no subcommands required
- **Segmented downloads**: Parallel byte-range chunks written into a single pre-allocated file
- **Durable resume**: Per-chunk progress is persisted to a sidecar and resumed via HTTP Range
- **Live progress**: Speed, transferred/total, and ETA, rendered only on a TTY
- **Integrity**: Final size check plus optional sha256 verification
- **Mirror failover**: Pass `-m/--mirror` alternates for one file; `dr` fails over in order and even resumes the partial across mirrors
- **Flexible**: HTTP/HTTPS, custom output paths, custom headers, and a quiet mode for scripting

## Installation

```shell
go install github.com/azhovan/durable-resume@latest
```

## Usage

### Basic Examples

```shell
# Download to current directory (filename from Content-Disposition or the URL)
dr https://example.com/file.zip

# Download to a specific path
dr https://example.com/file.zip -o myfile.zip

# Download into a directory (the filename is resolved automatically)
dr https://example.com/download?id=42 -o ~/Downloads

# More parallel chunks
dr https://example.com/largefile.iso -c 8

# Disable resume (start fresh, ignore any sidecar)
dr https://example.com/file.zip --resume=false

# Re-download even if the file already exists and is complete
dr https://example.com/file.zip --force

# Stream to stdout to pipe into another program
dr https://example.com/archive.tar.gz -o - | tar xz

# Download several files into a directory
dr https://example.com/a.zip https://example.com/b.zip -o ~/Downloads

# Download every URL listed in a file (one per line; # comments allowed)
dr -i urls.txt -o ~/Downloads

# Mirror failover: try the primary, then each mirror in order, for ONE file
dr https://a.example/file.iso -m https://b.example/file.iso -m https://c.example/file.iso -o file.iso

# Verify integrity with sha256
dr https://example.com/file.zip --checksum sha256:<hex>

# Send custom request headers (repeatable)
dr https://example.com/file.zip -H "Authorization: Bearer <token>"

# Quiet mode for scripting
dr https://example.com/config.json --quiet

# Verbose mode for debugging
dr https://example.com/file.tar.gz --verbose
```

### Command Options

```
Usage: dr <url> [flags]

  -o, --output string        destination file or directory, or - for stdout (default: Content-Disposition or URL name)
  -i, --input-file string    read URLs from a file, one per line (blank/# lines skipped; - = stdin)
  -c, --concurrency int      number of parallel chunks (default 4)
      --resume               resume a previous interrupted download (default true)
  -f, --force                re-download even if the destination already exists
      --checksum string      verify with "sha256:<hex>"
      --timeout duration     per-request HTTP timeout (0 = none) (default 30s)
      --retries int          per-chunk retry attempts (default 3)
  -H, --header stringArray   extra request header "Key: Value" (repeatable)
  -m, --mirror stringArray   alternate URL serving the SAME file; tried in order if the primary fails (repeatable). Only valid with exactly one positional URL.
      --limit-rate string    limit download speed, e.g. 500k, 1M, 1MiB, 100000 (KiB/MiB/GiB 1024-based; 0/empty = unlimited)
      --proxy string         route through proxy URL (http/https/socks5/socks5h); when unset, HTTP_PROXY/HTTPS_PROXY/NO_PROXY env vars are honored
      --json                 emit one machine-readable JSON object per download to stdout (NDJSON); implies --quiet and cannot be combined with -o -
  -q, --quiet                suppress progress output
  -v, --verbose              extra logging
      --version              version for dr
```

`--resume` defaults to `true`; pass `--resume=false` to disable it. Only `http`
and `https` URLs are supported.

### Output filename

When `-o/--output` is omitted (or names an existing directory), `dr` chooses the
filename like `curl -OJ` / `wget --content-disposition`, in this order:

1. an explicit `-o <file>` path (used verbatim);
2. the server's `Content-Disposition` filename, including the RFC 5987
   `filename*` UTF-8 form;
3. the basename of the **final** URL after redirects;
4. the basename of the requested URL;
5. `download` as a last resort.

A server-supplied name is reduced to a single safe path component, so a malicious
`Content-Disposition` cannot write outside the destination directory. On success
`dr` prints `dr: saved to <path>` (unless `--quiet`).

## Progress Display

When stdout is a TTY and `--quiet` is not set, a single line is redrawn in place
roughly five times per second. Sizes and rates use binary units (KiB, MiB, GiB).

When the total size is known:

```
 78.50% 1.20 GiB / 2.10 GiB  45.20 MiB/s  ETA 23s
```

When the size is unknown (no segmented plan), only transferred bytes and rate
are shown:

```
45.30 MiB  12.00 MiB/s
```

## Atomic output

`dr` never writes to the final filename in place. Bytes are staged in a
`<output>.part` file, and only after the download is complete and size +
checksum verification pass is it atomically renamed onto `<output>` (a single
same-directory `os.Rename`). So the final path either does not exist yet or is a
complete, verified file — an observer never sees a half-written or zero-holed
file at the real name. On interruption or failure the `.part` is kept and the
final path is left untouched.

## Writing to stdout

Use `-o -` to stream the downloaded body to standard output, so `dr` composes in
shell pipelines:

```shell
dr https://example.com/archive.tar.gz -o - | tar xz
dr https://example.com/data.json -o - | jq .
```

In this mode all progress and diagnostic output goes to stderr (never stdout, so
the payload is never corrupted), and the download runs as a single sequential
stream — `.part` staging, resume, and skip-if-complete don't apply to a pipe.
Size is still verified against the server's `Content-Length` (a short stream
exits non-zero). `--checksum` and multiple URLs cannot be combined with `-o -`.

## Batch downloads

Pass more than one URL — as positional arguments and/or from a file with
`-i/--input-file` (one URL per line; blank lines and lines starting with `#` are
ignored; `-` reads the list from stdin):

```shell
dr https://example.com/a.zip https://example.com/b.zip -o ~/Downloads
dr -i urls.txt -o ~/Downloads
```

With multiple URLs, `-o` must be an existing directory (each file is named from
its `Content-Disposition`/URL inside it) and `--checksum` is not allowed. The
batch is *continue-on-error*: every URL is attempted, a summary is printed at the
end (`dr: N of M downloads succeeded`, plus a line per failure), and `dr` exits
non-zero if any download failed.

## Machine-readable output (`--json`)

`--json` makes `dr` scriptable: instead of human progress and `dr: ...` lines, it
emits **NDJSON** — one compact JSON object per line — to **stdout**. A single URL
emits exactly one line; a batch emits one line per URL, streamed as each download
completes (so a pipeline sees results incrementally and survives a mid-batch
kill). `--json` is **opt-in**; without it the human output is unchanged.

```shell
# Print the URLs that failed in a batch
dr --json -i urls.txt | jq -c 'select(.success | not) | .url'

# Single download, inspect the verified digest
dr --json --checksum sha256:<hex> -o file.iso https://example.com/file.iso | jq .
```

Each record carries:

| field     | type   | notes                                                              |
|-----------|--------|--------------------------------------------------------------------|
| `url`     | string | the requested (primary) URL                                        |
| `output`  | string | the resolved final path                                            |
| `bytes`   | int    | bytes written to the destination                                   |
| `size`    | int    | probed total size; `-1` when unknown                               |
| `sha256`  | string | verified lowercase hex; present **only** with `--checksum`         |
| `resumed` | bool   | a matching resume sidecar was honored                              |
| `skipped` | bool   | skip-if-complete short-circuit hit (`success` is then `true`)      |
| `source`  | string | the URL that actually served the bytes (a mirror after failover)   |
| `success` | bool   | whether the download succeeded                                     |
| `error`   | string | failure message; present **only** when `success` is `false`        |

`size`, `bytes`, `resumed`, `skipped`, and `success` are always present;
`sha256`, `source`, and `error` are omitted when empty.

IO routing: in `--json` mode **stdout carries only the NDJSON records**. All
human output (the progress bar and `dr: saved to`/`skipping`/verbose lines) is
suppressed — `--json` implies `--quiet` — and any residual diagnostics go to
stderr, so the JSON stream is never corrupted. Records are emitted for **both
success and failure** — even an invalid URL yields a record with `success:false`
and a non-empty `error` (no record is ever dropped); in a batch `dr` still exits
non-zero if any URL failed (no human "N of M" tally is printed). `--json`
**cannot be combined with `-o -`** (both write to stdout) and is rejected before
any download starts. `--json` with `--quiet` is allowed and behaves like `--json`
alone. The multi-URL guards apply identically under `--json`: with more than one
URL, `-o` must name an existing **directory** and `--checksum` is rejected (one
digest cannot validate many files).

## Mirror failover

`-m/--mirror <url>` (repeatable) supplies alternate URLs that serve the **same
file** as the primary positional URL. `dr` tries the primary first, then each
mirror in order, and the download succeeds as soon as any source delivers a
complete, verified file:

```shell
dr https://a.example/file.iso \
   -m https://b.example/file.iso \
   -m https://c.example/file.iso \
   -o file.iso
```

Key behavior:

- **One file only.** `--mirror` requires exactly one positional URL; it cannot be
  combined with batch mode (multiple positional URLs or `-i/--input-file`). Each
  mirror must be a valid `http`/`https` URL.
- **Resume across mirrors.** Failover reuses the same `<output>.part` staging file
  and resume sidecar. When the next mirror reports the same size *and* validator
  (`ETag`/`Last-Modified`) as the partial on disk, `dr` **resumes** from where the
  previous source left off instead of restarting. When the mirror's size/validator
  differs, the partial is discarded and a **fresh** download starts from that
  mirror.
- **What triggers failover.** Any per-source failure advances to the next mirror:
  a connection error, an HTTP 4xx/5xx, an exhausted-retry chunk failure, a size or
  range mismatch, or a checksum mismatch. Only when **every** source is exhausted
  does `dr` fail (reporting each source's error).
- **Cancellation never burns a mirror.** Ctrl-C / `SIGTERM` (a context cancel or
  timeout) aborts immediately without trying the next mirror; the `.part` and
  sidecar are retained so a later run can resume.
- **Checksum is a cross-mirror safety net.** `--checksum sha256:<hex>` is verified
  on the final assembled file regardless of which mirror(s) served the bytes, so a
  corrupt mirror is caught.
- **Output naming is keyed on the primary.** The output filename (and the sidecar's
  recorded URL) is derived from the primary URL, so failover never renames the
  output mid-flight.
- **stdout.** `-m` works with `-o -`, but a pipe cannot be rewound: failover only
  happens *before* any byte is emitted (typically a probe/connect failure). Once a
  source has written bytes to the pipe, a later error is returned rather than
  replaying a mirror (which would duplicate the leading bytes). There is no resume
  in stdout mode. `--checksum` with `-o -` remains rejected.

## Limiting bandwidth

`--limit-rate <rate>` caps the download speed, like `wget --limit-rate` /
`curl --limit-rate`. The cap is an **aggregate** whole-download cap across all
`-c/--concurrency` workers (with `-c 4 --limit-rate 1M` the total is ~1 MiB/s,
not 4 MiB/s), and it applies to every path: segmented, single-stream, and
`-o -` stdout.

The grammar is `<number><unit>`, case-insensitive, where the unit is one of:

| Unit              | Meaning              | Example   | Bytes/sec  |
| ----------------- | -------------------- | --------- | ---------- |
| *(none)* or `b`   | bytes                | `100000`  | 100000     |
| `k` / `K` / `kib` | KiB (1024 bytes)     | `500k`    | 512000     |
| `m` / `M` / `mib` | MiB (1024² bytes)    | `1M`      | 1048576    |
| `g` / `G` / `gib` | GiB (1024³ bytes)    | `2g`      | 2147483648 |

Suffixes are **1024-based** (matching `wget --limit-rate` and the binary units
used in the progress display). A fractional number is allowed (`1.5M` =
1572864 B/s). An empty value, `0`, or omitting the flag means **unlimited** — no
limiter is allocated and throughput is byte-for-byte unchanged. A negative or
unparseable value is rejected with an error before any download starts.

In a batch, the cap is **per-download**: each URL is limited independently (it
is not divided across the batch), matching `wget`'s per-invocation-per-file
semantics.

## Proxy support

`dr` honors the standard proxy environment variables by default, exactly like
`curl`/`wget` and Go's HTTP client: `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`
(and their lowercase forms `http_proxy`, `https_proxy`, `no_proxy`). When none
are set, the connection is direct.

Pass `--proxy <url>` to route a single invocation through an explicit proxy. It
**overrides** the environment for that run, so the `NO_PROXY` bypass list is
**not** consulted (an explicit `--proxy` always proxies). Accepted proxy schemes
are `http`, `https`, `socks5`, and `socks5h` (`socks5h` delegates DNS
resolution to the proxy). The proxy is dialed by the Go standard library, so no
extra dependency is added.

```shell
# Honor the environment proxies (default)
export HTTPS_PROXY=http://proxy.internal:8080
dr https://example.com/file.zip

# Override the environment for this invocation
dr --proxy socks5://127.0.0.1:1080 https://example.com/file.zip
```

The proxy URL is validated before any download starts; an unparseable URL, a
missing host, or an unsupported scheme is rejected up front. The **download**
URL itself is still restricted to `http`/`https` — only the proxy URL may use
`socks5`/`socks5h`.

## Skipping completed downloads

Re-running `dr` on a destination that already exists and is verifiably complete
returns immediately without fetching the body. "Complete" means: the `--checksum`
matches if one was given, otherwise the on-disk size equals the size the server
reports. If completeness cannot be proven (unknown size and no checksum), or the
existing file does not match, `dr` downloads normally. Pass `--force` (`-f`) to
always re-download.

## How Resume Works

On a segmented download, `dr` writes a sidecar named `<output>.part.dr.json`
alongside the `.part` staging file. The sidecar records:

- the per-chunk byte cursors (how many bytes of each chunk are already on disk), and
- a remote validator: the total size plus the server's `ETag` and/or
  `Last-Modified`.

Re-running the same command reloads the sidecar and continues each unfinished
chunk with an HTTP `Range` request, skipping bytes already written. Bytes land
directly in the pre-allocated `.part` file via `WriteAt`, so there is no merge
step.

On success the `.part` is renamed onto the final path and the sidecar is
removed. On interruption or failure both are retained so the next run can
resume. A stale `.part` left by an unrelated earlier attempt is overwritten when
a fresh download starts.

If the remote has changed since the saved state - a different size, or a
different `ETag`/`Last-Modified` - resuming is refused rather than silently
producing a corrupt file. The failure surfaces as `download: ... remote changed
since saved state; cannot resume`. Use `--resume=false` to discard the sidecar
and start fresh.

## Error Handling

On failure, `dr` prints a single concise line to stderr and exits non-zero:

```
dr: <error>
```

The download engine returns wrapped sentinel errors, so the message identifies
the cause. Examples:

```
dr: scheme "ftp": download: only http and https are supported
dr: download: checksum mismatch
dr: download: size 1048576 vs 2097152: download: remote changed since saved state; cannot resume
dr: invalid checksum hex "zz": encoding/hex: invalid byte: U+007A 'z'
```

## Building & Testing

```shell
# Build the dr binary; ldflags inject version, revision, and date
make build

# Run the test suite with the race detector
make test

# Install into $GOBIN / $GOPATH/bin
go install github.com/azhovan/durable-resume@latest
```

`make build` stamps `main.Version`, `main.Revision`, and `main.Date` via
`-ldflags`; the values are surfaced by `dr --version`. `make test` runs
`go test ./... -race`.

## Roadmap

The following are potential future work and are not yet implemented:

- Dynamic segment adjustment based on network conditions
- Configuration file support
- Download queue management

## Contributing

Contributions are welcome. Please open an issue or pull request.
