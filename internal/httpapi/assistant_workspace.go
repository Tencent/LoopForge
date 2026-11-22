package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	pathpkg "path"
	"strings"

	"agentflow/internal/application"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/mcpserver"
)

type mainBotWorkspaceKey struct{}

func mainBotWorkspaceRun(ctx context.Context) string {
	id, _ := ctx.Value(mainBotWorkspaceKey{}).(string)
	return id
}

// workspaceRun is the run whose workspace the workspace tools operate on: the
// task of a task conversation, else the workspace the conversation is bound to.
func (s *AgentToolSession) workspaceRun() string {
	if s.RunID != "" {
		return s.RunID
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	return s.workspaceRunID
}

// conversationWorkspace returns the workspace an ordinary conversation is
// bound to; task conversations never bind one.
func (s *AgentToolSession) conversationWorkspace() string {
	if s.RunID != "" {
		return ""
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	return s.workspaceRunID
}

// Project participants share one workspace binding. A personal conversation
// uses its owner's binding.
func (s *AgentToolSession) canOpenConversationWorkspace() bool {
	return s.RunID == "" && s.ChatSessionID != "" &&
		(s.ProjectID != "" || s.UserID > 0 && s.ChatOwnerID == s.UserID)
}

func (h *agentToolGateway) mayUseWorkspace(s *AgentToolSession, run domain.Run) bool {
	if run.ExecutionContext.ProjectID != s.ProjectID || s.User.ID != s.UserID {
		return false
	}
	if !run.IsPersonalWorkspace() {
		return true
	}
	if !s.canOpenConversationWorkspace() {
		return false
	}
	conv, ok := h.repo.AssistantSession(s.UserID, s.ChatSessionID)
	return ok && conv.WorkspaceRunID == run.ID && conversationMayBind(conv, run, s.ProjectID)
}

func mainBotWorkspaceTools(s *AgentToolSession) []agentToolDescriptor {
	if s.Mode != AgentToolAssistant || (s.RunID == "" && !s.canOpenConversationWorkspace()) {
		return nil
	}
	// Files, patches and commands use the CLI's native tools inside the
	// workspace; git, pushes and PRs use the agent's own git with the
	// requester's credentials, and PRs are registered when the turn ends. Only
	// the mode switch stays; files reach the user through
	// workbench_publish_artifact's path.
	return []agentToolDescriptor{workspaceOpenDescriptor()}
}

func (h *agentToolGateway) callMainBotWorkspace(ctx context.Context, s *AgentToolSession, name string, args map[string]any) (result string, resultErr error) {
	if len(mainBotWorkspaceTools(s)) == 0 {
		return "", errAgentToolForbidden
	}
	if name == "workbench_platform_mode" {
		s.requestMode(domain.AssistantModePlatform)
		raw, _ := json.Marshal(map[string]any{"mode": "platform", "handoff": true, "notice": "工作区保留，准备切回平台模式。" + assistantHandoffNotice})
		return string(raw), nil
	}
	if name == "workbench_workspace_open" {
		if s.RuntimeMode == domain.AssistantModeWorkspace {
			return "", fmt.Errorf("切换工作区前先调用 workbench_platform_mode，等待原生进程退出后再打开工作区")
		}
		// Only Main Bot picks the workspace. A subagent calls through the same
		// gateway, so while any dispatch runs the binding stays as it is.
		if s.dispatchRunning() {
			return "", fmt.Errorf("子 Agent 运行期间不能打开或切换工作区：子 Agent 只在当前工作区里工作。需要别的工作区时在汇报里说明，由 Main Bot 在子 Agent 结束后打开")
		}
		return h.openConversationWorkspace(ctx, s, args)
	}
	releaseTurn := s.workspaceTurn.lockOperation()
	defer releaseTurn()
	runID := s.workspaceRun()
	if expected := mainBotWorkspaceRun(ctx); expected != "" && expected != runID {
		return "", fmt.Errorf("工作区已切换，请重新调用工具")
	}
	if runID == "" {
		return "", fmt.Errorf("本对话还没有工作区：先调用 workbench_workspace_open 打开一个")
	}
	run, ok := h.repo.RunState(runID)
	if !ok || !h.mayUseWorkspace(s, run) {
		return "", errAgentToolForbidden
	}
	if engine.WorkspaceRunID(run) != run.ID {
		return "", fmt.Errorf("子 SOP 共享父任务工作区，请从父任务操作")
	}
	wasReclaimed := !run.WorkspaceCleanedAt.IsZero()
	var restoredArtifacts []string
	// The first successful operation must tell Main Bot it restored a snapshot;
	// a readable file after lazy restore does not imply the old workspace survived.
	defer func() {
		if (!wasReclaimed && len(restoredArtifacts) == 0) || resultErr != nil {
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

			raw, err := json.Marshal(payload)
			if err == nil {
				result = string(raw)
			}
		}
	}()
	ctx, restoredArtifacts, release, err := h.mainBotWorkspaceLease(ctx, s, runID)
	if err != nil {
		return "", err
	}
	defer release()
	ctx = context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(s.User))
	ctx = context.WithValue(ctx, mainBotWorkspaceKey{}, runID)
	switch name {
	case "workbench_workspace_tree":
		// Internal only: preparing the workspace on open restores a reclaimed
		// snapshot and starts the sandbox before the CLI hands over.
		path, _ := args["path"].(string)
		return h.invokeMainBotWorkspace(ctx, s, "GET", "tree", nil, map[string]string{"path": path})
	case workspaceAttachInternal:
		// Internal: workbench_publish_artifact with a path, and role deliverables.
		return h.attachWorkspaceFile(ctx, s, args)
	}
	return "", errAgentToolUnknown
}

// mainBotWorkspaceLease reuses the turn's lease and restores a reclaimed
// workspace. Callers outside a conversation turn own a short operation lease.
func (h *agentToolGateway) mainBotWorkspaceLease(ctx context.Context, s *AgentToolSession, runID string) (context.Context, []string, func(), error) {
	release := func() {}
	if h.engine == nil {
		return ctx, nil, release, nil
	}
	ctx = withAssistantDockerBuildProgress(ctx, s)
	if s.workspaceTurn != nil {
		ctx, err := s.workspaceTurn.lease(ctx, runID)
		if err != nil {
			return ctx, nil, release, err
		}
		restored, err := h.engine.PrepareConversationWorkspace(ctx, runID, engine.ConversationWorkspaceToken(ctx, runID))
		return ctx, restored, release, err
	}
	if err := h.engine.MaterializeConversationWorkspace(ctx, runID); err != nil {
		return ctx, nil, release, fmt.Errorf("准备工作区失败：%w", err)
	}
	// The generation's lease only covers the task of a task conversation.
	token := ""
	if runID == s.RunID {
		token = s.WorkspaceToken
	}
	if token == "" {
		var err error
		if token, err = h.engine.BeginConversationWorkspaceUse(runID); err != nil {
			return ctx, nil, release, err
		}
		release = func() {
			if result, err := h.engine.EndConversationWorkspaceUseAsync(ctx, runID, token); err == nil {
				trackAssistantWorkspaceSave(runID, result)
			}
		}
	}
	restored, err := h.engine.PrepareConversationWorkspace(ctx, runID, token)
	if err != nil {
		release()
		return ctx, nil, func() {}, err
	}
	return engine.WithConversationWorkspaceToken(ctx, token, runID), restored, release, nil
}
func (h *agentToolGateway) invokeMainBotWorkspace(ctx context.Context, s *AgentToolSession, method, endpoint string, body any, query map[string]string) (string, error) {
	runID := mainBotWorkspaceRun(ctx)
	if runID == "" {
		runID = s.workspaceRun()
	}
	path := "/api/runs/{id}/workspace"
	if endpoint != "" {
		path += "/" + endpoint
	}
	result, err := (mcpserver.LocalInvoker{Handler: h.api}).Invoke(ctx, application.Operation{Method: method, Path: path}, mcpserver.ToolInput{Path: map[string]string{"id": runID}, Query: query, Body: body})
	if err != nil {
		return "", err
	}
	if result.Status < 200 || result.Status >= 300 {
		return "", &mainBotWorkspaceError{status: result.Status, body: string(result.Body)}
	}
	return redactAgentToolResult(string(result.Body), s.User), nil
}

// mainBotWorkspaceError keeps the HTTP answer of a workspace endpoint; its text
// is the response body, as Main Bot has always seen it.
type mainBotWorkspaceError struct {
	status int
	body   string
}

func (e *mainBotWorkspaceError) Error() string { return e.body }

var workspaceAttachmentTypes = map[string]string{
	".html": "text/html", ".htm": "text/html",
	".md": "text/markdown", ".markdown": "text/markdown",
	".txt": "text/plain", ".log": "text/plain",
	".csv": "text/csv", ".yaml": "text/yaml", ".yml": "text/yaml",
	".json": "application/json", ".xml": "application/xml",
}

// attachWorkspaceFile hands a workspace text file to the user as an
// attachment of this turn's reply: the user cannot reach the execution
// machine, so a workspace path or localhost link opens nothing for them.
func (h *agentToolGateway) attachWorkspaceFile(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	runID := mainBotWorkspaceRun(ctx)
	if runID == "" {
		runID = s.workspaceRun()
	}
	raw, _ := args["path"].(string)
	rel := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "file://"), "/workspace/")
	if marker := "/" + runID + "/"; runID != "" && pathpkg.IsAbs(rel) && strings.Contains(rel, marker) {
		rel = rel[strings.LastIndex(rel, marker)+len(marker):]
	}
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	name := pathpkg.Base(strings.ReplaceAll(rel, "\\", "/"))
	mimeType := workspaceAttachmentTypes[strings.ToLower(pathpkg.Ext(name))]
	if mimeType == "" {
		return "", fmt.Errorf("path 只能是文本文件（html、md、txt、log、csv、yaml、json、xml）；要给用户看页面效果，用 workbench_browser_screenshot 截图")
	}
	result, err := (mcpserver.LocalInvoker{Handler: h.api}).Invoke(ctx, application.Operation{Method: "GET", Path: "/api/runs/{id}/workspace/file"}, mcpserver.ToolInput{Path: map[string]string{"id": runID}, Query: map[string]string{"path": rel}})
	if err != nil {
		return "", err
	}
	if result.Status < 200 || result.Status >= 300 {
		return "", fmt.Errorf("%s", redactAgentToolResult(string(result.Body), s.User))
	}
	var file struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(result.Body, &file); err != nil {
		return "", err
	}
	// Same redaction as any tool result, without its length cap.
	content := redactAuthorizationValues(redactAssistantSecrets(file.Content, s.User))
	_, deliverable := ctx.Value(roleDeliverableKey{}).(bool)
	if err := s.addReplyFile(name, mimeType, []byte(content), !deliverable); err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"attached": name, "notice": "已附在本轮回复里，用户可在对话和产物栏打开；回复里提文件名即可，不要再给路径。"})
	return string(out), nil
}
