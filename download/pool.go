package download

import (
	"context"
	"net/http"
	"os"
)

// retryFunc runs op with retry/backoff; see retry.go.
type retryFunc func(ctx context.Context, op func() error) error

// runSegmented downloads all not-yet-complete chunks with bounded concurrency,
// flushing st via st.Save(statePath) periodically and once on exit. The first
// fatal error cancels remaining workers and is returned (wrapped with ErrChunkFailed
// when it originates from a chunk fetch). WriteAt offsets are disjoint, so file
// writes need no extra locking; State.mu guards only the State struct.
func runSegmented(ctx context.Context, c *http.Client, url string, hdr http.Header, dst *os.File, st *State, statePath string, chunks []chunk, concurrency int, onBytes func(int64), retry retryFunc) error {
	panic("not implemented")
}

// runSingle streams the whole resource into dst sequentially (truncates dst to 0
// first; no ranges, no resume). Returns the number of bytes copied.
func runSingle(ctx context.Context, c *http.Client, url string, hdr http.Header, dst *os.File, onBytes func(int64)) (int64, error) {
	panic("not implemented")
}
