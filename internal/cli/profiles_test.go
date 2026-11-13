package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snreynolds/agents-cli/internal/app"
)

// run executes the CLI with the given home and returns stdout and stderr.
func run(t *testing.T, home, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewRootCmd("test")
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"--home", home}, args...))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// mustRun is run for steps that must succeed.
func mustRun(t *testing.T, home string, args ...string) string {
	t.Helper()
	out, errOut, err := run(t, home, "", args...)
	if err != nil {
		t.Fatalf("agents %s: %v\n%s", strings.Join(args, " "), err, errOut)
	}
	return out
}

// listJSON returns `agents ls --json` decoded.
func listJSON(t *testing.T, home string) map[string]app.ProfileStatus {
	t.Helper()
	var list []app.ProfileStatus
	if err := json.Unmarshal([]byte(mustRun(t, home, "ls", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	byName := map[string]app.ProfileStatus{}
	for _, p := range list {
		byName[p.Name] = p
	}
	return byName
}

func TestNew(t *testing.T) {
	home := t.TempDir()
	out := mustRun(t, home, "new", "fast", "pi")
	if !strings.Contains(out, "Created profile fast (pi)") {
		t.Errorf("new printed %q", out)
	}
	mustRun(t, home, "new", "careful", "claude-code", "--credentials", "claude")

	got := listJSON(t, home)
	if p := got["fast"]; p.Harness != "pi" || strings.Join(p.AllowedCredentials, ",") != "*" {
		t.Errorf("fast = %+v", p)
	}
	if p := got["careful"]; p.Harness != "claude-code" || strings.Join(p.AllowedCredentials, ",") != "claude" {
		t.Errorf("careful = %+v", p)
	}
}

// An explicitly empty --credentials allows no logins, and ls says "none"
// (QA Q8: it used to allow all).
func TestNew_noCredentials(t *testing.T) {
	home := t.TempDir()
	mustRun(t, home, "new", "bare", "pi", "--credentials", "")
	mustRun(t, home, "new", "bare2", "pi", "--credentials=")
	got := listJSON(t, home)
	for _, name := range []string{"bare", "bare2"} {
		if p := got[name]; len(p.AllowedCredentials) != 0 {
			t.Errorf("%s allows %v, want none", name, p.AllowedCredentials)
		}
	}
	if out := mustRun(t, home, "ls"); !strings.Contains(out, "none") {
		t.Errorf("ls = %q, want CREDENTIALS none", out)
	}
}

func TestNew_rejectsBadInput(t *testing.T) {
	home := t.TempDir()
	mustRun(t, home, "new", "fast", "pi")
	for name, args := range map[string][]string{
		"duplicate name":  {"new", "fast", "pi"},
		"unknown harness": {"new", "x", "hermes"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := run(t, home, "", args...); err == nil {
				t.Error("succeeded, want an error")
			}
		})
	}
}

// Missing arguments show the command's usage, not just a count.
func TestArgs_showUsage(t *testing.T) {
	home := t.TempDir()
	for want, args := range map[string][]string{
		"usage: agents new <name> <harness>": {"new"},
		"usage: agents rm <name>":            {"rm"},
		"usage: agents run <name>":           {"run"},
	} {
		t.Run(args[0], func(t *testing.T) {
			if _, _, err := run(t, home, "", args...); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("got %v, want %q", err, want)
			}
		})
	}
}

func TestLs_empty(t *testing.T) {
	home := t.TempDir()
	if out := mustRun(t, home, "ls"); !strings.Contains(out, "No profiles yet") {
		t.Errorf("ls = %q", out)
	}
	if out := mustRun(t, home, "ls", "--json"); strings.TrimSpace(out) != "[]" {
		t.Errorf("ls --json = %q, want []", out)
	}
}

func TestLs_table(t *testing.T) {
	home := t.TempDir()
	mustRun(t, home, "new", "fast", "pi")
	out := mustRun(t, home, "ls")
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "fast") || !strings.Contains(out, "pi") {
		t.Errorf("ls table = %q", out)
	}
}

// A broken profile is reported by ls and can still be deleted by rm.
func TestLs_brokenProfile(t *testing.T) {
	home := t.TempDir()
	mustRun(t, home, "new", "fast", "pi")
	if err := os.WriteFile(filepath.Join(home, "profiles", "fast", "profile.toml"), []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := run(t, home, "", "ls")
	if err != nil || !strings.Contains(errOut, "profile fast can't be read") {
		t.Errorf("ls: %v, stderr %q", err, errOut)
	}
	mustRun(t, home, "rm", "fast", "--yes")
}

// One bad user harness file breaks only commands that need harnesses.
func TestLsRm_badHarnessFile(t *testing.T) {
	home := t.TempDir()
	mustRun(t, home, "new", "fast", "pi")
	if err := os.MkdirAll(filepath.Join(home, "harnesses"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "harnesses", "bad.toml"), []byte("nonsense = true"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, home, "ls")
	mustRun(t, home, "rm", "fast", "--yes")
	if _, _, err := run(t, home, "", "new", "x", "pi"); err == nil {
		t.Error("new should report the bad harness file")
	}
}

func TestRm(t *testing.T) {
	home := t.TempDir()
	mustRun(t, home, "new", "fast", "pi")

	if _, _, err := run(t, home, "n\n", "rm", "fast"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("answering n should cancel with an error, got %v", err)
	}
	if _, ok := listJSON(t, home)["fast"]; !ok {
		t.Error("profile deleted despite answering n")
	}
	mustRun(t, home, "rm", "fast", "--yes")
	if _, ok := listJSON(t, home)["fast"]; ok {
		t.Error("profile still listed after rm --yes")
	}
	if _, _, err := run(t, home, "", "rm", "nope", "--yes"); err == nil {
		t.Error("removing a missing profile should fail")
	}
}
