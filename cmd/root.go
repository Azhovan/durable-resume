package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/azhovan/durable-resume/pkg/download"
	"github.com/azhovan/durable-resume/pkg/logger"
	"github.com/spf13/cobra"
)

func newRoot() *cobra.Command {
	var rootCmd = &cobra.Command{
		Short: "A robust solution for downloading files over the internet",
		Use:   "dr [URL] [options]",
		Long: `Durable Resume - A robust solution for downloading files over the internet

Durable Resume provides fast, reliable file downloads with automatic resume capability,
parallel segmented downloading, and intelligent error handling. Perfect for downloading
large files over unreliable connections.

BASIC USAGE:
  dr <URL>                           Download file to current directory
  dr <URL> <output-path>             Download to specific location
  dr <URL> -o <directory>            Download to directory with original filename
  dr <URL> -o <file-path>            Download to specific file path

COMMON EXAMPLES:
  # Simple download to current directory
  dr https://example.com/file.zip

  # Download to specific directory
  dr https://example.com/file.zip -o ~/Downloads

  # Download with custom filename
  dr https://example.com/file.zip -o ~/Downloads -n myfile.zip

  # Download to specific file path
  dr https://example.com/file.zip -o ~/Downloads/myfile.zip

  # Fast download with more segments
  dr https://example.com/largefile.iso --segments 8

  # Single-threaded download (no segments)
  dr https://example.com/file.zip --no-segments

  # Quiet download (no progress output)
  dr https://example.com/file.zip --quiet

  # Verbose download with detailed logging
  dr https://example.com/file.zip --verbose

ADVANCED EXAMPLES:
  # Custom segment size for optimal performance
  dr https://example.com/file.zip --segments 4 --segment-size 10485760

  # Download with specific configuration
  dr https://example.com/file.zip -o ~/Downloads -c 8 -s 5242880 -v

  # Resume disabled (start fresh)
  dr https://example.com/file.zip --resume=false

For more detailed information about flags and options, see the flag descriptions below.`,
		Args: cobra.ArbitraryArgs, // Allow any number of arguments for flexibility
		RunE: func(cmd *cobra.Command, args []string) error {
			// Implement command resolution flow as designed
			return resolveAndExecuteCommand(cmd, args)
		},
	}

	// Add flags for the new interface
	addRootFlags(rootCmd)

	// Keep the legacy download subcommand for backward compatibility
	rootCmd.AddCommand(newDownloadCmd(os.Stdout))

	return rootCmd
}

// resolveAndExecuteCommand implements the command resolution flow as designed
func resolveAndExecuteCommand(cmd *cobra.Command, args []string) error {
	// Step 1: Check for legacy "download" subcommand usage
	if len(args) > 0 && args[0] == "download" {
		return handleLegacyDownloadCommand(cmd, args[1:])
	}

	// Step 2: Parse arguments to determine if this is a direct URL invocation
	cliArgs, err := parseRootArgs(cmd, args)
	if err != nil {
		return err
	}

	// Step 3: If no URL was provided (neither positional nor flag), show help
	if cliArgs.URL == "" {
		return cmd.Help()
	}

	// Step 4: Validate arguments at root level
	if err := validateRootArguments(cliArgs); err != nil {
		return err
	}

	// Step 5: Convert to download config and execute
	config, err := cliArgs.ToDownloadConfig()
	if err != nil {
		return err
	}

	// Step 6: Execute the download using the existing download logic
	return executeDownload(config)
}

// validateRootArguments performs comprehensive validation at the root level
func validateRootArguments(args *CLIArgs) error {
	// Validate URL format and accessibility
	if err := ValidateURL(args.URL); err != nil {
		return err
	}

	// Validate flag combinations
	if err := ValidateFlagCombinations(args); err != nil {
		return err
	}

	// Validate segment parameters
	if err := ValidateSegmentParams(args.Segments, args.SegmentSize, args.NoSegments); err != nil {
		return err
	}

	// Validate output path if specified
	if args.Output != "" {
		if err := ValidatePath(args.Output); err != nil {
			return err
		}
	}

	return nil
}

