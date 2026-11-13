package cli

import (
	"github.com/spf13/cobra"

	"github.com/snreynolds/agents-cli/internal/app"
)

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <name> [harness args]",
		Short: "Run a profile's harness in the current folder",
		Long: `Runs the profile's harness in the current folder. Arguments after the name
go to the harness. Your shell's credential variables (e.g. OPENAI_API_KEY)
are replaced by the logins the profile allows (see agents auth).`,
		Example: `  agents run fast
  agents run fast --continue
  agents run fast -- --help`,
		Args: withUsage(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			code, err := a.Run(app.RunOptions{
				Name:   args[0],
				Args:   harnessArgs(args[1:]),
				Stdin:  cmd.InOrStdin(),
				Stdout: cmd.OutOrStdout(),
				Stderr: cmd.ErrOrStderr(),
				Warn:   func(w string) { printWarnings(cmd, w) },
			})
			if err != nil {
				return err
			}
			if code != 0 {
				return &ExitError{Code: code}
			}
			return nil
		},
	}
	// Flags after the profile name belong to the harness, not to agents.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// harnessArgs drops a leading "--", which some users type out of habit to
// mark where the harness's arguments start.
func harnessArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}
