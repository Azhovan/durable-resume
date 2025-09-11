package download

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestProgressTrackingIntegration tests progress tracking during actual downloads
func TestProgressTrackingIntegration(t *testing.T) {
	tests := []struct {
		name         string
		fileSize     int64
		segmentCount int
		expectError  bool
	}{
		{
			name:         "small file single segment",
			fileSize:     1024,
			segmentCount: 1,
			expectError:  false,
		},
		{
			name:         "medium file multiple segments",
			fileSize:     10240,
			segmentCount: 4,
			expectError:  false,
		},
		{
			name:         "large file many segments",
			fileSize:     102400,
			segmentCount: 8,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server with controlled content
			server := createTestServer(tt.fileSize)
			defer server.Close()

			// Create temporary directory for test
			tempDir := t.TempDir()

			// Set up downloader
			destURL, _ := url.Parse("file://" + tempDir)
			downloader := &Downloader{
				DestinationDIR: destURL,
				RangeSupport: RangeSupport{
					ContentLength:         tt.fileSize,
					SupportsRangeRequests: tt.segmentCount > 1,
				},
			}

			// Create download manager with progress tracking
			dm := NewDownloadManager(downloader, DefaultRetryPolicy())

			// Track progress events for validation
			progressEvents := make([]ProgressEvent, 0)
			var eventsMutex sync.Mutex

			// Create custom progress tracker that captures events
			dm.ProgressTracker = NewProgressTracker(tt.fileSize)
			originalReporter := dm.ProgressTracker

			// Wrap the progress tracker to capture events
			captureReporter := &progressEventCapture{
				original: originalReporter,
				events:   &progressEvents,
				mutex:    &eventsMutex,
			}

			// Start progress tracking
			dm.ProgressTracker.Start()
			defer dm.ProgressTracker.Stop()

			// Create segments with progress reporting
			segmentManager, err := NewSegmentManager(tempDir, tt.fileSize, WithNumberOfSegments(tt.segmentCount))
			if err != nil {
				t.Fatalf("Failed to create segment manager: %v", err)
			}

			// Assign progress reporter to segments
			for _, segment := range segmentManager.Segments {
				segment.progressReporter = captureReporter
			}

			// Simulate download by writing data to segments
			err = simulateSegmentedDownload(segmentManager, tt.fileSize)
			if (err != nil) != tt.expectError {
				t.Errorf("Expected error: %v, got: %v", tt.expectError, err)
			}

			if !tt.expectError {
				// Validate progress tracking accuracy
				validateProgressAccuracy(t, &progressEvents, &eventsMutex, tt.fileSize, tt.segmentCount)

				// Validate final progress state
				validateFinalProgressState(t, dm.ProgressTracker, tt.fileSize)
			}
		})
	}
}

// TestProgressAccuracyWithVariousFileSizes tests progress accuracy with different file sizes
func TestProgressAccuracyWithVariousFileSizes(t *testing.T) {
	fileSizes := []int64{
		512,      // Very small
		1024,     // Small
		10240,    // Medium
		102400,   // Large
		1048576,  // Very large (1MB)
	}

	for _, fileSize := range fileSizes {
		t.Run(fmt.Sprintf("fileSize_%d", fileSize), func(t *testing.T) {
			tracker := NewProgressTracker(fileSize)
			tracker.Start()
			defer tracker.Stop()

			// Simulate progress from multiple segments
			numSegments := 4
			segmentSize := fileSize / int64(numSegments)

			var wg sync.WaitGroup
			for i := 0; i < numSegments; i++ {
				wg.Add(1)
				go func(segmentID int) {
					defer wg.Done()
					
					segmentStart := int64(segmentID) * segmentSize
					segmentEnd := segmentStart + segmentSize
					if segmentID == numSegments-1 {
						segmentEnd = fileSize // Last segment gets remainder
					}

					// Simulate progressive download
					bytesPerStep := (segmentEnd - segmentStart) / 10
					if bytesPerStep == 0 {
						bytesPerStep = 1
					}

					currentBytes := int64(0)
					for currentBytes < (segmentEnd - segmentStart) {
						currentBytes += bytesPerStep
						if currentBytes > (segmentEnd - segmentStart) {
							currentBytes = segmentEnd - segmentStart
						}

						event := ProgressEvent{
							SegmentID:  segmentID,
							BytesRead:  currentBytes,
							TotalBytes: segmentEnd - segmentStart,
							Speed:      1024.0,
							Timestamp:  time.Now(),
						}
						tracker.ReportProgress(event)
						time.Sleep(10 * time.Millisecond)
					}
				}(i)
			}

			wg.Wait()
			time.Sleep(100 * time.Millisecond) // Allow processing

			// Validate final state
			percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
			if err != nil {
				t.Errorf("Error getting progress: %v", err)
			}

			if total != fileSize {
				t.Errorf("Expected total size %d, got %d", fileSize, total)
			}

			if downloaded != fileSize {
				t.Errorf("Expected downloaded %d, got %d", fileSize, downloaded)
			}

			if percentage != 100.0 {
				t.Errorf("Expected 100%% completion, got %.1f%%", percentage)
			}

			if speed <= 0 {
				t.Error("Expected positive speed")
			}

			_ = eta // ETA should be 0 for completed downloads
		})
	}
}

