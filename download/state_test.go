package download

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatePath(t *testing.T) {
	assert.Equal(t, "out.bin.dr.json", statePath("out.bin"))
	assert.Equal(t, "/tmp/x.dr.json", statePath("/tmp/x"))

	// The staging path and the sidecar that travels with it.
	assert.Equal(t, "out.bin.part", partPath("out.bin"))
	assert.Equal(t, "out.bin.part.dr.json", statePath(partPath("out.bin")))
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.bin.dr.json")

	info := remoteInfo{
		size:         300,
		acceptRanges: true,
		etag:         `"abc123"`,
		lastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
	}
	chunks := []chunk{
		{index: 0, start: 0, end: 99, done: 10},
		{index: 1, start: 100, end: 199, done: 0},
		{index: 2, start: 200, end: 299, done: 100},
	}
	orig := newState("http://example.com/file", info, 3, chunks)

	require.NoError(t, orig.Save(path))

	loaded, err := LoadState(path)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, orig.URL, loaded.URL)
	assert.Equal(t, orig.Size, loaded.Size)
	assert.Equal(t, orig.ETag, loaded.ETag)
	assert.Equal(t, orig.LastModified, loaded.LastModified)
	assert.Equal(t, orig.Concurrency, loaded.Concurrency)
	require.Len(t, loaded.Chunks, 3)
	for i := range orig.Chunks {
		assert.Equal(t, orig.Chunks[i], loaded.Chunks[i])
	}
}

func TestSaveAtomicNoLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.dr.json")
	st := newState("u", remoteInfo{size: 10}, 1, []chunk{{index: 0, start: 0, end: 9}})

	require.NoError(t, st.Save(path))

	// No leftover .tmp file.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "found leftover tmp file %q", e.Name())
	}
	assert.Len(t, entries, 1)

	// On-disk JSON is valid and parseable.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var generic map[string]any
	require.NoError(t, json.Unmarshal(data, &generic))
	assert.Equal(t, "u", generic["url"])
}

func TestMatches(t *testing.T) {
	tests := []struct {
		name               string
		size               int64
		etag, lastModified string
		info               remoteInfo
		want               bool
	}{
		{
			name: "same size and etag",
			size: 100, etag: `"x"`,
			info: remoteInfo{size: 100, etag: `"x"`},
			want: true,
		},
		{
			name: "changed etag",
			size: 100, etag: `"x"`,
			info: remoteInfo{size: 100, etag: `"y"`},
			want: false,
		},
		{
			name: "changed size",
			size: 100, etag: `"x"`,
			info: remoteInfo{size: 200, etag: `"x"`},
			want: false,
		},
		{
			name: "etag absent on both uses last-modified match",
			size: 100, lastModified: "Mon",
			info: remoteInfo{size: 100, lastModified: "Mon"},
			want: true,
		},
		{
			name: "etag absent on both uses last-modified mismatch",
			size: 100, lastModified: "Mon",
			info: remoteInfo{size: 100, lastModified: "Tue"},
			want: false,
		},
		{
			name: "etag present only on remote falls back to last-modified",
			size: 100, lastModified: "Mon",
			info: remoteInfo{size: 100, etag: `"x"`, lastModified: "Mon"},
			want: true,
		},
		{
			name: "no validator present",
			size: 100,
			info: remoteInfo{size: 100},
			want: false,
		},
		{
			name: "etag matches even when last-modified differs",
			size: 100, etag: `"x"`, lastModified: "Mon",
			info: remoteInfo{size: 100, etag: `"x"`, lastModified: "Tue"},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := State{Size: tt.size, ETag: tt.etag, LastModified: tt.lastModified}
			assert.Equal(t, tt.want, st.Matches(tt.info))
		})
	}
}

func TestToChunksAndCompletedBytes(t *testing.T) {
	st := newState("u", remoteInfo{size: 300}, 3, []chunk{
		{index: 0, start: 0, end: 99, done: 10},
		{index: 1, start: 100, end: 199, done: 50},
		{index: 2, start: 200, end: 299, done: 100},
	})

	got := st.toChunks()
	require.Len(t, got, 3)
	assert.Equal(t, chunk{index: 0, start: 0, end: 99, done: 10}, got[0])
	assert.Equal(t, chunk{index: 1, start: 100, end: 199, done: 50}, got[1])
	assert.Equal(t, chunk{index: 2, start: 200, end: 299, done: 100}, got[2])

	assert.Equal(t, int64(160), st.completedBytes())
}

func TestMarkProgressConcurrent(t *testing.T) {
	const nChunks = 8
	const perGoroutine = 1000

	chunks := make([]chunk, nChunks)
	for i := range chunks {
		chunks[i] = chunk{index: i, start: int64(i) * 100, end: int64(i)*100 + 99}
	}
	st := newState("u", remoteInfo{size: nChunks * 100}, nChunks, chunks)

	var wg sync.WaitGroup
	for i := 0; i < nChunks; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				st.MarkProgress(idx, 1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int64(nChunks*perGoroutine), st.completedBytes())
	for _, cs := range st.Chunks {
		assert.Equal(t, int64(perGoroutine), cs.Done)
	}
}

func TestMarkProgressUnknownIndexNoop(t *testing.T) {
	st := newState("u", remoteInfo{size: 100}, 1, []chunk{{index: 0, start: 0, end: 99}})
	st.MarkProgress(99, 50)
	assert.Equal(t, int64(0), st.completedBytes())
}

func TestLoadStateMissing(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadState(filepath.Join(dir, "does-not-exist.dr.json"))
	require.NoError(t, err)
	assert.Nil(t, st)
}

func TestLoadStateCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.dr.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o644))

	st, err := LoadState(path)
	require.NoError(t, err)
	assert.Nil(t, st)
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rm.dr.json")
	st := newState("u", remoteInfo{size: 10}, 1, []chunk{{index: 0, start: 0, end: 9}})
	require.NoError(t, st.Save(path))

	require.FileExists(t, path)
	require.NoError(t, st.Remove(path))
	assert.NoFileExists(t, path)

	// Remove on missing file is not an error.
	require.NoError(t, st.Remove(path))
}
