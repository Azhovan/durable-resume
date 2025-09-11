package download

import (
	"strings"
	"testing"
	"time"
)

func TestNewProgressTracker(t *testing.T) {
	totalSize := int64(1000)
	tracker := NewProgressTracker(totalSize)

	if tracker.totalSize != totalSize {
		t.Errorf("Expected total size %d, got %d", totalSize, tracker.totalSize)
	}

	if tracker.downloadedBytes != 0 {
		t.Errorf("Expected downloaded bytes to be 0, got %d", tracker.downloadedBytes)
	}

	if tracker.segmentProgress == nil {
		t.Error("Expected segment progress map to be initialized")
	}

	if len(tracker.segmentProgress) != 0 {
		t.Errorf("Expected empty segment progress map, got %d entries", len(tracker.segmentProgress))
	}

	if tracker.eventChan == nil {
		t.Error("Expected event channel to be initialized")
	}

	if tracker.done == nil {
		t.Error("Expected done channel to be initialized")
	}
}

func TestProgressTracker_ReportProgress(t *testing.T) {
	tracker := NewProgressTracker(1000)
	tracker.Start()
	defer tracker.Stop()

	// Test reporting progress for a new segment
	event := ProgressEvent{
		SegmentID:  1,
		BytesRead:  100,
		TotalBytes: 500,
		Speed:      1000.0,
		Timestamp:  time.Now(),
	}

	tracker.ReportProgress(event)

	// Give some time for the event to be processed
	time.Sleep(10 * time.Millisecond)

	// Check that the progress was updated
	downloaded := tracker.GetDownloadedBytes()
	if downloaded != 100 {
		t.Errorf("Expected downloaded bytes to be 100, got %d", downloaded)
	}

	speed := tracker.GetSpeed()
	if speed != 1000.0 {
		t.Errorf("Expected speed to be 1000.0, got %f", speed)
	}
}

func TestProgressTracker_MultipleSegments(t *testing.T) {
	tracker := NewProgressTracker(1000)
	tracker.Start()
	defer tracker.Stop()

	// Report progress for multiple segments
	events := []ProgressEvent{
		{SegmentID: 1, BytesRead: 100, Speed: 500.0, Timestamp: time.Now()},
		{SegmentID: 2, BytesRead: 200, Speed: 750.0, Timestamp: time.Now()},
		{SegmentID: 3, BytesRead: 150, Speed: 600.0, Timestamp: time.Now()},
	}

	for _, event := range events {
		tracker.ReportProgress(event)
	}

	// Give some time for events to be processed
	time.Sleep(20 * time.Millisecond)

	// Check total downloaded bytes
	downloaded := tracker.GetDownloadedBytes()
	expectedDownloaded := int64(100 + 200 + 150)
	if downloaded != expectedDownloaded {
		t.Errorf("Expected downloaded bytes to be %d, got %d", expectedDownloaded, downloaded)
	}

	// Check total speed (sum of all segment speeds)
	speed := tracker.GetSpeed()
	expectedSpeed := 500.0 + 750.0 + 600.0
	if speed != expectedSpeed {
		t.Errorf("Expected speed to be %f, got %f", expectedSpeed, speed)
	}
}

func TestProgressTracker_ProgressUpdates(t *testing.T) {
	tracker := NewProgressTracker(1000)
	tracker.Start()
	defer tracker.Stop()

	// Initial progress for segment 1
	event1 := ProgressEvent{
		SegmentID: 1,
		BytesRead: 100,
		Speed:     500.0,
		Timestamp: time.Now(),
	}
	tracker.ReportProgress(event1)

	time.Sleep(10 * time.Millisecond)

	// Update progress for the same segment
	event2 := ProgressEvent{
		SegmentID: 1,
		BytesRead: 250, // Additional 150 bytes
		Speed:     600.0,
		Timestamp: time.Now(),
	}
	tracker.ReportProgress(event2)

	time.Sleep(10 * time.Millisecond)

	// Check that only the difference was added
	downloaded := tracker.GetDownloadedBytes()
	if downloaded != 250 {
		t.Errorf("Expected downloaded bytes to be 250, got %d", downloaded)
	}

	// Check that speed was updated
	speed := tracker.GetSpeed()
	if speed != 600.0 {
		t.Errorf("Expected speed to be 600.0, got %f", speed)
	}
}

