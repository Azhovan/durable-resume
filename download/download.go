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
// Mirror failover (Phase 1, sequential): opts.URL is the PRIMARY source and
// opts.Mirrors holds ordered alternates serving the SAME file. opts.sources()
// yields [primary, mirrors...]. Run resolves the output path and checks
// skip-if-complete ONCE from a single primary probe, then runs an OUTER
// sequential-failover loop (runSources) that tries each source's whole-download
// attempt in turn, reusing the SAME .part + sidecar. A mirror reporting the same
// size + validator RESUMES the partial; a mismatching one discards and restarts
// fresh from that mirror. A per-source (non-ctx) failure advances to the next
// source; a ctx cancel/deadline aborts ALL sources immediately (never burns a
// mirror). When every source is exhausted Run returns ErrAllSourcesFailed
// wrapping the per-source errors via errors.Join. With NO mirrors the path is
// byte-for-byte identical to a single-source download.
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
	"strings"
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

	// ErrAllSourcesFailed is returned when every source (the primary plus all
	// mirrors) failed for one file. It wraps the per-source errors via
	// errors.Join, so errors.Is matches BOTH this sentinel and every wrapped
	// per-source sentinel (ErrChunkFailed, ErrSizeMismatch, ...). A single-source
	// download (no mirrors) returns its lone error UNWRAPPED instead.
	ErrAllSourcesFailed = errors.New("download: all sources (primary + mirrors) failed")

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
	URL string // primary source

	// Mirrors holds ordered alternate sources for the SAME file, tried in turn
	// after the primary fails. Empty (the default) means today's single-source
	// behavior. opts.URL stays the PRIMARY: it drives output-name resolution and
	// the sidecar's State.URL so failover never renames the output or invalidates
	// the resume sidecar.
	Mirrors []string

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

	// Result is an OPTIONAL output seam: when non-nil, Run populates it with the
	// structured outcome (resolved path, bytes, size, sha256, resumed, skipped,
	// source) on BOTH success and failure paths, for the cmd layer's --json
	// rendering. nil (the production default and every existing test) => Run never
	// touches it and behaves identically. Filled best-effort even on error so cmd
	// can emit a failure record carrying whatever facts were learned. A pointer
	// (not a return value) keeps Run's signature stable and mirrors Out/Data/clk.
	Result *Result

	// clk is an UNEXPORTED test seam: it overrides the rate limiter's clock so a
	// same-package test can assert the COMPUTED throttle delay deterministically
	// (no real sleeping) and prove the limiter is shared/aggregate across workers.
	// nil (the only value cmd can produce) => realClock, so production is unchanged.
	clk clock
}

// sources returns the ordered source list: the primary (opts.URL) first, then the
// mirrors in order. With no mirrors this is exactly []string{opts.URL}, so the
// single-source path is byte-for-byte unchanged. Empty mirror entries are skipped
// defensively.
func (o Options) sources() []string {
	srcs := make([]string, 0, 1+len(o.Mirrors))
	srcs = append(srcs, o.URL)
	for _, m := range o.Mirrors {
		if m != "" {
			srcs = append(srcs, m)
		}
	}
	return srcs
}

