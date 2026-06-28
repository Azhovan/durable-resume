package download

import (
	"context"
	"encoding/json"
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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAtomicSegmentedSuccess (case a): a segmented download lands the verified
// payload at the final path and leaves NO .part and NO sidecar behind.
func TestAtomicSegmentedSuccess(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("atomic-segmented-", 5000)) // multi-chunk
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part must be renamed away on success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar must be removed on success")
}

// TestAtomicMidDownloadInterruption (case b): an interrupted download retains a
// .part and a readable sidecar, and NEVER leaves a (partial) file at the final
// path.
func TestAtomicMidDownloadInterruption(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("interrupt-me", 20000)) // multi-chunk
	etag := `"v1"`

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
		// Probe (bytes=0-0): answer normally so the download actually starts.
		if start == 0 && end == 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[0:1])
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		// Flush ~256 bytes, then cancel and stall so ctx cancellation wins the race.
		flusher, _ := w.(http.Flusher)
		body := payload[start : end+1]
		n := 256
		if n > len(body) {
			n = len(body)
		}
		_, _ = w.Write(body[:n])
		if flusher != nil {
			flusher.Flush()
		}
		cancel()
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.MaxRetries = 0

	err := Run(ctx, opts)
	require.Error(t, err)

	fi, statErr := os.Stat(partPath(out))
	require.NoError(t, statErr, ".part retained on interruption")
	// The segmented path pre-allocates the .part to the full final size; a
	// regression that truncated it back to 0 on the failure path would break
	// resume, so assert the retained .part is genuinely the pre-allocated size.
	assert.Equal(t, int64(len(payload)), fi.Size(), ".part must keep its pre-allocated size")
	_, statErr = os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "final path must never hold a partial")

	data, sideErr := os.ReadFile(statePath(partPath(out)))
	require.NoError(t, sideErr, "sidecar retained on interruption")
	var saved State
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, int64(len(payload)), saved.Size)
	// The sidecar's resume cursor must stay within the file size; an over-counted
	// or corrupted cursor would make the retained .part unusable for resume. We do
	// NOT assert > 0: the server cancels immediately after flushing, so whether
	// those bytes reach a chunk write before teardown is a legitimate race.
	// Deterministic non-zero-offset resume is proven end-to-end by
	// TestAtomicInterruptThenResumeRoundTrip.
	assert.LessOrEqual(t, saved.completedBytes(), saved.Size,
		"resume cursor must stay within the file size")
}

// TestAtomicSingleStreamSuccess (case e): a single-stream (200, no Accept-Ranges)
// download renames .part -> final, creates NO sidecar, and leaves NO .part.
func TestAtomicSingleStreamSuccess(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("single-atomic-", 3000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200, no Accept-Ranges => non-streamable single-stream path.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr := os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "single-stream must never create a sidecar")
	_, statErr = os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part must be renamed away on success")
}

