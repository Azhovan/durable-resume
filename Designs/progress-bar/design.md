# Design Document

## Overview

The progress bar feature will provide real-time visual feedback during file downloads by tracking progress across all segments and displaying relevant metrics like completion percentage, download speed, data transferred, and estimated time remaining. The implementation will integrate seamlessly with the existing segmented download architecture without disrupting the current download flow.

## Architecture

The progress bar system will be implemented using a publisher-subscriber pattern where download segments publish progress events, and a progress tracker aggregates and displays this information. This approach ensures loose coupling and maintains the existing concurrent download architecture.

### Key Components

1. **ProgressTracker**: Central component that aggregates progress from all segments
2. **ProgressEvent**: Data structure for progress updates from segments  
3. **ProgressDisplay**: Handles the visual rendering of progress information
4. **ProgressReporter**: Interface for segments to report progress updates

## Components and Interfaces

### ProgressEvent
```go
type ProgressEvent struct {
    SegmentID     int
    BytesRead     int64
    TotalBytes    int64
    Speed         float64  // bytes per second
    Timestamp     time.Time
}
```

### ProgressTracker
```go
type ProgressTracker struct {
    totalSize       int64
    downloadedBytes int64
    segmentProgress map[int]*SegmentProgress
    startTime       time.Time
    lastUpdate      time.Time
    display         *ProgressDisplay
    eventChan       chan ProgressEvent
    done            chan struct{}
    mu              sync.RWMutex
}

type SegmentProgress struct {
    BytesDownloaded int64
    LastUpdate      time.Time
    Speed           float64
}
```

### ProgressDisplay
```go
type ProgressDisplay struct {
    writer    io.Writer
    lastLine  string
    isActive  bool
}
```

### ProgressReporter Interface
```go
type ProgressReporter interface {
    ReportProgress(event ProgressEvent)
}
```

## Data Models

### Progress Metrics
- **Completion Percentage**: Calculated as `(totalDownloaded / totalSize) * 100`
- **Download Speed**: Moving average of bytes per second across all segments
- **ETA**: Estimated time remaining based on current speed and remaining bytes
- **Data Display**: Human-readable format (B, KB, MB, GB) for downloaded/total bytes

### Segment Integration
Each segment will be enhanced with progress reporting capability:
```go
type Segment struct {
    // existing fields...
    progressReporter ProgressReporter
    lastReportedBytes int64
}
```

## Error Handling

### Progress Display Errors
- **Terminal Output Issues**: Gracefully degrade to simple text output if terminal manipulation fails
- **Calculation Errors**: Handle division by zero and invalid data gracefully
- **Segment Reporting Failures**: Continue progress tracking even if individual segments fail to report

### Recovery Strategies
- Progress display failures should not interrupt downloads
- Missing segment progress data should be handled by using last known values
- Speed calculations should handle temporary network interruptions

## Testing Strategy

### Unit Tests
1. **ProgressTracker Tests**
   - Test progress aggregation from multiple segments
   - Verify speed calculation accuracy
   - Test ETA estimation logic
   - Validate thread-safety of concurrent updates

2. **ProgressDisplay Tests**
   - Test progress bar rendering with various completion percentages
   - Verify proper formatting of data sizes and speeds
   - Test terminal output cleanup on completion/cancellation

3. **Integration Tests**
   - Test progress reporting during actual downloads
   - Verify accuracy of progress tracking with segmented downloads
   - Test behavior with unknown file sizes
   - Validate progress display during network interruptions

### Performance Considerations
- Progress updates should not significantly impact download performance
- Use buffered channels to prevent blocking segment downloads
- Limit progress display update frequency to avoid terminal flicker
- Minimize memory allocation in hot paths

## Integration Points

### DownloadManager Integration
The DownloadManager will be enhanced to:
1. Create and initialize a ProgressTracker
2. Pass the progress reporter to each segment
3. Start progress display before beginning downloads
4. Clean up progress display on completion/error

### Segment Integration
Each segment will:
1. Accept a ProgressReporter during initialization
2. Report progress after each successful read operation
3. Include progress reporting in the existing download loop

### Command Line Integration
The download command will:
1. Initialize progress tracking when downloads start
2. Handle progress display cleanup on interruption (Ctrl+C)
3. Show final completion message after progress bar cleanup

## Implementation Phases

### Phase 1: Core Progress Tracking
- Implement ProgressTracker and ProgressEvent structures
- Add progress reporting interface to segments
- Create basic progress aggregation logic

### Phase 2: Progress Display
- Implement ProgressDisplay with terminal output
- Add progress bar rendering with percentage and data metrics
- Implement speed calculation and ETA estimation

### Phase 3: Integration
- Integrate progress tracking into DownloadManager
- Enhance segments with progress reporting
- Add progress display to download command

### Phase 4: Polish and Testing
- Add comprehensive error handling
- Implement graceful degradation for unsupported terminals
- Add unit and integration tests
- Performance optimization and testing