// ctxCanceled reports a real ctx cancellation/deadline on the context itself
// (never a failover trigger).
func ctxCanceled(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// isCtxErr reports whether err is (or wraps) a ctx cancel/deadline; used to
// distinguish "abort all sources" from "advance to the next source".
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// setResult is the single guarded mutation helper for Options.Result: when
// Result is nil (the human path and every existing test) it is a no-op, so the
// non-json behavior is provably side-effect-free. opts is passed by value but
// Result is a pointer, so mutations through it are visible to the caller (cmd)
// that supplied &res.
func setResult(opts Options, mutate func(*Result)) {
	if opts.Result == nil {
		return
	}
	mutate(opts.Result)
}

// Run executes the download to completion or returns the first fatal error.
// It honors ctx cancellation, flushes resume state on interrupt, removes the
// sidecar on success, and retains the sidecar + partial file on failure.
//
// With mirrors configured (opts.Mirrors), Run resolves the output path and checks
// skip-if-complete ONCE from a single primary probe, then runs a sequential
// failover loop over opts.sources(). See the package comment for the failover
// rules. With no mirrors the behavior is identical to a single-source download.
func Run(ctx context.Context, opts Options) error {
	if opts.URL == "" {
		return ErrNoURL
	}
	// Validate the primary AND every mirror scheme up front: Run is public API and
	// must stand alone (cmd validates too, but the engine cannot rely on that).
	if err := validateScheme(opts.URL); err != nil {
		return err
	}
	for _, m := range opts.Mirrors {
		if m == "" {
			continue
		}
		if err := validateScheme(m); err != nil {
			return err
		}
	}

	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}

	// Seed the always-known fields + unknown-size default so even an early-failure
	// record is well-formed (valid url, size present as -1 until probed).
	setResult(opts, func(r *Result) {
		r.URL = opts.URL
		r.Size = -1
	})

	client, err := httpClient(opts)
	if err != nil {
		return err
	}

	// Build ONE shared rate limiter for this download (nil when unlimited -> a
	// true no-op). The SAME instance is threaded into every source attempt and
	// every concurrent chunk worker so the cap is an AGGREGATE whole-download cap,
	// not a per-chunk or per-source cap.
	lim := newRateLimiter(opts.LimitRate, opts.clk) // nil clk => realClock; nil result when unlimited
	if lim != nil {
		vlogf(opts, "rate: limiting to %d B/s (aggregate)", opts.LimitRate)
	}

	// STDOUT MODE: a pipe is not seekable, cannot be stat'd, renamed, or resumed.
	// Sequential failover with NO resume across mirrors; handled fully here before
	// any resolve/skip/.part work. The cmd layer routes opts.Out to stderr so
	// diagnostics never corrupt the piped payload, and rejects --checksum + "-".
	if stdoutMode(opts) {
		return runStdoutSources(ctx, client, opts, lim)
	}

	// 1. PROBE the PRIMARY once, to resolve the output name and check
	// skip-if-complete BEFORE trying any source. This keeps the output naming +
	// State.URL keyed on the primary so failover never renames the output.
	primaryInfo, perr := probe(ctx, client, opts.URL, opts.Header)
	if perr != nil {
		// A real cancel/deadline aborts immediately, even on the primary probe.
		if ctxCanceled(ctx) {
			return ctx.Err()
		}
		// A primary probe transport failure must NOT be fatal when mirrors are
		// present: resolve the name from the raw primary URL (resolveOutputPath
		// tolerates a -1 size remoteInfo) and enter the loop, which re-probes the
		// primary as source[0] and aggregates its error alongside the mirrors.
		if len(opts.sources()) == 1 {
			return fmt.Errorf("download: probe: %w", perr)
		}
		resolved, rerr := resolveOutputPath(opts.Output, remoteInfo{size: -1}, opts.URL)
		if rerr != nil {
			return fmt.Errorf("download: resolve output path: %w", rerr)
		}
		opts.Output = resolved
		setResult(opts, func(r *Result) { r.Output = opts.Output })
		// Skip-if-complete is best-effort here: with the primary unreachable there is
		// no probed size to compare, so only a matching --checksum can prove the
		// existing final file is complete. When it does, skip before probing any
		// mirror; otherwise fall through to failover. (Without --checksum,
		// alreadyComplete on a -1 size always returns false, so this is a no-op.)
		if skip, why := alreadyComplete(opts, remoteInfo{size: -1}); skip {
			vlogf(opts, "skip: %s (primary unreachable)", why)
			setResult(opts, func(r *Result) {
				r.Skipped = true
				if fi, e := os.Stat(opts.Output); e == nil {
					r.Bytes = fi.Size()
				}
			})
			skippedf(opts)
			return nil
		}
		vlogf(opts, "probe: primary failed (%v); resolved %q from URL and entering failover", perr, resolved)
		return runSources(ctx, client, opts, lim, nil) // nil => source[0] self-probes
	}
	vlogf(opts, "probe: size=%d acceptRanges=%t etag=%q lastModified=%q", primaryInfo.size, primaryInfo.acceptRanges, primaryInfo.etag, primaryInfo.lastModified)

	// 2. RESOLVE OUTPUT PATH (single source of truth; uses Content-Disposition and
	// the post-redirect final URL captured by the primary probe). opts is a value
	// copy local to Run; mutating opts.Output makes statePath/dest-open/resume/
	// verify all consistent on the resolved path for EVERY source attempt.
	resolved, err := resolveOutputPath(opts.Output, primaryInfo, opts.URL)
	if err != nil {
		return fmt.Errorf("download: resolve output path: %w", err)
	}
	opts.Output = resolved
	setResult(opts, func(r *Result) {
		r.Output = opts.Output
		r.Size = primaryInfo.size
	})
	vlogf(opts, "output: resolved %q (cd=%q finalURL=%q)", resolved, primaryInfo.contentDisposition, primaryInfo.finalURL)

	// 2b. SKIP-IF-COMPLETE once, before trying ANY source: when the final file
	// already exists and is verifiably complete (and --force is not set),
	// short-circuit with no body fetch. Never touches/creates a .part or sidecar.
	if skip, why := alreadyComplete(opts, primaryInfo); skip {
		vlogf(opts, "skip: %s", why)
		setResult(opts, func(r *Result) {
			r.Skipped = true
			if fi, e := os.Stat(opts.Output); e == nil {
				r.Bytes = fi.Size()
			}
		})
		skippedf(opts)
		return nil
	} else {
		vlogf(opts, "skip: not skipping (%s)", why)
	}

	// 3. SEQUENTIAL FAILOVER over the source list, reusing the primary probe for
	// source[0] to avoid a duplicate request.
	return runSources(ctx, client, opts, lim, &primaryInfo)
}