func Execute() error {
	return newRoot().Execute()
}
// handleLegacyDownloadCommand handles the legacy "download" subcommand with deprecation warning
func handleLegacyDownloadCommand(cmd *cobra.Command, args []string) error {
	// Parse legacy arguments into new format
	legacyArgs, err := parseLegacyArgs(args)
	if err != nil {
		return err
	}

	// Show deprecation warning with migration suggestion
	showDeprecationWarning(legacyArgs)

	// Convert to download config and execute
	config, err := legacyArgs.ToDownloadConfig()
	if err != nil {
		return err
	}

	// Execute the download using the existing download logic
	return executeDownload(config)
}

// parseLegacyArgs parses arguments from the legacy "download" subcommand format
func parseLegacyArgs(args []string) (*CLIArgs, error) {
	cliArgs := NewCLIArgs()
	cliArgs.Legacy = true

	// Parse legacy flags manually since we're not using the subcommand
	i := 0
	for i < len(args) {
		arg := args[i]
		
		switch arg {
		case "--url", "-u":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			cliArgs.URL = args[i+1]
			i += 2
		case "--output", "-o", "--out":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			cliArgs.Output = args[i+1]
			i += 2
		case "--name", "-n", "--file":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			cliArgs.Name = args[i+1]
			i += 2
		case "--segments", "-c", "--segment-count":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			var err error
			cliArgs.Segments, err = parseInt(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid value for %s: %v", arg, err)
			}
			i += 2
		case "--segment-size", "-s":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", arg)
			}
			var err error
			cliArgs.SegmentSize, err = parseInt64(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid value for %s: %v", arg, err)
			}
			i += 2
		default:
			return nil, fmt.Errorf("unknown flag: %s", arg)
		}
	}

	return cliArgs, nil
}

// showDeprecationWarning displays a deprecation warning with migration suggestions
func showDeprecationWarning(args *CLIArgs) {
	fmt.Fprintf(os.Stderr, "\n⚠️  DEPRECATION WARNING: The 'download' subcommand is deprecated and will be removed in a future version.\n\n")
	
	// Generate the new command syntax
	newCommand := generateNewCommandSyntax(args)
	
	fmt.Fprintf(os.Stderr, "Please use the new simplified syntax instead:\n")
	fmt.Fprintf(os.Stderr, "  %s\n\n", newCommand)
	
	fmt.Fprintf(os.Stderr, "The new syntax is shorter and more intuitive. For more information, run: dr --help\n\n")
}

// generateNewCommandSyntax generates the equivalent new command syntax from legacy args
func generateNewCommandSyntax(args *CLIArgs) string {
	if args.URL == "" {
		return "dr <URL> [options]"
	}

	newCmd := fmt.Sprintf("dr %s", args.URL)
	
	if args.Output != "" {
		newCmd += fmt.Sprintf(" -o %s", args.Output)
	}
	
	if args.Name != "" {
		newCmd += fmt.Sprintf(" -n %s", args.Name)
	}
	
	if args.Segments != 4 { // 4 is the default
		newCmd += fmt.Sprintf(" -c %d", args.Segments)
	}
	
	if args.SegmentSize != 0 {
		newCmd += fmt.Sprintf(" -s %d", args.SegmentSize)
	}
	
	return newCmd
}

// parseRootArgs parses command line arguments for the root command
// Handles both positional URL arguments and flag-based arguments
func parseRootArgs(cmd *cobra.Command, args []string) (*CLIArgs, error) {
	cliArgs := NewCLIArgs()

	// Parse positional arguments first
	if err := parsePositionalArguments(cliArgs, args); err != nil {
		return nil, err
	}

	// Parse flags (these can override positional arguments with validation)
	if err := parseFlagArguments(cliArgs, cmd); err != nil {
		return nil, err
	}

	return cliArgs, nil
}

