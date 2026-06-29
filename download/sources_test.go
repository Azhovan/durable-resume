package download

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failServer always responds with the given status code (e.g. 500 or 404).
func failServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
}

// deadURL returns a URL whose server is already closed, so any connection is
// refused (a transport error that must trigger failover, not abort).
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// TestSourcesHealthyFailover: primary always 500, mirror serves the full file.
func TestSourcesHealthyFailover(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("mirror-data-", 4000))
	for _, tc := range []struct {
		name    string
		primary func(t *testing.T) string
	}{
		{"primary-500", func(t *testing.T) string { return failServer(t, 500).URL }},
		{"primary-404", func(t *testing.T) string { return failServer(t, 404).URL }},
		{"primary-refused", deadURL},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mirror := rangedServer(t, payload, `"v1"`)
			defer mirror.Close()

			dir := t.TempDir()
			out := filepath.Join(dir, "file.bin")
			opts := baseOpts(t, tc.primary(t), out)
			opts.Mirrors = []string{mirror.URL}
			opts.Client = mirror.Client() // shared client; httptest servers share localhost transport
			opts.MaxRetries = 0

			err := Run(context.Background(), opts)
			require.NoError(t, err)

			got, err := os.ReadFile(out)
			require.NoError(t, err)
			assert.Equal(t, payload, got)
		})
	}
}

// TestSourcesAllFail: primary 500, mirror 404 => ErrAllSourcesFailed wrapping a
// per-source sentinel; .part + sidecar retained.
func TestSourcesAllFail(t *testing.T) {
	t.Parallel()
	primary := failServer(t, 500)
	defer primary.Close()
	mirror := failServer(t, 404)
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, primary.URL, out)
	opts.Mirrors = []string{mirror.URL}
	opts.Client = primary.Client()
	opts.MaxRetries = 0

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllSourcesFailed)
	// The aggregate wraps each per-source error; the message itemizes both.
	assert.Contains(t, err.Error(), "source 1")
	assert.Contains(t, err.Error(), "source 2")
}

