package download

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// renderInterval is how often the live progress line is redrawn.
const renderInterval = 200 * time.Millisecond

// Progress is a concurrency-safe download progress reporter with a single render
// goroutine. total<=0 means unknown size.
type Progress struct {
	total int64
	done  atomic.Int64
	out   *os.File
	quiet bool
	start time.Time
	stop  chan struct{}
	wg    sync.WaitGroup
}

// NewProgress builds a reporter. It renders only when !quiet and isTTY(out).
func NewProgress(total int64, out *os.File, quiet bool) *Progress {
	return &Progress{
		total: total,
		out:   out,
		quiet: quiet,
		stop:  make(chan struct{}),
	}
}

// Add increments the byte counter; safe for concurrent callers.
func (p *Progress) Add(n int64) {
	p.done.Add(n)
}

// Seed sets the initial byte count (used when resuming).
func (p *Progress) Seed(n int64) {
	p.done.Store(n)
}

// active reports whether rendering should occur (not quiet, with a usable TTY).
func (p *Progress) active() bool {
	return !p.quiet && p.out != nil && isTTY(p.out)
}

// Start launches the render loop (no-op when quiet or out is not a TTY).
func (p *Progress) Start(ctx context.Context) {
	if !p.active() {
		return
	}
	p.start = time.Now()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(renderInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case <-ticker.C:
				p.render(false)
			}
		}
	}()
}

// Stop halts rendering and prints a final line.
func (p *Progress) Stop() {
	if !p.active() {
		return
	}
	select {
	case <-p.stop:
		// already stopped
	default:
		close(p.stop)
	}
	p.wg.Wait()
	p.render(true)
}

// render writes a single progress line. When final is true it terminates the
// line with a newline; otherwise it uses a carriage return to redraw in place.
func (p *Progress) render(final bool) {
	done := p.done.Load()
	elapsed := time.Since(p.start).Seconds()
	var rate float64
	if elapsed > 0 {
		rate = float64(done) / elapsed
	}

	var line string
	if p.total > 0 {
		line = fmt.Sprintf(
			"\r%6.2f%% %s / %s  %s  ETA %s",
			percent(done, p.total),
			formatBytes(done),
			formatBytes(p.total),
			formatRate(rate),
			eta(done, p.total, rate),
		)
	} else {
		line = fmt.Sprintf("\r%s  %s", formatBytes(done), formatRate(rate))
	}
	if final {
		line += "\n"
	}
	fmt.Fprint(p.out, line)
}

// isTTY reports whether f is a character device (stdlib-only terminal detection
// via f.Stat and os.ModeCharDevice).
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// formatBytes renders a byte count as B/KiB/MiB/GiB.
func formatBytes(n int64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GiB", float64(n)/gib)
	case n >= mib:
		return fmt.Sprintf("%.2f MiB", float64(n)/mib)
	case n >= kib:
		return fmt.Sprintf("%.2f KiB", float64(n)/kib)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// formatRate renders bytes-per-second as a human string (e.g. "1.2 MiB/s").
func formatRate(bytesPerSec float64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case bytesPerSec >= gib:
		return fmt.Sprintf("%.2f GiB/s", bytesPerSec/gib)
	case bytesPerSec >= mib:
		return fmt.Sprintf("%.2f MiB/s", bytesPerSec/mib)
	case bytesPerSec >= kib:
		return fmt.Sprintf("%.2f KiB/s", bytesPerSec/kib)
	default:
		return fmt.Sprintf("%.0f B/s", bytesPerSec)
	}
}

// percent returns done/total*100, or 0 when total<=0 (no divide-by-zero).
func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(done) / float64(total) * 100
}

// eta returns the estimated remaining time; 0 when rate<=0 or total<=0.
func eta(done, total int64, bytesPerSec float64) time.Duration {
	if bytesPerSec <= 0 || total <= 0 {
		return 0
	}
	remaining := total - done
	if remaining <= 0 {
		return 0
	}
	secs := float64(remaining) / bytesPerSec
	return time.Duration(secs * float64(time.Second))
}
