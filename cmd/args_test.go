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
			name:    "empty URL",
			url:     "",
			wantErr: true,
		},
		{
			name:    "invalid URL format",
			url:     "not-a-url",
			wantErr: true,
		},
		{
			name:    "unsupported scheme",
			url:     "file:///local/file.txt",
			wantErr: true,
		},
		{
			name:    "URL without host",
			url:     "https:///file.zip",
			wantErr: true,
		},
		{
			name:    "URL with path and query",
			url:     "https://example.com/path/file.zip?version=1",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL() error = %v, wantErr %v", err, tt.wantErr)
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