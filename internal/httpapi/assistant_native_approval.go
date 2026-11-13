package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

func (h *assistantHandlers) nativeApprovals(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	owner, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	if c.Request.Method == "GET" {
		stored, _ := h.repo.User(user.ID)
		items := h.repo.PendingNativeApprovals(owner, c.Param("id"))
		for i := range items {
			items[i].Input = json.RawMessage(redactAgentToolResult(string(items[i].Input), stored))
		}
		c.JSON(200, items)
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if !decode(c, &body) {
		return
	}
	item, ok := h.repo.NativeApproval(owner, c.Param("id"), c.Param("approvalId"))
	if !ok {
		c.JSON(404, gin.H{"error": "审批不存在"})
		return
	}
	if item.GenerationID != "" {
		g, ok := h.repo.AssistantGeneration(owner, c.Param("id"))
		if !ok || g.ID != item.GenerationID || g.Status != "running" || g.CancelRequested {
			c.JSON(409, gin.H{"error": "本轮已结束，审批已失效"})
			return
		}
	}
	if err = h.repo.DecideNativeApproval(owner, user.ID, c.Param("id"), item.ID, body.Decision); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"decision": body.Decision})
}

// Native callbacks poll this private RPC. Each callback keeps one invocation
// ID across transport retries, so reconnects neither create another approval
// nor change the exact action being approved. Nothing waits on a browser inside
// an HTTP request and human decisions have no deadline.
type nativeToolApprovalResult struct {
	Allow   bool   `json:"allow"`
	Pending bool   `json:"pending,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func nativeApprovalPhase(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *agentToolGateway) approveNativeTool(ctx context.Context, s *AgentToolSession, raw json.RawMessage) nativeToolApprovalResult {
	deny := func(reason string) nativeToolApprovalResult { return nativeToolApprovalResult{Reason: reason} }
	var action struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if len(raw) > 256<<10 || json.Unmarshal(raw, &action) != nil || strings.TrimSpace(action.Name) == "" || len(action.ID) > 128 {
		return deny("原生工具审批参数无效")
	}
	if !h.nativeToolTurnActive(ctx, s) {
		return deny("会话正在停止或交接，请结束本阶段")
	}
	if h.nativeGatewayTool(ctx, s, action.Name, action.Input) {
		return nativeToolApprovalResult{Allow: true}
	}
	switch strings.ToLower(action.Name) {
	case "skill", "read", "read_file", "readfile", "glob", "grep", "list", "ls", "list_directory", "list_resources", "view_image", "todowrite", "todo_read", "todoread":
		return nativeToolApprovalResult{Allow: true}
	}
	if s.RuntimeMode != domain.AssistantModeWorkspace {
		return deny("平台模式禁用原生命令和写入工具，请先打开工作区")
	}
	if len(action.Input) == 0 {
		action.Input = json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, action.Input) != nil {
		return deny("原生工具审批参数无效")
	}
	action.Input = compact.Bytes()
	generation := ""
	if s.ctx != nil {
		generation = assistantControlScopeFrom(s.ctx).GenerationID
	}
	phase := nativeApprovalPhase(s.Token)
	id := nativeApprovalPhase(s.Token + "/" + action.ID)
	// Resolve an existing exact invocation before considering the policy again.
	// A declined request must not turn into a new permit on the next poll.
	if h.repo != nil && action.ID != "" {
		if old, ok := h.repo.NativeApproval(s.ChatOwnerID, s.ChatSessionID, id); ok {
			if old.GenerationID != generation || old.ActorID != s.UserID || old.Tool != action.Name || old.InputDigest != nativeApprovalPhase(string(action.Input)) {
				return deny("审批调用标识已被其他操作使用")
			}
			switch old.Decision {
			case "allow":
				return nativeToolApprovalResult{Allow: true}
			case "deny":
				return deny("用户拒绝了本次操作")
			default:
				return nativeToolApprovalResult{Pending: true}
			}
		}
	}
	if s.ExecMode.AutoWrite && !s.ExecMode.ReviewWrites {
		return nativeToolApprovalResult{Allow: true}
	}
	if s.ExecMode.ReviewWrites {
		reviewCtx, cancelReview := context.WithTimeout(ctx, 12*time.Second)
		args := map[string]any{"tool": action.Name, "input": action.Input, "runId": s.workspaceRun()}
		review := reviewAssistantAction(withAssistantApprovalReviewer(reviewCtx, s.approvalReviewer), "native_"+action.Name, "工作区 CLI 原生工具，将在绑定的 Docker 工作区中执行", args)
		cancelReview()
		if review.allows() {
			if !h.nativeToolTurnActive(ctx, s) {
				return deny("本轮已结束，审批已失效")
			}
			return nativeToolApprovalResult{Allow: true, Reason: review.Rationale}
		}
	}
	if h.repo == nil || s.ChatSessionID == "" || s.ChatOwnerID == 0 || generation == "" || action.ID == "" {
		return deny("缺少审批上下文或调用标识")
	}
	item := domain.AssistantNativeApproval{ID: id, GenerationID: generation, ActorID: s.UserID, Tool: action.Name, Input: action.Input}
	if err := h.repo.CreateNativeApproval(s.ChatOwnerID, s.ChatSessionID, phase, item); err != nil {
		return deny("无法保存审批请求")
	}
	if !h.nativeToolTurnActive(ctx, s) {
		_ = h.repo.ReleaseNativeApprovals(s.ChatOwnerID, s.ChatSessionID, generation, phase)
		return deny("本阶段已结束，审批已失效")
	}
	if s.Notify != nil {
		s.Notify("native_approval", map[string]string{"id": item.ID})
	}
	return nativeToolApprovalResult{Pending: true}
}

// Native hooks see the CLI's local MCP name, not necessarily the wire name.
// Permit only the session gateway's aliases/wrappers here; tools/call still
// checks the current catalog, token, mode and concrete action permissions.
func (h *agentToolGateway) nativeGatewayTool(ctx context.Context, s *AgentToolSession, name string, input json.RawMessage) bool {
	for _, prefix := range []string{"mcp__" + agentToolSessionMCPName + "__", agentToolSessionMCPName + "_"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	var args struct {
		ServerName string `json:"ServerName"`
		ToolName   string `json:"ToolName"`
		Namespace  string `json:"namespace"`
		Server     string `json:"server"`
	}
	if strings.HasPrefix(name, "MCP:") {
		// Cursor's generic hook exposes MCP:<wire tool name>. Its native
		// tools never use this prefix; match the session catalog exactly.
		_, ok := lookupAgentTool(h.buildCatalog(ctx, s), strings.TrimPrefix(name, "MCP:"))
		return ok
	}
	if json.Unmarshal(input, &args) != nil {
		return false
	}
	server := args.Namespace
	if server == "" {
		server = args.Server
	}
	if args.Server != "" && args.Server != server {
		return false
	}
	switch name {
	case "call_mcp_tool": // Agy
		return args.ServerName == agentToolSessionMCPName && args.ToolName != ""
	case "CallMcpTool", "CallDynamicTool": // Cursor
		return server == agentToolSessionMCPName && args.ToolName != ""
	case "GetMcpTools", "GetDynamicTools":
		return server == agentToolSessionMCPName
	default:
		return false
	}
}

func (h *agentToolGateway) nativeToolTurnActive(ctx context.Context, s *AgentToolSession) bool {
	if ctx.Err() != nil || s.nativePhaseClosed.Load() || s.closed.Load() || s.ctx != nil && s.ctx.Err() != nil || s.requestedMode() != "" {
		return false
	}
	if s.ctx == nil {
		return true
	}
	generation := assistantControlScopeFrom(s.ctx).GenerationID
	if generation == "" {
		return true
	}
	if h.repo == nil {
		return false
	}
	current, ok := h.repo.AssistantGeneration(s.ChatOwnerID, s.ChatSessionID)
	return ok && current.ID == generation && current.Status == "running" && !current.CancelRequested
}
