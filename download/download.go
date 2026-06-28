// Package download implements a correct, resumable, segmented file downloader.
//
// The end-to-end flow is: probe the remote for size and range support, plan a
// set of disjoint byte chunks, (optionally) load durable resume state, download
// every not-yet-complete chunk concurrently writing into a single pre-allocated
// destination file via os.File.WriteAt, then verify size and optional checksum.
// On success the resume sidecar is removed; on failure or interruption the
// sidecar and partial file are retained so a later run can resume.
package download

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Default tuning constants.
const (
	DefaultConcurrency = 4
	DefaultRetries     = 3
	DefaultTimeout     = 30 * time.Second
	copyBufferSize     = 32 * 1024 // fixed per-read buffer; never sized to a chunk
	stateFlushInterval = 1 * time.Second
)

// Sentinel errors. Callers wrap these with %w.
var (
	ErrNoURL             = errors.New("download: no url provided")
	ErrUnsupportedScheme = errors.New("download: only http and https are supported")
	ErrNoLength          = errors.New("download: server did not report content length")
	ErrRemoteChanged     = errors.New("download: remote changed since saved state; cannot resume")
	ErrSizeMismatch      = errors.New("download: downloaded size does not match expected size")
	ErrChecksumMismatch  = errors.New("download: checksum mismatch")
	ErrRangeNot206       = errors.New("download: server did not honor range request")
	ErrChunkFailed       = errors.New("download: chunk failed after retries")
)

// Checksum is an optional post-download integrity check. Algo is currently only
// "sha256". The zero value (empty Algo) means "no checksum".
type Checksum struct {
	Algo string // "sha256" or ""
	Hex  string // lowercase hex digest
}

// Empty reports whether no checksum was requested.
func (c Checksum) Empty() bool {
	return c.Algo == "" || c.Hex == ""
}

// Options is the fully-resolved configuration for one download. cmd/ builds it.
type Options struct {
	URL         string
	Output      string        // destination path; cmd derives the default
	Concurrency int           // parallel chunks; clamped to >=1 inside Run
	Resume      bool          // false when --no-resume
	Checksum    Checksum      // optional sha256 verification
	Timeout     time.Duration // http client timeout (0 = none; ctx still applies)
	MaxRetries  int           // per-chunk retry attempts
	Header      http.Header   // extra request headers (-H, repeatable)
	Quiet       bool          // suppress progress
	Verbose     bool          // extra logging to Out
	Out         *os.File      // progress/diag sink, normally os.Stdout (injectable)
	Client      *http.Client  // injectable for tests; nil => default built from Timeout
}

// Run executes the download to completion or returns the first fatal error.
// It honors ctx cancellation, flushes resume state on interrupt, removes the
// sidecar on success, and retains the sidecar + partial file on failure.
func Run(ctx context.Context, opts Options) error {
	if opts.URL == "" {
		return ErrNoURL
	}
	if err := validateScheme(opts.URL); err != nil {
		return err
	}

	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	client := httpClient(opts)

	// 1. PROBE
	info, err := probe(ctx, client, opts.URL, opts.Header)
	if err != nil {
		return fmt.Errorf("download: probe: %w", err)
	}

	// 2. STRATEGY
	if !info.streamable() {
		return runSingleStream(ctx, client, opts, info)
	}
	return runSegmentedDownload(ctx, client, opts, info, concurrency)
}

// runSingleStream handles the non-streamable path: a single sequential stream
// into the destination with no resume state and no size verification when the
// size is unknown.
func runSingleStream(ctx context.Context, client *http.Client, opts Options, info remoteInfo) error {
	dst, err := os.OpenFile(opts.Output, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("download: open destination: %w", err)
	}
	defer dst.Close()

	prog := NewProgress(info.size, opts.Out, opts.Quiet)
	prog.Start(ctx)
	defer prog.Stop()

	onBytes := func(n int64) { prog.Add(n) }

	if _, err := runSingle(ctx, client, opts.URL, opts.Header, dst, onBytes); err != nil {
		return err
	}

	// VERIFY (size verify skipped when unknown), then checksum.
	if err := verifySize(dst, info.size); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("download: close destination: %w", err)
	}
	if err := verifyChecksum(opts.Output, opts.Checksum); err != nil {
		return err
	}
	return nil
}

