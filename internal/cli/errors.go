package cli

import (
	"fmt"

	"github.com/snreynolds/agents-cli/internal/credential"
)

// ErrCancelled is in the error when the user pressed Ctrl-C in a prompt or a
// sign-in. main then exits with 130 (128 + SIGINT), as shells do.
var ErrCancelled = credential.ErrCancelled

// ExitError ends agents with Code and no message, e.g. to pass on a
// harness's exit code; the harness has already reported its own failure.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
