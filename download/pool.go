package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// retryFunc runs op with retry/backoff; see retry.go.
type retryFunc func(ctx context.Context, op func() error) error

// runSegmented downloads all not-yet-complete chunks with bounded concurrency,
// flushing st via st.Save(statePath) periodically and once on exit. The first
// fatal error cancels remaining workers and is returned (wrapped with ErrChunkFailed
// when it originates from a chunk fetch). WriteAt offsets are disjoint, so file
// writes need no extra locking; State.mu guards only the State struct.
func runSegmented(ctx context.Context, c *http.Client, url string, hdr http.Header, dst *os.File, st *State, statePath string, chunks []chunk, concurrency int, onBytes func(int64), retry retryFunc) error {
	if concurrency < 1 {
		concurrency = 1
	}

	// Derived context so the first fatal error (or an external cancel) tears
	// down all in-flight workers promptly.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Periodic state flush. Runs alongside the workers and always performs a
	// final save when the workers finish (or the group is cancelled).
	var flushWG sync.WaitGroup
	flushWG.Add(1)
	go func() {
		defer flushWG.Done()
		ticker := time.NewTicker(stateFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = st.Save(statePath)
			}
		}
	}()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	var errOnce sync.Once
	var firstErr error
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for i := range chunks {
		ch := &chunks[i]
		if _, todo := ch.remaining(); !todo {
			continue // already complete (resume)
		}

		// Stop dispatching new work as soon as the group is cancelled.
		if err := ctx.Err(); err != nil {
			fail(err)
			break
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			fail(ctx.Err())
			// fallthrough out of the loop below
		}
		if ctx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(ch *chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			// Each chunk advances st via onBytes; bridge fetchChunk's
			// onBytes to both the caller callback and State.MarkProgress.
			onWrite := func(n int64) {
				if onBytes != nil {
					onBytes(n)
				}
				st.MarkProgress(ch.index, n)
			}

			err := retry(ctx, func() error {
				return fetchChunk(ctx, c, url, hdr, ch, dst, onWrite)
			})
			if err != nil {
				fail(classifyChunkError(err))
			}
		}(ch)
	}

	wg.Wait()

	// Tear down the flush goroutine and persist a final snapshot.
	cancel()
	flushWG.Wait()
	if saveErr := st.Save(statePath); saveErr != nil && firstErr == nil {
		firstErr = saveErr
	}

	return firstErr
}

// classifyChunkError adds the ErrChunkFailed sentinel to a chunk fetch failure,
// while letting context errors propagate unwrapped so callers see ctx.Err().
func classifyChunkError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrChunkFailed, err)
}

// copyToStream fetches the whole resource (no Range header) and copies the body to
// w via plain Write in a fixed copyBufferSize loop, returning the bytes written. It
// uses NO Truncate, NO WriteAt, and NO Seek, so w may be any io.Writer including a
// non-seekable pipe (stdout). It honors ctx between reads and surfaces a short write
// as io.ErrShortWrite. It performs NO internal retry: an error after partial
// emission is returned as-is, and the caller (runStdoutStream) invokes it at most
// once so emitted bytes are never duplicated. runSingle (the file path) is left
// untouched.
func copyToStream(ctx context.Context, c *http.Client, url string, hdr http.Header, w io.Writer, onBytes func(int64)) (int64, error) {
	// No range header: fetch the whole resource.
	req, err := newRequest(ctx, url, hdr, -1, -1)
	if err != nil {
		return 0, fmt.Errorf("download: build request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: fetch: %w", err)
	}
	defer resp.Body.Close()

	// No Range header was sent, so only 200 is valid; a 206 here is anomalous and
	// would deliver only a partial body that we would otherwise stream as complete.
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download: unexpected status %d: %w", resp.StatusCode, &httpStatusError{code: resp.StatusCode})
	}

	var total int64
	buf := make([]byte, copyBufferSize)
	for {
		if cerr := ctx.Err(); cerr != nil {
			return total, cerr
		}
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			nw, werr := w.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
				if onBytes != nil {
					onBytes(int64(nw))
				}
			}
			if werr != nil {
				return total, fmt.Errorf("download: write: %w", werr)
			}
			if nw < nr {
				return total, fmt.Errorf("download: short write: %w", io.ErrShortWrite)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return total, fmt.Errorf("download: read: %w", rerr)
		}
	}

	return total, nil
}

// runSingle streams the whole resource into dst sequentially (truncates dst to 0
// first; no ranges, no resume). Returns the number of bytes copied.
func runSingle(ctx context.Context, c *http.Client, url string, hdr http.Header, dst *os.File, onBytes func(int64)) (int64, error) {
	// No range header: fetch the whole resource.
	req, err := newRequest(ctx, url, hdr, -1, -1)
	if err != nil {
		return 0, fmt.Errorf("download: build request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: fetch: %w", err)
	}
	defer resp.Body.Close()

	// This path never sends a Range header, so only 200 is valid. A 206 here is
	// anomalous (a non-conformant server/intermediary) and would deliver only a
	// partial body that we would otherwise write as the complete file.
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download: unexpected status %d: %w", resp.StatusCode, &httpStatusError{code: resp.StatusCode})
	}

	if err := dst.Truncate(0); err != nil {
		return 0, fmt.Errorf("download: truncate dst: %w", err)
	}

	var total int64
	var off int64
	buf := make([]byte, copyBufferSize)
	for {
		if cerr := ctx.Err(); cerr != nil {
			return total, cerr
		}
		nr, rerr := resp.Body.Read(buf)
		if nr > 0 {
			nw, werr := dst.WriteAt(buf[:nr], off)
			if nw > 0 {
				off += int64(nw)
				total += int64(nw)
				if onBytes != nil {
					onBytes(int64(nw))
				}
			}
			if werr != nil {
				return total, fmt.Errorf("download: write: %w", werr)
			}
			if nw < nr {
				return total, fmt.Errorf("download: short write: %w", io.ErrShortWrite)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return total, fmt.Errorf("download: read: %w", rerr)
		}
	}

	return total, nil
}
