package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"strings"
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
		g, ok := h.readGeneration(owner, c.Param("id"))
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

// Native callbacks use this private RPC to fence expired turns and platform
// mode. The pending field remains compatible with callbacks from older CLIs.
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
	// The execution mode controls platform/external writes. Native development
	// runs inside the bound Docker workspace in every approval mode; keep this
	// hook as the live-turn and platform-mode fence, without human or AI review.
	return nativeToolApprovalResult{Allow: true}
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
	current, ok := readAssistantGeneration(h.repo, s.ChatOwnerID, s.ChatSessionID)
	return ok && current.ID == generation && current.Status == "running" && !current.CancelRequested
}

// A predecessor may still own a CLI blocked on its old per-command policy.
// Apply the new workspace policy to those exact pending invocations so a UI
// refresh during rolling deployment cannot strand an otherwise live turn.
func (h *assistantHandlers) settleLegacyWorkspaceNativeApprovals(g domain.AssistantGeneration) {
	if g.Status != "running" || g.CancelRequested {
		return
	}
	items := h.repo.PendingNativeApprovals(g.UserID, g.SessionID)
	if len(items) == 0 {
		return
	}
	session, ok := h.repo.AssistantSession(g.UserID, g.SessionID)
	if !ok || session.RuntimeMode != domain.AssistantModeWorkspace {
		return
	}
	for _, item := range items {
		if item.GenerationID == g.ID && item.ActorID == g.ActorID {
			_ = h.repo.DecideNativeApproval(g.UserID, item.ActorID, g.SessionID, item.ID, "allow")
		}
	}
}
