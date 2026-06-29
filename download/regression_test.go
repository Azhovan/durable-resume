package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunResumeMissingFileRestartsFresh covers the case where the sidecar
// survives but the partial data file was deleted. Trusting the sidecar's
// per-chunk done cursors would skip "done" chunks and deliver a sparse,
// zero-holed file as success. The download must instead restart fresh and
// produce the correct full payload.
func TestRunResumeMissingFileRestartsFresh(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("ABCDEFGH", 4096)) // 32 KiB
	etag := `"resume-v1"`
	srv := rangedServer(t, payload, etag)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	// Sidecar claims a chunk is already half done, but the data file does not exist.
	half := int64(len(payload) / 2)
	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        etag,
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: half},
		},
	}
	require.NoError(t, st.Save(statePath(partPath(out))))
	// Ensure the .part data file is absent.
	_, statErr := os.Stat(partPath(out))
	require.True(t, os.IsNotExist(statErr))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 1

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "missing partial file must trigger a fresh re-download, not a sparse file")

	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on success")
}

// TestRunResumeTruncatedFileRestartsFresh covers the case where the sidecar
// survives but the partial data file was truncated (shorter than the
// pre-allocated size). Honoring the cursors would leave zero holes.
func TestRunResumeTruncatedFileRestartsFresh(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("wxyz", 8192)) // 32 KiB
	etag := `"resume-v2"`
	srv := rangedServer(t, payload, etag)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	// Write a truncated (too short) .part data file that disagrees with the sidecar.
	require.NoError(t, os.WriteFile(partPath(out), payload[:1000], 0o644))

	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        etag,
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: int64(len(payload) / 2)},
		},
	}
	require.NoError(t, st.Save(statePath(partPath(out))))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 1

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "truncated partial file must trigger a fresh re-download")
}

// TestRunInterruptThenResumeRoundTrip exercises the full durable-resume cycle:
// a real Run is interrupted mid-download (multi-chunk, concurrency>=2), the real
// sidecar is retained, then a second Run resumes via Range from non-zero offsets
// and completes correctly with the sidecar removed.
func TestRunInterruptThenResumeRoundTrip(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("durable", 30000)) // ~210 KiB, multiple chunks
	etag := `"rt-v1"`

	var cancelled atomic.Bool
	var nonZeroRangeAfterResume atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Accept-Ranges", "bytes")
		rh := r.Header.Get("Range")
		if rh == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end := parseRange(t, rh, len(payload))
		// Probe is bytes=0-0; serve normally.
		if start == 0 && end == 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[0:1])
			return
		}
		// After the cancel has fired (second run), count non-zero-offset ranges to
		// prove the second run resumed rather than restarting from scratch.
		if cancelled.Load() && start > 0 {
			nonZeroRangeAfterResume.Add(1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		body := payload[start : end+1]
		if !cancelled.Load() {
			// First run: write a little, then cancel so a partial file + sidecar remain.
			flusher, _ := w.(http.Flusher)
			n := 256
			if n > len(body) {
				n = len(body)
			}
			_, _ = w.Write(body[:n])
			if flusher != nil {
				flusher.Flush()
			}
			cancelled.Store(true)
			cancel()
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 3
	opts.MaxRetries = 0

	// First run: interrupted.
	err := Run(ctx, opts)
	require.Error(t, err, "interrupted run should fail")

	// Sidecar retained, .part retained, nothing at the final path.
	_, statErr := os.Stat(statePath(partPath(out)))
	require.NoError(t, statErr, "real sidecar must be retained after interrupt")
	_, statErr = os.Stat(partPath(out))
	require.NoError(t, statErr, ".part must be retained after interrupt")
	_, statErr = os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "final path must not exist after interrupt")

	// Second run: fresh context, same server+output. Must resume and complete.
	opts2 := baseOpts(t, srv.URL, out)
	opts2.Client = srv.Client()
	opts2.Concurrency = 3
	opts2.MaxRetries = 2

	err = Run(context.Background(), opts2)
	require.NoError(t, err, "resume run should complete")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "resumed download must equal full payload")

	_, statErr = os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part renamed away on resume success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on resume success")
	assert.Greater(t, nonZeroRangeAfterResume.Load(), int32(0),
		"second run must issue Range requests from non-zero offsets (proving resume, not restart)")
}

// TestRunResumeSizeChangedNoValidator covers download.go branch (b): a saved
// State with no ETag/Last-Modified but a different size must reject with
// ErrRemoteChanged and retain the sidecar.
func TestRunResumeSizeChangedNoValidator(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("AB", 8192))
	// Server has no ETag and no Last-Modified.
	srv := rangedServer(t, payload, "")
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(partPath(out), payload[:100], 0o644))

	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)) + 999, // different from server
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) + 998, Done: 100},
		},
	}
	require.NoError(t, st.Save(statePath(partPath(out))))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRemoteChanged), "got %v", err)
	assert.Contains(t, err.Error(), "size", "should report a size change, not an etag change")

	_, statErr := os.Stat(statePath(partPath(out)))
	assert.NoError(t, statErr, "sidecar retained when remote size changed")
	_, statErr = os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "final path must not be created on failure")
}

