package download

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressAddConcurrent(t *testing.T) {
	p := NewProgress(1<<30, nil, true)

	const goroutines = 64
	const perG = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				p.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(goroutines*perG), p.done.Load())
}

func TestProgressSeed(t *testing.T) {
	p := NewProgress(100, nil, true)
	p.Seed(42)
	assert.Equal(t, int64(42), p.done.Load())

	p.Add(8)
	assert.Equal(t, int64(50), p.done.Load())
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"kib boundary", 1024, "1.00 KiB"},
		{"kib", 1536, "1.50 KiB"},
		{"just below mib", 1024*1024 - 1, "1024.00 KiB"},
		{"mib boundary", 1024 * 1024, "1.00 MiB"},
		{"mib", 1024 * 1024 * 3 / 2, "1.50 MiB"},
		{"gib boundary", 1024 * 1024 * 1024, "1.00 GiB"},
		{"gib", 1024 * 1024 * 1024 * 5 / 2, "2.50 GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatBytes(tt.n))
		})
	}
}

func TestFormatRate(t *testing.T) {
	tests := []struct {
		name string
		bps  float64
		want string
	}{
		{"zero", 0, "0 B/s"},
		{"bytes", 500, "500 B/s"},
		{"kib", 2048, "2.00 KiB/s"},
		{"mib", 1024 * 1024, "1.00 MiB/s"},
		{"mib fraction", 1024 * 1024 * 1.2, "1.20 MiB/s"},
		{"gib", 1024 * 1024 * 1024, "1.00 GiB/s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRate(tt.bps))
		})
	}
}

func TestPercent(t *testing.T) {
	tests := []struct {
		name        string
		done, total int64
		want        float64
	}{
		{"zero done", 0, 100, 0},
		{"partial", 25, 100, 25},
		{"full", 100, 100, 100},
		{"total zero", 50, 0, 0},
		{"total negative", 50, -1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, percent(tt.done, tt.total), 1e-9)
		})
	}
}

func TestETA(t *testing.T) {
	tests := []struct {
		name        string
		done, total int64
		bps         float64
		want        time.Duration
	}{
		{"rate zero", 0, 100, 0, 0},
		{"rate negative", 0, 100, -5, 0},
		{"total zero", 0, 0, 10, 0},
		{"complete", 100, 100, 10, 0},
		{"normal", 0, 100, 10, 10 * time.Second},
		{"normal partial", 50, 100, 25, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, eta(tt.done, tt.total, tt.bps))
		})
	}
}

func TestIsTTYRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "progress")
	require.NoError(t, err)
	defer f.Close()

	assert.False(t, isTTY(f))
	assert.False(t, isTTY(nil))
}

func TestProgressQuietWritesNothing(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	defer f.Close()

	p := NewProgress(100, f, true) // quiet
	ctx := context.Background()
	p.Start(ctx)
	p.Add(50)
	p.Stop()

	info, err := f.Stat()
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())
}

func TestProgressNonTTYWritesNothing(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	defer f.Close()

	// not quiet, but a regular file is not a TTY => no output.
	p := NewProgress(100, f, false)
	ctx := context.Background()
	p.Start(ctx)
	p.Add(100)
	p.Stop()

	info, err := f.Stat()
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())
}
