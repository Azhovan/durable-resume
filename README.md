# Durable Resume

A reliable file downloader with durable resume, parallel segmented downloading, and optional integrity verification.

## Key Features

- **Simple CLI**: Just `dr <URL>` - no subcommands required
- **Segmented downloads**: Parallel byte-range chunks written into a single pre-allocated file
- **Durable resume**: Per-chunk progress is persisted to a sidecar and resumed via HTTP Range
- **Live progress**: Speed, transferred/total, and ETA, rendered only on a TTY
- **Integrity**: Final size check plus optional sha256 verification
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

  -o, --output string        destination file or directory (default: Content-Disposition or URL name)
  -c, --concurrency int      number of parallel chunks (default 4)
      --resume               resume a previous interrupted download (default true)
  -f, --force                re-download even if the destination already exists
      --checksum string      verify with "sha256:<hex>"
      --timeout duration     per-request HTTP timeout (0 = none) (default 30s)
      --retries int          per-chunk retry attempts (default 3)
  -H, --header stringArray   extra request header "Key: Value" (repeatable)
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
- Bandwidth limiting
- Proxy support

## Contributing

Contributions are welcome. Please open an issue or pull request.
