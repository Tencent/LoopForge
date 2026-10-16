package httpapi

// Main Bot uses the same user-scoped credentials as SOP roles. Its native
// tools are deliberately restricted: all platform operations still pass
// through the authenticated tool gateway and its approval policy.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func assistantExecutor(id string) string {
	if id == "" {
		return "codebuddy"
	}
	return strings.TrimSpace(id)
}

func validateAssistantProvider(req *assistantRequest, user domain.User) error {
	req.Executor = assistantExecutor(req.Executor)
	var key, label string
	switch req.Executor {
	case "codebuddy":
		key, label = user.CodeBuddyAPIKey, "CodeBuddy"
	case "codex":
		key, label = user.CodexAPIKey, "Codex"
	case "claude":
		key, label = user.ClaudeCodeAPIKey, "Claude Code"
	default:
		return fmt.Errorf("不支持的执行器")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("请先在个人设置中配置 %s API Key", label)
	}
	if req.Executor == "codebuddy" {
		req.Model = assistantAllowedModel(req.Model, loadAssistantModels(context.Background(), user.CodeBuddyInternetEnvironment))
	} else {
		req.Model = strings.TrimSpace(req.Model)
		if len(req.Model) > 256 || strings.ContainsAny(req.Model, "\r\n\x00") {
			return fmt.Errorf("模型 ID 无效")
		}
	}
	return nil
}

func assistantProviderCredentials(user domain.User) runtime.RunCredentials {
	return runtime.RunCredentials{
		CodeBuddy:  runtime.CodeBuddyCredential{APIKey: user.CodeBuddyAPIKey, InternetEnvironment: user.CodeBuddyInternetEnvironment},
		Codex:      runtime.ProviderCredential{APIKey: user.CodexAPIKey, BaseURL: user.CodexBaseURL},
		ClaudeCode: runtime.ProviderCredential{APIKey: user.ClaudeCodeAPIKey, BaseURL: user.ClaudeCodeBaseURL},
	}
}

// A second boundary for Codex tools, independent of model instructions and
// feature flags. A built-in tool can never bypass the platform gateway.
const assistantCodexGuard = `import json,sys
event=json.load(sys.stdin)
name=event.get("tool_name", "")
if not name.startswith("mcp__agentflow_session__"):
    print(json.dumps({"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Use the LoopForge session MCP tools; native tools are disabled in Main Bot."}}))
    sys.exit(2)
`

func prepareAlternateAssistantCommand(req assistantCLIRequest, dir string) (runtime.CommandSpec, error) {
	binary, kind, binding := "claude", "claude-code", runtime.BindClaudeCode
	if req.Executor == "codex" {
		binary, kind, binding = "codex", "codex", runtime.BindCodex
	}
	bin, err := exec.LookPath(binary)
	if err != nil {
		return runtime.CommandSpec{}, fmt.Errorf("%s CLI 未安装", binary)
	}
	sys := strings.TrimSpace(req.SystemPrompt)
	if sys == "" {
		sys = assistantSystemPromptText()
	}
	var args []string
	if req.Executor == "codex" {
		var config struct {
			Servers map[string]struct {
				URL string `json:"url"`
			} `json:"mcpServers"`
		}
		raw, err := os.ReadFile(req.MCPConfig)
		if err != nil {
			return runtime.CommandSpec{}, err
		}
		if err = json.Unmarshal(raw, &config); err != nil {
			return runtime.CommandSpec{}, err
		}
		server, ok := config.Servers[agentToolSessionMCPServerID]
		if !ok || server.URL == "" {
			return runtime.CommandSpec{}, fmt.Errorf("missing session MCP server")
		}
		guard := filepath.Join(dir, "guard.py")
		if err = os.WriteFile(guard, []byte(assistantCodexGuard), 0600); err != nil {
			return runtime.CommandSpec{}, err
		}
		// The bearer URL stays in a mode-0600 file, never in process arguments.
		text := "[mcp_servers.agentflow_session]\nurl=" + strconv.Quote(server.URL) + "\nrequired=true\ndefault_tools_approval_mode=\"approve\"\n"
		if err = os.MkdirAll(filepath.Join(dir, ".codex"), 0700); err != nil {
			return runtime.CommandSpec{}, err
		}
		if err = os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte(text), 0600); err != nil {
			return runtime.CommandSpec{}, err
		}
		args = []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-rules", "--sandbox", "read-only", "-c", "approval_policy=\"never\"", "-c", "developer_instructions=" + strconv.Quote(sys), "-c", "web_search=\"disabled\""}
		for _, feature := range []string{"shell_tool", "unified_exec", "apply_patch_freeform", "multi_agent", "multi_agent_v2", "js_repl", "code_mode", "apps", "plugins", "memories", "image_generation", "browser_use", "computer_use", "tool_suggest", "skill_search", "view_image", "goals", "collab", "sleep_tool", "worktrees"} {
			args = append(args, "-c", "features."+feature+"=false")
		}
		args = append(args, "-c", "features.hooks=true", "--dangerously-bypass-hook-trust", "-c", "hooks.PreToolUse=[{hooks=[{type=\"command\",command="+strconv.Quote("python3 '"+strings.ReplaceAll(guard, "'", "'\\''")+"'")+",timeout=5}]}]")
	} else {
		args = []string{"-p", "--bare", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--max-turns", nativeAssistantMaxTurns, "--system-prompt", sys, "--tools", "", "--mcp-config", req.MCPConfig, "--strict-mcp-config", "--setting-sources", "", "--permission-mode", "dontAsk", "--allowedTools", "mcp__agentflow_session__*", "--no-session-persistence"}
	}
	if req.Model != "" && req.Model != "default" {
		args = append(args, "--model", req.Model)
	}
	args = append([]string{bin}, append(args, req.Prompt)...)
	return runtime.CommandSpec{Argv: args, Provider: kind, CredentialBindings: []runtime.CredentialBinding{binding}, PrivateHome: true}, nil
}

