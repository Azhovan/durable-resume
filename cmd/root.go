package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newRoot() *cobra.Command {
	var rootCmd = &cobra.Command{
		Short: "A robust solution for downloading files over the internet",
		Use:   "dr [URL] [options]",
		Long: `A robust solution for downloading files over the internet.

Examples:
  dr https://example.com/file.zip
  dr https://example.com/file.zip -o /path/to/directory
  dr https://example.com/file.zip -o /path/to/file.zip
  dr https://example.com/file.zip --segments 8`,
		Args: cobra.ArbitraryArgs, // Allow any number of arguments for flexibility
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check for legacy "download" subcommand usage
			if len(args) > 0 && args[0] == "download" {
				return handleLegacyDownloadCommand(cmd, args[1:])
			}

			// Parse arguments to determine if this is a direct URL invocation
			cliArgs, err := parseRootArgs(cmd, args)
			if err != nil {
				return err
			}

			// If no URL was provided (neither positional nor flag), show help
			if cliArgs.URL == "" {
				return cmd.Help()
			}

			// Convert to download config and execute
			config, err := cliArgs.ToDownloadConfig()
			if err != nil {
				return err
			}

			// Execute the download using the existing download logic
			return executeDownload(config)
		},
	}

	// Add flags for the new interface
	addRootFlags(rootCmd)

	// Keep the legacy download subcommand for backward compatibility
	rootCmd.AddCommand(newDownloadCmd(os.Stdout))

	return rootCmd
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

	// Parse positional arguments
	if len(args) > 0 {
		// Check for legacy "download" subcommand
		if args[0] == "download" {
			cliArgs.Legacy = true
			// Don't process further positional args for legacy mode
		} else {
			// First positional argument should be the URL
			firstArg := args[0]
			
			// Validate that the first argument looks like a URL
			if err := ValidateURL(firstArg); err == nil {
				cliArgs.URL = firstArg
			} else {
				return nil, fmt.Errorf("first argument '%s' is not a valid URL: %v", firstArg, err)
			}

			// Second positional argument (if present) should be output path
			if len(args) > 1 {
				cliArgs.Output = args[1]
			}

			// If there are more than 2 positional arguments, that's an error
			if len(args) > 2 {
				return nil, fmt.Errorf("too many positional arguments: expected URL and optional output path, got %d arguments", len(args))
			}
		}
	}

	// Parse flags (these can override positional arguments)
	if cmd.Flag("url").Changed {
		flagURL, _ := cmd.Flags().GetString("url")
		if cliArgs.URL != "" && cliArgs.URL != flagURL {
			return nil, fmt.Errorf("URL specified both as positional argument (%s) and flag (%s)", cliArgs.URL, flagURL)
		}
		cliArgs.URL = flagURL
	}

	if cmd.Flag("output").Changed {
		flagOutput, _ := cmd.Flags().GetString("output")
		if cliArgs.Output != "" && cliArgs.Output != flagOutput {
			return nil, fmt.Errorf("output path specified both as positional argument (%s) and flag (%s)", cliArgs.Output, flagOutput)
		}
		cliArgs.Output = flagOutput
	}

	// Parse other flags
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

	return cliArgs, nil
}

// addRootFlags adds all the flags to the root command
func addRootFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("url", "u", "", "The remote file URL to download (alternative to positional argument)")
	cmd.Flags().StringP("output", "o", "", "Output path (file path or directory)")
	cmd.Flags().StringP("name", "n", "", "Filename (when output is a directory)")
	cmd.Flags().IntP("segments", "c", 4, "Number of download segments")
	cmd.Flags().Int64P("segment-size", "s", 0, "Size of each segment (0 for auto)")
	cmd.Flags().Bool("no-segments", false, "Disable segmented downloading")
	cmd.Flags().BoolP("quiet", "q", false, "Suppress progress output")
	cmd.Flags().BoolP("verbose", "v", false, "Enable verbose logging")
	cmd.Flags().BoolP("resume", "r", true, "Enable resume mode")
}

// executeDownload executes the download with the given configuration
// This is a placeholder that will be implemented in a later task
func executeDownload(config *DownloadConfig) error {
	// For now, just print the configuration to verify parsing works
	fmt.Printf("Download configuration:\n")
	fmt.Printf("  URL: %s\n", config.URL)
	fmt.Printf("  Output Path: %s\n", config.OutputPath)
	fmt.Printf("  Directory: %s\n", config.Directory)
	fmt.Printf("  Filename: %s\n", config.Filename)
	fmt.Printf("  Use Segments: %t\n", config.UseSegments)
	fmt.Printf("  Segment Count: %d\n", config.SegmentCount)
	fmt.Printf("  Segment Size: %d\n", config.SegmentSize)
	fmt.Printf("  Resume: %t\n", config.Resume)
	fmt.Printf("  Quiet: %t\n", config.Quiet)
	fmt.Printf("  Verbose: %t\n", config.Verbose)
	
	return fmt.Errorf("download execution not yet implemented - this is task 2, implementation will come in later tasks")
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