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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingServer serves payload with full range support (206) and counts every
// full-body fetch — that is, any GET whose effective range covers the whole file
// (no Range header, or a Range whose end reaches the last byte). The probe's
// "Range: bytes=0-0" one-byte request is explicitly NOT counted, so a skip case
// can assert fullGets==0 while still answering the probe correctly. announceSize
// controls whether Content-Length / Content-Range total is advertised (false =>
// unknown size). When announceSize is false the server also drops Accept-Ranges
// from the probe path so info.size stays -1 and the strategy is single-stream.
func countingServer(t *testing.T, payload []byte, etag string, announceSize bool, fullGets *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		rangeHdr := r.Header.Get("Range")

		// Unknown-size mode: never advertise size or ranges, so info.size stays -1
		// and the strategy is single-stream. The probe still sends "Range: bytes=0-0";
		// answer it with a 200 full body (the server ignores the range, which is what
		// drives info.acceptRanges=false / size=-1) but do NOT count the probe. Only a
		// rangeless GET (the real single-stream body fetch) is counted.
		if !announceSize {
			if rangeHdr == "" {
				atomic.AddInt32(fullGets, 1)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}

		w.Header().Set("Accept-Ranges", "bytes")
		if rangeHdr == "" {
			// Full GET, no range.
			atomic.AddInt32(fullGets, 1)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}

		start, end := parseRange(t, rangeHdr, len(payload))
		// Probe is bytes=0-0: a one-byte range that does not reach the last byte.
		// Anything that reaches the final byte (or spans more than one byte) is a
		// real body fetch and is counted.
		if !(start == 0 && end == 0) {
			atomic.AddInt32(fullGets, 1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
}

// readOut returns the bytes written to an *os.File used as opts.Out.
func readOut(t *testing.T, f *os.File) string {
	t.Helper()
	require.NoError(t, f.Sync())
	b, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	return string(b)
}

func TestRunSkipSizeMatches(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("skip-size-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644)) // exact size

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, int32(0), atomic.LoadInt32(&fullGets), "no full body fetched on skip")

	// File unchanged.
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// No artifacts.
	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part must not be created on skip")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar must not be created on skip")
}

func TestRunSkipChecksumMatches(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("skip-sum-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex(payload)}

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, int32(0), atomic.LoadInt32(&fullGets), "no full body fetched on checksum skip")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestRunChecksumMismatchDownloadsAnyway(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("real-body-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	// Pre-create with wrong bytes but the CORRECT-length is irrelevant; pass the
	// checksum of the real server body. The on-disk file does not match it, so the
	// skip is declined and the real body is downloaded.
	require.NoError(t, os.WriteFile(out, []byte("wrong contents entirely"), 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex(payload)}

	require.NoError(t, Run(context.Background(), opts))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&fullGets), int32(1), "full body served on checksum mismatch")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	require.NoError(t, verifyChecksum(out, opts.Checksum))
}

func TestRunSizeMismatchDownloadsAnyway(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("size-fix-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	// Pre-create with a different size, no checksum => size path declines skip.
	require.NoError(t, os.WriteFile(out, []byte("short"), 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&fullGets), int32(1), "full body served on size mismatch")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, int64(len(payload)), int64(len(got)))
}

func TestRunForceDownloadsAndNoSkipMessage(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("force-dl-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644)) // complete + matching

	outFile, err := os.CreateTemp(dir, "out-*.log")
	require.NoError(t, err)
	defer outFile.Close()

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Force = true
	opts.Quiet = false
	opts.Out = outFile

	require.NoError(t, Run(context.Background(), opts))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&fullGets), int32(1), "force must fetch the body")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	logged := readOut(t, outFile)
	assert.NotContains(t, logged, "already complete", "no skip message when forcing")
}

func TestRunFileAbsentNormalDownload(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("absent-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&fullGets), int32(1), "absent file downloads normally")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestRunUnknownSizeNoChecksumDownloads(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("unknown-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, "", false, &fullGets) // no size, no ranges
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, []byte("pre-existing stale contents"), 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&fullGets), int32(1), "cannot prove complete => download")

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestRunUnknownSizeChecksumMatchesSkips(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("unknown-sum-", 5000))
	var fullGets int32
	srv := countingServer(t, payload, "", false, &fullGets) // unknown size
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex(payload)}

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, int32(0), atomic.LoadInt32(&fullGets), "checksum precedence over unknown size => skip")
}

