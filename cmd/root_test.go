package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azhovan/durable-resume/download"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validChecksumHex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// stubRunFunc swaps the package-level runFunc for the duration of the test,
// restoring it afterward. The provided fn receives each invocation's Options.
func stubRunFunc(t *testing.T, fn func(ctx context.Context, opts download.Options) error) {
	t.Helper()
	orig := runFunc
	t.Cleanup(func() { runFunc = orig })
	runFunc = fn
}

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

	// With cobra.ArbitraryArgs the Args stage no longer rejects any count; URL
	// count semantics (zero => error, batch => continue-on-error) moved to RunE
	// and are exercised by the TestRunE_* tests below.
	tests := []struct {
		name string
		args []string
	}{
		{name: "zero args", args: []string{}},
		{name: "two args", args: []string{"http://a/x", "http://b/y"}},
		{name: "one arg", args: []string{"http://a/x"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := NewRootCmd("v", "r", "d")
			require.NoError(t, cmd.Args(cmd, tt.args))
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

// TestParseProxy is a pure, table-driven validator test mirroring the accepted
// proxy schemes. Errors must wrap download.ErrInvalidProxy so callers can branch
// on it via errors.Is.
func TestParseProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty ok", "", false},
		{"http", "http://p:8080", false},
		{"https", "https://p", false},
		{"socks5", "socks5://p:1080", false},
		{"socks5h", "socks5h://p:1080", false},
		{"ftp scheme", "ftp://p", true},
		{"empty scheme", "://", true},
		{"missing host", "http://", true},
		{"port only no host", "http://:8080", true},
		{"garbage", "garbage", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, err := parseProxy(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, download.ErrInvalidProxy)
				return
			}
			require.NoError(t, err)
			if tt.raw == "" {
				assert.Nil(t, u, "empty proxy yields a nil URL")
			} else {
				require.NotNil(t, u)
			}
		})
	}
}

