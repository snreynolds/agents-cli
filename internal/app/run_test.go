package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/snreynolds/agents-cli/internal/process"
	"github.com/snreynolds/agents-cli/internal/profile"
)

// Records of runs that have ended are ignored, including one whose process
// ID now belongs to another process (doc scenario 27), and left in place:
// reading never deletes.
func TestLiveRuns(t *testing.T) {
	a, _ := New(t.TempDir())
	if _, err := a.CreateProfile(CreateOptions{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	start, err := process.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []profile.RunRecord{
		{PID: os.Getpid(), Start: start, Dir: "/live"},
		{PID: os.Getpid(), Start: start - 1, Dir: "/reused-pid"},
	} {
		if _, err := a.profiles.AddRun("fast", r); err != nil {
			t.Fatal(err)
		}
	}

	live, err := a.liveRuns("fast")
	if err != nil || len(live) != 1 || live[0].Dir != "/live" {
		t.Fatalf("liveRuns = %+v, %v; want only /live", live, err)
	}
	if records, _ := a.profiles.Runs("fast"); len(records) != 2 {
		t.Errorf("records = %+v, want both still on disk", records)
	}
}

// fakePiHome returns an App whose "fakepi" harness keeps chatgpt in its
// auth.json, like pi, and exits at once when run. Its ChatGPT sign-in talks
// to a fake OpenAI that renews logins, or fails to if renewals is false.
func fakePiHome(t *testing.T, renewals bool) *App {
	t.Helper()
	home := t.TempDir()
	toml := `name = "fakepi"
binary = "true"
config_env = "FAKEPI_DIR"
secret_files = ["auth.json"]
secret_envs = ["FAKEPI_KEY"]
[credentials]
openai = { env = "FAKEPI_KEY" }
chatgpt = { auth_file = "auth.json", format = "pi-auth-json", key = "openai-codex" }
`
	if err := os.MkdirAll(filepath.Join(home, "harnesses"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "harnesses", "fakepi.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fakeOpenAI(renewals))
	t.Cleanup(srv.Close)
	useOpenAI(a, srv.URL)
	return a
}

// useOpenAI points a's ChatGPT sign-in at a fake OpenAI at url.
func useOpenAI(a *App, url string) {
	for i, c := range a.catalog {
		if c.ID == "chatgpt" {
			a.catalog[i].Source = credential.ChatGPT{AuthBase: url}
		}
	}
}

// fakeOpenAI serves OpenAI's sign-in endpoints: a device sign-in approved
// on the first poll, and token requests answered with "new-access" (or
// refused, if renewals is false).
func fakeOpenAI(renewals bool) http.Handler {
	claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"}})
	access := "new-access." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/accounts/deviceauth/usercode", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"device_auth_id": "d", "user_code": "ABCD-1234", "interval": 0}`)
	})
	mux.HandleFunc("POST /api/accounts/deviceauth/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"authorization_code": "code", "code_verifier": "v"}`)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		if !renewals {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error": "invalid_grant"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "rt-new", "expires_in": 864000})
	})
	return mux
}

// chatgptEntry is a saved ChatGPT login expiring in expiresIn.
func chatgptEntry(expiresIn time.Duration) credential.Entry {
	return credential.Entry{Secret: "old-access", Refresh: "rt-old", Expires: time.Now().Add(expiresIn), Account: "acct", Added: time.Now()}
}

// runProfile runs a profile and returns the warnings it printed.
func runProfile(t *testing.T, a *App, name string) []string {
	t.Helper()
	var warnings []string
	if _, err := a.Run(RunOptions{Name: name, Warn: func(w string) { warnings = append(warnings, w) }}); err != nil {
		t.Fatal(err)
	}
	return warnings
}

