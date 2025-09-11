package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandHelp(t *testing.T) {
	// Create a buffer to capture output
	var buf bytes.Buffer
	
	// Create root command and set output to our buffer
	rootCmd := newRoot()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	
	// Execute help command
	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	
	if err != nil {
		t.Fatalf("Help command failed: %v", err)
	}
	
	output := buf.String()
	
	// Test that key sections are present
	testCases := []struct {
		name     string
		expected string
	}{
		{"Title", "Durable Resume - A robust solution for downloading files over the internet"},
		{"Basic Usage Section", "BASIC USAGE:"},
		{"Common Examples Section", "COMMON EXAMPLES:"},
		{"Advanced Examples Section", "ADVANCED EXAMPLES:"},
		{"Simple download example", "dr https://example.com/file.zip"},
		{"Directory output example", "dr https://example.com/file.zip -o ~/Downloads"},
		{"Custom filename example", "dr https://example.com/file.zip -o ~/Downloads -n myfile.zip"},
		{"Segments example", "dr https://example.com/largefile.iso --segments 8"},
		{"No segments example", "dr https://example.com/file.zip --no-segments"},
		{"Quiet example", "dr https://example.com/file.zip --quiet"},
		{"Verbose example", "dr https://example.com/file.zip --verbose"},
		{"URL flag description", "Remote file URL to download (supports HTTP, HTTPS, FTP, FTPS)"},
		{"Output flag description", "Output path - directory or file path (default: current directory)"},
		{"Segments flag description", "Number of parallel download segments (1-32, recommended: 4-8)"},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(output, tc.expected) {
				t.Errorf("Help output missing expected content: %s", tc.expected)
				t.Logf("Full output:\n%s", output)
			}
		})
	}
}

func TestDownloadCommandHelp(t *testing.T) {
	// Create a buffer to capture output
	var buf bytes.Buffer
	
	// Create root command and set output to our buffer
	rootCmd := newRoot()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	
	// Execute download help command
	rootCmd.SetArgs([]string{"download", "--help"})
	err := rootCmd.Execute()
	
	if err != nil {
		t.Fatalf("Download help command failed: %v", err)
	}
	
	output := buf.String()
	
	// Test that deprecation notice is prominent
	testCases := []struct {
		name     string
		expected string
	}{
		{"Deprecation notice", "DEPRECATED: This subcommand is deprecated"},
		{"New syntax suggestion", "Please use the new simplified syntax instead:"},
		{"New syntax example", "dr <URL> [options]"},
		{"Migration examples", "dr https://example.com/file.zip"},
		{"Help reference", "For full help on the new interface, run: dr --help"},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(output, tc.expected) {
				t.Errorf("Download help output missing expected content: %s", tc.expected)
				t.Logf("Full output:\n%s", output)
			}
		})
	}
}

func TestHelpWithNoArgs(t *testing.T) {
	// Create a buffer to capture output
	var buf bytes.Buffer
	
	// Create root command and set output to our buffer
	rootCmd := newRoot()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	
	// Execute with no arguments (should show help)
	rootCmd.SetArgs([]string{})
	err := rootCmd.Execute()
	
	// Should not error when showing help
	if err != nil {
		t.Fatalf("Root command with no args failed: %v", err)
	}
	
	output := buf.String()
	
	// Should contain the same help content as --help
	if !strings.Contains(output, "BASIC USAGE:") {
		t.Error("Root command with no args should show help")
	}
	
	if !strings.Contains(output, "COMMON EXAMPLES:") {
		t.Error("Root command with no args should show examples")
	}
}

func TestFlagDescriptionsAreInformative(t *testing.T) {
	// Create a buffer to capture output
	var buf bytes.Buffer
	
	// Create root command and set output to our buffer
	rootCmd := newRoot()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	
	// Execute help command
	rootCmd.SetArgs([]string{"--help"})
	err := rootCmd.Execute()
	
	if err != nil {
		t.Fatalf("Help command failed: %v", err)
	}
	
	output := buf.String()
	
	// Test that flag descriptions include useful information
	flagTests := []struct {
		flag        string
		shouldContain []string
	}{
		{
			"url",
			[]string{"HTTP", "HTTPS", "FTP", "FTPS"},
		},
		{
			"output", 
			[]string{"directory", "file path", "current directory"},
		},
		{
			"segments",
			[]string{"1-32", "recommended", "4-8"},
		},
		{
			"segment-size",
			[]string{"bytes", "auto-calculation"},
		},
		{
			"quiet",
			[]string{"scripting"},
		},
		{
			"verbose",
			[]string{"detailed information"},
		},
	}
	
	for _, test := range flagTests {
		t.Run("Flag_"+test.flag, func(t *testing.T) {
			for _, expected := range test.shouldContain {
				if !strings.Contains(output, expected) {
					t.Errorf("Flag %s description should contain '%s'", test.flag, expected)
				}
			}
		})
	}
}