// TestProgressDisplayWithUnknownFileSizes tests progress display behavior with unknown file sizes
func TestProgressDisplayWithUnknownFileSizes(t *testing.T) {
	tests := []struct {
		name              string
		initialTotalSize  int64
		discoveredSize    int64
		expectSizeUpdate  bool
	}{
		{
			name:              "unknown size becomes known",
			initialTotalSize:  -1,
			discoveredSize:    2048,
			expectSizeUpdate:  true,
		},
		{
			name:              "zero size becomes known",
			initialTotalSize:  0,
			discoveredSize:    1024,
			expectSizeUpdate:  true,
		},
		{
			name:              "known size remains",
			initialTotalSize:  1024,
			discoveredSize:    1024,
			expectSizeUpdate:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewProgressTracker(tt.initialTotalSize)
			tracker.Start()
			defer tracker.Stop()

			var buffer bytes.Buffer
			display := NewProgressDisplay(&buffer)

			// Initial progress with unknown size
			event1 := ProgressEvent{
				SegmentID:  0,
				BytesRead:  256,
				TotalBytes: 0, // Unknown initially
				Speed:      1024,
				Timestamp:  time.Now(),
			}
			tracker.ReportProgress(event1)
			time.Sleep(50 * time.Millisecond)

			// Show initial progress
			percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
			if err != nil {
				t.Errorf("Error getting initial progress: %v", err)
			}
			display.Show(percentage, speed, downloaded, total, eta)

			// Progress with discovered size
			event2 := ProgressEvent{
				SegmentID:  0,
				BytesRead:  512,
				TotalBytes: tt.discoveredSize,
				Speed:      1024,
				Timestamp:  time.Now(),
			}
			tracker.ReportProgress(event2)
			time.Sleep(50 * time.Millisecond)

			// Show updated progress
			percentage, speed, eta, downloaded, total, err = tracker.GetProgressSafely()
			if err != nil {
				t.Errorf("Error getting updated progress: %v", err)
			}
			display.Show(percentage, speed, downloaded, total, eta)

			// Validate size update behavior
			if tt.expectSizeUpdate {
				if total <= 0 {
					t.Error("Expected total size to be updated from segment information")
				}
				if percentage == 0 && downloaded > 0 {
					t.Error("Expected percentage calculation to work with updated total size")
				}
			}

			// Validate display output
			output := buffer.String()
			if len(output) == 0 {
				t.Error("Expected display output to be generated")
			}

			// Check that display handles unknown sizes gracefully
			if tt.initialTotalSize <= 0 {
				// Should show downloaded amount even without total
				if !strings.Contains(output, formatBytes(downloaded)) {
					t.Error("Expected display to show downloaded bytes for unknown size")
				}
			}
		})
	}
}

