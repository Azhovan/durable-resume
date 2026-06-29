package download

import (
	"context"
	"sync"
	"time"
)

// rateLimiter gates the AGGREGATE byte throughput of a download. A SINGLE
// limiter instance is shared by every concurrent chunk worker, so the cap is a
// whole-download cap (with -c 4 and --limit-rate 1M the total is ~1 MiB/s, not
// 4). wait blocks until n more bytes may be transferred, honoring ctx; it
// returns ctx.Err() if the context is cancelled while waiting. n<=0 returns nil.
//
// UNLIMITED is represented by a nil rateLimiter, NOT an implementation: see
// newRateLimiter and gate. A nil limiter allocates nothing and has zero
// per-byte overhead.
type rateLimiter interface {
	wait(ctx context.Context, n int64) error
}

// clock is the injectable time seam so rate math is asserted deterministically
// without real sleeps. Production uses realClock; tests use a virtual clock that
// advances now() by the requested sleep and records the cumulative delay, so a
// test asserts the COMPUTED delay instead of wall-clock elapsed.
type clock interface {
	now() time.Time
	// sleep blocks for d or until ctx is done, returning ctx.Err() on cancel.
	// It reuses retry.go's timer+select idiom so the wait is cancellable.
	sleep(ctx context.Context, d time.Duration) error
}

// realClock is the production clock.
type realClock struct{}

func (realClock) now() time.Time { return time.Now() }

func (realClock) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		// Observe an already-cancelled ctx so a saturated bucket cannot ignore
		// a cancel on a zero-length wait.
		return ctx.Err()
	}
	timer := time.NewTimer(d) // same idiom as retry.go's backoff
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// tokenBucket is a stdlib aggregate token-bucket limiter, safe for concurrent
// use. Tokens are bytes; they refill continuously at rate bytes/sec up to burst.
// All state (tokens, last) is guarded by mu so N workers share one bucket and
// the total rate converges to rate, never N*rate.
//
// wait(n) under mu: (1) refill by elapsed*rate capped at burst; (2) SUBTRACT n
// (tokens may go negative = a reserved deficit); (3) compute d from the
// post-subtraction deficit; release mu; sleep(ctx, d) OUTSIDE the lock. The
// deficit is reserved the instant tokens goes negative, so concurrent waiters
// SERIALIZE their delays (a virtual reservation queue) instead of all sleeping
// on the same instant -> aggregate convergence. The lock scope (subtract under
// lock, sleep outside) is load-bearing; do not move the subtraction after the
// unlock or two workers could both see positive tokens and over-admit (N*cap).
type tokenBucket struct {
	rate  float64 // bytes per second (> 0)
	burst float64 // max tokens (bytes); also the initial fill
	clk   clock

	mu     sync.Mutex
	tokens float64   // current tokens (bytes); may be negative (reserved deficit)
	last   time.Time // last refill instant (clk.now())
}

// newRateLimiter builds the limiter for the resolved cap. bytesPerSec<=0 returns
// nil: the TRUE no-op path (no allocation, no goroutine, no clock). clk nil =>
// realClock. burst is one copy buffer (copyBufferSize) so the first buffer-sized
// read passes with zero delay; a single read can therefore always be admitted in
// one shot. The bucket starts full (tokens=burst).
func newRateLimiter(bytesPerSec int64, clk clock) rateLimiter {
	if bytesPerSec <= 0 {
		return nil // unlimited: no allocation, no-op
	}
	if clk == nil {
		clk = realClock{}
	}
	burst := float64(copyBufferSize)
	return &tokenBucket{
		rate:   float64(bytesPerSec),
		burst:  burst,
		clk:    clk,
		tokens: burst,
		last:   clk.now(),
	}
}

func (b *tokenBucket) wait(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	b.mu.Lock()
	now := b.clk.now()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	b.tokens -= float64(n) // reserve n bytes (may go negative)
	var d time.Duration
	if b.tokens < 0 {
		d = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()

	// Sleep OUTSIDE the lock so concurrent workers do not serialize on mu while
	// one is throttled; the deficit was already reserved under the lock.
	return b.clk.sleep(ctx, d)
}

// gate calls lim.wait(ctx, n) when lim is non-nil, and is a pure no-op when lim
// is nil (unlimited). It is the single chokepoint the copy loops call, so the
// unlimited path is a branch-only nil check with no method dispatch.
func gate(ctx context.Context, lim rateLimiter, n int64) error {
	if lim == nil {
		return nil
	}
	return lim.wait(ctx, n)
}
