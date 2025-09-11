package download

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// BenchmarkProgressReporting benchmarks the performance impact of progress reporting
func BenchmarkProgressReporting(b *testing.B) {
	dataSize := int64(1024 * 1024) // 1MB
	testData := make([]byte, dataSize)
	for i := range testData {
		testData[i] = byte(i % 256)
	}

	b.Run("WithoutProgressReporting", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tempDir := b.TempDir()
			fileWriter, err := NewFileWriter(tempDir, "bench-test")
			if err != nil {
				b.Fatalf("Failed to create file writer: %v", err)
			}

			segment, err := NewSegment(SegmentParams{
				ID:             0,
				Name:           "bench-test",
				Start:          0,
				End:            dataSize - 1,
				MaxSegmentSize: dataSize,
				Writer:         fileWriter,
				// No progress reporter
			})
			if err != nil {
				b.Fatalf("Failed to create segment: %v", err)
			}

			reader := bytes.NewReader(testData)
			
			b.StartTimer()
			_, err = segment.ReadFrom(reader)
			b.StopTimer()
			
			if err != nil {
				b.Errorf("Error during read: %v", err)
			}
			
			segment.Flush()
			fileWriter.Close()
		}
	})

	b.Run("WithProgressReporting", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			tempDir := b.TempDir()
			fileWriter, err := NewFileWriter(tempDir, "bench-test")
			if err != nil {
				b.Fatalf("Failed to create file writer: %v", err)
			}

			tracker := NewProgressTracker(dataSize)
			tracker.Start()

			segment, err := NewSegment(SegmentParams{
				ID:               0,
				Name:             "bench-test",
				Start:            0,
				End:              dataSize - 1,
				MaxSegmentSize:   dataSize,
				Writer:           fileWriter,
				ProgressReporter: tracker,
			})
			if err != nil {
				b.Fatalf("Failed to create segment: %v", err)
			}

			reader := bytes.NewReader(testData)
			
			b.StartTimer()
			_, err = segment.ReadFrom(reader)
			b.StopTimer()
			
			if err != nil {
				b.Errorf("Error during read: %v", err)
			}
			
			segment.Flush()
			tracker.Stop()
			fileWriter.Close()
		}
	})
}

// BenchmarkProgressTracker benchmarks the progress tracker operations
func BenchmarkProgressTracker(b *testing.B) {
	tracker := NewProgressTracker(1024 * 1024)
	tracker.Start()
	defer tracker.Stop()

	b.Run("ReportProgress", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			event := ProgressEvent{
				SegmentID:  i % 4,
				BytesRead:  int64(i * 100),
				TotalBytes: 1024 * 1024,
				Speed:      1024.0,
				Timestamp:  time.Now(),
			}
			tracker.ReportProgress(event)
		}
	})

	b.Run("GetProgress", func(b *testing.B) {
		// Report some initial progress
		for i := 0; i < 10; i++ {
			event := ProgressEvent{
				SegmentID:  i % 4,
				BytesRead:  int64(i * 1000),
				TotalBytes: 1024 * 1024,
				Speed:      1024.0,
				Timestamp:  time.Now(),
			}
			tracker.ReportProgress(event)
		}
		
		time.Sleep(50 * time.Millisecond) // Allow processing

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tracker.GetProgress()
		}
	})

	b.Run("GetProgressSafely", func(b *testing.B) {
		// Report some initial progress
		for i := 0; i < 10; i++ {
			event := ProgressEvent{
				SegmentID:  i % 4,
				BytesRead:  int64(i * 1000),
				TotalBytes: 1024 * 1024,
				Speed:      1024.0,
				Timestamp:  time.Now(),
			}
			tracker.ReportProgress(event)
		}
		
		time.Sleep(50 * time.Millisecond) // Allow processing

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tracker.GetProgressSafely()
		}
	})
}

