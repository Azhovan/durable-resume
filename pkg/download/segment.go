package download

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoContent          = errors.New("there is no contents in segments")
	ErrInvalidContentType = errors.New("can't determine the content type")
)

// The Segment represents a part of the file being downloaded.
// It contains the data and state for a specific portion (segment) of the file.
type Segment struct {
	// SegmentParams contains the configuration parameters for the segment.
	SegmentParams

	// Done indicates whether the download of this segment is complete.
	// It is set to true once the segment is successfully downloaded or if an irrecoverable error occurs.
	Done bool

	// Resumable indicates whether the provided writer supports resuming.
	// It is set to true if the writer implements the io.Seeker interface, enabling the segment
	// to resume writing from a specific offset.
	Resumable bool

	// Buffer is used to temporarily store data for this segment before writing to the file.
	// It helps in efficient writing by reducing the number of write operations.
	Buffer *bufio.Writer

	// CurrentOffset represents the current position within the file immediately after the last write operation.
	// It tracks the byte offset where the next writing will occur, ensuring data is written to the correct location in the file.
	// This offset is updated each time a write operation is completed, reflecting the new position for subsequent operations.
	CurrentOffset int

	// progressReporter is used to report progress updates during download operations.
	// It allows the segment to communicate its download progress to the progress tracking system.
	progressReporter ProgressReporter

	// lastReportedBytes tracks the number of bytes that were last reported to avoid duplicate reporting.
	// This helps optimize progress reporting by only sending updates when there are actual changes.
	lastReportedBytes int64

	// lastProgressTime tracks the last time progress was reported to control reporting frequency.
	lastProgressTime time.Time
}

// SegmentManager manages the segments involved in a file download process.
// It encapsulates all the information and operations related to the segments
// which are parts of the file being downloaded.
type SegmentManager struct {
	// ID is a unique segment manager identifier, this is used to prefix downloaded temporary files.
	ID int

	// DestinationDir is the directory where temporary/final segment file(s) is stored
	DestinationDir string

	// FileSize represents the size of the remote file to be downloaded. It can hold various values:
	// - A positive value: Indicates the known size of the file.
	// - Zero or -1: These values imply that the file size is unknown at the start of the download.
	//               In such cases, the file is treated as a non-segmented download, and the stream
	//               is read until the end. This approach is used for dynamically sized content or
	//               when the server doesn't provide a 'Content-Length' header.
	// - Upon completion of the download, if FileSize was initially zero or -1 but the downloaded content
	//   has a non-zero size, FileSize is updated to reflect the actual size of the downloaded file.
	//   This provides accurate information about the file size post-download, which is particularly useful
	//   for downloads with initially unknown sizes.
	FileSize int64

	// Segments is a map where each key represents a unique segment index,
	// and the corresponding value is a pointer to a Segment struct.
	// Each Segment struct contains data representing a specific part of the
	// file being downloaded. This map is populated dynamically as the file
	// download progresses.
	//
	// TODO(azhovan): consider dynamic adjustment of segment size
	Segments []*Segment

	// SegmentSize specifies the maximum size, in bytes, that each segment can contain.
	// It determines the volume of data each segment fetches in a single request and
	// can be adjusted to optimize data transfer based on network capabilities and server
	// limitations. Smaller segment sizes can be more efficient in low-bandwidth situations,
	// while larger sizes may enhance performance over high-speed connections.
	SegmentSize int64

	// TotalSegments represents the total number of segments
	// that the downloader will use for the download process.
	// This number controls the concurrency level of the download,
	// with each segment being downloaded simultaneously.
	// A higher value for TotalSegments can increase download speeds
	// but may also lead to increased memory and network resource usage.
	// Conversely, a lower value may be more resource-efficient.
	TotalSegments int
}

type SegmentManagerOption func(manager *SegmentManager)

// WithSegmentSize is an option function for configuring the size of each segment in a Downloader instance.
// This function sets the size of individual segments into which the file will be divided during the download process.
func WithSegmentSize(size int64) SegmentManagerOption {
	return func(sm *SegmentManager) {
		if size > 0 {
			sm.SegmentSize = size
		}
	}
}

// WithNumberOfSegments is an option function used to configure the number of segments for a Downloader instance.
// This function sets up the initial segment map with the specified size, where each segment represents a part
// of the file to be downloaded. Each segment in the map will be downloaded concurrently.
//
// Note:
//   - A higher segment count can increase download speed by downloading parts of the file in parallel, but
//     it can also lead to increased memory and network resource usage.
//   - Conversely, a lower segment size might be more efficient in terms of resource usage, especially in
//     environments with bandwidth limitations or less capable servers.
//   - It is important to find a balance that suits the specific requirements of the environment and the file size.
func WithNumberOfSegments(n int) SegmentManagerOption {
	return func(sm *SegmentManager) {
		if n > 0 {
			sm.TotalSegments = n
		}
	}
}

