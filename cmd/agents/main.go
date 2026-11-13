// Command agents runs coding-agent harnesses (pi, Claude Code) in named
// profiles that share one login.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/snreynolds/agents-cli/internal/cli"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = ""

func main() {
	err := cli.NewRootCmd(resolveVersion()).Execute()
	var exit *cli.ExitError
	switch {
	case errors.As(err, &exit):
		os.Exit(exit.Code)
	case errors.Is(err, cli.ErrCancelled):
		fmt.Fprintln(os.Stderr, err) // the user's choice, not a failure: no "Error:"
		os.Exit(130)
	case err != nil:
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// resolveVersion prefers the release version, then the module version that
// `go install …@v1.2.3` records, and falls back to "dev".
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
