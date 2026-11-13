# agents-cli

`agents` is a CLI for experimenting with different agent harnesses and setups. It starts with [pi](https://github.com/earendil-works/pi) and Claude Code. You log in to each provider once, and every setup shares that login.

Each setup is a **profile**: one harness with its own settings, skills, extensions and sessions. Profiles don't affect each other or your normal `~/.pi` and `~/.claude` setups. Use them to try a new extension in a throwaway profile, or to compare pi and Claude Code on the same repo.

## Install

You need Go 1.26 or later, plus `pi` and/or `claude` installed.

```sh
git clone https://github.com/snreynolds/agents-cli
cd agents-cli
make install        # installs agents to ~/.local/bin; make sure that's on your PATH
```

## Get started

**1. Log in to a provider.** Run this once per login; every profile shares it.

```sh
agents auth login              # pick from a list
agents auth login chatgpt      # ChatGPT Plus/Pro, for pi (signs in with your browser)
agents auth login claude       # Claude Pro/Max, for Claude Code (runs claude setup-token)
pbpaste | agents auth login openrouter   # API keys: anthropic, openai, openrouter
```

**2. Create a profile.**

```sh
agents new sara-agent pi
agents new reviewer claude-code --credentials claude      # limit which logins it gets
agents new trial pi --from ~/.pi/agent                    # copy settings, skills and extensions
```

**3. Run it** in the folder you want to work in:

```sh
agents run sara-agent
agents run sara-agent --continue    # anything after the name goes to the harness
```

Other commands: `agents ls`, `agents rm <name>`, `agents auth list`, `agents auth revoke <id>`.

## Good to know

- **Profiles aren't a security boundary.** A harness runs as you, so it can read any file you can, including the logins agents saves. A sandbox is planned.
- **Revoking only stops agents handing a login out.** `agents auth revoke` doesn't invalidate the key or token with the provider; do that on their site.
- **A Claude token lasts a year.** When it expires, Claude Code fails with a 401. Run `agents auth login claude` again.
- **By default, new pi profiles don't load `~/.agents/skills`,** because profiles are meant to start fresh, so you can test new skills and extensions on their own. To use that folder in a profile, remove the `!…/.agents/skills/**` entry from the profile's `config/settings.json`.
- **Env vars:** if `PI_CODING_AGENT_SESSION_DIR` is set, pi saves the profile's sessions there instead of in the profile; unset it.

**Status:** early development.

## License

MIT
