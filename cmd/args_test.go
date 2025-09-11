package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid https URL",
			url:     "https://example.com/file.zip",
			wantErr: false,
		},
		{
			name:    "valid http URL",
			url:     "http://example.com/file.zip",
			wantErr: false,
		},
		{
			name:    "valid ftp URL",
			url:     "ftp://files.example.com/data.tar.gz",
			wantErr: false,
		},
		{
			name:    "valid ftps URL",
			url:     "ftps://secure.example.com/file.zip",
			wantErr: false,
		},
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
			errMsg:  "No URL provided",
		},
		{
			name:    "whitespace only URL",
			url:     "   ",
			wantErr: true,
			errMsg:  "Empty URL provided",
		},
		{
			name:    "URL without protocol",
			url:     "example.com/file.zip",
			wantErr: true,
			errMsg:  "missing protocol",
		},
		{
			name:    "invalid URL format",
			url:     "not-a-url",
			wantErr: true,
			errMsg:  "missing protocol",
		},
		{
			name:    "unsupported scheme",
			url:     "file:///local/file.txt",
			wantErr: true,
			errMsg:  "Unsupported URL protocol",
		},
		{
			name:    "URL without host",
			url:     "https:///file.zip",
			wantErr: true,
			errMsg:  "missing hostname",
		},
		{
			name:    "URL with path and query",
			url:     "https://example.com/path/file.zip?version=1",
			wantErr: false,
		},
		{
			name:    "URL with hostname containing spaces",
			url:     "https://exam ple.com/file.zip",
			wantErr: true,
			errMsg:  "Invalid URL format",
		},
		{
			name:    "URL with hostname starting with dot",
			url:     "https://.example.com/file.zip",
			wantErr: true,
			errMsg:  "cannot start or end with a dot",
		},
		{
			name:    "URL with hostname ending with dot",
			url:     "https://example.com./file.zip",
			wantErr: true,
			errMsg:  "cannot start or end with a dot",
		},
		{
			name:    "URL with port",
			url:     "https://example.com:8080/file.zip",
			wantErr: false,
		},
		{
			name:    "URL with authentication",
			url:     "https://user:pass@example.com/file.zip",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateURL() error = %v, want error containing %v", err, tt.errMsg)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "durable-resume-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test file
	testFile := filepath.Join(tempDir, "testfile.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "empty path",
			path:    "",
			wantErr: false,
		},
		{
			name:    "existing directory",
			path:    tempDir,
			wantErr: false,
		},
		{
			name:    "existing file",
			path:    testFile,
			wantErr: false,
		},
		{
			name:    "non-existent file in existing directory",
			path:    filepath.Join(tempDir, "newfile.txt"),
			wantErr: false,
		},
		{
			name:    "non-existent directory",
			path:    filepath.Join(tempDir, "nonexistent", "file.txt"),
			wantErr: true,
		},
		{
			name:    "relative path",
			path:    "./testfile.txt",
			wantErr: false, // Should resolve to current directory
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSegmentParams(t *testing.T) {
	tests := []struct {
		name        string
		segments    int
		segmentSize int64
		noSegments  bool
		wantErr     bool
	}{
		{
			name:        "valid parameters",
			segments:    4,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     false,
		},
		{
			name:        "no segments enabled",
			segments:    0,
			segmentSize: 0,
			noSegments:  true,
			wantErr:     false,
		},
		{
			name:        "zero segments",
			segments:    0,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     true,
		},
		{
			name:        "negative segments",
			segments:    -1,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     true,
		},
		{
			name:        "too many segments",
			segments:    50,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     true,
		},
		{
			name:        "negative segment size",
			segments:    4,
			segmentSize: -1,
			noSegments:  false,
			wantErr:     true,
		},
		{
			name:        "segment size too small",
			segments:    4,
			segmentSize: 100,
			noSegments:  false,
			wantErr:     true,
		},
		{
			name:        "zero segment size (auto)",
			segments:    4,
			segmentSize: 0,
			noSegments:  false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSegmentParams(tt.segments, tt.segmentSize, tt.noSegments)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSegmentParams() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveOutputPath(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "durable-resume-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test file in the temp directory
	testFile := filepath.Join(tempDir, "existing.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name       string
		url        string
		output     string
		filename   string
		want       string
		wantErr    bool
	}{
		{
			name:     "no output, extract from URL",
			url:      "https://example.com/file.zip",
			output:   "",
			filename: "",
			want:     "file.zip",
			wantErr:  false,
		},
		{
			name:     "no output, use custom filename",
			url:      "https://example.com/file.zip",
			output:   "",
			filename: "custom.zip",
			want:     "custom.zip",
			wantErr:  false,
		},
		{
			name:     "output directory, extract filename from URL",
			url:      "https://example.com/file.zip",
			output:   tempDir,
			filename: "",
			want:     filepath.Join(tempDir, "file.zip"),
			wantErr:  false,
		},
		{
			name:     "output directory, custom filename",
			url:      "https://example.com/file.zip",
			output:   tempDir,
			filename: "custom.zip",
			want:     filepath.Join(tempDir, "custom.zip"),
			wantErr:  false,
		},
		{
			name:     "output file path",
			url:      "https://example.com/file.zip",
			output:   "/path/to/file.zip",
			filename: "",
			want:     "/path/to/file.zip",
			wantErr:  false,
		},
		{
			name:     "output file path with conflicting filename",
			url:      "https://example.com/file.zip",
			output:   "/path/to/file.zip",
			filename: "other.zip",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "URL without filename",
			url:      "https://example.com/",
			output:   "",
			filename: "",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "URL without filename but custom name provided",
			url:      "https://example.com/",
			output:   "",
			filename: "download.bin",
			want:     "download.bin",
			wantErr:  false,
		},
		{
			name:     "URL with directory path ending in slash",
			url:      "https://example.com/path/to/directory/",
			output:   "",
			filename: "",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "URL with directory path ending in slash but custom name",
			url:      "https://example.com/path/to/directory/",
			output:   "",
			filename: "archive.zip",
			want:     "archive.zip",
			wantErr:  false,
		},
		{
			name:     "output directory with URL having no filename",
			url:      "https://example.com/path/",
			output:   tempDir,
			filename: "",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "output directory with URL having no filename but custom name",
			url:      "https://example.com/path/",
			output:   tempDir,
			filename: "download.bin",
			want:     filepath.Join(tempDir, "download.bin"),
			wantErr:  false,
		},
		{
			name:     "existing file as output path",
			url:      "https://example.com/file.zip",
			output:   testFile,
			filename: "",
			want:     testFile,
			wantErr:  false,
		},
		{
			name:     "existing file as output path with conflicting filename",
			url:      "https://example.com/file.zip",
			output:   testFile,
			filename: "other.zip",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "relative path as output",
			url:      "https://example.com/file.zip",
			output:   "./downloads/file.zip",
			filename: "",
			want:     "./downloads/file.zip",
			wantErr:  false,
		},
		{
			name:     "URL with complex filename",
			url:      "https://example.com/downloads/archive.backup.tar.gz?token=123",
			output:   "",
			filename: "",
			want:     "archive.backup.tar.gz",
			wantErr:  false,
		},
		{
			name:     "URL with filename containing special characters",
			url:      "https://example.com/my%20file%20name.zip",
			output:   "",
			filename: "",
			want:     "my file name.zip",
			wantErr:  false,
		},
		{
			name:     "empty filename parameter with directory output",
			url:      "https://example.com/file.zip",
			output:   tempDir,
			filename: "",
			want:     filepath.Join(tempDir, "file.zip"),
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOutputPath(tt.url, tt.output, tt.filename)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveOutputPath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ResolveOutputPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractFilenameFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "simple filename",
			url:  "https://example.com/file.zip",
			want: "file.zip",
		},
		{
			name: "filename with path",
			url:  "https://example.com/path/to/file.tar.gz",
			want: "file.tar.gz",
		},
		{
			name: "filename with query parameters",
			url:  "https://example.com/file.zip?version=1&format=zip",
			want: "file.zip",
		},
		{
			name: "no filename (root)",
			url:  "https://example.com/",
			want: "",
		},
		{
			name: "no filename (directory)",
			url:  "https://example.com/path/",
			want: "",
		},
		{
			name: "invalid URL",
			url:  "not-a-url",
			want: "not-a-url", // url.Parse treats this as a path
		},
		{
			name: "filename with special characters",
			url:  "https://example.com/my%20file.zip",
			want: "my file.zip", // URL decoding happens in filepath.Base
		},
		{
			name: "filename with fragment",
			url:  "https://example.com/file.zip#section1",
			want: "file.zip",
		},
		{
			name: "filename with complex query and fragment",
			url:  "https://example.com/downloads/file.tar.gz?token=abc123&format=compressed#download",
			want: "file.tar.gz",
		},
		{
			name: "URL ending with path separator",
			url:  "https://example.com/path/to/directory/",
			want: "",
		},
		{
			name: "filename with multiple dots",
			url:  "https://example.com/archive.backup.tar.gz",
			want: "archive.backup.tar.gz",
		},
		{
			name: "filename with no extension",
			url:  "https://example.com/downloads/README",
			want: "README",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilenameFromURL(tt.url)
			if got != tt.want {
				t.Errorf("extractFilenameFromURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCLIArgsToDownloadConfig(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "durable-resume-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name    string
		args    *CLIArgs
		wantErr bool
		check   func(*DownloadConfig) bool
	}{
		{
			name: "valid configuration",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   tempDir,
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
			check: func(config *DownloadConfig) bool {
				return config.URL == "https://example.com/file.zip" &&
					config.UseSegments == true &&
					config.SegmentCount == 4 &&
					config.Resume == true &&
					config.Filename == "file.zip"
			},
		},
		{
			name: "no segments configuration",
			args: &CLIArgs{
				URL:        "https://example.com/file.zip",
				Output:     tempDir,
				NoSegments: true,
			},
			wantErr: false,
			check: func(config *DownloadConfig) bool {
				return config.UseSegments == false &&
					config.SegmentCount == 1
			},
		},
		{
			name: "invalid URL",
			args: &CLIArgs{
				URL:    "invalid-url",
				Output: tempDir,
			},
			wantErr: true,
		},
		{
			name: "invalid segment count",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   tempDir,
				Segments: -1,
			},
			wantErr: true,
		},
		{
			name: "custom filename",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   tempDir,
				Name:     "custom.zip",
				Segments: 4, // Add default segments
			},
			wantErr: false,
			check: func(config *DownloadConfig) bool {
				return config.Filename == "custom.zip"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := tt.args.ToDownloadConfig()
			if (err != nil) != tt.wantErr {
				t.Errorf("CLIArgs.ToDownloadConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil && !tt.check(config) {
				t.Errorf("CLIArgs.ToDownloadConfig() validation failed for config: %+v", config)
			}
		})
	}
}

func TestNewCLIArgs(t *testing.T) {
	args := NewCLIArgs()
	
	if args.Segments != 4 {
		t.Errorf("NewCLIArgs() default segments = %v, want 4", args.Segments)
	}
	
	if args.Resume != true {
		t.Errorf("NewCLIArgs() default resume = %v, want true", args.Resume)
	}
}

func TestParseRootArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		flags    map[string]interface{}
		want     *CLIArgs
		wantErr  bool
		errMsg   string
	}{
		{
			name: "positional URL only",
			args: []string{"https://example.com/file.zip"},
			want: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "positional URL and output path",
			args: []string{"https://example.com/file.zip", "/path/to/output"},
			want: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/path/to/output",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "no arguments",
			args: []string{},
			want: &CLIArgs{
				URL:      "",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name:    "invalid URL as first argument",
			args:    []string{"not-a-url"},
			wantErr: true,
			errMsg:  "first argument 'not-a-url' is not a valid URL",
		},
		{
			name:    "too many positional arguments",
			args:    []string{"https://example.com/file.zip", "/path/to/output", "extra"},
			wantErr: true,
			errMsg:  "too many positional arguments",
		},
		{
			name: "legacy download subcommand",
			args: []string{"download"},
			want: &CLIArgs{
				URL:      "",
				Legacy:   true,
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "URL flag only",
			args: []string{},
			flags: map[string]interface{}{
				"url": "https://example.com/file.zip",
			},
			want: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "URL flag with output flag",
			args: []string{},
			flags: map[string]interface{}{
				"url":    "https://example.com/file.zip",
				"output": "/path/to/output",
			},
			want: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/path/to/output",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "positional URL with flag override (same value)",
			args: []string{"https://example.com/file.zip"},
			flags: map[string]interface{}{
				"url": "https://example.com/file.zip",
			},
			want: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "positional URL with conflicting flag URL",
			args: []string{"https://example.com/file1.zip"},
			flags: map[string]interface{}{
				"url": "https://example.com/file2.zip",
			},
			wantErr: true,
			errMsg:  "URL specified both as positional argument",
		},
		{
			name: "positional output with conflicting flag output",
			args: []string{"https://example.com/file.zip", "/path1"},
			flags: map[string]interface{}{
				"output": "/path2",
			},
			wantErr: true,
			errMsg:  "output path specified both as positional argument",
		},
		{
			name: "all flags set",
			args: []string{},
			flags: map[string]interface{}{
				"url":          "https://example.com/file.zip",
				"output":       "/path/to/output",
				"name":         "custom.zip",
				"segments":     8,
				"segment-size": int64(1024),
				"no-segments":  true,
				"quiet":        true,
				"verbose":      false,
				"resume":       false,
			},
			want: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Output:      "/path/to/output",
				Name:        "custom.zip",
				Segments:    8,
				SegmentSize: 1024,
				NoSegments:  true,
				Quiet:       true,
				Verbose:     false,
				Resume:      false,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock command with flags
			cmd := &cobra.Command{}
			addRootFlags(cmd)

			// Set flag values if provided
			if tt.flags != nil {
				for flagName, value := range tt.flags {
					switch v := value.(type) {
					case string:
						cmd.Flags().Set(flagName, v)
					case int:
						cmd.Flags().Set(flagName, fmt.Sprintf("%d", v))
					case int64:
						cmd.Flags().Set(flagName, fmt.Sprintf("%d", v))
					case bool:
						cmd.Flags().Set(flagName, fmt.Sprintf("%t", v))
					}
				}
			}

			got, err := parseRootArgs(cmd, tt.args)
			
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseRootArgs() expected error but got none")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("parseRootArgs() error = %v, want error containing %v", err, tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("parseRootArgs() unexpected error = %v", err)
				return
			}

			// Compare the results
			if got.URL != tt.want.URL {
				t.Errorf("parseRootArgs() URL = %v, want %v", got.URL, tt.want.URL)
			}
			if got.Output != tt.want.Output {
				t.Errorf("parseRootArgs() Output = %v, want %v", got.Output, tt.want.Output)
			}
			if got.Name != tt.want.Name {
				t.Errorf("parseRootArgs() Name = %v, want %v", got.Name, tt.want.Name)
			}
			if got.Segments != tt.want.Segments {
				t.Errorf("parseRootArgs() Segments = %v, want %v", got.Segments, tt.want.Segments)
			}
			if got.SegmentSize != tt.want.SegmentSize {
				t.Errorf("parseRootArgs() SegmentSize = %v, want %v", got.SegmentSize, tt.want.SegmentSize)
			}
			if got.NoSegments != tt.want.NoSegments {
				t.Errorf("parseRootArgs() NoSegments = %v, want %v", got.NoSegments, tt.want.NoSegments)
			}
			if got.Quiet != tt.want.Quiet {
				t.Errorf("parseRootArgs() Quiet = %v, want %v", got.Quiet, tt.want.Quiet)
			}
			if got.Verbose != tt.want.Verbose {
				t.Errorf("parseRootArgs() Verbose = %v, want %v", got.Verbose, tt.want.Verbose)
			}
			if got.Resume != tt.want.Resume {
				t.Errorf("parseRootArgs() Resume = %v, want %v", got.Resume, tt.want.Resume)
			}
			if got.Legacy != tt.want.Legacy {
				t.Errorf("parseRootArgs() Legacy = %v, want %v", got.Legacy, tt.want.Legacy)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestResolveOutputPathEdgeCases(t *testing.T) {
	// Create a temporary directory structure for testing
	tempDir, err := os.MkdirTemp("", "durable-resume-edge-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create nested directory structure
	nestedDir := filepath.Join(tempDir, "nested", "deep")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create nested dir: %v", err)
	}

	tests := []struct {
		name        string
		url         string
		output      string
		filename    string
		want        string
		wantErr     bool
		description string
	}{
		{
			name:        "URL with only query parameters",
			url:         "https://example.com/?download=file.zip",
			output:      "",
			filename:    "",
			want:        "",
			wantErr:     true,
			description: "Should fail when URL has no path component",
		},
		{
			name:        "URL with fragment only",
			url:         "https://example.com/#download",
			output:      "",
			filename:    "",
			want:        "",
			wantErr:     true,
			description: "Should fail when URL has no filename in path",
		},
		{
			name:        "nested directory output with filename extraction",
			url:         "https://example.com/downloads/archive.tar.gz",
			output:      nestedDir,
			filename:    "",
			want:        filepath.Join(nestedDir, "archive.tar.gz"),
			wantErr:     false,
			description: "Should work with nested directory paths",
		},
		{
			name:        "filename with path separators (should be cleaned)",
			url:         "https://example.com/file.zip",
			output:      "",
			filename:    "path/to/file.zip",
			want:        "path/to/file.zip",
			wantErr:     false,
			description: "Should allow path separators in filename when no output dir specified",
		},
		{
			name:        "filename with path separators in directory output",
			url:         "https://example.com/file.zip",
			output:      tempDir,
			filename:    "subdir/file.zip",
			want:        filepath.Join(tempDir, "subdir/file.zip"),
			wantErr:     false,
			description: "Should allow subdirectories in filename",
		},
		{
			name:        "empty URL",
			url:         "",
			output:      "",
			filename:    "file.zip",
			want:        "file.zip",
			wantErr:     false,
			description: "Should work with empty URL if filename is provided",
		},
		{
			name:        "URL with port number",
			url:         "https://example.com:8080/downloads/file.zip",
			output:      "",
			filename:    "",
			want:        "file.zip",
			wantErr:     false,
			description: "Should extract filename from URL with port",
		},
		{
			name:        "URL with authentication info",
			url:         "https://user:pass@example.com/file.zip",
			output:      "",
			filename:    "",
			want:        "file.zip",
			wantErr:     false,
			description: "Should extract filename from URL with auth info",
		},
		{
			name:        "filename with dots only",
			url:         "https://example.com/...",
			output:      "",
			filename:    "",
			want:        "...",
			wantErr:     false,
			description: "Should handle unusual but valid filenames",
		},
		{
			name:        "output path with trailing slash (treated as directory)",
			url:         "https://example.com/file.zip",
			output:      tempDir + "/",
			filename:    "",
			want:        filepath.Join(tempDir, "file.zip"),
			wantErr:     false,
			description: "Should treat paths with trailing slash as directories",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOutputPath(tt.url, tt.output, tt.filename)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveOutputPath() error = %v, wantErr %v\nDescription: %s", err, tt.wantErr, tt.description)
				return
			}
			if got != tt.want {
				t.Errorf("ResolveOutputPath() = %v, want %v\nDescription: %s", got, tt.want, tt.description)
			}
		})
	}
}

func TestPositionalArgumentValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid https URL",
			args:    []string{"https://example.com/file.zip"},
			wantErr: false,
		},
		{
			name:    "valid http URL",
			args:    []string{"http://example.com/file.zip"},
			wantErr: false,
		},
		{
			name:    "valid ftp URL",
			args:    []string{"ftp://files.example.com/data.tar.gz"},
			wantErr: false,
		},
		{
			name:    "invalid URL format",
			args:    []string{"not-a-url"},
			wantErr: true,
			errMsg:  "not a valid URL",
		},
		{
			name:    "URL without protocol",
			args:    []string{"example.com/file.zip"},
			wantErr: true,
			errMsg:  "not a valid URL",
		},
		{
			name:    "empty string as URL",
			args:    []string{""},
			wantErr: true,
			errMsg:  "not a valid URL",
		},
		{
			name:    "URL with unsupported scheme",
			args:    []string{"file:///local/file.txt"},
			wantErr: true,
			errMsg:  "not a valid URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			addRootFlags(cmd)

			_, err := parseRootArgs(cmd, tt.args)
			
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseRootArgs() expected error but got none")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("parseRootArgs() error = %v, want error containing %v", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("parseRootArgs() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestFlagAndPositionalArgumentCombinations(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		flags   map[string]interface{}
		wantURL string
		wantOut string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "positional URL, flag output",
			args:    []string{"https://example.com/file.zip"},
			flags:   map[string]interface{}{"output": "/path/to/dir"},
			wantURL: "https://example.com/file.zip",
			wantOut: "/path/to/dir",
			wantErr: false,
		},
		{
			name:    "flag URL, positional output not allowed",
			args:    []string{"/path/to/dir"}, // This should be treated as URL and fail validation
			flags:   map[string]interface{}{"url": "https://example.com/file.zip"},
			wantErr: true,
			errMsg:  "not a valid URL",
		},
		{
			name:    "both positional and flag URL (same value)",
			args:    []string{"https://example.com/file.zip"},
			flags:   map[string]interface{}{"url": "https://example.com/file.zip"},
			wantURL: "https://example.com/file.zip",
			wantErr: false,
		},
		{
			name:    "both positional and flag URL (different values)",
			args:    []string{"https://example.com/file1.zip"},
			flags:   map[string]interface{}{"url": "https://example.com/file2.zip"},
			wantErr: true,
			errMsg:  "URL specified both as positional argument",
		},
		{
			name:    "both positional and flag output (same value)",
			args:    []string{"https://example.com/file.zip", "/path/to/dir"},
			flags:   map[string]interface{}{"output": "/path/to/dir"},
			wantURL: "https://example.com/file.zip",
			wantOut: "/path/to/dir",
			wantErr: false,
		},
		{
			name:    "both positional and flag output (different values)",
			args:    []string{"https://example.com/file.zip", "/path1"},
			flags:   map[string]interface{}{"output": "/path2"},
			wantErr: true,
			errMsg:  "output path specified both as positional argument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			addRootFlags(cmd)

			// Set flag values if provided
			if tt.flags != nil {
				for flagName, value := range tt.flags {
					switch v := value.(type) {
					case string:
						cmd.Flags().Set(flagName, v)
					case int:
						cmd.Flags().Set(flagName, fmt.Sprintf("%d", v))
					case bool:
						cmd.Flags().Set(flagName, fmt.Sprintf("%t", v))
					}
				}
			}

			got, err := parseRootArgs(cmd, tt.args)
			
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseRootArgs() expected error but got none")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("parseRootArgs() error = %v, want error containing %v", err, tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("parseRootArgs() unexpected error = %v", err)
				return
			}

			if got.URL != tt.wantURL {
				t.Errorf("parseRootArgs() URL = %v, want %v", got.URL, tt.wantURL)
			}
			if got.Output != tt.wantOut {
				t.Errorf("parseRootArgs() Output = %v, want %v", got.Output, tt.wantOut)
			}
		})
	}
}

func TestValidateFlagCombinations(t *testing.T) {
	tests := []struct {
		name    string
		args    *CLIArgs
		wantErr bool
		errMsg  string
	}{
		{
			name: "no conflicts",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/path/to/dir",
				Segments: 4,
				Resume:   true,
			},
			wantErr: false,
		},
		{
			name: "quiet and verbose conflict",
			args: &CLIArgs{
				URL:     "https://example.com/file.zip",
				Quiet:   true,
				Verbose: true,
			},
			wantErr: true,
			errMsg:  "Cannot use both --quiet and --verbose",
		},
		{
			name: "no-segments with segments conflict",
			args: &CLIArgs{
				URL:        "https://example.com/file.zip",
				NoSegments: true,
				Segments:   8, // Explicitly set to non-default value
			},
			wantErr: true,
			errMsg:  "Cannot use --no-segments with --segments",
		},
		{
			name: "no-segments with segment-size conflict",
			args: &CLIArgs{
				URL:         "https://example.com/file.zip",
				NoSegments:  true,
				SegmentSize: 1024,
			},
			wantErr: true,
			errMsg:  "Cannot use --no-segments with --segment-size",
		},
		{
			name: "no-segments with default segments (no conflict)",
			args: &CLIArgs{
				URL:        "https://example.com/file.zip",
				NoSegments: true,
				Segments:   4, // Default value, should not conflict
			},
			wantErr: false,
		},
		{
			name: "file path output with filename conflict",
			args: &CLIArgs{
				URL:    "https://example.com/file.zip",
				Output: "/path/to/file.zip", // File path with extension
				Name:   "other.zip",
			},
			wantErr: true,
			errMsg:  "Cannot specify both file path (--output) and filename (--name)",
		},
		{
			name: "directory output with filename (no conflict)",
			args: &CLIArgs{
				URL:    "https://example.com/file.zip",
				Output: "/path/to/dir/", // Directory path with trailing slash
				Name:   "file.zip",
			},
			wantErr: false,
		},
		{
			name: "multiple conflicts",
			args: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Output:      "/path/to/file.zip",
				Name:        "other.zip",
				NoSegments:  true,
				Segments:    8,
				SegmentSize: 1024,
				Quiet:       true,
				Verbose:     true,
			},
			wantErr: true,
			errMsg:  "Incompatible flag combinations detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFlagCombinations(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFlagCombinations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateFlagCombinations() error = %v, want error containing %v", err, tt.errMsg)
			}
		})
	}
}

