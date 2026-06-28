package download

import (
	"time"
)

// newRetry returns a retryFunc doing exponential backoff with full jitter, honoring
// ctx. It retries up to maxRetries times only while isRetryable(err) is true; a
// fatal error returns immediately. rng is injectable for deterministic tests; when
// nil a default time-seeded source is used.
func newRetry(maxRetries int, base time.Duration, rng func() float64) retryFunc {
	panic("not implemented")
}

// backoff returns the (jittered) sleep before the given attempt (0-based), bounded
// by max. r is a [0,1) jitter sample.
func backoff(attempt int, base, max time.Duration, r float64) time.Duration {
	panic("not implemented")
}

// httpStatusError carries an HTTP status code for retry classification.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	panic("not implemented")
}

// StatusCode returns the carried HTTP status.
func (e *httpStatusError) StatusCode() int {
	panic("not implemented")
}

// isRetryable classifies transient failures (5xx, 429, transient net/timeout, mid-
// stream EOF) as retryable; client errors (4xx except 429), ErrRemoteChanged, and
// context cancellation are fatal.
func isRetryable(err error) bool {
	panic("not implemented")
}
