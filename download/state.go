package download

import "sync"

// State is the durable resume sidecar persisted as <output>.dr.json.
// The mu field is unexported so encoding/json ignores it; Save snapshots the
// marshalable fields under lock to avoid racing the marshaler with MarkProgress.
type State struct {
	URL          string       `json:"url"`
	Size         int64        `json:"size"`
	ETag         string       `json:"etag,omitempty"`
	LastModified string       `json:"last_modified,omitempty"`
	Concurrency  int          `json:"concurrency"`
	Chunks       []ChunkState `json:"chunks"`

	mu sync.Mutex // guards Chunks during concurrent MarkProgress/Save
}

// ChunkState records resume progress for one chunk. Start/End are immutable; Done advances.
type ChunkState struct {
	Index int   `json:"index"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  int64 `json:"done"`
}

// statePath returns the sidecar path for an output file: output + ".dr.json".
func statePath(output string) string {
	panic("not implemented")
}

// LoadState reads and unmarshals the sidecar. Returns (nil, nil) when the file
// does not exist OR is corrupt (treated as absent so a clean restart is possible).
func LoadState(path string) (*State, error) {
	panic("not implemented")
}

// newState builds a fresh State from a chunk plan and remote info.
func newState(url string, info remoteInfo, concurrency int, chunks []chunk) *State {
	panic("not implemented")
}

// Save atomically writes the state via a temp file + os.Rename. Concurrency-safe.
func (s *State) Save(path string) error {
	panic("not implemented")
}

// Matches reports whether the remote is unchanged versus the saved state:
// size must match; then ETag if both present, else Last-Modified if both present.
// When no validator is available it returns false (resume is unsafe).
func (s *State) Matches(info remoteInfo) bool {
	panic("not implemented")
}

// MarkProgress atomically records that chunk `index` advanced its Done cursor by n.
func (s *State) MarkProgress(index int, n int64) {
	panic("not implemented")
}

// toChunks reconstructs the in-memory chunk plan (including done cursors) for resuming.
func (s *State) toChunks() []chunk {
	panic("not implemented")
}

// completedBytes returns the sum of all chunk Done values (for seeding progress).
func (s *State) completedBytes() int64 {
	panic("not implemented")
}

// Remove deletes the sidecar (called on success). A missing file is not an error.
func (s *State) Remove(path string) error {
	panic("not implemented")
}
