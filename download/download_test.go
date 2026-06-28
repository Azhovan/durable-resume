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

// rangedServer serves payload with full Range support (206) and a stable ETag.
func rangedServer(t *testing.T, payload []byte, etag string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end := parseRange(t, rangeHdr, len(payload))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
}

// parseRange parses "bytes=start-end" into absolute inclusive offsets.
func parseRange(t *testing.T, h string, size int) (int, int) {
	t.Helper()
	h = strings.TrimPrefix(h, "bytes=")
	parts := strings.SplitN(h, "-", 2)
	start, err := strconv.Atoi(parts[0])
	require.NoError(t, err)
	end := size - 1
	if len(parts) == 2 && parts[1] != "" {
		end, err = strconv.Atoi(parts[1])
		require.NoError(t, err)
	}
	return start, end
}

func baseOpts(t *testing.T, url, out string) Options {
	t.Helper()
	return Options{
		URL:         url,
		Output:      out,
		Concurrency: 4,
		Resume:      true,
		Timeout:     5 * time.Second,
		MaxRetries:  2,
		Quiet:       true, // suppress progress (also keeps Out nil safe)
	}
}

func TestRunRangedEndToEnd(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("durable-resume-", 5000)) // ~75 KiB, multiple chunks
	srv := rangedServer(t, payload, `"v1"`)
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

	// Sidecar removed on success.
	_, statErr := os.Stat(statePath(out))
	assert.True(t, os.IsNotExist(statErr), "sidecar should be removed on success")
}

func TestRunNoRangeSingleStream(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("no-range-", 4000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200, no Accept-Ranges => not streamable.
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
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

	// No sidecar should ever be created on the single-stream path.
	_, statErr := os.Stat(statePath(out))
	assert.True(t, os.IsNotExist(statErr), "single-stream must not create a sidecar")
}

func TestRunResume(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("ABCDEFGH", 4096)) // 32 KiB
	etag := `"resume-v1"`

	var rangeRequested int32
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
		if start > 0 {
			atomic.AddInt32(&rangeRequested, 1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	// Pre-seed: single chunk covering the whole file, half already done.
	half := int64(len(payload) / 2)
	require.NoError(t, os.WriteFile(out, payload[:half], 0o644))
	// Pad the file out to full size so resume opens (no truncate) over real bytes.
	f, err := os.OpenFile(out, os.O_WRONLY, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(int64(len(payload))))
	require.NoError(t, f.Close())

	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        etag,
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: half},
		},
	}
	require.NoError(t, st.Save(statePath(out)))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 1

	err = Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&rangeRequested), int32(1), "remainder served via Range from a non-zero offset")

	_, statErr := os.Stat(statePath(out))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on resume success")
}

