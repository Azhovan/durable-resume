// Package cmd wires the `dr [flags] [url...]` command-line interface onto the
// download package. It accepts one or more URLs (positional args and/or a list
// read from -i/--input-file), parses flags into download.Options, installs
// SIGINT/SIGTERM handling, and invokes download.Run once per URL.
package cmd

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/azhovan/durable-resume/download"
	"github.com/spf13/cobra"
)

// errNoURLs is returned by RunE when no URLs are supplied (no positional args
// and an empty/effective-empty -i list). It wraps download.ErrNoURL so callers
// matching with errors.Is still succeed, while presenting a CLI-level message
// without the internal "download:" package prefix.
var errNoURLs = fmt.Errorf("no URLs provided: %w", download.ErrNoURL)

// errBatchFailed drives a non-zero exit when one or more URLs fail in batch
// mode. It carries no user-facing detail: runBatch's stderr summary already
// itemizes each failure, and main prints this error verbatim, so a content-free
// sentinel avoids a second, opposite-framed tally line.
var errBatchFailed = errors.New("some downloads failed")

// runFunc is the download entry point invoked by RunE. It is a package-level var
// (defaulting to download.Run) solely so tests can intercept the fully-built
// download.Options and assert that flags (e.g. --force) are wired into the struct,
// without performing a real download. Production never reassigns it.
var runFunc = download.Run

// stdoutDash mirrors download's unexported sentinel: the conventional "-" output
// value that streams the body to stdout (with diagnostics routed to stderr).
const stdoutDash = "-"

// NewRootCmd builds the single `dr <url> [flags]` command. version/revision/date
// come from main's ldflag vars and feed cobra's Version field (surfaced by --version).
func NewRootCmd(version, revision, date string) *cobra.Command {
	var (
		output      string
		inputFile   string
		concurrency int
		resume      bool
		checksum    string
		timeout     = download.DefaultTimeout
		retries     int
		headers     []string
		quiet       bool
		verbose     bool
		force       bool
		limitRate   string
	)

	cmd := &cobra.Command{
		Use:   "dr [flags] [url...]",
		Short: "Durable, resumable, segmented file downloader",
		Long: "Download one or more files. Pass URLs as positional arguments and/or " +
			"read them from a file with -i/--input-file (one URL per line; blank lines " +
			"and lines beginning with # are ignored; use - to read the list from stdin).",
		Version:       formatVersion(version, revision, date),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			header, err := parseHeaders(headers)
			if err != nil {
				return err
			}
			sum, err := parseChecksum(checksum)
			if err != nil {
				return err
			}
			// Parse the rate cap before assembling URLs so a bad value fails fast
			// and no download (single, stdout, or batch) is ever started.
			rate, err := parseRate(limitRate)
			if err != nil {
				return err
			}

			urls, err := assembleURLs(cmd, args, inputFile)
			if err != nil {
				return err
			}
			if len(urls) == 0 {
				return errNoURLs
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			base := download.Options{
				Output:      output,
				Concurrency: concurrency,
				Resume:      resume,
				Force:       force,
				Checksum:    sum,
				Timeout:     timeout,
				MaxRetries:  retries,
				Header:      header,
				Quiet:       quiet,
				Verbose:     verbose,
				Out:         os.Stdout,
				LimitRate:   rate,
			}

			stdout := output == stdoutDash

			// SINGLE URL: byte-for-byte unchanged behavior, except for stdout mode.
			if len(urls) == 1 {
				if err := validateURL(urls[0]); err != nil {
					return err
				}
				if stdout {
					// Cannot verify a checksum before the bytes reach the consumer
					// (the pipe cannot be re-read), so reject the combination outright.
					if !sum.Empty() {
						return fmt.Errorf("--checksum cannot be used when writing to stdout (-o -)")
					}
					// Decouple the sinks: the body goes to stdout (base.Data stays nil
					// so download streams to os.Stdout), and ALL diagnostics go to
					// stderr so they never corrupt the piped payload.
					base.Out = os.Stderr
				}
				base.URL = urls[0]
				return runFunc(ctx, base)
			}

			// MULTIPLE URLs: a single pipe cannot disambiguate multiple bodies.
			if stdout {
				return fmt.Errorf("-o - (stdout) cannot be used with multiple URLs")
			}
			// MULTIPLE URLs: multi-only global guards before any download.
			if !sum.Empty() {
				return fmt.Errorf("--checksum cannot be used with multiple URLs")
			}
			if output != "" && !isExistingDir(output) {
				return fmt.Errorf("--output must be a directory when downloading multiple URLs: %q", output)
			}

			results := runBatch(ctx, urls, base)
			if writeSummary(cmd.ErrOrStderr(), results) {
				// The summary already itemized every failure on stderr; return a
				// content-free sentinel solely to drive a non-zero exit, so main
				// does not echo a second, opposite-framed tally line.
				return errBatchFailed
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "", "destination file or directory, or - for stdout (default: Content-Disposition or URL name)")
	flags.StringVarP(&inputFile, "input-file", "i", "",
		`read URLs from a file, one per line (blank/# lines skipped; - = stdin)`)
	flags.IntVarP(&concurrency, "concurrency", "c", download.DefaultConcurrency, "number of parallel chunks")
	flags.BoolVar(&resume, "resume", true, "resume a previous interrupted download")
	flags.StringVar(&checksum, "checksum", "", `verify with "sha256:<hex>"`)
	flags.DurationVar(&timeout, "timeout", download.DefaultTimeout, "per-request HTTP timeout (0 = none)")
	flags.IntVar(&retries, "retries", download.DefaultRetries, "per-chunk retry attempts")
	flags.StringArrayVarP(&headers, "header", "H", nil, `extra request header "Key: Value" (repeatable)`)
	flags.BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	flags.BoolVarP(&verbose, "verbose", "v", false, "extra logging")
	flags.BoolVarP(&force, "force", "f", false, "re-download even if the destination already exists")
	flags.StringVar(&limitRate, "limit-rate", "",
		"limit download speed, e.g. 500k, 1M, 1MiB, 100000 (KiB/MiB/GiB 1024-based; 0/empty = unlimited)")

	return cmd
}

// formatVersion renders the --version string from build info.
func formatVersion(version, revision, date string) string {
	return fmt.Sprintf("%s (revision %s, built %s)", version, revision, date)
}

// parseHeaders converts repeatable "Key: Value" strings into an http.Header.
// Returns a wrapped error on a malformed entry (missing colon / empty key).
func parseHeaders(raw []string) (http.Header, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	header := make(http.Header, len(raw))
	for _, entry := range raw {
		key, value, found := strings.Cut(entry, ":")
		if !found {
			return nil, fmt.Errorf("invalid header %q: missing colon", entry)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("invalid header %q: empty key", entry)
		}
		header.Add(key, strings.TrimSpace(value))
	}
	return header, nil
}

// parseChecksum parses "sha256:<hex>" into a download.Checksum. The empty string
// yields the zero (empty) Checksum and a nil error. Errors on unknown algo or bad hex.
func parseChecksum(s string) (download.Checksum, error) {
	if s == "" {
		return download.Checksum{}, nil
	}
	algo, digest, found := strings.Cut(s, ":")
	if !found {
		return download.Checksum{}, fmt.Errorf("invalid checksum %q: expected \"algo:hex\"", s)
	}
	if algo != "sha256" {
		return download.Checksum{}, fmt.Errorf("unsupported checksum algorithm %q: only sha256 is supported", algo)
	}
	digest = strings.ToLower(digest)
	decoded, err := hex.DecodeString(digest)
	if err != nil {
		return download.Checksum{}, fmt.Errorf("invalid checksum hex %q: %w", digest, err)
	}
	if len(decoded) != 32 {
		return download.Checksum{}, fmt.Errorf("invalid sha256 digest %q: expected 64 hex characters, got %d", digest, len(digest))
	}
	return download.Checksum{Algo: algo, Hex: digest}, nil
}

// validateURL ensures rawURL parses and uses the http or https scheme.
// Returns download.ErrUnsupportedScheme (wrapped) otherwise.
func validateURL(rawURL string) error {
	if rawURL == "" {
		return download.ErrNoURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", rawURL, err)
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("scheme %q: %w", u.Scheme, download.ErrUnsupportedScheme)
	}
}

// readURLsFromFile reads URLs one per line from r. It trims surrounding
// whitespace (TrimSpace also strips the trailing \r of CRLF lines), skips blank
// lines and lines whose first non-space character is '#' (comments), and
// preserves order. It is pure (no filesystem/stdin access) so it is directly
// table-testable. The scanner buffer is raised to 1 MiB so a pathologically long
// URL line yields a wrapped error rather than silently truncating.
func readURLsFromFile(r io.Reader) ([]string, error) {
	var urls []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read input file: %w", err)
	}
	return urls, nil
}

// assembleURLs builds the effective URL list: positional args (in order)
// followed by URLs read from inputFile (in file order). inputFile == "" means no
// file; "-" reads from cmd.InOrStdin(); otherwise the file is opened with
// os.Open. Open/read errors are wrapped and returned (global fail-fast). No
// de-duplication is performed.
func assembleURLs(cmd *cobra.Command, args []string, inputFile string) ([]string, error) {
	urls := make([]string, 0, len(args))
	urls = append(urls, args...)

	if inputFile == "" {
		return urls, nil
	}

	var r io.Reader
	if inputFile == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(inputFile)
		if err != nil {
			return nil, fmt.Errorf("open input file %q: %w", inputFile, err)
		}
		defer f.Close()
		r = f
	}

	fileURLs, err := readURLsFromFile(r)
	if err != nil {
		return nil, err
	}
	return append(urls, fileURLs...), nil
}

