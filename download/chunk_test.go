package download

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writerAtBuf is a concurrency-safe in-memory io.WriterAt backed by a fixed slice.
type writerAtBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *writerAtBuf) WriteAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if off < 0 || off+int64(len(p)) > int64(len(b.buf)) {
		return 0, io.ErrShortWrite
	}
	n := copy(b.buf[off:], p)
	return n, nil
}

func testModTime() time.Time {
	return time.Unix(1_600_000_000, 0).UTC()
}

// newByteSeeker adapts a byte slice to io.ReadSeeker for http.ServeContent.
func newByteSeeker(data []byte) io.ReadSeeker {
	return &byteSeeker{data: data}
}

type byteSeeker struct {
	data []byte
	pos  int64
}

func (s *byteSeeker) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *byteSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.pos + offset
	case io.SeekEnd:
		abs = int64(len(s.data)) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	s.pos = abs
	return abs, nil
}

func TestCeilDiv(t *testing.T) {
	tests := []struct {
		name string
		a, b int64
		want int64
	}{
		{"10/3 rounds up", 10, 3, 4},
		{"9/3 exact", 9, 3, 3},
		{"0/3 is zero", 0, 3, 0},
		{"1/1 is one", 1, 1, 1},
		{"divide by zero is zero", 5, 0, 0},
		{"exact multiple", 8, 4, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ceilDiv(tt.a, tt.b))
		})
	}
}

func TestRemaining(t *testing.T) {
	tests := []struct {
		name       string
		ch         chunk
		wantOffset int64
		wantTodo   bool
	}{
		{
			name:       "fresh chunk done=0",
			ch:         chunk{start: 0, end: 9, done: 0},
			wantOffset: 0,
			wantTodo:   true,
		},
		{
			name:       "partially done",
			ch:         chunk{start: 100, end: 199, done: 30},
			wantOffset: 130,
			wantTodo:   true,
		},
		{
			name:       "fully done",
			ch:         chunk{start: 0, end: 9, done: 10},
			wantOffset: 10,
			wantTodo:   false,
		},
		{
			name:       "nonzero start fully done",
			ch:         chunk{start: 50, end: 99, done: 50},
			wantOffset: 100,
			wantTodo:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := tt.ch
			offset, todo := ch.remaining()
			assert.Equal(t, tt.wantOffset, offset)
			assert.Equal(t, tt.wantTodo, todo)
		})
	}
}

// assertChunksValid verifies a chunk plan is gapless, non-overlapping, has no
// empty chunks, and exactly covers [0,size).
func assertChunksValid(t *testing.T, chunks []chunk, size int64) {
	t.Helper()
	require.NotEmpty(t, chunks)
	var prevEnd int64 = -1
	for i, ch := range chunks {
		assert.Equal(t, i, ch.index, "index should be sequential")
		assert.LessOrEqual(t, ch.start, ch.end, "chunk %d must be non-empty", i)
		assert.Equal(t, prevEnd+1, ch.start, "chunk %d must start right after previous", i)
		prevEnd = ch.end
	}
	assert.Equal(t, int64(0), chunks[0].start, "first chunk starts at 0")
	assert.Equal(t, size-1, chunks[len(chunks)-1].end, "last chunk ends at size-1")
}

