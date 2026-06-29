package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/azhovan/durable-resume/v3/download"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureHumanSink returns a temp *os.File and a func that reads back everything
// Run wrote to its human sink (opts.Out). RunE points opts.Out at os.Stderr under
// --json, which is the REAL process stderr — NOT cobra's SetErr buffer — so a cmd
// test can only observe Run's human emissions (savedf/skippedf/progress/vlogf) by
// redirecting opts.Out itself. Stubs should set opts.Out = f before calling Run.
func captureHumanSink(t *testing.T) (f *os.File, read func() string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "human-*.log")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f, func() string {
		b, err := os.ReadFile(f.Name())
		require.NoError(t, err)
		return string(b)
	}
}

// jsonTestRecord mirrors cmd.jsonRecord's wire shape for parsing emitted NDJSON.
type jsonTestRecord struct {
	URL     string `json:"url"`
	Output  string `json:"output"`
	Bytes   int64  `json:"bytes"`
	Size    int64  `json:"size"`
	Sha256  string `json:"sha256"`
	Resumed bool   `json:"resumed"`
	Skipped bool   `json:"skipped"`
	Source  string `json:"source"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// rangedTestServer serves payload with full Range support (206) and a stable ETag.
func rangedTestServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		h := strings.TrimPrefix(rangeHdr, "bytes=")
		parts := strings.SplitN(h, "-", 2)
		start, _ := strconv.Atoi(parts[0])
		end := len(payload) - 1
		if len(parts) == 2 && parts[1] != "" {
			end, _ = strconv.Atoi(parts[1])
		}
		w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(end)+"/"+strconv.Itoa(len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
}

// parseNDJSON splits out into trimmed non-empty lines, parsing each into a record.
func parseNDJSON(t *testing.T, out string) []jsonTestRecord {
	t.Helper()
	var recs []jsonTestRecord
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec jsonTestRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "each line must be valid JSON: %q", line)
		recs = append(recs, rec)
	}
	return recs
}

func TestJSON_SingleSuccess(t *testing.T) {
	payload := []byte(strings.Repeat("durable-resume-", 5000))
	srv := rangedTestServer(t, payload)
	defer srv.Close()
	// Capture Run's actual human sink (opts.Out): under --json RunE points it at the
	// real os.Stderr, so this redirect is the ONLY way to observe whether human
	// output (savedf/progress) leaked. The JSON itself goes to cmd.OutOrStdout().
	human, readHuman := captureHumanSink(t)
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		opts.Client = srv.Client()
		opts.Out = human
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--json", "-o", out, srv.URL})
	require.NoError(t, cmd.Execute())

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1, "single URL must emit exactly one line")
	rec := recs[0]
	assert.Equal(t, srv.URL, rec.URL)
	assert.Equal(t, out, rec.Output)
	assert.Equal(t, int64(len(payload)), rec.Bytes)
	assert.Equal(t, int64(len(payload)), rec.Size)
	assert.True(t, rec.Success)
	assert.False(t, rec.Skipped)
	assert.False(t, rec.Resumed)
	assert.Empty(t, rec.Error)

	// Run's human sink must carry NO saved/progress lines under --json (suppression),
	// and stdout must carry ONLY the JSON record (already parsed above).
	assert.NotContains(t, readHuman(), "dr: saved to", "no human saved line under --json")
	assert.NotContains(t, stdout.String(), "dr: saved to", "stdout is pure NDJSON")
}

func TestJSON_SingleChecksum(t *testing.T) {
	payload := []byte(strings.Repeat("CHK-", 9000))
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])
	srv := rangedTestServer(t, payload)
	defer srv.Close()
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		opts.Client = srv.Client()
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--json", "--checksum", "sha256:" + hexSum, "-o", out, srv.URL})
	require.NoError(t, cmd.Execute())

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1)
	assert.Equal(t, hexSum, recs[0].Sha256)
	assert.True(t, recs[0].Success)
}

func TestJSON_SingleFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		opts.Client = srv.Client()
		opts.MaxRetries = 0
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")

	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--json", "-o", out, srv.URL})
	err := cmd.Execute()
	require.Error(t, err, "a failed single download must produce a non-zero exit")

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1)
	assert.False(t, recs[0].Success)
	assert.NotEmpty(t, recs[0].Error)
	assert.Equal(t, srv.URL, recs[0].URL)
	// The real error never appears on stdout beyond the JSON record's error field;
	// the sentinel errJSONFailed carries no detail.
	assert.ErrorIs(t, err, errJSONFailed)
}

func TestJSON_BatchMix(t *testing.T) {
	payload := []byte(strings.Repeat("ok-", 5000))
	good := rangedTestServer(t, payload)
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	human, readHuman := captureHumanSink(t)
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		// Both servers share the localhost transport; either client works.
		opts.Client = good.Client()
		opts.MaxRetries = 0
		opts.Out = human
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--json", "-o", dir, good.URL, bad.URL})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, errBatchFailed)

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 2, "batch must emit one line per URL")
	byURL := map[string]jsonTestRecord{}
	for _, r := range recs {
		byURL[r.URL] = r
	}
	assert.True(t, byURL[good.URL].Success)
	assert.False(t, byURL[bad.URL].Success)
	assert.NotEmpty(t, byURL[bad.URL].Error)

	// No human tally on stdout (the JSON sink) and none in Run's human sink either;
	// writeSummary is never invoked in the --json batch branch.
	assert.NotContains(t, stdout.String(), "downloads succeeded")
	assert.NotContains(t, stderr.String(), "downloads succeeded")
	assert.NotContains(t, readHuman(), "downloads succeeded")
	assert.NotContains(t, readHuman(), "dr: saved to")
}

func TestJSON_BatchAllSuccess(t *testing.T) {
	payload := []byte(strings.Repeat("ok-", 5000))
	srv := rangedTestServer(t, payload)
	defer srv.Close()
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		opts.Client = srv.Client()
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	// Two distinct output names within the dir (derived from path); use distinct URLs.
	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--json", "-o", dir, srv.URL + "/a.bin", srv.URL + "/b.bin"})
	require.NoError(t, cmd.Execute(), "all-success batch must exit zero")

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 2)
	for _, r := range recs {
		assert.True(t, r.Success)
		assert.Empty(t, r.Error)
	}
}

func TestJSON_RejectStdoutDash(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "-o", "-", "http://example.com/file.bin"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdout")
	assert.Equal(t, 0, calls, "runFunc must never be called when --json + -o - is rejected")
}

func TestJSON_QuietNoError(t *testing.T) {
	payload := []byte(strings.Repeat("q-", 5000))
	srv := rangedTestServer(t, payload)
	defer srv.Close()
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		opts.Client = srv.Client()
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	cmd := NewRootCmd("v", "r", "d")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "--quiet", "-o", out, srv.URL})
	require.NoError(t, cmd.Execute(), "--json + --quiet must not error")

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1)
	assert.True(t, recs[0].Success)
}

// TestJSON_NonJSONModeUnchanged: without --json the human "dr: saved to" line is
// emitted to Options.Out (os.Stdout in production) and is NOT JSON. We capture
// Run's real human sink to positively assert the human line is present, and assert
// it does not parse as JSON, and that cmd.OutOrStdout() carries no NDJSON record.
func TestJSON_NonJSONModeUnchanged(t *testing.T) {
	payload := []byte(strings.Repeat("h-", 5000))
	srv := rangedTestServer(t, payload)
	defer srv.Close()
	// In non-json mode RunE leaves opts.Out = os.Stdout; emulate that real sink with
	// a temp file so the test can read back the actual human output.
	human, readHuman := captureHumanSink(t)
	stubRunFunc(t, func(ctx context.Context, opts download.Options) error {
		opts.Client = srv.Client()
		opts.Out = human
		return download.Run(ctx, opts)
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"-o", out, srv.URL})
	require.NoError(t, cmd.Execute())

	// Positive: the human path emitted the unchanged "dr: saved to <out>" line.
	humanOut := readHuman()
	assert.Contains(t, humanOut, "dr: saved to "+out, "human saved line unchanged in non-json mode")
	// And that human line is NOT JSON.
	var rec jsonTestRecord
	assert.Error(t, json.Unmarshal([]byte(strings.TrimSpace(humanOut)), &rec),
		"human output must not be JSON")
	// cmd's stdout (the NDJSON sink in --json mode) gets nothing in non-json mode.
	assert.Empty(t, strings.TrimSpace(stdout.String()), "no JSON record on cmd stdout in non-json mode")
}

// TestJSON_SingleInvalidURL: a single bad-scheme URL under --json must still emit
// exactly one JSON record (success=false + error) on stdout and exit non-zero,
// instead of returning a raw error with empty stdout (finding #2 / requirement #3).
func TestJSON_SingleInvalidURL(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--json", "ftp://example.com/file.bin"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, errJSONFailed)
	assert.Equal(t, 0, calls, "runFunc must not run for an invalid URL")

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1, "an invalid single URL must still emit one JSON record")
	assert.False(t, recs[0].Success)
	assert.NotEmpty(t, recs[0].Error)
	assert.Equal(t, "ftp://example.com/file.bin", recs[0].URL)
	assert.Equal(t, int64(-1), recs[0].Size, "size fallback present on early-failure record")
}

// TestJSON_BatchRejectsFileOutput: --json with multiple URLs and a plain-file -o
// must be rejected before any download, sharing the multi-URL directory guard with
// the non-json path (finding #1). Otherwise all URLs collide onto one path.
func TestJSON_BatchRejectsFileOutput(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "single.bin") // a regular file, not a directory

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "-o", out, "http://a.example/x", "http://b.example/y"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a directory")
	assert.Equal(t, 0, calls, "runFunc must not be called when the multi-URL output guard rejects")
}

// TestJSON_BatchRejectsChecksum: --json with multiple URLs and --checksum must be
// rejected up front (one sha256 cannot validate N files), sharing the non-json
// guard (finding #3).
func TestJSON_BatchRejectsChecksum(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	dir := t.TempDir()
	cmd := NewRootCmd("v", "r", "d")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "--checksum", "sha256:" + validChecksumHex, "-o", dir, "http://a.example/x", "http://b.example/y"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--checksum cannot be used with multiple URLs")
	assert.Equal(t, 0, calls, "runFunc must not be called when the multi-URL checksum guard rejects")
}

// TestJSON_RecordResumed: the emitted NDJSON line carries resumed=true when Run
// reports a resume. Drives the cmd-layer Result->jsonRecord->NDJSON wiring directly
// via the stub (engine-level resume is covered in download/result_test.go).
func TestJSON_RecordResumed(t *testing.T) {
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		if opts.Result != nil {
			opts.Result.URL = opts.URL
			opts.Result.Output = opts.Output
			opts.Result.Bytes = 1024
			opts.Result.Size = 1024
			opts.Result.Resumed = true
			opts.Result.Source = opts.URL
		}
		return nil
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	cmd := NewRootCmd("v", "r", "d")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "-o", out, "http://example.com/file.bin"})
	require.NoError(t, cmd.Execute())

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1)
	assert.True(t, recs[0].Resumed, "resumed must surface in the NDJSON record")
	assert.True(t, recs[0].Success)
}

// TestJSON_RecordSkipped: the emitted NDJSON line carries skipped=true,success=true
// when Run reports a skip-if-complete short-circuit.
func TestJSON_RecordSkipped(t *testing.T) {
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		if opts.Result != nil {
			opts.Result.URL = opts.URL
			opts.Result.Output = opts.Output
			opts.Result.Bytes = 2048
			opts.Result.Size = 2048
			opts.Result.Skipped = true
		}
		return nil
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	cmd := NewRootCmd("v", "r", "d")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "-o", out, "http://example.com/file.bin"})
	require.NoError(t, cmd.Execute())

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1)
	assert.True(t, recs[0].Skipped, "skipped must surface in the NDJSON record")
	assert.True(t, recs[0].Success)
}

// TestJSON_RecordMirrorSource: when a mirror served the bytes, the NDJSON record's
// source differs from the requested url.
func TestJSON_RecordMirrorSource(t *testing.T) {
	const primary = "http://primary.example/file.bin"
	const mirror = "http://mirror.example/file.bin"
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		if opts.Result != nil {
			opts.Result.URL = opts.URL
			opts.Result.Output = opts.Output
			opts.Result.Bytes = 512
			opts.Result.Size = 512
			opts.Result.Source = mirror
		}
		return nil
	})

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	cmd := NewRootCmd("v", "r", "d")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "-m", mirror, "-o", out, primary})
	require.NoError(t, cmd.Execute())

	recs := parseNDJSON(t, stdout.String())
	require.Len(t, recs, 1)
	assert.Equal(t, primary, recs[0].URL)
	assert.Equal(t, mirror, recs[0].Source, "source must record the serving mirror")
	assert.NotEqual(t, recs[0].URL, recs[0].Source)
}
