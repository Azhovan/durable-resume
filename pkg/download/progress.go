package download

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ProgressEvent represents a progress update from a download segment.
// It contains information about the segment's current download state including
// bytes read, total bytes, speed, and timestamp.
type ProgressEvent struct {
	// SegmentID uniquely identifies the segment reporting progress
	SegmentID int

	// BytesRead represents the number of bytes read by this segment so far
	BytesRead int64

	// TotalBytes represents the total number of bytes this segment needs to download
	// This may be 0 or -1 if the total size is unknown
	TotalBytes int64

	// Speed represents the current download speed in bytes per second
	Speed float64

	// Timestamp indicates when this progress event was generated
	Timestamp time.Time
}

// ProgressReporter defines the interface for components that can report download progress.
// Segments implement this interface to send progress updates to the progress tracker.
type ProgressReporter interface {
	// ReportProgress sends a progress event to the progress tracking system
	ReportProgress(event ProgressEvent)
}

// SegmentProgress tracks the progress metrics for an individual download segment.
// It maintains the current state of bytes downloaded, timing information, and speed calculations.
type SegmentProgress struct {
	// BytesDownloaded represents the total number of bytes downloaded by this segment
	BytesDownloaded int64

	// LastUpdate stores the timestamp of the last progress update from this segment
	LastUpdate time.Time

	// Speed represents the current download speed for this segment in bytes per second
	Speed float64
}

// ProgressTracker aggregates progress from multiple download segments and provides
// overall download metrics including completion percentage, speed, and ETA.
type ProgressTracker struct {
	// totalSize represents the total size of the file being downloaded
	totalSize int64

	// downloadedBytes represents the total bytes downloaded across all segments
	downloadedBytes int64

	// segmentProgress maps segment IDs to their individual progress tracking
	segmentProgress map[int]*SegmentProgress

	// startTime records when the download started
	startTime time.Time

	// lastUpdate records the timestamp of the last progress update
	lastUpdate time.Time

	// eventChan receives progress events from segments
	eventChan chan ProgressEvent

	// done signals when progress tracking should stop
	done chan struct{}

	// trackingErrors stores any errors encountered during progress tracking
	trackingErrors []error

	// isHealthy indicates whether progress tracking is functioning properly
	isHealthy bool

	// mu protects concurrent access to progress data
	mu sync.RWMutex
}

// NewProgressTracker creates a new ProgressTracker instance for tracking download progress.
func NewProgressTracker(totalSize int64) *ProgressTracker {
	return &ProgressTracker{
		totalSize:       totalSize,
		downloadedBytes: 0,
		segmentProgress: make(map[int]*SegmentProgress),
		startTime:       time.Now(),
		lastUpdate:      time.Now(),
		eventChan:       make(chan ProgressEvent, 100), // Buffered channel to prevent blocking
		done:            make(chan struct{}),
		trackingErrors:  make([]error, 0),
		isHealthy:       true,
	}
}

// Start begins the progress tracking by starting the event processing goroutine.
func (pt *ProgressTracker) Start() {
	go pt.processEvents()
}

// Stop signals the progress tracker to stop processing events and clean up resources.
func (pt *ProgressTracker) Stop() {
	close(pt.done)
}

// ReportProgress implements the ProgressReporter interface to receive progress events from segments.
// It gracefully handles errors and ensures that progress reporting failures don't block downloads.
func (pt *ProgressTracker) ReportProgress(event ProgressEvent) {
	// Validate the event before processing
	if err := pt.validateProgressEvent(event); err != nil {
		pt.handleTrackingError(err)
		return
	}

	select {
	case pt.eventChan <- event:
		// Event sent successfully
	case <-pt.done:
		// Progress tracker is shutting down
	default:
		// Channel is full, skip this update to prevent blocking
		// This ensures downloads continue even if progress tracking can't keep up
		pt.handleTrackingError(fmt.Errorf("progress event channel full, skipping update for segment %d", event.SegmentID))
	}
}

