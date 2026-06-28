package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"
)

// defaultBackoffMax bounds the per-attempt backoff sleep.
const defaultBackoffMax = 30 * time.Second

// newRetry returns a retryFunc doing exponential backoff with full jitter, honoring
// ctx. It retries up to maxRetries times only while isRetryable(err) is true; a
// fatal error returns immediately. rng is injectable for deterministic tests; when
// nil a default time-seeded source is used.
func newRetry(maxRetries int, base time.Duration, rng func() float64) retryFunc {
	if maxRetries < 0 {
		maxRetries = 0
	}
	if rng == nil {
		src := rand.New(rand.NewSource(time.Now().UnixNano()))
		rng = src.Float64
	}
	return func(ctx context.Context, op func() error) error {
		var err error
		for attempt := 0; ; attempt++ {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			err = op()
			if err == nil {
				return nil
			}
			if !isRetryable(err) {
				return err
			}
			if attempt >= maxRetries {
				return err
			}
			d := backoff(attempt, base, defaultBackoffMax, rng())
			timer := time.NewTimer(d)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// backoff returns the (jittered) sleep before the given attempt (0-based), bounded
// by max. r is a [0,1) jitter sample.
func backoff(attempt int, base, max time.Duration, r float64) time.Duration {
	if base <= 0 || attempt < 0 {
		return 0
	}
	// Exponential window: base * 2^attempt, guarding against overflow.
	window := base
	for i := 0; i < attempt; i++ {
		if window >= max {
			window = max
			break
		}
		window *= 2
		if window <= 0 || window > max {
			window = max
			break
		}
	}
	if r < 0 {
		r = 0
	} else if r >= 1 {
		r = 0.999999999
	}
	d := time.Duration(float64(window) * r)
	if d < 0 {
		d = 0
	}
	if d > max {
		d = max
	}
	return d
}

// httpStatusError carries an HTTP status code for retry classification.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("download: unexpected http status %d", e.code)
}

// StatusCode returns the carried HTTP status.
func (e *httpStatusError) StatusCode() int {
	return e.code
}

// isRetryable classifies transient failures (5xx, 429, transient net/timeout, mid-
// stream EOF) as retryable; client errors (4xx except 429), ErrRemoteChanged, and
// context cancellation are fatal.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Context cancellation/deadline is always fatal for retry purposes.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// A changed remote can never be resumed; fatal.
	if errors.Is(err, ErrRemoteChanged) {
		return false
	}

	// HTTP status classification.
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		code := statusErr.StatusCode()
		if code == 429 {
			return true
		}
		if code >= 500 && code <= 599 {
			return true
		}
		// All other client/redirect/informational statuses are fatal.
		return false
	}

	// Mid-stream truncation is retryable.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Transient network errors / timeouts are retryable.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Unknown errors are treated as retryable transient failures.
	return true
}
