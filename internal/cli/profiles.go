package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/snreynolds/agents-cli/internal/app"
)

func newNewCmd() *cobra.Command {
	var o app.CreateOptions
	cmd := &cobra.Command{
		Use:   "new <name> <harness>",
		Short: "Create a profile for a harness",
		Long: `Creates a profile: a named setup of one harness with its own config folder.
Nothing is launched until the profile's first run, which gets the saved logins
the profile allows (see agents auth).`,
		Example: `  agents new fast pi
  agents new trial pi --from ~/.pi/agent
  agents new careful claude-code --credentials claude`,
		Args: withUsage(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			o.Name, o.Harness = args[0], args[1]
			if cmd.Flags().Changed("credentials") && o.AllowedCredentials == nil {
				o.AllowedCredentials = []string{} // --credentials "": no logins
			}
			created, err := a.CreateProfile(o)
			if err != nil {
				return err
			}
			printWarnings(cmd, created.Warnings...)
			fmt.Fprintf(cmd.OutOrStdout(), "Created profile %s (%s)\n  config: %s\n", o.Name, o.Harness, created.ConfigDir)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.From, "from", "", "copy setup files (settings, extensions, skills…) from this config folder")
	f.StringSliceVar(&o.AllowedCredentials, "credentials", nil, "logins this profile may use, e.g. openai,openrouter (default: all; \"\" for none)")
	return cmd
}

func newLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List profiles",
		Args:  withUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			profiles, unreadable, err := a.ListProfiles()
			if err != nil {
				return err
			}
			for _, name := range unreadable {
				printWarnings(cmd, fmt.Sprintf("profile %s can't be read (profile.toml is missing or invalid); delete it with: agents rm %s", name, name))
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, profiles)
			}
			if len(profiles) == 0 {
				fmt.Fprintln(out, "No profiles yet. Create one with: agents new <name> <harness>")
				return nil
			}
			return printTable(out, []string{"NAME", "HARNESS", "CREDENTIALS", "RUNNING"}, profileRows(profiles))
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func newRmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a profile, including its sessions and settings",
		Args:  withUsage(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			if err := a.CheckRemoveProfile(name); err != nil { // fail before asking
				return err
			}
			confirmed := yes || confirm(cmd, "Delete profile %s, including its sessions and settings?", name)
			if !confirmed {
				return errors.New("cancelled; nothing was deleted (use --yes to skip this question)")
			}
			if err := a.RemoveProfile(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted profile %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}

// profileRows turns profiles into `agents ls` table rows.
func profileRows(statuses []app.ProfileStatus) [][]string {
	rows := make([][]string, len(statuses))
	for i, s := range statuses {
		rows[i] = []string{s.Name, s.Harness, credentialsText(s.AllowedCredentials), runningText(s.Running)}
	}
	return rows
}

// runningText shows how many runs of a profile are live for `agents ls`, or "-".
func runningText(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

// credentialsText is the CREDENTIALS cell of agents ls: the allowed IDs, or
// "none" for a profile allowed no logins.
func credentialsText(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ",")
}
