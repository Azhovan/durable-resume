# Durable Resume

A fast, reliable file downloader with automatic resume capability, parallel segmented downloading, and intelligent error handling.

## Key Features

- **Simple CLI**: Just `dr <URL>` - no subcommands required
- **Fast Downloads**: Parallel chunks with true durable resume
- **Real-Time Progress**: Live progress with speed and ETA (TTY only)
- **Smart Error Handling**: Clear, actionable error messages
- **Flexible**: HTTP/HTTPS, custom output paths, custom headers, and quiet mode for scripting
- **Integrity**: Final size check plus optional sha256 verification

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

# Faster download with more parallel chunks
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

  -o, --output string        Destination path (default: derived from URL)
  -c, --concurrency int      Number of parallel chunks (default: 4)
      --resume               Resume a previous interrupted download (default: true; use --resume=false to disable)
      --checksum string      Verify with "sha256:<hex>"
      --timeout duration     Per-request HTTP timeout (0 = none) (default: 30s)
      --retries int          Per-chunk retry attempts (default: 3)
  -H, --header stringArray   Extra request header "Key: Value" (repeatable)
  -q, --quiet                Suppress progress output
  -v, --verbose              Extra logging
```

Only `http` and `https` URLs are supported.

## Progress Display

Real-time progress with speed, data transfer, and ETA:

```
[===================>    ] 78.5% 45.2 MB/s 1.2GB/2.1GB ETA: 0m23s
```



## Error Handling

Clear, actionable error messages with solutions:

```
❌ Error: Invalid URL format - missing protocol

URL: example.com/file.zip

Did you mean:
  https://example.com/file.zip
  http://example.com/file.zip
```

## Contributing

Contributions are welcome! Please refer to our contributing guidelines.

## What's New in v2.0

- **Simplified CLI**: Direct `dr <URL>` syntax (no subcommands)
- **True Durable Resume**: Per-chunk cursors persisted to a sidecar; resumes via HTTP Range
- **Real-Time Progress**: Live progress with speed and ETA
- **Smart Error Messages**: Clear, actionable error reporting

## Roadmap

- Dynamic segment adjustment based on network conditions
- Configuration file support
- Download queue management
- Bandwidth limiting
- Proxy/VPN support

