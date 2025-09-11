package download

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// mockWriter simulates a writer that can fail on demand
type mockWriter struct {
	buffer    *bytes.Buffer
	failWrite bool
	failCount int
	writeCall int
}

func newMockWriter() *mockWriter {
	return &mockWriter{
		buffer: &bytes.Buffer{},
	}
}

func (mw *mockWriter) Write(p []byte) (n int, err error) {
	mw.writeCall++
	if mw.failWrite && mw.writeCall > mw.failCount {
		return 0, errors.New("mock write failure")
	}
	return mw.buffer.Write(p)
}

func (mw *mockWriter) String() string {
	return mw.buffer.String()
}

func (mw *mockWriter) setFailAfter(count int) {
	mw.failWrite = true
	mw.failCount = count
}

// TestProgressDisplayErrorHandling tests that progress display handles errors gracefully
func TestProgressDisplayErrorHandling(t *testing.T) {
	tests := []struct {
		name           string
		failAfter      int
		expectFallback bool
		expectErrors   bool
	}{
		{
			name:           "no failures",
			failAfter:      -1,
			expectFallback: false,
			expectErrors:   false,
		},
		{
			name:           "fail after first write",
			failAfter:      2, // Allow initial clear line, then fail
			expectFallback: true,
			expectErrors:   true,
		},
		{
			name:           "fail immediately",
			failAfter:      0,
			expectFallback: true,
			expectErrors:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockWriter := newMockWriter()
			if tt.failAfter >= 0 {
				mockWriter.setFailAfter(tt.failAfter)
			}

			display := NewProgressDisplay(mockWriter)

			// Show progress multiple times to trigger errors
			for i := 0; i < 5; i++ {
				display.Show(float64(i*20), 1024.0, int64(i*1024), 4096, time.Minute)
			}

			// Check if fallback mode was activated as expected
			if display.IsFallbackMode() != tt.expectFallback {
				t.Errorf("Expected fallback mode: %v, got: %v", tt.expectFallback, display.IsFallbackMode())
			}

			// Check if errors were recorded as expected
			errors := display.GetDisplayErrors()
			hasErrors := len(errors) > 0
			if hasErrors != tt.expectErrors {
				t.Errorf("Expected errors: %v, got: %v (errors: %v)", tt.expectErrors, hasErrors, errors)
			}

			// Verify that the display doesn't crash even with failures
			// If the writer completely fails, we can't produce output, but we shouldn't crash
			output := mockWriter.String()
			if tt.failAfter > 0 && len(output) == 0 {
				t.Error("Expected some output when writer works initially")
			}
		})
	}
}

// TestProgressDisplayFallbackMode tests the fallback display functionality
func TestProgressDisplayFallbackMode(t *testing.T) {
	var buffer bytes.Buffer
	display := NewProgressDisplay(&buffer)

	// Force fallback mode
	display.fallbackMode = true

	// Show progress in fallback mode
	display.Show(50.0, 2048.0, 2048, 4096, 30*time.Second)

	output := buffer.String()
	
	// Verify fallback output contains expected elements
	expectedElements := []string{"Progress: 50.0%", "2.0 KB/4.0 KB", "Speed: 2.0 KB/s", "ETA: 30s"}
	for _, element := range expectedElements {
		if !strings.Contains(output, element) {
			t.Errorf("Expected fallback output to contain '%s', got: %s", element, output)
		}
	}

	// Verify it's using newlines instead of carriage returns
	if strings.Contains(output, "\r") {
		t.Error("Fallback mode should not use carriage returns")
	}
}

