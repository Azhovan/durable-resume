package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/azhovan/durable-resume/download"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		wantErr error
	}{
		{name: "http ok", rawURL: "http://example.com/file.bin"},
		{name: "https ok", rawURL: "https://example.com/file.bin"},
		{name: "empty", rawURL: "", wantErr: download.ErrNoURL},
		{name: "ftp scheme", rawURL: "ftp://example.com/file.bin", wantErr: download.ErrUnsupportedScheme},
		{name: "file scheme", rawURL: "file:///tmp/file.bin", wantErr: download.ErrUnsupportedScheme},
		{name: "no scheme", rawURL: "example.com/file.bin", wantErr: download.ErrUnsupportedScheme},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateURL(tt.rawURL)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestParseHeaders(t *testing.T) {
	t.Parallel()

	t.Run("nil input yields nil header", func(t *testing.T) {
		t.Parallel()
		h, err := parseHeaders(nil)
		require.NoError(t, err)
		assert.Nil(t, h)
	})

	t.Run("single header", func(t *testing.T) {
		t.Parallel()
		h, err := parseHeaders([]string{"Authorization: Bearer xyz"})
		require.NoError(t, err)
		assert.Equal(t, "Bearer xyz", h.Get("Authorization"))
	})

	t.Run("trims whitespace", func(t *testing.T) {
		t.Parallel()
		h, err := parseHeaders([]string{"  X-Key  :   value  "})
		require.NoError(t, err)
		assert.Equal(t, "value", h.Get("X-Key"))
	})

	t.Run("repeated headers accumulate", func(t *testing.T) {
		t.Parallel()
		h, err := parseHeaders([]string{"X-Tag: a", "X-Tag: b"})
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, h.Values("X-Tag"))
	})

	t.Run("value with colon preserved", func(t *testing.T) {
		t.Parallel()
		h, err := parseHeaders([]string{"X-Range: bytes:0-1"})
		require.NoError(t, err)
		assert.Equal(t, "bytes:0-1", h.Get("X-Range"))
	})

	t.Run("missing colon errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseHeaders([]string{"bogus"})
		require.Error(t, err)
	})

	t.Run("empty key errors", func(t *testing.T) {
		t.Parallel()
		_, err := parseHeaders([]string{": value"})
		require.Error(t, err)
	})
}

func TestParseChecksum(t *testing.T) {
	t.Parallel()

	const validHex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	tests := []struct {
		name     string
		input    string
		wantAlgo string
		wantHex  string
		wantErr  bool
	}{
		{name: "empty is empty checksum", input: "", wantAlgo: "", wantHex: ""},
		{name: "valid sha256", input: "sha256:" + validHex, wantAlgo: "sha256", wantHex: validHex},
		{name: "uppercase hex lowered", input: "sha256:" + strings.ToUpper(validHex), wantAlgo: "sha256", wantHex: validHex},
		{name: "unknown algo md5", input: "md5:" + validHex, wantErr: true},
		{name: "no colon", input: validHex, wantErr: true},
		{name: "odd length hex", input: "sha256:abc", wantErr: true},
		{name: "non-hex chars", input: "sha256:" + strings.Repeat("z", 64), wantErr: true},
		{name: "too short", input: "sha256:abcd", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := parseChecksum(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAlgo, c.Algo)
			assert.Equal(t, tt.wantHex, c.Hex)
			if tt.input == "" {
				assert.True(t, c.Empty())
			}
		})
	}
}

func TestFormatVersion(t *testing.T) {
	t.Parallel()
	s := formatVersion("1.2.3", "abc123", "2026-06-28")
	assert.Contains(t, s, "1.2.3")
	assert.Contains(t, s, "abc123")
	assert.Contains(t, s, "2026-06-28")
}

func TestNewRootCmdVersion(t *testing.T) {
	t.Parallel()
	cmd := NewRootCmd("1.2.3", "abc123", "2026-06-28")
	assert.Contains(t, cmd.Version, "1.2.3")
	assert.Contains(t, cmd.Version, "abc123")
	assert.Contains(t, cmd.Version, "2026-06-28")
}

func TestNewRootCmdArgsValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "zero args", args: []string{}, wantErr: true},
		{name: "two args", args: []string{"http://a/x", "http://b/y"}, wantErr: true},
		{name: "one arg", args: []string{"http://a/x"}, wantErr: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := NewRootCmd("v", "r", "d")
			err := cmd.Args(cmd, tt.args)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNewRootCmdFlagDefaults(t *testing.T) {
	t.Parallel()
	cmd := NewRootCmd("v", "r", "d")
	flags := cmd.Flags()

	concurrency, err := flags.GetInt("concurrency")
	require.NoError(t, err)
	assert.Equal(t, download.DefaultConcurrency, concurrency)
	assert.Equal(t, 4, concurrency)

	retries, err := flags.GetInt("retries")
	require.NoError(t, err)
	assert.Equal(t, download.DefaultRetries, retries)
	assert.Equal(t, 3, retries)

	resume, err := flags.GetBool("resume")
	require.NoError(t, err)
	assert.True(t, resume)

	timeout, err := flags.GetDuration("timeout")
	require.NoError(t, err)
	assert.Equal(t, download.DefaultTimeout, timeout)
	assert.Equal(t, 30*time.Second, timeout)

	quiet, err := flags.GetBool("quiet")
	require.NoError(t, err)
	assert.False(t, quiet)

	verbose, err := flags.GetBool("verbose")
	require.NoError(t, err)
	assert.False(t, verbose)

	force, err := flags.GetBool("force")
	require.NoError(t, err)
	assert.False(t, force)
}

func TestNewRootCmdForceFlag(t *testing.T) {
	t.Parallel()
	cmd := NewRootCmd("v", "r", "d")
	f := cmd.Flags().Lookup("force")
	require.NotNil(t, f, "force flag should be registered")
	assert.Equal(t, "f", f.Shorthand)
	assert.Equal(t, "false", f.DefValue)
}

// TestRunEWiresForceIntoOptions exercises the RunE closure end-to-end and asserts
// the --force flag is copied into download.Options.Force (default false). It
// intercepts runFunc so no real download occurs; this pins the flag->struct wiring
// (root.go `Force: force`) that the flag-registration tests above do not cover, so a
// regression dropping that assignment (making --force a silent no-op) would fail.
func TestRunEWiresForceIntoOptions(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantForce bool
	}{
		{name: "no flag defaults false", args: []string{"http://example.com/file.bin"}, wantForce: false},
		{name: "--force sets true", args: []string{"http://example.com/file.bin", "--force"}, wantForce: true},
		{name: "-f sets true", args: []string{"http://example.com/file.bin", "-f"}, wantForce: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: swaps the package-level runFunc.
			orig := runFunc
			t.Cleanup(func() { runFunc = orig })

			var captured download.Options
			var called bool
			runFunc = func(_ context.Context, opts download.Options) error {
				called = true
				captured = opts
				return nil
			}

			cmd := NewRootCmd("v", "r", "d")
			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.Execute())

			require.True(t, called, "runFunc must be invoked by RunE")
			assert.Equal(t, tt.wantForce, captured.Force)
		})
	}
}

// ensure the command type is what callers expect.
var _ *cobra.Command = NewRootCmd("", "", "")
