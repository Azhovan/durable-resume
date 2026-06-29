# dr — durable, resumable, parallel downloads

[![CI](https://github.com/azhovan/durable-resume/actions/workflows/test.yml/badge.svg)](https://github.com/azhovan/durable-resume/actions/workflows/test.yml)
![Go 1.22+](https://img.shields.io/badge/go-1.22%2B-00ADD8)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A single-binary download manager (Go, stdlib-only) built around bulletproof resume: parallel segmented downloads, durable per-chunk resume, atomic output, and mirror failover — the resilience `curl`/`wget` lack, without `aria2`'s footprint. The binary is `dr`; there are no subcommands.

## Why dr

| Capability | dr | curl | wget | aria2 |
|---|---|---|---|---|
| Parallel segmented (byte-range) download | yes | no | no | yes |
| Durable per-chunk resume (survives crash/Ctrl-C) | yes | partial | partial | yes |
| Resume across mirrors | yes | no | no | partial |
| Mirror failover (alternate sources, in order) | yes | no | no | yes |
| Atomic output (`.part` → rename, no half-written file) | yes | no | no | no |
| sha256 verification | yes | no | no | yes |
| Batch (multiple URLs / `-i` list, continue-on-error) | yes | yes | yes | yes |
| Aggregate `--limit-rate` across workers | yes | per-conn | yes | yes |
| Machine-readable NDJSON output | yes | no | no | no |
| Single static binary, no non-stdlib deps | yes | — | — | no |

`partial`/`per-conn` are honest qualifiers, not knocks: e.g. `curl -C -`/`wget -c` resume a single sequential stream but not per-chunk; `aria2` does mirror failover but without `dr`'s atomic single-file staging.

## Install

```shell
go install github.com/azhovan/durable-resume/v3@latest
```

Or build from source — see [Build & test](#build--test).

### Shell completions

`completion` is `dr`'s single helper subcommand (the conventional gh/kubectl exception); it does not change the no-subcommand download UX. Enable for the current shell:

```shell
source <(dr completion bash)                          # bash (needs bash-completion v2)
source <(dr completion zsh)                           # zsh  (once: autoload -U compinit; compinit)
dr completion fish | source                           # fish
dr completion powershell | Out-String | Invoke-Expression   # PowerShell
```

Run `dr completion --help` for persistent-install one-liners per shell.

## Quickstart

```shell
# Parallel, resumable download. Ctrl-C, then re-run the SAME command to resume.
dr https://example.com/big.iso -c 8

# Mirror failover for ONE file: try primary, then each mirror in order; resumes across them.
dr https://a.example/big.iso -m https://b.example/big.iso -m https://c.example/big.iso -o big.iso

# Batch: download every URL in a file into a directory (continue-on-error).
dr -i urls.txt -o ~/Downloads

# Stream to stdout to compose in a pipeline.
dr https://example.com/archive.tar.gz -o - | tar xz

# Verify integrity.
dr https://example.com/big.iso --checksum sha256:<hex>
```

## Output filenames

When `-o/--output` is omitted (or names an existing directory), `dr` chooses the filename like `curl -OJ` / `wget --content-disposition`, in order:

1. an explicit `-o <file>` path (used verbatim);
2. the server's `Content-Disposition` filename (including the RFC 5987 `filename*` UTF-8 form);
3. the basename of the **final** URL after redirects;
4. the basename of the requested URL;
5. `download` as a last resort.

A server-supplied name is reduced to a single safe path component, so a malicious `Content-Disposition` cannot write outside the destination directory. On success `dr` prints `dr: saved to <path>` (unless `--quiet`).

## Behavior & guarantees

**Atomic output.** `dr` never writes the final filename in place. Bytes are staged in `<output>.part`, and only after the download completes and size + checksum verification pass is it atomically renamed onto `<output>` (a single same-directory `os.Rename`). The final path is therefore either absent or a complete, verified file — an observer never sees a half-written or zero-holed file. On interruption or failure the `.part` is kept and the final path is untouched.

**Durable resume.** A segmented download tracks per-chunk progress in a sidecar at `<output>.part.dr.json`. Re-running the same command resumes each unfinished chunk via an HTTP `Range` request — there is no merge step. If the remote changed (different size or `ETag`/`Last-Modified`), resume is refused rather than silently producing a corrupt file; pass `--resume=false` to discard the sidecar and start fresh. Resume defaults to on. (Internals: [docs/REFERENCE.md](docs/REFERENCE.md) and [Designs/ARCHITECTURE.md](Designs/ARCHITECTURE.md).)

**Skip-if-complete.** Re-running on a destination that already exists and is verifiably complete returns immediately without fetching. "Complete" means the `--checksum` matches if given, otherwise the on-disk size equals the size the server reports; if completeness cannot be proven (unknown size and no checksum), `dr` downloads normally. Pass `--force` (`-f`) to always re-download.

**Progress display.** When stdout is a TTY and `--quiet` is not set, a single line is redrawn in place ~5 times/second, using binary units (KiB/MiB/GiB):

```
 78.50% 1.20 GiB / 2.10 GiB  45.20 MiB/s  ETA 23s     # size known
45.30 MiB  12.00 MiB/s                                # size unknown
```

## Advanced usage

### Batch downloads

Pass multiple URLs as positional arguments and/or via `-i/--input-file` (one URL per line; blank lines and `#` comments ignored; `-` reads the list from stdin):

```shell
dr https://example.com/a.zip https://example.com/b.zip -o ~/Downloads
dr -i urls.txt -o ~/Downloads
```

With multiple URLs, `-o` must be an existing directory and `--checksum` is not allowed. The batch is *continue-on-error*: every URL is attempted, a summary is printed (`dr: N of M downloads succeeded`, plus a line per failure), and `dr` exits non-zero if any failed.

### Writing to stdout

`-o -` streams the body to stdout so `dr` composes in pipelines:

```shell
dr https://example.com/archive.tar.gz -o - | tar xz
dr https://example.com/data.json -o - | jq .
```

In this mode the body goes to stdout and all progress/diagnostics go to stderr (the payload is never corrupted). The download runs as a single sequential stream — `.part` staging, resume, and skip-if-complete don't apply to a pipe — but size is still verified against `Content-Length`. `--checksum`, multiple URLs, and `--json` cannot be combined with `-o -`.

### Mirror failover

`-m/--mirror <url>` (repeatable) supplies alternate URLs serving the **same file** as the primary positional URL. `dr` tries the primary first, then each mirror in order, succeeding as soon as any source delivers a complete, verified file:

```shell
dr https://a.example/file.iso -m https://b.example/file.iso -m https://c.example/file.iso -o file.iso
```

- **One file only** — `--mirror` requires exactly one positional URL; it cannot be combined with batch mode.
- **Resume across mirrors** — failover reuses the same `.part` and sidecar; if the next mirror reports the same size and validator, `dr` resumes instead of restarting (otherwise it starts fresh from that mirror).
- **Checksum is a cross-mirror safety net** — `--checksum` is verified on the final file regardless of which mirror served the bytes, so a corrupt mirror is caught.

Failover triggers, cancellation behavior, output naming, and stdout caveats: [docs/REFERENCE.md](docs/REFERENCE.md).

### Rate limiting

`--limit-rate <rate>` caps download speed (like `wget`/`curl --limit-rate`). The cap is an **aggregate** across all `-c` workers (`-c 4 --limit-rate 1M` ≈ 1 MiB/s total, not 4) and applies to every path (segmented, single-stream, stdout). The grammar is a number with an optional 1024-based suffix (`k`/`m`/`g` or `kib`/`mib`/`gib`); e.g. `500k`, `1.5M`. A bare number is bytes/s; `0`/empty is unlimited. In a batch the cap is per-download. Full unit table: [docs/REFERENCE.md](docs/REFERENCE.md).

### Proxy

`dr` honors the standard proxy environment variables by default (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, and lowercase forms). Pass `--proxy <url>` to route one invocation through an explicit proxy; it **overrides** the environment, so `NO_PROXY` is not consulted. Accepted proxy schemes are `http`, `https`, `socks5`, `socks5h` (proxy only — the **download** URL stays `http`/`https`). The proxy is dialed by the Go stdlib, so no extra dependency is added.

```shell
export HTTPS_PROXY=http://proxy.internal:8080
dr https://example.com/file.zip                                  # honor env (default)
dr --proxy socks5://127.0.0.1:1080 https://example.com/file.zip  # override for this run
```

### Machine-readable output (`--json`)

`--json` emits **NDJSON** — one compact JSON object per download — to stdout (implying `--quiet` for human output). A single URL emits one line; a batch streams one line per URL as each completes, so a pipeline sees results incrementally and survives a mid-batch kill. Records are emitted for both success and failure (each carries `url`, `success`, and on failure `error`); a batch still exits non-zero if any URL failed. `--json` cannot be combined with `-o -`.

```shell
dr --json -i urls.txt | jq -c 'select(.success | not) | .url'                # URLs that failed
dr --json --checksum sha256:<hex> -o f.iso https://example.com/f.iso | jq .  # inspect the digest
```

Full record schema (all fields, types, presence rules): [docs/REFERENCE.md](docs/REFERENCE.md).

## Flags

Run `dr --help` for the full, always-current flag list and defaults. The most-used flags:

| Flag | Purpose |
|---|---|
| `-o, --output` | destination file or directory, or `-` for stdout |
| `-c, --concurrency` | number of parallel chunks (default 4) |
| `-i, --input-file` | read URLs from a file (one per line; `-` = stdin) |
| `-m, --mirror` | alternate URL for the same file (repeatable; one URL only) |
| `--checksum` | verify with `sha256:<hex>` |
| `--limit-rate` | cap speed, e.g. `500k`, `1M` |
| `--proxy` | route through a proxy URL |
| `--json` | emit NDJSON to stdout |
| `-H, --header` | extra request header (repeatable) |
| `--timeout` | per-request HTTP timeout (default 30s; 0 = none) |
| `--retries` | per-chunk retry attempts (default 3) |
| `-q, --quiet` / `-v, --verbose` | suppress / increase logging |
| `--force` / `--resume` | re-download / control resume (default `--resume=true`) |

Only `http` and `https` download URLs are supported.

## Errors & exit codes

On failure `dr` prints a single line `dr: <error>` to stderr and exits non-zero. The engine returns wrapped sentinel errors, so the message identifies the cause, e.g.:

```
dr: download: remote changed since saved state; cannot resume
```

## Build & test

```shell
make build   # cross-builds dr; ldflags inject version, revision, date (surfaced by `dr --version`)
make test    # go test ./... -race
```

## Roadmap

Not yet implemented:

- Configuration file support

## Contributing

Contributions are welcome. Please open an issue or pull request.

## License

[MIT](LICENSE)
