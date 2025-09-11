package cmd

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CLIArgs represents the parsed command line arguments for the enhanced CLI interface
type CLIArgs struct {
	// URL is the source URL to download (positional argument or --url flag)
	URL string

	// Output is the output path (-o/--output flag) - can be file path or directory
	Output string

	// Name is the filename (-n/--name flag) - used when output is a directory
	Name string

	// Segments is the number of download segments (-c/--segments flag)
	Segments int

	// SegmentSize is the size of each segment (-s/--segment-size flag)
	SegmentSize int64

	// NoSegments disables segmented downloading (--no-segments flag)
	NoSegments bool

	// Quiet suppresses progress output (-q/--quiet flag)
	Quiet bool

	// Verbose enables enhanced logging (-v/--verbose flag)
	Verbose bool

	// Resume enables resume mode (-r/--resume flag, default true)
	Resume bool

	// Legacy indicates if this is using the legacy "download" subcommand
	Legacy bool
}

// DownloadConfig represents the resolved configuration for a download operation
type DownloadConfig struct {
	// Core settings
	URL        string // Validated source URL
	OutputPath string // Full path to output file

	// Segmentation settings
	SegmentCount int   // Number of segments to use
	SegmentSize  int64 // Size of each segment
	UseSegments  bool  // Whether to use segmented downloading

	// Behavior settings
	Resume  bool // Whether to resume interrupted downloads
	Quiet   bool // Suppress progress output
	Verbose bool // Enable verbose logging

	// Derived fields
	Directory string // Directory portion of OutputPath
	Filename  string // Filename portion of OutputPath
}

// NewCLIArgs creates a new CLIArgs instance with default values
func NewCLIArgs() *CLIArgs {
	return &CLIArgs{
		Segments: 4, // Default number of segments
		Resume:   true,
	}
}

// ValidateURL validates that the provided URL is valid and accessible
func ValidateURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("Error: No URL provided\n\nA download URL is required to proceed.\n\nUsage:\n  dr <URL>\n  dr --url <URL>\n\nExamples:\n  dr https://example.com/file.zip\n  dr ftp://files.example.com/data.tar.gz")
	}

	// Trim whitespace that might cause parsing issues
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("Error: Empty URL provided\n\nPlease provide a valid URL.\n\nExamples:\n  dr https://example.com/file.zip\n  dr ftp://files.example.com/data.tar.gz")
	}

	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil {
		// Provide more specific error messages based on common mistakes
		if !strings.Contains(rawURL, "://") {
			return fmt.Errorf("Error: Invalid URL format - missing protocol\n\nURL: %s\n\nThe URL must include a protocol (http://, https://, or ftp://).\n\nDid you mean:\n  https://%s\n  http://%s", rawURL, rawURL, rawURL)
		}
		return fmt.Errorf("Error: Invalid URL format\n\nURL: %s\nReason: %v\n\nPlease provide a complete URL including protocol.\n\nExamples:\n  https://example.com/file.zip\n  ftp://files.example.com/data.tar.gz", rawURL, err)
	}

	// Check if scheme is supported
	scheme := strings.ToLower(parsedURL.Scheme)
	supportedSchemes := []string{"http", "https", "ftp", "ftps"}
	isSupported := false
	for _, supported := range supportedSchemes {
		if scheme == supported {
			isSupported = true
			break
		}
	}
	
	if !isSupported {
		return fmt.Errorf("Error: Unsupported URL protocol '%s'\n\nURL: %s\n\nSupported protocols: %s\n\nExamples:\n  https://example.com/file.zip\n  ftp://files.example.com/data.tar.gz", scheme, rawURL, strings.Join(supportedSchemes, ", "))
	}

	// Check if host is present
	if parsedURL.Host == "" {
		return fmt.Errorf("Error: Invalid URL - missing hostname\n\nURL: %s\n\nThe URL must include a hostname after the protocol.\n\nExample: https://example.com/file.zip", rawURL)
	}

	// Validate hostname format (basic check)
	if strings.Contains(parsedURL.Host, " ") {
		return fmt.Errorf("Error: Invalid hostname in URL\n\nURL: %s\n\nHostnames cannot contain spaces.\n\nExample: https://example.com/file.zip", rawURL)
	}

	// Check for suspicious or malformed URLs
	if strings.HasPrefix(parsedURL.Host, ".") || strings.HasSuffix(parsedURL.Host, ".") {
		return fmt.Errorf("Error: Invalid hostname format\n\nURL: %s\n\nHostname cannot start or end with a dot.\n\nExample: https://example.com/file.zip", rawURL)
	}

	return nil
}