// SegmentParams represents the parameters for a specific segment of a file being downloaded.
// It contains information such as the segment ID, start and end byte offsets, errors encountered,
// maximum segment size, and the writer to which the segment data will be written.
type SegmentParams struct {
	// id uniquely identifies the segment.
	ID int

	// Name is the name of the temporary file associated with the segment.
	Name string

	// Start indicates the starting byte of the segment within the file.
	// It marks the beginning of the portion of the file this segment is responsible for downloading.
	Start int64

	// End indicates the ending byte of the segment within the file.
	// This is the last byte in the range of this segment, inclusive.
	End int64

	// Err captures any error encountered during the downloading of this segment.
	// A 'sticky' error remains associated with the segment to indicate issues like network failures or server errors.
	Err error

	// maxSegmentSize specifies the maximum size in bytes that this segment can handle.
	// It's used to control the volume of data fetched in a single request and can be adjusted for optimization.
	MaxSegmentSize int64

	// Writer is an io.WriteCloser where the data for each segment is written and persisted.
	// This field allows the Segment to abstract the details of where and how the data is stored.
	// It could be a file, a buffer in memory, or any other type that implements the io.Writer interface.
	Writer io.WriteCloser

	// ProgressReporter is used to report progress updates during download operations.
	// It can be nil if progress reporting is not needed for this segment.
	ProgressReporter ProgressReporter
}

const DefaultNumberOfSegments = 4

// NewSegmentManager initializes and returns a new SegmentManager.
// It takes the destination directory for segment files, the total file size to be downloaded,
// and optional SegmentManagerOption functions to configure the SegmentManager.
//
// dstDir specifies the directory where segment files will be stored. If it is empty,
// the default directory used will be "/tmp".
//
// This function creates a SegmentManager with a unique ID (based on the current time's nanoseconds),
// calculates the segment size or number of segments based on the provided options,
// and initializes Segment structs for each segment of the file.
//
// The SegmentManager's responsibility includes keeping track of each segment's
// status and managing the download process for each part of the file.
//
// If fileSize is invalid, or if there's a conflict between the total number of segments
// and segment size, an InvalidParamError is returned.
//
// Each segment is represented by a file in the destination directory, named with a pattern
// that includes the SegmentManager's ID and the segment's index.
func NewSegmentManager(dstDir string, fileSize int64, opts ...SegmentManagerOption) (*SegmentManager, error) {
	if dstDir == "" {
		dstDir = "/tmp"
	}
	sm := &SegmentManager{
		ID:             time.Now().Nanosecond(),
		DestinationDir: dstDir,
		FileSize:       fileSize,
	}

	for _, opt := range opts {
		opt(sm)
	}

	if sm.TotalSegments > 0 && sm.SegmentSize > 0 {
		return nil, &InvalidParamError{
			param:   "TotalSegments, SegmentSize",
			message: "these two properties are mutually exclusive, set only one of them",
		}
	}
	if sm.TotalSegments < 0 || sm.SegmentSize < 0 {
		return nil, &InvalidParamError{
			param:   "TotalSegments, SegmentSize",
			message: "only non-negative values are accepted",
		}
	}

	// The file either is empty or is unknown, either case it is treated like a non-segmented file
	if sm.FileSize == -1 || sm.FileSize == 0 {
		sm.TotalSegments = 1
		sm.SegmentSize = 0
	}

	if sm.FileSize > 0 {
		ts := sm.TotalSegments
		sz := sm.SegmentSize
		if ts > 0 && sz == 0 {
			sm.SegmentSize = fileSize / int64(sm.TotalSegments)
		} else if ts == 0 && sz > 0 {
			sm.TotalSegments = int(math.Ceil(float64(fileSize / sm.SegmentSize)))
		} else {
			sm.TotalSegments = DefaultNumberOfSegments
			sm.SegmentSize = fileSize / int64(sm.TotalSegments)
		}
	}

	// Initialize segments
	sm.Segments = make([]*Segment, sm.TotalSegments)
	for i := 0; i < sm.TotalSegments; i++ {
		var start, end = int64(0), int64(0)
		// A non-segmented remote file, only one segment is created and SegmentSize = -1
		if sm.SegmentSize > 0 {
			start = int64(i) * sm.SegmentSize
			end = start + sm.SegmentSize - 1
			// Set the end range can be set for the last segment.
			// In other cases, we are relying on the server returned status code
			if i == sm.TotalSegments-1 {
				end = fileSize - 1
			}
		}

		segmentName := fmt.Sprintf("segment-%d-part-%d", sm.ID, i)
		// create a new temporary file for each segment,
		fileWriter, err := NewFileWriter(dstDir, segmentName)
		if err != nil {
			return nil, err
		}

		segment, err := NewSegment(SegmentParams{
			ID:             i,
			Name:           segmentName,
			Start:          start,
			End:            end,
			MaxSegmentSize: sm.SegmentSize,
			Writer:         fileWriter,
		})
		if err != nil {
			return nil, err
		}

		sm.Segments[segment.ID] = segment
	}

	return sm, nil
}

