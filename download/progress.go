package download

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

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
	panic("not implemented")
}

// Add increments the byte counter; safe for concurrent callers.
func (p *Progress) Add(n int64) {
	panic("not implemented")
}

// Seed sets the initial byte count (used when resuming).
func (p *Progress) Seed(n int64) {
	panic("not implemented")
}

// Start launches the render loop (no-op when quiet or out is not a TTY).
func (p *Progress) Start(ctx context.Context) {
	panic("not implemented")
}

// Stop halts rendering and prints a final line.
func (p *Progress) Stop() {
	panic("not implemented")
}

// isTTY reports whether f is a character device (stdlib-only terminal detection
// via f.Stat and os.ModeCharDevice).
func isTTY(f *os.File) bool {
	panic("not implemented")
}

// formatBytes renders a byte count as B/KiB/MiB/GiB.
func formatBytes(n int64) string {
	panic("not implemented")
}

// formatRate renders bytes-per-second as a human string (e.g. "1.2 MiB/s").
func formatRate(bytesPerSec float64) string {
	panic("not implemented")
}

// percent returns done/total*100, or 0 when total<=0 (no divide-by-zero).
func percent(done, total int64) float64 {
	panic("not implemented")
}

// eta returns the estimated remaining time; 0 when rate<=0 or total<=0.
func eta(done, total int64, bytesPerSec float64) time.Duration {
	panic("not implemented")
}