// processEvents runs in a goroutine to process incoming progress events from segments.
// It includes error recovery to ensure progress tracking continues even if individual updates fail.
func (pt *ProgressTracker) processEvents() {
	defer func() {
		// Recover from any panics to prevent crashing the entire download
		if r := recover(); r != nil {
			pt.handleTrackingError(fmt.Errorf("progress tracking panic recovered: %v", r))
		}
	}()

	for {
		select {
		case event := <-pt.eventChan:
			// Process the event with error handling
			if err := pt.updateProgressSafely(event); err != nil {
				pt.handleTrackingError(err)
			}
		case <-pt.done:
			return
		}
	}
}

// updateProgress processes a single progress event and updates the internal state.
func (pt *ProgressTracker) updateProgress(event ProgressEvent) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Get or create segment progress
	segmentProg, exists := pt.segmentProgress[event.SegmentID]
	if !exists {
		segmentProg = &SegmentProgress{}
		pt.segmentProgress[event.SegmentID] = segmentProg
	}

	// Calculate the difference in bytes for this segment
	bytesDiff := event.BytesRead - segmentProg.BytesDownloaded

	// Update segment progress
	segmentProg.BytesDownloaded = event.BytesRead
	segmentProg.LastUpdate = event.Timestamp
	segmentProg.Speed = event.Speed

	// Update total downloaded bytes
	pt.downloadedBytes += bytesDiff
	pt.lastUpdate = event.Timestamp
}

// updateProgressSafely wraps updateProgress with additional error handling and validation.
func (pt *ProgressTracker) updateProgressSafely(event ProgressEvent) error {
	// Additional validation for edge cases
	if event.BytesRead < 0 {
		return fmt.Errorf("invalid bytes read: %d for segment %d", event.BytesRead, event.SegmentID)
	}

	if event.Speed < 0 {
		return fmt.Errorf("invalid speed: %f for segment %d", event.Speed, event.SegmentID)
	}

	// Handle unknown file sizes gracefully
	if pt.totalSize <= 0 && event.TotalBytes > 0 {
		pt.mu.Lock()
		if pt.totalSize <= 0 {
			pt.totalSize = event.TotalBytes
		}
		pt.mu.Unlock()
	}

	pt.updateProgress(event)
	return nil
}

// GetProgress returns the current progress metrics in a thread-safe manner.
func (pt *ProgressTracker) GetProgress() (percentage float64, speed float64, eta time.Duration, downloaded int64, total int64) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	downloaded = pt.downloadedBytes
	total = pt.totalSize

	// Calculate percentage
	if total > 0 {
		percentage = float64(downloaded) / float64(total) * 100
		if percentage > 100 {
			percentage = 100
		}
	} else {
		percentage = 0
	}

	// Calculate overall speed (sum of all segment speeds)
	speed = 0
	for _, segmentProg := range pt.segmentProgress {
		speed += segmentProg.Speed
	}

	// Calculate ETA
	if speed > 0 && total > 0 {
		remainingBytes := total - downloaded
		if remainingBytes > 0 {
			eta = time.Duration(float64(remainingBytes)/speed) * time.Second
		}
	}

	return percentage, speed, eta, downloaded, total
}

// GetPercentage returns the current completion percentage (0-100).
func (pt *ProgressTracker) GetPercentage() float64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	if pt.totalSize <= 0 {
		return 0
	}

	percentage := float64(pt.downloadedBytes) / float64(pt.totalSize) * 100
	if percentage > 100 {
		percentage = 100
	}
	return percentage
}

// GetSpeed returns the current overall download speed in bytes per second.
func (pt *ProgressTracker) GetSpeed() float64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	speed := 0.0
	for _, segmentProg := range pt.segmentProgress {
		speed += segmentProg.Speed
	}
	return speed
}

// GetETA returns the estimated time remaining for the download.
func (pt *ProgressTracker) GetETA() time.Duration {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	if pt.totalSize <= 0 {
		return 0
	}

	speed := 0.0
	for _, segmentProg := range pt.segmentProgress {
		speed += segmentProg.Speed
	}

	if speed <= 0 {
		return 0
	}

	remainingBytes := pt.totalSize - pt.downloadedBytes
	if remainingBytes <= 0 {
		return 0
	}

	return time.Duration(float64(remainingBytes)/speed) * time.Second
}