// TestProgressPerformanceImpact tests the performance impact of progress reporting on download speed
func TestProgressPerformanceImpact(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	dataSize := int64(1048576) // 1MB of data
	testData := make([]byte, dataSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	tests := []struct {
		name                string
		enableProgress      bool
		reportingFrequency  time.Duration
		expectedSlowdown    float64 // Maximum acceptable slowdown factor
	}{
		{
			name:                "no progress reporting",
			enableProgress:      false,
			reportingFrequency:  0,
			expectedSlowdown:    1.0,
		},
		{
			name:                "progress reporting every 100ms",
			enableProgress:      true,
			reportingFrequency:  100 * time.Millisecond,
			expectedSlowdown:    2.0, // Max 100% slowdown for small data
		},
		{
			name:                "progress reporting every 50ms",
			enableProgress:      true,
			reportingFrequency:  50 * time.Millisecond,
			expectedSlowdown:    2.5, // Max 150% slowdown for small data
		},
		{
			name:                "progress reporting every 10ms",
			enableProgress:      true,
			reportingFrequency:  10 * time.Millisecond,
			expectedSlowdown:    3.0, // Max 200% slowdown for small data
		},
	}

	baselineTime := time.Duration(0)

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			
			// Create segment for testing
			fileWriter, err := NewFileWriter(tempDir, "perf-test")
			if err != nil {
				t.Fatalf("Failed to create file writer: %v", err)
			}
			defer fileWriter.Close()

			var progressReporter ProgressReporter
			if tt.enableProgress {
				tracker := NewProgressTracker(dataSize)
				tracker.Start()
				defer tracker.Stop()
				progressReporter = tracker
			}

			segment, err := NewSegment(SegmentParams{
				ID:               0,
				Name:             "perf-test",
				Start:            0,
				End:              dataSize - 1,
				MaxSegmentSize:   dataSize,
				Writer:           fileWriter,
				ProgressReporter: progressReporter,
			})
			if err != nil {
				t.Fatalf("Failed to create segment: %v", err)
			}

			// Measure write performance
			reader := bytes.NewReader(testData)
			
			startTime := time.Now()
			_, err = segment.ReadFrom(reader)
			duration := time.Since(startTime)
			
			if err != nil {
				t.Errorf("Error during read: %v", err)
			}

			err = segment.Flush()
			if err != nil {
				t.Errorf("Error during flush: %v", err)
			}

			// Record baseline time
			if i == 0 {
				baselineTime = duration
			}

			// Calculate performance impact
			if baselineTime > 0 {
				slowdownFactor := float64(duration) / float64(baselineTime)
				t.Logf("Test: %s, Duration: %v, Slowdown: %.2fx", tt.name, duration, slowdownFactor)

				if slowdownFactor > tt.expectedSlowdown {
					t.Errorf("Performance impact too high: %.2fx slowdown (expected max %.2fx)", 
						slowdownFactor, tt.expectedSlowdown)
				}
			}

			// Validate data integrity
			fileWriter.Seek(0, io.SeekStart)
			readData := make([]byte, dataSize)
			n, err := fileWriter.Read(readData)
			if err != nil && err != io.EOF {
				t.Errorf("Error reading back data: %v", err)
			}
			if int64(n) != dataSize {
				t.Errorf("Expected to read %d bytes, got %d", dataSize, n)
			}
			if !bytes.Equal(testData, readData) {
				t.Error("Data integrity check failed")
			}
		})
	}
}

