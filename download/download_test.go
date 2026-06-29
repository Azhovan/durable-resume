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
	"os/exec"
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

	// .part staging file and sidecar removed on success (renamed onto the final path).
	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part should be renamed away on success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar should be removed on success")
}

func TestRunResolvesContentDispositionName(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("derived-", 5000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Content-Disposition should win over the URL basename.
		w.Header().Set("Content-Disposition", `attachment; filename="derived.bin"`)
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
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	dir := t.TempDir()

	// Capture the "saved to" line via a temp file used as Out (Options.Out is *os.File).
	outFile, err := os.CreateTemp(dir, "out-*.log")
	require.NoError(t, err)
	defer outFile.Close()

	opts := baseOpts(t, srv.URL+"/some/path/url-name.bin", dir)
	opts.Client = srv.Client()
	opts.Quiet = false
	opts.Out = outFile

	require.NoError(t, Run(context.Background(), opts))

	want := filepath.Join(dir, "derived.bin")
	got, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	logged, err := os.ReadFile(outFile.Name())
	require.NoError(t, err)
	assert.Contains(t, string(logged), "dr: saved to "+want)
}

func TestRunSavedLineSuppressedUnderQuiet(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("quiet-", 5000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="q.bin"`)
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
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	dir := t.TempDir()
	outFile, err := os.CreateTemp(dir, "out-*.log")
	require.NoError(t, err)
	defer outFile.Close()

	opts := baseOpts(t, srv.URL, dir)
	opts.Client = srv.Client()
	opts.Quiet = true
	opts.Out = outFile

	require.NoError(t, Run(context.Background(), opts))

	// The file must still land at the resolved dir-join path under quiet; the
	// quiet flag only suppresses the "saved to" line, not the resolution itself.
	want := filepath.Join(dir, "q.bin")
	got, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	logged, err := os.ReadFile(outFile.Name())
	require.NoError(t, err)
	assert.NotContains(t, string(logged), "saved to")
}

// TestRunResolvesFinalURLBasenameAfterRedirect drives the spec's headline gap end
// to end: a 302 redirect to a CDN-style path whose basename is the real filename,
// with NO Content-Disposition. The saved file must be named from the FINAL
// (post-redirect) URL, proving Run resolves against info.finalURL rather than the
// originally-requested URL.
func TestRunResolvesFinalURLBasenameAfterRedirect(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("iso-bytes-", 5000))
	const realName = "ubuntu-24.04.iso"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/real/"+realName, http.StatusFound)
			return
		}
		// Target: ranged 206 body, deliberately NO Content-Disposition so the
		// name can only come from this final URL's basename.
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
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	dir := t.TempDir()
	opts := baseOpts(t, srv.URL+"/start", dir)
	opts.Client = srv.Client() // follows the redirect

	require.NoError(t, Run(context.Background(), opts))

	want := filepath.Join(dir, realName)
	got, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

// TestRunResolvesURLBasenameWithoutContentDisposition exercises precedence rung (c)
// through Run: an existing-directory Output, a URL ending in a real filename, and a
// server emitting no Content-Disposition. The saved file must be named from the URL
// path basename.
func TestRunResolvesURLBasenameWithoutContentDisposition(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("url-name-", 5000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	opts := baseOpts(t, srv.URL+"/downloads/file-name.iso", dir)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))

	want := filepath.Join(dir, "file-name.iso")
	got, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
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

	// No sidecar should ever be created on the single-stream path, and the .part
	// must be renamed away on success.
	_, statErr := os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "single-stream must not create a sidecar")
	_, statErr = os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part should be renamed away on success")
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

	// Pre-seed the .part staging file: single chunk covering the whole file, half
	// already done.
	half := int64(len(payload) / 2)
	require.NoError(t, os.WriteFile(partPath(out), payload[:half], 0o644))
	// Pad the .part out to full size so resume opens (no truncate) over real bytes.
	f, err := os.OpenFile(partPath(out), os.O_WRONLY, 0o644)
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
	require.NoError(t, st.Save(statePath(partPath(out))))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 1

	err = Run(context.Background(), opts)
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&rangeRequested), int32(1), "remainder served via Range from a non-zero offset")

	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part renamed away on resume success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar removed on resume success")
}

func TestRunResumeRemoteChanged(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("XY", 8192))
	srv := rangedServer(t, payload, `"new-etag"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(partPath(out), payload[:100], 0o644))

	st := &State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        `"old-etag"`,
		Concurrency: 1,
		Chunks: []ChunkState{
			{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: 100},
		},
	}
	require.NoError(t, st.Save(statePath(partPath(out))))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRemoteChanged), "got %v", err)

	// Sidecar retained; final path never created.
	_, statErr := os.Stat(statePath(partPath(out)))
	assert.NoError(t, statErr, "sidecar retained when remote changed")
	_, statErr = os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "final path must not be created on failure")
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

	_, statErr := os.Stat(statePath(partPath(out)))
	assert.NoError(t, statErr, "sidecar retained on under-delivery")
	_, statErr = os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "final path must not be created on failure")
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

	_, statErr := os.Stat(statePath(partPath(out)))
	assert.NoError(t, statErr, "sidecar retained on checksum mismatch")
	_, statErr = os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "final path must not be created on failure")
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

	_, statErr := os.Stat(statePath(partPath(out)))
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

	// Partial .part retained; nothing at the final path.
	_, statErr := os.Stat(partPath(out))
	assert.NoError(t, statErr, ".part retained on cancel")
	_, statErr = os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "final path must not be created on cancel")
	// Sidecar retained (state flushed on exit).
	data, sideErr := os.ReadFile(statePath(partPath(out)))
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
		got, err := httpClient(Options{Client: c})
		require.NoError(t, err)
		assert.Same(t, c, got)
	})

	t.Run("returns injected client unchanged ignoring proxy", func(t *testing.T) {
		t.Parallel()
		c := &http.Client{Timeout: 99 * time.Second}
		got, err := httpClient(Options{Client: c, Proxy: "http://p:8080"})
		require.NoError(t, err)
		assert.Same(t, c, got)
	})

	t.Run("nil client builds one honoring timeout", func(t *testing.T) {
		t.Parallel()
		got, err := httpClient(Options{Timeout: 7 * time.Second})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, 7*time.Second, got.Timeout)
		tr, ok := got.Transport.(*http.Transport)
		require.True(t, ok, "expected a cloned *http.Transport")
		require.NotNil(t, tr)
	})

	t.Run("nil client zero timeout", func(t *testing.T) {
		t.Parallel()
		got, err := httpClient(Options{})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, time.Duration(0), got.Timeout)
		tr, ok := got.Transport.(*http.Transport)
		require.True(t, ok, "expected a cloned *http.Transport")
		require.NotNil(t, tr)
	})

	t.Run("explicit proxy wires transport.Proxy", func(t *testing.T) {
		t.Parallel()
		got, err := httpClient(Options{Proxy: "http://127.0.0.1:9"})
		require.NoError(t, err)
		tr, ok := got.Transport.(*http.Transport)
		require.True(t, ok)
		require.NotNil(t, tr.Proxy)
		u, err := tr.Proxy(httptest.NewRequest(http.MethodGet, "http://origin.example/x", nil))
		require.NoError(t, err)
		require.NotNil(t, u)
		assert.Equal(t, "127.0.0.1:9", u.Host)
	})

	t.Run("invalid explicit proxy returns ErrInvalidProxy", func(t *testing.T) {
		t.Parallel()
		_, err := httpClient(Options{Proxy: "ftp://nope"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidProxy)
	})
}

