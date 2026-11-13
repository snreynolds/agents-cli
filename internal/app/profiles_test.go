package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/snreynolds/agents-cli/internal/harness"
	"github.com/snreynolds/agents-cli/internal/process"
	"github.com/snreynolds/agents-cli/internal/profile"
)

func TestCreateProfile(t *testing.T) {
	a, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := a.CreateProfile(CreateOptions{Name: "fast", Harness: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ConfigDir != filepath.Join(a.home, "profiles", "fast", "config") {
		t.Errorf("ConfigDir = %s", created.ConfigDir)
	}
	p, _ := a.profiles.Load("fast")
	if !slices.Equal(p.AllowedCredentials, []string{"*"}) {
		t.Errorf("default allow = %v, want [*]", p.AllowedCredentials)
	}
}

// A new pi profile starts fresh: its settings keep out the user's global
// ~/.agents/skills, which pi would otherwise load in every profile.
func TestCreateProfile_keepsOutGlobalSkills(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	a, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := a.CreateProfile(CreateOptions{Name: "fast", Harness: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(created.ConfigDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"!/home/u/.agents/skills/**"`) {
		t.Errorf("settings.json = %s, want the ~/.agents/skills exclude", got)
	}
}

func TestCreateProfile_rejectsBadInput(t *testing.T) {
	cases := map[string]CreateOptions{
		"unknown harness":         {Name: "x", Harness: "hermes"},
		"credential not accepted": {Name: "x", Harness: "claude-code", AllowedCredentials: []string{"openai"}},
		"missing --from folder":   {Name: "x", Harness: "pi", From: "/does/not/exist"},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			a, _ := New(t.TempDir())
			if _, err := a.CreateProfile(o); err == nil {
				t.Fatal("CreateProfile succeeded, want an error")
			}
			if _, err := os.Stat(filepath.Join(a.home, "profiles", "x")); !os.IsNotExist(err) {
				t.Error("a half-made profile was left behind")
			}
		})
	}
}

// Every input problem is reported together, not one per attempt.
func TestCreateProfile_reportsAllProblems(t *testing.T) {
	a, _ := New(t.TempDir())
	_, err := a.CreateProfile(CreateOptions{
		Name: "Bad Name", Harness: "pi", From: "/does/not/exist", AllowedCredentials: []string{"bogus"},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"invalid profile name", "--from", `"bogus"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestRemoveProfile(t *testing.T) {
	a, _ := New(t.TempDir())
	if _, err := a.CreateProfile(CreateOptions{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveProfile("fast"); err != nil {
		t.Fatal(err)
	}
	if a.profiles.Exists("fast") {
		t.Error("profile still exists after RemoveProfile")
	}
	if err := a.RemoveProfile("fast"); err == nil {
		t.Error("removing a missing profile should fail")
	}
}

// Both the early check and RemoveProfile itself refuse a running profile, so
// RemoveProfile is safe whatever its caller did first.
func TestRemoveProfile_refusesWhileRunning(t *testing.T) {
	a, _ := New(t.TempDir())
	if _, err := a.CreateProfile(CreateOptions{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	start, _ := process.StartTime(os.Getpid())
	if _, err := a.profiles.AddRun("fast", profile.RunRecord{PID: os.Getpid(), Start: start}); err != nil {
		t.Fatal(err)
	}
	if err := a.checkNotRunning("fast"); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Errorf("checkNotRunning: got %v, want a refusal", err)
	}
	for _, f := range []func(string) error{a.CheckRemoveProfile, a.RemoveProfile} { // both callers
		if err := f("fast"); err == nil || !strings.Contains(err.Error(), "is running") {
			t.Errorf("got %v, want a refusal", err)
		}
	}
	if !a.profiles.Exists("fast") {
		t.Error("running profile was deleted")
	}
}

func TestAllowedCredentials(t *testing.T) {
	spec := harness.Spec{Name: "pi", Credentials: map[string]harness.Binding{
		"openai":     {Env: "OPENAI_API_KEY"},
		"openrouter": {Env: "OPENROUTER_API_KEY"},
	}}
	cases := []struct {
		name string
		ids  []string
		want []string // nil = expect an error
	}{
		{"not given means all", nil, []string{"*"}},
		{"given empty means none", []string{}, []string{}},
		{"star means all", []string{"*"}, []string{"*"}},
		{"specific", []string{"openai"}, []string{"openai"}},
		{"duplicates removed, sorted", []string{"openrouter", "openai", "openrouter"}, []string{"openai", "openrouter"}},
		{"star mixed with ids", []string{"*", "openai"}, nil},
		{"not accepted", []string{"claude"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := allowedCredentials(spec, c.ids)
			if c.want == nil {
				if err == nil {
					t.Errorf("got %v, want an error", got)
				}
				return
			}
			if err != nil || !slices.Equal(got, c.want) {
				t.Errorf("got %v, %v; want %v", got, err, c.want)
			}
		})
	}
}
