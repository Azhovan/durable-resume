package download

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These integration tests assert CORRECTNESS, not timing: LimitRate is set far
// above the tiny payload so the realClock-backed limiter adds no measurable
// delay, but every copy path still routes through gate(). They guard against a
// limiter that drops, duplicates, or misorders bytes across the three paths.
const hugeRate = int64(1) << 30 // 1 GiB/s: effectively unlimited for a small payload

func TestRunLimitRate_SegmentedCorrectBytes(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("limit-rate-segmented-", 5000)) // multi-chunk
	srv := rangedServer(t, payload, `"lr1"`)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 4
	opts.LimitRate = hugeRate

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// Sidecar and .part removed on verified success.
	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part should be renamed away on success")
	_, statErr = os.Stat(statePath(partPath(out)))
	assert.True(t, os.IsNotExist(statErr), "sidecar should be removed on success")
}

func TestRunLimitRate_SingleStreamCorrectBytes(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("limit-rate-single-", 4000))
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
	opts.LimitRate = hugeRate

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr := os.Stat(partPath(out))
	assert.True(t, os.IsNotExist(statErr), ".part should be renamed away on success")
}

func TestRunLimitRate_StdoutCorrectBytes(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("limit-rate-stdout-", 4000))
	srv := rangedServer(t, payload, `"lr2"`)
	defer srv.Close()

	var buf bytes.Buffer
	opts := baseOpts(t, srv.URL, stdoutDash)
	opts.Client = srv.Client()
	opts.Data = &buf
	opts.LimitRate = hugeRate

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, payload, buf.Bytes())
}

// virtualDelayMatch asserts the limiter's cumulative virtual delay equals the
// time the excess-over-burst payload takes at the cap: (totalBytes-burst)/rate.
// The virtualClock advances ONLY on sleep, so this is the exact deterministic
// throttle the limiter computed (no wall-clock timing). A nonzero result proves
// throttling actually fired; the value (independent of worker count) proves the
// limiter is a SINGLE shared AGGREGATE instance, not one bucket per worker.
func virtualDelayMatch(t *testing.T, vclk *virtualClock, totalBytes, rate int64) {
	t.Helper()
	wantSeconds := float64(totalBytes-copyBufferSize) / float64(rate)
	want := time.Duration(wantSeconds * float64(time.Second))
	got := vclk.total()
	require.Positive(t, got, "a small cap must produce a positive throttle delay")
	// Tolerance: float->Duration floors once per wait call; bytes/copyBufferSize
	// waits at ~1us each is the worst-case accumulated rounding, plus slack.
	tol := time.Duration(totalBytes/copyBufferSize+10) * time.Microsecond
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	assert.LessOrEqualf(t, diff, tol,
		"cumulative throttle delay %v should match aggregate-cap computed %v", got, want)
}

// TestRunLimitRate_SegmentedAggregateDelay drives a real segmented Run with
// concurrency>1 and a SMALL cap through an injected virtual clock, then asserts
// the computed cumulative delay equals (totalBytes-burst)/rate. Because the cap
// is aggregate, the delay reflects `rate`, not `N*rate`: a per-chunk limiter
// (one bucket per worker) would let each of N workers refill independently and
// the cumulative virtual delay would be far smaller, failing this assertion.
func TestRunLimitRate_SegmentedAggregateDelay(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("aggregate-cap-", 6000)) // multi-chunk
	srv := rangedServer(t, payload, `"agg"`)
	defer srv.Close()

	const rate = int64(8 * 1024) // 8 KiB/s: small enough to force a real deficit
	vclk := newVirtualClock()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.Concurrency = 4
	opts.LimitRate = rate
	opts.clk = vclk

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got) // correctness preserved under throttle

	virtualDelayMatch(t, vclk, int64(len(payload)), rate)
}

// TestRunLimitRate_SingleStreamAggregateDelay proves the single-stream path also
// routes through the shared limiter and throttles by the same deterministic math.
func TestRunLimitRate_SingleStreamAggregateDelay(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("single-cap-", 6000))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	const rate = int64(8 * 1024)
	vclk := newVirtualClock()

	dir := t.TempDir()
	out := filepath.Join(dir, "file.bin")
	opts := baseOpts(t, srv.URL, out)
	opts.Client = srv.Client()
	opts.LimitRate = rate
	opts.clk = vclk

	require.NoError(t, Run(context.Background(), opts))

	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	virtualDelayMatch(t, vclk, int64(len(payload)), rate)
}
