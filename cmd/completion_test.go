package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/azhovan/durable-resume/v3/download"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompletion_EmitsScriptPerShell verifies each shell produces a non-empty
// script carrying that generator's stable top-level marker. Markers confirmed in
// cobra v1.8.0 generator output.
func TestCompletion_EmitsScriptPerShell(t *testing.T) {
	t.Parallel()

	cases := []struct {
		shell  string
		marker string
	}{
		{"bash", "__start_dr"},
		{"zsh", "#compdef dr"},
		{"fish", "__dr_perform_completion"},
		{"powershell", "Register-ArgumentCompleter"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.shell, func(t *testing.T) {
			t.Parallel()
			c := NewRootCmd("v", "r", "d")
			var out bytes.Buffer
			c.SetOut(&out)
			c.SetArgs([]string{"completion", tc.shell})
			require.NoError(t, c.Execute())
			require.NotEmpty(t, out.String())
			require.Contains(t, out.String(), tc.marker)
		})
	}
}

// TestCompletion_UnknownShellErrors: an unsupported shell is rejected by
// OnlyValidArgs with a message naming the bad argument.
func TestCompletion_UnknownShellErrors(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	c.SetArgs([]string{"completion", "tcsh"})
	err := c.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "tcsh")
}

// TestCompletion_NoShellErrors / TooManyShells: ExactArgs(1) rejects 0 and 2.
func TestCompletion_NoShellErrors(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	c.SetArgs([]string{"completion"})
	require.Error(t, c.Execute())
}

func TestCompletion_TooManyShells(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	c.SetArgs([]string{"completion", "bash", "zsh"})
	require.Error(t, c.Execute())
}

// TestRoot_StillDownloads_completionWiringDidNotBreakDispatch proves that adding
// the completion subcommand did not steal root dispatch: a URL still reaches
// runFunc with that URL.
func TestRoot_StillDownloads_completionWiringDidNotBreakDispatch(t *testing.T) {
	// Not parallel: swaps the package-level runFunc.
	orig := runFunc
	t.Cleanup(func() { runFunc = orig })

	var called bool
	var gotURL string
	runFunc = func(_ context.Context, o download.Options) error {
		called = true
		gotURL = o.URL
		return nil
	}

	c := NewRootCmd("v", "r", "d")
	c.SetArgs([]string{"https://example.com/f"})
	require.NoError(t, c.Execute())
	require.True(t, called)
	require.Equal(t, "https://example.com/f", gotURL)
}

// TestFlagCompletion_PathFlagsRegistered: -o/-i register a func returning
// ShellCompDirectiveDefault (NoFileComp bit clear) so the shell does file
// completion. This is exactly what MarkFlagFilename would FAIL (it registers no
// func, so GetFlagCompletionFunc returns ok==false).
func TestFlagCompletion_PathFlagsRegistered(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	for _, f := range []string{"output", "input-file"} {
		fn, ok := c.GetFlagCompletionFunc(f)
		require.True(t, ok, "flag %q must have a registered completion func", f)
		_, d := fn(c, nil, "")
		require.Equal(t, cobra.ShellCompDirectiveDefault, d)
		require.Zero(t, d&cobra.ShellCompDirectiveNoFileComp)
	}
}

// TestFlagCompletion_ValueFlagsNoFile: value flags never offer filenames.
func TestFlagCompletion_ValueFlagsNoFile(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	for _, f := range []string{"concurrency", "timeout", "retries", "header", "mirror", "limit-rate", "proxy"} {
		fn, ok := c.GetFlagCompletionFunc(f)
		require.True(t, ok, "flag %q must have a registered completion func", f)
		_, d := fn(c, nil, "")
		require.NotZero(t, d&cobra.ShellCompDirectiveNoFileComp, "flag %q must set NoFileComp", f)
	}
}

// TestFlagCompletion_ChecksumHint: hints "sha256:" while the user is typing a
// prefix of it; otherwise nothing. Always NoFileComp; NoSpace for the hint.
func TestFlagCompletion_ChecksumHint(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	fn, ok := c.GetFlagCompletionFunc("checksum")
	require.True(t, ok)

	vals, d := fn(c, nil, "")
	require.Equal(t, []string{"sha256:"}, vals)
	require.NotZero(t, d&cobra.ShellCompDirectiveNoFileComp)
	require.NotZero(t, d&cobra.ShellCompDirectiveNoSpace)

	vals2, d2 := fn(c, nil, "sha256:ab")
	require.Nil(t, vals2)
	require.NotZero(t, d2&cobra.ShellCompDirectiveNoFileComp)
}

// TestPositionalCompletion_NoFile: positional URLs offer no filenames.
func TestPositionalCompletion_NoFile(t *testing.T) {
	t.Parallel()
	c := NewRootCmd("v", "r", "d")
	require.NotNil(t, c.ValidArgsFunction)
	_, d := c.ValidArgsFunction(c, nil, "")
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, d)
}

// TestComplete_EndToEnd_directive exercises the real __complete machinery through
// Execute and reads the trailing ":<directive>" line. NoFileComp==4, Default==0.
//
// Both subcases are FALSIFIABLE w.r.t. their registration. cobra v1.8.0 sets the
// baseline directive to ShellCompDirectiveDefault (:0) for any flag awaiting a
// value (completions.go:435) and only overrides it when a func is registered
// (line ~511). So asserting :0 for an unregistered flag proves nothing. Instead:
//   - --proxy (NoFileComp): an unregistered flag would stay at the :0 baseline, so
//     observing :4 proves cobra.NoFileCompletions actually ran.
//   - --checksum (sentinel candidate): the unregistered default offers files and
//     emits no "sha256:" line, so observing the sentinel proves our hint func ran.
func TestComplete_EndToEnd_directive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		flag     string
		contains []string
	}{
		{"value flag proxy -> NoFileComp", "--proxy", []string{":4"}},
		{"checksum -> sentinel hint + NoFileComp", "--checksum", []string{"sha256:", ":6"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewRootCmd("v", "r", "d")
			var out bytes.Buffer
			c.SetOut(&out)
			c.SetArgs([]string{cobra.ShellCompRequestCmd, tc.flag, ""})
			require.NoError(t, c.Execute())
			for _, want := range tc.contains {
				assert.Contains(t, out.String(), want)
			}
		})
	}
}
