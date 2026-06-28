package download

import (
	"context"
	"io"
	"net/http"
)

type chunk struct {
	index int
	start int64 // absolute inclusive start offset in the file
	end   int64 // absolute inclusive end offset (HTTP Range semantics)
	done  int64 // bytes already written for this chunk (resume cursor)
}

// remaining reports the absolute offset of the next byte to fetch and whether
// any work is left for this chunk.
func (c *chunk) remaining() (offset int64, todo bool) {
	panic("not implemented")
}

// ceilDiv computes ceil(a/b) in pure integers; b==0 yields 0.
func ceilDiv(a, b int64) int64 {
	panic("not implemented")
}

// planChunks splits [0,size) into up to `concurrency` disjoint contiguous chunks
// of near-equal size. concurrency is clamped to >=1; chunks never overlap.
func planChunks(size int64, concurrency int) []chunk {
	panic("not implemented")
}

// fetchChunk fetches ch's remaining range and writes bytes via w.WriteAt at the
// correct absolute offset, advancing ch.done and calling onBytes(n) after each
// successful write. Validates a 206 response (else ErrRangeNot206). Uses a fixed
// copyBufferSize buffer, never a per-chunk buffer. Safe for concurrent use across
// distinct chunks because WriteAt targets disjoint offsets.
func fetchChunk(ctx context.Context, c *http.Client, url string, hdr http.Header, ch *chunk, w io.WriterAt, onBytes func(int64)) error {
	panic("not implemented")
}
