package download

import (
	"bytes"
	"net/url"
	"testing"
	"time"
)

// TestProgressErrorHandlingIntegration tests that progress error handling works end-to-end
func TestProgressErrorHandlingIntegration(t *testing.T) {
	// Create a temporary directory for test files
	tempDir := t.TempDir()

	// Create a mock downloader that will work but with a failing progress display
	destURL, _ := url.Parse("file://" + tempDir)
	downloader := &Downloader{
		DestinationDIR: destURL,
		RangeSupport: RangeSupport{
			ContentLength:         1024,
			SupportsRangeRequests: false, // Non-segmented download
		},
	}

	// Create download manager
	dm := NewDownloadManager(downloader, DefaultRetryPolicy())

	// Initialize progress tracking with a failing writer
	dm.ProgressTracker = NewProgressTracker(1024)
	failingWriter := &failingWriter{}
	progressDisplay := NewProgressDisplay(failingWriter)

	// Start progress tracking
	dm.ProgressTracker.Start()
	defer dm.ProgressTracker.Stop()

	// Start progress display in a goroutine (simulating real usage)
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for i := 0; i < 10; i++ {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				// This should not crash even though the writer fails
				percentage, speed, eta, downloaded, total, err := dm.ProgressTracker.GetProgressSafely()
				if err != nil {
					continue // Error handling working
				}
				progressDisplay.Show(percentage, speed, downloaded, total, eta)
			}
		}
	}()

	// Simulate some progress events
	for i := 0; i < 5; i++ {
		event := ProgressEvent{
			SegmentID:  0,
			BytesRead:  int64(i * 100),
			TotalBytes: 1024,
			Speed:      1024,
			Timestamp:  time.Now(),
		}
		dm.ProgressTracker.ReportProgress(event)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for progress display to finish
	time.Sleep(200 * time.Millisecond)
	close(progressDone)
	time.Sleep(50 * time.Millisecond) // Give goroutine time to exit

	// Verify that progress tracking is still healthy despite display failures
	if !dm.ProgressTracker.IsHealthy() {
		t.Error("Progress tracker should remain healthy despite display failures")
	}

	// Verify that display switched to fallback mode
	if !progressDisplay.IsFallbackMode() {
		t.Error("Progress display should have switched to fallback mode")
	}

	// Verify that display errors were recorded
	displayErrors := progressDisplay.GetDisplayErrors()
	if len(displayErrors) == 0 {
		t.Error("Expected display errors to be recorded")
	}
}

// TestProgressWithUnknownFileSizeIntegration tests progress handling with unknown file sizes
func TestProgressWithUnknownFileSizeIntegration(t *testing.T) {
	// Create progress tracker with unknown size
	tracker := NewProgressTracker(-1)
	tracker.Start()
	defer tracker.Stop()

	var buffer bytes.Buffer
	display := NewProgressDisplay(&buffer)

	// Simulate progress events with unknown total size initially
	events := []ProgressEvent{
		{SegmentID: 0, BytesRead: 256, TotalBytes: 0, Speed: 1024, Timestamp: time.Now()},
		{SegmentID: 0, BytesRead: 512, TotalBytes: 1024, Speed: 1024, Timestamp: time.Now()}, // Size becomes known
		{SegmentID: 0, BytesRead: 768, TotalBytes: 1024, Speed: 512, Timestamp: time.Now()},
	}

	for i, event := range events {
		tracker.ReportProgress(event)
		time.Sleep(50 * time.Millisecond)

		percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
		if err != nil {
			t.Errorf("Unexpected error at step %d: %v", i, err)
		}

		display.Show(percentage, speed, downloaded, total, eta)

		// After the second event, total size should be known
		if i >= 1 && total <= 0 {
			t.Errorf("Expected total size to be known after step %d, got: %d", i, total)
		}
	}

	// Verify output was generated
	output := buffer.String()
	if len(output) == 0 {
		t.Error("Expected progress output to be generated")
	}
}