// GetDownloadedBytes returns the total number of bytes downloaded so far.
func (pt *ProgressTracker) GetDownloadedBytes() int64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.downloadedBytes
}

// GetTotalSize returns the total size of the file being downloaded.
func (pt *ProgressTracker) GetTotalSize() int64 {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.totalSize
}

// ProgressDisplay handles the visual rendering of progress information to the terminal.
// It manages terminal output, line clearing, and formatting of progress data.
type ProgressDisplay struct {
	// writer is the output destination for progress display
	writer io.Writer

	// lastLine stores the last rendered line for proper cleanup
	lastLine string

	// isActive indicates whether the progress display is currently active
	isActive bool

	// supportsTerminalControl indicates whether the terminal supports ANSI escape sequences
	supportsTerminalControl bool

	// fallbackMode indicates whether to use simple text output instead of progress bars
	fallbackMode bool

	// displayErrors tracks any errors encountered during display operations
	displayErrors []error

	// mu protects concurrent access to display state
	mu sync.Mutex
}

// NewProgressDisplay creates a new ProgressDisplay instance that writes to the specified writer.
func NewProgressDisplay(writer io.Writer) *ProgressDisplay {
	pd := &ProgressDisplay{
		writer:                  writer,
		lastLine:                "",
		isActive:                false,
		supportsTerminalControl: true,
		fallbackMode:            false,
		displayErrors:           make([]error, 0),
	}

	// Test terminal capabilities on initialization
	pd.testTerminalCapabilities()
	return pd
}

// Show renders the current progress information to the terminal.
// It displays a progress bar with percentage, speed, data transferred, and ETA.
// If terminal manipulation fails, it gracefully degrades to simple text output.
func (pd *ProgressDisplay) Show(percentage float64, speed float64, downloaded int64, total int64, eta time.Duration) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	// If we're in fallback mode, use simple text output
	if pd.fallbackMode {
		pd.showFallback(percentage, speed, downloaded, total, eta)
		return
	}

	// Try to clear the previous line if one exists
	if pd.lastLine != "" {
		if err := pd.clearLine(); err != nil {
			pd.handleDisplayError(err)
			pd.showFallback(percentage, speed, downloaded, total, eta)
			return
		}
	}

	// Build the progress line
	line := pd.buildProgressLine(percentage, speed, downloaded, total, eta)

	// Try to write the new line
	if _, err := fmt.Fprint(pd.writer, line); err != nil {
		pd.handleDisplayError(err)
		pd.showFallback(percentage, speed, downloaded, total, eta)
		return
	}

	pd.lastLine = line
	pd.isActive = true
}

// Clear removes the progress display from the terminal and marks it as inactive.
func (pd *ProgressDisplay) Clear() {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	if pd.lastLine != "" && !pd.fallbackMode {
		// Try to clear the line, but don't fail if it doesn't work
		_ = pd.clearLine()
		pd.lastLine = ""
	}
	pd.isActive = false
}

// IsActive returns whether the progress display is currently active.
func (pd *ProgressDisplay) IsActive() bool {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return pd.isActive
}

// clearLine clears the current line in the terminal by moving cursor to beginning and clearing to end.
// Returns an error if the terminal operation fails.
func (pd *ProgressDisplay) clearLine() error {
	if !pd.supportsTerminalControl {
		return fmt.Errorf("terminal does not support control sequences")
	}

	// Move cursor to beginning of line and clear to end
	_, err := fmt.Fprint(pd.writer, "\r\033[K")
	return err
}

