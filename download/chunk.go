package download

import (
	"context"
	"fmt"
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
	offset = c.start + c.done
	todo = offset <= c.end
	return offset, todo
}

// ceilDiv computes ceil(a/b) in pure integers; b==0 yields 0.
func ceilDiv(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return (a + b - 1) / b
}

// planChunks splits [0,size) into up to `concurrency` disjoint contiguous chunks
// of near-equal size. concurrency is clamped to >=1; chunks never overlap.
func planChunks(size int64, concurrency int) []chunk {
	if concurrency < 1 {
		concurrency = 1
	}
	if size <= 0 {
		return nil
	}
	// Never produce more chunks than bytes, so no chunk is empty.
	n := int64(concurrency)
	if n > size {
		n = size
	}
	per := ceilDiv(size, n)

	chunks := make([]chunk, 0, n)
	var start int64
	idx := 0
	for start < size {
		end := start + per - 1
		if end >= size {
			end = size - 1
		}
		chunks = append(chunks, chunk{
			index: idx,
			start: start,
			end:   end,
		})
		idx++
		start = end + 1
	}
	return chunks
}

// fetchChunk fetches ch's remaining range and writes bytes via w.WriteAt at the
// correct absolute offset, advancing ch.done and calling onBytes(n) after each
// successful write. Validates a 206 response (else ErrRangeNot206). Uses a fixed
// copyBufferSize buffer, never a per-chunk buffer. Safe for concurrent use across
// distinct chunks because WriteAt targets disjoint offsets.
func fetchChunk(ctx context.Context, c *http.Client, url string, hdr http.Header, ch *chunk, w io.WriterAt, onBytes func(int64)) error {
	offset, todo := ch.remaining()
	if !todo {
		return nil
	}

	req, err := newRequest(ctx, url, hdr, offset, ch.end)
	if err != nil {
		return fmt.Errorf("download: build chunk request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("download: fetch chunk: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download: chunk %d got status %d: %w", ch.index, resp.StatusCode, ErrRangeNot206)
	}

	buf := make([]byte, copyBufferSize)
	for {
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			// Absolute offset advances as ch.done advances.
			at := ch.start + ch.done
			nw, werr := w.WriteAt(buf[:nr], at)
			if nw > 0 {
				ch.done += int64(nw)
				if onBytes != nil {
					onBytes(int64(nw))
				}
			}
			if werr != nil {
				return fmt.Errorf("download: write chunk %d: %w", ch.index, werr)
			}
			if nw < nr {
				return fmt.Errorf("download: short write chunk %d: %w", ch.index, io.ErrShortWrite)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("download: read chunk %d: %w", ch.index, rerr)
		}
	}

	return nil
}
