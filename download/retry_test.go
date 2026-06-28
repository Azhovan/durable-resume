package download

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// constRng returns a fixed jitter sample for deterministic tests.
func constRng(v float64) func() float64 {
	return func() float64 { return v }
}

func TestNewRetry_TransientThenSuccess(t *testing.T) {
	t.Parallel()

	retry := newRetry(3, time.Microsecond, constRng(0.5))

	attempts := 0
	err := retry(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return io.ErrUnexpectedEOF // retryable
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 3, attempts, "should succeed on the third attempt")
}

func TestNewRetry_FatalReturnsImmediately(t *testing.T) {
	t.Parallel()

	retry := newRetry(5, time.Microsecond, constRng(0.5))

	attempts := 0
	fatal := &httpStatusError{code: 404}
	err := retry(context.Background(), func() error {
		attempts++
		return fatal
	})

	require.Error(t, err)
	assert.Equal(t, 1, attempts, "fatal error must not be retried")
	var se *httpStatusError
	require.True(t, errors.As(err, &se))
	assert.Equal(t, 404, se.StatusCode())
}

func TestNewRetry_ExhaustsMaxRetries(t *testing.T) {
	t.Parallel()

	retry := newRetry(2, time.Microsecond, constRng(0.5))

	attempts := 0
	sentinel := io.ErrUnexpectedEOF
	err := retry(context.Background(), func() error {
		attempts++
		return sentinel
	})

	require.Error(t, err)
	// maxRetries=2 => initial attempt + 2 retries = 3 total.
	assert.Equal(t, 3, attempts)
	assert.True(t, errors.Is(err, sentinel), "should return the last error")
}

func TestNewRetry_CtxCanceledDuringBackoff(t *testing.T) {
	t.Parallel()

	// Large base so the backoff sleep is long; cancel mid-sleep.
	retry := newRetry(5, time.Hour, constRng(0.9))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	attempts := 0
	go func() {
		done <- retry(ctx, func() error {
			attempts++
			return io.ErrUnexpectedEOF // retryable => will enter backoff sleep
		})
	}()

	// Give the op a moment to run once and enter the sleep, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not return promptly after ctx cancellation")
	}
}

func TestNewRetry_CtxAlreadyCanceled(t *testing.T) {
	t.Parallel()

	retry := newRetry(3, time.Microsecond, constRng(0.5))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	attempts := 0
	err := retry(ctx, func() error {
		attempts++
		return nil
	})

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, 0, attempts, "op must not run when ctx is already canceled")
}

func TestBackoff_WithinBoundsAndGrows(t *testing.T) {
	t.Parallel()

	base := 100 * time.Millisecond
	max := 5 * time.Second

	// Bounds: across a sweep of attempts and r values, stay within [0, max].
	for attempt := 0; attempt < 12; attempt++ {
		for _, r := range []float64{0, 0.25, 0.5, 0.75, 0.999999} {
			d := backoff(attempt, base, max, r)
			assert.GreaterOrEqual(t, d, time.Duration(0))
			assert.LessOrEqual(t, d, max)
		}
	}

	// Grows with attempt for a fixed, non-zero jitter sample (until clamped at max).
	const r = 0.9
	prev := backoff(0, base, max, r)
	for attempt := 1; attempt < 6; attempt++ {
		cur := backoff(attempt, base, max, r)
		assert.GreaterOrEqual(t, cur, prev, "backoff should be non-decreasing with attempt")
		prev = cur
	}

	// Deterministic with a constant rng.
	a := backoff(3, base, max, r)
	b := backoff(3, base, max, r)
	assert.Equal(t, a, b)

	// r==0 => zero sleep (full jitter lower bound).
	assert.Equal(t, time.Duration(0), backoff(4, base, max, 0))
}

// fakeNetError implements net.Error for classification tests.
type fakeNetError struct{ timeout bool }

func (e *fakeNetError) Error() string   { return "fake net error" }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return true }

var _ net.Error = (*fakeNetError)(nil)

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"500", &httpStatusError{code: 500}, true},
		{"502", &httpStatusError{code: 502}, true},
		{"503", &httpStatusError{code: 503}, true},
		{"429", &httpStatusError{code: 429}, true},
		{"400", &httpStatusError{code: 400}, false},
		{"404", &httpStatusError{code: 404}, false},
		{"403", &httpStatusError{code: 403}, false},
		{"remote changed", ErrRemoteChanged, false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net timeout", &fakeNetError{timeout: true}, true},
		{"net error", &fakeNetError{}, true},
		{"wrapped 503", errors.Join(errors.New("ctx"), &httpStatusError{code: 503}), true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isRetryable(tt.err))
		})
	}
}

func TestHTTPStatusError(t *testing.T) {
	t.Parallel()

	e := &httpStatusError{code: 418}
	assert.Equal(t, 418, e.StatusCode())
	assert.Contains(t, e.Error(), "418")
}