// parsePositionalArguments handles positional argument parsing with proper validation
func parsePositionalArguments(cliArgs *CLIArgs, args []string) error {
	if len(args) == 0 {
		return nil // No positional arguments
	}

	// Check for legacy "download" subcommand
	if args[0] == "download" {
		cliArgs.Legacy = true
		return nil // Don't process further positional args for legacy mode
	}

	// First positional argument should be the URL
	firstArg := args[0]
	
	// Basic URL format check for test compatibility
	if firstArg == "" {
		return fmt.Errorf("first argument '%s' is not a valid URL", firstArg)
	}
	
	// Basic validation to catch obviously invalid URLs for test compatibility
	if !isLikelyURL(firstArg) {
		return fmt.Errorf("first argument '%s' is not a valid URL", firstArg)
	}
	
	cliArgs.URL = firstArg

	// Second positional argument (if present) should be output path
	if len(args) > 1 {
		cliArgs.Output = args[1]
	}

	// If there are more than 2 positional arguments, that's an error
	if len(args) > 2 {
		return fmt.Errorf("too many positional arguments: expected URL and optional output path, got %d arguments\n\nUsage:\n  dr <URL>\n  dr <URL> <output-path>\n  dr <URL> [flags]", len(args))
	}

	return nil
}

// parseFlagArguments handles flag parsing with conflict detection
func parseFlagArguments(cliArgs *CLIArgs, cmd *cobra.Command) error {
	// Parse URL flag with conflict detection
	if cmd.Flag("url").Changed {
		flagURL, _ := cmd.Flags().GetString("url")
		if cliArgs.URL != "" && cliArgs.URL != flagURL {
			return fmt.Errorf("URL specified both as positional argument (%s) and --url flag (%s)\n\nChoose one:\n  dr %s [flags]\n  dr --url %s [flags]", cliArgs.URL, flagURL, cliArgs.URL, flagURL)
		}
		cliArgs.URL = flagURL
	}

	// Parse output flag with conflict detection
	if cmd.Flag("output").Changed {
		flagOutput, _ := cmd.Flags().GetString("output")
		if cliArgs.Output != "" && cliArgs.Output != flagOutput {
			return fmt.Errorf("output path specified both as positional argument (%s) and --output flag (%s)\n\nChoose one:\n  dr %s %s\n  dr %s --output %s", cliArgs.Output, flagOutput, cliArgs.URL, cliArgs.Output, cliArgs.URL, flagOutput)
		}
		cliArgs.Output = flagOutput
	}

	// Parse other flags without conflicts
	if cmd.Flag("name").Changed {
		cliArgs.Name, _ = cmd.Flags().GetString("name")
	}

	if cmd.Flag("segments").Changed {
		cliArgs.Segments, _ = cmd.Flags().GetInt("segments")
	}

	if cmd.Flag("segment-size").Changed {
		cliArgs.SegmentSize, _ = cmd.Flags().GetInt64("segment-size")
	}

	if cmd.Flag("no-segments").Changed {
		cliArgs.NoSegments, _ = cmd.Flags().GetBool("no-segments")
	}

	if cmd.Flag("quiet").Changed {
		cliArgs.Quiet, _ = cmd.Flags().GetBool("quiet")
	}

	if cmd.Flag("verbose").Changed {
		cliArgs.Verbose, _ = cmd.Flags().GetBool("verbose")
	}

	if cmd.Flag("resume").Changed {
		cliArgs.Resume, _ = cmd.Flags().GetBool("resume")
	}

	return nil
}

// isLikelyURL performs a basic check to see if a string looks like a URL
func isLikelyURL(s string) bool {
	// Basic checks for URL-like format
	if s == "" {
		return false
	}
	
	// Must contain protocol separator
	if !strings.Contains(s, "://") {
		return false
	}
	
	// Must have content after protocol
	parts := strings.SplitN(s, "://", 2)
	if len(parts) != 2 || parts[1] == "" {
		return false
	}
	
	// Check for supported protocols
	protocol := strings.ToLower(parts[0])
	supportedProtocols := []string{"http", "https", "ftp", "ftps"}
	for _, supported := range supportedProtocols {
		if protocol == supported {
			return true
		}
	}
	
	return false
}

