package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	atomCodeConfigPath = ".atomcode/config.toml"
	atomCodeMCPPath    = ".atomcode/mcp.json"
	atomCodeHooksPath  = ".atomcode/hooks.json"
)

// AtomCodeHeadlessAvailable checks the public flags that distinguish the CLI
// contract LoopForge uses from older AtomCode releases. The probe sends no prompt.
func AtomCodeHeadlessAvailable(path string) bool {
	return atomCodeProbe(path, false)
}

func atomCodeProbe(path string, requireApproval bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	args := []string{"--output-format", "jsonl", "--no-review"}
	if requireApproval {
		args = append(args, "--require-approval-hook")
	}
	return exec.CommandContext(ctx, path, append(args, "--version")...).Run() == nil
}

// AtomCode automatically loads project hooks, which can spawn shell commands
// outside the model tool approval path. Only LoopForge's managed hooks run.
func checkAtomCodeProjectHooks(workspace string) error {
	_, err := os.Lstat(filepath.Join(workspace, ".hooks.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查 AtomCode 项目 hooks: %w", err)
	}
	return fmt.Errorf("当前工作目录包含 AtomCode 项目 hooks（.hooks.json），LoopForge 执行器只加载平台管理的 hooks")
}

// AtomCode's cross-project resume can silently adopt the old working directory.
// Fence resumes to the same workspace so a handoff rebuilds the conversation
// from LoopForge history before the CLI can access the previous directory.
func bindAtomCodeConversationWorkspace(root, workspace string, command CommandSpec) error {
	path := filepath.Join(root, "atomcode-working-dir")
	current := filepath.Clean(workspace) + "\x00" + filepath.Clean(command.WorkingDir)
	if idx := slices.Index(command.Argv, "--resume"); idx >= 0 && idx+1 < len(command.Argv) {
		saved, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if string(saved) != current {
			return fmt.Errorf("No conversation found with session ID: %s", command.Argv[idx+1])
		}
		return nil
	}
	return os.WriteFile(path, []byte(current), 0o600)
}

func applyAtomCodeCredential(home AgentCLIHome, homeEnv, key, baseURL, protocol, model string, options AgentCLIOptions, argv []string) ([]string, map[string]string, error) {
	if model == "default" {
		return nil, nil, fmt.Errorf("AtomCode 需要指定模型")
	}
	providerType := "openai"
	if protocol == AgentCLIProtocolMessages {
		providerType = "claude"
	}
	denied := []string{"request_user_input"}
	managedChat := options.ReadOnly || slices.Contains(argv, "--require-approval-hook")
	if managedChat {
		// Delegation and scheduling use LoopForge's gateway in conversations.
		// AtomCode permission rules match exact tool names, except for "*".
		denied = append(denied, "task", "team", "schedule_create", "schedule_update", "schedule_delete", "schedule_wakeup")
	}
	if options.ReadOnly {
		denied = append(denied, "bash", "bash_start", "bash_write", "bash_kill", "pwsh", "cd", "write_file", "edit_file", "parallel_edit_files",
			"search_replace", "apply_patch", "open_file", "web_fetch", "web_search", "browser", "computer_use", "debug", "memory")
	}
	config, err := toml.Marshal(map[string]any{
		"default_provider": "loopforge",
		"providers": map[string]any{"loopforge": map[string]any{
			"type": providerType, "api_key": "$" + agentCLIKeyEnv, "base_url": baseURL, "model": model,
		}},
		"permissions": map[string]any{"deny": denied},
		"ui":          map[string]any{"ai_session_naming": false},
		"plugin":      map[string]any{"auto_install_default_skills": false, "auto_update_marketplaces": false},
		"tools":       map[string]any{"mcp": map[string]string{"tool_search": "off"}},
	})
	if err != nil {
		return nil, nil, err
	}
	if err := writeAgentCLIFile(home, atomCodeConfigPath, config); err != nil {
		return nil, nil, err
	}
	argv = append(append([]string{argv[0], "--config", homeEnv + "/" + atomCodeConfigPath}, argv[1:]...), "--provider=loopforge", "--model="+model)
	env := map[string]string{
		agentCLIKeyEnv:                key,
		"ATOMCODE_HOME":               homeEnv + "/.atomcode",
		"ATOMCODE_ATOMGIT":            "0",
		"ATOMCODE_REQUEST_USER_INPUT": "0",
		"ATOMGIT_TOKEN":               "",
	}
	if managedChat {
		env["ATOMCODE_SUBAGENT"] = "0"
	}
	return argv, env, nil
}

// atomCodeStream adapts AtomCode's versioned headless JSONL events.
type atomCodeStream struct {
	block   string
	body    strings.Builder
	failure string
	tools   map[string]bool
}

func (a *atomCodeStream) flush(s *ClaudeStream) []string {
	text, kind := a.body.String(), a.block
	a.body.Reset()
	a.block = ""
	switch kind {
	case "text":
		return s.Text(text)
	case "thinking":
		return s.Thinking(text)
	}
	return nil
}

func (a *atomCodeStream) translate(s *ClaudeStream, raw map[string]any) []string {
	if s.Finished() {
		return nil
	}
	event := stringValue(raw["type"])
	switch event {
	case "run.started":
		return s.Init(stringValue(raw["session_id"]), stringValue(raw["model"]))
	case "message.delta", "reasoning.delta":
		kind := "text"
		if event == "reasoning.delta" {
			kind = "thinking"
		}
		var out []string
		if a.block != kind {
			out = a.flush(s)
			a.block = kind
		}
		delta := stringValue(raw["text"])
		a.body.WriteString(delta)
		return append(out, s.Delta(kind, kind, delta)...)
	case "tool.started":
		var input map[string]any
		_ = json.Unmarshal([]byte(stringValue(raw["arguments"])), &input)
		if a.tools == nil {
			a.tools = make(map[string]bool)
		}
		id := stringValue(raw["id"])
		a.tools[id] = true
		return append(a.flush(s), s.ToolUse(id, stringValue(raw["name"]), input)...)
	case "tool.completed":
		failed, _ := raw["is_error"].(bool)
		id, content := stringValue(raw["id"]), stringValue(raw["content"])
		out := a.flush(s)
		if failed && !a.tools[id] {
			out = append(out, s.ToolUse(id, "AtomCode", nil)...)
		}
		return append(out, s.ToolResult(id, failed, content)...)
	case "error":
		a.failure = stringValue(raw["message"])
		if status := int64Value(raw["http_status"]); status != 0 {
			a.failure += fmt.Sprintf(" (HTTP %d)", status)
		}
	case "turn.completed", "run.failed":
		out := a.flush(s)
		failed := event == "run.failed" || int64Value(raw["exit_code"]) != 0
		if event == "turn.completed" {
			s.SetUsage(int64Value(raw["prompt_tokens"]), int64Value(raw["completion_tokens"]), int64Value(raw["cached_tokens"]), 0, 0)
		}
		message := ""
		if failed {
			message = a.failure
			if message == "" {
				message = stringValue(raw["message"])
			}
			if message == "" {
				message = "AtomCode 本轮执行失败"
			}
		}
		return append(out, s.Result(message, failed)...)
	}
	return nil
}