// TestProgressThreadSafety tests thread-safety under concurrent segment downloads
func TestProgressThreadSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping thread safety test in short mode")
	}

	tracker := NewProgressTracker(10240)
	tracker.Start()
	defer tracker.Stop()

	numSegments := 8
	numOperationsPerSegment := 100
	segmentSize := int64(1280) // 10240 / 8

	// Test concurrent progress reporting
	var wg sync.WaitGroup
	errors := make(chan error, numSegments*numOperationsPerSegment)

	for segmentID := 0; segmentID < numSegments; segmentID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for i := 0; i < numOperationsPerSegment; i++ {
				// Simulate progress reporting
				event := ProgressEvent{
					SegmentID:  id,
					BytesRead:  int64(i * 10),
					TotalBytes: segmentSize,
					Speed:      float64(1024 + id*100),
					Timestamp:  time.Now(),
				}

				// Report progress
				tracker.ReportProgress(event)

				// Concurrent reads of progress data
				go func() {
					_, _, _, _, _, err := tracker.GetProgressSafely()
					if err != nil {
						errors <- err
					}
				}()

				// Small delay to create realistic timing
				time.Sleep(time.Microsecond * 100)
			}
		}(segmentID)
	}

	// Concurrent progress readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				tracker.GetProgress()
				tracker.GetPercentage()
				tracker.GetSpeed()
				tracker.GetETA()
				tracker.GetDownloadedBytes()
				tracker.GetTotalSize()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for any errors
	for err := range errors {
		t.Errorf("Thread safety error: %v", err)
	}

	// Validate final state consistency
	percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
	if err != nil {
		t.Errorf("Error getting final progress: %v", err)
	}

	// Basic sanity checks
	if total != 10240 {
		t.Errorf("Expected total size 10240, got %d", total)
	}

	if downloaded < 0 {
		t.Errorf("Downloaded bytes should not be negative: %d", downloaded)
	}

	if percentage < 0 || percentage > 100 {
		t.Errorf("Percentage should be 0-100, got %.1f", percentage)
	}

	if speed < 0 {
		t.Errorf("Speed should not be negative: %.1f", speed)
	}

	_ = eta // ETA can vary based on timing
}

// TestProgressDisplayThreadSafety tests thread-safety of progress display
func TestProgressDisplayThreadSafety(t *testing.T) {
	var buffer bytes.Buffer
	display := NewProgressDisplay(&buffer)

	numGoroutines := 10
	numOperations := 50

	var wg sync.WaitGroup

	// Concurrent display operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				percentage := float64(j * 2)
				speed := float64(1024 * (id + 1))
				downloaded := int64(j * 100)
				total := int64(5000)
				eta := time.Duration(j) * time.Second

				display.Show(percentage, speed, downloaded, total, eta)

				// Concurrent status checks
				go func() {
					display.IsActive()
					display.IsFallbackMode()
				}()

				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Concurrent clear operations
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				display.Clear()
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	wg.Wait()

	// Test should complete without panics or race conditions
	// The exact output content is not important, just that it doesn't crash
}

// TestProgressWithNetworkInterruptions tests progress handling during network interruptions
func TestProgressWithNetworkInterruptions(t *testing.T) {
	tracker := NewProgressTracker(2048)
	tracker.Start()
	defer tracker.Stop()

	// Simulate normal progress
	event1 := ProgressEvent{
		SegmentID:  0,
		BytesRead:  512,
		TotalBytes: 2048,
		Speed:      1024,
		Timestamp:  time.Now(),
	}
	tracker.ReportProgress(event1)
	time.Sleep(50 * time.Millisecond)

	// Simulate network interruption (retry with lower bytes)
	event2 := ProgressEvent{
		SegmentID:  0,
		BytesRead:  256, // Lower than before due to retry
		TotalBytes: 2048,
		Speed:      512,
		Timestamp:  time.Now(),
	}
	tracker.ReportProgress(event2)
	time.Sleep(50 * time.Millisecond)

	// Simulate recovery
	event3 := ProgressEvent{
		SegmentID:  0,
		BytesRead:  768,
		TotalBytes: 2048,
		Speed:      1024,
		Timestamp:  time.Now(),
	}
	tracker.ReportProgress(event3)
	time.Sleep(50 * time.Millisecond)

	// Validate that progress handling is robust
	percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
	if err != nil {
		t.Errorf("Error getting progress after interruption: %v", err)
	}

	if downloaded < 0 {
		t.Error("Downloaded bytes should not be negative after interruption")
	}

	if total != 2048 {
		t.Errorf("Expected total size 2048, got %d", total)
	}

	if percentage < 0 || percentage > 100 {
		t.Errorf("Percentage should be 0-100 after interruption, got %.1f", percentage)
	}

	_ = speed
	_ = eta
}

// Helper functions

