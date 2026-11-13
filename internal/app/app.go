// Package app holds the use cases behind each command. Commands parse flags
// and call one method here; this package composes the others.
package app

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/snreynolds/agents-cli/internal/harness"
	"github.com/snreynolds/agents-cli/internal/profile"
)

// App is the state every command works from. Its fields are private so
// commands can only use App's methods: cli talks to app, never to the
// packages below it.
type App struct {
	home        string
	profiles    *profile.Store
	credentials *credential.Store
	catalog     credential.Catalog // the supported credentials; tests swap in fake Sources
}

// New opens the agents home folder: home if non-empty, else $AGENTS_HOME,
// else ~/.agents-cli. Nothing is created until a command writes something.
func New(home string) (*App, error) {
	home, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	return &App{
		home:        home,
		profiles:    profile.NewStore(filepath.Join(home, "profiles")),
		credentials: credential.NewStore(filepath.Join(home, "credentials")),
		catalog:     credential.Supported(),
	}, nil
}

// harnesses loads the harness list. Only actions that need harnesses call it,
// so a broken user harness file can't stop `agents ls` or `agents rm`.
func (a *App) harnesses() (*harness.Registry, error) {
	return harness.Load(filepath.Join(a.home, "harnesses"))
}

// resolveHome picks the home folder: the flag, then $AGENTS_HOME, then
// ~/.agents-cli. A leading ~ is expanded, since the shell doesn't do that in
// AGENTS_HOME="~/x" or --home=~/x.
func resolveHome(flag string) (string, error) {
	home := cmp.Or(flag, os.Getenv("AGENTS_HOME"), "~/.agents-cli")
	if home == "~" || strings.HasPrefix(home, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding your home folder: %w", err)
		}
		home = filepath.Join(userHome, home[1:])
	}
	return filepath.Abs(home)
}