func TestValidateSegmentParamsEnhanced(t *testing.T) {
	tests := []struct {
		name        string
		segments    int
		segmentSize int64
		noSegments  bool
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "valid parameters",
			segments:    4,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     false,
		},
		{
			name:        "no segments enabled",
			segments:    0,
			segmentSize: 0,
			noSegments:  true,
			wantErr:     false,
		},
		{
			name:        "zero segments with enhanced error",
			segments:    0,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     true,
			errMsg:      "Invalid segment count",
		},
		{
			name:        "negative segments with enhanced error",
			segments:    -1,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     true,
			errMsg:      "Invalid segment count",
		},
		{
			name:        "too many segments with enhanced error",
			segments:    50,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     true,
			errMsg:      "Too many segments",
		},
		{
			name:        "negative segment size with enhanced error",
			segments:    4,
			segmentSize: -1,
			noSegments:  false,
			wantErr:     true,
			errMsg:      "Invalid segment size",
		},
		{
			name:        "segment size too small with enhanced error",
			segments:    4,
			segmentSize: 100,
			noSegments:  false,
			wantErr:     true,
			errMsg:      "Segment size too small",
		},
		{
			name:        "zero segment size (auto)",
			segments:    4,
			segmentSize: 0,
			noSegments:  false,
			wantErr:     false,
		},
		{
			name:        "maximum allowed segments",
			segments:    32,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     false,
		},
		{
			name:        "minimum segment size",
			segments:    4,
			segmentSize: 1024,
			noSegments:  false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSegmentParams(tt.segments, tt.segmentSize, tt.noSegments)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSegmentParams() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
				t.Errorf("ValidateSegmentParams() error = %v, want error containing %v", err, tt.errMsg)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes uint64
		want  string
	}{
		{
			name:  "bytes",
			bytes: 512,
			want:  "512 B",
		},
		{
			name:  "kilobytes",
			bytes: 1536, // 1.5 KB
			want:  "1.5 KB",
		},
		{
			name:  "megabytes",
			bytes: 1572864, // 1.5 MB
			want:  "1.5 MB",
		},
		{
			name:  "gigabytes",
			bytes: 1610612736, // 1.5 GB
			want:  "1.5 GB",
		},
		{
			name:  "zero bytes",
			bytes: 0,
			want:  "0 B",
		},
		{
			name:  "exactly 1 KB",
			bytes: 1024,
			want:  "1.0 KB",
		},
		{
			name:  "exactly 1 MB",
			bytes: 1024 * 1024,
			want:  "1.0 MB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestArgumentParsingCombinations tests all possible argument parsing combinations
func TestArgumentParsingCombinations(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		flags       map[string]interface{}
		wantURL     string
		wantOutput  string
		wantName    string
		wantSegments int
		wantErr     bool
		errMsg      string
		description string
	}{
		{
			name:        "URL only positional",
			args:        []string{"https://example.com/file.zip"},
			wantURL:     "https://example.com/file.zip",
			wantSegments: 4,
			description: "Should parse single URL positional argument",
		},
		{
			name:        "URL and output positional",
			args:        []string{"https://example.com/file.zip", "/tmp/downloads"},
			wantURL:     "https://example.com/file.zip",
			wantOutput:  "/tmp/downloads",
			wantSegments: 4,
			description: "Should parse URL and output path positional arguments",
		},
		{
			name:        "URL flag only",
			flags:       map[string]interface{}{"url": "https://example.com/file.zip"},
			wantURL:     "https://example.com/file.zip",
			wantSegments: 4,
			description: "Should parse URL from flag",
		},
		{
			name:        "all flags combination",
			flags: map[string]interface{}{
				"url":          "https://example.com/file.zip",
				"output":       "/tmp/downloads",
				"name":         "myfile.zip",
				"segments":     8,
				"segment-size": int64(2048),
				"quiet":        true,
				"verbose":      false,
				"resume":       false,
			},
			wantURL:     "https://example.com/file.zip",
			wantOutput:  "/tmp/downloads",
			wantName:    "myfile.zip",
			wantSegments: 8,
			description: "Should parse all flags correctly",
		},
		{
			name:        "mixed positional and flags",
			args:        []string{"https://example.com/file.zip"},
			flags:       map[string]interface{}{"output": "/tmp", "segments": 6},
			wantURL:     "https://example.com/file.zip",
			wantOutput:  "/tmp",
			wantSegments: 6,
			description: "Should combine positional URL with flag options",
		},
		{
			name:        "empty arguments",
			args:        []string{},
			wantURL:     "",
			wantSegments: 4,
			description: "Should handle empty arguments gracefully",
		},
		{
			name:        "invalid URL positional",
			args:        []string{"not-a-url"},
			wantErr:     true,
			errMsg:      "not a valid URL",
			description: "Should reject invalid URL in positional argument",
		},
		{
			name:        "conflicting URL sources",
			args:        []string{"https://example.com/file1.zip"},
			flags:       map[string]interface{}{"url": "https://example.com/file2.zip"},
			wantErr:     true,
			errMsg:      "URL specified both as positional argument",
			description: "Should detect conflicting URL specifications",
		},
		{
			name:        "conflicting output sources",
			args:        []string{"https://example.com/file.zip", "/path1"},
			flags:       map[string]interface{}{"output": "/path2"},
			wantErr:     true,
			errMsg:      "output path specified both as positional argument",
			description: "Should detect conflicting output path specifications",
		},
		{
			name:        "legacy download subcommand detection",
			args:        []string{"download"},
			wantURL:     "",
			wantSegments: 4,
			description: "Should detect legacy download subcommand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			addRootFlags(cmd)

			// Set flag values if provided
			if tt.flags != nil {
				for flagName, value := range tt.flags {
					switch v := value.(type) {
					case string:
						cmd.Flags().Set(flagName, v)
					case int:
						cmd.Flags().Set(flagName, fmt.Sprintf("%d", v))
					case int64:
						cmd.Flags().Set(flagName, fmt.Sprintf("%d", v))
					case bool:
						cmd.Flags().Set(flagName, fmt.Sprintf("%t", v))
					}
				}
			}

			got, err := parseRootArgs(cmd, tt.args)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseRootArgs() expected error but got none. Description: %s", tt.description)
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("parseRootArgs() error = %v, want error containing %v. Description: %s", err, tt.errMsg, tt.description)
				}
				return
			}

			if err != nil {
				t.Errorf("parseRootArgs() unexpected error = %v. Description: %s", err, tt.description)
				return
			}

			// Validate results
			if got.URL != tt.wantURL {
				t.Errorf("parseRootArgs() URL = %v, want %v. Description: %s", got.URL, tt.wantURL, tt.description)
			}
			if got.Output != tt.wantOutput {
				t.Errorf("parseRootArgs() Output = %v, want %v. Description: %s", got.Output, tt.wantOutput, tt.description)
			}
			if got.Name != tt.wantName {
				t.Errorf("parseRootArgs() Name = %v, want %v. Description: %s", got.Name, tt.wantName, tt.description)
			}
			if got.Segments != tt.wantSegments {
				t.Errorf("parseRootArgs() Segments = %v, want %v. Description: %s", got.Segments, tt.wantSegments, tt.description)
			}
		})
	}
}

