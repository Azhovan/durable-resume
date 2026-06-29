package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/azhovan/durable-resume/v3/download"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunEWiresAuthIntoOptions pins the flag->Options.Auth wiring: --user/--netrc
// populate a non-nil Auth, and their absence leaves Auth nil (the no-auth path).
func TestRunEWiresAuthIntoOptions(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantNonNil  bool
		wantRunCall bool
	}{
		{name: "no auth flags = nil Auth", args: []string{"https://example.com/f"}, wantNonNil: false, wantRunCall: true},
		{name: "--user sets Auth", args: []string{"--user", "u:p", "https://example.com/f"}, wantNonNil: true, wantRunCall: true},
		{name: "--netrc-file sets Auth", args: []string{"--netrc-file", writeTempNetrc(t), "https://example.com/f"}, wantNonNil: true, wantRunCall: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
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
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.Execute())

			require.Equal(t, tt.wantRunCall, called)
			if tt.wantNonNil {
				assert.NotNil(t, captured.Auth)
			} else {
				assert.Nil(t, captured.Auth)
			}
		})
	}
}

// TestRunEUserEmptyUsernameFailsFast: --user with an empty username (":pass")
// returns download.ErrInvalidUser BEFORE any runFunc call, and never echoes the
// password.
func TestRunEUserEmptyUsernameFailsFast(t *testing.T) {
	orig := runFunc
	t.Cleanup(func() { runFunc = orig })
	called := false
	runFunc = func(_ context.Context, _ download.Options) error {
		called = true
		return nil
	}

	cmd := NewRootCmd("v", "r", "d")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--user", ":supersecret", "https://example.com/f"})
	err := cmd.Execute()
	require.ErrorIs(t, err, download.ErrInvalidUser)
	assert.False(t, called, "runFunc must NOT be called when --user is invalid")
	assert.NotContains(t, err.Error(), "supersecret")
}

// TestJSONRecordRedactsUserinfo: a --json single record for https://user:pass@host
// has a redacted url field and no password value/key.
func TestJSONRecordRedactsUserinfo(t *testing.T) {
	orig := runFunc
	t.Cleanup(func() { runFunc = orig })
	// cmd sets res.URL = RedactURL(raw) BEFORE calling runFunc, so the stub need
	// not touch the Result; the emitted record's url is already redacted.
	runFunc = func(_ context.Context, _ download.Options) error { return nil }

	cmd := NewRootCmd("v", "r", "d")
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--json", "https://alice:supersecret@example.com/f"})
	require.NoError(t, cmd.Execute())

	line := stdout.String()
	assert.Contains(t, line, "redacted@example.com")
	assert.NotContains(t, line, "supersecret")
	assert.NotContains(t, line, "alice:supersecret")
	assert.NotContains(t, line, "password")
}

// TestWriteSummaryRedactsUserinfo pins that the non-JSON batch summary (the only
// sibling URL-echo path that previously printed the raw URL) redacts userinfo: a
// failed download for https://user:pass@host must show redacted@host, never the
// cleartext password.
func TestWriteSummaryRedactsUserinfo(t *testing.T) {
	results := []batchResult{
		{url: "https://alice:supersecret@example.com/f", err: errBoom},
		{url: "https://example.com/ok", err: nil},
	}
	var buf bytes.Buffer
	failed := writeSummary(&buf, results)
	assert.True(t, failed)

	out := buf.String()
	assert.Contains(t, out, "redacted@example.com")
	assert.NotContains(t, out, "supersecret")
	assert.NotContains(t, out, "alice:supersecret")
}

var errBoom = errSentinel("boom")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

func writeTempNetrc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".netrc")
	require.NoError(t, os.WriteFile(p, []byte("machine example.com login u password p\n"), 0o600))
	return p
}