// Codex JSONL uses item.completed rather than Anthropic's result envelope.
func assistantCodexEvent(line string) (string, []assistantStreamEvent) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Text      string          `json:"text"`
			Server    string          `json:"server"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(line), &event) != nil {
		return "", nil
	}
	if event.Type == "item.completed" && event.Item.Type == "agent_message" {
		return event.Item.Text, []assistantStreamEvent{{Kind: "answer", Text: event.Item.Text, Full: true}}
	}
	if event.Type == "item.started" && event.Item.Type == "mcp_tool_call" {
		name := "mcp__" + event.Item.Server + "__" + event.Item.Tool
		if ev, ok := assistantToolStreamEvent(event.Item.ID, name, event.Item.Arguments); ok {
			return "", []assistantStreamEvent{ev}
		}
	}
	return "", nil
}

func runAlternateAssistantCLI(ctx context.Context, req assistantCLIRequest, user domain.User, onEvent func(assistantStreamEvent)) (string, string, error) {
	dir, err := os.MkdirTemp("", "loopforge-assistant-"+req.Executor+"-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	command, err := prepareAlternateAssistantCommand(req, dir)
	if err != nil {
		return "", "", err
	}
	command = command.WithPrivateHomeDir(dir)
	if err = os.MkdirAll(filepath.Join(dir, "tmp"), 0700); err != nil {
		return "", "", err
	}
	var final string
	var resultError bool
	var filter assistantStreamFilter
	var stderr []string
	sink := func(event domain.TelemetryEvent) {
		if event.Name == "process.stderr" {
			if len(stderr) < 20 {
				stderr = append(stderr, event.Message)
			}
			return
		}
		if event.Name != "process.stdout" {
			return
		}
		line := strings.TrimSpace(event.Message)
		events := assistantStreamEvents(line)
		if req.Executor == "codex" {
			var text string
			text, events = assistantCodexEvent(line)
			if text != "" {
				final = text
			}
		} else {
			var result struct {
				Type    string `json:"type"`
				Result  string `json:"result"`
				IsError bool   `json:"is_error"`
			}
			if json.Unmarshal([]byte(line), &result) == nil && result.Type == "result" {
				final, resultError = result.Result, result.IsError
			}
		}
		if onEvent != nil {
			for _, event := range events {
				if filter.accept(event) {
					onEvent(event)
				}
			}
		}
	}
	_, err = runtime.ExecuteLocalCommand(ctx, dir, command, assistantProviderCredentials(user), sink)
	if err != nil {
		return "", req.SessionID, fmt.Errorf("%s CLI execution failed: %w", req.Executor, err)
	}
	if strings.TrimSpace(final) == "" && req.Executor == "codex" {
		// A successful submit_assistant_reply may end without agent_message;
		// the authenticated tool session holds the actual reply in that case.
		return "", req.SessionID, nil
	}
	out, err := decodeAssistantCLIResult(final, resultError, nil, nil)
	return out, req.SessionID, err
}