// envProxyHelperEnv selects the helper-process mode of TestHTTPClientEnvProxy.
// http.ProxyFromEnvironment reads the environment once per process and caches it
// (sync.Once), so each env-proxy assertion must run in a FRESH process to be
// deterministic. The parent re-execs the test binary with -run on the helper and
// this env var set; the child resolves the proxy and prints the result.
const (
	envProxyHelperEnv  = "DR_TEST_ENV_PROXY_HELPER"
	envProxyHelperReq  = "DR_TEST_ENV_PROXY_REQ"  // request URL to resolve
	envProxyHelperOpts = "DR_TEST_ENV_PROXY_OPTS" // explicit Options.Proxy (may be "")
)

// TestHTTPClientEnvProxyHelper is not a real test: it is the child-process entry
// point used by TestHTTPClientEnvProxy. When DR_TEST_ENV_PROXY_HELPER is unset it
// is a no-op so a normal `go test` run skips it cleanly.
func TestHTTPClientEnvProxyHelper(t *testing.T) {
	if os.Getenv(envProxyHelperEnv) != "1" {
		t.Skip("helper process entry point; only run via TestHTTPClientEnvProxy")
	}
	client, err := httpClient(Options{Proxy: os.Getenv(envProxyHelperOpts)})
	if err != nil {
		fmt.Printf("ERR %v\n", err)
		return
	}
	tr := client.Transport.(*http.Transport)
	u, err := tr.Proxy(httptest.NewRequest(http.MethodGet, os.Getenv(envProxyHelperReq), nil))
	switch {
	case err != nil:
		fmt.Printf("ERR %v\n", err)
	case u == nil:
		fmt.Printf("DIRECT\n")
	default:
		fmt.Printf("PROXY %s\n", u.Host)
	}
}

