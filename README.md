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
# Download to current directory (filename derived from the URL)
dr https://example.com/file.zip

# Download to a specific path
dr https://example.com/file.zip -o myfile.zip

# More parallel chunks
dr https://example.com/largefile.iso -c 8

# Disable resume (start fresh, ignore any sidecar)
dr https://example.com/file.zip --resume=false

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

  -o, --output string        destination path (default: derived from URL)
  -c, --concurrency int      number of parallel chunks (default 4)
      --resume               resume a previous interrupted download (default true)
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

## How Resume Works

On a segmented download, `dr` writes a sidecar named `<output>.dr.json` next to
the destination file. The sidecar records:

- the per-chunk byte cursors (how many bytes of each chunk are already on disk), and
- a remote validator: the total size plus the server's `ETag` and/or
  `Last-Modified`.

Re-running the same command reloads the sidecar and continues each unfinished
chunk with an HTTP `Range` request, skipping bytes already written. Bytes land
directly in the pre-allocated destination file via `WriteAt`, so no temporary
files or merge step are involved.

The sidecar is removed after a successful, fully verified download, and retained
on interruption or failure so the next run can resume.

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
