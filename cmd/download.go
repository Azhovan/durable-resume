package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/azhovan/durable-resume/pkg/download"
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
		Args:  cobra.MaximumNArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show deprecation warning when subcommand is used directly
			showSubcommandDeprecationWarning(opts)
			
			// Continue with original logic
			src, err := url.ParseRequestURI(opts.remoteURL)
			if err != nil {
				return fmt.Errorf("invalid remote url: %v", err)
			}

			downloader, err := download.NewDownloader(
				opts.dstDIR,
				src.String(),
				download.WithFileName(opts.filename),
			)
			if err != nil {
				return err
			}

			dm := download.NewDownloadManager(downloader, download.DefaultRetryPolicy())

			// Create a context that can be cancelled on interrupt signals
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// Set up signal handling for graceful shutdown
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

			// Start download in a goroutine
			downloadDone := make(chan error, 1)
			go func() {
				downloadDone <- dm.Download(ctx, download.WithSegmentSize(opts.segSize), download.WithNumberOfSegments(opts.segCount))
			}()

			// Wait for either download completion or interrupt signal
			select {
			case err := <-downloadDone:
				// Download completed (successfully or with error)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nDownload failed: %v\n", err)
					return err
				}
				fmt.Println("\nDownload completed successfully!")
				return nil

			case sig := <-sigChan:
				// User interrupted the download
				fmt.Printf("\nReceived %v signal, stopping download...\n", sig)
				cancel() // Cancel the download context
				
				// Wait a moment for graceful cleanup
				select {
				case err := <-downloadDone:
					if err != nil && err != context.Canceled {
						fmt.Fprintf(os.Stderr, "Download stopped with error: %v\n", err)
					} else {
						fmt.Println("Download stopped by user.")
					}
				case <-cmd.Context().Done():
					fmt.Println("Download cleanup completed.")
				}
				return fmt.Errorf("download interrupted by user")
			}
		},
	}

	cmd.Flags().StringVarP(&opts.remoteURL, "url", "u", "", "The remote file address to download.")
	cmd.Flags().StringVarP(&opts.dstDIR, "output", "o", "", "The local file target directory to save file.")
	cmd.Flags().Int64VarP(&opts.segSize, "segment-size", "s", 0, "The size of each segment for download a file.")
	cmd.Flags().IntVarP(&opts.segCount, "segments", "c", download.DefaultNumberOfSegments, "The number of segments for download a file.")
	cmd.Flags().StringVarP(&opts.filename, "name", "n", "", "The downloaded file name")

	return cmd
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
	
	if opts.segCount != download.DefaultNumberOfSegments {
		newCmd += fmt.Sprintf(" -c %d", opts.segCount)
	}
	
	if opts.segSize != 0 {
		newCmd += fmt.Sprintf(" -s %d", opts.segSize)
	}
	
	return newCmd
}
