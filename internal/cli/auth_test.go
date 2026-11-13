package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/spf13/cobra"
)

// A login saved once reaches every profile that allows it, replacing the
// shell's value; a profile that doesn't allow it never sees it.
func TestAuth_credentialReachesRuns(t *testing.T) {
	home := newFakeHome(t, "")
	t.Setenv("FAKE_API_KEY", "from-shell")
	mustRun(t, home, "new", "all", "fake")
	mustRun(t, home, "new", "none", "fake", "--credentials", "claude")

	if _, _, err := run(t, home, "sk-saved\n", "auth", "login", "openai"); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, home, "run", "all"); !strings.Contains(out, "key=sk-saved ") {
		t.Errorf("profile allowing all: %s", out)
	}
	if out := mustRun(t, home, "run", "none"); !strings.Contains(out, "key=unset ") {
		t.Errorf("profile not allowing openai: %s", out)
	}

	// After revoke, the next run doesn't get it.
	mustRun(t, home, "auth", "revoke", "openai")
	if out := mustRun(t, home, "run", "all"); !strings.Contains(out, "key=unset ") {
		t.Errorf("after revoke: %s", out)
	}
}

// auth list shows what's saved and when, never the secret.
func TestAuth_listNeverShowsSecrets(t *testing.T) {
	home := newFakeHome(t, "")
	if out := mustRun(t, home, "auth", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Errorf("empty list --json = %q", out)
	}
	if _, _, err := run(t, home, "sk-very-secret\n", "auth", "login", "openai"); err != nil {
		t.Fatal(err)
	}
	table, errOut, err := run(t, home, "", "auth", "list")
	asJSON := mustRun(t, home, "auth", "list", "--json")
	if err != nil || !strings.Contains(table, "openai") {
		t.Fatalf("list: %v\n%s", err, table)
	}
	for _, out := range []string{table, errOut, asJSON} {
		if strings.Contains(out, "sk-very-secret") {
			t.Errorf("output contains the secret:\n%s", out)
		}
	}
	var list []struct{ ID string }
	if err := json.Unmarshal([]byte(asJSON), &list); err != nil || len(list) != 1 || list[0].ID != "openai" {
		t.Errorf("list --json = %s (%v)", asJSON, err)
	}
}

// Logins a harness must not get together: the first in its group wins, with
// a warning saying how to choose the other.
func TestAuth_exclusiveWarning(t *testing.T) {
	home := newFakeHome(t, "")
	mustRun(t, home, "new", "a", "fake")
	for _, id := range []string{"claude", "openai"} {
		if _, _, err := run(t, home, "value-"+id+"\n", "auth", "login", id); err != nil {
			t.Fatal(err)
		}
	}
	out, errOut, err := run(t, home, "", "run", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "key=unset token=value-claude ") {
		t.Errorf("harness got: %s", out)
	}
	if !strings.Contains(errOut, "warning: a gets claude, not openai") || !strings.Contains(errOut, "--credentials openai") {
		t.Errorf("stderr = %q, want the exclusive warning", errOut)
	}
}

// revoke lists live runs that keep the login.
func TestAuth_revokeListsLiveRuns(t *testing.T) {
	home := newFakeHome(t, "")
	mustRun(t, home, "new", "a", "fake")
	mustRun(t, home, "new", "other", "fake", "--credentials", "claude")
	if _, _, err := run(t, home, "sk\n", "auth", "login", "openai"); err != nil {
		t.Fatal(err)
	}
	pid, done := startWaiting(t, home, "a")
	otherPID, otherDone := startWaiting(t, home, "other")
	defer stopWaiting(t, otherPID, otherDone)
	out, errOut, err := run(t, home, "", "auth", "revoke", "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Removed openai") || !strings.Contains(errOut, "warning: a may still have it (process") {
		t.Errorf("revoke printed:\n%s\nand on stderr:\n%s", out, errOut)
	}
	if strings.Contains(out, "may still") {
		t.Errorf("warnings belong on stderr, not stdout:\n%s", out)
	}
	if strings.Contains(errOut, "other") {
		t.Errorf("listed a run of a profile that doesn't allow openai:\n%s", errOut)
	}
	stopWaiting(t, pid, done)
	if out := mustRun(t, home, "auth", "revoke", "openai"); !strings.Contains(out, "wasn't saved") {
		t.Errorf("second revoke printed %q", out)
	}
}

