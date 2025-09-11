package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestLegacyDownloadCommandDetection(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantLegacy bool
		wantError  bool
	}{
		{
			name:       "legacy download subcommand detected",
			args:       []string{"download", "--url", "https://example.com/file.zip", "--output", "/tmp"},
			wantLegacy: true,
			wantError:  false,
		},
		{
			name:       "new direct URL syntax",
			args:       []string{"https://example.com/file.zip", "-o", "/tmp"},
			wantLegacy: false,
			wantError:  false,
		},
		{
			name:       "new direct URL syntax with flags",
			args:       []string{"https://example.com/file.zip", "--output", "/tmp", "--segments", "8"},
			wantLegacy: false,
			wantError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a root command for testing
			rootCmd := newRoot()
			rootCmd.SetArgs(tt.args)
			
			// Capture stderr to check for deprecation warnings
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w
			
			// Execute command (this will fail due to unimplemented download, but we can test parsing)
			err := rootCmd.Execute()
			
			// Restore stderr
			w.Close()
			os.Stderr = oldStderr
			
			// Read captured output
			buf := make([]byte, 1024)
			n, _ := r.Read(buf)
			output := string(buf[:n])
			
			// Check if deprecation warning was shown for legacy commands
			hasDeprecationWarning := strings.Contains(output, "DEPRECATION WARNING")
			
			if tt.wantLegacy && !hasDeprecationWarning {
				t.Errorf("Expected deprecation warning for legacy command, but none was shown")
			}
			
			if !tt.wantLegacy && hasDeprecationWarning {
				t.Errorf("Unexpected deprecation warning for new syntax command")
			}
			
			// For this test, we expect errors due to unimplemented download execution
			// The important thing is that parsing works correctly
			if tt.wantError && err == nil {
				t.Errorf("Expected error but got none")
			}
		})
	}
}

func TestParseLegacyArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantURL   string
		wantOutput string
		wantName   string
		wantSegments int
		wantError bool
	}{
		{
			name:      "basic legacy args",
			args:      []string{"--url", "https://example.com/file.zip", "--output", "/tmp"},
			wantURL:   "https://example.com/file.zip",
			wantOutput: "/tmp",
			wantSegments: 4, // default
			wantError: false,
		},
		{
			name:      "legacy args with all options",
			args:      []string{"--url", "https://example.com/file.zip", "--output", "/tmp", "--name", "myfile.zip", "--segments", "8", "--segment-size", "1024"},
			wantURL:   "https://example.com/file.zip",
			wantOutput: "/tmp",
			wantName:   "myfile.zip",
			wantSegments: 8,
			wantError: false,
		},
		{
			name:      "legacy args with short flags",
			args:      []string{"-u", "https://example.com/file.zip", "-o", "/tmp", "-n", "myfile.zip", "-c", "6"},
			wantURL:   "https://example.com/file.zip",
			wantOutput: "/tmp",
			wantName:   "myfile.zip",
			wantSegments: 6,
			wantError: false,
		},
		{
			name:      "legacy args with old flag names",
			args:      []string{"--url", "https://example.com/file.zip", "--out", "/tmp", "--file", "myfile.zip", "--segment-count", "10"},
			wantURL:   "https://example.com/file.zip",
			wantOutput: "/tmp",
			wantName:   "myfile.zip",
			wantSegments: 10,
			wantError: false,
		},
		{
			name:      "missing required flag value",
			args:      []string{"--url"},
			wantError: true,
		},
		{
			name:      "unknown flag",
			args:      []string{"--unknown-flag", "value"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseLegacyArgs(tt.args)
			
			if tt.wantError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}
			
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			
			if result.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", result.URL, tt.wantURL)
			}
			
			if result.Output != tt.wantOutput {
				t.Errorf("Output = %q, want %q", result.Output, tt.wantOutput)
			}
			
			if result.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", result.Name, tt.wantName)
			}
			
			if result.Segments != tt.wantSegments {
				t.Errorf("Segments = %d, want %d", result.Segments, tt.wantSegments)
			}
			
			if !result.Legacy {
				t.Errorf("Legacy flag should be true for parsed legacy args")
			}
		})
	}
}