func TestProgressTracker_GetPercentage(t *testing.T) {
	tests := []struct {
		name           string
		totalSize      int64
		downloadedBytes int64
		expectedPercent float64
	}{
		{"Zero progress", 1000, 0, 0.0},
		{"Half progress", 1000, 500, 50.0},
		{"Full progress", 1000, 1000, 100.0},
		{"Over progress", 1000, 1200, 100.0}, // Should cap at 100%
		{"Unknown size", 0, 500, 0.0},        // Unknown total size
		{"Unknown size negative", -1, 500, 0.0}, // Negative total size
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewProgressTracker(tt.totalSize)
			tracker.downloadedBytes = tt.downloadedBytes

			percentage := tracker.GetPercentage()
			if percentage != tt.expectedPercent {
				t.Errorf("Expected percentage %f, got %f", tt.expectedPercent, percentage)
			}
		})
	}
}

func TestProgressTracker_GetETA(t *testing.T) {
	tracker := NewProgressTracker(1000)
	tracker.Start()
	defer tracker.Stop()

	// Report progress to establish speed
	event := ProgressEvent{
		SegmentID: 1,
		BytesRead: 200,
		Speed:     100.0, // 100 bytes per second
		Timestamp: time.Now(),
	}
	tracker.ReportProgress(event)

	time.Sleep(10 * time.Millisecond)

	eta := tracker.GetETA()
	// Remaining: 800 bytes, Speed: 100 bytes/sec = 8 seconds
	expectedETA := 8 * time.Second
	if eta != expectedETA {
		t.Errorf("Expected ETA %v, got %v", expectedETA, eta)
	}
}

func TestProgressTracker_GetETAEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		totalSize   int64
		downloaded  int64
		speed       float64
		expectedETA time.Duration
	}{
		{"No speed", 1000, 500, 0.0, 0},
		{"Unknown total size", 0, 500, 100.0, 0},
		{"Completed download", 1000, 1000, 100.0, 0},
		{"Over-downloaded", 1000, 1200, 100.0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewProgressTracker(tt.totalSize)
			tracker.downloadedBytes = tt.downloaded
			
			// Set up a segment with the specified speed
			if tt.speed > 0 {
				tracker.segmentProgress[1] = &SegmentProgress{
					Speed: tt.speed,
				}
			}

			eta := tracker.GetETA()
			if eta != tt.expectedETA {
				t.Errorf("Expected ETA %v, got %v", tt.expectedETA, eta)
			}
		})
	}
}

func TestProgressTracker_GetProgress(t *testing.T) {
	tracker := NewProgressTracker(1000)
	tracker.Start()
	defer tracker.Stop()

	// Report progress from multiple segments
	events := []ProgressEvent{
		{SegmentID: 1, BytesRead: 200, Speed: 100.0, Timestamp: time.Now()},
		{SegmentID: 2, BytesRead: 300, Speed: 150.0, Timestamp: time.Now()},
	}

	for _, event := range events {
		tracker.ReportProgress(event)
	}

	time.Sleep(20 * time.Millisecond)

	percentage, speed, eta, downloaded, total := tracker.GetProgress()

	// Check percentage: 500/1000 = 50%
	if percentage != 50.0 {
		t.Errorf("Expected percentage 50.0, got %f", percentage)
	}

	// Check speed: 100 + 150 = 250
	if speed != 250.0 {
		t.Errorf("Expected speed 250.0, got %f", speed)
	}

	// Check downloaded bytes
	if downloaded != 500 {
		t.Errorf("Expected downloaded 500, got %d", downloaded)
	}

	// Check total size
	if total != 1000 {
		t.Errorf("Expected total 1000, got %d", total)
	}

	// Check ETA: remaining 500 bytes at 250 bytes/sec = 2 seconds
	expectedETA := 2 * time.Second
	if eta != expectedETA {
		t.Errorf("Expected ETA %v, got %v", expectedETA, eta)
	}
}

