package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

const (
	nativeDisallowedTools       = "Bash,Write,Edit,MultiEdit,NotebookEdit,WebFetch,WebSearch,Task,KillShell"
	nativeAssistantMaxTurns     = "30"
	nativeTerminalMaxRetries    = 2
	agentToolSessionMCPServerID = "agentflow_session"
)

type agentToolFiles struct {
	Dir       string
	MCPConfig string
	Settings  string
}

func (f agentToolFiles) Cleanup() {
	if f.Dir == "" {
		return
	}
	_ = os.Remove(f.MCPConfig)
	_ = os.Remove(f.Settings)
	_ = os.Remove(f.Dir)
}

func agentToolMCPURL(token string) string {
	return config.Current().AgentToolGateway + "/internal/agent-tools/" + strings.TrimSpace(token) + "/mcp"
}

func writeAgentToolFiles(parentDir, token string) (agentToolFiles, error) {
	parentDir = strings.TrimSpace(parentDir)
	if parentDir == "" {
		return agentToolFiles{}, fmt.Errorf("empty agent tool config directory")
	}
	dir, err := os.MkdirTemp(parentDir, "mcp-")
	if err != nil {
		if err := os.MkdirAll(parentDir, 0o700); err != nil {
			return agentToolFiles{}, err
		}
		dir, err = os.MkdirTemp(parentDir, "mcp-")
		if err != nil {
			return agentToolFiles{}, err
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return agentToolFiles{}, err
	}
	files := agentToolFiles{
		Dir:       dir,
		MCPConfig: filepath.Join(dir, "mcp.json"),
		Settings:  filepath.Join(dir, "settings.json"),
	}
	config, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			agentToolSessionMCPServerID: map[string]any{
				"type": "http",
				"url":  agentToolMCPURL(token),
			},
		},
	})
	if err != nil {
		files.Cleanup()
		return agentToolFiles{}, err
	}
	if err := os.WriteFile(files.MCPConfig, config, 0o600); err != nil {
		files.Cleanup()
		return agentToolFiles{}, err
	}
	settings, err := json.Marshal(nativeToolSettings())
	if err != nil {
		files.Cleanup()
		return agentToolFiles{}, err
	}
	if err := os.WriteFile(files.Settings, settings, 0o600); err != nil {
		files.Cleanup()
		return agentToolFiles{}, err
	}
	return files, nil
}

func nativeToolSettings() map[string]any {
	return map[string]any{
		"permissions": map[string]any{
			"deny": strings.Split(nativeDisallowedTools, ","),
		},
	}
}

func applyNativeToolSafety(argv []string, mode AgentToolMode) []string {
	_ = mode
	// --tools is a built-in allowlist. CodeBuddy also applies it to MCP tools,
	// so listing Read/Grep/Glob or mcp__* globs hides submit_* and TAPD/工蜂.
	argv = dropNativeArg(argv, "--tools")
	if !argvHasFlag(argv, "--disallowedTools") {
		argv = append(argv, "--disallowedTools", nativeDisallowedTools)
	}
	if !argvHasFlag(argv, "--strict-mcp-config") {
		argv = append(argv, "--strict-mcp-config")
	}
	return argv
}

func dropNativeArg(argv []string, flag string) []string {
	out := make([]string, 0, len(argv))
	skip := false
	for _, arg := range argv {
		if skip {
			skip = false
			continue
		}
		if arg == flag {
			skip = true
			continue
		}
		if strings.HasPrefix(arg, flag+"=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func replaceArgValue(argv []string, flag, value string) []string {
	out := make([]string, 0, len(argv)+2)
	replaced := false
	skip := false
	for i := 0; i < len(argv); i++ {
		if skip {
			skip = false
			continue
		}
		if argv[i] == flag {
			out = append(out, flag, value)
			skip = true
			replaced = true
			continue
		}
		out = append(out, argv[i])
	}
	if !replaced {
		out = append(out, flag, value)
	}
	return out
}

func argvHasFlag(argv []string, flag string) bool {
	for _, arg := range argv {
		if arg == flag {
			return true
		}
	}
	return false
}

func argvFlagValue(argv []string, flag string) (string, bool) {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

func nativeCommandContainsSecret(command runtime.CommandSpec, files agentToolFiles, token string) bool {
	blob, _ := os.ReadFile(files.MCPConfig)
	text := string(blob)
	if strings.Contains(text, "Authorization") || strings.Contains(strings.ToLower(text), "bearer ") {
		return true
	}
	joined := strings.Join(command.Argv, " ")
	if token != "" && strings.Contains(joined, token) {
		return true
	}
	return false
}

func startAgentToolSession(ctx context.Context, user domain.User, projectID string, mode AgentToolMode, exec assistantExecMode, issueID string, servers []domain.MCPServerDefinition, cancel context.CancelFunc, parent string) (*AgentToolSession, agentToolFiles, error) {
	session := &AgentToolSession{
		UserID:      user.ID,
		ProjectID:   projectID,
		Mode:        mode,
		IssueID:     issueID,
		ExpiresAt:   time.Now().Add(agentToolSessionTTL(ctx)),
		MCPServers:  servers,
		User:        user,
		ExecMode:    exec,
		Final:       make(chan AgentTerminalResult, 1),
		CancelModel: cancel,
	}
	if err := agentToolSessions.create(session); err != nil {
		return nil, agentToolFiles{}, err
	}
	files, err := writeAgentToolFiles(parent, session.Token)
	if err != nil {
		session.Close()
		return nil, agentToolFiles{}, err
	}
	return session, files, nil
}

func redactAgentToolPath(path string) string {
	const prefix = "/internal/agent-tools/"
	if !strings.HasPrefix(path, prefix) {
		return path
	}
	rest := path[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return prefix + "[redacted]" + rest[i:]
	}
	return prefix + "[redacted]"
}

func nativeShouldRetryTerminal(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if err == nil {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "api key"), strings.Contains(msg, "凭据"), strings.Contains(msg, "未找到"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "401"):
		return false
	default:
		return true
	}
}