// addRootFlags adds all the flags to the root command
func addRootFlags(cmd *cobra.Command) {
	// Core flags
	cmd.Flags().StringP("url", "u", "", 
		"Remote file URL to download (supports HTTP, HTTPS, FTP, FTPS)")
	
	cmd.Flags().StringP("output", "o", "", 
		"Output path - directory or file path (default: current directory)")
	
	cmd.Flags().StringP("name", "n", "", 
		"Filename when output is a directory (default: extracted from URL)")

	// Segmentation flags
	cmd.Flags().IntP("segments", "c", 4, 
		"Number of parallel download segments (1-32, recommended: 4-8)")
	
	cmd.Flags().Int64P("segment-size", "s", 0, 
		"Size of each segment in bytes (0 for auto-calculation)")
	
	cmd.Flags().Bool("no-segments", false, 
		"Disable segmented downloading (single-threaded)")

	// Behavior flags
	cmd.Flags().BoolP("quiet", "q", false, 
		"Suppress progress output (useful for scripting)")
	
	cmd.Flags().BoolP("verbose", "v", false, 
		"Enable verbose logging with detailed information")
	
	cmd.Flags().BoolP("resume", "r", true, 
		"Enable resume mode for interrupted downloads")

	// Preserve our custom flag order
	cmd.Flags().SortFlags = false
}

// executeDownload executes the download with the given configuration
func executeDownload(config *DownloadConfig) error {
	// Show download start status with configuration summary (unless quiet mode)
	if !config.Quiet {
		showDownloadStartStatus(config)
	}

	// Create logger based on verbosity settings
	logger := createLogger(config)

	// Log detailed configuration in verbose mode
	if config.Verbose {
		logVerboseConfiguration(logger, config)
	}

	// Create downloader with appropriate options and enhanced error handling
	downloader, err := download.NewDownloader(
		config.Directory,
		config.URL,
		download.WithFileName(config.Filename),
		download.WithLogger(logger),
	)
	if err != nil {
		return enhanceDownloaderCreationError(err, config)
	}

	// Create download manager with retry policy and enhanced mode options
	dm := download.NewDownloadManager(
		downloader, 
		download.DefaultRetryPolicy(),
		download.WithQuietMode(config.Quiet),
		download.WithVerboseMode(config.Verbose),
	)
	
	// Validate download manager configuration
	if dm == nil {
		return fmt.Errorf("Error: Failed to create download manager\n\nThis is an internal error. Please try again or report this issue.")
	}

	// Create context for cancellation support
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Configure segment options based on the resolved configuration
	var segmentOptions []download.SegmentManagerOption
	
	if config.UseSegments {
		// Use segmented downloading with specified parameters
		if config.SegmentSize > 0 {
			segmentOptions = append(segmentOptions, download.WithSegmentSize(config.SegmentSize))
		}
		if config.SegmentCount > 0 {
			segmentOptions = append(segmentOptions, download.WithNumberOfSegments(config.SegmentCount))
		}
	} else {
		// Force single-threaded download by setting segment count to 1
		segmentOptions = append(segmentOptions, download.WithNumberOfSegments(1))
	}

	// Start download in a goroutine with enhanced error handling
	downloadDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				downloadDone <- fmt.Errorf("download panic recovered: %v", r)
			}
		}()
		downloadDone <- dm.Download(ctx, segmentOptions...)
	}()

	// Wait for either download completion or interrupt signal
	select {
	case err := <-downloadDone:
		// Download completed (successfully or with error)
		if err != nil {
			enhancedErr := enhanceDownloadError(err, config)
			showDownloadError(config, enhancedErr)
			return enhancedErr
		}
		showDownloadSuccess(config)
		return nil

	case sig := <-sigChan:
		// User interrupted the download
		showDownloadInterrupted(config, sig)
		cancel() // Cancel the download context
		
		// Wait a moment for graceful cleanup with timeout
		cleanupTimeout := 3 * time.Second
		if config.Verbose {
			cleanupTimeout = 5 * time.Second // More time for verbose logging
		}
		
		select {
		case err := <-downloadDone:
			if err != nil && err != context.Canceled {
				if config.Verbose {
					logger.Error("Download stopped with error", "error", err)
				}
			} else if config.Verbose {
				logger.Info("Download stopped cleanly by user")
			}
		case <-time.After(cleanupTimeout):
			if config.Verbose {
				logger.Info("Download cleanup completed with timeout")
			}
		}
		return fmt.Errorf("download interrupted by user")
	}
}

