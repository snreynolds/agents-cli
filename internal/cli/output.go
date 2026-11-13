package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// confirm asks a yes/no question and reports whether the answer was yes. The
// question goes to stderr so it stays visible when stdout is redirected.
// Anything but "y" or "yes" counts as no, including no input at all (as in a
// script).
func confirm(cmd *cobra.Command, format string, args ...any) bool {
	fmt.Fprintf(cmd.ErrOrStderr(), format+" [y/N] ", args...)
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n') // no input: answer is "", so no
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr()) // nobody pressed Enter; end the question's line
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// printWarnings writes each warning to stderr as "warning: …".
func printWarnings(cmd *cobra.Command, warnings ...string) {
	for _, w := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}
}

// printJSON writes v as indented JSON, for commands with a --json flag.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printTable writes a header and rows as aligned columns.
func printTable(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}