// TestRunEWiresProxyIntoOptions pins the --proxy flag -> download.Options.Proxy
// wiring and the fail-fast validation in RunE: a valid value reaches Options and
// runFunc is invoked; an invalid value fails before any download so runFunc is
// never called.
func TestRunEWiresProxyIntoOptions(t *testing.T) {
	t.Run("good proxy wired and download started", func(t *testing.T) {
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
		cmd.SetArgs([]string{"--proxy", "http://p:8080", "https://example.com/file"})
		require.NoError(t, cmd.Execute())

		require.True(t, called, "runFunc must be invoked for a valid proxy")
		assert.Equal(t, "http://p:8080", captured.Proxy)
	})

	t.Run("bad proxy fails fast with no download", func(t *testing.T) {
		orig := runFunc
		t.Cleanup(func() { runFunc = orig })

		var called bool
		runFunc = func(_ context.Context, _ download.Options) error {
			called = true
			return nil
		}

		cmd := NewRootCmd("v", "r", "d")
		cmd.SetArgs([]string{"--proxy", "ftp://nope", "https://example.com/file"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.ErrorIs(t, err, download.ErrInvalidProxy)
		assert.False(t, called, "no download must start when --proxy is invalid")
	})
}

func TestReadURLsFromFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty", input: "", want: nil},
		{name: "single", input: "http://a\n", want: []string{"http://a"}},
		{name: "no trailing newline", input: "http://a", want: []string{"http://a"}},
		{
			name:  "trims spaces",
			input: "  http://a  \n\thttp://b\t\n",
			want:  []string{"http://a", "http://b"},
		},
		{
			name:  "skips blank and comment lines",
			input: "http://a\n\n   \n# a comment\n   # indented comment\nhttp://b\n",
			want:  []string{"http://a", "http://b"},
		},
		{
			name:  "crlf and order",
			input: "   http://a   \r\n# c\n\nhttp://b\n",
			want:  []string{"http://a", "http://b"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readURLsFromFile(strings.NewReader(tt.input))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAssembleURLs_StdinDash(t *testing.T) {
	t.Parallel()
	cmd := NewRootCmd("v", "r", "d")
	cmd.SetIn(strings.NewReader("http://b\nhttp://c\n"))
	got, err := assembleURLs(cmd, []string{"http://a"}, "-")
	require.NoError(t, err)
	assert.Equal(t, []string{"http://a", "http://b", "http://c"}, got)
}

func TestAssembleURLs_File(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "urls.txt")
	require.NoError(t, os.WriteFile(path, []byte("http://b\n# comment\nhttp://c\n"), 0o600))

	cmd := NewRootCmd("v", "r", "d")
	got, err := assembleURLs(cmd, []string{"http://a"}, path)
	require.NoError(t, err)
	assert.Equal(t, []string{"http://a", "http://b", "http://c"}, got)
}

func TestAssembleURLs_OpenError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := NewRootCmd("v", "r", "d")
	_, err := assembleURLs(cmd, []string{"http://a"}, filepath.Join(dir, "nope.txt"))
	require.Error(t, err)
}

func TestRunE_SingleURL_Unchanged(t *testing.T) {
	var captured download.Options
	var calls int
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		calls++
		captured = opts
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://example.com/file.bin", "-o", "/tmp/out.bin"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, 1, calls)
	assert.Equal(t, "http://example.com/file.bin", captured.URL)
	assert.Equal(t, "/tmp/out.bin", captured.Output)
	assert.Empty(t, errBuf.String())
}

func TestRunE_SingleInvalidURL(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetArgs([]string{"ftp://x/file.bin"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, download.ErrUnsupportedScheme)
	assert.Equal(t, 0, calls)
}

func TestRunE_SingleRunError(t *testing.T) {
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		return download.ErrChecksumMismatch
	})

	cmd := NewRootCmd("v", "r", "d")
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://example.com/file.bin"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, download.ErrChecksumMismatch)
	assert.Empty(t, errBuf.String())
}

func TestRunE_SingleChecksumPassthrough(t *testing.T) {
	var captured download.Options
	var calls int
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		calls++
		captured = opts
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://example.com/file.bin", "--checksum", "sha256:" + validChecksumHex})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, 1, calls)
	assert.Equal(t, "sha256", captured.Checksum.Algo)
	assert.Empty(t, errBuf.String())
}

func TestRunE_MultiPositional_Order(t *testing.T) {
	dir := t.TempDir()
	var capturedURLs []string
	var capturedOutputs []string
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		capturedURLs = append(capturedURLs, opts.URL)
		capturedOutputs = append(capturedOutputs, opts.Output)
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "http://c/z", "-o", dir})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []string{"http://a/x", "http://b/y", "http://c/z"}, capturedURLs)
	for _, out := range capturedOutputs {
		assert.Equal(t, dir, out)
	}
}

func TestRunE_InputFileAppended(t *testing.T) {
	dir := t.TempDir()
	listPath := filepath.Join(dir, "urls.txt")
	require.NoError(t, os.WriteFile(listPath, []byte("http://b/y\nhttp://c/z\n"), 0o600))
	outDir := t.TempDir()

	var capturedURLs []string
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		capturedURLs = append(capturedURLs, opts.URL)
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://a/x", "-i", listPath, "-o", outDir})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, []string{"http://a/x", "http://b/y", "http://c/z"}, capturedURLs)
}

func TestRunE_ZeroURLs(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, download.ErrNoURL)
	assert.Equal(t, 0, calls)
}

func TestRunE_MultiOutputRegularFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "regular.bin")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))

	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "-o", filePath})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a directory")
	assert.Equal(t, 0, calls)
}

func TestRunE_MultiOutputDir(t *testing.T) {
	dir := t.TempDir()
	var capturedOutputs []string
	var calls int
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		calls++
		capturedOutputs = append(capturedOutputs, opts.Output)
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "-o", dir})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, 2, calls)
	for _, out := range capturedOutputs {
		assert.Equal(t, dir, out)
	}
}