func TestRunSkipMessage(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("msg-", 5000))
	var fullGets int32

	t.Run("non-quiet writes skip message", func(t *testing.T) {
		t.Parallel()
		srv := countingServer(t, payload, `"v1"`, true, &fullGets)
		defer srv.Close()

		dir := t.TempDir()
		out := filepath.Join(dir, "file.bin")
		require.NoError(t, os.WriteFile(out, payload, 0o644))

		outFile, err := os.CreateTemp(dir, "out-*.log")
		require.NoError(t, err)
		defer outFile.Close()

		opts := baseOpts(t, srv.URL, out)
		opts.Client = srv.Client()
		opts.Quiet = false
		opts.Out = outFile

		require.NoError(t, Run(context.Background(), opts))
		logged := readOut(t, outFile)
		assert.Contains(t, logged, fmt.Sprintf("dr: %s already complete; skipping (use --force to re-download)", out))
	})

	t.Run("quiet writes nothing", func(t *testing.T) {
		t.Parallel()
		srv := countingServer(t, payload, `"v1"`, true, &fullGets)
		defer srv.Close()

		dir := t.TempDir()
		out := filepath.Join(dir, "file.bin")
		require.NoError(t, os.WriteFile(out, payload, 0o644))

		outFile, err := os.CreateTemp(dir, "out-*.log")
		require.NoError(t, err)
		defer outFile.Close()

		opts := baseOpts(t, srv.URL, out)
		opts.Client = srv.Client()
		opts.Quiet = true
		opts.Out = outFile

		require.NoError(t, Run(context.Background(), opts))
		logged := readOut(t, outFile)
		assert.NotContains(t, logged, "already complete")
	})
}

func TestRunSkipLeavesNoArtifacts(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("no-artifacts-", 3000))
	var fullGets int32
	srv := countingServer(t, payload, `"v1"`, true, &fullGets)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	require.NoError(t, Run(context.Background(), opts))

	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), "<output>.part must not exist after skip")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "<output>.part.dr.json must not exist after skip")
}

// TestAlreadyComplete is a pure unit table-test covering every branch of
// alreadyComplete without any HTTP. It asserts (bool, reason-substring) and that
// the helper never returns an error (the signature has none).
func TestAlreadyComplete(t *testing.T) {
	t.Parallel()

	content := []byte("the complete file contents for unit test")
	sum := Checksum{Algo: "sha256", Hex: sha256Hex(content)}

	// writeFile creates a regular file with the given bytes and returns its path.
	writeFile := func(t *testing.T, dir, name string, b []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, b, 0o644))
		return p
	}

	t.Run("force", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := writeFile(t, dir, "f.bin", content)
		ok, why := alreadyComplete(Options{Output: p, Force: true, Checksum: sum}, remoteInfo{size: int64(len(content))})
		assert.False(t, ok)
		assert.Contains(t, why, "force")
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := filepath.Join(dir, "missing.bin")
		ok, why := alreadyComplete(Options{Output: p, Checksum: sum}, remoteInfo{size: int64(len(content))})
		assert.False(t, ok)
		assert.Contains(t, why, "absent")
	})

	t.Run("non-regular (dir)", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ok, why := alreadyComplete(Options{Output: dir}, remoteInfo{size: 10})
		assert.False(t, ok)
		assert.Contains(t, why, "not a regular file")
	})

	t.Run("checksum match", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := writeFile(t, dir, "ok.bin", content)
		// Size deliberately wrong/unknown to prove checksum precedence.
		ok, why := alreadyComplete(Options{Output: p, Checksum: sum}, remoteInfo{size: -1})
		assert.True(t, ok)
		assert.Contains(t, why, "checksum matches")
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := writeFile(t, dir, "bad.bin", []byte("different contents"))
		ok, why := alreadyComplete(Options{Output: p, Checksum: sum}, remoteInfo{size: int64(len(content))})
		assert.False(t, ok)
		assert.Contains(t, why, "checksum not satisfied")
	})

	t.Run("checksum read error", func(t *testing.T) {
		t.Parallel()
		// chmod 0o000 is a no-op for the superuser (DAC bypass), so under root
		// (common in containerized CI) os.Open would still succeed, verifyChecksum
		// would read the matching bytes, and alreadyComplete would report a match —
		// flipping this assertion. Skip the branch as root to keep it deterministic;
		// the non-root path still exercises the verifyChecksum read-error branch, and
		// the "checksum mismatch" subtest covers the same false/"not satisfied" outcome
		// independently of permissions.
		if os.Geteuid() == 0 {
			t.Skip("chmod 0o000 is ineffective as root; cannot force an open error deterministically")
		}
		dir := t.TempDir()
		// A path whose parent is a regular file: os.Stat on the path fails =>
		// "absent". To exercise the verifyChecksum read-error branch we instead make
		// the target a file we can stat but whose open fails by removing read perms.
		p := writeFile(t, dir, "noperm.bin", content)
		require.NoError(t, os.Chmod(p, 0o000))
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		ok, why := alreadyComplete(Options{Output: p, Checksum: sum}, remoteInfo{size: int64(len(content))})
		assert.False(t, ok)
		assert.Contains(t, why, "checksum not satisfied")
	})

	t.Run("size match", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := writeFile(t, dir, "sz.bin", content)
		ok, why := alreadyComplete(Options{Output: p}, remoteInfo{size: int64(len(content))})
		assert.True(t, ok)
		assert.Contains(t, why, "size matches")
	})

	t.Run("size mismatch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := writeFile(t, dir, "sz2.bin", content)
		ok, why := alreadyComplete(Options{Output: p}, remoteInfo{size: int64(len(content)) + 1})
		assert.False(t, ok)
		assert.Contains(t, why, "size")
	})

	t.Run("unknown size no checksum", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		p := writeFile(t, dir, "unk.bin", content)
		ok, why := alreadyComplete(Options{Output: p}, remoteInfo{size: -1})
		assert.False(t, ok)
		assert.Contains(t, why, "size unknown")
	})
}