func TestAuth_badInput(t *testing.T) {
	home := newFakeHome(t, "")
	for name, c := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"typo":                           {"sk\n", []string{"auth", "login", "opnai"}, "did you mean openai?"},
		"empty secret":                   {"\n", []string{"auth", "login", "openai"}, "nothing was entered"},
		"no ID and no terminal":          {"sk\n", []string{"auth", "login"}, "name the login to save"},
		"revoke with no ID, no terminal": {"", []string{"auth", "revoke"}, "name the login to revoke"},
		"revoke an unknown ID":           {"", []string{"auth", "revoke", "groq"}, `unknown login "groq" (supported:`},
		"space inside the secret":        {"sk-1 extra\n", []string{"auth", "login", "openai"}, "no spaces or line breaks"},
		"far too long":                   {strings.Repeat("a", 70<<10), []string{"auth", "login", "openai"}, "far too long"},
		"browser sign-in, no terminal":   {"pasted\n", []string{"auth", "login", "chatgpt"}, "run agents auth login chatgpt on a terminal"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := run(t, home, c.stdin, c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
	if out := mustRun(t, home, "auth", "list"); !strings.Contains(out, "No logins saved") {
		t.Errorf("bad input saved something: %s", out)
	}
}

// Revoking a login kept in a harness's auth file says which profiles it
// was removed from.
func TestAuth_revokeFileCredential(t *testing.T) {
	home := newFakeHome(t, "")
	writeFile(t, filepath.Join(home, "harnesses", "fakepi.toml"), `
name = "fakepi"
binary = "true"
config_env = "FAKEPI_DIR"
secret_files = ["auth.json"]
[credentials]
chatgpt = { auth_file = "auth.json", format = "pi-auth-json", key = "openai-codex" }
`, 0o600)
	expires := time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	writeFile(t, filepath.Join(home, "credentials", "credentials.json"),
		`{"chatgpt": {"secret": "access", "added": "2026-10-08T00:00:00Z", "refresh": "rt", "expires": "`+expires+`", "account": "a"}}`, 0o600)
	mustRun(t, home, "new", "p", "fakepi")
	mustRun(t, home, "run", "p")
	authFile := filepath.Join(home, "profiles", "p", "config", "auth.json")
	if data, err := os.ReadFile(authFile); err != nil || !strings.Contains(string(data), `"access": "access"`) {
		t.Fatalf("run didn't write the login: %s, %v", data, err)
	}
	if list := mustRun(t, home, "auth", "list"); strings.Contains(list, "access") || strings.Contains(list, `"rt"`) {
		t.Error("auth list shows a secret")
	}
	out := mustRun(t, home, "auth", "revoke", "chatgpt")
	if !strings.Contains(out, "Removed it from p;") {
		t.Errorf("revoke printed:\n%s", out)
	}
	if data, _ := os.ReadFile(authFile); strings.Contains(string(data), "openai-codex") {
		t.Errorf("the login is still in auth.json: %s", data)
	}
}

// A setup token is piped in like a key; one copied wrapped across two
// lines is joined back together.
func TestAuth_pipedSetupToken(t *testing.T) {
	home := newFakeHome(t, "")
	if _, _, err := run(t, home, "sk-ant-oat01-aaaa\nbbbb\n", "auth", "login", "claude"); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, home, "auth", "list"); !strings.Contains(out, "claude") {
		t.Errorf("claude wasn't saved: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(home, "credentials", "credentials.json"))
	if err != nil || !strings.Contains(string(data), `"sk-ant-oat01-aaaabbbb"`) {
		t.Errorf("saved %s, %v; want the token joined", data, err)
	}
}

func TestChoiceText(t *testing.T) {
	c := credential.Spec{ID: "chatgpt", Name: "ChatGPT subscription", Hint: "Plus/Pro"}
	if got, want := choiceText(c, []string{"pi"}, true), "chatgpt     ChatGPT subscription (Plus/Pro)  · pi  ✓ saved"; got != want {
		t.Errorf("choiceText = %q, want %q", got, want)
	}
	if got := choiceText(credential.Spec{ID: "openai", Name: "OpenAI API key"}, []string{"claude-code", "pi"}, false); got != "openai      OpenAI API key  · claude-code, pi" {
		t.Errorf("choiceText = %q", got)
	}
}

// terminalUI.RunProgram runs the program on the command's streams and says
// when it isn't installed.
func TestTerminalUI_RunProgram(t *testing.T) {
	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	if err := (terminalUI{c}).RunProgram([]string{"sh", "-c", "echo printed-token"}); err != nil || !strings.Contains(out.String(), "printed-token") {
		t.Errorf("RunProgram = %v, output %q", err, out.String())
	}
	if err := (terminalUI{c}).RunProgram([]string{"sh", "-c", "exit 3"}); err == nil {
		t.Error("a failing program returned no error")
	}
	if err := (terminalUI{c}).RunProgram([]string{"agents-test-no-such-program"}); err == nil || !strings.Contains(err.Error(), "is agents-test-no-such-program installed?") {
		t.Errorf("missing program: %v", err)
	}
}