// buildProgressLine constructs the formatted progress line with all relevant information.
func (pd *ProgressDisplay) buildProgressLine(percentage float64, speed float64, downloaded int64, total int64, eta time.Duration) string {
	var parts []string

	// Progress bar (20 characters wide)
	progressBar := pd.buildProgressBar(percentage, 20)
	parts = append(parts, progressBar)

	// Percentage
	parts = append(parts, fmt.Sprintf("%.1f%%", percentage))

	// Data transferred
	if total > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s", formatBytes(downloaded), formatBytes(total)))
	} else {
		parts = append(parts, formatBytes(downloaded))
	}

	// Speed
	if speed > 0 {
		parts = append(parts, fmt.Sprintf("%s/s", formatBytes(int64(speed))))
	}

	// ETA
	if eta > 0 {
		parts = append(parts, fmt.Sprintf("ETA: %s", formatDuration(eta)))
	}

	return strings.Join(parts, " ")
}

// buildProgressBar creates a visual progress bar with the specified width and completion percentage.
func (pd *ProgressDisplay) buildProgressBar(percentage float64, width int) string {
	if width <= 2 {
		return "[]"
	}

	// Ensure percentage is within valid range
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}

	innerWidth := width - 2 // Account for [ and ]
	filled := int(percentage / 100.0 * float64(innerWidth))
	if filled > innerWidth {
		filled = innerWidth
	}
	if filled < 0 {
		filled = 0
	}

	bar := "["
	
	if filled > 0 {
		bar += strings.Repeat("=", filled)
	}
	
	// Add the progress indicator ">" only if not at 0% or 100%
	if percentage > 0 && percentage < 100 && filled < innerWidth {
		bar += ">"
		remaining := innerWidth - filled - 1
		if remaining > 0 {
			bar += strings.Repeat(" ", remaining)
		}
	} else {
		// Fill remaining space with spaces or equals
		remaining := innerWidth - filled
		if remaining > 0 {
			bar += strings.Repeat(" ", remaining)
		}
	}
	
	bar += "]"
	return bar
}

// formatBytes converts a byte count to a human-readable string with appropriate units.
func formatBytes(bytes int64) string {
	if bytes < 0 {
		return "0 B"
	}

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	units := []string{"KB", "MB", "GB", "TB", "PB"}
	if exp >= len(units) {
		exp = len(units) - 1
		div = int64(1)
		for i := 0; i <= exp; i++ {
			div *= unit
		}
	}

	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}