// ValidatePath validates that the provided path is valid, writable, and has sufficient disk space
func ValidatePath(path string) error {
	if path == "" {
		return nil // Empty path is valid (will use current directory)
	}

	// Resolve relative paths
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("Error: Invalid path format\n\nPath: %s\nReason: %v\n\nPlease provide a valid file or directory path.", path, err)
	}

	// Check for invalid characters in path (basic validation)
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("Error: Invalid path - contains null character\n\nPath: %s\n\nPaths cannot contain null characters.", path)
	}

	// Check if path exists
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Path doesn't exist, check if parent directory exists and is writable
			parentDir := filepath.Dir(absPath)
			parentInfo, parentErr := os.Stat(parentDir)
			if parentErr != nil {
				if os.IsNotExist(parentErr) {
					return fmt.Errorf("Error: Parent directory does not exist\n\nPath: %s\nParent directory: %s\n\nPlease create the directory first or choose an existing location.", path, parentDir)
				}
				return fmt.Errorf("Error: Cannot access parent directory\n\nPath: %s\nParent directory: %s\nReason: %v", path, parentDir, parentErr)
			}
			if !parentInfo.IsDir() {
				return fmt.Errorf("Error: Parent path is not a directory\n\nPath: %s\nParent path: %s\n\nThe parent path must be a directory, not a file.", path, parentDir)
			}
			// Check write permission and disk space on parent directory
			if err := checkWritePermission(parentDir); err != nil {
				return err
			}
			return checkDiskSpace(parentDir)
		}
		return fmt.Errorf("Error: Cannot access path\n\nPath: %s\nReason: %v\n\nPlease check that the path exists and you have permission to access it.", path, err)
	}

	// If path exists and is a directory, check write permission and disk space
	if info.IsDir() {
		if err := checkWritePermission(absPath); err != nil {
			return err
		}
		return checkDiskSpace(absPath)
	}

	// If path exists and is a file, check write permission and disk space on parent directory
	parentDir := filepath.Dir(absPath)
	if err := checkWritePermission(parentDir); err != nil {
		return err
	}
	return checkDiskSpace(parentDir)
}

// checkWritePermission checks if the directory is writable
func checkWritePermission(dir string) error {
	// Try to create a temporary file to test write permission
	tempFile := filepath.Join(dir, ".durable-resume-write-test")
	file, err := os.Create(tempFile)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("Error: Permission denied\n\nDirectory: %s\n\nYou don't have write permission to this directory.\n\nSolutions:\n  - Choose a different directory (e.g., your home directory)\n  - Run with appropriate permissions\n  - Check directory ownership and permissions", dir)
		}
		return fmt.Errorf("Error: Cannot write to directory\n\nDirectory: %s\nReason: %v\n\nPlease ensure the directory is writable.", dir, err)
	}
	file.Close()
	os.Remove(tempFile) // Clean up
	return nil
}

// checkDiskSpace checks if there's sufficient disk space available
func checkDiskSpace(dir string) error {
	// Get disk usage information
	var stat syscall.Statfs_t
	err := syscall.Statfs(dir, &stat)
	if err != nil {
		// If we can't get disk space info, just warn but don't fail
		// This might happen on some filesystems or in containers
		return nil
	}

	// Calculate available space in bytes
	availableBytes := stat.Bavail * uint64(stat.Bsize)
	
	// Require at least 100MB of free space as a safety margin
	const minRequiredBytes = 100 * 1024 * 1024 // 100MB
	
	if availableBytes < minRequiredBytes {
		return fmt.Errorf("Error: Insufficient disk space\n\nDirectory: %s\nAvailable space: %s\nMinimum required: %s\n\nPlease free up disk space or choose a different location.", 
			dir, 
			formatBytes(availableBytes), 
			formatBytes(minRequiredBytes))
	}

	// Warn if less than 1GB available
	const warnThresholdBytes = 1024 * 1024 * 1024 // 1GB
	if availableBytes < warnThresholdBytes {
		fmt.Fprintf(os.Stderr, "Warning: Low disk space\n\nDirectory: %s\nAvailable space: %s\n\nConsider freeing up space before downloading large files.\n\n", 
			dir, formatBytes(availableBytes))
	}

	return nil
}

