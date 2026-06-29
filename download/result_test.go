package download

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResultSegmentedSuccess: a ranged (segmented) download populates Bytes,
// Size, Source, Output; Resumed=false and Sha256 empty with no checksum.
func TestResultSegmentedSuccess(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("durable-resume-", 5000)) // ~75 KiB
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	var res Result
	opts.Result = &res
	require.NoError(t, Run(context.Background(), opts))

	assert.Equal(t, srv.URL, res.URL)
	assert.Equal(t, out, res.Output)
	assert.Equal(t, int64(len(payload)), res.Bytes)
	assert.Equal(t, int64(len(payload)), res.Size)
	assert.False(t, res.Resumed)
	assert.False(t, res.Skipped)
	assert.Equal(t, srv.URL, res.Source)
	assert.Empty(t, res.Sha256)
}

// TestResultChecksumSuccess: a verified --checksum echoes the lowercased hex.
func TestResultChecksumSuccess(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("CHK-", 9000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	// Pass the hex uppercased to prove it is normalized to lowercase in the record.
	opts.Checksum = Checksum{Algo: "sha256", Hex: strings.ToUpper(sha256Hex(payload))}

	var res Result
	opts.Result = &res
	require.NoError(t, Run(context.Background(), opts))

	assert.Equal(t, sha256Hex(payload), res.Sha256)
}

// TestResultChecksumFailureEmpty: a wrong checksum fails and Sha256 stays empty.
func TestResultChecksumFailureEmpty(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("CHK-", 9000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Checksum = Checksum{Algo: "sha256", Hex: sha256Hex([]byte("different"))}

	var res Result
	opts.Result = &res
	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrChecksumMismatch)
	assert.Empty(t, res.Sha256, "sha256 must not be set when verification fails")
}

// TestResultResume: a pre-seeded matching .part + sidecar is honored; Resumed
// is recorded true and Bytes equals the full size.
func TestResultResume(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("z", 40000))
	const etag = `"resume-v1"`
	srv := rangedServer(t, payload, etag)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "f.bin")
	part := partPath(out)

	// Pre-allocate the .part at the full size and a matching sidecar whose single
	// chunk has a non-zero done cursor, so the run RESUMES rather than starting fresh.
	require.NoError(t, os.WriteFile(part, make([]byte, len(payload)), 0o644))
	st := State{
		URL:         srv.URL,
		Size:        int64(len(payload)),
		ETag:        etag,
		Concurrency: 1,
		Chunks:      []ChunkState{{Index: 0, Start: 0, End: int64(len(payload)) - 1, Done: 100}},
	}
	data, err := json.MarshalIndent(&st, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath(part), data, 0o644))

	opts := baseOpts(t, srv.URL, out)
	opts.Concurrency = 1
	opts.Client = srv.Client()

	var res Result
	opts.Result = &res
	require.NoError(t, Run(context.Background(), opts))

	assert.True(t, res.Resumed, "Resumed must be true when a matching sidecar is honored")
	assert.Equal(t, int64(len(payload)), res.Bytes)
	assert.Equal(t, int64(len(payload)), res.Size)
}

// TestResultSkipIfComplete: a complete final file short-circuits; Skipped=true,
// Bytes is the on-disk size, run returns nil.
func TestResultSkipIfComplete(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("DONE-", 6000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "f.bin")
	require.NoError(t, os.WriteFile(out, payload, 0o644)) // already complete

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()

	var res Result
	opts.Result = &res
	require.NoError(t, Run(context.Background(), opts))

	assert.True(t, res.Skipped)
	assert.Equal(t, int64(len(payload)), res.Bytes)
	assert.Equal(t, int64(len(payload)), res.Size, "size is probed before the skip short-circuit")
	assert.Empty(t, res.Source)
}

// TestResultMirrorFailover: the primary fails, a mirror serves; Source records
// the mirror URL (which differs from opts.URL).
func TestResultMirrorFailover(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("mirror-data-", 4000))

	primary := failServer(t, 500)
	defer primary.Close()
	mirror := rangedServer(t, payload, `"v1"`)
	defer mirror.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, primary.URL, out)
	opts.Mirrors = []string{mirror.URL}
	opts.Client = mirror.Client()
	opts.MaxRetries = 0

	var res Result
	opts.Result = &res
	require.NoError(t, Run(context.Background(), opts))

	assert.Equal(t, mirror.URL, res.Source)
	assert.NotEqual(t, opts.URL, res.Source, "source must be the serving mirror, not the primary")
	assert.Equal(t, primary.URL, res.URL, "URL stays the requested primary")
}

// TestResultSingleStreamSuccess: a non-streamable (no ranges) server uses the
// single-stream path; Bytes comes from the on-disk file, Source is set.
func TestResultSingleStreamSuccess(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("SS-", 30000))
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

	var res Result
	opts.Result = &res
	require.NoError(t, Run(context.Background(), opts))

	assert.Equal(t, int64(len(payload)), res.Bytes)
	assert.Equal(t, int64(len(payload)), res.Size)
	assert.Equal(t, srv.URL, res.Source)
}

// TestResultNilNoPanic: the success path with a nil Result must not panic and
// must still emit the human "dr: saved to" line when Out is set and not quiet.
func TestResultNilNoPanic(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("durable-resume-", 5000))
	srv := rangedServer(t, payload, `"v1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	outFile, err := os.CreateTemp(dir, "out-*.log")
	require.NoError(t, err)
	defer outFile.Close()

	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Result = nil // explicit: human path
	opts.Quiet = false
	opts.Out = outFile

	require.NoError(t, Run(context.Background(), opts))

	logged, err := os.ReadFile(outFile.Name())
	require.NoError(t, err)
	assert.Contains(t, string(logged), "dr: saved to "+out)

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

// TestResultJSONRoundTrip: a Result with Size=-1 and empty sha256/source
// marshals to a single compact line, dropping the omitempty keys but always
// keeping size and the bools.
func TestResultJSONRoundTrip(t *testing.T) {
	t.Parallel()
	res := Result{
		URL:    "http://example.com/x",
		Output: "/tmp/x",
		Bytes:  0,
		Size:   -1,
	}
	b, err := json.Marshal(res)
	require.NoError(t, err)

	s := string(b)
	assert.NotContains(t, s, "\n", "must be a single compact line")
	assert.NotContains(t, s, "  ", "must not be indented")
	assert.Contains(t, s, `"size":-1`)
	assert.Contains(t, s, `"resumed":false`)
	assert.Contains(t, s, `"skipped":false`)
	assert.Contains(t, s, `"bytes":0`)
	assert.NotContains(t, s, "sha256")
	assert.NotContains(t, s, "source")

	var back Result
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, res, back)
}
