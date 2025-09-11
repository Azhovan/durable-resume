package cmd

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
		return fmt.Errorf("URL cannot be empty")
	}

	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %v\n\nPlease provide a complete URL including protocol.\n\nExamples:\n  https://example.com/file.zip\n  ftp://files.example.com/data.tar.gz", err)
	}

	// Check if scheme is supported
	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "ftp" {
		return fmt.Errorf("unsupported URL scheme '%s'\n\nSupported schemes: http, https, ftp", scheme)
	}

	// Check if host is present
	if parsedURL.Host == "" {
		return fmt.Errorf("URL must include a hostname\n\nExample: https://example.com/file.zip")
	}

	return nil
}

// ValidatePath validates that the provided path is valid and writable
func ValidatePath(path string) error {
	if path == "" {
		return nil // Empty path is valid (will use current directory)
	}

	// Resolve relative paths
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path '%s': %v", path, err)
	}

	// Check if path exists
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Path doesn't exist, check if parent directory exists and is writable
			parentDir := filepath.Dir(absPath)
			parentInfo, parentErr := os.Stat(parentDir)
			if parentErr != nil {
				return fmt.Errorf("parent directory '%s' does not exist", parentDir)
			}
			if !parentInfo.IsDir() {
				return fmt.Errorf("parent path '%s' is not a directory", parentDir)
			}
			// Check write permission on parent directory
			return checkWritePermission(parentDir)
		}
		return fmt.Errorf("cannot access path '%s': %v", path, err)
	}

	// If path exists and is a directory, check write permission
	if info.IsDir() {
		return checkWritePermission(absPath)
	}

	// If path exists and is a file, check write permission on parent directory
	parentDir := filepath.Dir(absPath)
	return checkWritePermission(parentDir)
}

// checkWritePermission checks if the directory is writable
func checkWritePermission(dir string) error {
	// Try to create a temporary file to test write permission
	tempFile := filepath.Join(dir, ".durable-resume-write-test")
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("directory '%s' is not writable: %v", dir, err)
	}
	file.Close()
	os.Remove(tempFile) // Clean up
	return nil
}

// ValidateSegmentParams validates segment-related parameters
func ValidateSegmentParams(segments int, segmentSize int64, noSegments bool) error {
	if noSegments {
		return nil // No validation needed when segments are disabled
	}

	if segments < 1 {
		return fmt.Errorf("segment count must be at least 1, got %d\n\nSuggestion: Use --segments 4 for optimal performance", segments)
	}

	if segments > 32 {
		return fmt.Errorf("segment count too high: %d\n\nUsing too many segments may hurt performance.\nSuggestion: Use --segments 8 or fewer", segments)
	}

	if segmentSize < 0 {
		return fmt.Errorf("segment size cannot be negative: %d", segmentSize)
	}

	if segmentSize > 0 && segmentSize < 1024 {
		return fmt.Errorf("segment size too small: %d bytes\n\nMinimum recommended size: 1024 bytes (1KB)", segmentSize)
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
			return "", fmt.Errorf("cannot determine filename from URL '%s'\n\nPlease specify a filename using --name flag", url)
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
				return "", fmt.Errorf("cannot determine filename from URL '%s'\n\nPlease specify a filename using --name flag", url)
			}
		}
		return filepath.Join(output, filename), nil
	}

	// Case 3: Output is a file path (existing file or new file path)
	if name != "" {
		return "", fmt.Errorf("cannot specify both file path (%s) and filename (%s)\n\nUse either:\n  --output /path/to/file.ext\n  --output /path/to/dir --name file.ext", output, name)
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

// ToDownloadConfig converts CLIArgs to DownloadConfig with validation and resolution
func (args *CLIArgs) ToDownloadConfig() (*DownloadConfig, error) {
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