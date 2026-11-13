package credential

import (
	"context"
	"fmt"
	"strings"
)

// SetupToken is the Source for a long-lived token a provider's own program
// prints after signing the user in, such as `claude setup-token`. agents
// runs Command on the user's terminal, then asks them to paste the token it
// printed. It can't be renewed: when it expires (after a year, for Claude),
// the user signs in again.
type SetupToken struct {
	Command []string
}

// SignIn runs the program, then asks for the token it printed. Piped input
// (no terminal) is taken as the token itself, without running the program.
// A long token printed wrapped across lines often keeps its line breaks when
// copied, so all whitespace is removed: a token never contains any.
func (s SetupToken) SignIn(ctx context.Context, c Spec, ui UI) (Entry, error) {
	prompt := "Paste the token:"
	if ui.Interactive() {
		err := ui.RunProgram(s.Command)
		if ctx.Err() != nil {
			return Entry{}, errCancelledNothingSaved
		}
		if err != nil {
			return Entry{}, fmt.Errorf("running %s: %w", strings.Join(s.Command, " "), err)
		}
		prompt = "Paste the token printed above:"
	}
	token, err := ui.AskSecret(prompt)
	return Entry{Secret: strings.Join(strings.Fields(token), "")}, err
}
