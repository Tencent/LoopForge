package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCodexSandboxAuth = `{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":""}}`

func codexBindCommand() CommandSpec {
	return CommandSpec{Argv: []string{"codex", "exec", "prompt"}, Provider: "codex", CredentialBindings: []CredentialBinding{BindCodex}, PrivateHome: true}
}

func TestBindCommandInstallsCodexChatGPTLogin(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, environment: map[string]string{}}
	credentials := RunCredentials{Codex: ProviderCredential{APIKey: "sk-ignored", BaseURL: "https://gateway.example/v1", ChatGPTAuth: testCodexSandboxAuth}}
	bound, err := bindCommand(t.Context(), sb, workspace, codexBindCommand(), credentials)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(workspace, ".agentflow", "private-home", "codex", ".codex")
	if bound.environment["CODEX_HOME"] != home {
		t.Fatalf("CODEX_HOME = %q", bound.environment["CODEX_HOME"])
	}
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(data) != testCodexSandboxAuth {
		t.Fatalf("auth.json = %q, %v", data, err)
	}
	if info, _ := os.Stat(filepath.Join(home, "auth.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode = %v", info.Mode().Perm())
	}
	if bound.environment["CODEX_API_KEY"] != "" || strings.Contains(strings.Join(bound.Argv, " "), "agentflow_user") {
		t.Fatalf("API key path used alongside the ChatGPT login: env=%q argv=%v", bound.environment["CODEX_API_KEY"], bound.Argv)
	}

	// Switching back to an API key removes the earlier login.
	bound, err = bindCommand(t.Context(), sb, workspace, codexBindCommand(), RunCredentials{Codex: ProviderCredential{APIKey: "sk-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("stale ChatGPT login kept: %v", err)
	}
	if bound.environment["CODEX_API_KEY"] != "sk-test" {
		t.Fatal("API key not bound")
	}
}

func TestBindCommandReportsUnusableCodexChatGPTLogin(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, environment: map[string]string{}}
	_, err := bindCommand(t.Context(), sb, workspace, codexBindCommand(), RunCredentials{Codex: ProviderCredential{ChatGPTProblem: "Codex ChatGPT 登录已失效，请在个人配置中重新登录"}})
	if err == nil || !strings.Contains(err.Error(), "重新登录") {
		t.Fatalf("err = %v", err)
	}
	_, err = bindCommand(t.Context(), sb, workspace, codexBindCommand(), RunCredentials{})
	if err == nil || !strings.Contains(err.Error(), "Codex API Key 或 ChatGPT 登录") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallCodexChatGPTLoginRejectsSymlinkedHome(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	parent := filepath.Join(workspace, ".agentflow", "private-home", "codex")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, ".codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexChatGPTLogin(workspace, "codex", testCodexSandboxAuth); err == nil {
		t.Fatal("symlinked Codex home accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("login written outside the workspace: %v", entries)
	}
	if err := RemoveCodexChatGPTLogin(t.TempDir(), "codex"); err != nil {
		t.Fatalf("remove without a login: %v", err)
	}
}
