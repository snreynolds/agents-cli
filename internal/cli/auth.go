package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/snreynolds/agents-cli/internal/app"
	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/snreynolds/agents-cli/internal/process"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Save logins once and share them with your profiles",
		Long: `Saves API keys and tokens in one place. Every profile that allows a login
gets it when it runs (see agents new --credentials).`,
	}
	cmd.AddCommand(newAuthLoginCmd(), newAuthListCmd(), newAuthRevokeCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login [id]",
		Short: "Save a login (pick one, or name it)",
		Long: `Saves a key or token. Without an ID, pick one from a list.
ChatGPT signs in with your browser, with a code you enter on OpenAI's site.
For a Claude subscription, it runs claude setup-token (which signs in with
your browser and prints a token that lasts a year), then asks you to paste
the token. Keys and tokens are read from a hidden prompt, or from standard
input when piped (a piped claude token is saved without running anything).`,
		Example: `  agents auth login
  agents auth login chatgpt
  agents auth login claude
  pbpaste | agents auth login openrouter`,
		Args: withUsage(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			id, err := saveID(cmd, a, args)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt) // Ctrl-C cancels a browser sign-in
			defer stop()
			replaced, err := a.SignIn(ctx, id, terminalUI{cmd})
			if err != nil {
				return err
			}
			verb := "Saved"
			if replaced {
				verb = "Replaced"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s. Runs started from now get it.\n", verb, id)
			return nil
		},
	}
}

func newAuthListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved logins (never the secrets)",
		Args:  withUsage(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			saved, err := a.ListCredentials()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSON(out, saved)
			}
			if len(saved) == 0 {
				fmt.Fprintln(out, "No logins saved. Add one with: agents auth login")
				return nil
			}
			rows := make([][]string, len(saved))
			for i, c := range saved {
				rows[i] = []string{c.ID, c.Added.Local().Format("2006-01-02 15:04")}
			}
			return printTable(out, []string{"ID", "ADDED"}, rows)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func newAuthRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke [id]",
		Short: "Delete a saved login, so runs no longer get it",
		Args:  withUsage(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd)
			if err != nil {
				return err
			}
			id, err := revokeID(cmd, a, args)
			if err != nil {
				return err
			}
			revoked, err := a.RevokeCredential(id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case revoked.Removed:
				fmt.Fprintf(out, "Removed %s. Runs started from now won't get it.\n", id)
			case len(revoked.ClearedFrom) == 0 && len(revoked.Unchecked) == 0:
				fmt.Fprintf(out, "%s wasn't saved; nothing to remove.\n", id)
				return nil
			}
			if len(revoked.ClearedFrom) > 0 {
				what := "it" // the line above named it
				if !revoked.Removed {
					what = id
				}
				fmt.Fprintf(out, "Removed %s from %s; running sessions lose it when they next read their login file.\n", what, strings.Join(revoked.ClearedFrom, ", "))
			}
			if len(revoked.Unchecked) > 0 {
				printWarnings(cmd, fmt.Sprintf("couldn't check %s (unreadable, or harness missing); if they had %s, remove it from their login file by hand", strings.Join(revoked.Unchecked, ", "), id))
			}
			for _, r := range revoked.Running {
				printWarnings(cmd, fmt.Sprintf("%s may still have it (process %d in %s); stop it to take it away", r.Profile, r.PID, r.Dir))
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "This only stops agents handing it out; to invalidate it, revoke it (or sign out) with the provider.")
			return nil
		},
	}
}

// terminalUI is how a sign-in talks to the user (a credential.UI): prompts
// and messages on standard error.
type terminalUI struct{ cmd *cobra.Command }

func (u terminalUI) Interactive() bool { return interactive(u.cmd) }

func (u terminalUI) AskSecret(prompt string) (string, error) { return readSecret(u.cmd, prompt) }

func (u terminalUI) ShowSignInCode(url, code string) {
	errOut := u.cmd.ErrOrStderr()
	fmt.Fprintf(errOut, "To sign in, open %s\nand enter the code  %s\n", url, code)
	if process.OpenBrowser(url) == nil {
		fmt.Fprintln(errOut, "(Opened it in your browser.)")
	}
	fmt.Fprintln(errOut, "Waiting for you to finish…")
}

// RunProgram runs command with the terminal, so the user uses it directly.
// Ctrl-C reaches it the usual way (the terminal sends it to both).
func (u terminalUI) RunProgram(command []string) error {
	p := exec.Command(command[0], command[1:]...) //nolint:gosec // a command from the credential catalog, not user input
	p.Stdin, p.Stdout, p.Stderr = u.cmd.InOrStdin(), u.cmd.OutOrStdout(), u.cmd.ErrOrStderr()
	err := p.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%w (is %s installed?)", err, command[0])
	}
	return err
}