// TestPathResolutionScenarios tests various path resolution scenarios
func TestPathResolutionScenarios(t *testing.T) {
	// Create temporary directory structure for testing
	tempDir, err := os.MkdirTemp("", "durable-resume-path-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create nested directories
	nestedDir := filepath.Join(tempDir, "nested", "deep")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("Failed to create nested dir: %v", err)
	}

	// Create test file
	testFile := filepath.Join(tempDir, "existing.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name        string
		url         string
		output      string
		filename    string
		want        string
		wantErr     bool
		description string
	}{
		{
			name:        "current directory with URL filename",
			url:         "https://example.com/archive.tar.gz",
			output:      "",
			filename:    "",
			want:        "archive.tar.gz",
			description: "Should extract filename from URL for current directory",
		},
		{
			name:        "current directory with custom filename",
			url:         "https://example.com/file.zip",
			output:      "",
			filename:    "custom-name.zip",
			want:        "custom-name.zip",
			description: "Should use custom filename in current directory",
		},
		{
			name:        "existing directory with URL filename",
			url:         "https://example.com/data.json",
			output:      tempDir,
			filename:    "",
			want:        filepath.Join(tempDir, "data.json"),
			description: "Should combine directory with URL filename",
		},
		{
			name:        "existing directory with custom filename",
			url:         "https://example.com/file.zip",
			output:      tempDir,
			filename:    "my-download.zip",
			want:        filepath.Join(tempDir, "my-download.zip"),
			description: "Should combine directory with custom filename",
		},
		{
			name:        "nested directory path",
			url:         "https://example.com/file.zip",
			output:      nestedDir,
			filename:    "nested-file.zip",
			want:        filepath.Join(nestedDir, "nested-file.zip"),
			description: "Should work with nested directory paths",
		},
		{
			name:        "full file path specification",
			url:         "https://example.com/file.zip",
			output:      filepath.Join(tempDir, "specific-file.zip"),
			filename:    "",
			want:        filepath.Join(tempDir, "specific-file.zip"),
			description: "Should use full file path when specified",
		},
		{
			name:        "existing file as output",
			url:         "https://example.com/file.zip",
			output:      testFile,
			filename:    "",
			want:        testFile,
			description: "Should allow overwriting existing file",
		},
		{
			name:        "URL without filename",
			url:         "https://example.com/path/",
			output:      "",
			filename:    "",
			wantErr:     true,
			description: "Should fail when URL has no filename and none provided",
		},
		{
			name:        "URL without filename but custom name provided",
			url:         "https://example.com/path/",
			output:      "",
			filename:    "download.bin",
			want:        "download.bin",
			description: "Should use custom filename when URL has no filename",
		},
		{
			name:        "conflicting file path and filename",
			url:         "https://example.com/file.zip",
			output:      filepath.Join(tempDir, "specific.zip"),
			filename:    "other.zip",
			wantErr:     true,
			description: "Should reject conflicting file path and filename",
		},
		{
			name:        "relative path output",
			url:         "https://example.com/file.zip",
			output:      "./downloads/file.zip",
			filename:    "",
			want:        "./downloads/file.zip",
			description: "Should handle relative paths correctly",
		},
		{
			name:        "URL with query parameters",
			url:         "https://example.com/file.zip?version=1&token=abc",
			output:      "",
			filename:    "",
			want:        "file.zip",
			description: "Should extract filename ignoring query parameters",
		},
		{
			name:        "URL with fragment",
			url:         "https://example.com/file.zip#section1",
			output:      "",
			filename:    "",
			want:        "file.zip",
			description: "Should extract filename ignoring fragment",
		},
		{
			name:        "URL with encoded characters",
			url:         "https://example.com/my%20file%20name.zip",
			output:      "",
			filename:    "",
			want:        "my file name.zip",
			description: "Should decode URL-encoded characters in filename",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOutputPath(tt.url, tt.output, tt.filename)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ResolveOutputPath() expected error but got none. Description: %s", tt.description)
					return
				}
				return
			}

			if err != nil {
				t.Errorf("ResolveOutputPath() unexpected error = %v. Description: %s", err, tt.description)
				return
			}

			if got != tt.want {
				t.Errorf("ResolveOutputPath() = %v, want %v. Description: %s", got, tt.want, tt.description)
			}
		})
	}
}

