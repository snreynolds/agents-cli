// Package cli defines the agents commands. Commands only parse flags, call
// one app method, and format output; they hold no logic of their own.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/snreynolds/agents-cli/internal/app"
)

// NewRootCmd returns the root command with every subcommand.
func NewRootCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "agents",
		Short:         "Run coding-agent harnesses in profiles that share one login",
		Long:          `The agents cli is used to run coding-agent harnesses in profiles that share one login. Profiles run in the current folder. Use this tool to switch between profiles, try extensions in a throwaway profile, or compare different harnesses on the same codebase.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetVersionTemplate("agents {{.Version}}\n")

	cmd.PersistentFlags().String("home", "", "folder where agents keeps its profiles and settings (default $AGENTS_HOME or ~/.agents-cli)")

	cmd.AddCommand(newNewCmd(), newLsCmd(), newRmCmd(), newRunCmd(), newAuthCmd())
	return cmd
}

// openApp builds the App from the global --home flag.
func openApp(cmd *cobra.Command) (*app.App, error) {
	home, _ := cmd.Flags().GetString("home")
	return app.New(home)
}

// withUsage wraps a Cobra argument check (e.g. cobra.ExactArgs(2)) so that
// its error shows the command's usage, e.g. "usage: agents new <name>
// <harness> [flags]", instead of only a count.
func withUsage(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if check(cmd, args) != nil {
			return fmt.Errorf("usage: %s", cmd.UseLine())
		}
		return nil
	}
}
