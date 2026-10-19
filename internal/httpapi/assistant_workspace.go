package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/application"
	"agentflow/internal/engine"
	"agentflow/internal/mcpserver"
	"agentflow/internal/runtime"
)

type mainBotWorkspaceKey struct{}

func mainBotWorkspaceRun(ctx context.Context) string {
	id, _ := ctx.Value(mainBotWorkspaceKey{}).(string)
	return id
}
func mainBotWorkspaceTools(s *AgentToolSession) []agentToolDescriptor {
	if s.Mode != AgentToolAssistant || s.RunID == "" || s.ExecMode.Consultation {
		return nil
	}
	descriptors := []struct{ name, description, schema string }{
		{"tree", "列出当前任务真实工作区的文件。路径相对任务工作区，不是控制平面目录。", `{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`},
		{"read", "读取当前任务工作区文件，返回 content 与 sha256；修改前先读取。", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`},
		{"write", "直接保存当前任务工作区文件。expectedSha256 必须为读取时的 sha256；新文件传 missing。若文件已变更，重新读取并对齐，不能盲目覆盖。SOP 正在执行时需先暂停；自由协作允许并行写入。", `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"expectedSha256":{"type":"string"}},"required":["path","content","expectedSha256"],"additionalProperties":false}`},
		{"command", "在当前任务工作区运行 shell 命令并返回 output、exitCode，用于小改动与验证。最长 120 秒；SOP 正在执行时需先暂停。", `{"type":"object","properties":{"command":{"type":"string"},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":120}},"required":["command"],"additionalProperties":false}`},
	}
	out := make([]agentToolDescriptor, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, agentToolDescriptor{Name: "workbench_workspace_" + d.name, Description: d.description, InputSchema: json.RawMessage(d.schema), Kind: "workspace"})
	}
	return out
}
func (h *agentToolGateway) callMainBotWorkspace(ctx context.Context, s *AgentToolSession, name string, args map[string]any) (result string, resultErr error) {
	if len(mainBotWorkspaceTools(s)) == 0 {
		return "", errAgentToolForbidden
	}
	run, ok := h.repo.RunState(s.RunID)
	if !ok || run.ExecutionContext.ProjectID != s.ProjectID || s.User.ID != s.UserID {
		return "", errAgentToolForbidden
	}
	project, exists := h.repo.Project(s.ProjectID)
	allowed := h.repo.IsPlatformAdmin(s.User.LoginName) || exists && (project.OwnerID == s.UserID || project.OwnerID == 0 && len(project.Members) == 0)
	for _, member := range project.Members {
		if member.UserID == s.UserID && !strings.EqualFold(member.Role, "viewer") {
			allowed = true
		}
	}
	if !allowed {
		return "", errAgentToolForbidden
	}
	wasReclaimed := !run.WorkspaceCleanedAt.IsZero()
	var restoredArtifacts []string
	// The first successful operation must tell Main Bot it restored a snapshot;
	// a readable file after lazy restore does not imply the old workspace survived.
	defer func() {
		if (!wasReclaimed && len(restoredArtifacts) == 0 && name != "workbench_workspace_command") || resultErr != nil {
			return
		}
		var payload map[string]any
		if json.Unmarshal([]byte(result), &payload) == nil && payload != nil {
			if wasReclaimed {
				payload["workspaceRestored"] = true
				payload["workspaceNotice"] = "原工作区已回收；本次已恢复保存的文件快照。命令执行前会检查并准备当前运行环境，空闲 1 小时后再次回收。"
			}
			if len(restoredArtifacts) > 0 {
				payload["restoredArtifacts"] = restoredArtifacts
				payload["workspaceNotice"] = "当前目录已补回缺失的归档产物，已有文件未覆盖；这是产物重建，不是完整历史工作区快照恢复。"
			}

			if name == "workbench_workspace_command" {
				payload["environmentReady"] = true
				if h.engine != nil {
					if status, err := h.engine.WorkspacePreparationStatus(ctx, s.RunID); err == nil {
						payload["workspacePreparation"] = runtime.PublicWorkspacePreparationStatus(status)
						if len(status.Steps) == 0 {
							payload["workspaceNotice"] = "本次命令已在任务配置的基础运行环境中执行；未配置额外工作区准备脚本，原 SOP 前置脚本不会自动重放。"
						} else {
							payload["workspaceNotice"] = "本次命令执行前已完成配置的工作区准备；同一环境中的后续文件改动不会触发这些重建脚本。SOP 历史节点状态保持不变。"
						}
					}
				}
			}
			raw, err := json.Marshal(payload)
			if err == nil {
				result = string(raw)
			}
		}
	}()
	if h.engine != nil {
		token := s.WorkspaceToken
		if token == "" {
			var err error
			token, err = h.engine.BeginConversationWorkspaceUse(s.RunID)
			if err != nil {
				return "", err
			}
			defer func() {
				saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer stop()
				_ = h.engine.EndConversationWorkspaceUse(saveCtx, s.RunID, token)
			}()
		}
		var prepareErr error
		restoredArtifacts, prepareErr = h.engine.PrepareConversationWorkspace(ctx, s.RunID, token)
		if prepareErr != nil {
			return "", prepareErr
		}
		ctx = engine.WithConversationWorkspaceToken(ctx, token, s.RunID)
	}
	ctx = context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(s.User))
	ctx = context.WithValue(ctx, mainBotWorkspaceKey{}, s.RunID)
	switch name {
	case "workbench_workspace_tree", "workbench_workspace_read":
		path, _ := args["path"].(string)
		endpoint := "tree"
		if name == "workbench_workspace_read" {
			endpoint = "file"
			if strings.TrimSpace(path) == "" {
				return "", fmt.Errorf("path is required")
			}
		}
		return h.invokeMainBotWorkspace(ctx, s, "GET", endpoint, nil, map[string]string{"path": path})
	case "workbench_workspace_write":
		path, pok := args["path"].(string)
		content, cok := args["content"].(string)
		sha, sok := args["expectedSha256"].(string)
		if !pok || !cok || !sok || strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("path, content and expectedSha256 are required")
		}
		return h.invokeMainBotWorkspace(ctx, s, "PUT", "file", map[string]any{"path": path, "content": content, "expectedSha256": sha}, nil)
	case "workbench_workspace_command":
		return h.mainBotWorkspaceCommand(ctx, s, args)
	}
	return "", errAgentToolUnknown
}
func (h *agentToolGateway) invokeMainBotWorkspace(ctx context.Context, s *AgentToolSession, method, endpoint string, body any, query map[string]string) (string, error) {
	result, err := (mcpserver.LocalInvoker{Handler: h.api}).Invoke(ctx, application.Operation{Method: method, Path: "/api/runs/{id}/workspace/" + endpoint}, mcpserver.ToolInput{Path: map[string]string{"id": s.RunID}, Query: query, Body: body})
	if err != nil {
		return "", err
	}
	if result.Status < 200 || result.Status >= 300 {
		return "", fmt.Errorf("%s", string(result.Body))
	}
	return redactAgentToolResult(string(result.Body), s.User), nil
}
func (h *agentToolGateway) mainBotWorkspaceCommand(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok || strings.TrimSpace(command) == "" || len(command) > 16384 {
		return "", fmt.Errorf("command is required and must be at most 16384 bytes")
	}
	seconds := 30
	if value, ok := args["timeoutSeconds"]; ok {
		n, err := strconv.Atoi(fmt.Sprint(value))
		if err != nil || n < 1 || n > 120 {
			return "", fmt.Errorf("timeoutSeconds must be 1..120")
		}
		seconds = n
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	terminal := func(ctx context.Context, operation, id, data string) (runtime.TerminalResult, error) {
		raw, err := h.invokeMainBotWorkspace(ctx, s, "POST", "terminal", map[string]any{"operation": operation, "id": id, "data": data}, nil)
		var result runtime.TerminalResult
		if err == nil {
			err = json.Unmarshal([]byte(raw), &result)
		}
		return result, err
	}
	opened, err := terminal(ctx, "open", "", "")
	if err != nil {
		return "", err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_, _ = terminal(cleanup, "close", opened.ID, "")
	}()
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	marker := "__main_bot_exit_" + hex.EncodeToString(nonce) + ":"
	// Treat the requested command as a single shell argument, preserving quotes,
	// substitutions and newlines without letting it alter our status wrapper.
	quoted := "'" + strings.ReplaceAll(command, "'", "'\"'\"'") + "'"
	input := "stty -echo\n/bin/sh -c " + quoted + "\nprintf '\\n" + marker + "%s\\n' \"$?\"\nexit\n"
	if _, err = terminal(ctx, "input", opened.ID, input); err != nil {
		return "", err
	}
	var output strings.Builder
	for {
		result, err := terminal(ctx, "read", opened.ID, "")
		if err != nil {
			return "", err
		}
		if output.Len()+len(result.Data) > 128<<10 {
			return "", fmt.Errorf("command output exceeds 128 KB")
		}
		output.Write(result.Data)
		if result.Exited {
			text := strings.ReplaceAll(output.String(), "\r\n", "\n")
			at := strings.LastIndex(text, "\n"+marker)
			if at < 0 {
				return "", fmt.Errorf("command ended without an exit status: %s", text)
			}
			line := strings.SplitN(text[at+1+len(marker):], "\n", 2)[0]
			code, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				return "", err
			}
			raw, _ := json.Marshal(map[string]any{"output": text[:at], "exitCode": code})
			return string(raw), nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("command stopped: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