func TestRunResumeRemoteChanged(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("XY", 8192))
	srv := rangedServer(t, payload, `"new-etag"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, payload[:100], 0o644))

	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        `"old-etag"`,
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: 100},
		},
	}
	require.NoError(t, st.Save(statePath(out)))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRemoteChanged), "got %v", err)

	// Sidecar retained.
	_, statErr := os.Stat(statePath(out))
	assert.NoError(t, statErr, "sidecar retained when remote changed")
}

func TestRunSizeMismatch(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("Z", 20000))
	etag := `"v1"`
	// Server advertises a larger size than it actually serves so the on-disk
	// file ends up smaller than expected.
	advertised := len(payload) + 1000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Accept-Ranges", "bytes")
		rh := r.Header.Get("Range")
		if rh == "" {
			w.Header().Set("Content-Length", strconv.Itoa(advertised))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end := parseRange(t, rh, advertised)
		// Clamp to what we actually have.
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
	// The server promises a range that extends past the bytes it actually
	// delivers, so each affected chunk surfaces a retryable short read (per-chunk
	// truncation detection) and, after exhausting retries, the download fails as a
	// chunk failure wrapping io.ErrUnexpectedEOF. Either way the under-delivery is
	// never reported as success and the sidecar is retained.
	assert.True(t, errors.Is(err, ErrChunkFailed), "got %v", err)
	assert.True(t, errors.Is(err, io.ErrUnexpectedEOF), "got %v", err)

	_, statErr := os.Stat(statePath(out))
	assert.NoError(t, statErr, "sidecar retained on under-delivery")
}

func TestRunChecksumMismatch(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("checksum-data-", 2000))
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

	_, statErr := os.Stat(statePath(out))
	assert.NoError(t, statErr, "sidecar retained on checksum mismatch")
}

func TestRunChecksumMatch(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("good-checksum-", 2000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex(payload)}

	err := Run(context.Background(), opts)
	require.NoError(t, err)

	_, statErr := os.Stat(statePath(out))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunContextCancelPreservesPartialAndSidecar(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("slow", 40000)) // 160 KiB
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
		// The initial probe issues "Range: bytes=0-0"; answer it normally so the
		// download actually starts. Only the subsequent real chunk fetches trigger
		// the cancel so that a partial file and sidecar exist to be retained.
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
		// Write a little, flush, then cancel and stall so ctx cancellation wins.
		flusher, _ := w.(http.Flusher)
		body := payload[start : end+1]
		n := 1024
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

	// Partial file retained.
	_, statErr := os.Stat(out)
	assert.NoError(t, statErr, "partial file retained on cancel")
	// Sidecar retained (state flushed on exit).
	data, sideErr := os.ReadFile(statePath(out))
	require.NoError(t, sideErr, "sidecar retained on cancel")
	var saved State
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, int64(len(payload)), saved.Size)
}

func TestRunForwardsHeaders(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("hdr", 5000))
	var sawAuth, sawMulti int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer tok" {
			atomic.StoreInt32(&sawAuth, 1)
		}
		if vals := r.Header.Values("X-Multi"); len(vals) == 2 && vals[0] == "a" && vals[1] == "b" {
			atomic.StoreInt32(&sawMulti, 1)
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		rh := r.Header.Get("Range")
		if rh == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end := parseRange(t, rh, len(payload))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok")
	hdr.Add("X-Multi", "a")
	hdr.Add("X-Multi", "b")

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Header = hdr

	err := Run(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&sawAuth), "Authorization header forwarded")
	assert.Equal(t, int32(1), atomic.LoadInt32(&sawMulti), "multi-value header forwarded")
}

func TestRunNoURL(t *testing.T) {
	t.Parallel()
	err := Run(context.Background(), Options{})
	assert.True(t, errors.Is(err, ErrNoURL), "got %v", err)
}

func TestRunUnsupportedScheme(t *testing.T) {
	t.Parallel()
	err := Run(context.Background(), Options{URL: "ftp://example.com/file"})
	assert.True(t, errors.Is(err, ErrUnsupportedScheme), "got %v", err)
}

func TestChecksumEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		c    Checksum
		want bool
	}{
		{"zero value", Checksum{}, true},
		{"algo only", Checksum{Algo: "sha256"}, true},
		{"hex only", Checksum{Hex: "abc"}, true},
		{"fully set", Checksum{Algo: "sha256", Hex: "abc"}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.c.Empty())
		})
	}
}

func TestHTTPClient(t *testing.T) {
	t.Parallel()

	t.Run("returns injected client unchanged", func(t *testing.T) {
		t.Parallel()
		c := &http.Client{Timeout: 99 * time.Second}
		got := httpClient(Options{Client: c})
		assert.Same(t, c, got)
	})

	t.Run("nil client builds one honoring timeout", func(t *testing.T) {
		t.Parallel()
		got := httpClient(Options{Timeout: 7 * time.Second})
		require.NotNil(t, got)
		assert.Equal(t, 7*time.Second, got.Timeout)
	})

	t.Run("nil client zero timeout", func(t *testing.T) {
		t.Parallel()
		got := httpClient(Options{})
		require.NotNil(t, got)
		assert.Equal(t, time.Duration(0), got.Timeout)
	})
}