func TestPlanChunks(t *testing.T) {
	t.Run("divisible by concurrency yields equal chunks", func(t *testing.T) {
		chunks := planChunks(100, 4)
		require.Len(t, chunks, 4)
		assertChunksValid(t, chunks, 100)
		for _, ch := range chunks {
			assert.Equal(t, int64(25), ch.end-ch.start+1)
		}
	})

	t.Run("remainder carried by chunks gapless", func(t *testing.T) {
		// 103 / 4 => per = 26; chunks: 0-25,26-51,52-77,78-102
		chunks := planChunks(103, 4)
		require.Len(t, chunks, 4)
		assertChunksValid(t, chunks, 103)
		// Total coverage equals size.
		var total int64
		for _, ch := range chunks {
			total += ch.end - ch.start + 1
		}
		assert.Equal(t, int64(103), total)
	})

	t.Run("size smaller than concurrency clamps to fewer chunks", func(t *testing.T) {
		chunks := planChunks(3, 8)
		require.Len(t, chunks, 3)
		assertChunksValid(t, chunks, 3)
		for _, ch := range chunks {
			assert.Equal(t, int64(1), ch.end-ch.start+1, "each chunk is exactly one byte")
		}
	})

	t.Run("size==0 returns no chunks", func(t *testing.T) {
		assert.Empty(t, planChunks(0, 4))
	})

	t.Run("concurrency<1 does not panic and is clamped", func(t *testing.T) {
		chunks := planChunks(10, 0)
		require.Len(t, chunks, 1)
		assertChunksValid(t, chunks, 10)

		chunksNeg := planChunks(10, -5)
		require.Len(t, chunksNeg, 1)
		assertChunksValid(t, chunksNeg, 10)
	})

	t.Run("negative size returns no chunks", func(t *testing.T) {
		assert.Empty(t, planChunks(-1, 4))
	})
}

// rangeServer serves data honoring Range requests with 206 responses.
func rangeServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "file.bin", testModTime(), newByteSeeker(data))
	}))
}

func TestFetchChunkWritesAtOffset(t *testing.T) {
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	srv := rangeServer(t, data)
	defer srv.Close()

	// Chunk covering bytes [50,99].
	ch := &chunk{index: 1, start: 50, end: 99, done: 0}
	dst := make([]byte, 256)
	w := &writerAtBuf{buf: dst}

	var sum int64
	onBytes := func(n int64) { atomic.AddInt64(&sum, n) }

	err := fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, w, onBytes, nil)
	require.NoError(t, err)

	assert.Equal(t, int64(50), atomic.LoadInt64(&sum), "onBytes sum equals chunk length")
	assert.Equal(t, int64(50), ch.done, "ch.done advanced to full chunk length")
	assert.Equal(t, data[50:100], dst[50:100], "bytes written at correct absolute offset")
	// Bytes outside the chunk are untouched.
	assert.Equal(t, make([]byte, 50), dst[0:50])
	assert.Equal(t, make([]byte, 156), dst[100:256])
}

func TestFetchChunkResumeWithDone(t *testing.T) {
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	srv := rangeServer(t, data)
	defer srv.Close()

	// Chunk [50,99], already fetched first 20 bytes.
	ch := &chunk{index: 1, start: 50, end: 99, done: 20}
	dst := make([]byte, 256)
	// Pre-populate the already-done region as a prior run would have.
	copy(dst[50:70], data[50:70])
	w := &writerAtBuf{buf: dst}

	var sum int64
	onBytes := func(n int64) { atomic.AddInt64(&sum, n) }

	err := fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, w, onBytes, nil)
	require.NoError(t, err)

	assert.Equal(t, int64(30), atomic.LoadInt64(&sum), "only remaining 30 bytes fetched")
	assert.Equal(t, int64(50), ch.done, "ch.done advanced to full chunk length")
	assert.Equal(t, data[50:100], dst[50:100], "full chunk content correct after resume append")
}

func TestFetchChunkRejectsNon206(t *testing.T) {
	data := make([]byte, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ignore Range, always return 200 with full body.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	ch := &chunk{index: 0, start: 0, end: 99, done: 0}
	w := &writerAtBuf{buf: make([]byte, 100)}

	err := fetchChunk(context.Background(), srv.Client(), srv.URL, nil, ch, w, func(int64) {}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRangeNot206), "expected ErrRangeNot206, got %v", err)
}

func TestFetchChunkNothingToDo(t *testing.T) {
	// A fully-done chunk performs no request and returns nil.
	ch := &chunk{index: 0, start: 0, end: 9, done: 10}
	w := &writerAtBuf{buf: make([]byte, 10)}
	called := false
	err := fetchChunk(context.Background(), http.DefaultClient, "http://invalid.invalid", nil, ch, w, func(int64) { called = true }, nil)
	require.NoError(t, err)
	assert.False(t, called, "onBytes must not be called for a completed chunk")
}
