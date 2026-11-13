package harness

// builtins are the harnesses shipped with agents.
func builtins() []Spec {
	return []Spec{
		{
			Name:      "pi",
			Binary:    "pi",
			ConfigEnv: "PI_CODING_AGENT_DIR",
			SetupFiles: []string{
				"settings.json", "models.json", "keybindings.json", "mcp.json",
				"AGENTS.md", "AGENTS.override.md", "SYSTEM.md", "APPEND_SYSTEM.md",
				"extensions", "skills", "prompts", "themes",
			},
			DropJSONKeys: map[string][]string{"settings.json": {"sessionDir"}},
			// pi always loads ~/.agents/skills, whatever PI_CODING_AGENT_DIR
			// says (pi-coding-agent/dist/core/package-manager.js, pi 0.87.1 and 1.5.2);
			// a "!" pattern in settings.json skills turns it off.
			JSONListItems: map[string]map[string][]string{
				"settings.json": {"skills": {"!${HOME}/.agents/skills/**"}},
			},
			SecretFiles: []string{"auth.json", "mcp-auth.json"},
			// No claude: since April 2026, Claude subscriptions cover only
			// Anthropic's own apps, and pi's requests on a subscription token
			// bill pay-as-you-go "extra usage" (or fail with "out of extra
			// usage"). ANTHROPIC_OAUTH_TOKEN is still stripped below.
			//
			// pi keeps a ChatGPT login in auth.json under "openai-codex"
			// (pi-ai/dist/auth/oauth/openai-codex.js in pi 0.87.1; checked working
			// with pi 1.5.2).
			Credentials: map[string]Binding{
				"anthropic":  {Env: "ANTHROPIC_API_KEY"},
				"openai":     {Env: "OPENAI_API_KEY"},
				"openrouter": {Env: "OPENROUTER_API_KEY"},
				"chatgpt":    {AuthFile: "auth.json", Format: "pi-auth-json", Key: "openai-codex"},
			},
			// From pi-ai's env-api-keys.js (pi 1.5.2). General cloud and tool
			// credentials (AWS, Google Cloud, Hugging Face, GitHub) are left
			// alone: tools the agent runs may need them.
			SecretEnvs: []string{
				"AI_GATEWAY_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_OAUTH_TOKEN",
				"ANT_LING_API_KEY", "AZURE_OPENAI_API_KEY", "BASETEN_API_KEY", "CEREBRAS_API_KEY",
				"CLOUDFLARE_API_KEY", "DEEPSEEK_API_KEY", "FIREWORKS_API_KEY", "GEMINI_API_KEY",
				"GROQ_API_KEY", "KIMI_API_KEY", "META_API_KEY", "MINIMAX_API_KEY", "MINIMAX_CN_API_KEY",
				"MISTRAL_API_KEY", "MOONSHOT_API_KEY", "NVIDIA_API_KEY", "OPENAI_API_KEY", "OPENCODE_API_KEY",
				"OPENROUTER_API_KEY", "QWEN_TOKEN_PLAN_API_KEY", "QWEN_TOKEN_PLAN_CN_API_KEY", "RADIUS_API_KEY",
				"TOGETHER_API_KEY", "TYPESAFE_API_KEY", "XAI_API_KEY", "XIAOMI_API_KEY", "XIAOMI_TOKEN_PLAN_AMS_API_KEY",
				"XIAOMI_TOKEN_PLAN_CN_API_KEY", "XIAOMI_TOKEN_PLAN_SGP_API_KEY", "ZAI_API_KEY", "ZAI_CODING_CN_API_KEY",
			},
		},
		{
			Name:      "claude-code",
			Binary:    "claude",
			ConfigEnv: "CLAUDE_CONFIG_DIR",
			SetupFiles: []string{
				"settings.json", "CLAUDE.md", "keybindings.json",
				"agents", "commands", "skills", "output-styles",
			},
			// autoMemoryDirectory would point the profile at another folder's memory.
			DropJSONKeys: map[string][]string{"settings.json": {"autoMemoryDirectory"}},
			SecretFiles:  []string{".credentials.json"},
			Credentials: map[string]Binding{
				"claude":    {Env: "CLAUDE_CODE_OAUTH_TOKEN"},
				"anthropic": {Env: "ANTHROPIC_API_KEY"},
			},
			// Every login Claude Code 2.1.323 reads from the environment.
			// Provider switches (CLAUDE_CODE_USE_BEDROCK, …) aren't logins and
			// are left alone: the user may route Claude Code on purpose.
			SecretEnvs: []string{
				"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_AWS_API_KEY",
				"ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_AUTH_TOKEN",
				"ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE",
				"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN",
				"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
			},
			// Claude Code uses ANTHROPIC_API_KEY over CLAUDE_CODE_OAUTH_TOKEN, so
			// given both it would silently bill the API key. Prefer the
			// subscription; a profile that wants the key allows only anthropic.
			ExclusiveCredentials: [][]string{{"claude", "anthropic"}},
		},
	}
}
