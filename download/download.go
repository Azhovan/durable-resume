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
	Output      string        // raw user -o/--output value: "" (derive after probe), an existing directory (place derived name inside), or a normal path (verbatim). Run resolves it to the final path after probe.
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
	vlogf(opts, "probe: size=%d acceptRanges=%t etag=%q lastModified=%q", info.size, info.acceptRanges, info.etag, info.lastModified)

	// 2. RESOLVE OUTPUT PATH (single source of truth; uses Content-Disposition and
	// the post-redirect final URL captured by the probe). opts is a value copy
	// local to Run; mutating opts.Output makes statePath/dest-open/resume/verify
	// all consistent on the resolved path.
	resolved, err := resolveOutputPath(opts.Output, info, opts.URL)
	if err != nil {
		return fmt.Errorf("download: resolve output path: %w", err)
	}
	opts.Output = resolved
	vlogf(opts, "output: resolved %q (cd=%q finalURL=%q)", resolved, info.contentDisposition, info.finalURL)

	// 3. STRATEGY
	if !info.streamable() {
		vlogf(opts, "strategy: single sequential stream (no ranges or unknown size)")
		if err := runSingleStream(ctx, client, opts, info); err != nil {
			return err
		}
		savedf(opts)
		return nil
	}
	vlogf(opts, "strategy: segmented download with concurrency=%d", concurrency)
	if err := runSegmentedDownload(ctx, client, opts, info, concurrency); err != nil {
		return err
	}
	savedf(opts)
	return nil
}

// savedf prints "dr: saved to <path>" to opts.Out unless --quiet (independent of
// --verbose). No-op when Out is nil. Distinct from vlogf so it shows without -v.
// Called on the success path after the strategy's deferred prog.Stop() has
// flushed, so the line never interleaves with the live progress render.
func savedf(opts Options) {
	if opts.Quiet || opts.Out == nil {
		return
	}
	fmt.Fprintf(opts.Out, "dr: saved to %s\n", opts.Output)
}

// runSingleStream handles the non-streamable path: a single sequential stream
// into the destination with no resume state and no size verification when the
// size is unknown.
func runSingleStream(ctx context.Context, client *http.Client, opts Options, info remoteInfo) error {
	dst, err := os.OpenFile(opts.Output, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("download: open destination: %w", err)
	}
	// Guard against the explicit Close below racing this deferred cleanup close.
	defer func() {
		if dst != nil {
			_ = dst.Close()
		}
	}()

	prog := NewProgress(info.size, opts.Out, opts.Quiet)
	prog.Start(ctx)
	defer prog.Stop()

	onBytes := func(n int64) { prog.Add(n) }

	// The single-stream path has no resume sidecar to fall back on, so wrap it in
	// the same retry policy as segmented chunks. runSingle truncates dst to 0 and
	// restarts from offset 0 each attempt, so a retry is idempotent for output
	// correctness; reset the progress counter so partial bytes are not counted twice.
	retry := newRetry(opts.MaxRetries, 1*time.Millisecond, nil)
	err = retry(ctx, func() error {
		prog.Seed(0)
		_, runErr := runSingle(ctx, client, opts.URL, opts.Header, dst, onBytes)
		return runErr
	})
	if err != nil {
		return err
	}

	// VERIFY (size verify skipped when unknown), then checksum.
	if err := verifySize(dst, info.size); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("download: close destination: %w", err)
	}
	dst = nil // prevent the deferred close from running on an already-closed file
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
			switch {
			case !saved.Matches(info):
				// Distinguish "remote changed (size)" from "remote changed
				// (validator)" from "no usable validator". Check size first so a
				// pure size change is not misreported as an ETag mismatch.
				if saved.Size != info.size {
					return fmt.Errorf("download: size %d vs %d: %w", saved.Size, info.size, ErrRemoteChanged)
				}
				if (saved.ETag != "" && info.etag != "") || (saved.LastModified != "" && info.lastModified != "") {
					return fmt.Errorf("download: etag %q vs %q: %w", saved.ETag, info.etag, ErrRemoteChanged)
				}
				// No usable validator: discard and start fresh.
				vlogf(opts, "resume: sidecar has no usable validator; discarding and starting fresh")
				saved = nil
			case !partialFileUsable(opts.Output, saved.Size):
				// The sidecar survived but the on-disk data file is missing or no
				// longer matches the pre-allocated size. Trusting the per-chunk
				// done cursors now would skip already-"done" chunks and deliver a
				// sparse, zero-holed file as a success. Discard and start fresh.
				vlogf(opts, "resume: on-disk file missing or wrong size; discarding sidecar and starting fresh")
				saved = nil
			default:
				st = saved
				chunks = saved.toChunks()
				resumed = true
				vlogf(opts, "resume: honoring sidecar; %d of %d bytes already complete", saved.completedBytes(), info.size)
			}
		}
	}

	if st == nil {
		// FRESH plan.
		chunks = planChunks(info.size, concurrency)
		st = newState(opts.URL, info, concurrency, chunks)
	}

	// 4. open destination: truncate to pre-allocate on fresh; no truncation on
	// resume (the on-disk size was validated above before honoring the cursors).
	dst, err := os.OpenFile(opts.Output, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("download: open destination: %w", err)
	}
	if !resumed {
		if err := dst.Truncate(info.size); err != nil {
			dst.Close()
			return fmt.Errorf("download: pre-allocate destination: %w", err)
		}
	}
	// Guard against the explicit Close below racing this deferred cleanup close.
	defer func() {
		if dst != nil {
			_ = dst.Close()
		}
	}()

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
	dst = nil // prevent the deferred close from running on an already-closed file
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

// partialFileUsable reports whether the existing destination file is consistent
// with a sidecar whose chunk done-cursors will be trusted on resume. The fresh
// run pre-allocates the file with Truncate(size), so a resumable partial file
// must still exist with exactly that on-disk size. A missing, truncated, or
// otherwise resized file means the cursors can no longer be trusted (resuming
// would skip "done" chunks and leave zero holes), so the caller must restart.
func partialFileUsable(output string, size int64) bool {
	if size <= 0 {
		// Unknown/zero size is never the segmented (pre-allocated) path.
		return false
	}
	fi, err := os.Stat(output)
	if err != nil {
		return false
	}
	return fi.Size() == size
}

// vlogf writes a diagnostic line to opts.Out when opts.Verbose is set. It is a
// no-op under --quiet, when Verbose is false, or when Out is nil.
func vlogf(opts Options, format string, args ...any) {
	if !opts.Verbose || opts.Quiet || opts.Out == nil {
		return
	}
	fmt.Fprintf(opts.Out, "dr: "+format+"\n", args...)
}

// httpClient returns opts.Client or a default *http.Client built from opts.Timeout.
func httpClient(opts Options) *http.Client {
	if opts.Client != nil {
		return opts.Client
	}
	return &http.Client{Timeout: opts.Timeout}
}
