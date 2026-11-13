# AGENTS.md

`agents` is a Go CLI that runs coding-agent harnesses (pi, Claude Code) in named **profiles**. Each profile has its own config folder, and all profiles share one set of saved provider logins (**credentials**). This file is for anyone changing the code, people and coding agents alike. User docs are in `README.md`. To add a harness or a provider, follow `docs/extending.md`.

## Commands

Requires Go 1.26 (see `go.mod`). The dependencies are cobra, go-toml v2, huh v2 and x/term.

```sh
make build                                   # bin/agents
make test                                    # go test -race -count=1 ./...
make lint                                    # pinned golangci-lint, the same as CI; must report 0 issues
go test ./internal/credential -run TestStore_Renew   # one test
gofmt -l .                                   # must print nothing
```

CI (`.github/workflows/ci.yml`) runs build, test, `agents --version` and lint. To try a build by hand, always use a throwaway home:

```sh
export AGENTS_HOME=$(mktemp -d)              # or pass --home on every command
./bin/agents new t pi && ./bin/agents run t -- --version
```

## Boundaries

- **Always:**
  - run `make test` and `make lint` before committing;
  - add a regression test with every bug fix;
  - use `t.TempDir()` in tests and a temporary `--home` by hand.
- **Ask first:**
  - adding a dependency;
  - changing anything users or their files rely on: `profile.toml`, `credentials.json`, harness TOML fields, `--json` output, flags, exit codes.
- **Never:**
  - commit or print a secret, including in an error message;
  - run a development build against a real `~/.agents-cli`, `~/.pi` or `~/.claude`;
  - let a test reach a real provider (OAuth tests use `httptest`);
  - follow a symlink when reading or writing in a profile folder.

## Project structure

```
cmd/agents/          entry point: runs cli, maps errors to exit codes (cancelled = 130)
internal/cli         Cobra commands: parse flags, call one App method, print
internal/app         one method per user action; the only package that connects the others
internal/profile     a profile's own files: profile.toml, run records, the lock
internal/harness     describes harnesses as data (Spec, Registry); copies setup files for --from
internal/credential  credential lifecycle: catalog, sign-in, store, renewal, harness auth files
internal/process     starting a harness, signals, liveness of a PID, opening a browser
internal/atomicfile  write-then-rename; internal/filelock: cross-process lock
```

Imports point one way: `cli → app → profile, harness → credential, process → atomicfile, filelock`.

- **`cli` holds no logic.** It calls `App` methods, whose fields are private. It may use lower packages' types and errors, and do terminal I/O (`terminalUI`).
- **Every user action is an exported `App` method,** even a one-liner. That method checks its own inputs and rules every time, and never trusts `cli` to have done it.
- **Lower packages never import `app` or `cli`.** Only one imports another: `harness` imports `credential`, to validate bindings. `app` joins everything else.
- **Where code goes:**
  - a profile's own files → `profile`, mechanics only;
  - decisions, or anything involving more than one package → `app`;
  - a harness's description or setup files → `harness`, which never touches a credential;
  - anything about a credential → `credential`, which knows nothing about harnesses or profiles.
- **New packages are named for their job.** Never `util`, `helpers` or `common`.
- **Files are named by subject, the same in every layer** (`cli/profiles.go`, `app/profiles.go`). Helpers stay unexported next to their caller. Helpers shared within a package go in a topic file (`cli/output.go`).

## Key concepts

- **Harness specifics live only in data.** A harness is a `harness.Spec`: built-ins are in `harness/builtin.go`, and users add `<home>/harnesses/*.toml`.
  - Core code uses the Spec's concepts (`SetupFiles`, `SecretFiles`, `SecretEnvs`, `Credentials`…), never a harness's file names.
  - A new harness is a new Spec, never a new branch in core code.
- **A credential is a `credential.Spec`** in `credential.Supported()`, with a `Source` that signs in:
  - `APIKey`, `SetupToken` or `ChatGPT`;
  - a `Renewer` also renews.
  - Code never switches on the kind of credential. A capability is an interface check (`Spec.Renews()`).
- **A harness receives a credential through a `Binding`:** an env var, or an entry in its own **auth file** (`AuthFile` + `Format` + `Key`; only renewable credentials).
  - agents keeps the only real refresh token, and writes `agents-managed` in its place.
  - It never overwrites a login the user made inside the harness.
