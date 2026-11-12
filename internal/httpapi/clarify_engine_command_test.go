package httpapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/runtime"
)

// Task 2: 按引擎构造澄清命令。这些测试钉住的是「每个引擎用它自己那套命令行与配置」：
// 把 CodeBuddy 的 argv 原样交给 codex / Claude Code / agent CLI，进程会直接起不来。

const clarifyTestMCPServerURL = "http://127.0.0.1:43123/internal/agent-tools/test-token/mcp"

// writeClarifyMCPFixture 写一份 CodeBuddy 风格的会话 MCP 配置：含 CodeBuddy 专属的
// defer_loading 扩展，用来验证其它引擎的写法确实把它剥掉了。
func writeClarifyMCPFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	config := map[string]any{"mcpServers": map[string]any{
		agentToolSessionMCPServerID: map[string]any{
			"url":   clarifyTestMCPServerURL,
			"tools": map[string]any{submitClarificationTool: map[string]bool{"defer_loading": false}},
		},
	}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func argvContains(argv []string, want ...string) bool {
	for _, value := range want {
		found := false
		for _, arg := range argv {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestPrepareClarifyInvocationCodeBuddyKeepsArgv(t *testing.T) {
	config := writeClarifyMCPFixture(t)
	settings := filepath.Join(filepath.Dir(config), "settings.json")

	invocation, err := prepareClarifyInvocation(clarifyEngineOf("default"), clarifyInvocationOpts{
		Bin: "codebuddy", Root: filepath.Dir(config), Config: config, Settings: settings,
		Prompt: "p", Model: "default", SessionID: "af1", RunID: "clarify-test",
	})
	if err != nil {
		t.Fatalf("codebuddy invocation: %v", err)
	}
	argv := invocation.Command.Argv
	if !argvContains(argv, "--output-format", "stream-json", "--session-id", "af1", "--strict-mcp-config") {
		t.Fatalf("codebuddy argv lost its stream-json session flags: %v", argv)
	}
	denied, ok := argvFlagValue(argv, "--disallowedTools")
	if !ok || !strings.Contains(denied, "Bash") || !strings.Contains(denied, "Write") || !strings.Contains(denied, "Edit") {
		t.Fatalf("codebuddy argv must deny shell and edit tools: %q", denied)
	}
	if invocation.Translate != nil {
		t.Fatal("CodeBuddy already speaks stream-json; it must not be translated")
	}
	if invocation.Finish != nil {
		t.Fatal("CodeBuddy keeps its own transcript, so there is nothing to persist after the turn")
	}
}

func TestPrepareClarifyInvocationClaudeUsesClaudeDialect(t *testing.T) {
	root := t.TempDir()
	config := writeClarifyMCPFixture(t)

	invocation, err := prepareClarifyInvocation(clarifyEngineOf("claude-code:claude-opus-5"), clarifyInvocationOpts{
		Bin: "claude", Root: root, Config: config, Prompt: "p", Model: "claude-opus-5",
		SessionID: "af1", SystemPrompt: "你是研发工作项分析师", RunID: "clarify-test",
	})
	if err != nil {
		t.Fatalf("claude invocation: %v", err)
	}
	argv := invocation.Command.Argv
	if !argvContains(argv, "--verbose", "--mcp-config") {
		t.Fatalf("claude argv missing its own dialect: %v", argv)
	}
	resume, ok := argvFlagValue(argv, "--session-id")
	if !ok || resume != assistantClaudeSessionID("af1") {
		t.Fatalf("claude session id = %q (present=%v), want %q", resume, ok, assistantClaudeSessionID("af1"))
	}
	mcpFile := filepath.Join(root, "claude-mcp.json")
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("claude mcp config not written: %v", err)
	}
	if strings.Contains(string(data), "defer_loading") {
		t.Fatalf("claude rejects CodeBuddy's defer_loading extension: %s", data)
	}
	contained := false
	for _, binding := range invocation.Command.CredentialBindings {
		contained = contained || binding == runtime.BindClaudeCode
	}
	if !contained {
		t.Fatalf("claude argv must bind the Claude Code credential: %v", invocation.Command.CredentialBindings)
	}
}

func TestPrepareClarifyInvocationCodexUsesAppServer(t *testing.T) {
	root := t.TempDir()
	config := writeClarifyMCPFixture(t)

	invocation, err := prepareClarifyInvocation(clarifyEngineOf("codex:gpt-5.5"), clarifyInvocationOpts{
		Bin: "codex", Root: root, Config: config, Prompt: "p", Model: "gpt-5.5",
		SessionID: "af1", SystemPrompt: "你是研发工作项分析师", RunID: "clarify-test",
	})
	if err != nil {
		t.Fatalf("codex invocation: %v", err)
	}
	if invocation.Command.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", invocation.Command.Provider)
	}
	if !argvContains(invocation.Command.Argv, "app-server") {
		t.Fatalf("codex must run its app-server: %v", invocation.Command.Argv)
	}
	if invocation.Command.Stdin == nil {
		t.Fatal("codex app-server is driven over stdio")
	}
	if invocation.Translate == nil {
		t.Fatal("codex speaks JSON-RPC, so its output must be translated before the clarify parser")
	}
	if invocation.Finish == nil {
		t.Fatal("codex threads must be persisted after a usable turn")
	}
	bound := false
	for _, binding := range invocation.Command.CredentialBindings {
		bound = bound || binding == runtime.BindCodex
	}
	if !bound {
		t.Fatalf("codex argv must bind the Codex credential: %v", invocation.Command.CredentialBindings)
	}
}

func TestPrepareClarifyInvocationAgentCLIWritesReadOnlyMCP(t *testing.T) {
	root := t.TempDir()
	config := writeClarifyMCPFixture(t)

	invocation, err := prepareClarifyInvocation(clarifyEngineOf("opencode:glm-4"), clarifyInvocationOpts{
		Bin: "opencode", Root: root, Config: config, Prompt: "p", Model: "glm-4",
		SessionID: "af1", SystemPrompt: "你是研发工作项分析师", RunID: "clarify-test",
		User: runtime.ProviderCredential{APIKey: "sk-test", BaseURL: "https://gateway.example.com/v1"},
	})
	if err != nil {
		t.Fatalf("opencode invocation: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "home", ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("opencode MCP config not written under the private home: %v", err)
	}
	if !strings.Contains(string(data), clarifyTestMCPServerURL) {
		t.Fatalf("opencode config lost the clarification tool gateway: %s", data)
	}
	if strings.Contains(string(data), "defer_loading") {
		t.Fatalf("opencode rejects CodeBuddy's per-tool extension: %s", data)
	}
	bound := false
	for _, binding := range invocation.Command.CredentialBindings {
		bound = bound || binding == runtime.BindAgentCLI
	}
	if !bound {
		t.Fatalf("agent CLI argv must bind the CLI credential: %v", invocation.Command.CredentialBindings)
	}
	if invocation.Translate == nil {
		t.Fatal("agent CLIs need the stream translator before clarifyStreamEvents can read them")
	}
}

func TestPrepareClarifyInvocationRejectsUnknownEngine(t *testing.T) {
	_, err := prepareClarifyInvocation(clarifyEngine{ID: "foobar"}, clarifyInvocationOpts{
		Root: t.TempDir(), Config: writeClarifyMCPFixture(t),
	})
	if !errors.Is(err, errClarifyEngineUnknown) {
		t.Fatalf("err = %v, want errClarifyEngineUnknown", err)
	}
}

func TestClarifySubmitOnlySupport(t *testing.T) {
	for _, tc := range []struct {
		modelID string
		support bool
	}{
		{"default", true},                   // CodeBuddy: --disallowedTools 可以逐工具收窄
		{"glm-5.3-flash-ioa", true},         // CodeBuddy
		{"claude-code:claude-opus-5", true}, // Claude Code: 同名 --disallowedTools
		{"codex:gpt-5.5", false},            // Codex: 护栏是 PreToolUse 脚本
		{"opencode:glm-4", false},           // agent CLI: 只有整体只读档
		{"cursor:gpt-5", false},             // agent CLI: 同上
	} {
		if got := clarifySubmitOnlySupported(clarifyEngineOf(tc.modelID)); got != tc.support {
			t.Errorf("clarifySubmitOnlySupported(%q) = %v, want %v", tc.modelID, got, tc.support)
		}
	}
}
