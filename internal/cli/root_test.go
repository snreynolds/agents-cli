package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	cmd := NewRootCmd("1.2.3")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := out.String(), "agents 1.2.3\n"; got != want {
		t.Errorf("--version printed %q, want %q", got, want)
	}
}

func TestHelp_listsGlobalFlags(t *testing.T) {
	cmd := NewRootCmd("dev")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, flag := range []string{"--home", "--version"} {
		if !strings.Contains(out.String(), flag) {
			t.Errorf("--help output is missing %s", flag)
		}
	}
}

// Bare `agents` prints help; an unknown command is an error.
func TestRoot_bareAndUnknown(t *testing.T) {
	cmd := NewRootCmd("dev")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "Usage:") {
		t.Errorf("bare agents: %v, printed %q", err, out.String())
	}
	cmd = NewRootCmd("dev")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"bogus"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("agents bogus: %v", err)
	}
}
