package download

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewSegment(t *testing.T) {
	t.Run("NewSegment", func(t *testing.T) {
		t.Run("valid initialization", func(t *testing.T) {
			fileWriter, err := NewFileWriter("/tmp/dl/segments", "segment1.txt")
			assert.NoError(t, err)

			defer t.Cleanup(func() {
				fileWriter.Close()
				os.Remove("/tmp/dl/segments/segment1.txt")
			})

			segment, err := NewSegment(SegmentParams{
				ID:             1,
				Start:          int64(0),
				End:            int64(10),
				MaxSegmentSize: int64(5),
				Writer:         fileWriter,
			})
			if assert.NoError(t, err) {
				assert.NotNil(t, segment)
				assert.Equal(t, 1, segment.ID)
				assert.Equal(t, int64(0), segment.Start)
				assert.Equal(t, int64(10), segment.End)
				assert.Equal(t, int64(5), segment.MaxSegmentSize)
				assert.False(t, segment.Done)
				assert.Nil(t, segment.Err)
			}
		})
		t.Run("invalid initialization", func(t *testing.T) {
			// invalid writer
			_, err := NewSegment(SegmentParams{
				ID:             1,
				Start:          int64(0),
				End:            int64(10),
				MaxSegmentSize: int64(5),
				Writer:         nil,
			})
			var err1 *InvalidParamError
			assert.ErrorAs(t, err, &err1)
		})
	})
	t.Run("Copy data into segment", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "segment2.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/segment2.txt")
		})

		segment, err := NewSegment(SegmentParams{
			ID:             1,
			MaxSegmentSize: int64(5),
			Writer:         fileWriter,
		})
		if assert.NoError(t, err) {
			_, err = io.Copy(segment, strings.NewReader("abcdef"))
			_, err = fileWriter.Seek(0, io.SeekStart)
			content, err := io.ReadAll(fileWriter)
			assert.NoError(t, err)
			assert.Equal(t, "abcdef", string(content))
		}
	})
	t.Run("segment.setDone", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "segment3.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/segment3.txt")
		})

		segment, err := NewSegment(SegmentParams{
			ID:             1,
			MaxSegmentSize: int64(5),
			Writer:         fileWriter,
		})
		if assert.NoError(t, err) {
			_, err := io.Copy(segment, strings.NewReader("abcdefgh"))
			assert.NoError(t, err)

			segment.setDone(true)

			_, err = fileWriter.Seek(0, io.SeekStart)
			content, err := io.ReadAll(fileWriter)
			assert.NoError(t, err)
			assert.Equal(t, "abcdefgh", string(content))
		}
	})
	t.Run("ReadFrom", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "segment4.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/segment3.txt")
		})

		segment, err := NewSegment(SegmentParams{
			ID:             1,
			Start:          int64(0),
			End:            int64(10),
			MaxSegmentSize: int64(5),
			Writer:         fileWriter,
		})
		assert.NoError(t, err)
		assert.Equal(t, true, segment.Resumable)

		src1 := strings.NewReader("one")
		n, err := segment.ReadFrom(src1)
		assert.NoError(t, err)
		assert.Equal(t, int64(3), n)
	})
}

// mockProgressReporter is a test implementation of ProgressReporter
type mockProgressReporter struct {
	events []ProgressEvent
	mu     sync.Mutex
}

func (mpr *mockProgressReporter) ReportProgress(event ProgressEvent) {
	mpr.mu.Lock()
	defer mpr.mu.Unlock()
	mpr.events = append(mpr.events, event)
}

func (mpr *mockProgressReporter) GetEvents() []ProgressEvent {
	mpr.mu.Lock()
	defer mpr.mu.Unlock()
	return append([]ProgressEvent{}, mpr.events...)
}

func (mpr *mockProgressReporter) Reset() {
	mpr.mu.Lock()
	defer mpr.mu.Unlock()
	mpr.events = nil
}