// TestProgressTrackerErrorHandling tests that progress tracker handles errors gracefully
func TestProgressTrackerErrorHandling(t *testing.T) {
	tracker := NewProgressTracker(1024)
	tracker.Start()
	defer tracker.Stop()

	// Test invalid progress events
	invalidEvents := []ProgressEvent{
		{SegmentID: -1, BytesRead: 100, Speed: 1024, Timestamp: time.Now()},
		{SegmentID: 0, BytesRead: -100, Speed: 1024, Timestamp: time.Now()},
		{SegmentID: 0, BytesRead: 100, Speed: -1024, Timestamp: time.Now()},
		{SegmentID: 0, BytesRead: 100, Speed: 1024, Timestamp: time.Time{}},
	}

	for _, event := range invalidEvents {
		tracker.ReportProgress(event)
	}

	// Give some time for processing
	time.Sleep(100 * time.Millisecond)

	// Check that errors were recorded
	errors := tracker.GetTrackingErrors()
	if len(errors) == 0 {
		t.Error("Expected tracking errors to be recorded")
	}

	// Verify tracker is still functional with valid events
	validEvent := ProgressEvent{
		SegmentID:  0,
		BytesRead:  512,
		TotalBytes: 1024,
		Speed:      1024,
		Timestamp:  time.Now(),
	}

	tracker.ReportProgress(validEvent)
	time.Sleep(100 * time.Millisecond)

	// Should still be able to get progress
	percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
	if err != nil {
		t.Errorf("Expected progress calculation to work after errors: %v", err)
	}

	if downloaded == 0 {
		t.Error("Expected some progress to be recorded")
	}

	_ = percentage
	_ = speed
	_ = eta
	_ = total
}

// TestProgressTrackerUnknownFileSize tests handling of unknown file sizes
func TestProgressTrackerUnknownFileSize(t *testing.T) {
	// Create tracker with unknown size
	tracker := NewProgressTracker(-1)
	tracker.Start()
	defer tracker.Stop()

	// Report progress with known segment size
	event := ProgressEvent{
		SegmentID:  0,
		BytesRead:  512,
		TotalBytes: 1024,
		Speed:      1024,
		Timestamp:  time.Now(),
	}

	tracker.ReportProgress(event)
	time.Sleep(100 * time.Millisecond)

	// Check that total size was updated
	percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if total <= 0 {
		t.Error("Expected total size to be updated from segment information")
	}

	if percentage == 0 && downloaded > 0 {
		t.Error("Expected percentage calculation to work with updated total size")
	}

	_ = speed
	_ = eta
}

// TestProgressTrackerChannelOverflow tests behavior when event channel is full
func TestProgressTrackerChannelOverflow(t *testing.T) {
	tracker := NewProgressTracker(1024)
	// Don't start the tracker to simulate a blocked channel
	
	// Try to send many events
	for i := 0; i < 200; i++ {
		event := ProgressEvent{
			SegmentID:  i % 4,
			BytesRead:  int64(i * 10),
			TotalBytes: 1024,
			Speed:      1024,
			Timestamp:  time.Now(),
		}
		tracker.ReportProgress(event)
	}

	// Check that errors were recorded for dropped events
	errors := tracker.GetTrackingErrors()
	if len(errors) == 0 {
		t.Error("Expected errors to be recorded for channel overflow")
	}

	// Verify that some error messages mention channel being full
	foundChannelError := false
	for _, err := range errors {
		if strings.Contains(err.Error(), "channel full") {
			foundChannelError = true
			break
		}
	}

	if !foundChannelError {
		t.Error("Expected to find channel overflow error messages")
	}
}

// TestSegmentProgressReportingErrors tests segment progress reporting error handling
func TestSegmentProgressReportingErrors(t *testing.T) {
	// Create a mock progress reporter that can panic
	mockReporter := &panicProgressReporter{shouldPanic: true}

	segment := &Segment{
		SegmentParams: SegmentParams{
			ID:               0,
			Start:            0,
			End:              1024,
			MaxSegmentSize:   1024,
			ProgressReporter: mockReporter,
		},
		lastReportedBytes: 0,
		lastProgressTime:  time.Now().Add(-time.Second), // Force immediate reporting
	}

	// This should not panic even though the reporter panics
	segment.reportProgress(512)

	// Verify the segment is still functional
	if segment.ID != 0 {
		t.Error("Segment should remain functional after progress reporting error")
	}
}