// runSegmentedDownload handles the streamable path: plan chunks (honoring resume
// state), pre-allocate or reopen the destination, run the bounded worker pool,
// then verify and clean up.
func runSegmentedDownload(ctx context.Context, client *http.Client, opts Options, info remoteInfo, concurrency int) error {
	sp := statePath(opts.Output)

	var st *State
	var chunks []chunk
	resumed := false

	// 3. RESUME STATE
	if opts.Resume {
		saved, err := LoadState(sp)
		if err != nil {
			return fmt.Errorf("download: load state: %w", err)
		}
		if saved != nil {
			if !saved.Matches(info) {
				// Distinguish "remote changed" from "no validator / size differs".
				if (saved.ETag != "" && info.etag != "") || (saved.LastModified != "" && info.lastModified != "") {
					return fmt.Errorf("download: etag %q vs %q: %w", saved.ETag, info.etag, ErrRemoteChanged)
				}
				if saved.Size != info.size {
					return fmt.Errorf("download: size %d vs %d: %w", saved.Size, info.size, ErrRemoteChanged)
				}
				// No usable validator: discard and start fresh.
				saved = nil
			} else {
				st = saved
				chunks = saved.toChunks()
				resumed = true
			}
		}
	}

	if st == nil {
		// FRESH plan.
		chunks = planChunks(info.size, concurrency)
		st = newState(opts.URL, info, concurrency, chunks)
	}

	// 4. open destination: truncate to pre-allocate on fresh; no truncation on resume.
	var dst *os.File
	var err error
	if resumed {
		dst, err = os.OpenFile(opts.Output, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("download: open destination: %w", err)
		}
	} else {
		dst, err = os.OpenFile(opts.Output, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("download: open destination: %w", err)
		}
		if err := dst.Truncate(info.size); err != nil {
			dst.Close()
			return fmt.Errorf("download: pre-allocate destination: %w", err)
		}
	}
	defer dst.Close()

	// 5. PROGRESS
	prog := NewProgress(info.size, opts.Out, opts.Quiet)
	if resumed {
		prog.Seed(st.completedBytes())
	}
	prog.Start(ctx)
	defer prog.Stop()

	onBytes := func(n int64) { prog.Add(n) }

	// 6. DOWNLOAD via bounded worker pool.
	retry := newRetry(opts.MaxRetries, 1*time.Millisecond, nil)
	dlErr := runSegmented(ctx, client, opts.URL, opts.Header, dst, st, sp, chunks, concurrency, onBytes, retry)
	if dlErr != nil {
		// 7. INTERRUPT / failure: retain sidecar + partial file. Pool already
		// flushed state on exit; nothing to remove.
		return dlErr
	}

	// 8. VERIFY + CLEANUP (success only).
	// The destination is pre-allocated with Truncate(size), so its on-disk size
	// always equals info.size regardless of how many bytes actually arrived. The
	// meaningful integrity check for a segmented download is therefore the total
	// bytes written (tracked via State); a short server response leaves a hole of
	// zero bytes that must be reported as a size mismatch.
	if info.size > 0 {
		if got := st.completedBytes(); got != info.size {
			return fmt.Errorf("download: wrote %d, want %d: %w", got, info.size, ErrSizeMismatch)
		}
	}
	if err := verifySize(dst, info.size); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("download: close destination: %w", err)
	}
	if err := verifyChecksum(opts.Output, opts.Checksum); err != nil {
		return err
	}

	if err := st.Remove(sp); err != nil {
		return err
	}
	return nil
}

// validateScheme ensures the URL is well-formed and uses http or https.
func validateScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("download: parse url: %w", ErrUnsupportedScheme)
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("download: scheme %q: %w", u.Scheme, ErrUnsupportedScheme)
	}
}

// httpClient returns opts.Client or a default *http.Client built from opts.Timeout.
func httpClient(opts Options) *http.Client {
	if opts.Client != nil {
		return opts.Client
	}
	return &http.Client{Timeout: opts.Timeout}
}