// formatDuration converts a duration to a human-readable string (e.g., "2m30s", "1h15m").
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}

	// Round to seconds
	d = d.Round(time.Second)

	hours := d / time.Hour
	minutes := (d % time.Hour) / time.Minute
	seconds := (d % time.Minute) / time.Second

	if hours > 0 {
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}

	if minutes > 0 {
		if seconds > 0 {
			return fmt.Sprintf("%dm%ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	}

	return fmt.Sprintf("%ds", seconds)
}

// testTerminalCapabilities tests whether the terminal supports ANSI escape sequences.
// This helps determine if we can use advanced terminal features or need to fall back to simple text.
func (pd *ProgressDisplay) testTerminalCapabilities() {
	// For now, assume terminal control is supported unless we detect otherwise
	// More sophisticated detection could be added here (e.g., checking TERM environment variable)
	pd.supportsTerminalControl = true
	pd.fallbackMode = false
}

// handleDisplayError handles errors that occur during display operations.
// It switches to fallback mode to ensure the download can continue.
func (pd *ProgressDisplay) handleDisplayError(err error) {
	pd.displayErrors = append(pd.displayErrors, err)
	
	// Switch to fallback mode after the first error
	if !pd.fallbackMode {
		pd.fallbackMode = true
		pd.supportsTerminalControl = false
	}
}

// showFallback displays progress using simple text output without terminal manipulation.
// This is used when terminal control sequences are not supported or have failed.
func (pd *ProgressDisplay) showFallback(percentage float64, speed float64, downloaded int64, total int64, eta time.Duration) {
	var parts []string

	// Simple percentage display
	parts = append(parts, fmt.Sprintf("Progress: %.1f%%", percentage))

	// Data transferred
	if total > 0 {
		parts = append(parts, fmt.Sprintf("(%s/%s)", formatBytes(downloaded), formatBytes(total)))
	} else {
		parts = append(parts, fmt.Sprintf("(%s downloaded)", formatBytes(downloaded)))
	}

	// Speed
	if speed > 0 {
		parts = append(parts, fmt.Sprintf("Speed: %s/s", formatBytes(int64(speed))))
	}

	// ETA
	if eta > 0 {
		parts = append(parts, fmt.Sprintf("ETA: %s", formatDuration(eta)))
	}

	line := strings.Join(parts, " ")
	
	// In fallback mode, we print each update on a new line
	// If this also fails, we just continue silently to not block downloads
	_, err := fmt.Fprintln(pd.writer, line)
	if err != nil {
		pd.handleDisplayError(err)
	}
	pd.isActive = true
}

// GetDisplayErrors returns any errors that occurred during display operations.
// This can be used for debugging or logging purposes.
func (pd *ProgressDisplay) GetDisplayErrors() []error {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	
	// Return a copy to prevent external modification
	errors := make([]error, len(pd.displayErrors))
	copy(errors, pd.displayErrors)
	return errors
}

// IsFallbackMode returns whether the display is currently operating in fallback mode.
func (pd *ProgressDisplay) IsFallbackMode() bool {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return pd.fallbackMode
}

// validateProgressEvent validates a progress event for correctness.
func (pt *ProgressTracker) validateProgressEvent(event ProgressEvent) error {
	if event.SegmentID < 0 {
		return fmt.Errorf("invalid segment ID: %d", event.SegmentID)
	}

	if event.BytesRead < 0 {
		return fmt.Errorf("invalid bytes read: %d", event.BytesRead)
	}

	if event.Speed < 0 {
		return fmt.Errorf("invalid speed: %f", event.Speed)
	}

	if event.Timestamp.IsZero() {
		return fmt.Errorf("invalid timestamp for segment %d", event.SegmentID)
	}

	return nil
}

// handleTrackingError handles errors that occur during progress tracking.
// It logs the error and may disable progress tracking if too many errors occur.
func (pt *ProgressTracker) handleTrackingError(err error) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.trackingErrors = append(pt.trackingErrors, err)

	// If we have too many errors, mark tracking as unhealthy but continue
	if len(pt.trackingErrors) > 10 {
		pt.isHealthy = false
	}
}

// GetTrackingErrors returns any errors that occurred during progress tracking.
func (pt *ProgressTracker) GetTrackingErrors() []error {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	// Return a copy to prevent external modification
	errors := make([]error, len(pt.trackingErrors))
	copy(errors, pt.trackingErrors)
	return errors
}

// IsHealthy returns whether progress tracking is functioning properly.
func (pt *ProgressTracker) IsHealthy() bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.isHealthy
}

// GetProgressSafely returns progress metrics with error handling for edge cases.
// It ensures that calculations don't panic and provides sensible defaults for invalid states.
func (pt *ProgressTracker) GetProgressSafely() (percentage float64, speed float64, eta time.Duration, downloaded int64, total int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("progress calculation panic: %v", r)
			// Provide safe defaults
			percentage, speed, eta, downloaded, total = 0, 0, 0, 0, 0
		}
	}()

	pt.mu.RLock()
	defer pt.mu.RUnlock()

	downloaded = pt.downloadedBytes
	total = pt.totalSize

	// Handle unknown file sizes
	if total <= 0 {
		percentage = 0
		// For unknown sizes, we can't calculate meaningful ETA
		eta = 0
	} else {
		percentage = float64(downloaded) / float64(total) * 100
		if percentage > 100 {
			percentage = 100
		}
	}

	// Calculate overall speed (sum of all segment speeds)
	speed = 0
	activeSegments := 0
	for _, segmentProg := range pt.segmentProgress {
		if segmentProg != nil {
			speed += segmentProg.Speed
			activeSegments++
		}
	}

	// Calculate ETA only if we have valid data
	if speed > 0 && total > 0 && downloaded < total {
		remainingBytes := total - downloaded
		if remainingBytes > 0 {
			eta = time.Duration(float64(remainingBytes)/speed) * time.Second
		}
	}

	return percentage, speed, eta, downloaded, total, nil
}