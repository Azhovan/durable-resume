// Package download implements a correct, resumable, segmented file downloader.
//
// The end-to-end flow is: probe the remote for size and range support, plan a
// set of disjoint byte chunks, (optionally) load durable resume state, download
// every not-yet-complete chunk concurrently into a single pre-allocated
// "<output>.part" staging file via os.File.WriteAt, verify size and optional
// checksum, then atomically rename the .part onto the final path. The final path
// is never written in place, so it only ever appears as a complete, verified
// file. On success the resume sidecar is removed; on failure or interruption the
// .part file and its sidecar are retained so a later run can resume.
//
// All bytes are staged into a sibling "<output>.part" file (see partPath); the
// segmented resume sidecar travels with it as "<output>.part.dr.json". The final
// path opts.Output is touched exactly once, by an atomic same-directory
// os.Rename(part, output) performed only after the bytes are fully written and
// both size and checksum verification pass. The final path therefore never holds
// a partial, zero-holed, or unverified file: it goes from absent straight to a
// complete, verified file in one step. On failure/interruption the .part (and
// its sidecar) remain so a later run resumes.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	// ErrInvalidProxy is returned for an unparseable proxy URL, an unsupported
	// proxy scheme, or a proxy URL missing a host. Accepted schemes: http,
	// https, socks5, socks5h. Callers/tests branch on it via errors.Is.
	ErrInvalidProxy = errors.New("download: invalid proxy url")
)

// stdoutDash is the conventional "-" output value that selects stdout streaming.
const stdoutDash = "-"

// stdoutMode reports whether the body should be streamed to the data sink (stdout)
// instead of staged into a file. Triggered solely by Output == "-".
func stdoutMode(opts Options) bool { return opts.Output == stdoutDash }

