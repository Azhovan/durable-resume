package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

type downloadOptions struct {
	remoteURL string

	segSize  int64
	segCount int

	dstDIR   string
	filename string
}

func newDownloadCmd(output io.Writer) *cobra.Command {
	var opts = &downloadOptions{}

	var cmd = &cobra.Command{
		Use:   "download --url [ADDRESS] --output [DIRECTORY]",
		Short: "download remote file and store it in a local directory (DEPRECATED)",
		Long: `DEPRECATED: This subcommand is deprecated and will be removed in a future version.

Please use the new simplified syntax instead:
  dr <URL> [options]

Examples of new syntax:
  dr https://example.com/file.zip
  dr https://example.com/file.zip -o ~/Downloads
  dr https://example.com/file.zip -o ~/Downloads -n myfile.zip

The new syntax is shorter, more intuitive, and provides the same functionality.
For full help on the new interface, run: dr --help

This legacy command still works for backward compatibility but will show
deprecation warnings during execution.`,
		Args:  cobra.MaximumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Convert legacy options to new CLIArgs structure
			cliArgs := convertLegacyOptionsToArgs(opts)
			
			// Show deprecation warning when subcommand is used directly
			showSubcommandDeprecationWarning(opts)
			
			// Use the enhanced validation and error handling from the new system
			config, err := cliArgs.ToDownloadConfig()
			if err != nil {
				return err
			}
			
			// Execute using the unified download logic with enhanced features
			return executeDownload(config)
		},
	}

	cmd.Flags().StringVarP(&opts.remoteURL, "url", "u", "", "The remote file address to download.")
	cmd.Flags().StringVarP(&opts.dstDIR, "output", "o", "", "The local file target directory to save file.")
	cmd.Flags().Int64VarP(&opts.segSize, "segment-size", "s", 0, "The size of each segment for download a file.")
	cmd.Flags().IntVarP(&opts.segCount, "segments", "c", 4, "The number of segments for download a file.")
	cmd.Flags().StringVarP(&opts.filename, "name", "n", "", "The downloaded file name")

	return cmd
}

// convertLegacyOptionsToArgs converts legacy downloadOptions to new CLIArgs structure
func convertLegacyOptionsToArgs(opts *downloadOptions) *CLIArgs {
	cliArgs := NewCLIArgs()
	cliArgs.URL = opts.remoteURL
	cliArgs.Output = opts.dstDIR
	cliArgs.Name = opts.filename
	cliArgs.Segments = opts.segCount
	cliArgs.SegmentSize = opts.segSize
	cliArgs.Legacy = true
	
	// Legacy subcommand doesn't support quiet/verbose modes, use defaults
	cliArgs.Quiet = false
	cliArgs.Verbose = false
	cliArgs.Resume = true // Default behavior
	
	return cliArgs
}

// showSubcommandDeprecationWarning shows deprecation warning for direct subcommand usage
func showSubcommandDeprecationWarning(opts *downloadOptions) {
	fmt.Fprintf(os.Stderr, "\n⚠️  DEPRECATION WARNING: The 'download' subcommand is deprecated and will be removed in a future version.\n\n")
	
	// Generate the new command syntax from the options
	newCommand := generateNewSyntaxFromOptions(opts)
	
	fmt.Fprintf(os.Stderr, "Please use the new simplified syntax instead:\n")
	fmt.Fprintf(os.Stderr, "  %s\n\n", newCommand)
	
	fmt.Fprintf(os.Stderr, "The new syntax is shorter and more intuitive. For more information, run: dr --help\n\n")
}

// generateNewSyntaxFromOptions generates new command syntax from downloadOptions
func generateNewSyntaxFromOptions(opts *downloadOptions) string {
	if opts.remoteURL == "" {
		return "dr <URL> [options]"
	}

	newCmd := fmt.Sprintf("dr %s", opts.remoteURL)
	
	if opts.dstDIR != "" {
		newCmd += fmt.Sprintf(" -o %s", opts.dstDIR)
	}
	
	if opts.filename != "" {
		newCmd += fmt.Sprintf(" -n %s", opts.filename)
	}
	
	if opts.segCount != 4 { // DefaultNumberOfSegments value
		newCmd += fmt.Sprintf(" -c %d", opts.segCount)
	}
	
	if opts.segSize != 0 {
		newCmd += fmt.Sprintf(" -s %d", opts.segSize)
	}
	
	return newCmd
}