func TestProgressTracker_ThreadSafety(t *testing.T) {
	tracker := NewProgressTracker(10000)
	tracker.Start()
	defer tracker.Stop()

	// Test concurrent reads and writes to ensure no race conditions
	done := make(chan bool)
	numGoroutines := 5

	// Start goroutines that continuously read progress
	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				tracker.GetProgress()
				tracker.GetPercentage()
				tracker.GetSpeed()
				tracker.GetETA()
				tracker.GetDownloadedBytes()
				tracker.GetTotalSize()
				time.Sleep(time.Millisecond)
			}
			done <- true
		}()
	}

	// Start goroutines that report progress for different segments
	for i := 0; i < numGoroutines; i++ {
		go func(segmentID int) {
			for j := 0; j < 50; j++ {
				event := ProgressEvent{
					SegmentID: segmentID,
					BytesRead: int64(j * 20), // Progressive bytes for this segment
					Speed:     float64(100 + segmentID*10),
					Timestamp: time.Now(),
				}
				tracker.ReportProgress(event)
				time.Sleep(time.Millisecond)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}

	// Give time for all events to be processed
	time.Sleep(50 * time.Millisecond)

	// Verify that we have some progress (exact amount doesn't matter due to timing)
	downloaded := tracker.GetDownloadedBytes()
	if downloaded <= 0 {
		t.Error("Expected some downloaded bytes, got 0")
	}

	// Verify that we have some speed
	speed := tracker.GetSpeed()
	if speed <= 0 {
		t.Error("Expected some speed, got 0")
	}

	// The test passes if we reach here without race conditions or panics
}

func TestProgressTracker_StopAndCleanup(t *testing.T) {
	tracker := NewProgressTracker(1000)
	tracker.Start()

	// Report some progress
	event := ProgressEvent{
		SegmentID: 1,
		BytesRead: 100,
		Speed:     100.0,
		Timestamp: time.Now(),
	}
	tracker.ReportProgress(event)

	time.Sleep(10 * time.Millisecond)

	// Stop the tracker
	tracker.Stop()

	// Try to report progress after stopping (should not block or panic)
	event2 := ProgressEvent{
		SegmentID: 2,
		BytesRead: 200,
		Speed:     200.0,
		Timestamp: time.Now(),
	}
	tracker.ReportProgress(event2)

	// Should not block or panic
}

func TestNewProgressDisplay(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	if display.writer != &buf {
		t.Error("Expected writer to be set correctly")
	}

	if display.lastLine != "" {
		t.Errorf("Expected empty lastLine, got %q", display.lastLine)
	}

	if display.isActive {
		t.Error("Expected isActive to be false initially")
	}
}

func TestProgressDisplay_Show(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	// Test showing progress
	display.Show(50.0, 1024.0, 512, 1024, 30*time.Second)

	output := buf.String()
	if output == "" {
		t.Error("Expected output to be written")
	}

	// Check that display is now active
	if !display.IsActive() {
		t.Error("Expected display to be active after Show")
	}

	// Check that lastLine is set
	if display.lastLine == "" {
		t.Error("Expected lastLine to be set after Show")
	}
}

func TestProgressDisplay_Clear(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	// Show some progress first
	display.Show(50.0, 1024.0, 512, 1024, 30*time.Second)
	
	// Clear the display
	display.Clear()

	// Check that display is no longer active
	if display.IsActive() {
		t.Error("Expected display to be inactive after Clear")
	}

	// Check that lastLine is cleared
	if display.lastLine != "" {
		t.Errorf("Expected lastLine to be empty after Clear, got %q", display.lastLine)
	}
}

func TestProgressDisplay_buildProgressBar(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	tests := []struct {
		name       string
		percentage float64
		width      int
		expected   string
	}{
		{"Empty bar", 0.0, 10, "[        ]"},
		{"Half full", 50.0, 10, "[====>   ]"},
		{"Full bar", 100.0, 10, "[========]"},
		{"Over full", 150.0, 10, "[========]"},
		{"Small width", 25.0, 4, "[> ]"},
		{"Minimum width", 50.0, 2, "[]"},
		{"Very small", 50.0, 1, "[]"},
		{"Near full", 90.0, 10, "[=======>]"},
		{"Very small progress", 5.0, 10, "[>       ]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := display.buildProgressBar(tt.percentage, tt.width)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		expected string
	}{
		{"Zero bytes", 0, "0 B"},
		{"Negative bytes", -100, "0 B"},
		{"Bytes", 512, "512 B"},
		{"Kilobytes", 1536, "1.5 KB"},      // 1.5 * 1024
		{"Megabytes", 2097152, "2.0 MB"},   // 2 * 1024 * 1024
		{"Gigabytes", 3221225472, "3.0 GB"}, // 3 * 1024^3
		{"Large number", 1024, "1.0 KB"},
		{"Exact KB", 2048, "2.0 KB"},
		{"Exact MB", 1048576, "1.0 MB"},
		{"Fractional MB", 1572864, "1.5 MB"}, // 1.5 * 1024^2
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBytes(tt.bytes)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"Zero duration", 0, "0s"},
		{"Negative duration", -5 * time.Second, "0s"},
		{"Seconds only", 30 * time.Second, "30s"},
		{"Minutes only", 5 * time.Minute, "5m"},
		{"Minutes and seconds", 2*time.Minute + 30*time.Second, "2m30s"},
		{"Hours only", 2 * time.Hour, "2h"},
		{"Hours and minutes", 1*time.Hour + 30*time.Minute, "1h30m"},
		{"Hours without minutes", 3*time.Hour + 15*time.Second, "3h"},
		{"Complex duration", 2*time.Hour + 15*time.Minute + 45*time.Second, "2h15m"},
		{"Sub-second rounding", 1500 * time.Millisecond, "2s"}, // Should round to 2s
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestProgressDisplay_buildProgressLine(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	tests := []struct {
		name       string
		percentage float64
		speed      float64
		downloaded int64
		total      int64
		eta        time.Duration
		contains   []string // Strings that should be present in the output
	}{
		{
			name:       "Complete progress info",
			percentage: 75.5,
			speed:      1048576, // 1 MB/s
			downloaded: 3145728, // 3 MB
			total:      4194304, // 4 MB
			eta:        30 * time.Second,
			contains:   []string{"75.5%", "3.0 MB/4.0 MB", "1.0 MB/s", "ETA: 30s"},
		},
		{
			name:       "Unknown total size",
			percentage: 0.0,
			speed:      512000, // 500 KB/s
			downloaded: 1048576, // 1 MB
			total:      0,       // Unknown
			eta:        0,
			contains:   []string{"0.0%", "1.0 MB", "500.0 KB/s"},
		},
		{
			name:       "No speed or ETA",
			percentage: 25.0,
			speed:      0,
			downloaded: 256,
			total:      1024,
			eta:        0,
			contains:   []string{"25.0%", "256 B/1.0 KB"},
		},
		{
			name:       "High speed",
			percentage: 90.0,
			speed:      10737418240, // 10 GB/s
			downloaded: 9663676416,  // 9 GB
			total:      10737418240, // 10 GB
			eta:        1 * time.Second,
			contains:   []string{"90.0%", "9.0 GB/10.0 GB", "10.0 GB/s", "ETA: 1s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := display.buildProgressLine(tt.percentage, tt.speed, tt.downloaded, tt.total, tt.eta)
			
			for _, expected := range tt.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("Expected result to contain %q, got %q", expected, result)
				}
			}
			
			// Check that result contains a progress bar (starts with [)
			if !strings.Contains(result, "[") {
				t.Errorf("Expected result to contain progress bar, got %q", result)
			}
		})
	}
}

func TestProgressDisplay_ThreadSafety(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	// Test concurrent access to display methods
	done := make(chan bool)
	numGoroutines := 5

	// Start goroutines that continuously show progress
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < 20; j++ {
				percentage := float64(j * 5)
				speed := float64(1024 * (id + 1))
				downloaded := int64(j * 100)
				total := int64(2000)
				eta := time.Duration(j) * time.Second
				
				display.Show(percentage, speed, downloaded, total, eta)
				time.Sleep(time.Millisecond)
			}
			done <- true
		}(i)
	}

	// Start goroutines that check status and clear
	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				display.IsActive()
				if j%10 == 0 {
					display.Clear()
				}
				time.Sleep(time.Millisecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}

	// The test passes if we reach here without race conditions or panics
}

