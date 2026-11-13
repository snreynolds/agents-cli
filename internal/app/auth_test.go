package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/snreynolds/agents-cli/internal/harness"
	"github.com/snreynolds/agents-cli/internal/profile"
)

// key is what pasting an API key (or a setup token) gives.
func key(secret string) credential.Entry { return credential.Entry{Secret: secret} }

// saveCredential saves e under id, as a sign-in would. An ID agents doesn't
// support is saved as an API key.
func saveCredential(t *testing.T, a *App, id string, e credential.Entry) {
	t.Helper()
	c, ok := a.catalog.Find(id)
	if !ok {
		c = credential.Spec{ID: id, Source: credential.APIKey{}}
	}
	if _, err := a.credentials.Save(c, e); err != nil {
		t.Fatal(err)
	}
}

// fakeUI is a credential.UI for tests: answer is piped in, and the
// sign-in code shown is recorded.
type fakeUI struct {
	interactive bool
	answer      string
	shownCode   string
	ran         [][]string // programs RunProgram was asked to run
	runErr      error
}

func (u *fakeUI) Interactive() bool                { return u.interactive }
func (u *fakeUI) AskSecret(string) (string, error) { return u.answer, nil }
func (u *fakeUI) ShowSignInCode(_, code string)    { u.shownCode = code }
func (u *fakeUI) RunProgram(c []string) error      { u.ran = append(u.ran, c); return u.runErr }

// pasting is a UI with no terminal, where answer is piped in.
func pasting(answer string) *fakeUI { return &fakeUI{answer: answer} }

// Signing in to an API key saves what was pasted; a bad paste or an unknown
// ID saves nothing.
func TestSignIn_apiKey(t *testing.T) {
	a, _ := New(t.TempDir())
	if replaced, err := a.SignIn(t.Context(), "openai", pasting("sk-1")); err != nil || replaced {
		t.Fatalf("SignIn = %v, %v", replaced, err)
	}
	if replaced, err := a.SignIn(t.Context(), "openai", pasting("sk-2")); err != nil || !replaced {
		t.Fatalf("second SignIn = %v, %v; want replaced", replaced, err)
	}
	for name, c := range map[string]struct{ id, answer, want string }{
		"unknown login": {"groq", "x", "unknown login"},
		"empty":         {"openrouter", "", "nothing was entered"},
		"space inside":  {"openrouter", "sk-1 extra", "no spaces"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := a.SignIn(t.Context(), c.id, pasting(c.answer)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("SignIn = %v, want %q", err, c.want)
			}
		})
	}
	if list, _ := a.ListCredentials(); len(list) != 1 {
		t.Errorf("saved %v, want only openai", list)
	}
}

func TestCredentialHarnesses(t *testing.T) {
	a := fakePiHome(t, true)
	got, err := a.CredentialHarnesses()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["anthropic"], []string{"claude-code", "pi"}) || !slices.Equal(got["chatgpt"], []string{"fakepi", "pi"}) || !slices.Equal(got["claude"], []string{"claude-code"}) {
		t.Errorf("CredentialHarnesses = %v", got)
	}
}

func TestRevokeCredential(t *testing.T) {
	a, _ := New(t.TempDir())
	saveCredential(t, a, "openai", key("sk-1"))
	if r, err := a.RevokeCredential("openai"); err != nil || !r.Removed {
		t.Fatalf("RevokeCredential = %+v, %v", r, err)
	}
	if r, err := a.RevokeCredential("openai"); err != nil || r.Removed {
		t.Errorf("revoking again = %+v, %v; want not removed, no error", r, err)
	}
	if _, err := a.RevokeCredential("opnai"); err == nil || !strings.Contains(err.Error(), "did you mean openai?") {
		t.Errorf("revoking a typo: %v", err)
	}
	// A saved login this version doesn't support can still be removed.
	saveCredential(t, a, "retired", key("x"))
	if r, err := a.RevokeCredential("retired"); err != nil || !r.Removed {
		t.Errorf("revoking an unsupported saved login = %+v, %v", r, err)
	}
}

func TestGrantCredentials(t *testing.T) {
	spec := harness.Spec{
		Name: "cc",
		Credentials: map[string]harness.Binding{
			"claude": {Env: "TOKEN"}, "anthropic": {Env: "KEY"}, "openai": {Env: "OAI"},
			"chatgpt": {AuthFile: "auth.json", Format: "pi-auth-json", Key: "k"},
		},
		ExclusiveCredentials: [][]string{{"claude", "anthropic"}},
	}
	all := map[string]credential.Entry{"claude": {Secret: "t"}, "anthropic": {Secret: "k"}, "openai": {Secret: "o"}, "openrouter": {Secret: "r"}, "chatgpt": {Secret: "c"}}
	cases := []struct {
		name     string
		allow    []string
		saved    map[string]credential.Entry
		want     []string
		warnings int
	}{
		{"all allowed: one per group, plus the rest", []string{"*"}, all, []string{"chatgpt", "claude", "openai"}, 1},
		{"only what's allowed", []string{"anthropic"}, all, []string{"anthropic"}, 0},
		{"only what's saved", []string{"*"}, map[string]credential.Entry{"anthropic": {Secret: "k"}}, []string{"anthropic"}, 0},
		{"nothing saved", []string{"*"}, nil, nil, 0},
		{"both listed explicitly: still one", []string{"anthropic", "claude"}, all, []string{"claude"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := profile.Profile{Name: "careful", AllowedCredentials: c.allow}
			granted, warnings := grantCredentials(p, spec, c.saved)
			if !slices.Equal(granted, c.want) || len(warnings) != c.warnings {
				t.Errorf("got %v, %q; want %v and %d warnings", granted, warnings, c.want, c.warnings)
			}
		})
	}
}