// authEntry returns the openai-codex entry in a profile's auth.json, or nil.
func authEntry(t *testing.T, a *App, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(a.profiles.ConfigDir(name), "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	return entries["openai-codex"]
}

// A run writes the ChatGPT login into the profile's auth.json, with the
// placeholder refresh token. A profile that doesn't allow it gets nothing,
// and loses a login agents wrote there earlier.
func TestRun_credentialInFile(t *testing.T) {
	a := fakePiHome(t, true)
	for _, o := range []CreateOptions{{Name: "all", Harness: "fakepi"}, {Name: "keys", Harness: "fakepi", AllowedCredentials: []string{"openai"}}} {
		if _, err := a.CreateProfile(o); err != nil {
			t.Fatal(err)
		}
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(10*24*time.Hour))
	runProfile(t, a, "all")
	got := authEntry(t, a, "all")
	if got["access"] != "old-access" || got["refresh"] != "agents-managed" || got["accountId"] != "acct" {
		t.Errorf("auth.json entry = %v", got)
	}

	planted := `{"openai-codex": {"type": "oauth", "access": "stale", "refresh": "agents-managed", "expires": 1, "accountId": "a"}}`
	if err := os.WriteFile(filepath.Join(a.profiles.ConfigDir("keys"), "auth.json"), []byte(planted), 0o600); err != nil {
		t.Fatal(err)
	}
	runProfile(t, a, "keys")
	if got := authEntry(t, a, "keys"); got != nil {
		t.Errorf("a login the profile doesn't allow is still in auth.json: %v", got)
	}
}

// A login close to expiring is renewed before the run, and the new one is
// saved and written.
func TestRun_renewsCredential(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(time.Hour))
	if warnings := runProfile(t, a, "p"); len(warnings) != 0 {
		t.Errorf("warnings: %q", warnings)
	}
	saved, _ := a.credentials.Load()
	if e := saved["chatgpt"]; e.Refresh != "rt-new" || !strings.HasPrefix(e.Secret, "new-access.") || time.Until(e.Expires) < 9*24*time.Hour {
		t.Errorf("saved login wasn't renewed: refresh %q, expires %v", e.Refresh, e.Expires)
	}
	if got := authEntry(t, a, "p")["access"]; got != saved["chatgpt"].Secret {
		t.Errorf("auth.json has %v, not the renewed login", got)
	}
}

// A login that can't be renewed is still given while it works, with a
// warning; once expired, it's left out and removed from the profile.
func TestRun_renewalFails(t *testing.T) {
	a := fakePiHome(t, false)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(time.Hour))
	warnings := runProfile(t, a, "p")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "couldn't renew") || !strings.Contains(warnings[0], "invalid_grant") {
		t.Errorf("warnings = %q", warnings)
	}
	if got := authEntry(t, a, "p")["access"]; got != "old-access" {
		t.Errorf("a still-working login wasn't given: %v", got)
	}

	saveCredential(t, a, "chatgpt", chatgptEntry(-time.Hour))
	warnings = runProfile(t, a, "p")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "has expired") {
		t.Errorf("warnings = %q", warnings)
	}
	if got := authEntry(t, a, "p"); got != nil {
		t.Errorf("an expired login is still in auth.json: %v", got)
	}
}