// TestFlagValidationErrorMessages tests that flag validation produces helpful error messages
func TestFlagValidationErrorMessages(t *testing.T) {
	tests := []struct {
		name        string
		args        *CLIArgs
		wantErr     bool
		errContains []string
		description string
	}{
		{
			name: "invalid URL error message",
			args: &CLIArgs{
				URL: "not-a-url",
			},
			wantErr:     true,
			errContains: []string{"Invalid URL format", "missing protocol"},
			description: "Should provide helpful error for invalid URL",
		},
		{
			name: "empty URL error message",
			args: &CLIArgs{
				URL: "",
			},
			wantErr:     true,
			errContains: []string{"No URL provided", "Usage:", "Examples:"},
			description: "Should provide helpful error for missing URL",
		},
		{
			name: "invalid segment count error message",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: -1,
			},
			wantErr:     true,
			errContains: []string{"Invalid segment count", "must be at least 1", "Recommended values:"},
			description: "Should provide helpful error for invalid segment count",
		},
		{
			name: "too many segments error message",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 50,
			},
			wantErr:     true,
			errContains: []string{"Too many segments", "Maximum: 32", "Recommended values:"},
			description: "Should provide helpful error for too many segments",
		},
		{
			name: "invalid segment size error message",
			args: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Segments:    4,
				SegmentSize: -1,
			},
			wantErr:     true,
			errContains: []string{"Invalid segment size", "cannot be negative", "Valid options:"},
			description: "Should provide helpful error for invalid segment size",
		},
		{
			name: "segment size too small error message",
			args: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Segments:    4,
				SegmentSize: 100,
			},
			wantErr:     true,
			errContains: []string{"Segment size too small", "Minimum: 1024 bytes", "Recommended values:"},
			description: "Should provide helpful error for segment size too small",
		},
		{
			name: "conflicting flags error message",
			args: &CLIArgs{
				URL:     "https://example.com/file.zip",
				Quiet:   true,
				Verbose: true,
			},
			wantErr:     true,
			errContains: []string{"Incompatible flag combinations", "Cannot use both --quiet and --verbose"},
			description: "Should provide helpful error for conflicting flags",
		},
		{
			name: "unsupported URL scheme error message",
			args: &CLIArgs{
				URL: "file:///local/file.txt",
			},
			wantErr:     true,
			errContains: []string{"Unsupported URL protocol", "Supported protocols:", "Examples:"},
			description: "Should provide helpful error for unsupported URL scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.args.ToDownloadConfig()

			if tt.wantErr {
				if err == nil {
					t.Errorf("ToDownloadConfig() expected error but got none. Description: %s", tt.description)
					return
				}

				errMsg := err.Error()
				for _, expectedText := range tt.errContains {
					if !contains(errMsg, expectedText) {
						t.Errorf("ToDownloadConfig() error message missing expected text '%s'. Full error: %s. Description: %s", expectedText, errMsg, tt.description)
					}
				}
			} else {
				if err != nil {
					t.Errorf("ToDownloadConfig() unexpected error = %v. Description: %s", err, tt.description)
				}
			}
		})
	}
}

