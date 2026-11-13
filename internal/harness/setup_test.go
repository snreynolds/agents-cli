package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --from copies only setup files, skips symlinks and the
// harness's login files (even inside copied folders), and drops sessionDir.
func TestCopySetup(t *testing.T) {
	src := t.TempDir()
	write(t, src, "settings.json", `{"defaultModel": "x", "sessionDir": "/elsewhere"}`)
	write(t, src, "models.json", `{"providers": {"p": {"apiKey": "sk-abcdefghijklmnopqrstuvwxyz"}}}`)
	write(t, src, "auth.json", `{"openai-codex": {}}`)
	write(t, src, "mcp-auth.json", `{}`)
	write(t, src, "trust.json", `{}`)
	write(t, src, "models-store.json", `{}`)
	write(t, src, "sessions/--repo--/s.jsonl", "chat")
	write(t, src, "install/releases/big", "binary")
	write(t, src, "extensions/mine/index.ts", "export {}")
	write(t, src, "extensions/mine/auth.json", `{"leaked": true}`) // never copied, even inside a listed folder
	write(t, src, "skills/s/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(src, "skills/s/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, src, "real-prompts/p.md", "prompt")
	symlink(t, "/etc/passwd", filepath.Join(src, "extensions", "link"))
	symlink(t, filepath.Join(src, "real-prompts"), filepath.Join(src, "prompts")) // a symlinked entry itself

	dst := t.TempDir()
	spec := Spec{
		SetupFiles:   []string{"settings.json", "models.json", "extensions", "skills", "prompts", "themes"},
		DropJSONKeys: map[string][]string{"settings.json": {"sessionDir"}},
		SecretFiles:  []string{"auth.json", "mcp-auth.json"},
	}
	warnings, err := spec.CopySetup(src, dst)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"settings.json", "models.json", "extensions/mine/index.ts", "skills/s/run.sh"} {
		if _, err := os.Stat(filepath.Join(dst, want)); err != nil {
			t.Errorf("%s was not copied", want)
		}
	}
	for _, never := range []string{"auth.json", "mcp-auth.json", "trust.json", "models-store.json", "sessions",
		"install", "extensions/link", "extensions/mine/auth.json", "prompts", "themes"} {
		if _, err := os.Lstat(filepath.Join(dst, never)); err == nil {
			t.Errorf("%s was copied", never)
		}
	}

	info, _ := os.Stat(filepath.Join(dst, "skills/s/run.sh"))
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("run.sh mode = %o, want 700 (still executable, owner only)", perm)
	}

	settings, _ := os.ReadFile(filepath.Join(dst, "settings.json"))
	if strings.Contains(string(settings), "sessionDir") || !strings.Contains(string(settings), "defaultModel") {
		t.Errorf("settings.json = %s, want sessionDir removed and the rest kept", settings)
	}

	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "skipped extensions/mine/auth.json") {
		t.Errorf("no warning for the login file inside extensions/ in %q", joined)
	}
	if !strings.Contains(joined, "skipped extensions/link (symlinks are never copied)") {
		t.Errorf("no symlink warning in %q", joined)
	}
}

// A settings file that can't be parsed must be skipped, not copied with
// sessionDir still in it.
func TestCopySetup_unparseableSettings(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "settings.json", `{"sessionDir": "/elsewhere", oops}`)
	spec := Spec{SetupFiles: []string{"settings.json"}, DropJSONKeys: map[string][]string{"settings.json": {"sessionDir"}}}
	warnings, err := spec.CopySetup(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "settings.json")); err == nil {
		t.Error("unparseable settings.json was copied")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped settings.json") {
		t.Errorf("warnings = %q", warnings)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// Settings that would move a profile's state into another config folder are
// dropped for each built-in harness: pi's sessionDir, Claude Code's
// autoMemoryDirectory.
func TestCopySetup_builtinsDropStateRedirects(t *testing.T) {
	cases := map[string]string{"pi": "sessionDir", "claude-code": "autoMemoryDirectory"}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			spec, err := r.Get(name)
			if err != nil {
				t.Fatal(err)
			}
			src, dst := t.TempDir(), t.TempDir()
			write(t, src, "settings.json", `{"model": "x", "`+key+`": "/somewhere/else"}`)
			if _, err := spec.CopySetup(src, dst); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dst, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(got), key) || !strings.Contains(string(got), `"model"`) {
				t.Errorf("settings.json = %s; want %s removed and model kept", got, key)
			}
		})
	}
}

// JSONListItems are added to a new profile's settings: created when missing,
// merged into an existing list without duplicates, other keys kept, and a
// file or key of the wrong shape left alone with a warning.
func TestAddJSONListItems(t *testing.T) {
	spec := Spec{JSONListItems: map[string]map[string][]string{
		"settings.json": {"skills": {"!${HOME}/.agents/skills/**"}},
		"other.json":    {"list": {"x"}},
		"bad.json":      {"list": {"x"}},
	}}
	dir := t.TempDir()
	write(t, dir, "other.json", `{"keep": 1, "list": ["a", "x"]}`)
	write(t, dir, "bad.json", `{"list": "not-a-list"}`)

	warnings, err := spec.AddJSONListItems(dir, "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bad.json") {
		t.Errorf("warnings = %q, want one about bad.json", warnings)
	}
	want := map[string]string{
		"settings.json": `{"skills":["!/home/u/.agents/skills/**"]}`,
		"other.json":    `{"keep":1,"list":["a","x"]}`,
		"bad.json":      `{"list":"not-a-list"}`,
	}
	for name, w := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(strings.Fields(string(got)), "") != w {
			t.Errorf("%s = %s, want %s", name, got, w)
		}
	}
}