// formatBytes formats byte count as human-readable string
func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ValidateSegmentParams validates segment-related parameters with enhanced error messages
func ValidateSegmentParams(segments int, segmentSize int64, noSegments bool) error {
	if noSegments {
		return nil // No validation needed when segments are disabled
	}

	if segments < 1 {
		return fmt.Errorf("Error: Invalid segment count\n\nValue: %d\n\nSegment count must be at least 1.\n\nRecommended values:\n  --segments 4  (default, good for most files)\n  --segments 8  (for large files or fast connections)\n  --segments 1  (single-threaded download)", segments)
	}

	if segments > 32 {
		return fmt.Errorf("Error: Too many segments\n\nValue: %d\nMaximum: 32\n\nUsing too many segments can hurt performance and may be blocked by servers.\n\nRecommended values:\n  --segments 4  (default)\n  --segments 8  (for large files)\n  --segments 16 (maximum for most use cases)", segments)
	}

	if segmentSize < 0 {
		return fmt.Errorf("Error: Invalid segment size\n\nValue: %d bytes\n\nSegment size cannot be negative.\n\nValid options:\n  --segment-size 0     (auto-calculate based on file size)\n  --segment-size 1048576  (1MB segments)\n  --segment-size 10485760 (10MB segments)", segmentSize)
	}

	if segmentSize > 0 && segmentSize < 1024 {
		return fmt.Errorf("Error: Segment size too small\n\nValue: %d bytes\nMinimum: 1024 bytes (1KB)\n\nSmall segments create excessive overhead.\n\nRecommended values:\n  --segment-size 0       (auto-calculate)\n  --segment-size 1048576 (1MB)\n  --segment-size 10485760 (10MB)", segmentSize)
	}

	// Warn about very large segment sizes
	const maxRecommendedSize = 100 * 1024 * 1024 // 100MB
	if segmentSize > maxRecommendedSize {
		fmt.Fprintf(os.Stderr, "Warning: Very large segment size\n\nValue: %s\n\nLarge segments may reduce the benefits of parallel downloading.\nConsider using smaller segments (1-10MB) for better performance.\n\n", formatBytes(uint64(segmentSize)))
	}

	return nil
}

// ResolveOutputPath resolves the final output path based on URL, output, and name parameters
func ResolveOutputPath(url, output, name string) (string, error) {
	// Case 1: No output specified - use current directory + name or URL filename
	if output == "" {
		if name != "" {
			return name, nil
		}
		// Extract filename from URL
		urlFilename := extractFilenameFromURL(url)
		if urlFilename == "" {
			return "", fmt.Errorf("Error: Cannot determine filename\n\nURL: %s\n\nThe URL doesn't contain a clear filename.\n\nSolutions:\n  dr %s --name myfile.ext\n  dr %s --output /path/to/myfile.ext\n\nExamples:\n  dr %s --name download.zip\n  dr %s --output ./downloads/file.bin", url, url, url, url, url)
		}
		return urlFilename, nil
	}

	// Check if output is an existing directory
	info, err := os.Stat(output)
	if err == nil && info.IsDir() {
		// Case 2: Output is directory - combine with name or URL filename
		filename := name
		if filename == "" {
			filename = extractFilenameFromURL(url)
			if filename == "" {
				return "", fmt.Errorf("Error: Cannot determine filename for directory output\n\nURL: %s\nDirectory: %s\n\nThe URL doesn't contain a clear filename.\n\nSolution:\n  dr %s --output %s --name myfile.ext\n\nExample:\n  dr %s --output %s --name download.zip", url, output, url, output, url, output)
			}
		}
		return filepath.Join(output, filename), nil
	}

	// Case 3: Output is a file path (existing file or new file path)
	if name != "" {
		return "", fmt.Errorf("Error: Conflicting output specification\n\nFile path: %s\nFilename: %s\n\nYou cannot specify both a complete file path and a separate filename.\n\nChoose one:\n  dr %s --output %s\n  dr %s --output %s --name %s", output, name, url, output, url, filepath.Dir(output), name)
	}

	return output, nil
}

// extractFilenameFromURL extracts the filename from a URL
func extractFilenameFromURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// If the path ends with a slash, it's a directory, not a file
	if strings.HasSuffix(parsedURL.Path, "/") {
		return ""
	}

	filename := filepath.Base(parsedURL.Path)
	if filename == "." || filename == "/" || filename == "path" {
		return ""
	}

	return filename
}