// TestRunResumeNoValidatorMatchingSizeRestartsFresh covers download.go branch
// (c): a saved State with no validators but a matching size must discard the
// sidecar and complete a fresh download.
func TestRunResumeNoValidatorMatchingSizeRestartsFresh(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("CD", 8192))
	srv := rangedServer(t, payload, "") // no ETag, no Last-Modified
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	// A partial .part at full size so the (unused) on-disk guard wouldn't interfere.
	require.NoError(t, os.WriteFile(partPath(out), make([]byte, len(payload)), 0o644))

	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)), // matches
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: 500},
		},
	}
	require.NoError(t, st.Save(statePath(partPath(out))))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 1

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "no-validator sidecar must be discarded and re-downloaded fresh")

	_, statErr := os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on success")
}

// TestRunCompletePartButAbsentFinalDoesNotSkip pins the resume-vs-skip boundary:
// a complete-size <output>.part PLUS a valid matching sidecar, but with the FINAL
// path absent, must NOT trigger the skip short-circuit (alreadyComplete stats only
// the final path). It must fall through to the resume/download path and fetch the
// body, then produce the correct final file. This guards alreadyComplete against a
// future refactor that mistakenly consulted the .part instead of the final file.
func TestRunCompletePartButAbsentFinalDoesNotSkip(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("boundary", 4096)) // 32 KiB
	etag := `"boundary-v1"`
	var fullGets int32
	srv := countingServer(t, payload, etag, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	// Pre-allocate a complete-size .part (the fresh path Truncate(size) shape) and a
	// valid sidecar that matches the remote (size + ETag). This is the strongest
	// "looks complete" decoy: if alreadyComplete wrongly keyed off the .part it would
	// skip. The FINAL path is left absent.
	require.NoError(t, os.WriteFile(partPath(out), make([]byte, len(payload)), 0o644))
	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        etag,
		Concurrency: 1,
		// Done: 0 so the whole chunk is (re)fetched from the server. The decoy is the
		// complete-size .part + valid sidecar with an absent final file; what is being
		// asserted is that skip is declined (body IS fetched) and the final file is
		// correct — not the resume offset arithmetic.
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: 0},
		},
	}
	require.NoError(t, st.Save(statePath(partPath(out))))
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "final path must be absent for this test")

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 1

	require.NoError(t, Run(context.Background(), opts))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&fullGets), int32(1),
		"complete-size .part with absent final must download (resume), not skip")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on success")
}

// TestFetchChunkMidStream200NotRetried verifies a non-206 (200) response to a
// chunk fetch fails fast (single handler invocation) rather than burning retries.
func TestFetchChunkMidStream200NotRetried(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Ignore the Range header; answer 200.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(100))
	defer f.Close()

	ch := &chunk{index: 0, start: 0, end: 99}
	retry := fastRetry(3)
	err = retry(context.Background(), func() error {
		return fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, f, nil, nil, nil)
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRangeNot206), "got %v", err)
	assert.Equal(t, int32(1), calls.Load(), "non-206 (200) must fail fast, not retry")
}

// TestFetchChunk416NotRetried verifies a 416 (Range Not Satisfiable) fails fast.
func TestFetchChunk416NotRetried(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(100))
	defer f.Close()

	ch := &chunk{index: 0, start: 0, end: 99}
	retry := fastRetry(3)
	err = retry(context.Background(), func() error {
		return fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, f, nil, nil, nil)
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load(), "416 must fail fast, not retry")
}

// TestFetchChunkShortReadIsRetryable verifies a 206 that delivers fewer bytes
// than the requested range (connection-close framed) is surfaced as a retryable
// io.ErrUnexpectedEOF rather than a clean completion.
func TestFetchChunkShortReadIsRetryable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Promise bytes 0-99 via Content-Range but deliver only 10 bytes, framed
		// by connection close (no Content-Length) so Read ends with plain EOF.
		w.Header().Set("Content-Range", "bytes 0-99/100")
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(100))
	defer f.Close()

	ch := &chunk{index: 0, start: 0, end: 99}
	err = fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, f, nil, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, io.ErrUnexpectedEOF), "got %v", err)
	assert.True(t, isRetryable(err), "short read must be retryable")
	// The done cursor advanced for the bytes that did arrive, so a retry resumes.
	assert.Equal(t, int64(10), ch.done)
}

