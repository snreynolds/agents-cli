package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfirm(t *testing.T) {
	cases := map[string]bool{
		"y\n":     true,
		"yes\n":   true,
		"Y\n":     true,
		" yes \n": true,
		"n\n":     false,
		"\n":      false, // just Enter
		"":        false, // no input at all, as in a script
		"maybe\n": false,
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			cmd := &cobra.Command{}
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetIn(strings.NewReader(input))

			if got := confirm(cmd, "Delete %s?", "fast"); got != want {
				t.Errorf("confirm = %v, want %v", got, want)
			}
			wantErr := "Delete fast? [y/N] "
			if !strings.HasSuffix(input, "\n") {
				wantErr += "\n" // nobody pressed Enter, so confirm ends the line
			}
			if stderr.String() != wantErr || stdout.Len() != 0 {
				t.Errorf("question went to stdout %q / stderr %q, want only stderr %q", stdout.String(), stderr.String(), wantErr)
			}
		})
	}
}
