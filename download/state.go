package download

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

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
	return output + ".dr.json"
}

// LoadState reads and unmarshals the sidecar. Returns (nil, nil) when the file
// does not exist OR is corrupt (treated as absent so a clean restart is possible).
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("download: read state: %w", err)
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt sidecar: treat as absent so a clean restart is possible.
		return nil, nil
	}
	return &s, nil
}

// newState builds a fresh State from a chunk plan and remote info.
func newState(url string, info remoteInfo, concurrency int, chunks []chunk) *State {
	cs := make([]ChunkState, len(chunks))
	for i, ch := range chunks {
		cs[i] = ChunkState{
			Index: ch.index,
			Start: ch.start,
			End:   ch.end,
			Done:  ch.done,
		}
	}
	return &State{
		URL:          url,
		Size:         info.size,
		ETag:         info.etag,
		LastModified: info.lastModified,
		Concurrency:  concurrency,
		Chunks:       cs,
	}
}

// Save atomically writes the state via a temp file + os.Rename. Concurrency-safe.
func (s *State) Save(path string) error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("download: marshal state: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("download: create temp state: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download: write temp state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download: sync temp state: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download: close temp state: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download: rename state: %w", err)
	}
	return nil
}

// Matches reports whether the remote is unchanged versus the saved state:
// size must match; then ETag if both present, else Last-Modified if both present.
// When no validator is available it returns false (resume is unsafe).
func (s *State) Matches(info remoteInfo) bool {
	if s.Size != info.size {
		return false
	}
	if s.ETag != "" && info.etag != "" {
		return s.ETag == info.etag
	}
	if s.LastModified != "" && info.lastModified != "" {
		return s.LastModified == info.lastModified
	}
	return false
}

// MarkProgress atomically records that chunk `index` advanced its Done cursor by n.
func (s *State) MarkProgress(index int, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Chunks {
		if s.Chunks[i].Index == index {
			s.Chunks[i].Done += n
			return
		}
	}
}

// toChunks reconstructs the in-memory chunk plan (including done cursors) for resuming.
func (s *State) toChunks() []chunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunks := make([]chunk, len(s.Chunks))
	for i, cs := range s.Chunks {
		chunks[i] = chunk{
			index: cs.Index,
			start: cs.Start,
			end:   cs.End,
			done:  cs.Done,
		}
	}
	return chunks
}

// completedBytes returns the sum of all chunk Done values (for seeding progress).
func (s *State) completedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, cs := range s.Chunks {
		total += cs.Done
	}
	return total
}

// Remove deletes the sidecar (called on success). A missing file is not an error.
func (s *State) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("download: remove state: %w", err)
	}
	return nil
}