func TestRunE_ChecksumMultiRejected(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "--checksum", "sha256:" + validChecksumHex})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
	assert.Equal(t, 0, calls)
}

func TestRunE_ContinueOnError(t *testing.T) {
	dir := t.TempDir()
	var capturedURLs []string
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		capturedURLs = append(capturedURLs, opts.URL)
		if opts.URL == "http://b/y" {
			return assert.AnError
		}
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "http://c/z", "-o", dir})
	err := cmd.Execute()
	require.Error(t, err)

	assert.Equal(t, []string{"http://a/x", "http://b/y", "http://c/z"}, capturedURLs)
	assert.Contains(t, errBuf.String(), "dr: 2 of 3 downloads succeeded")
	assert.Contains(t, errBuf.String(), "dr: http://b/y:")
}

func TestRunE_AllSuccess(t *testing.T) {
	dir := t.TempDir()
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "http://c/z", "-o", dir})
	require.NoError(t, cmd.Execute())

	assert.Contains(t, errBuf.String(), "dr: 3 of 3 downloads succeeded")
	assert.NotContains(t, errBuf.String(), "dr: http://")
}

func TestRunE_CancelMidBatch(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		cancel() // cancel at the end of the first invocation
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetContext(ctx)
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "http://c/z", "-o", dir})
	err := cmd.Execute()
	require.Error(t, err)

	assert.Equal(t, 1, calls, "later URLs must not be launched after cancellation")
	assert.Contains(t, errBuf.String(), "dr: 1 of 3 downloads succeeded")
	// The two not-attempted URLs must be reported as per-URL failures with a
	// context-cancellation error (guards against recording them with a nil error,
	// which would miscount them as successes).
	assert.Contains(t, errBuf.String(), "dr: http://b/y:")
	assert.Contains(t, errBuf.String(), "dr: http://c/z:")
	assert.Contains(t, errBuf.String(), context.Canceled.Error())
}

func TestNewRootCmdInputFileFlag(t *testing.T) {
	t.Parallel()
	cmd := NewRootCmd("v", "r", "d")
	f := cmd.Flags().Lookup("input-file")
	require.NotNil(t, f, "input-file flag should be registered")
	assert.Equal(t, "i", f.Shorthand)
	assert.Equal(t, "", f.DefValue)
}

// TestRunE_BatchEndToEnd exercises the batch path against a real httptest server
// using the default runFunc (download.Run), confirming two files land in the
// output directory and per-file "saved to" lines are emitted. Deterministic and
// race-clean: no real network, no sleeps.
func TestRunE_BatchEndToEnd(t *testing.T) {
	const (
		bodyA = "alpha contents"
		bodyB = "beta contents"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.txt":
			w.Header().Set("Content-Length", "14")
			_, _ = w.Write([]byte(bodyA))
		case "/b.txt":
			_, _ = w.Write([]byte(bodyB))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cmd := NewRootCmd("v", "r", "d")
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{srv.URL + "/a.txt", srv.URL + "/b.txt", "-o", dir, "-q"})
	require.NoError(t, cmd.Execute())

	gotA, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, bodyA, string(gotA))
	gotB, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	require.NoError(t, err)
	assert.Equal(t, bodyB, string(gotB))

	assert.Contains(t, errBuf.String(), "dr: 2 of 2 downloads succeeded")
}

// TestRunE_BatchInvalidURLContinues confirms requirement 6 at the RunE/batch
// boundary: an invalid URL among valid ones is a per-URL failure (counted,
// reported) while the valid URLs are still attempted. Guards against regressions
// that either fail-fast the whole batch on the first bad URL or skip per-URL
// validation (passing ftp:// straight to runFunc).
func TestRunE_BatchInvalidURLContinues(t *testing.T) {
	var capturedURLs []string
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		capturedURLs = append(capturedURLs, opts.URL)
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"http://a/x", "ftp://bad", "http://c/z", "-o", t.TempDir()})
	err := cmd.Execute()
	require.Error(t, err)

	// Only the valid URLs reach runFunc; ftp://bad is never downloaded.
	assert.Equal(t, []string{"http://a/x", "http://c/z"}, capturedURLs)
	assert.Contains(t, errBuf.String(), "dr: 2 of 3 downloads succeeded")
	assert.Contains(t, errBuf.String(), "dr: ftp://bad:")
	assert.Contains(t, errBuf.String(), download.ErrUnsupportedScheme.Error())
}