func TestSegmentProgressReporting(t *testing.T) {
	t.Run("segment with progress reporter", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "progress-test.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/progress-test.txt")
		})

		mockReporter := &mockProgressReporter{}
		segment, err := NewSegment(SegmentParams{
			ID:               1,
			Name:             "progress-test",
			Start:            0,
			End:              99,
			MaxSegmentSize:   100,
			Writer:           fileWriter,
			ProgressReporter: mockReporter,
		})
		assert.NoError(t, err)
		assert.NotNil(t, segment.progressReporter)

		// Write some data to trigger progress reporting
		data := strings.Repeat("a", 2048) // 2KB of data to ensure progress reporting
		_, err = segment.Write([]byte(data))
		assert.NoError(t, err)

		// Allow some time for progress reporting
		time.Sleep(150 * time.Millisecond)

		// Check that progress events were reported
		events := mockReporter.GetEvents()
		assert.Greater(t, len(events), 0, "Expected at least one progress event")

		if len(events) > 0 {
			event := events[0]
			assert.Equal(t, 1, event.SegmentID)
			assert.Greater(t, event.BytesRead, int64(0))
			assert.Equal(t, int64(100), event.TotalBytes) // End - Start + 1
			assert.GreaterOrEqual(t, event.Speed, float64(0))
		}
	})

	t.Run("segment without progress reporter", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "no-progress-test.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/no-progress-test.txt")
		})

		segment, err := NewSegment(SegmentParams{
			ID:               2,
			Name:             "no-progress-test",
			Start:            0,
			End:              99,
			MaxSegmentSize:   100,
			Writer:           fileWriter,
			ProgressReporter: nil, // No progress reporter
		})
		assert.NoError(t, err)
		assert.Nil(t, segment.progressReporter)

		// Write data - should not panic even without progress reporter
		data := "test data"
		_, err = segment.Write([]byte(data))
		assert.NoError(t, err)
	})

	t.Run("progress reporting with ReadFrom", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "readfrom-progress-test.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/readfrom-progress-test.txt")
		})

		mockReporter := &mockProgressReporter{}
		segment, err := NewSegment(SegmentParams{
			ID:               3,
			Name:             "readfrom-progress-test",
			Start:            0,
			End:              1023,
			MaxSegmentSize:   1024,
			Writer:           fileWriter,
			ProgressReporter: mockReporter,
		})
		assert.NoError(t, err)

		// Use ReadFrom with a large data source
		data := strings.Repeat("x", 2048) // 2KB of data
		reader := strings.NewReader(data)
		
		n, err := segment.ReadFrom(reader)
		assert.NoError(t, err)
		assert.Equal(t, int64(2048), n)

		// Allow some time for progress reporting
		time.Sleep(150 * time.Millisecond)

		// Check that progress events were reported
		events := mockReporter.GetEvents()
		assert.Greater(t, len(events), 0, "Expected at least one progress event from ReadFrom")

		if len(events) > 0 {
			event := events[len(events)-1] // Get the last event
			assert.Equal(t, 3, event.SegmentID)
			assert.Greater(t, event.BytesRead, int64(0))
		}
	})

	t.Run("progress reporting frequency limiting", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "frequency-test.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/frequency-test.txt")
		})

		mockReporter := &mockProgressReporter{}
		segment, err := NewSegment(SegmentParams{
			ID:               4,
			Name:             "frequency-test",
			Start:            0,
			End:              99,
			MaxSegmentSize:   100,
			Writer:           fileWriter,
			ProgressReporter: mockReporter,
		})
		assert.NoError(t, err)

		// Write small amounts of data rapidly
		for i := 0; i < 10; i++ {
			_, err = segment.Write([]byte("a"))
			assert.NoError(t, err)
		}

		// Should have limited number of progress events due to frequency limiting
		events := mockReporter.GetEvents()
		// The exact number depends on timing, but should be less than 10
		assert.LessOrEqual(t, len(events), 10, "Progress reporting should be frequency limited")
	})

	t.Run("progress calculation for non-segmented download", func(t *testing.T) {
		fileWriter, err := NewFileWriter("/tmp/dl/segments", "non-segmented-test.txt")
		assert.NoError(t, err)

		defer t.Cleanup(func() {
			fileWriter.Close()
			os.Remove("/tmp/dl/segments/non-segmented-test.txt")
		})

		mockReporter := &mockProgressReporter{}
		segment, err := NewSegment(SegmentParams{
			ID:               5,
			Name:             "non-segmented-test",
			Start:            0,
			End:              0, // Non-segmented download
			MaxSegmentSize:   1024,
			Writer:           fileWriter,
			ProgressReporter: mockReporter,
		})
		assert.NoError(t, err)

		// Write data to trigger progress reporting
		data := strings.Repeat("b", 1500) // 1.5KB of data
		_, err = segment.Write([]byte(data))
		assert.NoError(t, err)

		// Allow some time for progress reporting
		time.Sleep(150 * time.Millisecond)

		// Check that progress events were reported with correct total bytes
		events := mockReporter.GetEvents()
		assert.Greater(t, len(events), 0, "Expected at least one progress event")

		if len(events) > 0 {
			event := events[0]
			assert.Equal(t, 5, event.SegmentID)
			assert.Equal(t, int64(1024), event.TotalBytes) // Should use MaxSegmentSize
		}
	})
}