func TestProgressDisplay_MultipleShowCalls(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	// First show call
	display.Show(25.0, 1024.0, 256, 1024, 45*time.Second)
	firstOutput := buf.String()

	// Second show call should clear previous line and show new progress
	display.Show(50.0, 2048.0, 512, 1024, 30*time.Second)
	secondOutput := buf.String()

	// Should have more content after second call
	if len(secondOutput) <= len(firstOutput) {
		t.Error("Expected second output to be longer due to line clearing and new content")
	}

	// Should contain escape sequences for line clearing
	if !strings.Contains(secondOutput, "\r\033[K") {
		t.Error("Expected output to contain line clearing escape sequences")
	}
}

func TestProgressDisplay_EdgeCases(t *testing.T) {
	var buf strings.Builder
	display := NewProgressDisplay(&buf)

	// Test with extreme values
	tests := []struct {
		name       string
		percentage float64
		speed      float64
		downloaded int64
		total      int64
		eta        time.Duration
	}{
		{"Negative percentage", -10.0, 1024.0, 100, 1000, 10 * time.Second},
		{"Over 100 percentage", 150.0, 1024.0, 1500, 1000, 0},
		{"Negative speed", 50.0, -1024.0, 500, 1000, 10 * time.Second},
		{"Negative downloaded", 50.0, 1024.0, -500, 1000, 10 * time.Second},
		{"Negative total", 50.0, 1024.0, 500, -1000, 10 * time.Second},
		{"Negative ETA", 50.0, 1024.0, 500, 1000, -10 * time.Second},
		{"Very large numbers", 50.0, 1e12, 1e15, 2e15, 1000 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic with extreme values
			display.Show(tt.percentage, tt.speed, tt.downloaded, tt.total, tt.eta)
			
			// Should still be active
			if !display.IsActive() {
				t.Error("Expected display to remain active after showing extreme values")
			}
		})
	}
}