type SegmentError struct {
	Err     error
	Details string
}

func (e *SegmentError) Error() string {
	return fmt.Sprintf("%s, with error: %v", e.Details, e.Err)
}

// MergeFiles concatenates multiple segment files into one file with the specified filename.
// The content type of the merged file is determined by reading the first 512 bytes of the first segment.
// If there are no segments to merge, it returns an ErrNoContent error.
func (sm *SegmentManager) MergeFiles(filename string) error {
	if len(sm.Segments) == 0 {
		return ErrNoContent
	}

	// read 512 bytes of the first segment to determine the content type
	segment0 := sm.Segments[0]
	file0, err := NewFileWriter(sm.DestinationDir, segment0.Name)
	if err != nil {
		return &SegmentError{Err: err, Details: "reading segment0 failed"}
	}

	m := make([]byte, 512)
	_, err = file0.Read(m)
	if err != nil {
		return &SegmentError{Err: err, Details: "reading segment0 failed"}
	}

	ext, err := detectType(m)
	if err != nil {
		return err
	}

	wg := &sync.WaitGroup{}
	// concatenate segment files
	for i := 1; i < len(sm.Segments); i++ {
		current := sm.Segments[i]
		f, err := NewFileWriter(sm.DestinationDir, current.Name)
		if err != nil {
			return err
		}

		wg.Add(1)

		_, err = segment0.ReadFrom(f)
		if err != nil {
			return &SegmentError{Err: err, Details: fmt.Sprintf("reading segment %d failed", i)}
		}

		// remove the temporary segment file
		go func(file *os.File) {
			defer wg.Done()
			_ = file.Close()
			_ = os.Remove(file.Name())
		}(f)
	}

	wg.Wait()

	err = segment0.setDone(true)
	if err != nil {
		return &SegmentError{Err: err, Details: "closing the segment 0 failed"}
	}

	// set the destination file name
	return os.Rename(file0.Name(), fmt.Sprintf("%s/%s%s", sm.DestinationDir, filename, ext))
}

func detectType(m []byte) (string, error) {
	ct := http.DetectContentType(m)

	// usually the content type comes with <media type><subtype>; <extra information>
	mediaSubtype, _, _ := strings.Cut(ct, ";")
	ext, ok := commonMimeTypes[mediaSubtype]
	if ok {
		return ext, nil
	}

	_, subtype, ok := strings.Cut(ct, "/")
	if !ok {
		return "", ErrInvalidContentType
	}

	return fmt.Sprintf(".%s", subtype), nil
}

// progressTrackingReader wraps an io.Reader to track progress during read operations.
// It reports progress to the segment's progress reporter after each read.
type progressTrackingReader struct {
	reader  io.Reader
	segment *Segment
}

// Read implements io.Reader interface and tracks progress for each read operation.
func (ptr *progressTrackingReader) Read(p []byte) (n int, err error) {
	n, err = ptr.reader.Read(p)
	if n > 0 {
		ptr.segment.reportProgress(int64(n))
	}
	return n, err
}

// reportProgress reports the progress of this segment to the progress reporter.
// It calculates the current speed and sends a progress event.
// To avoid performance impact, it limits the frequency of progress reports.
// It includes error handling to ensure download continues even if progress reporting fails.
func (seg *Segment) reportProgress(bytesRead int64) {
	if seg.progressReporter == nil {
		return
	}

	// Validate input to prevent invalid progress reports
	if bytesRead < 0 {
		return
	}

	now := time.Now()
	seg.lastReportedBytes += bytesRead

	// Limit progress reporting frequency to avoid performance impact
	// Report at most every 100ms or when significant data (1KB) has been read
	timeSinceLastReport := now.Sub(seg.lastProgressTime)
	if timeSinceLastReport < 100*time.Millisecond && seg.lastReportedBytes < 1024 {
		return
	}

	// Calculate speed based on time elapsed since last report
	var speed float64
	if timeSinceLastReport > 0 {
		speed = float64(seg.lastReportedBytes) / timeSinceLastReport.Seconds()
	}

	// Handle edge cases for speed calculation
	if speed < 0 {
		speed = 0
	}

	// Calculate total bytes for this segment
	totalBytes := seg.calculateTotalBytes()

	// Create and send progress event
	event := ProgressEvent{
		SegmentID:  seg.ID,
		BytesRead:  seg.getCurrentTotalBytes(),
		TotalBytes: totalBytes,
		Speed:      speed,
		Timestamp:  now,
	}

	// Try to report progress, but don't fail if it doesn't work
	defer func() {
		if r := recover(); r != nil {
			// Progress reporting failed, but continue download
			// This could be logged if a logger was available
		}
	}()

	seg.progressReporter.ReportProgress(event)

	// Reset tracking for next interval
	seg.lastReportedBytes = 0
	seg.lastProgressTime = now
}

