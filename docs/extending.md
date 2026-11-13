# Extending agents

Step-by-step guides for the two common extensions. Read `AGENTS.md` first.

## Adding a harness

A harness is data: one `harness.Spec`, either in `builtins()` in `internal/harness/builtin.go`, or as a user TOML file in `<home>/harnesses/<name>.toml` with the same fields in snake_case. No core code changes.

### 1. Check that it fits

Answer each question from the harness's docs or source, and note where you found the answer. You'll put that in a code comment.

| Question | Fits if |
|---|---|
| Is there **one env var that moves all its state** (settings, sessions, logins)? | Yes, and it names the config folder itself (like `PI_CODING_AGENT_DIR`). An env var naming a parent folder, or replacing `HOME`, doesn't fit. |
| Where does it keep **logins**? | Env vars (best), or a file agents can write. Not the OS keychain. |
| Does it **renew OAuth tokens** itself? | API keys or long-lived tokens only, or OAuth where it re-reads its file before renewing. Only agents holds a real refresh token. |
| Does it have an **interactive terminal mode** you run in a repo and exit? | Yes. A server- or gateway-only harness doesn't fit. |

If the first, second or fourth question fails, stop: it needs design work, not a new Spec.

### 2. Classify its config folder

Install it, use it once (log in, chat, change a setting, add an extension), then list its config folder. Put **every** entry in exactly one group:

| Kind | Goes in | pi examples |
|---|---|---|
| Setup: settings, instructions, skills, extensions | `SetupFiles` | `settings.json`, `AGENTS.md`, `skills`, `extensions` |
| A setup file with a key that must not be copied | `SetupFiles` + `DropJSONKeys` | `settings.json` → drop `sessionDir` |
| Logins and secrets | `SecretFiles` | `auth.json`, `mcp-auth.json` |
| State: sessions, caches, logs, memory | nothing (anything not in `SetupFiles` is never copied) | `sessions` |

Entries are single names in the config folder: no `/`, `.` or `..`. Then find **every env var it reads a key or token from** by searching its source for `API_KEY` and `TOKEN`. List them all in `SecretEnvs`, so a key exported in the user's shell can't override the profile's credentials. Leave out general tool credentials, such as AWS or GitHub, that the agent's own tools may need. Leave out switches that choose a provider, such as `CLAUDE_CODE_USE_BEDROCK`.

### 3. Write the Spec

```go
{
	Name:         "example",                 // what users type: agents new x example
	Binary:       "example",                 // what agents run starts
	ConfigEnv:    "EXAMPLE_HOME",            // moves all its state into the profile's config/
	SetupFiles:   []string{"config.json", "skills"},
	DropJSONKeys: map[string][]string{"config.json": {"sessionDir"}},
	SecretFiles:  []string{"auth.json"},
	SecretEnvs:   []string{"OPENROUTER_API_KEY", "EXAMPLE_TOKEN"},
	Credentials: map[string]Binding{         // credential ID → how this harness receives it
		"openrouter": {Env: "OPENROUTER_API_KEY"},
	},
},
```

In TOML:

```toml
name = "example"
binary = "example"
config_env = "EXAMPLE_HOME"
setup_files = ["config.json", "skills"]
secret_files = ["auth.json"]
secret_envs = ["OPENROUTER_API_KEY", "EXAMPLE_TOKEN"]

[credentials]
openrouter = { env = "OPENROUTER_API_KEY" }
```

- **Reuse credential IDs** where the meaning is the same: `claude`, `chatgpt`, `anthropic`, `openai`, `openrouter`.
- **Each binding's env var must also be in `SecretEnvs`,** and each `auth_file` in `SecretFiles`. `Spec.Validate` checks this.
- **Prefer env vars.** Only renewable (OAuth) credentials may use `AuthFile` + `Format` + `Key`. A new file layout needs a format name and writer in `internal/credential/harnessauthfile.go`, plus its name in `Formats()`.
- **`ExclusiveCredentials`** lists groups the harness must not get together, because it would silently prefer one (Claude Code prefers an API key over a subscription token).
- **`JSONListItems`** adds list entries to every new profile's JSON settings. pi uses it to exclude `~/.agents/skills`.
- **Comment each non-obvious choice** with its source and the harness version you checked, as the existing entries in `builtin.go` do.

### 4. Test it

- **Automated:** `TestBuiltins_valid` runs `Validate` on every built-in. Add a test for anything special about the new entry, e.g. `TestBuiltins_claudeCodeStripsShellLogins`.
- **By hand, always in a throwaway home:**

  ```sh
  export AGENTS_HOME=$(mktemp -d)
  make build
  ./bin/agents new t example --from ~/.example    # the real folder is only read
  ls -A $AGENTS_HOME/profiles/t/config            # only setup files?
  ./bin/agents run t                              # sessions land in the profile's config/, not in ~?
  ./bin/agents rm t --yes
  ```

## Adding a credential or a provider

A credential is one entry in `credential.Supported()` (`internal/credential/credential.go`):

```go
{ID: "openrouter", Name: "OpenRouter API key", Source: APIKey{}},
```

- **ID naming:** subscriptions are named after the product (`claude`, `chatgpt`), API keys after the company (`anthropic`, `openai`). `Hint` is optional text shown in the login picker.
- **A new way of signing in is a new `Source`.** It's one method, and it talks to the user only through `credential.UI`:

  ```go
  type APIKey struct{}

  func (APIKey) SignIn(_ context.Context, c Spec, ui UI) (Entry, error) {
      key, err := ui.AskSecret(fmt.Sprintf("Paste your %s:", c.Name))
      return Entry{Secret: key}, err
  }
  ```

  `UI` offers `Interactive`, `AskSecret`, `ShowSignInCode` and `RunProgram`. The CLI implements it as `terminalUI`, and tests use a fake.
- **If its credentials expire, also implement `Renewer`** (`Renew(ctx, Entry) (Entry, error)`):
  - Return `Refresh`, `Expires` and, if a harness's auth file needs it, `Account`.
  - `Store.Renew` calls it under the store's lock, only when `Entry.RenewDue()` says it's due.
- **A cancel (Ctrl-C, i.e. `ctx` done) returns an error matching `ErrCancelled`.**
- **`Store.Save` checks every entry:**
  - a renewable credential must have a refresh token and an expiry;
  - a key must not;
  - a secret can't be empty or contain whitespace or control characters.
  - Don't repeat these checks in the Source.
- **Talking to a provider over HTTP:**
  - inject the base URL (as `ChatGPT.AuthBase` does), so tests use `httptest`;
  - use a client with a timeout that doesn't follow redirects;
  - cap response sizes;
  - never put a reply's body in an error.
- **Then let harnesses use it:** add a `Binding` for it to each harness that supports it (see step 3 above).
