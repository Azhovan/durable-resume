// Package download provides a framework for downloading files
// in segments with support for retries in case of errors.
package download

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// DownloadManager coordinates the segmented downloading of a file.
// It uses a Downloader for actual download operations and applies a RetryPolicy
// for handling transient errors in the download process.
type DownloadManager struct {
	// Downloader is responsible for the actual downloading of file segments.
	Downloader *Downloader

	// RetryPolicy defines the strategy for retrying download attempts in case of failure.
	RetryPolicy *RetryPolicy

	// ProgressTracker tracks and displays download progress across all segments.
	ProgressTracker *ProgressTracker

	Segm *SegmentManager
}

// NewDownloadManager creates a new instance of DownloadManager with the specified downloader
// and retry policy. It returns a pointer to the DownloadManager.
func NewDownloadManager(downloader *Downloader, retryPolicy *RetryPolicy) *DownloadManager {
	return &DownloadManager{
		Downloader:  downloader,
		RetryPolicy: retryPolicy,
	}
}

// Download initiates the download process.
// It returns nil if the download completes successfully or an error if issues occur.
// TODO(azhovan): not override existing files
func (dm *DownloadManager) Download(ctx context.Context, opts ...SegmentManagerOption) error {
	err := dm.Downloader.ValidateRangeSupport(ctx, dm.Downloader.UpdateRangeSupportState)
	if err != nil {
		return err
	}

	dm.Segm, err = NewSegmentManager(
		dm.Downloader.DestinationDIR.String(),
		dm.Downloader.RangeSupport.ContentLength,
		opts...,
	)
	if err != nil {
		return err
	}

	// Initialize progress tracking
	dm.ProgressTracker = NewProgressTracker(dm.Downloader.RangeSupport.ContentLength)
	
	// Create progress display and start progress tracking
	progressDisplay := NewProgressDisplay(os.Stdout)
	dm.ProgressTracker.Start()
	
	// Start progress display goroutine
	progressDone := make(chan struct{})
	go dm.displayProgress(progressDisplay, progressDone)

	// Pass progress reporter to each segment during initialization
	for _, segment := range dm.Segm.Segments {
		segment.progressReporter = dm.ProgressTracker
	}

	// Ensure progress display cleanup on function exit
	defer func() {
		// Stop progress tracking
		dm.ProgressTracker.Stop()
		
		// Signal progress display to stop and clean up
		close(progressDone)
		progressDisplay.Clear()
	}()

	// capture errors for each segment
	errs := make(chan error, dm.Segm.TotalSegments)

	// Use a WaitGroup to wait for all download goroutines to complete
	wg := &sync.WaitGroup{}
	wg.Add(dm.Segm.TotalSegments)
	for _, segment := range dm.Segm.Segments {
		go func(seg *Segment) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				return
			default:
			}

			// Attempt to download the segment with retries
			err = dm.RetryPolicy.Retry(ctx, seg.ID, func() error {
				return dm.Downloader.DownloadSegment(ctx, seg)
			})
			if err != nil {
				errs <- err
			}
		}(segment)
	}
	wg.Wait()
	close(errs)

	// Aggregate and return any errors encountered during the download
	var allErrors []error
	for err := range errs {
		allErrors = append(allErrors, err)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("download encountered following errors: %v", allErrors)
	}

	return dm.Segm.MergeFiles(dm.Downloader.Filename())
}

// displayProgress runs in a goroutine to periodically update the progress display.
// It updates the display every 250ms with current progress metrics.
// It includes error handling to ensure downloads continue even if progress display fails.
func (dm *DownloadManager) displayProgress(display *ProgressDisplay, done <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	// Recovery function to prevent progress display issues from crashing downloads
	defer func() {
		if r := recover(); r != nil {
			// Progress display failed, but download should continue
			// This could be logged if a logger was available
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if dm.ProgressTracker != nil {
				// Use the safe progress getter that handles edge cases
				percentage, speed, eta, downloaded, total, err := dm.ProgressTracker.GetProgressSafely()
				if err != nil {
					// Progress calculation failed, but continue trying
					continue
				}

				// Try to show progress, but don't fail if display has issues
				func() {
					defer func() {
						if r := recover(); r != nil {
							// Display failed, but continue
						}
					}()
					display.Show(percentage, speed, downloaded, total, eta)
				}()
			}
		}
	}
}