- **A profile allows credentials** (`allowed_credentials`, where `*` means all). A run gets a credential only if all three hold: its harness supports it, the profile allows it, and it's saved.

## Security rules

- **A profile's `config/` folder is untrusted,** because the harness and its extensions can write anything there. Code that reads or writes there must:
  - refuse symlinks;
  - read only regular files, never a pipe or device;
  - cap how much it reads;
  - never quote file contents in an error.
- **Secrets stay hidden.** `credential.Entry` hides its secrets from `%v`. Errors never include a server reply's body. `auth list` shows IDs and dates only.
- **Never guess a key's format.** A harness declares where its credentials go, and core code honours exactly that.
- **Decoding:** agents' own files are decoded strictly, so an unknown field is an error. A harness's files are decoded leniently, and the fields agents doesn't use are kept.
- **Writes:** folders `0700`, settings and secret files `0600`, and state files only through `atomicfile.Write`.
- **Locks:**
  - the credential store's lock for saving, renewing and removing (a refresh token works only once);
  - the profile's lock for starting a run, writing its auth file, and `rm`.
- **Broken state never blocks cleanup.** Report what can't be read, and keep `rm` and `revoke` working.
- **Harness TOML files are trusted like code.** Validation catches mistakes; it isn't a defence against a hostile file.

## Code style

- **Naming:** "credential" in code, "login" in text users read. The other credential words:
  - **sign in:** obtain a secret;
  - **provision:** write a credential into a harness's auth file;
  - **allow:** what a profile does with a credential;
  - **support:** what a harness does with a credential.
- **Constructors** are `New`/`NewX` (or `Load` when they read files). **Actions** are verbs (`CreateProfile`), with input structs named after them (`CreateOptions`).
- **No stutter** (`harness.Spec`, `Store.Exists`). One meaning per word. No look-alike types: stored types are separate from `--json` output types. Don't store what's derived (a profile's name is its folder name). Slice fields have plural names.
- **Structs plus methods,** no hierarchies. Add an interface only when real alternative implementations exist.
- **Every exported name has a doc comment** starting with its name. Comments say *why*. A doc comment states the contract: what the function guarantees, and what it assumes.
- **Keep the main flow to the steps that can fail.** Collect input validation into one `check…` helper with `errors.Join`. A `check…` function never writes, and neither does any read-only command.
- **A command's `RunE` reads as its steps** and stays inline in `newXCmd`. Use `printTable`, `printJSON`, `confirm` and `printWarnings` from `cli/output.go`.
- **Keep code minimal.** No checks for mistakes nobody can make, and no fields for cases that don't exist yet.

## Errors and output

- **Error messages** start lowercase (a proper noun keeps its capital), have no trailing period, and name the fix: `unknown harness "x" (known: claude-code, pi)`.
- **Wrap with context:** `fmt.Errorf("doing x: %w", err)`. Ignore errors only explicitly (`_ = f.Close()`).
- **stdout carries results** (tables, `--json`). **stderr carries everything else:** prompts, progress, errors, and `warning: …`. Always write through `cmd.OutOrStdout()` and `cmd.ErrOrStderr()`.
- **Prompts default to no** (`[y/N]`).
- **Exit codes:** a cancel returns `ErrCancelled` (exit 130), and `run` exits with the harness's own code.
- **`--json` is a public interface.** Only fields users need, and empty lists are `[]`.
- **Signals:** never `signal.Ignore` before starting a child, because the child inherits it. Use one buffered channel per group of signals. Signal tests drive a separate process (`TestStart_signals`).

## Testing

- **Test files sit next to the code,** in the same package.
- **Names:** `Test<Function>`, and `Test<Function>_<scenario>` for variants, with the scenario in lowerCamelCase.
- **Use table tests with `t.Run`** for many cases of one function.
- **Use the existing fakes:**
  - CLI tests run the real command tree against a temporary home (`run`, `mustRun`, `newFakeHome` in `internal/cli`), with a fake harness binary;
  - app tests use `fakePiHome`;
  - sign-in tests use a fake `credential.UI`;
  - OAuth tests point `ChatGPT.AuthBase` at an `httptest` server.

## Git workflow

- **Branch from `dev`;** merge back after review.
- **Commit messages:** a short imperative subject, then a body that says what changed and why.
- **Before committing:** `make test` and `make lint` pass, `gofmt -l .` prints nothing, every bug fix has a regression test, and doc comments, `README.md` and this file still match the code.