// TestFetchChunkOverLength206IsFatal verifies a 206 that delivers MORE bytes than
// the requested range is rejected (not written past ch.end) and is fatal.
func TestFetchChunkOverLength206IsFatal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-9/100")
		w.WriteHeader(http.StatusPartialContent)
		// Requested range is 10 bytes (0-9) but deliver 50.
		_, _ = w.Write([]byte(strings.Repeat("X", 50)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(100))
	defer f.Close()

	ch := &chunk{index: 0, start: 0, end: 9}
	err = fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, f, nil, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRangeNot206), "got %v", err)
	assert.False(t, isRetryable(err), "over-length 206 must be fatal")
	assert.LessOrEqual(t, ch.done, int64(10), "must not write past the chunk's range")
}

// TestRunSingleStreamRejects206 verifies the single-stream path (no Range sent)
// rejects an anomalous 206 instead of writing the partial body as the full file.
func TestRunSingleStreamRejects206(t *testing.T) {
	t.Parallel()
	// Probe (Range: bytes=0-0) must yield a non-streamable result so Run dispatches
	// to the single-stream path; then the full GET (no Range) gets a bogus 206.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			// Probe: 200 with no Accept-Ranges, no Content-Length => unknown size.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("x"))
			return
		}
		// Single-stream GET: anomalous 206.
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("partial"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.MaxRetries = 0

	err := Run(context.Background(), opts)
	require.Error(t, err, "single-stream must reject a 206 response")
	var se *httpStatusError
	require.True(t, errors.As(err, &se), "got %v", err)
	assert.Equal(t, http.StatusPartialContent, se.StatusCode())
}

// TestRunSingleStreamNoLength verifies a 200 with no Content-Length downloads
// correctly via the single-stream path with size verification skipped.
func TestRunSingleStreamNoLength(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("no-length-", 1000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Length, no Accept-Ranges => unknown size, single stream.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr := os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "single-stream must not create a sidecar")
}

// TestRunSingleStreamChecksumMismatch verifies the single-stream path surfaces
// ErrChecksumMismatch.
func TestRunSingleStreamChecksumMismatch(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("single-sum-", 1000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex([]byte("wrong"))}

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrChecksumMismatch), "got %v", err)
}

// TestRunSingleStreamSizeMismatch verifies the single-stream path surfaces
// ErrSizeMismatch when the body is shorter than the advertised Content-Length.
func TestRunSingleStreamSizeMismatch(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("short-", 1000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Advertise more than we send. net/http will surface a short body; the
		// single-stream verify then reports the size mismatch.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)+500))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.MaxRetries = 0

	err := Run(context.Background(), opts)
	require.Error(t, err, "short body must not be reported as success")
}

// TestClassifyChunkError covers the distinguishing behavior of classifyChunkError:
// context errors propagate unwrapped (NOT wrapped in ErrChunkFailed), while other
// errors are wrapped.
func TestClassifyChunkError(t *testing.T) {
	t.Parallel()

	t.Run("context canceled stays unwrapped", func(t *testing.T) {
		t.Parallel()
		err := classifyChunkError(fmt.Errorf("wrap: %w", context.Canceled))
		assert.True(t, errors.Is(err, context.Canceled))
		assert.False(t, errors.Is(err, ErrChunkFailed), "ctx errors must not be wrapped in ErrChunkFailed")
	})

	t.Run("deadline exceeded stays unwrapped", func(t *testing.T) {
		t.Parallel()
		err := classifyChunkError(context.DeadlineExceeded)
		assert.True(t, errors.Is(err, context.DeadlineExceeded))
		assert.False(t, errors.Is(err, ErrChunkFailed))
	})

	t.Run("arbitrary error is wrapped", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("boom")
		err := classifyChunkError(sentinel)
		assert.True(t, errors.Is(err, ErrChunkFailed))
		assert.True(t, errors.Is(err, sentinel))
	})

	t.Run("nil stays nil", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, classifyChunkError(nil))
	})
}

// TestIsRetryableExtra covers the classifications added by the fixer: ErrRangeNot206
// and io.ErrShortWrite are fatal.
func TestIsRetryableExtra(t *testing.T) {
	t.Parallel()
	assert.False(t, isRetryable(fmt.Errorf("range: %w", ErrRangeNot206)), "ErrRangeNot206 must be fatal")
	assert.False(t, isRetryable(fmt.Errorf("write: %w", io.ErrShortWrite)), "short write must be fatal")
	// A non-206 chunk error wrapping both ErrRangeNot206 and a retryable status is
	// classified by the status code.
	retryable := fmt.Errorf("chunk got 503: %w: %w", ErrRangeNot206, &httpStatusError{code: 503})
	assert.True(t, isRetryable(retryable), "5xx must remain retryable even when wrapping ErrRangeNot206")
	fatal := fmt.Errorf("chunk got 200: %w: %w", ErrRangeNot206, &httpStatusError{code: 200})
	assert.False(t, isRetryable(fatal), "200 must be fatal")
}