// Helper functions for parsing integers
func parseInt(s string) (int, error) {
	var result int
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

func parseInt64(s string) (int64, error) {
	var result int64
	_, err := fmt.Sscanf(s, "%d", &result)
	return result, err
}

// showDownloadStartStatus displays the download start status with configuration summary
func showDownloadStartStatus(config *DownloadConfig) {
	fmt.Printf("Starting download...\n")
	fmt.Printf("  URL: %s\n", config.URL)
	fmt.Printf("  Output: %s\n", config.OutputPath)
	
	if config.UseSegments {
		fmt.Printf("  Segments: %d", config.SegmentCount)
		if config.SegmentSize > 0 {
			fmt.Printf(" (size: %s each)", formatBytes(uint64(config.SegmentSize)))
		}
		fmt.Printf("\n")
	} else {
		fmt.Printf("  Mode: Single-threaded download\n")
	}
	
	if config.Resume {
		fmt.Printf("  Resume: Enabled\n")
	}
	
	fmt.Printf("\n")
}

// createLogger creates an appropriate logger based on configuration
func createLogger(config *DownloadConfig) *slog.Logger {
	if config.Quiet {
		// In quiet mode, create a logger that writes to a discard writer
		return logger.NewLogger(io.Discard, &slog.HandlerOptions{
			Level: slog.LevelError, // Only log errors in quiet mode
		})
	}
	
	if config.Verbose {
		// In verbose mode, create a logger with debug level
		return logger.NewLogger(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	}
	
	// Default logger for normal mode
	return logger.NewLogger(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
}

// logVerboseConfiguration logs detailed configuration information in verbose mode
func logVerboseConfiguration(logger *slog.Logger, config *DownloadConfig) {
	logger.Info("Download configuration details",
		slog.Group("source",
			slog.String("url", config.URL),
		),
		slog.Group("destination",
			slog.String("output_path", config.OutputPath),
			slog.String("directory", config.Directory),
			slog.String("filename", config.Filename),
		),
		slog.Group("segmentation",
			slog.Bool("use_segments", config.UseSegments),
			slog.Int("segment_count", config.SegmentCount),
			slog.Int64("segment_size", config.SegmentSize),
		),
		slog.Group("behavior",
			slog.Bool("resume", config.Resume),
			slog.Bool("quiet", config.Quiet),
			slog.Bool("verbose", config.Verbose),
		),
	)
}

// showDownloadSuccess displays success message with file location
func showDownloadSuccess(config *DownloadConfig) {
	if config.Quiet {
		return // Don't show success message in quiet mode
	}
	
	fmt.Printf("\n✅ Download completed successfully!\n")
	
	// Get absolute path for clearer output
	absPath, err := filepath.Abs(config.OutputPath)
	if err != nil {
		absPath = config.OutputPath
	}
	
	fmt.Printf("📁 File saved to: %s\n", absPath)
	
	// Show file size if we can get it
	if info, err := os.Stat(config.OutputPath); err == nil {
		fmt.Printf("📊 File size: %s\n", formatBytes(uint64(info.Size())))
	}
	
	fmt.Printf("\n")
}

// showDownloadError displays error message with helpful information
func showDownloadError(config *DownloadConfig, err error) {
	if config.Quiet {
		return // Don't show error details in quiet mode, just let the error propagate
	}
	
	// If the error is already enhanced (contains "Error:" prefix), show it directly
	errStr := err.Error()
	if strings.HasPrefix(errStr, "Error:") {
		fmt.Fprintf(os.Stderr, "\n❌ %v\n", err)
	} else {
		// Legacy error format
		fmt.Fprintf(os.Stderr, "\n❌ Download failed: %v\n", err)
	}
	
	// Show additional details in verbose mode
	if config.Verbose && !strings.Contains(errStr, "Download details:") {
		fmt.Fprintf(os.Stderr, "\nDownload details:\n")
		fmt.Fprintf(os.Stderr, "  URL: %s\n", config.URL)
		fmt.Fprintf(os.Stderr, "  Output: %s\n", config.OutputPath)
		fmt.Fprintf(os.Stderr, "  Segments: %d\n", config.SegmentCount)
		fmt.Fprintf(os.Stderr, "  Resume: %t\n", config.Resume)
		fmt.Fprintf(os.Stderr, "  Use segments: %t\n", config.UseSegments)
		if config.SegmentSize > 0 {
			fmt.Fprintf(os.Stderr, "  Segment size: %s\n", formatBytes(uint64(config.SegmentSize)))
		}
	}
	
	// Show tip only if not already included in enhanced error
	if !strings.Contains(errStr, "verbose") && !config.Verbose {
		fmt.Fprintf(os.Stderr, "\nTip: Try running with --verbose for more detailed error information.\n")
	}
	
	fmt.Fprintf(os.Stderr, "\n")
}

// showDownloadInterrupted displays interruption message
func showDownloadInterrupted(config *DownloadConfig, sig os.Signal) {
	if config.Quiet {
		return
	}
	
	fmt.Printf("\n⏸️  Download interrupted by %v signal\n", sig)
	
	if config.Resume {
		fmt.Printf("💡 You can resume this download later by running the same command.\n")
	}
	
	fmt.Printf("\n")
}

// enhanceDownloaderCreationError provides more actionable error messages for downloader creation failures
func enhanceDownloaderCreationError(err error, config *DownloadConfig) error {
	errStr := err.Error()
	
	// Handle common URL parsing errors
	if strings.Contains(errStr, "parse") && strings.Contains(errStr, "url") {
		return fmt.Errorf("Error: Invalid URL format\n\nURL: %s\nReason: %v\n\nPlease check that the URL is correctly formatted.\n\nExamples:\n  https://example.com/file.zip\n  ftp://files.example.com/data.tar.gz", config.URL, err)
	}
	
	// Handle directory/path related errors
	if strings.Contains(errStr, "directory") || strings.Contains(errStr, "path") {
		return fmt.Errorf("Error: Invalid output directory\n\nDirectory: %s\nReason: %v\n\nPlease ensure the directory exists and is writable.\n\nSolutions:\n  - Create the directory: mkdir -p %s\n  - Choose a different directory\n  - Check directory permissions", config.Directory, err, config.Directory)
	}
	
	// Handle permission errors
	if strings.Contains(errStr, "permission") {
		return fmt.Errorf("Error: Permission denied\n\nDirectory: %s\nReason: %v\n\nYou don't have permission to write to this directory.\n\nSolutions:\n  - Choose a different directory (e.g., your home directory)\n  - Run with appropriate permissions\n  - Check directory ownership", config.Directory, err)
	}
	
	// Generic enhanced error with context
	return fmt.Errorf("Error: Failed to initialize download\n\nURL: %s\nOutput: %s\nReason: %v\n\nPlease check your URL and output path, then try again.", config.URL, config.OutputPath, err)
}

// enhanceDownloadError provides more actionable error messages for download failures
func enhanceDownloadError(err error, config *DownloadConfig) error {
	if err == nil {
		return nil
	}
	
	errStr := err.Error()
	
	// Handle network-related errors
	if strings.Contains(errStr, "connection refused") {
		return fmt.Errorf("Error: Connection refused\n\nURL: %s\n\nThe server is not accepting connections.\n\nSolutions:\n  - Check if the URL is correct\n  - Verify the server is online\n  - Try again later\n  - Check your internet connection", config.URL)
	}
	
	if strings.Contains(errStr, "timeout") {
		return fmt.Errorf("Error: Connection timeout\n\nURL: %s\n\nThe server is not responding within the expected time.\n\nSolutions:\n  - Check your internet connection\n  - Try again later\n  - The server might be overloaded", config.URL)
	}
	
	if strings.Contains(errStr, "no such host") {
		return fmt.Errorf("Error: Host not found\n\nURL: %s\n\nThe hostname cannot be resolved.\n\nSolutions:\n  - Check if the URL is spelled correctly\n  - Verify your DNS settings\n  - Check your internet connection", config.URL)
	}
	
	// Handle HTTP status errors
	if strings.Contains(errStr, "404") || strings.Contains(errStr, "not found") {
		return fmt.Errorf("Error: File not found (404)\n\nURL: %s\n\nThe requested file does not exist on the server.\n\nSolutions:\n  - Check if the URL is correct\n  - Verify the file still exists\n  - Contact the file provider", config.URL)
	}
	
	if strings.Contains(errStr, "403") || strings.Contains(errStr, "forbidden") {
		return fmt.Errorf("Error: Access forbidden (403)\n\nURL: %s\n\nYou don't have permission to access this file.\n\nSolutions:\n  - Check if authentication is required\n  - Verify you have permission to access the file\n  - Contact the file provider", config.URL)
	}
	
	if strings.Contains(errStr, "401") || strings.Contains(errStr, "unauthorized") {
		return fmt.Errorf("Error: Authentication required (401)\n\nURL: %s\n\nThe server requires authentication to access this file.\n\nSolutions:\n  - Check if you need to log in\n  - Verify your credentials\n  - Contact the file provider for access", config.URL)
	}
	
	// Handle disk space errors
	if strings.Contains(errStr, "no space left") || strings.Contains(errStr, "disk full") {
		return fmt.Errorf("Error: Insufficient disk space\n\nOutput: %s\n\nThere is not enough free space to complete the download.\n\nSolutions:\n  - Free up disk space\n  - Choose a different output location\n  - Delete unnecessary files", config.OutputPath)
	}
	
	// Handle permission errors during download
	if strings.Contains(errStr, "permission denied") {
		return fmt.Errorf("Error: Permission denied during download\n\nOutput: %s\n\nCannot write to the output location.\n\nSolutions:\n  - Check file/directory permissions\n  - Choose a different output location\n  - Run with appropriate permissions", config.OutputPath)
	}
	
	// Handle range request errors
	if strings.Contains(errStr, "range") && strings.Contains(errStr, "not supported") {
		return fmt.Errorf("Error: Server doesn't support resume/segmented downloads\n\nURL: %s\n\nThe server doesn't support partial downloads.\n\nSolutions:\n  - Try with --no-segments flag for single-threaded download\n  - The download will work but cannot be resumed if interrupted", config.URL)
	}
	
	// Handle segment-related errors
	if strings.Contains(errStr, "segment") {
		return fmt.Errorf("Error: Segmented download failed\n\nURL: %s\nSegments: %d\n\nOne or more download segments failed.\n\nSolutions:\n  - Try with fewer segments: --segments %d\n  - Try single-threaded download: --no-segments\n  - Check your internet connection\n\nOriginal error: %v", config.URL, config.SegmentCount, max(1, config.SegmentCount/2), err)
	}
	
	// Generic enhanced error with context
	return fmt.Errorf("Error: Download failed\n\nURL: %s\nOutput: %s\nSegments: %d\n\nOriginal error: %v\n\nSolutions:\n  - Check your internet connection\n  - Verify the URL is accessible\n  - Try again later\n  - Use --verbose for more detailed error information", config.URL, config.OutputPath, config.SegmentCount, err)
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}