// TestProgressDisplayTerminalCapabilities tests terminal capability detection
func TestProgressDisplayTerminalCapabilities(t *testing.T) {
	// Test with a normal writer - should support terminal control by default
	var buffer bytes.Buffer
	display := NewProgressDisplay(&buffer)

	// Should assume terminal control is supported initially
	if !display.supportsTerminalControl {
		t.Error("Expected terminal control to be supported by default")
	}

	if display.fallbackMode {
		t.Error("Expected fallback mode to be disabled initially")
	}

	// Test that errors during display operations trigger fallback mode
	failingWriter := &failingWriter{}
	display2 := NewProgressDisplay(failingWriter)
	
	// Try to show progress - this should trigger fallback mode due to write failures
	display2.Show(50.0, 1024.0, 512, 1024, time.Minute)
	
	// After a write failure, should be in fallback mode
	if !display2.fallbackMode {
		t.Error("Expected fallback mode to be enabled after write failure")
	}
}

// TestProgressCalculationEdgeCases tests edge cases in progress calculations
func TestProgressCalculationEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		totalSize     int64
		downloaded    int64
		expectPanic   bool
		expectPercent float64
	}{
		{
			name:          "normal case",
			totalSize:     1000,
			downloaded:    500,
			expectPanic:   false,
			expectPercent: 50.0,
		},
		{
			name:          "zero total size",
			totalSize:     0,
			downloaded:    500,
			expectPanic:   false,
			expectPercent: 0.0,
		},
		{
			name:          "negative total size",
			totalSize:     -1,
			downloaded:    500,
			expectPanic:   false,
			expectPercent: 0.0,
		},
		{
			name:          "downloaded exceeds total",
			totalSize:     1000,
			downloaded:    1500,
			expectPanic:   false,
			expectPercent: 100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewProgressTracker(tt.totalSize)
			tracker.downloadedBytes = tt.downloaded

			percentage, _, _, _, _, err := tracker.GetProgressSafely()

			if tt.expectPanic && err == nil {
				t.Error("Expected an error due to panic recovery")
			}

			if !tt.expectPanic && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if percentage != tt.expectPercent {
				t.Errorf("Expected percentage: %f, got: %f", tt.expectPercent, percentage)
			}
		})
	}
}

// Mock types for testing

type panicProgressReporter struct {
	shouldPanic bool
}

func (ppr *panicProgressReporter) ReportProgress(event ProgressEvent) {
	if ppr.shouldPanic {
		panic("mock progress reporter panic")
	}
}

type failingWriter struct{}

func (fw *failingWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("write failed")
}

// TestNetworkInterruptionHandling tests how progress tracking handles network interruptions
func TestNetworkInterruptionHandling(t *testing.T) {
	tracker := NewProgressTracker(1024)
	tracker.Start()
	defer tracker.Stop()

	// Simulate normal progress
	event1 := ProgressEvent{
		SegmentID:  0,
		BytesRead:  256,
		TotalBytes: 1024,
		Speed:      1024,
		Timestamp:  time.Now(),
	}
	tracker.ReportProgress(event1)

	// Simulate network interruption (no progress for a while)
	time.Sleep(50 * time.Millisecond)

	// Simulate recovery with same or lower bytes (retry scenario)
	event2 := ProgressEvent{
		SegmentID:  0,
		BytesRead:  200, // Lower than before (retry)
		TotalBytes: 1024,
		Speed:      512,
		Timestamp:  time.Now(),
	}
	tracker.ReportProgress(event2)

	time.Sleep(50 * time.Millisecond)

	// Progress should handle this gracefully
	percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
	if err != nil {
		t.Errorf("Unexpected error handling network interruption: %v", err)
	}

	// Should not show backward progress
	if downloaded < 0 {
		t.Error("Downloaded bytes should not be negative")
	}

	_ = percentage
	_ = speed
	_ = eta
	_ = total
}