// TestRunE_BadHeaderFailsFast confirms requirement 6's global fail-fast: a
// malformed -H header (or bad --checksum syntax) aborts before assembleURLs and
// the batch loop, so runFunc is never invoked even with multiple URLs.
func TestRunE_BadHeaderFailsFast(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "bad header",
			args: []string{"http://a/x", "http://b/y", "-H", "bogus"},
		},
		{
			name: "bad checksum syntax",
			args: []string{"http://a/x", "http://b/y", "--checksum", "sha256:nothex"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			stubRunFunc(t, func(_ context.Context, _ download.Options) error {
				calls++
				return nil
			})

			cmd := NewRootCmd("v", "r", "d")
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Equal(t, 0, calls, "no download must be attempted on a parse error")
		})
	}
}

// TestRunE_StdoutChecksumRejected confirms -o - with --checksum is rejected at the
// cmd layer before any download (runFunc is never called).
func TestRunE_StdoutChecksumRejected(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://example.com/file.bin", "-o", "-", "--checksum", "sha256:" + validChecksumHex})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
	assert.Contains(t, err.Error(), "stdout")
	assert.Equal(t, 0, calls, "no download must be attempted when -o - is combined with --checksum")
}

// TestRunE_StdoutMultiURLRejected confirms -o - with multiple URLs is rejected
// before any download (runFunc is never called).
func TestRunE_StdoutMultiURLRejected(t *testing.T) {
	var calls int
	stubRunFunc(t, func(_ context.Context, _ download.Options) error {
		calls++
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://a/x", "http://b/y", "-o", "-"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple URLs")
	assert.Equal(t, 0, calls, "no download must be attempted when -o - is combined with multiple URLs")
}

// TestRunE_StdoutSingleURLWiring confirms that a single URL with -o - reaches
// runFunc with Output=="-", Out routed to os.Stderr (so diagnostics never corrupt
// the pipe), and Data left nil (download defaults the data sink to os.Stdout).
func TestRunE_StdoutSingleURLWiring(t *testing.T) {
	var captured download.Options
	var calls int
	stubRunFunc(t, func(_ context.Context, opts download.Options) error {
		calls++
		captured = opts
		return nil
	})

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http://example.com/file.bin", "-o", "-"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, 1, calls)
	assert.Equal(t, "-", captured.Output)
	assert.Same(t, os.Stderr, captured.Out)
	assert.Nil(t, captured.Data)
}

func TestNewRootCmdLimitRateFlag(t *testing.T) {
	t.Parallel()
	cmd := NewRootCmd("v", "r", "d")
	f := cmd.Flags().Lookup("limit-rate")
	require.NotNil(t, f, "limit-rate flag should be registered")
	assert.Equal(t, "", f.DefValue)
}

// TestRunE_LimitRateWiring confirms a good --limit-rate value is parsed and wired
// into download.Options.LimitRate (bytes/sec), via the runFunc capture seam.
func TestRunE_LimitRateWiring(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int64
	}{
		{name: "no flag unlimited", args: []string{"http://example.com/file.bin"}, want: 0},
		{name: "500k", args: []string{"http://example.com/file.bin", "--limit-rate", "500k"}, want: 512000},
		{name: "1M", args: []string{"http://example.com/file.bin", "--limit-rate", "1M"}, want: 1048576},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var captured download.Options
			var called bool
			stubRunFunc(t, func(_ context.Context, opts download.Options) error {
				called = true
				captured = opts
				return nil
			})

			cmd := NewRootCmd("v", "r", "d")
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.Execute())

			require.True(t, called)
			assert.Equal(t, tt.want, captured.LimitRate)
		})
	}
}

