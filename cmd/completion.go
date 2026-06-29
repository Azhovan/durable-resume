package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newCompletionCmd builds the single allowed helper subcommand, `dr completion
// [bash|zsh|fish|powershell]`, which prints a shell-completion script to stdout.
// dr is otherwise a no-subcommand binary; this lone exception is the conventional
// pattern that gh, kubectl, and similar polished CLIs follow. It does NOT change
// how `dr <url> [flags]` is parsed: only the literal first token "completion"
// dispatches here; every other first token (including a URL) reaches the root
// RunE. The scripts are generated from cmd.Root(), so the binary name is `dr`
// and every per-flag/positional completion registered in NewRootCmd is baked in.
//
// Why explicit (not cobra's auto-added command): cobra v1.8.0
// InitDefaultCompletionCmd returns early when the root has no subcommands
// (!c.HasSubCommands()), so a no-subcommand root like dr gets NO auto completion
// command. We add our own.
func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate a shell completion script",
		Long: `Generate a shell completion script for dr.

dr has no other subcommands; "completion" is the single conventional helper
(as in gh/kubectl) and never changes how "dr <url> [flags]" behaves.

Bash:
  source <(dr completion bash)                                        # current shell
  dr completion bash | sudo tee /etc/bash_completion.d/dr >/dev/null  # Linux, persist
  dr completion bash > $(brew --prefix)/etc/bash_completion.d/dr      # macOS, persist

Zsh:
  # once, in ~/.zshrc: autoload -U compinit; compinit
  dr completion zsh > "${fpath[1]}/_dr"                               # Linux, persist
  dr completion zsh > $(brew --prefix)/share/zsh/site-functions/_dr   # macOS, persist
  source <(dr completion zsh)                                         # current shell

Fish:
  dr completion fish | source                                        # current shell
  dr completion fish > ~/.config/fish/completions/dr.fish            # persist

PowerShell:
  dr completion powershell | Out-String | Invoke-Expression          # current session
  # persist: add the line above to your $PROFILE`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		// ExactArgs(1) gives a useful count error for none/too-many;
		// OnlyValidArgs rejects an unknown shell with cobra's
		// 'invalid argument "tcsh" for "dr completion"'.
		Args:              cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgsFunction: cobra.FixedCompletions([]string{"bash", "zsh", "fish", "powershell"}, cobra.ShellCompDirectiveNoFileComp),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Generate from the ROOT command so the script targets `dr` and
			// carries every registered flag/positional completion. Write to
			// OutOrStdout so tests can capture it via SetOut.
			root := cmd.Root()
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				// V2 delegates value completion to the __complete Go machinery
				// (honors our RegisterFlagCompletionFunc); requires bash-completion v2.
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(out)
			default:
				// Unreachable given Args validation, but explicit so a future
				// ValidArgs edit cannot silently emit nothing.
				return fmt.Errorf("unsupported shell %q: must be one of bash, zsh, fish, powershell", args[0])
			}
		},
	}
}

// registerCompletions wires per-flag and positional completion onto the root
// command, then attaches the completion helper. Called once at the END of
// NewRootCmd, after every flag is defined. Path flags get file completion (the
// shell does the matching); every value-only flag gets NoFileComp so the shell
// does not wrongly offer filenames; positional URLs get NoFileComp.
func registerCompletions(cmd *cobra.Command) {
	// Path flags: returning (nil, ShellCompDirectiveDefault) tells the shell to
	// perform its own file/path completion. Use a registered func (NOT
	// MarkFlagFilename) so it works across all shells via the __complete
	// machinery and is discoverable through GetFlagCompletionFunc in tests.
	// MarkFlagFilename only sets the bash-only BashCompFilenameExt annotation and
	// registers no func, so it would be invisible to GetFlagCompletionFunc.
	pathFlagComp := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveDefault
	}
	mustRegister(cmd, "output", pathFlagComp)
	mustRegister(cmd, "input-file", pathFlagComp)

	// Value-only flags: never offer filenames. cobra's default for an
	// unregistered flag awaiting a value is file completion, which is wrong here.
	// (--checksum is handled separately below; bool flags need no registration:
	// cobra never offers files for bools.)
	for _, name := range []string{
		"concurrency", "timeout", "retries",
		"header", "mirror", "limit-rate", "proxy",
	} {
		mustRegister(cmd, name, cobra.NoFileCompletions)
	}

	// --checksum: tasteful one-token hint of the only supported prefix. Suggest
	// "sha256:" while the user has typed a prefix of it (NoSpace keeps the cursor
	// on the line for the hex), otherwise nothing. Always NoFileComp.
	mustRegister(cmd, "checksum", func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		const hint = "sha256:"
		if len(toComplete) < len(hint) && hint[:len(toComplete)] == toComplete {
			return []string{hint}, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	})

	// Positional args are URLs, not local paths: never offer filenames. No
	// dynamic URL completion (deliberately out of scope).
	cmd.ValidArgsFunction = cobra.NoFileCompletions

	// The lone helper subcommand. Adding it does not change root Args parsing.
	cmd.AddCommand(newCompletionCmd())
}

// mustRegister panics on a typo'd/duplicate flag name. RegisterFlagCompletionFunc
// errors only for an unknown flag ("flag '%s' does not exist") or a duplicate
// ("flag '%s' already registered"); both are programmer errors caught by the
// first test run or `dr completion` invocation, so fail-fast is correct.
func mustRegister(cmd *cobra.Command, flag string, f func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)) {
	if err := cmd.RegisterFlagCompletionFunc(flag, f); err != nil {
		panic(fmt.Sprintf("dr: completion wiring bug: %v", err))
	}
}