// runSources is the OUTER sequential-failover loop for the file path. primaryInfo,
// when non-nil, is reused for source[0] (avoids a duplicate probe). A per-source
// (non-ctx) failure advances to the next source; a ctx cancel/deadline aborts all
// sources. On every per-source failure the .part + sidecar are RETAINED (attempt /
// the strategies never Remove on failure), so the next source can resume.
//
// Single-source (no mirrors): the loop runs once and returns the lone error
// UNWRAPPED on failure, identical to today. Multi-source: an exhausted list
// returns ErrAllSourcesFailed wrapping the per-source errors via errors.Join.
func runSources(ctx context.Context, client *http.Client, opts Options, lim rateLimiter, primaryInfo *remoteInfo) error {
	srcs := opts.sources()
	multi := len(srcs) > 1
	// Single-source returns its lone error UNWRAPPED (identical to today), so keep
	// the raw error separate from the prefixed/aggregated multi-source list.
	if !multi {
		err := attempt(ctx, client, opts, srcs[0], primaryInfo, false, lim)
		if err == nil {
			setResult(opts, func(r *Result) {
				if r.Source == "" {
					r.Source = srcs[0]
				}
			})
			savedf(opts)
		}
		return err
	}
	var errs []error
	for i, src := range srcs {
		if ctxCanceled(ctx) {
			return ctx.Err()
		}
		var reuse *remoteInfo
		if i == 0 {
			reuse = primaryInfo // nil if the primary probe failed in Run
		}
		err := attempt(ctx, client, opts, src, reuse, multi, lim)
		if err == nil {
			setResult(opts, func(r *Result) {
				if r.Source == "" {
					r.Source = src
				}
			})
			savedf(opts)
			return nil
		}
		// A cancel/deadline never burns the next mirror: abort all sources.
		if ctxCanceled(ctx) || isCtxErr(err) {
			return err
		}
		errs = append(errs, fmt.Errorf("source %d (%s): %w", i+1, src, err))
		if i < len(srcs)-1 {
			vlogf(opts, "mirror: source %d (%s) failed (%v); trying next", i+1, src, err)
		}
	}
	// errors.Join lets errors.Is match BOTH ErrAllSourcesFailed and every wrapped
	// per-source sentinel (ErrChunkFailed, ErrSizeMismatch, ErrChecksumMismatch, ...).
	return fmt.Errorf("%w: %w", ErrAllSourcesFailed, errors.Join(errs...))
}

