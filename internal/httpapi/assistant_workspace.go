package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
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

// Project access is checked at the conversation entry point. The storage owner
// does not limit which participants can use the conversation workspace.
func (s *AgentToolSession) canOpenConversationWorkspace() bool {
	return s.RunID == "" && strings.TrimSpace(s.ProjectID) != "" && s.ChatSessionID != ""
}

func mainBotWorkspaceTools(s *AgentToolSession) []agentToolDescriptor {
	if s.Mode != AgentToolAssistant || (s.RunID == "" && !s.canOpenConversationWorkspace()) {
		return nil
	}
	descriptors := []struct{ name, description, schema string }{
		{"tree", "列出当前工作区的文件（任务会话是任务工作区；普通对话是用 workbench_workspace_open 打开的临时工作区）。路径相对工作区，不是控制平面目录。", `{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`},
		{"read", "读取当前工作区文件，返回 content 与 sha256；修改前先读取。", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`},
		{"write", "直接保存当前工作区文件。expectedSha256 必须为读取时的 sha256；新文件传 missing。若文件已变更，重新读取并对齐，不能盲目覆盖。SOP 正在执行时需先暂停；临时工作区允许直接写入。", `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"expectedSha256":{"type":"string"}},"required":["path","content","expectedSha256"],"additionalProperties":false}`},
		{"command", "在当前工作区运行 shell 命令并返回 output、exitCode，用于小改动与验证。最长 120 秒；SOP 正在执行时需先暂停。", `{"type":"object","properties":{"command":{"type":"string"},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":120}},"required":["command"],"additionalProperties":false}`},
		{"attach", "把工作区里的文本文件（HTML demo、Markdown/文本报告、JSON、CSV 等，单个不超过 1MB）附在本轮回复里交给用户。工作区地址只在工作区存活时能在侧栏「浏览器」页签打开，要让用户留存或下载文件就用它：HTML 在产物栏以沙箱方式可交互预览，其他文件可查看和下载。每轮最多 4 个；同名文件再次附加时以最新内容为准，所以改完文件后重新附加即可。", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`},
	}
	out := make([]agentToolDescriptor, 0, len(descriptors)+2)
	if s.RunID == "" {
		out = append(out, workspaceOpenDescriptor(), workspaceSOPDescriptor())
	}
	for _, d := range descriptors {
		out = append(out, agentToolDescriptor{Name: "workbench_workspace_" + d.name, Description: d.description, InputSchema: json.RawMessage(d.schema), Kind: "workspace"})
	}
	return append(out, workspaceSubmitMRDescriptor())
}

func (h *agentToolGateway) callMainBotWorkspace(ctx context.Context, s *AgentToolSession, name string, args map[string]any) (result string, resultErr error) {
	if len(mainBotWorkspaceTools(s)) == 0 {
		return "", errAgentToolForbidden
	}
	if name == "workbench_workspace_submit_mr" && !s.ExecMode.AutoWrite {
		return "", fmt.Errorf("提交 MR 或推送分支需要外部写入授权，请切换到「帮我批准」或「完全权限」后继续")
	}
	if s.ExecMode.ReviewWrites && name != "workbench_workspace_tree" && name != "workbench_workspace_read" && name != "workbench_workspace_attach" && name != "workbench_workspace_open" && name != "workbench_platform_mode" {
		// Commands are always reviewed: arbitrary shell text can mutate state.
		ctx = withAssistantApprovalReviewer(ctx, s.approvalReviewer)
		review := reviewAssistantAction(ctx, name, "当前项目工作区操作，文件写入、命令执行或提交 MR 会产生副作用", map[string]any{"runId": s.workspaceRun(), "arguments": args})
		if !review.allows() {
			return "", assistantReviewRefusal(name, review)
		}
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
	if name == "workbench_workspace_sop" {
		return h.controlConversationSOP(ctx, s, args)
	}
	releaseTurn := s.workspaceTurn.lockOperation()
	defer releaseTurn()
	runID := s.workspaceRun()
	if expected := mainBotWorkspaceRun(ctx); expected != "" && expected != runID {
		return "", fmt.Errorf("工作区已切换，请重新调用工具")
	}
	if runID == "" {
		return "", fmt.Errorf("本对话还没有工作区：先调用 workbench_workspace_open 为要改的仓库开一个临时工作区")
	}
	run, ok := h.repo.RunState(runID)
	if !ok || run.ExecutionContext.ProjectID != s.ProjectID || s.User.ID != s.UserID {
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
					if status, err := h.engine.WorkspacePreparationStatus(ctx, runID); err == nil {
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
	ctx, restoredArtifacts, release, err := h.mainBotWorkspaceLease(ctx, s, runID)
	if err != nil {
		return "", err
	}
	defer release()
	ctx = context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(s.User))
	ctx = context.WithValue(ctx, mainBotWorkspaceKey{}, runID)
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
	case "workbench_workspace_attach":
		return h.attachWorkspaceFile(ctx, s, args)
	case "workbench_workspace_submit_mr":
		return h.workspaceSubmitMR(ctx, s, args)
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

// mainBotWorkspaceBusy reports an answer that is safe to retry: the endpoint
// refused before touching the PTY because another holder (a parallel role's
// command preparing the environment, a snapshot save) owns the workspace.
func mainBotWorkspaceBusy(err error) bool {
	var failure *mainBotWorkspaceError
	if !errors.As(err, &failure) || failure.status != 409 {
		return false
	}
	var body struct {
		Code string `json:"code"`
	}
	return json.Unmarshal([]byte(failure.body), &body) == nil && body.Code == "workspace_busy"
}

// mainBotTerminalBusyWait bounds how long a command waits for parallel roles
// before its own timeout starts; it is long enough for one environment setup.
var mainBotTerminalBusyWait = 90 * time.Second

func openMainBotTerminal(ctx context.Context, open func(context.Context) (runtime.TerminalResult, error)) (runtime.TerminalResult, error) {
	ctx, cancel := context.WithTimeout(ctx, mainBotTerminalBusyWait)
	defer cancel()
	delay := 300 * time.Millisecond
	for {
		opened, err := open(ctx)
		if err == nil || !mainBotWorkspaceBusy(err) {
			return opened, err
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return opened, fmt.Errorf("等待其他角色释放工作区超时（%s）：%w", mainBotTerminalBusyWait, err)
			}
			return opened, ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > 3*time.Second {
			delay = 3 * time.Second
		}
	}
}

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
	raw, _ := args["path"].(string)
	rel := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "file://"), "/workspace/")
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	name := pathpkg.Base(strings.ReplaceAll(rel, "\\", "/"))
	mimeType := workspaceAttachmentTypes[strings.ToLower(pathpkg.Ext(name))]
	if mimeType == "" {
		return "", fmt.Errorf("只能附加文本文件（html、md、txt、log、csv、yaml、json、xml）；要给用户看页面效果，用 workbench_browser_screenshot 截图")
	}
	runID := mainBotWorkspaceRun(ctx)
	if runID == "" {
		runID = s.workspaceRun()
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
	parent := ctx
	terminal := func(ctx context.Context, operation, id, data string) (runtime.TerminalResult, error) {
		raw, err := h.invokeMainBotWorkspace(ctx, s, "POST", "terminal", map[string]any{"operation": operation, "id": id, "data": data}, nil)
		var result runtime.TerminalResult
		if err == nil {
			err = json.Unmarshal([]byte(raw), &result)
		}
		return result, err
	}
	// Roles dispatched in parallel share this workspace. One may be preparing
	// its environment right now; wait for it instead of failing this command.
	// The wait does not count against the command's own timeout.
	opened, err := openMainBotTerminal(parent, func(ctx context.Context) (runtime.TerminalResult, error) {
		return terminal(ctx, "open", "", "")
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_, _ = terminal(cleanup, "close", opened.ID, "")
	}()
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	token := hex.EncodeToString(nonce)
	marker := "__main_bot_exit_" + token + ":"
	ready := "__main_bot_ready_" + token
	// The terminal echoes input typed before `stty -echo` takes effect and
	// prints a prompt before every line. Silence both first, then wait for a
	// ready line that only the shell's own output can produce (the echoed
	// input has a quote before the token), and keep what follows it.
	if _, err = terminal(ctx, "input", opened.ID, "stty -echo; PS1=; PS2=; printf '\\n%s\\n' '"+ready+"'\n"); err != nil {
		return "", err
	}
	var output strings.Builder
	read := func() (bool, error) {
		result, err := terminal(ctx, "read", opened.ID, "")
		if err != nil {
			return false, err
		}
		if output.Len()+len(result.Data) > 128<<10 {
			return false, fmt.Errorf("command output exceeds 128 KB")
		}
		output.Write(result.Data)
		return result.Exited, nil
	}
	pause := func() error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("command stopped: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	}
	for {
		exited, err := read()
		if err != nil {
			return "", err
		}
		text := strings.ReplaceAll(output.String(), "\r\n", "\n")
		if at := strings.Index(text, "\n"+ready+"\n"); at >= 0 {
			output.Reset()
			output.WriteString(text[at+len(ready)+2:])
			break
		}
		if exited {
			return "", fmt.Errorf("workspace terminal closed before the command ran: %s", truncateRunes(text, 2000))
		}
		if err := pause(); err != nil {
			return "", err
		}
	}
	// Treat the requested command as a single shell argument, preserving quotes,
	// substitutions and newlines without letting it alter our status wrapper.
	quoted := "'" + strings.ReplaceAll(command, "'", "'\"'\"'") + "'"
	// One input line: the shell reads it whole before running anything, so
	// line-editor output (e.g. bash 5 bracketed-paste toggles around every
	// read) lands before the begin line and none follows the exit line.
	begin := "__main_bot_begin_" + token
	input := "printf '\\n%s\\n' '" + begin + "'; /bin/sh -c " + quoted + "; printf '\\n" + marker + "%s\\n' \"$?\"; exit\n"
	if _, err = terminal(ctx, "input", opened.ID, input); err != nil {
		return "", err
	}
	for {
		exited, err := read()
		if err == nil && !exited {
			err = pause()
		}
		if err != nil {
			if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", workspaceCommandTimeout(seconds, output.String(), begin)
			}
			return "", err
		}
		if exited {
			out, code, err := workspaceCommandOutput(output.String(), begin, marker)
			if err != nil {
				return "", err
			}
			raw, _ := json.Marshal(map[string]any{"output": out, "exitCode": code})
			return string(raw), nil
		}
	}
}

// workspaceCommandTimeout tells the model what the stopped command printed and
// how to proceed, instead of a bare "context deadline exceeded".
func workspaceCommandTimeout(seconds int, raw, begin string) error {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if at := strings.Index(text, "\n"+begin+"\n"); at >= 0 {
		text = text[at+len(begin)+2:]
	} else {
		text = ""
	}
	msg := fmt.Sprintf("command did not finish within %d seconds and was stopped", seconds)
	if seconds < 120 {
		msg += "; retry with a larger timeoutSeconds (max 120)"
	} else {
		msg += "; split it into shorter steps, or start it in the background (nohup ... > log 2>&1 &) and poll the log"
	}
	if strings.TrimSpace(text) == "" {
		return errors.New(msg + ". It printed nothing before it was stopped.")
	}
	return fmt.Errorf("%s. Output so far:\n%s", msg, tailRunes(text, 4000))
}

// workspaceCommandOutput keeps only what the command printed: the text between
// the begin line and the exit-status line, both printed by the shell itself
// (echoed input has a quote before the token, never a newline).
func workspaceCommandOutput(raw, begin, marker string) (string, int, error) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	start := strings.Index(text, "\n"+begin+"\n")
	if start < 0 {
		return "", 0, fmt.Errorf("command ended before it started: %s", truncateRunes(text, 2000))
	}
	text = text[start+len(begin)+2:]
	at := strings.LastIndex(text, "\n"+marker)
	if at < 0 {
		return "", 0, fmt.Errorf("command ended without an exit status: %s", truncateRunes(text, 2000))
	}
	line := strings.SplitN(text[at+1+len(marker):], "\n", 2)[0]
	code, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return "", 0, err
	}
	return text[:at], code, nil
}
