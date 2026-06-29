package download

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// virtualClock is a deterministic, concurrency-safe test clock. sleep advances
// now() by the requested duration and accumulates the total virtual delay, so a
// test asserts the COMPUTED delay rather than wall-clock elapsed time. It never
// actually sleeps.
type virtualClock struct {
	mu         sync.Mutex
	cur        time.Time
	totalSleep time.Duration
	nowCalls   int
	sleepCalls int

	// cancelOnSleep, when non-nil, is observed at the top of sleep: if it is
	// already done, sleep returns ctx.Err() without advancing, modeling a cancel
	// that lands while a worker is throttled.
}

func newVirtualClock() *virtualClock {
	return &virtualClock{cur: time.Unix(0, 0)}
}

func (c *virtualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowCalls++
	return c.cur
}

func (c *virtualClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleepCalls++
	c.mu.Unlock()
	if d <= 0 {
		return ctx.Err()
	}
	// Honor a cancel that is already pending so the test can assert prompt
	// cancellation without any real sleep.
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.cur = c.cur.Add(d)
	c.totalSleep += d
	c.mu.Unlock()
	return nil
}

func (c *virtualClock) total() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalSleep
}

// countingClock records how many times now()/sleep() are invoked so a test can
// prove the limiter path is never entered when unlimited.
type countingClock struct {
	now32   int64
	sleep32 int64
}

func (c *countingClock) now() time.Time {
	atomic.AddInt64(&c.now32, 1)
	return time.Unix(0, 0)
}

func (c *countingClock) sleep(ctx context.Context, _ time.Duration) error {
	atomic.AddInt64(&c.sleep32, 1)
	return ctx.Err()
}

func TestNewRateLimiter_UnlimitedIsNil(t *testing.T) {
	t.Parallel()
	assert.Nil(t, newRateLimiter(0, nil))
	assert.Nil(t, newRateLimiter(-1, nil))
}

// TestGate_NilNoOp proves gate never touches the clock when unlimited: a nil
// limiter returns nil immediately and the LimitRate<=0 path allocates nothing.
func TestGate_NilNoOp(t *testing.T) {
	t.Parallel()
	cc := &countingClock{}
	lim := newRateLimiter(0, cc) // <=0 => nil regardless of clk
	require.Nil(t, lim)

	for i := 0; i < 100; i++ {
		require.NoError(t, gate(context.Background(), lim, 1<<20))
	}
	assert.Equal(t, int64(0), atomic.LoadInt64(&cc.now32))
	assert.Equal(t, int64(0), atomic.LoadInt64(&cc.sleep32))
}

// TestTokenBucket_BurstFirstReadFree confirms the bucket starts full so the
// first buffer-sized read is admitted with ZERO delay, then the next over-rate
// read incurs a positive computed delay.
func TestTokenBucket_BurstFirstReadFree(t *testing.T) {
	t.Parallel()
	vclk := newVirtualClock()
	lim := newRateLimiter(1000, vclk) // 1000 B/s, burst = copyBufferSize
	require.NotNil(t, lim)

	// First read of exactly burst bytes: bucket starts full => no delay.
	require.NoError(t, lim.wait(context.Background(), copyBufferSize))
	assert.Equal(t, time.Duration(0), vclk.total(), "first burst read must be free")

	// Next read drains into a deficit => a positive delay.
	require.NoError(t, lim.wait(context.Background(), 1000))
	assert.Positive(t, vclk.total(), "an over-rate read must incur a computed delay")
}

// TestTokenBucket_CumulativeDelayMath feeds N bytes via repeated waits and
// asserts the SUM of computed delays equals (N-burst)/rate seconds, the time the
// excess-over-burst bytes take at the cap. Fully deterministic; no real sleep.
func TestTokenBucket_CumulativeDelayMath(t *testing.T) {
	t.Parallel()
	const rate = 1000.0 // B/s
	vclk := newVirtualClock()
	lim := newRateLimiter(int64(rate), vclk)
	require.NotNil(t, lim)

	// Feed many copyBufferSize reads. Because the virtual clock only advances on
	// sleep (never between waits unless we slept), the only refill comes from the
	// sleeps themselves, so total computed delay == (totalBytes - burst)/rate.
	const reads = 50
	var totalBytes int64
	for i := 0; i < reads; i++ {
		require.NoError(t, lim.wait(context.Background(), copyBufferSize))
		totalBytes += copyBufferSize
	}

	wantSeconds := float64(totalBytes-copyBufferSize) / rate
	want := time.Duration(wantSeconds * float64(time.Second))
	// Allow a tiny rounding tolerance from float->Duration flooring per call.
	got := vclk.total()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	assert.LessOrEqual(t, diff, time.Duration(reads)*time.Microsecond,
		"cumulative delay %v should match computed %v", got, want)
}