// TestRunE_LimitRateBadValueFailsFast confirms a malformed --limit-rate aborts
// RunE before any download (runFunc is never called).
func TestRunE_LimitRateBadValueFailsFast(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown unit", args: []string{"http://a/x", "--limit-rate", "1X"}},
		{name: "negative", args: []string{"http://a/x", "--limit-rate", "-5"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			stubRunFunc(t, func(_ context.Context, _ download.Options) error {
				calls++
				return nil
			})

			cmd := NewRootCmd("v", "r", "d")
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Equal(t, 0, calls, "no download must start on a bad --limit-rate")
		})
	}
}

// TestRunEWiresMirrorsIntoOptions pins the --mirror/-m flag -> Options.Mirrors
// wiring (order preserved) via the runFunc interception seam.
func TestRunEWiresMirrorsIntoOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantURL     string
		wantMirrors []string
	}{
		{
			name:        "no mirror flag",
			args:        []string{"http://primary.example/file"},
			wantURL:     "http://primary.example/file",
			wantMirrors: nil,
		},
		{
			name:        "single -m",
			args:        []string{"http://primary.example/file", "-m", "http://b.example/file"},
			wantURL:     "http://primary.example/file",
			wantMirrors: []string{"http://b.example/file"},
		},
		{
			name:        "repeatable --mirror in order",
			args:        []string{"http://primary.example/file", "-m", "http://a.example/f", "-m", "http://b.example/f"},
			wantURL:     "http://primary.example/file",
			wantMirrors: []string{"http://a.example/f", "http://b.example/f"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var captured download.Options
			var called bool
			stubRunFunc(t, func(_ context.Context, opts download.Options) error {
				called = true
				captured = opts
				return nil
			})

			cmd := NewRootCmd("v", "r", "d")
			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.Execute())

			require.True(t, called, "runFunc must be invoked by RunE")
			assert.Equal(t, tt.wantURL, captured.URL)
			assert.Equal(t, tt.wantMirrors, captured.Mirrors)
		})
	}
}

// TestRunEMirrorRejections covers the invalid combinations: bad-scheme mirror,
// --mirror with multiple positional URLs, and --mirror with -i (batch).
func TestRunEMirrorRejections(t *testing.T) {
	t.Run("bad scheme mirror", func(t *testing.T) {
		stubRunFunc(t, func(_ context.Context, _ download.Options) error {
			t.Fatal("runFunc must not be invoked for a bad-scheme mirror")
			return nil
		})
		cmd := NewRootCmd("v", "r", "d")
		cmd.SetArgs([]string{"http://primary.example/file", "-m", "ftp://nope/x"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.ErrorIs(t, err, download.ErrUnsupportedScheme)
	})

	t.Run("mirror with multiple positionals (batch)", func(t *testing.T) {
		stubRunFunc(t, func(_ context.Context, _ download.Options) error {
			t.Fatal("runFunc must not be invoked when --mirror is combined with batch")
			return nil
		})
		cmd := NewRootCmd("v", "r", "d")
		cmd.SetArgs([]string{"http://a.example/f", "http://b.example/f", "-m", "http://c.example/f", "-o", t.TempDir()})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--mirror requires exactly one URL")
	})

	t.Run("mirror with -i input file", func(t *testing.T) {
		dir := t.TempDir()
		listFile := filepath.Join(dir, "urls.txt")
		require.NoError(t, os.WriteFile(listFile, []byte("http://a.example/f\nhttp://b.example/f\n"), 0o644))

		stubRunFunc(t, func(_ context.Context, _ download.Options) error {
			t.Fatal("runFunc must not be invoked when --mirror is combined with -i")
			return nil
		})
		cmd := NewRootCmd("v", "r", "d")
		cmd.SetArgs([]string{"-i", listFile, "-m", "http://c.example/f", "-o", dir})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--mirror requires exactly one URL")
	})
}

// ensure the command type is what callers expect.
var _ *cobra.Command = NewRootCmd("", "", "")