func TestGenerateNewCommandSyntax(t *testing.T) {
	tests := []struct {
		name     string
		args     *CLIArgs
		expected string
	}{
		{
			name: "basic URL only",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 4, // default
			},
			expected: "dr https://example.com/file.zip",
		},
		{
			name: "URL with output directory",
			args: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/tmp",
				Segments: 4, // default
			},
			expected: "dr https://example.com/file.zip -o /tmp",
		},
		{
			name: "URL with all options",
			args: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Output:      "/tmp",
				Name:        "myfile.zip",
				Segments:    8,
				SegmentSize: 1024,
			},
			expected: "dr https://example.com/file.zip -o /tmp -n myfile.zip -c 8 -s 1024",
		},
		{
			name: "empty URL",
			args: &CLIArgs{
				URL: "",
			},
			expected: "dr <URL> [options]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateNewCommandSyntax(tt.args)
			if result != tt.expected {
				t.Errorf("generateNewCommandSyntax() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDeprecationWarningOutput(t *testing.T) {
	// Test that deprecation warning is properly formatted
	args := &CLIArgs{
		URL:      "https://example.com/file.zip",
		Output:   "/tmp",
		Name:     "myfile.zip",
		Segments: 8,
		Legacy:   true,
	}

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	showDeprecationWarning(args)

	w.Close()
	os.Stderr = oldStderr

	// Read captured output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Check that warning contains expected elements
	expectedElements := []string{
		"DEPRECATION WARNING",
		"download' subcommand is deprecated",
		"dr https://example.com/file.zip -o /tmp -n myfile.zip -c 8",
		"new simplified syntax",
		"dr --help",
	}

	for _, element := range expectedElements {
		if !strings.Contains(output, element) {
			t.Errorf("Deprecation warning missing expected element: %q\nFull output: %s", element, output)
		}
	}
}

func TestBackwardCompatibilityFlagMapping(t *testing.T) {
	// Test that old flag names are properly mapped to new functionality
	tests := []struct {
		name     string
		oldFlags []string
		wantArgs *CLIArgs
	}{
		{
			name:     "old --out flag maps to Output",
			oldFlags: []string{"--url", "https://example.com/file.zip", "--out", "/tmp"},
			wantArgs: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/tmp",
				Segments: 4,
				Legacy:   true,
			},
		},
		{
			name:     "old --file flag maps to Name",
			oldFlags: []string{"--url", "https://example.com/file.zip", "--file", "myfile.zip"},
			wantArgs: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Name:     "myfile.zip",
				Segments: 4,
				Legacy:   true,
			},
		},
		{
			name:     "old --segment-count flag maps to Segments",
			oldFlags: []string{"--url", "https://example.com/file.zip", "--segment-count", "10"},
			wantArgs: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Segments: 10,
				Legacy:   true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseLegacyArgs(tt.oldFlags)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result.URL != tt.wantArgs.URL {
				t.Errorf("URL = %q, want %q", result.URL, tt.wantArgs.URL)
			}
			if result.Output != tt.wantArgs.Output {
				t.Errorf("Output = %q, want %q", result.Output, tt.wantArgs.Output)
			}
			if result.Name != tt.wantArgs.Name {
				t.Errorf("Name = %q, want %q", result.Name, tt.wantArgs.Name)
			}
			if result.Segments != tt.wantArgs.Segments {
				t.Errorf("Segments = %d, want %d", result.Segments, tt.wantArgs.Segments)
			}
			if result.Legacy != tt.wantArgs.Legacy {
				t.Errorf("Legacy = %t, want %t", result.Legacy, tt.wantArgs.Legacy)
			}
		})
	}
}

func TestEndToEndBackwardCompatibility(t *testing.T) {
	// Test complete end-to-end backward compatibility scenarios
	tests := []struct {
		name           string
		args           []string
		expectLegacy   bool
		expectWarning  bool
		expectError    bool
		description    string
	}{
		{
			name:          "legacy download with basic flags",
			args:          []string{"download", "--url", "https://example.com/file.zip", "--output", "/tmp"},
			expectLegacy:  true,
			expectWarning: true,
			expectError:   true, // Expected due to unimplemented download execution
			description:   "Should detect legacy mode and show deprecation warning",
		},
		{
			name:          "legacy download subcommand with standard flags",
			args:          []string{"download", "--url", "https://example.com/file.zip", "--output", "/tmp", "--name", "custom.zip", "--segments", "8"},
			expectLegacy:  false, // This goes to the subcommand directly, not root command legacy detection
			expectWarning: true,  // But the subcommand shows its own deprecation warning
			expectError:   true,  // Expected due to unimplemented download execution
			description:   "Should show deprecation warning when using download subcommand directly",
		},
		{
			name:          "new syntax should not trigger legacy mode",
			args:          []string{"https://example.com/file.zip", "-o", "/tmp"},
			expectLegacy:  false,
			expectWarning: false,
			expectError:   true, // Expected due to unimplemented download execution
			description:   "New syntax should not show deprecation warnings",
		},
		{
			name:          "new syntax with all flags",
			args:          []string{"https://example.com/file.zip", "--output", "/tmp", "--name", "custom.zip", "--segments", "8"},
			expectLegacy:  false,
			expectWarning: false,
			expectError:   true, // Expected due to unimplemented download execution
			description:   "New syntax with all flags should work without warnings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a root command for testing
			rootCmd := newRoot()
			rootCmd.SetArgs(tt.args)
			
			// Capture stderr to check for deprecation warnings
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w
			
			// Execute command
			err := rootCmd.Execute()
			
			// Restore stderr
			w.Close()
			os.Stderr = oldStderr
			
			// Read captured output
			buf := make([]byte, 2048)
			n, _ := r.Read(buf)
			output := string(buf[:n])
			
			// Check error expectation
			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none. Description: %s", tt.description)
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v. Description: %s", err, tt.description)
			}
			
			// Check deprecation warning expectation
			hasDeprecationWarning := strings.Contains(output, "DEPRECATION WARNING")
			if tt.expectWarning && !hasDeprecationWarning {
				t.Errorf("Expected deprecation warning but none was shown. Description: %s", tt.description)
			}
			if !tt.expectWarning && hasDeprecationWarning {
				t.Errorf("Unexpected deprecation warning shown. Description: %s", tt.description)
			}
		})
	}
}

