package credential

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// piFile is where the built-in pi harness keeps chatgpt: auth.json in its
// config folder dir.
func piFile(dir string) HarnessAuthFile {
	return HarnessAuthFile{Dir: dir, File: "auth.json", Format: piAuthJSON, Key: "openai-codex"}
}

var expires = time.UnixMilli(1791505154000)

// readAuth decodes auth.json in dir.
func readAuth(t *testing.T, dir string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

// The login is written as pi reads it, with the placeholder refresh token,
// keeping the file's other entries, with mode 0600.
func TestProvision(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.json", `{"anthropic": {"type": "api_key", "key": "sk-mine"}}`)
	if written, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil || !written {
		t.Fatalf("Provision = %v, %v", written, err)
	}
	entries := readAuth(t, dir)
	got := entries["openai-codex"]
	if got["type"] != "oauth" || got["access"] != "access-1" || got["refresh"] != managedRefresh ||
		got["expires"] != float64(1791505154000) || got["accountId"] != "acct-1" {
		t.Errorf("entry = %v", got)
	}
	if entries["anthropic"]["key"] != "sk-mine" {
		t.Errorf("the other entry changed: %v", entries["anthropic"])
	}
	if info, _ := os.Stat(filepath.Join(dir, "auth.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	// Writing again replaces agents' own entry.
	if written, err := Provision(piFile(dir), Entry{Secret: "access-2", Expires: expires, Account: "acct-1"}); err != nil || !written {
		t.Fatalf("second Provision = %v, %v", written, err)
	}
	if got := readAuth(t, dir)["openai-codex"]["access"]; got != "access-2" {
		t.Errorf("access = %v, want access-2", got)
	}
}

// A login the user made inside pi (a real refresh token) is never replaced
// or removed.
func TestProvision_keepsUsersOwnLogin(t *testing.T) {
	dir := t.TempDir()
	own := `{"openai-codex": {"type": "oauth", "access": "theirs", "refresh": "rt-real", "expires": 1, "accountId": "a"}}`
	writeFile(t, dir, "auth.json", own)
	if written, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil || written {
		t.Errorf("Provision = %v, %v; want left alone", written, err)
	}
	if removed, err := Unprovision(piFile(dir)); err != nil || removed {
		t.Errorf("Unprovision = %v, %v; want left alone", removed, err)
	}
	if got := readAuth(t, dir)["openai-codex"]["access"]; got != "theirs" {
		t.Errorf("the user's login changed: access = %v", got)
	}
}

func TestUnprovision(t *testing.T) {
	dir := t.TempDir()
	if removed, err := Unprovision(piFile(dir)); err != nil || removed {
		t.Errorf("Unprovision with no file = %v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); !os.IsNotExist(err) {
		t.Error("Unprovision created auth.json")
	}
	writeFile(t, dir, "auth.json", `{"anthropic": {"type": "api_key", "key": "sk-mine"}}`)
	if _, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil {
		t.Fatal(err)
	}
	if removed, err := Unprovision(piFile(dir)); err != nil || !removed {
		t.Fatalf("Unprovision = %v, %v", removed, err)
	}
	entries := readAuth(t, dir)
	if _, ok := entries["openai-codex"]; ok || entries["anthropic"]["key"] != "sk-mine" {
		t.Errorf("after Unprovision: %v", entries)
	}
}

// A symlinked auth.json is never written through (so a link planted in a
// profile can't copy logins into another auth.json): it's left alone, like
// the user's own login.
func TestProvision_leavesSymlinkAlone(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	writeFile(t, elsewhere, "auth.json", `{"anthropic": {"type": "api_key", "key": "sk-main"}}`)
	if err := os.Symlink(filepath.Join(elsewhere, "auth.json"), filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if written, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil || written {
		t.Errorf("Provision = %v, %v; want not written, no error", written, err)
	}
	if info, err := os.Lstat(filepath.Join(dir, "auth.json")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced")
	}
	if entries := readAuth(t, elsewhere); len(entries) != 1 {
		t.Errorf("the linked file was changed: %v", entries)
	}
}

// A damaged file is reported without quoting it: it holds secrets.
func TestProvision_damagedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "auth.json", `{"anthropic": "sk-SECRET`)
	_, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
}

// agents waits for pi's lock, and breaks one a crash left behind.
func TestProvision_lock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "auth.json.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(200 * time.Millisecond); _ = os.Remove(lock) }()
	start := time.Now()
	if _, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Error("didn't wait for the lock")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("the lock wasn't released")
	}

	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil {
		t.Errorf("a stale lock wasn't broken: %v", err)
	}
}

func TestProvision_badAuthFile(t *testing.T) {
	for name, f := range map[string]HarnessAuthFile{
		"unknown format": {Dir: t.TempDir(), File: "auth.json", Format: "yaml", Key: "k"},
		"no key":         {Dir: t.TempDir(), File: "auth.json", Format: piAuthJSON},
		"path in File":   {Dir: t.TempDir(), File: "../auth.json", Format: piAuthJSON, Key: "k"},
	} {
		if _, err := Provision(f, Entry{Secret: "a"}); err == nil {
			t.Errorf("%s: Provision succeeded, want an error", name)
		}
	}
}

// A named pipe planted at auth.json is left alone at once, not waited on.
func TestProvision_leavesNamedPipeAlone(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "auth.json"), 0o600); err != nil {
		t.Skip("can't make a named pipe:", err)
	}
	done := make(chan error, 1)
	go func() {
		written, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"})
		if written {
			err = errors.New("wrote through a named pipe")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("err = %v, want it left alone", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Provision is stuck on a named pipe")
	}
}

// A temp file a crashed write left behind (holding an access token) is
// deleted on the next change.
func TestUnprovision_deletesLeftovers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".auth.json.tmp-123", `{"openai-codex": {"access": "leftover"}}`)
	if _, err := Unprovision(piFile(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".auth.json.tmp-123")); !os.IsNotExist(err) {
		t.Error("the leftover temp file is still there")
	}
}

// A symlinked config folder is refused: writes through it would land in
// another folder, such as the user's main pi config.
func TestProvision_refusesSymlinkedConfigDir(t *testing.T) {
	real, link := t.TempDir(), filepath.Join(t.TempDir(), "config")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(piFile(link), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err == nil {
		t.Error("want an error for a symlinked config folder")
	}
	if _, err := os.Stat(filepath.Join(real, "auth.json")); !os.IsNotExist(err) {
		t.Error("wrote through the symlinked config folder")
	}
}

// A folder planted where a temp file would be doesn't block changes.
func TestUnprovision_ignoresPlantedTempFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".auth.json.tmp-x", "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(piFile(dir), Entry{Secret: "access-1", Expires: expires, Account: "acct-1"}); err != nil {
		t.Errorf("Provision = %v", err)
	}
}

// writeFile writes content to name in dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