// attempt is the post-skip whole-download body for ONE source: probe-or-reuse,
// then dispatch the strategy (segmented or single stream) against `source`. The
// output is already resolved and skip-if-complete already ran, so it does NEITHER.
// It does NOT emit savedf (the caller does, on overall success), and it RETAINS the
// .part + sidecar on failure so the next source can resume. `multi` is threaded
// into the segmented strategy so the resume-gate mismatch branch can
// discard-and-restart instead of returning ErrRemoteChanged when more than one
// source exists.
func attempt(ctx context.Context, client *http.Client, opts Options, source string, reuse *remoteInfo, multi bool, lim rateLimiter) error {
	info := remoteInfo{}
	if reuse != nil {
		info = *reuse
	} else {
		probed, err := probe(ctx, client, source, opts.Header)
		if err != nil {
			return fmt.Errorf("download: probe: %w", err) // transport error advances
		}
		info = probed
		vlogf(opts, "probe: source=%s size=%d acceptRanges=%t etag=%q lastModified=%q", source, info.size, info.acceptRanges, info.etag, info.lastModified)
	}

	if !info.streamable() {
		vlogf(opts, "strategy: single sequential stream (no ranges or unknown size)")
		return runSingleStream(ctx, client, opts, info, lim, source)
	}
	vlogf(opts, "strategy: segmented download with concurrency=%d", opts.Concurrency)
	return runSegmentedDownload(ctx, client, opts, info, opts.Concurrency, lim, source, multi)
}

// runStdoutSources performs sequential failover to the data sink (stdout) with NO
// resume across mirrors. CRITICAL nuance: a pipe cannot be rewound, so we may only
// fail over BEFORE any byte is emitted. runStdoutStream returns the bytes written
// even on error; if a source emitted >0 bytes then failed, returning to a fresh
// mirror would duplicate the leading bytes on the consumer's stream, so we DO NOT
// fail over once bytes have been emitted — we return the error. (In practice probe
// failures emit 0 bytes and fail over cleanly; a mid-stream death after bytes is
// fatal, matching the existing "invoked at most once" stdout invariant.) No
// .part/sidecar/rename is ever touched, so no savedf is emitted.
func runStdoutSources(ctx context.Context, client *http.Client, opts Options, lim rateLimiter) error {
	srcs := opts.sources()
	var errs []error
	for i, src := range srcs {
		if ctxCanceled(ctx) {
			return ctx.Err()
		}
		info, err := probe(ctx, client, src, opts.Header)
		var emitted int64
		if err == nil {
			vlogf(opts, "strategy: single sequential stream to stdout (source=%s; no .part/resume/rename)", src)
			emitted, err = runStdoutStream(ctx, client, opts, info, lim, src)
		}
		if err == nil {
			return nil // nothing saved to a path; no savedf
		}
		if ctxCanceled(ctx) || isCtxErr(err) {
			return err
		}
		// Single-source returns its lone error UNWRAPPED (no "source 1 (url):"
		// prefix), matching the file path's runSources convention so a single-source
		// stdout failure reads identically to pre-mirror behavior.
		if len(srcs) == 1 {
			return err
		}
		errs = append(errs, fmt.Errorf("source %d (%s): %w", i+1, src, err))
		if emitted > 0 {
			// Bytes already on the wire: failover would corrupt the stream. Stop.
			return fmt.Errorf("%w: %w", ErrAllSourcesFailed, errors.Join(errs...))
		}
		if i < len(srcs)-1 {
			vlogf(opts, "mirror: stdout source %d (%s) failed (%v); trying next", i+1, src, err)
		}
	}
	return fmt.Errorf("%w: %w", ErrAllSourcesFailed, errors.Join(errs...))
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
// It RETURNS the number of bytes emitted so runStdoutSources can decide whether
// failover is still safe (only while emitted==0).
func runStdoutStream(ctx context.Context, client *http.Client, opts Options, info remoteInfo, lim rateLimiter, source string) (int64, error) {
	dst := dataSink(opts)

	prog := NewProgress(info.size, opts.Out, opts.Quiet) // opts.Out is stderr here
	prog.Start(ctx)
	defer prog.Stop()

	onBytes := func(n int64) { prog.Add(n) }

	n, err := copyToStream(ctx, client, source, opts.Header, dst, onBytes, lim)
	if err != nil {
		return n, err
	}
	if info.size > 0 && n != info.size {
		return n, fmt.Errorf("download: streamed %d to stdout, want %d: %w", n, info.size, ErrSizeMismatch)
	}
	return n, nil
}

// runSingleStream handles the non-streamable path: a single sequential stream
// into the destination with no resume state and no size verification when the
// size is unknown.
func runSingleStream(ctx context.Context, client *http.Client, opts Options, info remoteInfo, lim rateLimiter, source string) error {
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
		_, runErr := runSingle(ctx, client, source, opts.Header, dst, onBytes, lim)
		return runErr
	})
	if err != nil {
		return err
	}

	// VERIFY (size verify skipped when unknown), then checksum.
	if err := verifySize(dst, info.size); err != nil {
		return err
	}
	// Record actual bytes from the open file (Size may be -1 when unknown); the
	// Stat must run while dst is still open, before the Close below nils it.
	setResult(opts, func(r *Result) {
		if fi, e := dst.Stat(); e == nil {
			r.Bytes = fi.Size()
		}
		r.Size = info.size
	})
	if err := dst.Close(); err != nil {
		return fmt.Errorf("download: close destination: %w", err)
	}
	dst = nil // prevent the deferred close from running on an already-closed file
	if err := verifyChecksum(part, opts.Checksum); err != nil {
		return err
	}
	setResult(opts, func(r *Result) {
		if !opts.Checksum.Empty() {
			r.Sha256 = strings.ToLower(opts.Checksum.Hex)
		}
	})

	// SUCCESS: atomic same-directory rename .part -> final. The only touch of
	// opts.Output; on POSIX it overwrites any pre-existing file in one step.
	if err := os.Rename(part, opts.Output); err != nil {
		return fmt.Errorf("download: finalize output: %w", err)
	}
	// The single-stream path writes no sidecar of its own, but a PRIOR segmented
	// attempt (e.g. a streamable primary that failed before failing over to this
	// non-streamable mirror) may have left one beside the .part. Best-effort remove
	// it so a fully successful download leaves no orphan sidecar. Harmless if absent;
	// State.Remove ignores os.ErrNotExist. (An orphan is never unsafe — partialFileUsable
	// fails once the .part is renamed away — but it is undesirable hygiene.)
	_ = (&State{}).Remove(statePath(part))
	return nil
}

