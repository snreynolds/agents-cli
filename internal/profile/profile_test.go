package profile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStore(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "profiles"))
	p := Profile{Name: "fast", Harness: "pi", AllowedCredentials: []string{"*"}}

	if s.Exists("fast") {
		t.Error("Exists before Create")
	}
	if err := s.Create(p); err != nil {
		t.Fatal(err)
	}
	if !s.Exists("fast") {
		t.Error("Exists after Create = false")
	}
	if err := s.Create(p); err == nil {
		t.Error("creating an existing profile should fail")
	}
	for _, dir := range []string{"", "config", "run"} {
		info, err := os.Stat(filepath.Join(filepath.Join(s.root, "fast"), dir))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s/ mode = %o, want 700", dir, perm)
		}
	}

	got, err := s.Load("fast")
	if err != nil || got.Harness != "pi" || !slices.Equal(got.AllowedCredentials, []string{"*"}) {
		t.Fatalf("Load = %+v, %v", got, err)
	}

	if err := s.Create(Profile{Name: "careful", Harness: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	list, _, _ := s.List()
	if len(list) != 2 || list[0].Name != "careful" || list[1].Name != "fast" {
		t.Errorf("List = %+v, want careful then fast", list)
	}

	if err := s.Remove("fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("fast"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Remove, Load error = %v, want ErrNotFound", err)
	}
	if err := s.Remove("fast"); err != nil {
		t.Errorf("removing a missing profile: %v, want no error", err)
	}
}

func TestCheckedDir(t *testing.T) {
	s := NewStore("/profiles")
	if dir, err := s.checkedDir("fast-1_a"); err != nil || dir != "/profiles/fast-1_a" {
		t.Errorf("checkedDir(fast-1_a) = %q, %v", dir, err)
	}
	for _, name := range []string{"", "..", "../x", "a/b", "Fast", ".hidden", strings.Repeat("a", 65)} {
		if _, err := s.checkedDir(name); err == nil {
			t.Errorf("checkedDir(%q) succeeded", name)
		}
	}
}

// Every Store method that touches disk refuses an unsafe name, whoever calls
// it.
func TestStore_rejectsUnsafeNames(t *testing.T) {
	s := NewStore(t.TempDir())
	const name = "../x"
	for method, call := range map[string]func() error{
		"Create":    func() error { return s.Create(Profile{Name: name}) },
		"Load":      func() error { _, err := s.Load(name); return err },
		"Remove":    func() error { return s.Remove(name) },
		"Lock":      func() error { _, err := s.Lock(name); return err },
		"AddRun":    func() error { _, err := s.AddRun(name, RunRecord{}); return err },
		"RemoveRun": func() error { return s.RemoveRun(name, "id") },
		"Runs":      func() error { _, err := s.Runs(name); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
			t.Errorf("%s(%q) = %v, want an invalid-name error", method, name, err)
		}
	}
	if s.Exists(name) {
		t.Errorf("Exists(%q) = true", name)
	}
}

// A broken profile.toml must not leave a profile stuck: ls reports it and
// rm can still delete it.
func TestStore_brokenProfile(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Create(Profile{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Join(s.root, "fast"), "profile.toml"), []byte("not = [toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, unreadable, err := s.List()
	if err != nil || len(profiles) != 0 || !slices.Equal(unreadable, []string{"fast"}) {
		t.Errorf("List = %v, %v, %v; want fast reported as unreadable", profiles, unreadable, err)
	}
	if !s.Exists("fast") {
		t.Error("Exists = false for a broken profile")
	}
	if err := s.Remove("fast"); err != nil {
		t.Errorf("Remove: %v", err)
	}
}

// A setting this version doesn't know (e.g. a sandbox default written by an
// older build) makes the profile unreadable rather than silently ignored.
func TestStore_unknownSettingIsUnreadable(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Create(Profile{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "fast", "profile.toml"), []byte("harness = 'pi'\n[defaults]\nsandbox = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("fast"); err == nil {
		t.Error("Load accepted an unknown setting")
	}
}

func TestProfile_AllowsCredential(t *testing.T) {
	for _, c := range []struct {
		allow []string
		id    string
		want  bool
	}{{[]string{"*"}, "openai", true}, {[]string{"openai"}, "openai", true}, {[]string{"claude"}, "openai", false}, {nil, "openai", false}} {
		if got := (Profile{AllowedCredentials: c.allow}).AllowsCredential(c.id); got != c.want {
			t.Errorf("%v.AllowsCredential(%q) = %v, want %v", c.allow, c.id, got, c.want)
		}
	}
}
