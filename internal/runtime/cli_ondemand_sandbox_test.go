package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
)

// A failed on-demand install must stop the sandbox exec instead of silently
// running without the CLI on PATH.
func TestDockerSandboxOnDemandCLIPathReturnsInstallErrors(t *testing.T) {
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())
	t.Setenv("NPM_TEST_LOG", filepath.Join(t.TempDir(), "npm.log"))
	t.Setenv("NPM_TEST_BIN", "kimi")
	t.Setenv("NPM_TEST_FAIL", "1")
	sandbox := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}

	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	if got, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: "kimi", Argv: []string{"kimi"}}); err == nil || got != "" {
		t.Fatalf("npm agent CLI install failure: %q %v", got, err)
	}

	useFakeCommands(t, map[string]string{"npm": fakeNPM})
	if got, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: "claude-code", Argv: []string{"claude", "-p"}}); err == nil || got != "" {
		t.Fatalf("Claude Code install failure: %q %v", got, err)
	}
}

// Commands that never need an on-demand install must not touch npm at all.
func TestDockerSandboxOnDemandCLIPathSkipsWithoutInstalling(t *testing.T) {
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())
	t.Setenv("NPM_TEST_LOG", log)
	t.Setenv("NPM_TEST_BIN", "opencode")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	sandbox := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	for _, command := range []CommandSpec{
		{Provider: "opencode"},
		{Provider: "opencode", Argv: []string{}},
		{Provider: "unknown-engine", Argv: []string{"unknown-engine"}},
		{Provider: "", Argv: []string{"opencode"}},
		{Provider: "agy", Argv: []string{"agy"}},
		{Provider: "opencode", Argv: []string{"kimi"}},
	} {
		if got, err := sandbox.onDemandCLIPath(context.Background(), command); err != nil || got != "" {
			t.Fatalf("%q %v: %q %v", command.Provider, command.Argv, got, err)
		}
	}
	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM custom\n"}}
	if got, err := custom.onDemandCLIPath(context.Background(), CommandSpec{Provider: "opencode", Argv: []string{"opencode"}}); err != nil || got != "" {
		t.Fatalf("custom images keep their own agent CLIs: %q %v", got, err)
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	if got, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: "opencode", Argv: []string{"opencode"}}); err != nil || got != "" {
		t.Fatalf("no runtime CLI directory: %q %v", got, err)
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("npm must not run: %s", calls)
	}
}

func TestOnDemandCLICatalogMarksBundledCLIs(t *testing.T) {
	catalog := onDemandCLICatalog()
	for engine, want := range map[string]bool{
		"codex": true, "codebuddy": true, "dsh": true,
		"claude-code": false, "opencode": false, "cursor": false, "agy": false, "kimi": false, "pi": false,
	} {
		if got := catalog[engine].Bundled; got != want {
			t.Fatalf("%s bundled = %v, want %v", engine, got, want)
		}
	}
	if pkg := catalog["dsh"].Package; pkg != "@deepseek-ai/dsh@"+DeepSeekHarnessVersion || !catalog["dsh"].RelaxEngines {
		t.Fatalf("dsh must pin its release and relax engines: %+v", catalog["dsh"])
	}
}

func TestSensitiveProcessEnvironmentKeyMatchesCredentialSuffixes(t *testing.T) {
	for _, key := range []string{
		// keys listed explicitly before the suffix rule
		"CODEBUDDY_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "AGENTFLOW_GIT_TOKEN", "GIT_CONFIG_KEY_0",
		// covered by the suffix rule
		"DEEPSEEK_API_KEY", "CURSOR_AUTH_TOKEN", "WEBHOOK_SECRET", "DB_PASSWORD",
		"deepseek_api_key", "Db_Password", "git_config_key_1",
	} {
		if !sensitiveProcessEnvironmentKey(key) {
			t.Errorf("%s must be redacted", key)
		}
	}
	for _, key := range []string{
		"", "PATH", "HOME", "DSH_HOME", "API_KEY_FILE", "MAX_TOKENS", "TOKEN", "SECRET",
		"GIT_CONFIG_VALUE_0", "GIT_CONFIG_COUNT", "NODE_OPTIONS",
	} {
		if sensitiveProcessEnvironmentKey(key) {
			t.Errorf("%s must not be redacted", key)
		}
	}
}