// TestAtomicStalePartOverwrittenOnFreshRun (case f): a stale .part of the wrong
// length present before a fresh run (no sidecar) is overwritten and the final
// file is correct, with no .part or sidecar left behind.
func TestAtomicStalePartOverwrittenOnFreshRun(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("fresh-over-stale-", 5000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	// Pre-write garbage of the wrong length to the .part, with NO sidecar, so the
	// run starts fresh and must overwrite (Truncate) the stale staging file.
	require.NoError(t, os.WriteFile(partPath(out), []byte("stale-garbage-of-wrong-length"), 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "stale .part must be overwritten, not appended to")

	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part must be renamed away on success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "no sidecar should remain")
}

// TestAtomicMismatchRetainsPartAndSidecar (case d): both a checksum mismatch and
// a size/short-delivery mismatch return the expected sentinel error, retain the
// .part and the sidecar, and never create the final file.
func TestAtomicMismatchRetainsPartAndSidecar(t *testing.T) {
	t.Parallel()

	t.Run("checksum mismatch", func(t *testing.T) {
		t.Parallel()
		payload := []byte(strings.Repeat("checksum-atomic-", 2000))
		srv := rangedServer(t, payload, `"v1"`)
		defer srv.Close()

		dir := t.TempDir()
		out := filepath.Join(dir, "file.bin")
		opts := baseOpts(t, srv.URL, out)
		opts.Client = srv.Client()
		opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex([]byte("not the payload"))}

		err := Run(context.Background(), opts)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrChecksumMismatch), "got %v", err)

		fi, statErr := os.Stat(partPath(out))
		require.NoError(t, statErr, ".part retained on checksum mismatch")
		// All bytes were delivered (only the checksum failed), so the retained
		// .part must hold the complete payload, not a truncated/zeroed file.
		assert.Equal(t, int64(len(payload)), fi.Size(), ".part must hold the full payload")
		_, statErr = os.Stat(statePath(partPath(out)))
		assert.NoError(t, statErr, "sidecar retained on checksum mismatch")
		_, statErr = os.Stat(out)
		assert.True(t, os.IsNotExist(statErr), "final path must not be created on mismatch")
	})

	t.Run("short delivery", func(t *testing.T) {
		t.Parallel()
		payload := []byte(strings.Repeat("Q", 20000))
		advertised := len(payload) + 1000
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Accept-Ranges", "bytes")
			rh := r.Header.Get("Range")
			if rh == "" {
				w.Header().Set("Content-Length", strconv.Itoa(advertised))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(payload)
				return
			}
			start, end := parseRange(t, rh, advertised)
			if start >= len(payload) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, advertised))
				w.WriteHeader(http.StatusPartialContent)
				return
			}
			realEnd := end
			if realEnd >= len(payload) {
				realEnd = len(payload) - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, advertised))
			w.Header().Set("Content-Length", strconv.Itoa(realEnd-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[start : realEnd+1])
		}))
		defer srv.Close()

		dir := t.TempDir()
		out := filepath.Join(dir, "file.bin")
		opts := baseOpts(t, srv.URL, out)
		opts.Client = srv.Client()
		opts.Concurrency = 1

		err := Run(context.Background(), opts)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrChunkFailed), "got %v", err)
		assert.True(t, errors.Is(err, io.ErrUnexpectedEOF), "got %v", err)

		fi, statErr := os.Stat(partPath(out))
		require.NoError(t, statErr, ".part retained on under-delivery")
		// The segmented path pre-allocated the .part to the advertised size; the
		// failure path must leave that pre-allocation intact (not truncate to 0).
		assert.Equal(t, int64(advertised), fi.Size(), ".part must keep its pre-allocated size")
		_, statErr = os.Stat(statePath(partPath(out)))
		assert.NoError(t, statErr, "sidecar retained on under-delivery")
		_, statErr = os.Stat(out)
		assert.True(t, os.IsNotExist(statErr), "final path must not be created on under-delivery")
	})
}

// TestAtomicInterruptThenResumeRoundTrip (case c): interrupt a real run, confirm
// the .part + sidecar live at the .part locations with no final file, then a
// second run resumes via non-zero-offset Range and lands the verified payload,
// removing both the .part and the sidecar.
func TestAtomicInterruptThenResumeRoundTrip(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("roundtrip", 25000)) // multi-chunk
	etag := `"rt-atomic"`

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
		if start == 0 && end == 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[0:1])
			return
		}
		if cancelled.Load() && start > 0 {
			nonZeroRangeAfterResume.Add(1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		body := payload[start : end+1]
		if !cancelled.Load() {
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

	require.Error(t, Run(ctx, opts), "interrupted run should fail")

	_, statErr := os.Stat(partPath(out))
	require.NoError(t, statErr, ".part retained after interrupt")
	_, statErr = os.Stat(statePath(partPath(out)))
	require.NoError(t, statErr, "sidecar retained after interrupt")
	_, statErr = os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "final path must not exist after interrupt")

	opts2 := baseOpts(t, srv.URL, out)
	opts2.Client = srv.Client()
	opts2.Concurrency = 3
	opts2.MaxRetries = 2

	require.NoError(t, Run(context.Background(), opts2), "resume run should complete")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr = os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part renamed away on resume success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on resume success")
	assert.Greater(t, nonZeroRangeAfterResume.Load(), int32(0),
		"second run must resume from non-zero offsets")
}

// TestAtomicOverwritesExistingFinalFile: a complete file already at the final
// path is left untouched until the atomic rename, which overwrites it with the
// freshly downloaded, verified content (POSIX same-directory rename).
func TestAtomicOverwritesExistingFinalFile(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("new-content-", 5000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, []byte("OLD STALE COMPLETE FILE"), 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "atomic rename must overwrite the pre-existing final file")

	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part must be renamed away on success")
}