// TestTokenBucket_CancelDuringThrottle confirms a cancelled ctx aborts a wait
// promptly with context.Canceled and the clock records no further advance.
func TestTokenBucket_CancelDuringThrottle(t *testing.T) {
	t.Parallel()
	vclk := newVirtualClock()
	lim := newRateLimiter(1000, vclk)
	require.NotNil(t, lim)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// A large read forces a positive deficit/delay; the cancelled ctx must win.
	err := lim.wait(ctx, 1<<20)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, time.Duration(0), vclk.total(), "no virtual time advances on a cancelled wait")
}

// blockingClock.sleep parks on ctx.Done() (like realClock for a positive d) but
// without a timer, so the ONLY way it returns is a cancel that lands WHILE the
// caller is already inside sleep. entered is closed on entry so the test can
// cancel deterministically once a worker is genuinely parked. It models the
// production-relevant case: a stuck download cancelled mid-throttle.
type blockingClock struct {
	entered chan struct{}
	once    sync.Once
}

func newBlockingClock() *blockingClock { return &blockingClock{entered: make(chan struct{})} }

func (c *blockingClock) now() time.Time { return time.Unix(0, 0) }

func (c *blockingClock) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	c.once.Do(func() { close(c.entered) }) // signal: a worker is now parked in sleep
	<-ctx.Done()                           // unblock ONLY on a mid-sleep cancel
	return ctx.Err()
}

// TestTokenBucket_CancelArrivesMidSleep exercises the tokenBucket.wait ->
// clk.sleep cancel path with a deficit ALREADY reserved: wait computes a large
// positive delay, parks in sleep, and only then does the test cancel the ctx.
// This complements TestTokenBucket_CancelDuringThrottle (pre-cancelled) and
// TestRealClock_Sleep_PositiveCancel (clk in isolation) by driving the cancel
// through wait's real deficit computation.
func TestTokenBucket_CancelArrivesMidSleep(t *testing.T) {
	t.Parallel()
	bclk := newBlockingClock()
	lim := newRateLimiter(1000, bclk)
	require.NotNil(t, lim)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		// A large read forces a big positive deficit -> a long computed sleep that
		// blockingClock parks on until we cancel.
		errCh <- lim.wait(ctx, 1<<20)
	}()

	<-bclk.entered // wait until the goroutine is genuinely parked inside sleep
	cancel()       // cancel arrives MID-sleep, with the deficit already reserved

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("wait did not return promptly after a mid-sleep cancel")
	}
}

// TestRealClock_Sleep_CancelledZeroDuration confirms realClock.sleep returns
// ctx.Err() on an already-cancelled ctx even with d<=0.
func TestRealClock_Sleep_CancelledZeroDuration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, realClock{}.sleep(ctx, 0), context.Canceled)
	assert.ErrorIs(t, realClock{}.sleep(ctx, -time.Second), context.Canceled)
}

// TestRealClock_Sleep_PositiveCancel confirms a positive sleep is interrupted by
// a cancel rather than waiting out the full duration.
func TestRealClock_Sleep_PositiveCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := realClock{}.sleep(ctx, time.Hour)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second, "cancel must interrupt the sleep")
}

// TestTokenBucket_ConcurrencyConverges launches many goroutines sharing ONE
// bucket and asserts (under -race) no race and that aggregate admitted bytes per
// accumulated virtual second never exceeds the cap (proving aggregate, not
// N*cap, convergence). A mutex-safe virtual clock advances only on sleep.
func TestTokenBucket_ConcurrencyConverges(t *testing.T) {
	t.Parallel()
	const (
		rate    = 4096.0
		workers = 8
		perWk   = 40
	)
	vclk := newVirtualClock()
	lim := newRateLimiter(int64(rate), vclk)
	require.NotNil(t, lim)

	var admitted int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWk; i++ {
				const n = 1024
				require.NoError(t, lim.wait(context.Background(), n))
				atomic.AddInt64(&admitted, n)
			}
		}()
	}
	wg.Wait()

	total := atomic.LoadInt64(&admitted)
	elapsed := vclk.total().Seconds()
	// burst is admitted for free, so admitted <= burst + rate*elapsed. Equivalent:
	// (admitted - burst)/elapsed <= rate (within rounding).
	if elapsed > 0 {
		effective := float64(total-copyBufferSize) / elapsed
		assert.LessOrEqual(t, effective, rate*1.01,
			"aggregate throughput %.0f B/s must not exceed cap %.0f", effective, rate)
	}
	assert.Equal(t, int64(workers*perWk*1024), total)
}
