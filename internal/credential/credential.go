// Package credential owns the credentials agents hands to harnesses: the
// API keys and subscriptions a user saves once and every profile that
// allows them gets. It covers their whole lifecycle:
//
//   - What exists: the Catalog of supported credentials, each a Spec.
//   - Signing in: each Spec's Source gets the secret. A Source is one way of
//     signing in (APIKey, SetupToken, ChatGPT); a new provider is a new
//     Source, and nothing outside this package changes.
//   - Storing: Store keeps every saved credential in one file outside all
//     profiles, and renews the ones whose Source is a Renewer.
//   - Provisioning: most harnesses get a credential in an env var, which
//     app sets. A few read one only from their own auth file (pi reads a
//     ChatGPT subscription only from its auth.json); Provision and
//     Unprovision write and remove those entries.
//
// The package knows nothing about harnesses or profiles: app says which
// auth file to write and which credentials a profile gets.
package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Spec describes one credential agents supports, like harness.Spec
// describes a harness.
type Spec struct {
	ID     string // what users type, e.g. `agents auth login openai`
	Name   string // e.g. "OpenAI API key"
	Hint   string // shown in the login picker, if useful
	Source Source // how agents signs in to get its secret
}

// Source is one way of signing in to get a credential's secret. Each kind
// is a type: APIKey (paste it), SetupToken (paste what a provider's program
// prints), ChatGPT (OAuth). Adding a provider means adding a Source, here.
type Source interface {
	// SignIn asks the user, through ui, for whatever this way of signing
	// in needs, and returns the credential to save (Store.Save sets Added).
	// A cancelled sign-in returns an error matching ErrCancelled.
	SignIn(ctx context.Context, c Spec, ui UI) (Entry, error)
}

// Renewer is a Source whose credentials expire and are renewed (OAuth).
// Only a Renewer's credentials have a refresh token and an expiry, and only
// they may be provisioned into a auth file, whose formats store an expiry.
type Renewer interface {
	Source
	// Renew trades e's refresh token for a new credential.
	Renew(ctx context.Context, e Entry) (Entry, error)
}

// UI is what a sign-in may ask of the user. app provides it.
type UI interface {
	// Interactive reports whether the user is at a terminal to answer.
	Interactive() bool
	// AskSecret reads a key or token: from a hidden prompt on a terminal,
	// or from piped standard input.
	AskSecret(prompt string) (string, error)
	// ShowSignInCode tells the user to enter code at url, and opens url in
	// their browser.
	ShowSignInCode(url, code string)
	// RunProgram runs a program on the user's terminal, for them to use
	// directly, and waits for it to exit.
	RunProgram(command []string) error
}

// ErrCancelled is in the error when the user cancelled (Ctrl-C) a sign-in.
var ErrCancelled = errors.New("cancelled")

// errCancelledNothingSaved is what a cancelled sign-in returns.
var errCancelledNothingSaved = fmt.Errorf("%w; nothing was saved", ErrCancelled)

// Catalog is a list of supported credentials, in picker order.
type Catalog []Spec

// Supported returns the credentials agents supports, in picker order.
// Subscriptions are named after the product (claude, chatgpt), API keys
// after the company (anthropic, openai).
func Supported() Catalog {
	return Catalog{
		{
			ID: "claude", Name: "Claude subscription token", Hint: "Pro/Max; signs in with your browser",
			// `claude setup-token` (Claude Code) signs in with the browser and
			// prints a token valid for a year, with no way to renew it.
			Source: SetupToken{Command: []string{"claude", "setup-token"}},
		},
		{ID: "chatgpt", Name: "ChatGPT subscription", Hint: "Plus/Pro; signs in with your browser", Source: ChatGPT{}},
		{ID: "anthropic", Name: "Anthropic API key", Source: APIKey{}},
		{ID: "openai", Name: "OpenAI API key", Source: APIKey{}},
		{ID: "openrouter", Name: "OpenRouter API key", Source: APIKey{}},
	}
}

// Find returns the credential with this ID.
func (cat Catalog) Find(id string) (Spec, bool) {
	for _, c := range cat {
		if c.ID == id {
			return c, true
		}
	}
	return Spec{}, false
}

// Lookup returns the credential with this ID, or an error that suggests the
// closest one for a typo.
func (cat Catalog) Lookup(id string) (Spec, error) {
	if c, ok := cat.Find(id); ok {
		return c, nil
	}
	known := cat.IDs()
	hint := ""
	if near := closest(id, known); near != "" {
		hint = fmt.Sprintf("; did you mean %s?", near)
	}
	return Spec{}, fmt.Errorf("unknown login %q%s (supported: %s)", id, hint, strings.Join(known, ", "))
}

// IDs returns the credentials' IDs, in picker order.
func (cat Catalog) IDs() []string {
	ids := make([]string, len(cat))
	for i, c := range cat {
		ids[i] = c.ID
	}
	return ids
}

// Renews reports whether c's credentials expire and are renewed, i.e. its
// Source is a Renewer.
func (c Spec) Renews() bool {
	_, ok := c.Source.(Renewer)
	return ok
}