// saveID returns the credential to save: the argument, or one picked from
// the supported credentials on a terminal.
func saveID(cmd *cobra.Command, a *app.App, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	supported := a.SupportedCredentials()
	if !interactive(cmd) {
		ids := make([]string, len(supported))
		for i, c := range supported {
			ids[i] = c.ID
		}
		return "", fmt.Errorf("name the login to save: agents auth login <id> (supported: %s)", strings.Join(ids, ", "))
	}
	supportedBy, err := a.CredentialHarnesses()
	if err != nil {
		return "", err
	}
	saved, err := a.ListCredentials()
	if err != nil {
		return "", err
	}
	options := make([]huh.Option[string], len(supported))
	for i, c := range supported {
		isSaved := slices.ContainsFunc(saved, func(s app.SavedCredential) bool { return s.ID == c.ID })
		options[i] = huh.NewOption(choiceText(c, supportedBy[c.ID], isSaved), c.ID)
	}
	return pick(cmd, "Which login do you want to add?", options)
}

// choiceText is one row of the login picker, e.g.
// "openai      OpenAI API key  · pi  ✓ saved".
func choiceText(c credential.Spec, harnesses []string, saved bool) string {
	text := fmt.Sprintf("%-11s %s", c.ID, c.Name)
	if c.Hint != "" {
		text += " (" + c.Hint + ")"
	}
	text += "  · " + strings.Join(harnesses, ", ")
	if saved {
		text += "  ✓ saved"
	}
	return text
}

// revokeID returns the login to revoke: the argument, or one picked from the
// saved logins on a terminal.
func revokeID(cmd *cobra.Command, a *app.App, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	if !interactive(cmd) {
		return "", errors.New("name the login to revoke: agents auth revoke <id> (see agents auth list)")
	}
	saved, err := a.ListCredentials()
	if err != nil {
		return "", err
	}
	if len(saved) == 0 {
		return "", errors.New("no logins saved")
	}
	options := make([]huh.Option[string], len(saved))
	for i, c := range saved {
		options[i] = huh.NewOption(c.ID, c.ID)
	}
	return pick(cmd, "Which login do you want to revoke?", options)
}

// pick shows a list to choose from (arrow keys, type to filter) on stderr.
func pick(cmd *cobra.Command, title string, options []huh.Option[string]) (string, error) {
	var choice string
	field := huh.NewSelect[string]().Title(title).Options(options...).Filtering(true).Value(&choice)
	if err := runForm(cmd, field); err != nil {
		return "", err
	}
	return choice, nil
}

// maxSecret is how much piped input readSecret accepts: far more than any key.
const maxSecret = 64 << 10

// readSecret reads a key or token: from a hidden prompt on a terminal, or
// from standard input when it's piped. Surrounding whitespace (such as the
// newline a pipe adds) is dropped.
func readSecret(cmd *cobra.Command, prompt string) (string, error) {
	in := cmd.InOrStdin()
	if !isTerminal(in) {
		data, err := io.ReadAll(io.LimitReader(in, maxSecret+1))
		if err != nil {
			return "", err
		}
		if len(data) > maxSecret {
			return "", errors.New("the piped input is far too long to be a key or token")
		}
		return strings.TrimSpace(string(data)), nil
	}
	if !interactive(cmd) {
		return "", errors.New("can't show the prompt because error output isn't a terminal; pipe the secret in instead")
	}
	var secret string
	if err := runForm(cmd, huh.NewInput().Title(prompt).EchoMode(huh.EchoModePassword).Value(&secret)); err != nil {
		return "", err
	}
	return strings.TrimSpace(secret), nil
}

// runForm shows one prompt on stderr. Setting ACCESSIBLE (to anything) uses
// plain line-by-line prompts that work with screen readers. Pressing Ctrl-C
// or Esc becomes ErrCancelled.
func runForm(cmd *cobra.Command, field huh.Field) error {
	form := huh.NewForm(huh.NewGroup(field)).
		WithInput(cmd.InOrStdin()).
		WithOutput(cmd.ErrOrStderr()).
		WithAccessible(os.Getenv("ACCESSIBLE") != "")
	err := form.Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return fmt.Errorf("%w; nothing was changed", ErrCancelled)
	}
	return err
}

// interactive reports whether prompts can be shown: both standard input and
// error output (where prompts are drawn) are terminals.
func interactive(cmd *cobra.Command) bool {
	return isTerminal(cmd.InOrStdin()) && isTerminal(cmd.ErrOrStderr())
}

// isTerminal reports whether f is an interactive terminal rather than a pipe
// or file.
func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