// calculateTotalBytes calculates the total bytes for this segment, handling edge cases.
func (seg *Segment) calculateTotalBytes() int64 {
	// For segmented downloads with known ranges
	if seg.End > 0 && seg.Start >= 0 && seg.End >= seg.Start {
		return seg.End - seg.Start + 1
	}

	// For non-segmented downloads, use MaxSegmentSize if available
	if seg.MaxSegmentSize > 0 {
		return seg.MaxSegmentSize
	}

	// Unknown size - return 0 to indicate unknown
	return 0
}

// getCurrentTotalBytes returns the total bytes read by this segment so far.
// This includes both the current reporting interval and all previous bytes.
func (seg *Segment) getCurrentTotalBytes() int64 {
	// This should represent the total bytes read by this segment
	// We need to track this properly across all read operations
	return int64(seg.CurrentOffset) + seg.lastReportedBytes
}

// NewSegment creates a new instance of a Segment struct.
// It initializes a segment of a file to be downloaded, with specified start and end byte positions.
// The caller is responsible for managing the temporary file, including its deletion after the segment is processed.
func NewSegment(params SegmentParams) (*Segment, error) {
	if err := validateParams(params); err != nil {
		return nil, err
	}

	_, resumable := params.Writer.(io.Seeker)
	return &Segment{
		SegmentParams:     params,
		Done:              false,
		Resumable:         resumable,
		Buffer:            bufio.NewWriterSize(params.Writer, int(params.MaxSegmentSize)),
		progressReporter:  params.ProgressReporter,
		lastReportedBytes: 0,
		lastProgressTime:  time.Now(),
	}, nil
}

// NewFileWriter creates a new temporary file in the specified directory with the given name pattern.
// It returns a pointer to the created os.File and any error encountered during the file creation process.
func NewFileWriter(dir, name string) (*os.File, error) {
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return nil, err
	}

	fileName := fmt.Sprintf("%s/%s", strings.TrimSuffix(dir, string(filepath.Separator)), name)
	file, err := os.OpenFile(fileName, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}

	return file, nil
}

func (seg *Segment) ReadFrom(src io.Reader) (int64, error) {
	if seg.Resumable {
		seeker, ok := seg.Writer.(io.Seeker)
		if !ok {
			return 0, fmt.Errorf("writer does not support seeking")
		}
		n, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return n, nil
		}
	}

	// Use a custom reader wrapper to track progress during the read operation
	progressReader := &progressTrackingReader{
		reader:  src,
		segment: seg,
	}

	return seg.Buffer.ReadFrom(progressReader)
}

// Write writes the given data to the segment's buffer.
func (seg *Segment) Write(data []byte) (int, error) {
	n, err := seg.Buffer.Write(data)
	if err == nil {
		seg.CurrentOffset += n
		// Report progress for direct writes
		seg.reportProgress(int64(n))
	}
	return n, err
}

// Flush flushes the segment's buffer, writing any buffered data to the underlying io.Writer.
func (seg *Segment) Flush() error {
	return seg.Buffer.Flush()
}

// Close closes the segment's underline writer.
func (seg *Segment) Close() error {
	return seg.Writer.Close()
}

// setErr sets the segment's error field if the provided error is non-nil.
func (seg *Segment) setErr(err error) {
	if err != nil {
		seg.Err = err
	}
}

// setDone marks the segment as done or not done based on the provided boolean value.
// If the boolean is false or if there is an existing error, it returns the error.
// Otherwise, it sets the segment as done and flushes its buffer.
// This function is used to finalize the segment's operations.
func (seg *Segment) setDone(b bool) error {
	if b == false || seg.Err != nil {
		return seg.Err
	}

	seg.Done = b
	return seg.Flush()
}

// InvalidParamError is a custom error type, indicates an invalid segment parameters.
type InvalidParamError struct {
	param, message string
}

func (e *InvalidParamError) Error() string {
	return fmt.Sprintf("param:%s, given:%s", e.param, e.message)
}

func validateParams(params SegmentParams) error {
	if params.Start < 0 || params.End < 0 {
		return &InvalidParamError{param: "Start, End", message: "start, end position must be positive and start < end"}
	}
	if params.Writer == nil {
		return &InvalidParamError{param: "Writer", message: "segment writer is nil"}
	}
	if params.MaxSegmentSize < 0 {
		return &InvalidParamError{param: "MaxSegmentSize", message: "MaxSegmentSize must be greater or equal to zero"}
	}

	return nil
}