// A setup token is pasted and saved as it is (the age warning is tested in
// cli and credential).
func TestSignIn_setupToken(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.SignIn(t.Context(), "claude", pasting("sk-ant-oat01-x")); err != nil {
		t.Fatal(err)
	}
	if saved, _ := a.credentials.Load(); saved["claude"].Secret != "sk-ant-oat01-x" {
		t.Errorf("saved %q", saved["claude"].Secret)
	}
}

// Revoking a file login removes it from every profile agents wrote it
// into, and leaves the user's own login alone.
func TestRevokeCredential_inFile(t *testing.T) {
	a := fakePiHome(t, true)
	for _, name := range []string{"mine", "theirs"} {
		if _, err := a.CreateProfile(CreateOptions{Name: name, Harness: "fakepi"}); err != nil {
			t.Fatal(err)
		}
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(10*24*time.Hour))
	own := `{"openai-codex": {"type": "oauth", "access": "theirs", "refresh": "rt-real", "expires": 1, "accountId": "a"}}`
	if err := os.WriteFile(filepath.Join(a.profiles.ConfigDir("theirs"), "auth.json"), []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	runProfile(t, a, "mine")

	r, err := a.RevokeCredential("chatgpt")
	if err != nil || !r.Removed || !slices.Equal(r.ClearedFrom, []string{"mine"}) || len(r.Running) != 0 {
		t.Fatalf("RevokeCredential = %+v, %v; want cleared from mine only", r, err)
	}
	if got := authEntry(t, a, "mine"); got != nil {
		t.Errorf("still in mine's auth.json: %v", got)
	}
	if got := authEntry(t, a, "theirs")["access"]; got != "theirs" {
		t.Errorf("the user's own login was removed: %v", got)
	}
}

// Signing in to ChatGPT shows the code and saves the credential with its
// refresh token and expiry.
func TestSignIn_chatgpt(t *testing.T) {
	a := fakePiHome(t, true)
	ui := &fakeUI{interactive: true}
	if _, err := a.SignIn(t.Context(), "chatgpt", ui); err != nil || ui.shownCode != "ABCD-1234" {
		t.Fatalf("SignIn: %v (showed %q)", err, ui.shownCode)
	}
	saved, _ := a.credentials.Load()
	if e := saved["chatgpt"]; e.Refresh != "rt-new" || e.Account != "acct" || e.Expires.IsZero() || e.Added.IsZero() {
		t.Errorf("saved: refresh %q, account %q, expires %v, added %v", e.Refresh, e.Account, e.Expires, e.Added)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.SignIn(ctx, "chatgpt", ui); !errors.Is(err, credential.ErrCancelled) {
		t.Errorf("cancelled sign-in: %v, want ErrCancelled", err)
	}
}

// A revoke that can't clear one profile still clears the others, says what
// failed, and running it again (after the fix) finishes the job even though
// the login is no longer saved.
func TestRevokeCredential_resumes(t *testing.T) {
	a := fakePiHome(t, true)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := a.CreateProfile(CreateOptions{Name: name, Harness: "fakepi"}); err != nil {
			t.Fatal(err)
		}
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(10*24*time.Hour))
	for _, name := range []string{"a", "b", "c"} {
		runProfile(t, a, name)
	}
	broken := filepath.Join(a.profiles.ConfigDir("b"), "auth.json")
	if err := os.Rename(broken, broken+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := a.RevokeCredential("chatgpt")
	if err == nil || !strings.Contains(err.Error(), "b: ") || !strings.Contains(err.Error(), "run agents auth revoke chatgpt again") {
		t.Errorf("err = %v, want b named and the retry", err)
	}
	if !r.Removed || !slices.Equal(r.ClearedFrom, []string{"a", "c"}) {
		t.Errorf("revoked = %+v, want removed and a, c cleared", r)
	}

	if err := os.Remove(broken); err != nil { // the user fixes b
		t.Fatal(err)
	}
	if err := os.Rename(broken+".real", broken); err != nil {
		t.Fatal(err)
	}
	r, err = a.RevokeCredential("chatgpt")
	if err != nil || r.Removed || !slices.Equal(r.ClearedFrom, []string{"b"}) {
		t.Errorf("second revoke = %+v, %v; want b cleared", r, err)
	}
	if got := authEntry(t, a, "b"); got != nil {
		t.Errorf("still in b: %v", got)
	}
}

// Revoke reports profiles it couldn't look at instead of claiming success.
func TestRevokeCredential_reportsUnchecked(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.home, "harnesses", "fakepi.toml")); err != nil {
		t.Fatal(err)
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(10*24*time.Hour))
	if r, err := a.RevokeCredential("chatgpt"); err != nil || !slices.Equal(r.Unchecked, []string{"p"}) {
		t.Errorf("RevokeCredential = %+v, %v; want p unchecked", r, err)
	}
}

// Only OAuth credentials can be in a auth file, so an unreadable profile
// doesn't hide a typo or warn about an API key.
func TestRevokeCredential_unreadableProfile(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.home, "profiles", "p", "profile.toml"), []byte("broken = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RevokeCredential("opnai"); err == nil || !strings.Contains(err.Error(), "did you mean openai?") {
		t.Errorf("revoking a typo: %v", err)
	}
	if r, err := a.RevokeCredential("openai"); err != nil || r.Removed || len(r.Unchecked) != 0 {
		t.Errorf("revoking an unsaved key = %+v, %v; want nothing removed or unchecked", r, err)
	}
}
