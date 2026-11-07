package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentflow/internal/application"
	"agentflow/internal/domain"
	"agentflow/internal/mcpserver"
)

// A conversation Main Bot bound to an issue's SOP run works in that run's
// workspace. Main Bot controls the run directly, without a confirmation draft:
// it is the work the user is talking about, and every action goes through the
// run endpoints with the user's permissions.

func workspaceSOPDescriptor() agentToolDescriptor {
	return agentToolDescriptor{
		Name:        "workbench_workspace_sop",
		Description: "控制本对话当前绑定工作区所属的 SOP 运行（用 workbench_workspace_open 切到需求的 SOP 工作区后可用，它会返回 sopRunId），立即生效，不需要提交草稿。pause：让 SOP 在当前节点停下，停下后可以直接改文件、跑命令；resume：让暂停的 SOP 继续，由它的节点交付，context 写清你改了什么、SOP 接下来要注意什么；takeover：取消执行中的 SOP，之后直接在它的工作区改代码，并用 workbench_workspace_submit_mr 推送到它的工作分支提交 MR。用户在 SOP 执行时要改代码，先说明会暂停或接管再调用。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["pause","resume","takeover"]},"context":{"type":"string","maxLength":4000}},"required":["action"],"additionalProperties":false}`),
		Kind:        "workspace",
	}
}

// sopStopped mirrors the repo's workspace write rule for SOP runs.
func sopStopped(run domain.Run) bool {
	if domain.TerminalRunStatus(run.Status) {
		return true
	}
	if run.Status != "waiting" || !run.PauseRequested || run.CancelRequested {
		return false
	}
	for _, node := range run.Nodes {
		if node != nil && node.Status == "running" {
			return false
		}
	}
	return true
}

func borrowedSOPNotice(run domain.Run) string {
	switch {
	case domain.TerminalRunStatus(run.Status):
		return fmt.Sprintf("SOP 运行 %s 已结束（%s）：可以直接改文件、跑命令，已回收的工作区会先恢复快照；用户要提交时调用 workbench_workspace_submit_mr，推送到它的工作分支并发起或更新 MR。", run.ID, run.Status)
	case sopStopped(run):
		return fmt.Sprintf("SOP 运行 %s 已暂停：可以直接改文件、跑命令。改完调用 workbench_workspace_sop resume 让 SOP 带着改动继续并由它交付，或 takeover 后自己提交 MR；暂停期间不能提交 MR。", run.ID)
	case run.CancelRequested:
		return fmt.Sprintf("SOP 运行 %s 正在取消，当前节点停下后就可以改文件，稍后再试。", run.ID)
	case run.PauseRequested:
		return fmt.Sprintf("SOP 运行 %s 正在暂停，当前节点停下后就可以改文件，稍后再试。", run.ID)
	default:
		return fmt.Sprintf("SOP 运行 %s（%s）执行中，工作区只能读取。用户要改代码时调用 workbench_workspace_sop：pause 暂停后改、改完 resume；或 takeover 取消 SOP 后接着改并提交 MR。", run.ID, run.Status)
	}
}

func (h *agentToolGateway) controlConversationSOP(ctx context.Context, s *AgentToolSession, args map[string]any) (string, error) {
	releaseTurn := s.workspaceTurn.lockOperation()
	defer releaseTurn()
	runID := s.workspaceRun()
	ctx = s.workspaceTurn.withLease(ctx, runID)
	run, ok := h.repo.RunState(runID)
	if s.RunID != "" || runID == "" || !ok || !borrowableSOPRun(run, s.ProjectID) || s.User.ID != s.UserID {
		return "", fmt.Errorf("本对话当前绑定的不是 SOP 的工作区：先用 workbench_workspace_open 传 issueId 切到该需求的 SOP 工作区")
	}
	action, _ := args["action"].(string)
	note, _ := args["context"].(string)
	endpoint := ""
	var body any
	switch strings.TrimSpace(action) {
	case "pause":
		if domain.TerminalRunStatus(run.Status) {
			return "", fmt.Errorf("SOP 运行已结束（%s），不需要暂停", run.Status)
		}
		endpoint = "pause"
	case "resume":
		if !run.PauseRequested || domain.TerminalRunStatus(run.Status) {
			return "", fmt.Errorf("SOP 运行没有暂停（%s），不能继续", run.Status)
		}
		endpoint, body = "resume", map[string]any{"context": strings.TrimSpace(note)}
	case "takeover":
		if !domain.TerminalRunStatus(run.Status) {
			endpoint = "cancel"
		}
	default:
		return "", fmt.Errorf("action 只能是 pause、resume 或 takeover")
	}
	if endpoint != "" {
		ctx = context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(s.User))
		result, err := (mcpserver.LocalInvoker{Handler: h.api}).Invoke(ctx, application.Operation{Method: "POST", Path: "/api/runs/{id}/" + endpoint}, mcpserver.ToolInput{Path: map[string]string{"id": run.ID}, Body: body})
		if err != nil {
			return "", err
		}
		if result.Status < 200 || result.Status >= 300 {
			return "", fmt.Errorf("%s", redactAgentToolResult(string(result.Body), s.User))
		}
		if current, ok := h.repo.RunState(run.ID); ok {
			run = current
		}
	}
	raw, _ := json.Marshal(map[string]any{"sopRunId": run.ID, "sopStatus": run.Status, "pauseRequested": run.PauseRequested, "cancelRequested": run.CancelRequested, "notice": borrowedSOPNotice(run)})
	return string(raw), nil
}
