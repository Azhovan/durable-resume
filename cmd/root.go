// Package cmd wires the `dr <url> [flags]` command-line interface onto the
// download package. It validates the URL, parses flags into download.Options,
// installs SIGINT/SIGTERM handling, and invokes download.Run.
package cmd

import (
	"net/http"

	"github.com/azhovan/durable-resume/download"
	"github.com/spf13/cobra"
)

// NewRootCmd builds the single `dr <url> [flags]` command. version/revision/date
// come from main's ldflag vars and feed cobra's Version field (surfaced by --version).
func NewRootCmd(version, revision, date string) *cobra.Command {
	panic("not implemented")
}

// formatVersion renders the --version string from build info.
func formatVersion(version, revision, date string) string {
	panic("not implemented")
}

// parseHeaders converts repeatable "Key: Value" strings into an http.Header.
// Returns a wrapped error on a malformed entry (missing colon / empty key).
func parseHeaders(raw []string) (http.Header, error) {
	panic("not implemented")
}

// parseChecksum parses "sha256:<hex>" into a download.Checksum. The empty string
// yields the zero (empty) Checksum and a nil error. Errors on unknown algo or bad hex.
func parseChecksum(s string) (download.Checksum, error) {
	panic("not implemented")
}

// defaultOutputName derives the output filename from the URL path; falls back to
// "download" when the path is empty or ends in a slash.
func defaultOutputName(rawURL string) string {
	panic("not implemented")
}

// validateURL ensures rawURL parses and uses the http or https scheme.
// Returns download.ErrUnsupportedScheme (wrapped) otherwise.
func validateURL(rawURL string) error {
	panic("not implemented")
}
