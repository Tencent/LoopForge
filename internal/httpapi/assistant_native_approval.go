package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"fmt"
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

// The private nativeApproval RPC is called by CLI hooks before native effects.
// It is deliberately absent from tools/list and never grants a reusable permit.
func (h *agentToolGateway) approveNativeTool(ctx context.Context, s *AgentToolSession, raw json.RawMessage) (bool, string) {
	var action struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if len(raw) > 256<<10 || json.Unmarshal(raw, &action) != nil || strings.TrimSpace(action.Name) == "" {
		return false, "原生工具审批参数无效"
	}
	if !h.nativeToolTurnActive(ctx, s) {
		return false, "会话正在停止或交接，请结束本阶段"
	}
	if h.nativeGatewayTool(ctx, s, action.Name, action.Input) {
		return true, ""
	} // Gateway enforces its own exact capability set.
	switch strings.ToLower(action.Name) {
	case "skill", "read", "read_file", "readfile", "glob", "grep", "list", "ls", "list_directory", "list_resources", "view_image", "todowrite", "todo_read", "todoread":
		return true, ""
	}
	if s.RuntimeMode != domain.AssistantModeWorkspace {
		return false, "平台模式禁用原生命令和写入工具，请先打开工作区"
	}
	if s.ExecMode.AutoWrite && !s.ExecMode.ReviewWrites {
		return true, ""
	}
	if s.ExecMode.ReviewWrites {
		// Leave time for a manual decision inside the shortest CLI hook
		// deadline (60s); the hook's HTTP budget is 50s.
		reviewCtx, cancelReview := context.WithTimeout(ctx, 12*time.Second)
		args := map[string]any{"tool": action.Name, "input": action.Input, "runId": s.workspaceRun()}
		review := reviewAssistantAction(withAssistantApprovalReviewer(reviewCtx, s.approvalReviewer), "native_"+action.Name, "工作区 CLI 原生工具，将在绑定的 Docker 工作区中执行", args)
		cancelReview()
		if review.allows() {
			if !h.nativeToolTurnActive(ctx, s) {
				return false, "本轮已结束，审批已失效"
			}
			return true, review.Rationale
		}
	}
	if h.repo == nil || s.ChatSessionID == "" || s.ChatOwnerID == 0 {
		return false, "缺少审批上下文"
	}
	sessionCtx := s.ctx
	if sessionCtx == nil {
		sessionCtx = ctx
	}
	item := domain.AssistantNativeApproval{ID: newAssistantSessionID(), GenerationID: assistantControlScopeFrom(sessionCtx).GenerationID, ActorID: s.UserID, Tool: action.Name, Input: action.Input, ExpiresAt: time.Now().UTC().Add(30 * time.Second)}
	if len(item.Input) == 0 {
		item.Input = json.RawMessage(`{}`)
	}
	if err := h.repo.CreateNativeApproval(s.ChatOwnerID, s.ChatSessionID, item); err != nil {
		return false, "无法保存审批请求"
	}
	if s.Notify != nil {
		s.Notify("native_approval", map[string]string{"id": item.ID})
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		_ = h.repo.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, item.ID, "deny")
		if s.Notify != nil {
			s.Notify("native_approval", map[string]string{"id": item.ID})
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return false, "审批已取消"
		case <-sessionCtx.Done():
			return false, "本轮已停止"
		case <-ticker.C:
		}
		if time.Now().After(item.ExpiresAt) {
			return false, "审批等待超时"
		}
		current, ok := h.repo.NativeApproval(s.ChatOwnerID, s.ChatSessionID, item.ID)
		if !ok {
			return false, "审批已失效"
		}
		switch current.Decision {
		case "allow":
			if !h.nativeToolTurnActive(ctx, s) {
				return false, "本轮已结束，审批已失效"
			}
			return true, "用户已批准本次具体操作"
		case "deny":
			return false, fmt.Sprintf("用户拒绝了 %s 操作", action.Name)
		}
	}
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
	if ctx.Err() != nil || s.closed.Load() || s.ctx != nil && s.ctx.Err() != nil || s.requestedMode() != "" {
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
