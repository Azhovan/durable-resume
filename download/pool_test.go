package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePayload returns a deterministic byte slice of length n.
func makePayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('A' + (i % 26))
	}
	return b
}

// gaugeRangeServer serves payload honoring "Range: bytes=start-end" with 206
// responses while tracking concurrent handler executions. maxSeen records the
// observed peak so tests can assert the worker pool never exceeds concurrency.
func gaugeRangeServer(t *testing.T, payload []byte, inFlight, maxSeen *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(inFlight, 1)
		for {
			old := atomic.LoadInt64(maxSeen)
			if cur <= old || atomic.CompareAndSwapInt64(maxSeen, old, cur) {
				break
			}
		}
		// Hold briefly to widen the concurrency window deterministically.
		time.Sleep(10 * time.Millisecond)
		defer atomic.AddInt64(inFlight, -1)

		rng := r.Header.Get("Range")
		if rng == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end := parseRangeHeader(t, rng, len(payload))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
}

func parseRangeHeader(t *testing.T, header string, size int) (int, int) {
	t.Helper()
	spec := strings.TrimPrefix(header, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	start, err := strconv.Atoi(parts[0])
	require.NoError(t, err)
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.Atoi(parts[1])
		require.NoError(t, err)
	}
	return start, end
}

func tempFile(t *testing.T, size int64) (*os.File, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	require.NoError(t, err)
	if size > 0 {
		require.NoError(t, f.Truncate(size))
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, path
}

// fastRetry is a deterministic retry with a tiny base and fixed jitter.
func fastRetry(maxRetries int) retryFunc {
	return newRetry(maxRetries, time.Millisecond, func() float64 { return 0.0 })
}

func TestRunSegmented_DownloadsFullPayload(t *testing.T) {
	payload := makePayload(8000)
	srv := rangeServer(t, payload)
	defer srv.Close()

	dst, dstPath := tempFile(t, int64(len(payload)))
	chunks := planChunks(int64(len(payload)), 4)
	require.NotEmpty(t, chunks)

	st := newState(srv.URL, remoteInfo{size: int64(len(payload))}, 4, chunks)
	statePath := statePath(dstPath)

	var got int64
	onBytes := func(n int64) { atomic.AddInt64(&got, n) }

	err := runSegmented(context.Background(), srv.Client(), srv.URL, nil, dst, st, statePath, chunks, 4, onBytes, fastRetry(2))
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), atomic.LoadInt64(&got))

	out, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, payload, out)
}

func TestRunSegmented_NeverExceedsConcurrency(t *testing.T) {
	payload := makePayload(20000)
	var inFlight, maxSeen int64
	srv := gaugeRangeServer(t, payload, &inFlight, &maxSeen)
	defer srv.Close()

	dst, dstPath := tempFile(t, int64(len(payload)))
	const concurrency = 4
	chunks := planChunks(int64(len(payload)), concurrency)

	st := newState(srv.URL, remoteInfo{size: int64(len(payload))}, concurrency, chunks)

	err := runSegmented(context.Background(), srv.Client(), srv.URL, nil, dst, st, statePath(dstPath), chunks, concurrency, func(int64) {}, fastRetry(1))
	require.NoError(t, err)
	assert.LessOrEqual(t, atomic.LoadInt64(&maxSeen), int64(concurrency),
		"in-flight workers exceeded concurrency")
	assert.Greater(t, atomic.LoadInt64(&maxSeen), int64(0))
}

func TestRunSegmented_ContextCancelReturnsPromptly(t *testing.T) {
	payload := makePayload(40000)
	// Server blocks on each chunk so cancellation has work to interrupt.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	dst, dstPath := tempFile(t, int64(len(payload)))
	chunks := planChunks(int64(len(payload)), 4)
	st := newState(srv.URL, remoteInfo{size: int64(len(payload))}, 4, chunks)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		done <- runSegmented(ctx, srv.Client(), srv.URL, nil, dst, st, statePath(dstPath), chunks, 4, func(int64) {}, fastRetry(1))
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("runSegmented did not return promptly after cancel (possible goroutine leak/hang)")
	}
}

func TestRunSegmented_ChunkAlways500_AbortsWithErrChunkFailed(t *testing.T) {
	payload := makePayload(8000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dst, dstPath := tempFile(t, int64(len(payload)))
	chunks := planChunks(int64(len(payload)), 4)
	st := newState(srv.URL, remoteInfo{size: int64(len(payload))}, 4, chunks)

	err := runSegmented(context.Background(), srv.Client(), srv.URL, nil, dst, st, statePath(dstPath), chunks, 4, func(int64) {}, fastRetry(2))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChunkFailed)
}

func TestRunSegmented_SavesSidecar(t *testing.T) {
	payload := makePayload(4096)
	srv := rangeServer(t, payload)
	defer srv.Close()

	dst, dstPath := tempFile(t, int64(len(payload)))
	chunks := planChunks(int64(len(payload)), 2)
	st := newState(srv.URL, remoteInfo{size: int64(len(payload))}, 2, chunks)
	sp := statePath(dstPath)

	err := runSegmented(context.Background(), srv.Client(), srv.URL, nil, dst, st, sp, chunks, 2, func(int64) {}, fastRetry(2))
	require.NoError(t, err)

	_, statErr := os.Stat(sp)
	require.NoError(t, statErr, "sidecar should exist after run")

	loaded, err := LoadState(sp)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, int64(len(payload)), loaded.completedBytes())
}

func TestRunSingle_StreamsFullBody(t *testing.T) {
	payload := makePayload(5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No-range server: always 200 with the full body.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dst, dstPath := tempFile(t, 0)

	var got int64
	var mu sync.Mutex
	onBytes := func(n int64) {
		mu.Lock()
		got += n
		mu.Unlock()
	}

	n, err := runSingle(context.Background(), srv.Client(), srv.URL, nil, dst, onBytes)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n)
	assert.Equal(t, int64(len(payload)), got)

	out, err := os.ReadFile(dstPath)
	require.NoError(t, err)
	assert.Equal(t, payload, out)
}