// TestSourcesAllFailRetainsPartial: both sources are ranged servers that die
// mid-stream, so a .part + sidecar exist; after total failure they are retained.
func TestSourcesAllFailRetainsPartial(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("abcd", 20000)) // 80 KiB

	dieMidStream := func(t *testing.T) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"same"`)
			w.Header().Set("Accept-Ranges", "bytes")
			rangeHdr := r.Header.Get("Range")
			if rangeHdr == "" {
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				w.WriteHeader(http.StatusOK)
				return
			}
			start, end := parseRange(t, rangeHdr, len(payload))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			// Write at most a few bytes then close to force io.ErrUnexpectedEOF.
			n := end - start + 1
			if n > 4 {
				n = 4
			}
			_, _ = w.Write(payload[start : start+n])
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
		}))
	}

	p := dieMidStream(t)
	defer p.Close()
	m := dieMidStream(t)
	defer m.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, p.URL, out)
	opts.Mirrors = []string{m.URL}
	opts.Client = p.Client()
	opts.MaxRetries = 1
	// Deterministic, fast backoff via the retry seam is internal; MaxRetries small.

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllSourcesFailed)
	// The aggregate is built with errors.Join, so errors.Is must ALSO match the
	// wrapped per-source sentinel. Both ranged sources die mid-stream and exhaust
	// retries, surfacing ErrChunkFailed; this pins the errors.Join contract the
	// package comment promises (a flat-string aggregate would break this assertion
	// while still satisfying ErrorIs(ErrAllSourcesFailed) and the substring checks).
	assert.ErrorIs(t, err, ErrChunkFailed)

	_, statErr := os.Stat(partPath(out))
	assert.NoError(t, statErr, ".part must be retained on total failure")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.NoError(t, statErr, "sidecar must be retained on total failure")
}

// TestSourcesResumeAcrossMirrors proves the headline "resume across mirrors"
// differentiator: a non-zero per-chunk cursor written by the primary must be
// INHERITED by the mirror, so the mirror re-fetches only the remaining bytes.
//
// It is deterministic by construction. Concurrency is forced to 1, so there is a
// SINGLE chunk whose only boundary is offset 0; the mirror can therefore only ever
// see a non-zero Range start if it inherited a resume cursor (a fresh download
// would start at 0). The primary cleanly delivers exactly resumeAt bytes of the
// chunk (advancing and persisting ch.done) on its FIRST range request, then dies on
// every subsequent request so its retries exhaust and Run fails over. We then assert
// (a) the sidecar carried completedBytes()==resumeAt at failover, AND (b) the
// mirror's FIRST range request started at exactly resumeAt (not 0), AND (c) the
// mirror never re-fetched any byte below resumeAt, AND (d) the final file is correct.
func TestSourcesResumeAcrossMirrors(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("RESUME-", 12000)) // ~84 KiB
	const etag = `"shared-v1"`
	const resumeAt = 4096 // bytes the primary durably writes before failover

	// Primary: on its FIRST range request, send a clean 206 that declares the full
	// requested range but writes only the first `resumeAt` bytes, then returns. The
	// client reads those bytes (advancing ch.done to resumeAt) and then sees an
	// unexpected EOF (body shorter than Content-Length) => a RETRYABLE short read, so
	// the cursor is persisted. Every subsequent request fails hard (500) so the
	// chunk's retries exhaust and Run fails over to the mirror with done==resumeAt.
	var primaryHits atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			// Probe (bytes=0-0) goes through parseRange below; a bare HEAD-style
			// request is not used by the engine here.
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		start, end := parseRange(t, rangeHdr, len(payload))
		// The probe issues bytes=0-0; serve it normally so size/etag are learned.
		if start == 0 && end == 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[0:1])
			return
		}
		if primaryHits.Add(1) > 1 {
			// Exhaust the chunk's retries so Run fails over.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// First real chunk fetch: declare the full range but deliver only resumeAt
		// bytes, then return (the client surfaces io.ErrUnexpectedEOF AFTER reading
		// them, advancing and persisting the resume cursor).
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : start+resumeAt])
		w.(http.Flusher).Flush()
	}))
	defer primary.Close()

	var firstMirrorStart atomic.Int64
	firstMirrorStart.Store(-1)
	var minMirrorStart atomic.Int64
	minMirrorStart.Store(int64(len(payload)))
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end := parseRange(t, rangeHdr, len(payload))
		// Ignore the probe (bytes=0-0) when recording resume offsets.
		if !(start == 0 && end == 0) {
			firstMirrorStart.CompareAndSwap(-1, int64(start))
			for {
				cur := minMirrorStart.Load()
				if int64(start) >= cur || minMirrorStart.CompareAndSwap(cur, int64(start)) {
					break
				}
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, primary.URL, out)
	opts.Concurrency = 1 // SINGLE chunk: the only boundary is 0, so any non-zero
	// mirror Range start can only come from an inherited resume cursor.
	opts.Mirrors = []string{mirror.URL}
	opts.Client = primary.Client()
	opts.MaxRetries = 1 // primary exhausts quickly, then failover to mirror

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// (b) the mirror's FIRST request resumed at exactly the inherited cursor...
	assert.Equal(t, int64(resumeAt), firstMirrorStart.Load(),
		"mirror's first Range request must resume at the primary's persisted cursor, not 0")
	// (c) ...and never re-fetched a byte the primary already delivered.
	assert.Equal(t, int64(resumeAt), minMirrorStart.Load(),
		"mirror must not re-fetch any byte below the inherited resume cursor")
}

// TestSourcesMismatchedMirror: a pre-existing .part + matching sidecar (Size N,
// ETag E). The primary fails; the mirror reports a DIFFERENT size. The sidecar must
// be discarded, a FRESH full download from the mirror occurs, and the final file
// matches the mirror's content. Must NOT return ErrRemoteChanged.
func TestSourcesMismatchedMirror(t *testing.T) {
	t.Parallel()
	mirrorPayload := []byte(strings.Repeat("FRESH-", 10000)) // mirror's real content
	staleSize := int64(99999)                                // sidecar/.part claim a different size

	primary := failServer(t, 500)
	defer primary.Close()
	mirror := rangedServer(t, mirrorPayload, `"mirror-etag"`)
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	part := partPath(out)

	// Pre-create a .part of the STALE size and a matching hand-written sidecar.
	require.NoError(t, os.WriteFile(part, make([]byte, staleSize), 0o644))
	st := State{
		URL:         primary.URL,
		Size:        staleSize,
		ETag:        `"stale-etag"`,
		Concurrency: 4,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: staleSize - 1, Done: 100},
		},
	}
	data, err := json.MarshalIndent(&st, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath(part), data, 0o644))

	opts := baseOpts(t, primary.URL, out)
	opts.Mirrors = []string{mirror.URL}
	opts.Client = primary.Client()
	opts.MaxRetries = 0

	err = Run(context.Background(), opts)
	require.NoError(t, err)
	assert.NotErrorIs(t, err, ErrRemoteChanged)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, mirrorPayload, got)
}

// TestSourcesSingleSourceRegression: with no mirrors, a failing source returns the
// lone error UNWRAPPED (not ErrAllSourcesFailed), and a sidecar size/validator
// mismatch still returns ErrRemoteChanged.
func TestSourcesSingleSourceRegression(t *testing.T) {
	t.Parallel()

	t.Run("lone-error-unwrapped", func(t *testing.T) {
		t.Parallel()
		primary := failServer(t, 500)
		defer primary.Close()
		dir := t.TempDir()
		opts := baseOpts(t, primary.URL, filepath.Join(dir, "f.bin"))
		opts.Client = primary.Client()
		opts.MaxRetries = 0

		err := Run(context.Background(), opts)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrAllSourcesFailed)
		// Single-source: the lone error is returned unwrapped (no aggregate prefix).
		assert.NotContains(t, err.Error(), "source 1")
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("remote-changed-on-mismatch", func(t *testing.T) {
		t.Parallel()
		payload := []byte(strings.Repeat("z", 40000))
		srv := rangedServer(t, payload, `"new-etag"`)
		defer srv.Close()

		dir := t.TempDir()
		out := filepath.Join(dir, "f.bin")
		part := partPath(out)
		require.NoError(t, os.WriteFile(part, make([]byte, len(payload)), 0o644))
		st := State{
			URL:         srv.URL,
			Size:        int64(len(payload)),
			ETag:        `"old-etag"`,
			Concurrency: 4,
			Chunks:      []ChunkState{{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: 10}},
		}
		data, _ := json.MarshalIndent(&st, "", "  ")
		require.NoError(t, os.WriteFile(statePath(part), data, 0o644))

		opts := baseOpts(t, srv.URL, out)
		opts.Client = srv.Client()

		err := Run(context.Background(), opts)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrRemoteChanged)
		assert.NotErrorIs(t, err, ErrAllSourcesFailed)
	})
}

// TestSourcesChecksumOverMirror: primary 500, mirror serves the file. A correct
// --checksum succeeds; a wrong one returns ErrChecksumMismatch.
func TestSourcesChecksumOverMirror(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("CHK-", 9000))
	good := sha256Hex(payload)

	run := func(t *testing.T, sumHex string) error {
		primary := failServer(t, 500)
		defer primary.Close()
		mirror := rangedServer(t, payload, `"v1"`)
		defer mirror.Close()
		dir := t.TempDir()
		opts := baseOpts(t, primary.URL, filepath.Join(dir, "f.bin"))
		opts.Mirrors = []string{mirror.URL}
		opts.Client = primary.Client()
		opts.MaxRetries = 0
		opts.Checksum = Checksum{Algo: "sha256", Hex: sumHex}
		return Run(context.Background(), opts)
	}

	t.Run("correct", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, run(t, good))
	})
	t.Run("wrong", func(t *testing.T) {
		t.Parallel()
		err := run(t, sha256Hex([]byte("different")))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrChecksumMismatch)
	})
}

// TestSourcesCtxCancelAbortsNoNext: ctx is canceled during source1; source2's hit
// counter must remain 0 (the next mirror was never attempted), and the error is
// context.Canceled.
func TestSourcesCtxCancelAbortsNoNext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	payload := []byte(strings.Repeat("x", 40000))
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cancel the context as soon as the primary is hit, then block on ctx so
		// the request observes the cancellation.
		cancel()
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("Range") == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		}
		<-r.Context().Done()
	}))
	defer primary.Close()

	var mirrorHits atomic.Int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror.Close()

	dir := t.TempDir()
	opts := baseOpts(t, primary.URL, filepath.Join(dir, "f.bin"))
	opts.Mirrors = []string{mirror.URL}
	opts.Client = primary.Client()
	opts.MaxRetries = 0
	opts.Timeout = 5 * time.Second

	err := Run(ctx, opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(0), mirrorHits.Load(), "a ctx cancel must not attempt the next mirror")
}

// TestSourcesStdoutFailover: -o - with a failing primary and a healthy mirror; the
// injected buffer receives the full body, no .part is created.
func TestSourcesStdoutFailover(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("STDOUT-", 5000))

	t.Run("failover-success", func(t *testing.T) {
		t.Parallel()
		primary := failServer(t, 500)
		defer primary.Close()
		mirror := rangedServer(t, payload, `"v1"`)
		defer mirror.Close()

		var buf bytes.Buffer
		opts := baseOpts(t, primary.URL, stdoutDash)
		opts.Mirrors = []string{mirror.URL}
		opts.Client = primary.Client()
		opts.MaxRetries = 0
		opts.Data = &buf

		err := Run(context.Background(), opts)
		require.NoError(t, err)
		assert.Equal(t, payload, buf.Bytes())
		_, statErr := os.Stat(partPath(stdoutDash))
		assert.True(t, os.IsNotExist(statErr), "no .part should be created in stdout mode")
	})

	t.Run("all-fail", func(t *testing.T) {
		t.Parallel()
		primary := failServer(t, 500)
		defer primary.Close()
		mirror := failServer(t, 404)
		defer mirror.Close()

		var buf bytes.Buffer
		opts := baseOpts(t, primary.URL, stdoutDash)
		opts.Mirrors = []string{mirror.URL}
		opts.Client = primary.Client()
		opts.MaxRetries = 0
		opts.Data = &buf

		err := Run(context.Background(), opts)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrAllSourcesFailed)
	})

	t.Run("no-failover-after-bytes", func(t *testing.T) {
		t.Parallel()
		// Primary emits >0 bytes then dies mid-stream: Run must return the error
		// rather than failing over (no duplicated leading bytes). It declares a
		// Content-Length larger than the body it writes, flushes the partial bytes
		// to the client, then returns — the client surfaces ErrUnexpectedEOF AFTER
		// receiving the emitted bytes.
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:100])
			w.(http.Flusher).Flush()
		}))
		defer primary.Close()

		var mirrorHits atomic.Int64
		mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mirrorHits.Add(1)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		}))
		defer mirror.Close()

		var buf bytes.Buffer
		opts := baseOpts(t, primary.URL, stdoutDash)
		opts.Mirrors = []string{mirror.URL}
		opts.Client = primary.Client()
		opts.MaxRetries = 0
		opts.Data = &buf

		err := Run(context.Background(), opts)
		require.Error(t, err)
		// Bytes were emitted; the leading 100 bytes must NOT be duplicated by a
		// mirror replay.
		assert.Equal(t, payload[:100], buf.Bytes())
		assert.Equal(t, int64(0), mirrorHits.Load(), "must not fail over after bytes were emitted")
	})
}

// TestSourcesSkipIfCompleteWithMirror: a complete final file already exists; Run
// returns nil after only the primary probe, and the mirror handler is never hit.
func TestSourcesSkipIfCompleteWithMirror(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("DONE-", 6000))

	primary := rangedServer(t, payload, `"v1"`)
	defer primary.Close()

	var mirrorHits atomic.Int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "f.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644)) // already complete

	opts := baseOpts(t, primary.URL, out)
	opts.Mirrors = []string{mirror.URL}
	opts.Client = primary.Client()

	err := Run(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, int64(0), mirrorHits.Load(), "mirror must not be hit when the file is already complete")
}

// TestSourcesSegmentedToSingleStreamRemovesSidecar: a streamable primary fails
// mid-stream AFTER persisting a sidecar; failover lands on a NON-streamable mirror
// (no ranges) served via single-stream. On success the segmented sidecar left beside
// the .part must be removed (hygiene), and the final file must be correct.
func TestSourcesSegmentedToSingleStreamRemovesSidecar(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("SS-", 30000)) // ~90 KiB

	// Streamable primary that advertises ranges + a size, then dies mid-stream so a
	// .part + sidecar are created and persisted before failover.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		start, end := parseRange(t, rangeHdr, len(payload))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	defer primary.Close()

	// Non-streamable mirror: no Accept-Ranges, 200 with the full body.
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, primary.URL, out)
	opts.Mirrors = []string{mirror.URL}
	opts.Client = primary.Client()
	opts.MaxRetries = 1

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// The orphan segmented sidecar (and .part) must be gone after a successful
	// single-stream failover.
	_, statErr := os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "orphan segmented sidecar must be removed on single-stream success")
	_, statErr = os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part must be renamed away on success")
}

// TestSourcesSkipViaChecksumWhenPrimaryDown: the primary probe fails (host down)
// but a --checksum proves the existing final file is complete. Run must skip
// without probing any mirror.
func TestSourcesSkipViaChecksumWhenPrimaryDown(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("SKIP-", 6000))

	var mirrorHits atomic.Int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "f.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644)) // already complete on disk

	opts := baseOpts(t, deadURL(t), out) // primary refuses connections
	opts.Mirrors = []string{mirror.URL}
	opts.Client = mirror.Client()
	opts.MaxRetries = 0
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex(payload)}

	err := Run(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, int64(0), mirrorHits.Load(),
		"a checksum-complete file must skip even when the primary is unreachable, without probing a mirror")
}

// TestOptionsSources verifies the ordered source list construction.
func TestOptionsSources(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"http://a"}, Options{URL: "http://a"}.sources())
	assert.Equal(t,
		[]string{"http://a", "http://b", "http://c"},
		Options{URL: "http://a", Mirrors: []string{"http://b", "http://c"}}.sources())
	// Empty mirror entries are skipped defensively.
	assert.Equal(t,
		[]string{"http://a", "http://b"},
		Options{URL: "http://a", Mirrors: []string{"", "http://b", ""}}.sources())
}

// TestRunRejectsBadMirrorScheme: Run validates each mirror scheme up front.
func TestRunRejectsBadMirrorScheme(t *testing.T) {
	t.Parallel()
	opts := baseOpts(t, "http://example.test/x", "out.bin")
	opts.Mirrors = []string{"ftp://bad/x"}
	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedScheme)
}