// An OAuth credential the run won't get is never renewed, so its single-use
// refresh token isn't spent for nothing.
func TestRun_renewsOnlyGranted(t *testing.T) {
	a := fakePiHome(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	useOpenAI(a, srv.URL)
	if _, err := a.CreateProfile(CreateOptions{Name: "keys", Harness: "fakepi", AllowedCredentials: []string{"openai"}}); err != nil {
		t.Fatal(err)
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(time.Hour))
	if warnings := runProfile(t, a, "keys"); len(warnings) != 0 {
		t.Errorf("warnings = %q", warnings)
	}

	// Held back by an exclusive group: openai comes first, so chatgpt isn't given.
	excl := `name = "excl"
binary = "true"
config_env = "EXCL_DIR"
secret_files = ["auth.json"]
secret_envs = ["EXCL_KEY"]
exclusive_credentials = [["openai", "chatgpt"]]
[credentials]
openai = { env = "EXCL_KEY" }
chatgpt = { auth_file = "auth.json", format = "pi-auth-json", key = "openai-codex" }
`
	if err := os.WriteFile(filepath.Join(a.home, "harnesses", "excl.toml"), []byte(excl), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateProfile(CreateOptions{Name: "grouped", Harness: "excl"}); err != nil {
		t.Fatal(err)
	}
	saveCredential(t, a, "openai", key("sk-1"))
	if warnings := runProfile(t, a, "grouped"); len(warnings) != 1 || !strings.Contains(warnings[0], "gets openai, not chatgpt") {
		t.Errorf("warnings = %q", warnings)
	}
}

// An OAuth credential without a refresh token (e.g. edited by hand) is
// never sent to the provider: it's given while it works, then dropped.
func TestRun_noRefreshTokenNoRequest(t *testing.T) {
	a := fakePiHome(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	useOpenAI(a, srv.URL)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	// Edited by hand: Save would refuse an OAuth credential without one.
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Join(a.home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	edited := `{"chatgpt": {"secret": "access", "added": "2026-10-08T00:00:00Z", "expires": "` + expires + `", "account": "acct"}}`
	if err := os.WriteFile(filepath.Join(a.home, "credentials", "credentials.json"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if warnings := runProfile(t, a, "p"); len(warnings) != 1 || !strings.Contains(warnings[0], "no refresh token") {
		t.Errorf("warnings = %q", warnings)
	}
}

// A login the user made inside the harness is kept, with a warning.
func TestRun_keepsUsersOwnLogin(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(10*24*time.Hour))
	own := `{"openai-codex": {"type": "oauth", "access": "theirs", "refresh": "rt-real", "expires": 1, "accountId": "a"}}`
	if err := os.WriteFile(filepath.Join(a.profiles.ConfigDir("p"), "auth.json"), []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings := runProfile(t, a, "p")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "its own chatgpt login") {
		t.Errorf("warnings = %q", warnings)
	}
	if got := authEntry(t, a, "p")["access"]; got != "theirs" {
		t.Errorf("the user's own login was replaced: %v", got)
	}
}

// A login revoked after a run was planned isn't written back when it
// starts: the run re-reads the saved logins with the profile locked.
func TestProvisionAuthFiles_rereadsSaved(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	reg, _ := a.harnesses()
	spec, _ := reg.Get("fakepi")
	// Planned with chatgpt granted, but no longer saved.
	if _, err := a.provisionAuthFiles("p", spec, []string{"chatgpt"}); err != nil {
		t.Fatal(err)
	}
	if got := authEntry(t, a, "p"); got != nil {
		t.Errorf("a revoked login was written: %v", got)
	}
}

// A broken auth.json doesn't stop a profile that doesn't get the login
// kept there: removing an old one only warns.
func TestRun_brokenAuthFileOnlyWarns(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.profiles.ConfigDir("p"), "auth.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings := runProfile(t, a, "p")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "couldn't check p for an old chatgpt login") {
		t.Errorf("warnings = %q", warnings)
	}
}

// The user's own symlinked auth.json (e.g. to their main pi login) isn't
// agents' to touch: a profile that doesn't get chatgpt runs without a
// warning, one that does runs with a warning (not an error), and revoke
// doesn't fail on it.
func TestRun_usersSymlinkedAuthFile(t *testing.T) {
	a := fakePiHome(t, true)
	if _, err := a.CreateProfile(CreateOptions{Name: "p", Harness: "fakepi", AllowedCredentials: []string{"openai"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateProfile(CreateOptions{Name: "all", Harness: "fakepi"}); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(mine, []byte(`{"anthropic": {"type": "api_key", "key": "sk-mine"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "all"} {
		if err := os.Symlink(mine, filepath.Join(a.profiles.ConfigDir(name), "auth.json")); err != nil {
			t.Fatal(err)
		}
	}
	if warnings := runProfile(t, a, "p"); len(warnings) != 0 {
		t.Errorf("warnings = %q", warnings)
	}
	saveCredential(t, a, "chatgpt", chatgptEntry(10*24*time.Hour))
	if warnings := runProfile(t, a, "all"); len(warnings) != 1 || !strings.Contains(warnings[0], "uses its own chatgpt login") {
		t.Errorf("warnings = %q", warnings)
	}
	if r, err := a.RevokeCredential("chatgpt"); err != nil || len(r.ClearedFrom) != 0 {
		t.Errorf("RevokeCredential = %+v, %v", r, err)
	}
	if data, _ := os.ReadFile(mine); !strings.Contains(string(data), "sk-mine") {
		t.Errorf("the user's file changed: %s", data)
	}
}