// ValidateFlagCombinations checks for incompatible flag combinations
func ValidateFlagCombinations(args *CLIArgs) error {
	var conflicts []string

	// Check for quiet and verbose conflict
	if args.Quiet && args.Verbose {
		conflicts = append(conflicts, "Cannot use both --quiet and --verbose flags simultaneously")
	}

	// Check for no-segments with segment-specific flags
	// Note: We can't easily detect if segments was explicitly set vs default here,
	// so we'll only flag obvious conflicts where segments is significantly different from default
	if args.NoSegments {
		// Only conflict if segments is explicitly set to something other than the default
		// This is a limitation - ideally we'd track which flags were explicitly set
		if args.Segments != 4 && args.Segments != 0 && args.Segments != 1 {
			conflicts = append(conflicts, "Cannot use --no-segments with --segments flag")
		}
		if args.SegmentSize != 0 {
			conflicts = append(conflicts, "Cannot use --no-segments with --segment-size flag")
		}
	}

	// Check for conflicting output specifications
	if args.Output != "" && args.Name != "" {
		// This is only a conflict if output appears to be a file path, not a directory
		if !strings.HasSuffix(args.Output, "/") {
			// Check if output path exists and is a file
			if info, err := os.Stat(args.Output); err == nil && !info.IsDir() {
				conflicts = append(conflicts, "Cannot specify both file path (--output) and filename (--name) when output is a file")
			}
			// Also check if output path looks like a file (has extension)
			if filepath.Ext(args.Output) != "" {
				conflicts = append(conflicts, "Cannot specify both file path (--output) and filename (--name) when output appears to be a file path")
			}
		}
	}

	// Check for unreasonable segment combinations
	if !args.NoSegments && args.Segments > 1 && args.SegmentSize > 0 {
		// Warn if segment size is very large relative to number of segments
		totalEstimatedSize := int64(args.Segments) * args.SegmentSize
		if totalEstimatedSize > 10*1024*1024*1024 { // 10GB
			fmt.Fprintf(os.Stderr, "Warning: Large estimated download size\n\nSegments: %d\nSegment size: %s\nEstimated total: %s\n\nThis configuration may not be suitable for smaller files.\nConsider using --segment-size 0 for automatic sizing.\n\n", 
				args.Segments, formatBytes(uint64(args.SegmentSize)), formatBytes(uint64(totalEstimatedSize)))
		}
	}

	// Report conflicts
	if len(conflicts) > 0 {
		errorMsg := "Error: Incompatible flag combinations detected\n\n"
		for i, conflict := range conflicts {
			errorMsg += fmt.Sprintf("%d. %s\n", i+1, conflict)
		}
		errorMsg += "\nPlease review your command and remove conflicting flags.\n\nFor help: dr --help"
		return fmt.Errorf(errorMsg)
	}

	return nil
}

// ToDownloadConfig converts CLIArgs to DownloadConfig with validation and resolution
func (args *CLIArgs) ToDownloadConfig() (*DownloadConfig, error) {
	// Validate flag combinations first
	if err := ValidateFlagCombinations(args); err != nil {
		return nil, err
	}

	// Validate URL
	if err := ValidateURL(args.URL); err != nil {
		return nil, err
	}

	// Validate segment parameters
	if err := ValidateSegmentParams(args.Segments, args.SegmentSize, args.NoSegments); err != nil {
		return nil, err
	}

	// Resolve output path
	outputPath, err := ResolveOutputPath(args.URL, args.Output, args.Name)
	if err != nil {
		return nil, err
	}

	// Validate the resolved output path
	if err := ValidatePath(outputPath); err != nil {
		return nil, err
	}

	// Create configuration
	config := &DownloadConfig{
		URL:        args.URL,
		OutputPath: outputPath,
		Resume:     args.Resume,
		Quiet:      args.Quiet,
		Verbose:    args.Verbose,
		Directory:  filepath.Dir(outputPath),
		Filename:   filepath.Base(outputPath),
	}

	// Set segmentation options
	if args.NoSegments {
		config.UseSegments = false
		config.SegmentCount = 1
	} else {
		config.UseSegments = true
		config.SegmentCount = args.Segments
		config.SegmentSize = args.SegmentSize
	}

	return config, nil
}