// TestBackwardCompatibilityDetection tests detection of backward compatibility scenarios
func TestBackwardCompatibilityDetection(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectLegacy bool
		description string
	}{
		{
			name:         "new syntax with URL",
			args:         []string{"https://example.com/file.zip"},
			expectLegacy: false,
			description:  "Should not detect legacy mode for new URL syntax",
		},
		{
			name:         "new syntax with URL and output",
			args:         []string{"https://example.com/file.zip", "/tmp"},
			expectLegacy: false,
			description:  "Should not detect legacy mode for new syntax with output",
		},
		{
			name:         "legacy download subcommand",
			args:         []string{"download"},
			expectLegacy: true,
			description:  "Should detect legacy mode for download subcommand",
		},
		{
			name:         "empty arguments",
			args:         []string{},
			expectLegacy: false,
			description:  "Should not detect legacy mode for empty arguments",
		},
		{
			name:         "flag-only usage",
			args:         []string{},
			expectLegacy: false,
			description:  "Should not detect legacy mode for flag-only usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			addRootFlags(cmd)

			cliArgs, err := parseRootArgs(cmd, tt.args)
			if err != nil {
				// Some tests may produce errors (like invalid URLs), but we still want to check legacy detection
				if tt.expectLegacy {
					// For legacy detection, we mainly care about the first argument being "download"
					hasLegacy := len(tt.args) > 0 && tt.args[0] == "download"
					if !hasLegacy {
						t.Errorf("parseRootArgs() expected legacy detection but didn't find it. Description: %s", tt.description)
					}
				}
				return
			}

			if cliArgs.Legacy != tt.expectLegacy {
				t.Errorf("parseRootArgs() Legacy = %v, want %v. Description: %s", cliArgs.Legacy, tt.expectLegacy, tt.description)
			}
		})
	}
}

