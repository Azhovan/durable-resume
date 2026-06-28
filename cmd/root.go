// Package cmd wires the `dr <url> [flags]` command-line interface onto the
// download package. It validates the URL, parses flags into download.Options,
// installs SIGINT/SIGTERM handling, and invokes download.Run.
package cmd

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/azhovan/durable-resume/download"
	"github.com/spf13/cobra"
)

// NewRootCmd builds the single `dr <url> [flags]` command. version/revision/date
// come from main's ldflag vars and feed cobra's Version field (surfaced by --version).
func NewRootCmd(version, revision, date string) *cobra.Command {
	var (
		output      string
		concurrency int
		resume      bool
		checksum    string
		timeout     = download.DefaultTimeout
		retries     int
		headers     []string
		quiet       bool
		verbose     bool
	)

	cmd := &cobra.Command{
		Use:           "dr <url>",
		Short:         "Durable, resumable, segmented file downloader",
		Version:       formatVersion(version, revision, date),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawURL := args[0]
			if err := validateURL(rawURL); err != nil {
				return err
			}

			header, err := parseHeaders(headers)
			if err != nil {
				return err
			}

			sum, err := parseChecksum(checksum)
			if err != nil {
				return err
			}

			out := output
			if out == "" {
				out = defaultOutputName(rawURL)
			}

			opts := download.Options{
				URL:         rawURL,
				Output:      out,
				Concurrency: concurrency,
				Resume:      resume,
				Checksum:    sum,
				Timeout:     timeout,
				MaxRetries:  retries,
				Header:      header,
				Quiet:       quiet,
				Verbose:     verbose,
				Out:         os.Stdout,
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return download.Run(ctx, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVarP(&output, "output", "o", "", "destination path (default: derived from URL)")
	flags.IntVarP(&concurrency, "concurrency", "c", download.DefaultConcurrency, "number of parallel chunks")
	flags.BoolVar(&resume, "resume", true, "resume a previous interrupted download")
	flags.StringVar(&checksum, "checksum", "", `verify with "sha256:<hex>"`)
	flags.DurationVar(&timeout, "timeout", download.DefaultTimeout, "per-request HTTP timeout (0 = none)")
	flags.IntVar(&retries, "retries", download.DefaultRetries, "per-chunk retry attempts")
	flags.StringArrayVarP(&headers, "header", "H", nil, `extra request header "Key: Value" (repeatable)`)
	flags.BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	flags.BoolVarP(&verbose, "verbose", "v", false, "extra logging")

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

// defaultOutputName derives the output filename from the URL path; falls back to
// "download" when the path is empty or ends in a slash.
func defaultOutputName(rawURL string) string {
	const fallback = "download"
	u, err := url.Parse(rawURL)
	if err != nil {
		return fallback
	}
	if u.Path == "" || strings.HasSuffix(u.Path, "/") {
		return fallback
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		return fallback
	}
	return base
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
