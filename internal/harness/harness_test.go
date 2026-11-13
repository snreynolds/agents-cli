package harness

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltins_valid(t *testing.T) {
	for _, s := range builtins() {
		if err := s.Validate(); err != nil {
			t.Errorf("built-in %s: %v", s.Name, err)
		}
	}
}

// Claude Code logins that QA found reaching runs from the shell are stripped.
func TestBuiltins_claudeCodeStripsShellLogins(t *testing.T) {
	r, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec, err := r.Get("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "ANTHROPIC_FOUNDRY_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"} {
		if !slices.Contains(spec.SecretEnvs, env) {
			t.Errorf("claude-code SecretEnvs is missing %s", env)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "mine.toml", `
name = "mine"
binary = "mine"
config_env = "MINE_HOME"
setup_files = ["config.yaml"]
secret_envs = ["OPENROUTER_API_KEY"]
[credentials]
openrouter = { env = "OPENROUTER_API_KEY" }
`)
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Get("mine")
	if err != nil || s.ConfigEnv != "MINE_HOME" {
		t.Fatalf("Get(mine) = %+v, %v", s, err)
	}
	if got := r.Names(); !slices.Equal(got, []string{"claude-code", "mine", "pi"}) {
		t.Errorf("Names() = %v", got)
	}
}

func TestLoad_rejectsBadHarness(t *testing.T) {
	base := "binary = \"x\"\nconfig_env = \"X\"\n"
	cases := map[string]string{
		"builtin name":             "name = \"pi\"\n" + base,
		"bad name":                 "name = \"My Harness\"\n" + base,
		"no binary":                "name = \"x\"\nconfig_env = \"X\"\n",
		"copies a secret file":     "name = \"x\"\n" + base + "setup_files = [\"auth.json\"]\nsecret_files = [\"auth.json\"]\n",
		"typo'd field":             "name = \"x\"\n" + base + "setup_files = [\"auth.json\"]\nsecret_fles = [\"auth.json\"]\n",
		"nested secret file":       "name = \"x\"\n" + base + "secret_files = [\"sub/auth.json\"]\n",
		"escapes folder":           "name = \"x\"\n" + base + "setup_files = [\"../secrets\"]\n",
		"whole folder":             "name = \"x\"\n" + base + "setup_files = [\"./\"]\n",
		"dot":                      "name = \"x\"\n" + base + "setup_files = [\".\"]\n",
		"nested":                   "name = \"x\"\n" + base + "setup_files = [\"sub/conf.json\"]\n",
		"nested json list file":    "name = \"x\"\n" + base + "[json_list_items]\n\"sub/s.json\" = { skills = [\"x\"] }\n",
		"env not secret":           "name = \"x\"\n" + base + "[credentials]\nopenai = { env = \"X_KEY\" }\n",
		"unknown credential":       "name = \"x\"\n" + base + "secret_envs = [\"X_KEY\"]\n[credentials]\ngroq = { env = \"X_KEY\" }\n",
		"group of one":             "name = \"x\"\n" + base + "secret_envs = [\"A\"]\nexclusive_credentials = [[\"openai\"]]\n[credentials]\nopenai = { env = \"A\" }\n",
		"two credentials, one var": "name = \"x\"\n" + base + "secret_envs = [\"A\"]\n[credentials]\nopenai = { env = \"A\" }\nanthropic = { env = \"A\" }\n",
		"env is config_env":        "name = \"x\"\n" + base + "secret_envs = [\"X\"]\n[credentials]\nopenai = { env = \"X\" }\n",
		"bad env name":             "name = \"x\"\n" + base + "secret_envs = [\"A=B\"]\n[credentials]\nopenai = { env = \"A=B\" }\n",
		"group not delivered":      "name = \"x\"\n" + base + "secret_envs = [\"A\"]\nexclusive_credentials = [[\"openai\", \"claude\"]]\n[credentials]\nopenai = { env = \"A\" }\n",
		"file not secret":          "name = \"x\"\n" + base + "[credentials]\nchatgpt = { auth_file = \"auth.json\", format = \"pi-auth-json\", key = \"k\" }\n",
		"key in a file":            "name = \"x\"\n" + base + "secret_files = [\"auth.json\"]\n[credentials]\nopenai = { auth_file = \"auth.json\", format = \"pi-auth-json\", key = \"k\" }\n",
		"setup token in a file":    "name = \"x\"\n" + base + "secret_files = [\"auth.json\"]\n[credentials]\nclaude = { auth_file = \"auth.json\", format = \"pi-auth-json\", key = \"k\" }\n",
		"unknown format":           "name = \"x\"\n" + base + "secret_files = [\"auth.json\"]\n[credentials]\nchatgpt = { auth_file = \"auth.json\", format = \"yaml\", key = \"k\" }\n",
		"no key":                   "name = \"x\"\n" + base + "secret_files = [\"auth.json\"]\n[credentials]\nchatgpt = { auth_file = \"auth.json\", format = \"pi-auth-json\" }\n",
		"env and file":             "name = \"x\"\n" + base + "secret_files = [\"auth.json\"]\nsecret_envs = [\"A\"]\n[credentials]\nchatgpt = { env = \"A\", auth_file = \"auth.json\", format = \"pi-auth-json\", key = \"k\" }\n",
		"neither env nor file":     "name = \"x\"\n" + base + "[credentials]\nopenai = {}\n",
		"key with env":             "name = \"x\"\n" + base + "secret_envs = [\"A\"]\n[credentials]\nopenai = { env = \"A\", key = \"k\" }\n",
		"typo'd binding field":     "name = \"x\"\n" + base + "secret_files = [\"auth.json\"]\n[credentials]\nchatgpt = { auth_file = \"auth.json\", fromat = \"pi-auth-json\", key = \"k\" }\n",
		"old credential_envs":      "name = \"x\"\n" + base + "secret_envs = [\"A\"]\n[credential_envs]\nopenai = \"A\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "h.toml", content)
			if _, err := Load(dir); err == nil {
				t.Error("Load succeeded, want an error")
			}
		})
	}
}

// Every problem is reported at once, not one per attempt.
func TestValidate_reportsAllProblems(t *testing.T) {
	err := Spec{Name: "x", SetupFiles: []string{"../a"}}.Validate()
	for _, want := range []string{"binary is required", "config_env is required", `"../a"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestGet_unknownListsKnown(t *testing.T) {
	r, _ := Load(t.TempDir())
	_, err := r.Get("hermes")
	if err == nil || !strings.Contains(err.Error(), "claude-code, pi") {
		t.Errorf("error = %v, want it to list the known harnesses", err)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