// dataSink returns the io.Writer the downloaded body is written to in stdout mode.
// A nil opts.Data means production: os.Stdout. Tests inject a *bytes.Buffer so the
// real process stdout is never captured.
func dataSink(opts Options) io.Writer {
	if opts.Data != nil {
		return opts.Data
	}
	return os.Stdout
}

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
	Force       bool          // re-download even if a complete file already exists at the final path
	Checksum    Checksum      // optional sha256 verification
	Timeout     time.Duration // http client timeout (0 = none; ctx still applies)
	MaxRetries  int           // per-chunk retry attempts
	Header      http.Header   // extra request headers (-H, repeatable)
	Quiet       bool          // suppress progress
	Verbose     bool          // extra logging to Out
	Out         *os.File      // MESSAGE sink (progress/diag); normally os.Stdout, but cmd sets os.Stderr in stdout mode (injectable). Type stays *os.File for isTTY.
	Data        io.Writer     // DATA sink for stdout mode (Output=="-"); nil means os.Stdout via dataSink. Decoupled from the *os.File message sink; injectable for tests.
	Client      *http.Client  // injectable for tests; nil => default built from Timeout

	// Proxy is an explicit proxy URL for THIS download. "" (the default) means
	// honor the standard environment proxies (HTTP_PROXY/HTTPS_PROXY/NO_PROXY and
	// their lowercase forms) via http.ProxyFromEnvironment. A non-empty value
	// OVERRIDES the environment (NO_PROXY is then NOT consulted, by design) and
	// must use scheme http, https, socks5, or socks5h. cmd validates the string
	// before Run (see cmd.parseProxy); httpClient re-parses it when building the
	// default transport. Ignored when Client is non-nil (the injected client is
	// used verbatim, the test-injection seam).
	Proxy string

	LimitRate int64 // aggregate bytes/sec cap across all workers for THIS download; 0 = unlimited

	// clk is an UNEXPORTED test seam: it overrides the rate limiter's clock so a
	// same-package test can assert the COMPUTED throttle delay deterministically
	// (no real sleeping) and prove the limiter is shared/aggregate across workers.
	// nil (the only value cmd can produce) => realClock, so production is unchanged.
	clk clock
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

	client, err := httpClient(opts)
	if err != nil {
		return err
	}

	// Build ONE shared rate limiter for this download (nil when unlimited -> a
	// true no-op). The SAME instance is threaded into every strategy and every
	// concurrent chunk worker so the cap is an AGGREGATE whole-download cap, not
	// a per-chunk cap.
	lim := newRateLimiter(opts.LimitRate, opts.clk) // nil clk => realClock; nil result when unlimited
	if lim != nil {
		vlogf(opts, "rate: limiting to %d B/s (aggregate)", opts.LimitRate)
	}

	// 1. PROBE
	info, err := probe(ctx, client, opts.URL, opts.Header)
	if err != nil {
		return fmt.Errorf("download: probe: %w", err)
	}
	vlogf(opts, "probe: size=%d acceptRanges=%t etag=%q lastModified=%q", info.size, info.acceptRanges, info.etag, info.lastModified)

	// STDOUT MODE: a pipe is not seekable, cannot be stat'd, renamed, or resumed.
	// Force a single sequential stream to the data sink and return BEFORE any
	// resolveOutputPath / skip-if-complete / .part / rename / sidecar / strategy.
	// The cmd layer routes opts.Out to stderr here so diagnostics never corrupt the
	// piped payload, and rejects --checksum + multi-URL with "-" before we are even
	// reached. Nothing was saved to a path, so no savedf is emitted.
	if stdoutMode(opts) {
		vlogf(opts, "strategy: single sequential stream to stdout (forced; pipe not seekable; no .part/resume/rename)")
		return runStdoutStream(ctx, client, opts, info, lim)
	}

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

	// 2b. SKIP-IF-COMPLETE: when the final file already exists and is verifiably
	// complete (and --force is not set), short-circuit with no body fetch. Uses the
	// resolved final path and the probed size; never touches/creates a .part or
	// sidecar, so resume semantics are untouched.
	if skip, why := alreadyComplete(opts, info); skip {
		vlogf(opts, "skip: %s", why)
		skippedf(opts)
		return nil
	} else {
		vlogf(opts, "skip: not skipping (%s)", why)
	}

	// 3. STRATEGY
	if !info.streamable() {
		vlogf(opts, "strategy: single sequential stream (no ranges or unknown size)")
		if err := runSingleStream(ctx, client, opts, info, lim); err != nil {
			return err
		}
		savedf(opts)
		return nil
	}
	vlogf(opts, "strategy: segmented download with concurrency=%d", concurrency)
	if err := runSegmentedDownload(ctx, client, opts, info, concurrency, lim); err != nil {
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

// skippedf prints "dr: <path> already complete; skipping (use --force to
// re-download)" to opts.Out unless --quiet (independent of --verbose). No-op when
// Out is nil. Distinct from vlogf so it shows without -v. Emitted only on the
// skip-if-complete short-circuit, before any .part/sidecar work, so it never
// interleaves with progress.
func skippedf(opts Options) {
	if opts.Quiet || opts.Out == nil {
		return
	}
	fmt.Fprintf(opts.Out, "dr: %s already complete; skipping (use --force to re-download)\n", opts.Output)
}

// runStdoutStream streams the whole resource sequentially to the data sink
// (os.Stdout in production, an injected io.Writer in tests) via plain Write, with
// progress/diagnostics rendered to opts.Out (stderr in stdout mode). It uses NO
// .part staging, no rename, no resume sidecar, and no WriteAt/Truncate/Seek, so it
// is safe on a non-seekable pipe.
//
// copyToStream is invoked EXACTLY ONCE: a pipe cannot be rewound, so a retry after
// any byte has been emitted would duplicate data on the consumer's stream. The body
// stream therefore does not retry (MaxRetries is ignored for it); the probe above is
// still a normal single request. Size is verified by COUNTING the bytes copied
// against the probed size when known (>0); a shortfall is reported as ErrSizeMismatch
// even though bytes were already emitted, so a pipeline gets a non-zero exit.
func runStdoutStream(ctx context.Context, client *http.Client, opts Options, info remoteInfo, lim rateLimiter) error {
	dst := dataSink(opts)

	prog := NewProgress(info.size, opts.Out, opts.Quiet) // opts.Out is stderr here
	prog.Start(ctx)
	defer prog.Stop()

	onBytes := func(n int64) { prog.Add(n) }

	n, err := copyToStream(ctx, client, opts.URL, opts.Header, dst, onBytes, lim)
	if err != nil {
		return err
	}
	if info.size > 0 && n != info.size {
		return fmt.Errorf("download: streamed %d to stdout, want %d: %w", n, info.size, ErrSizeMismatch)
	}
	return nil
}

// runSingleStream handles the non-streamable path: a single sequential stream
// into the destination with no resume state and no size verification when the
// size is unknown.
func runSingleStream(ctx context.Context, client *http.Client, opts Options, info remoteInfo, lim rateLimiter) error {
	// Stage into <output>.part; rename onto the final path only after verification.
	// runSingle truncates dst to 0 on every attempt, so any pre-existing (stale)
	// .part is overwritten; opts.Output is never touched before the atomic rename.
	part := partPath(opts.Output)
	vlogf(opts, "staging: writing to %q", part)

	dst, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
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
		_, runErr := runSingle(ctx, client, opts.URL, opts.Header, dst, onBytes, lim)
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
	if err := verifyChecksum(part, opts.Checksum); err != nil {
		return err
	}

	// SUCCESS: atomic same-directory rename .part -> final. The only touch of
	// opts.Output; on POSIX it overwrites any pre-existing file in one step.
	if err := os.Rename(part, opts.Output); err != nil {
		return fmt.Errorf("download: finalize output: %w", err)
	}
	return nil
}

// runSegmentedDownload handles the streamable path: plan chunks (honoring resume
// state), pre-allocate or reopen the destination, run the bounded worker pool,
// then verify and clean up.
func runSegmentedDownload(ctx context.Context, client *http.Client, opts Options, info remoteInfo, concurrency int, lim rateLimiter) error {
	// Stage into <output>.part; the sidecar travels with it at statePath(part) ==
	// <output>.part.dr.json. On success: verify, close, rename .part -> final, THEN
	// remove the sidecar (order matters; see the rename-failure handling below).
	part := partPath(opts.Output)
	sp := statePath(part)
	vlogf(opts, "staging: writing to %q (sidecar %q)", part, sp)

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
			case !partialFileUsable(part, saved.Size):
				// The sidecar survived but the on-disk .part file is missing or no
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

	// 4. open the .part destination: FRESH => Truncate(info.size) pre-allocates AND
	// overwrites any stale .part left by a prior failed run (treated as discardable,
	// matching the previous in-place fresh-Truncate behavior; never an error).
	// RESUME => no truncation (the on-disk size was validated above before honoring
	// the per-chunk cursors).
	dst, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
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
	dlErr := runSegmented(ctx, client, opts.URL, opts.Header, dst, st, sp, chunks, concurrency, onBytes, retry, lim)
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
	if err := verifyChecksum(part, opts.Checksum); err != nil {
		return err
	}

	// SUCCESS: atomic rename FIRST, then drop the sidecar. If the rename fails,
	// return wrapped and KEEP the sidecar so the next run can retry/resume. (On
	// POSIX the same-directory rename atomically overwrites any pre-existing final
	// file. On Windows os.Rename fails if the target exists; a future port would
	// need an os.ReplaceFile / remove-then-rename shim — out of scope here.)
	//
	// A crash between the rename and the Remove leaves an orphan sidecar with no
	// .part; the next run's LoadState -> partialFileUsable(part) then fails and it
	// starts fresh, removing the orphan on the next success. Harmless.
	if err := os.Rename(part, opts.Output); err != nil {
		return fmt.Errorf("download: finalize output: %w", err)
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

// partPath returns the staging path for an in-progress download: output + ".part".
// All bytes are written here; the segmented resume sidecar travels with it as
// statePath(partPath(output)) == output + ".part.dr.json". partialFileUsable,
// verifySize, and verifyChecksum all key off this path. On success the .part is
// atomically renamed onto opts.Output via a single same-directory os.Rename, so
// the final path is never observed holding a partial or zero-holed file. A stale
// .part left by a prior failed run is overwritten on a fresh start (Truncate),
// matching the previous in-place fresh-Truncate behavior — never an error.
func partPath(output string) string {
	return output + ".part"
}

// partialFileUsable reports whether the existing .part staging file is consistent
// with a sidecar whose chunk done-cursors will be trusted on resume. The fresh
// run pre-allocates the .part with Truncate(size), so a resumable partial file
// must still exist with exactly that on-disk size. A missing, truncated, or
// otherwise resized file means the cursors can no longer be trusted (resuming
// would skip "done" chunks and leave zero holes), so the caller must restart.
func partialFileUsable(part string, size int64) bool {
	if size <= 0 {
		// Unknown/zero size is never the segmented (pre-allocated) path.
		return false
	}
	fi, err := os.Stat(part)
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
func httpClient(opts Options) (*http.Client, error) {
	// TEST/INJECTION SEAM: an injected client is used verbatim — never wrap it or
	// force a transport (and thus never override its proxy). Existing tests
	// (opts.Client = srv.Client()) depend on this; Options.Proxy is ignored here.
	if opts.Client != nil {
		return opts.Client, nil
	}

	// Clone the package default transport so connection pooling, TLS config,
	// HTTP/2 enablement, and dial/idle timeouts are preserved; we override only
	// the Proxy resolver. Clone() already sets Proxy to http.ProxyFromEnvironment,
	// which is exactly the env-honoring default we want when no explicit proxy is set.
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if opts.Proxy != "" {
		// Explicit --proxy OVERRIDES the environment: ProxyURL returns a fixed
		// proxy for every request, so NO_PROXY is intentionally not consulted.
		// cmd already validated the string; re-parse here and surface
		// ErrInvalidProxy on the (production-unreachable) failure rather than
		// silently falling back to env.
		pu, err := parseProxyURL(opts.Proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(pu)
	}
	// else: leave transport.Proxy == http.ProxyFromEnvironment (env honored,
	// including NO_PROXY and lowercase variants).

	return &http.Client{Timeout: opts.Timeout, Transport: transport}, nil
}

// parseProxyURL parses and validates an explicit proxy URL string. Accepted
// schemes: http, https, socks5, socks5h; a host is required. Empty is a
// programmer error here (httpClient only calls it when Proxy != ""); it returns
// ErrInvalidProxy rather than panicking.
func parseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("download: parse proxy %q: %w", raw, ErrInvalidProxy)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("download: proxy scheme %q: %w", u.Scheme, ErrInvalidProxy)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("download: proxy %q: missing host: %w", raw, ErrInvalidProxy)
	}
	return u, nil
}