// BenchmarkProgressDisplay benchmarks the progress display operations
func BenchmarkProgressDisplay(b *testing.B) {
	var buffer bytes.Buffer
	display := NewProgressDisplay(&buffer)

	b.Run("Show", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			percentage := float64(i%100 + 1)
			speed := 1024.0 * float64(i%10+1)
			downloaded := int64(i * 1000)
			total := int64(100000)
			eta := time.Duration(i%60) * time.Second

			display.Show(percentage, speed, downloaded, total, eta)
		}
	})

	b.Run("BuildProgressBar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			percentage := float64(i % 101)
			display.buildProgressBar(percentage, 20)
		}
	})

	b.Run("BuildProgressLine", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			percentage := float64(i%100 + 1)
			speed := 1024.0 * float64(i%10+1)
			downloaded := int64(i * 1000)
			total := int64(100000)
			eta := time.Duration(i%60) * time.Second

			display.buildProgressLine(percentage, speed, downloaded, total, eta)
		}
	})
}

// BenchmarkFormatting benchmarks the formatting functions
func BenchmarkFormatting(b *testing.B) {
	b.Run("FormatBytes", func(b *testing.B) {
		testSizes := []int64{
			512,
			1024,
			1536,
			1048576,
			1073741824,
			1099511627776,
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			size := testSizes[i%len(testSizes)]
			formatBytes(size)
		}
	})

	b.Run("FormatDuration", func(b *testing.B) {
		testDurations := []time.Duration{
			30 * time.Second,
			5 * time.Minute,
			2*time.Minute + 30*time.Second,
			2 * time.Hour,
			1*time.Hour + 30*time.Minute,
			2*time.Hour + 15*time.Minute + 45*time.Second,
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			duration := testDurations[i%len(testDurations)]
			formatDuration(duration)
		}
	})
}

// BenchmarkConcurrentProgressReporting benchmarks concurrent progress reporting
func BenchmarkConcurrentProgressReporting(b *testing.B) {
	tracker := NewProgressTracker(1024 * 1024)
	tracker.Start()
	defer tracker.Stop()

	b.RunParallel(func(pb *testing.PB) {
		segmentID := 0
		bytesRead := int64(0)
		
		for pb.Next() {
			event := ProgressEvent{
				SegmentID:  segmentID,
				BytesRead:  bytesRead,
				TotalBytes: 1024 * 1024,
				Speed:      1024.0,
				Timestamp:  time.Now(),
			}
			tracker.ReportProgress(event)
			
			bytesRead += 100
			if bytesRead > 1024*1024 {
				bytesRead = 0
				segmentID = (segmentID + 1) % 4
			}
		}
	})
}

// BenchmarkProgressDisplayConcurrent benchmarks concurrent progress display operations
func BenchmarkProgressDisplayConcurrent(b *testing.B) {
	display := NewProgressDisplay(io.Discard) // Use discard to avoid I/O overhead

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			percentage := float64(i%100 + 1)
			speed := 1024.0 * float64(i%10+1)
			downloaded := int64(i * 1000)
			total := int64(100000)
			eta := time.Duration(i%60) * time.Second

			display.Show(percentage, speed, downloaded, total, eta)
			i++
		}
	})
}

// BenchmarkSegmentProgressReporting benchmarks segment progress reporting
func BenchmarkSegmentProgressReporting(b *testing.B) {
	tempDir := b.TempDir()
	fileWriter, err := NewFileWriter(tempDir, "bench-segment")
	if err != nil {
		b.Fatalf("Failed to create file writer: %v", err)
	}
	defer fileWriter.Close()

	tracker := NewProgressTracker(1024 * 1024)
	tracker.Start()
	defer tracker.Stop()

	segment, err := NewSegment(SegmentParams{
		ID:               0,
		Name:             "bench-segment",
		Start:            0,
		End:              1024*1024 - 1,
		MaxSegmentSize:   1024 * 1024,
		Writer:           fileWriter,
		ProgressReporter: tracker,
	})
	if err != nil {
		b.Fatalf("Failed to create segment: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		segment.reportProgress(1024) // Report 1KB progress
	}
}