// progressEventCapture captures progress events for testing
type progressEventCapture struct {
	original ProgressReporter
	events   *[]ProgressEvent
	mutex    *sync.Mutex
}

func (pec *progressEventCapture) ReportProgress(event ProgressEvent) {
	pec.mutex.Lock()
	*pec.events = append(*pec.events, event)
	pec.mutex.Unlock()
	
	if pec.original != nil {
		pec.original.ReportProgress(event)
	}
}

// createTestServer creates a test HTTP server that serves controlled content
func createTestServer(fileSize int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.Header().Set("Accept-Ranges", "bytes")
		
		// Generate test data
		data := make([]byte, fileSize)
		for i := range data {
			data[i] = byte(i % 256)
		}
		
		w.Write(data)
	}))
}

// simulateSegmentedDownload simulates a segmented download by writing data to segments
func simulateSegmentedDownload(sm *SegmentManager, totalSize int64) error {
	var wg sync.WaitGroup
	errors := make(chan error, len(sm.Segments))

	for _, segment := range sm.Segments {
		wg.Add(1)
		go func(seg *Segment) {
			defer wg.Done()

			// Calculate segment size
			segmentSize := totalSize / int64(len(sm.Segments))
			if seg.ID == len(sm.Segments)-1 {
				// Last segment gets remainder
				segmentSize = totalSize - int64(seg.ID)*segmentSize
			}

			// Generate test data for this segment
			data := make([]byte, segmentSize)
			for i := range data {
				data[i] = byte((seg.ID*1000 + i) % 256)
			}

			// Write data in chunks to simulate realistic download
			chunkSize := int64(256)
			for offset := int64(0); offset < segmentSize; offset += chunkSize {
				end := offset + chunkSize
				if end > segmentSize {
					end = segmentSize
				}

				chunk := data[offset:end]
				_, err := seg.Write(chunk)
				if err != nil {
					errors <- err
					return
				}

				// Small delay to simulate network timing
				time.Sleep(time.Millisecond)
			}

			// Flush the segment
			err := seg.Flush()
			if err != nil {
				errors <- err
			}
		}(segment)
	}

	wg.Wait()
	close(errors)

	// Return first error if any
	for err := range errors {
		return err
	}

	return nil
}

// validateProgressAccuracy validates that progress tracking is accurate
func validateProgressAccuracy(t *testing.T, events *[]ProgressEvent, mutex *sync.Mutex, expectedTotal int64, segmentCount int) {
	mutex.Lock()
	defer mutex.Unlock()

	if len(*events) == 0 {
		t.Error("Expected progress events to be captured")
		return
	}

	// Validate that we have events from all segments
	segmentsSeen := make(map[int]bool)
	for _, event := range *events {
		segmentsSeen[event.SegmentID] = true

		// Validate event data
		if event.BytesRead < 0 {
			t.Errorf("Invalid bytes read in event: %d", event.BytesRead)
		}
		if event.Speed < 0 {
			t.Errorf("Invalid speed in event: %f", event.Speed)
		}
		if event.Timestamp.IsZero() {
			t.Error("Invalid timestamp in event")
		}
	}

	if len(segmentsSeen) != segmentCount {
		t.Errorf("Expected events from %d segments, got %d", segmentCount, len(segmentsSeen))
	}
}

// validateFinalProgressState validates the final state of progress tracking
func validateFinalProgressState(t *testing.T, tracker *ProgressTracker, expectedTotal int64) {
	percentage, speed, eta, downloaded, total, err := tracker.GetProgressSafely()
	if err != nil {
		t.Errorf("Error getting final progress: %v", err)
	}

	if total != expectedTotal {
		t.Errorf("Expected total size %d, got %d", expectedTotal, total)
	}

	if downloaded < 0 {
		t.Errorf("Downloaded bytes should not be negative: %d", downloaded)
	}

	if percentage < 0 || percentage > 100 {
		t.Errorf("Percentage should be 0-100, got %.1f", percentage)
	}

	if speed < 0 {
		t.Errorf("Speed should not be negative: %.1f", speed)
	}

	_ = eta // ETA can vary
}