// runEnvProxyHelper re-execs the test binary into TestHTTPClientEnvProxyHelper
// with the given environment and returns the single result line it printed.
func runEnvProxyHelper(t *testing.T, reqURL, optsProxy string, env map[string]string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHTTPClientEnvProxyHelper$", "-test.v")
	// Start from a proxy-scrubbed copy of the parent env so ambient proxy vars
	// (including lowercase forms) can never leak into the child. Go's
	// httpproxy.getEnvAny skips empty values, so explicitly setting NO_PROXY=""
	// would NOT shield against an inherited lowercase no_proxy — hence we drop
	// every proxy var up front and re-add only what each case provides.
	proxyVars := map[string]bool{
		"HTTP_PROXY": true, "http_proxy": true,
		"HTTPS_PROXY": true, "https_proxy": true,
		"NO_PROXY": true, "no_proxy": true,
		"ALL_PROXY": true, "all_proxy": true,
	}
	scrubbed := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 && proxyVars[kv[:i]] {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	cmd.Env = append(
		scrubbed,
		envProxyHelperEnv+"=1",
		envProxyHelperReq+"="+reqURL,
		envProxyHelperOpts+"="+optsProxy,
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helper output: %s", out)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PROXY ") || line == "DIRECT" || strings.HasPrefix(line, "ERR ") {
			return line
		}
	}
	t.Fatalf("no result line in helper output: %s", out)
	return ""
}

// TestHTTPClientEnvProxy covers the environment-proxy paths. http.ProxyFromEnvironment
// caches the environment once per process, so each case runs in a fresh re-exec of
// the test binary (see runEnvProxyHelper) — fully deterministic, dep-free, and it
// never dials a real proxy.
func TestHTTPClientEnvProxy(t *testing.T) {
	t.Parallel()

	t.Run("env proxy honored when unset", func(t *testing.T) {
		t.Parallel()
		got := runEnvProxyHelper(t, "http://example.com/x", "", map[string]string{
			"HTTP_PROXY": "http://env-proxy:1",
			"NO_PROXY":   "",
		})
		assert.Equal(t, "PROXY env-proxy:1", got)
	})

	t.Run("explicit proxy overrides env", func(t *testing.T) {
		t.Parallel()
		got := runEnvProxyHelper(t, "http://example.com/x", "http://explicit-proxy:2", map[string]string{
			"HTTP_PROXY": "http://env-proxy:1",
		})
		assert.Equal(t, "PROXY explicit-proxy:2", got)
	})

	t.Run("NO_PROXY direct for listed host", func(t *testing.T) {
		t.Parallel()
		got := runEnvProxyHelper(t, "http://skip.example.com/x", "", map[string]string{
			"HTTP_PROXY": "http://env-proxy:1",
			"NO_PROXY":   "skip.example.com",
		})
		assert.Equal(t, "DIRECT", got)
	})

	t.Run("NO_PROXY still proxies other host", func(t *testing.T) {
		t.Parallel()
		got := runEnvProxyHelper(t, "http://other.example.com/x", "", map[string]string{
			"HTTP_PROXY": "http://env-proxy:1",
			"NO_PROXY":   "skip.example.com",
		})
		assert.Equal(t, "PROXY env-proxy:1", got)
	})
}

// TestHTTPClientForwardProxyEndToEnd proves a real download is routed through an
// explicit --proxy: an httptest origin holds the body, and a separate httptest
// server acts as a forward proxy. In proxied plain HTTP the request line targets
// the absolute origin URL, so the proxy forwards by the request's Host. The test
// asserts the proxy handler was hit AND the downloaded bytes equal the origin body.
func TestHTTPClientForwardProxyEndToEnd(t *testing.T) {
	t.Parallel()

	body := []byte("hello through the proxy, this is the origin body")

	origin := rangedServer(t, body, `"e2e"`)
	defer origin.Close()

	var proxyHits int32
	forward := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxyHits, 1)
		// Proxied HTTP carries an absolute-form request URL; forward it to origin.
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer forward.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "downloaded.bin")

	err := Run(context.Background(), Options{
		URL:         origin.URL + "/file",
		Output:      out,
		Concurrency: 2,
		Resume:      true,
		MaxRetries:  1,
		Timeout:     10 * time.Second,
		Quiet:       true,
		Proxy:       forward.URL, // opts.Client intentionally nil so httpClient builds the proxied transport
	})
	require.NoError(t, err)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, body, got)
	assert.Greater(t, atomic.LoadInt32(&proxyHits), int32(0), "expected the forward proxy to be hit")
}

func TestParseProxyURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"http", "http://p:8080", false},
		{"https", "https://p", false},
		{"socks5", "socks5://p:1080", false},
		{"socks5h", "socks5h://p:1080", false},
		{"ftp scheme", "ftp://p", true},
		{"empty scheme", "://", true},
		{"missing host", "http://", true},
		{"port only no host", "http://:8080", true},
		{"garbage", "garbage", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseProxyURL(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidProxy)
				return
			}
			require.NoError(t, err)
		})
	}
}