// runSegmentedDownload handles the streamable path: plan chunks (honoring resume
// state), pre-allocate or reopen the destination, run the bounded worker pool,
// then verify and clean up.
func runSegmentedDownload(ctx context.Context, client *http.Client, opts Options, info remoteInfo, concurrency int, lim rateLimiter, source string, multi bool) error {
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
				// MISMATCH. Single-source keeps today's fatal ErrRemoteChanged
				// (the only behavioral branch keyed on source count). Multi-source
				// DISCARDS the sidecar and restarts fresh from THIS mirror, since a
				// different mirror legitimately may not match the partial.
				if multi {
					vlogf(opts, "resume: mirror does not match sidecar (size/validator); discarding and starting fresh from this source")
					saved = nil
					break
				}
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
				setResult(opts, func(r *Result) { r.Resumed = resumed })
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
	dlErr := runSegmented(ctx, client, source, opts.Header, dst, st, sp, chunks, concurrency, onBytes, retry, lim)
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
	setResult(opts, func(r *Result) {
		r.Bytes = st.completedBytes()
		r.Size = info.size
	})
	if err := dst.Close(); err != nil {
		return fmt.Errorf("download: close destination: %w", err)
	}
	dst = nil // prevent the deferred close from running on an already-closed file
	if err := verifyChecksum(part, opts.Checksum); err != nil {
		return err
	}
	setResult(opts, func(r *Result) {
		if !opts.Checksum.Empty() {
			r.Sha256 = strings.ToLower(opts.Checksum.Hex)
		}
	})

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
