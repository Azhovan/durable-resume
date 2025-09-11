# Durable Resume

A fast, reliable file downloader with automatic resume capability, parallel segmented downloading, and intelligent error handling.

## Key Features

- **🚀 Simple CLI**: Just `dr <URL>` - no subcommands required
- **⚡ Fast Downloads**: Parallel segments (1-32) with automatic resume
- **�  Real-Time Progress**: Visual progress bar with speed and ETA
- **🛡️ Smart Error Handling**: Clear, actionable error messages
- **🔧 Flexible**: Supports HTTP/HTTPS/FTP, custom output paths, and scripting modes
- **⚙️ Backward Compatible**: Works with existing scripts

## Installation

```shell
go install github.com/azhovan/durable-resume@latest
```

## Usage

### Basic Examples

```shell
# Download to current directory
dr https://example.com/file.zip

# Download to specific directory
dr https://example.com/file.zip -o ~/Downloads

# Download with custom filename
dr https://example.com/file.zip -o ~/Downloads -n myfile.zip

# Fast download with more segments
dr https://example.com/largefile.iso --segments 8

# Quiet mode for scripting
dr https://example.com/config.json --quiet

# Verbose mode for debugging
dr https://example.com/file.tar.gz --verbose
```

### Command Options

```
Usage: dr <URL> [options]

  -o, --output string      Output path (directory or file)
  -n, --name string        Custom filename
  -c, --segments int       Parallel segments (1-32, default: 4)
  -s, --segment-size int   Segment size in bytes (0 = auto)
      --no-segments        Single-threaded download
  -q, --quiet              Suppress progress output
  -v, --verbose            Detailed logging
  -r, --resume             Resume interrupted downloads (default: true)
```

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

## Legacy Support

The old `download` subcommand still works with deprecation warnings:

```shell
# Legacy (deprecated)
dr download --url https://example.com/file.zip --output ~/Downloads

# New syntax
dr https://example.com/file.zip -o ~/Downloads
```

## Contributing

Contributions are welcome! Please refer to our contributing guidelines.

## What's New in v2.0

- **Simplified CLI**: Direct `dr <URL>` syntax (no subcommands)
- **Real-Time Progress**: Visual progress bar with speed and ETA
- **Smart Error Messages**: Clear, actionable error reporting
- **Backward Compatible**: Legacy syntax still supported

## Roadmap

- Dynamic segment adjustment based on network conditions
- Configuration file support
- Download queue management
- Bandwidth limiting
- Proxy/VPN support

