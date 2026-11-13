package credential

import (
	"context"
	"fmt"
)

// APIKey is the Source for API keys: the user pastes the key at a hidden
// prompt, or pipes it in.
type APIKey struct{}

// SignIn asks for the key.
func (APIKey) SignIn(_ context.Context, c Spec, ui UI) (Entry, error) {
	key, err := ui.AskSecret(fmt.Sprintf("Paste your %s:", c.Name))
	return Entry{Secret: key}, err
}