// isExistingDir reports whether p names an existing directory.
func isExistingDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// batchResult records one URL's outcome; err == nil means success.
type batchResult struct {
	url string
	err error
}

// runBatch downloads each URL in turn under the shared signal-aware ctx with a
// continue-on-error policy. Before each download it checks ctx.Err(): once the
// context is canceled it records the current and remaining URLs as failures
// without calling runFunc. base already carries the multi-URL Output (a
// directory or ""); only URL is set per iteration.
func runBatch(ctx context.Context, urls []string, base download.Options) []batchResult {
	results := make([]batchResult, 0, len(urls))
	for _, rawURL := range urls {
		if err := ctx.Err(); err != nil {
			results = append(results, batchResult{url: rawURL, err: err})
			continue
		}
		if err := validateURL(rawURL); err != nil {
			results = append(results, batchResult{url: rawURL, err: err})
			continue
		}
		opts := base
		opts.URL = rawURL
		results = append(results, batchResult{url: rawURL, err: runFunc(ctx, opts)})
	}
	return results
}

// writeSummary prints a tally to w followed by one line per failed/canceled URL
// and reports whether any URL failed.
func writeSummary(w io.Writer, results []batchResult) (failed bool) {
	succeeded := 0
	for _, res := range results {
		if res.err == nil {
			succeeded++
		}
	}
	fmt.Fprintf(w, "dr: %d of %d downloads succeeded\n", succeeded, len(results))
	for _, res := range results {
		if res.err != nil {
			fmt.Fprintf(w, "dr: %s: %v\n", res.url, res.err)
		}
	}
	return succeeded < len(results)
}