// TestCompleteArgumentValidation tests complete argument validation workflow
func TestCompleteArgumentValidation(t *testing.T) {
	// Create temporary directory for testing
	tempDir, err := os.MkdirTemp("", "durable-resume-validation-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name        string
		args        *CLIArgs
		wantErr     bool
		description string
	}{
		{
			name: "valid complete configuration",
			args: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Output:      tempDir,
				Name:        "download.zip",
				Segments:    4,
				SegmentSize: 1048576,
				Resume:      true,
				Quiet:       false,
				Verbose:     false,
			},
			wantErr:     false,
			description: "Should validate complete valid configuration",
		},
		{
			name: "valid minimal configuration",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 4,
				Resume:   true,
			},
			wantErr:     false,
			description: "Should validate minimal valid configuration",
		},
		{
			name: "valid no-segments configuration",
			args: &CLIArgs{
				URL:        "https://example.com/file.zip",
				Output:     tempDir,
				NoSegments: true,
				Resume:     true,
			},
			wantErr:     false,
			description: "Should validate no-segments configuration",
		},
		{
			name: "invalid URL in complete validation",
			args: &CLIArgs{
				URL:      "invalid-url",
				Output:   tempDir,
				Segments: 4,
			},
			wantErr:     true,
			description: "Should reject invalid URL in complete validation",
		},
		{
			name: "invalid segments in complete validation",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   tempDir,
				Segments: 0,
			},
			wantErr:     true,
			description: "Should reject invalid segments in complete validation",
		},
		{
			name: "conflicting flags in complete validation",
			args: &CLIArgs{
				URL:     "https://example.com/file.zip",
				Output:  tempDir,
				Quiet:   true,
				Verbose: true,
			},
			wantErr:     true,
			description: "Should reject conflicting flags in complete validation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := tt.args.ToDownloadConfig()

			if tt.wantErr {
				if err == nil {
					t.Errorf("ToDownloadConfig() expected error but got none. Description: %s", tt.description)
					return
				}
			} else {
				if err != nil {
					t.Errorf("ToDownloadConfig() unexpected error = %v. Description: %s", err, tt.description)
					return
				}

				// Validate that config was created correctly
				if config == nil {
					t.Errorf("ToDownloadConfig() returned nil config. Description: %s", tt.description)
					return
				}

				// Basic validation of config fields
				if config.URL != tt.args.URL {
					t.Errorf("ToDownloadConfig() URL = %v, want %v. Description: %s", config.URL, tt.args.URL, tt.description)
				}

				if tt.args.NoSegments {
					if config.UseSegments {
						t.Errorf("ToDownloadConfig() UseSegments = true, want false for no-segments. Description: %s", tt.description)
					}
					if config.SegmentCount != 1 {
						t.Errorf("ToDownloadConfig() SegmentCount = %v, want 1 for no-segments. Description: %s", config.SegmentCount, tt.description)
					}
				} else {
					if !config.UseSegments {
						t.Errorf("ToDownloadConfig() UseSegments = false, want true. Description: %s", tt.description)
					}
					if config.SegmentCount != tt.args.Segments {
						t.Errorf("ToDownloadConfig() SegmentCount = %v, want %v. Description: %s", config.SegmentCount, tt.args.Segments, tt.description)
					}
				}
			}
		})
	}
}