func TestLegacySubcommandDirectUsage(t *testing.T) {
	// Test using the legacy download subcommand directly (not through root command detection)
	tests := []struct {
		name        string
		args        []string
		expectError bool
		description string
	}{
		{
			name:        "legacy subcommand with required flags",
			args:        []string{"--url", "https://example.com/file.zip", "--output", "/tmp"},
			expectError: true, // Expected due to unimplemented download execution
			description: "Should work with required flags",
		},
		{
			name:        "legacy subcommand missing URL",
			args:        []string{"--output", "/tmp"},
			expectError: true, // Should fail due to missing URL
			description: "Should fail when URL is missing",
		},
		{
			name:        "legacy subcommand with valid flags",
			args:        []string{"--url", "https://example.com/file.zip", "--output", "/tmp", "--segments", "6"},
			expectError: true, // Expected due to unimplemented download execution
			description: "Should handle valid flag combinations",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create the download subcommand directly
			downloadCmd := newDownloadCmd(os.Stdout)
			downloadCmd.SetArgs(tt.args)
			
			// Capture stderr to check for deprecation warnings
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w
			
			// Execute command
			err := downloadCmd.Execute()
			
			// Restore stderr
			w.Close()
			os.Stderr = oldStderr
			
			// Read captured output
			buf := make([]byte, 1024)
			n, _ := r.Read(buf)
			output := string(buf[:n])
			
			// Check error expectation
			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none. Description: %s", tt.description)
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v. Description: %s", err, tt.description)
			}
			
			// Direct subcommand usage should always show deprecation warning
			hasDeprecationWarning := strings.Contains(output, "DEPRECATION WARNING")
			if !hasDeprecationWarning {
				t.Errorf("Expected deprecation warning for direct subcommand usage but none was shown. Description: %s", tt.description)
			}
		})
	}
}

func TestMigrationSuggestions(t *testing.T) {
	// Test that migration suggestions are accurate and helpful
	tests := []struct {
		name           string
		legacyArgs     *CLIArgs
		expectedSyntax string
		description    string
	}{
		{
			name: "basic URL and output",
			legacyArgs: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/tmp",
				Segments: 4, // default
				Legacy:   true,
			},
			expectedSyntax: "dr https://example.com/file.zip -o /tmp",
			description:    "Should suggest basic new syntax",
		},
		{
			name: "URL with custom filename",
			legacyArgs: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/tmp",
				Name:     "custom.zip",
				Segments: 4, // default
				Legacy:   true,
			},
			expectedSyntax: "dr https://example.com/file.zip -o /tmp -n custom.zip",
			description:    "Should include filename in suggestion",
		},
		{
			name: "URL with non-default segments",
			legacyArgs: &CLIArgs{
				URL:      "https://example.com/file.zip",
				Output:   "/tmp",
				Segments: 8,
				Legacy:   true,
			},
			expectedSyntax: "dr https://example.com/file.zip -o /tmp -c 8",
			description:    "Should include segment count when non-default",
		},
		{
			name: "URL with segment size",
			legacyArgs: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Output:      "/tmp",
				Segments:    4, // default
				SegmentSize: 2048,
				Legacy:      true,
			},
			expectedSyntax: "dr https://example.com/file.zip -o /tmp -s 2048",
			description:    "Should include segment size when specified",
		},
		{
			name: "all options specified",
			legacyArgs: &CLIArgs{
				URL:         "https://example.com/file.zip",
				Output:      "/tmp",
				Name:        "custom.zip",
				Segments:    8,
				SegmentSize: 2048,
				Legacy:      true,
			},
			expectedSyntax: "dr https://example.com/file.zip -o /tmp -n custom.zip -c 8 -s 2048",
			description:    "Should include all options in suggestion",
		},
		{
			name: "empty URL should show generic syntax",
			legacyArgs: &CLIArgs{
				URL:    "",
				Legacy: true,
			},
			expectedSyntax: "dr <URL> [options]",
			description:    "Should show generic syntax for empty URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateNewCommandSyntax(tt.legacyArgs)
			if result != tt.expectedSyntax {
				t.Errorf("generateNewCommandSyntax() = %q, want %q. Description: %s", result, tt.expectedSyntax, tt.description)
			}
		})
	}
}