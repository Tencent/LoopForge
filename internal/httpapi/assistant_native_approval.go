package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
			items[i].Input = redactNativeApprovalInput(items[i].Input, stored)
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

// redactNativeApprovalInput redacts each decoded string of the input. Text
// redaction over the encoded JSON can cut an escape such as \" and leave the
// card with invalid JSON, which renders as an empty response.
func redactNativeApprovalInput(raw json.RawMessage, user domain.User) json.RawMessage {
	hidden := json.RawMessage(`"[redacted]"`)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return hidden
	}
	out, err := json.Marshal(redactJSONStrings(value, user))
	if err != nil {
		return hidden
	}
	return out
}

func redactJSONStrings(value any, user domain.User) any {
	switch v := value.(type) {
	case string:
		return redactAgentToolResult(v, user)
	case []any:
		for i := range v {
			v[i] = redactJSONStrings(v[i], user)
		}
	case map[string]any:
		for key := range v {
			v[key] = redactJSONStrings(v[key], user)
		}
	}
	return value
}

// Native callbacks poll this private RPC. Each callback keeps one invocation
// ID across transport retries, so reconnects neither create another approval
// nor change the exact action being approved.
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
	// Files and commands inside the sandbox run in every approval mode. Only
	// commands that write to outside systems follow the approval mode.
	label, external := nativeExternalWrite(action.Input)
	if !external {
		return nativeToolApprovalResult{Allow: true}
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
				return deny("用户拒绝了本次外部写入，不要换一种方式重试；先问用户怎么处理")
			}
			if time.Since(old.CreatedAt) > nativeApprovalWait {
				_ = h.repo.DecideNativeApproval(s.ChatOwnerID, old.ActorID, s.ChatSessionID, old.ID, "deny")
				return deny("等待用户批准超时，操作未执行；告诉用户需要批准什么，等用户回复后再试")
			}
			return nativeToolApprovalResult{Pending: true}
		}
	}
	if s.ExecMode.AutoWrite && !s.ExecMode.ReviewWrites {
		return nativeToolApprovalResult{Allow: true}
	}
	if s.ExecMode.ReviewWrites {
		// Assisted mode decides with the reviewer alone: allow or deny, never
		// a card for the speaker. The review outlives one hook request (20s),
		// so it runs in the background and the hook polls for the verdict.
		if action.ID == "" {
			return deny("缺少审批调用标识")
		}
		review, done, ok := s.nativeReview(ctx, nativeReviewPoll, id, nativeApprovalPhase(string(action.Input)), func(reviewCtx context.Context) assistantApprovalReview {
			args := map[string]any{"tool": action.Name, "input": action.Input, "runId": s.workspaceRun()}
			return reviewAssistantAction(withAssistantApprovalReviewer(reviewCtx, s.approvalReviewer), "native_"+action.Name, "工作区命令（"+label+"），会写入沙箱外的系统", args)
		})
		switch {
		case !ok:
			return deny("审批调用标识已被其他操作使用")
		case !done:
			return nativeToolApprovalResult{Pending: true}
		case review.allows():
			if !h.nativeToolTurnActive(ctx, s) {
				return deny("本轮已结束，审批已失效")
			}
			return nativeToolApprovalResult{Allow: true, Reason: review.Rationale}
		default:
			return deny(nativeReviewRefusal(review))
		}
	}
	// Ask the speaker; the hook polls until they decide or the turn ends.
	if h.repo == nil || s.ChatSessionID == "" || s.ChatOwnerID == 0 || generation == "" || action.ID == "" {
		return deny("缺少审批上下文或调用标识")
	}
	item := domain.AssistantNativeApproval{ID: id, GenerationID: generation, ActorID: s.UserID, Tool: action.Name, Summary: label, Input: action.Input}
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

// approveKnowledgeWrite gates potentially mutating knowledge MCP tool calls in
// interactive Chat turns using the same approval modes and NativeApprovalPanel
// workflow as native external writes.
func (h *agentToolGateway) approveKnowledgeWrite(ctx context.Context, s *AgentToolSession, toolName string, rawArgs json.RawMessage) (string, bool) {
	if s == nil {
		return "缺少会话上下文，已拒绝知识库写入操作", false
	}
	if s.ExecMode.AutoWrite && !s.ExecMode.ReviewWrites {
		return "", true
	}
	var compact bytes.Buffer
	if len(rawArgs) == 0 || string(bytes.TrimSpace(rawArgs)) == "null" {
		compact.WriteString("{}")
	} else if json.Compact(&compact, rawArgs) != nil {
		compact.WriteString("{}")
	}
	input := compact.Bytes()

	if s.ExecMode.ReviewWrites {
		review := reviewAssistantAction(
			withAssistantApprovalReviewer(ctx, s.approvalReviewer),
			toolName,
			"知识库 MCP 写入操作（"+toolName+"）",
			map[string]any{"tool": toolName, "arguments": json.RawMessage(input)},
		)
		if review.allows() {
			return "", true
		}
		return nativeReviewRefusal(review), false
	}

	generation := ""
	if s.ctx != nil {
		generation = assistantControlScopeFrom(s.ctx).GenerationID
	}
	if h.repo == nil || s.ChatSessionID == "" || s.ChatOwnerID == 0 || generation == "" {
		return "当前操作可能写入外部知识库，但缺少交互审批上下文，已拒绝执行；请先向用户确认后再试", false
	}
	phase := nativeApprovalPhase(s.Token)
	callID := fmt.Sprintf("kbw-%d", time.Now().UTC().UnixNano())
	id := nativeApprovalPhase(s.Token + "/" + callID)
	item := domain.AssistantNativeApproval{
		ID:           id,
		GenerationID: generation,
		ActorID:      s.UserID,
		Tool:         toolName,
		Summary:      "知识库写入操作：" + toolName,
		Input:        input,
	}
	if err := h.repo.CreateNativeApproval(s.ChatOwnerID, s.ChatSessionID, phase, item); err != nil {
		return "无法创建知识库写入审批请求，操作未执行", false
	}
	if !h.nativeToolTurnActive(ctx, s) {
		_ = h.repo.ReleaseNativeApprovals(s.ChatOwnerID, s.ChatSessionID, generation, phase)
		return "本轮已结束，知识库写入审批已失效", false
	}
	if s.Notify != nil {
		s.Notify("native_approval", map[string]string{"id": item.ID})
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(nativeApprovalWait)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = h.repo.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, item.ID, "deny")
			return "会话已取消，知识库写入操作未执行", false
		case <-deadline.C:
			_ = h.repo.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, item.ID, "deny")
			return "等待用户批准知识库写入超时，操作未执行；请告知用户需要确认的内容", false
		case <-ticker.C:
			if !h.nativeToolTurnActive(ctx, s) {
				_ = h.repo.DecideNativeApproval(s.ChatOwnerID, s.UserID, s.ChatSessionID, item.ID, "deny")
				return "本轮已结束，知识库写入操作未执行", false
			}
			cur, ok := h.repo.NativeApproval(s.ChatOwnerID, s.ChatSessionID, item.ID)
			if !ok {
				return "审批记录已失效，知识库写入操作未执行", false
			}
			switch cur.Decision {
			case "allow":
				return "", true
			case "deny":
				return "用户拒绝了本次知识库写入操作，不要换一种方式重试；请先询问用户如何处理", false
			}
		}
	}
}

// nativeReviewRefusal tells the agent why assisted mode declined a command.
// The reviewer's own texts for an unavailable review still mention a manual
// confirmation used by platform drafts; native commands have none.
func nativeReviewRefusal(review assistantApprovalReview) string {
	if review.Outcome != "allow" && review.Outcome != "deny" {
		return "帮我批准：自动审查暂不可用，已拒绝本次外部写入，操作未执行；告诉用户需要执行什么，不要换一种方式重试"
	}
	reason := strings.TrimSpace(review.Rationale)
	if reason == "" {
		reason = "审查未通过"
	}
	return "帮我批准：审查拒绝了本次外部写入（" + reason + "），操作未执行；不要换一种方式重试，先告诉用户原因"
}

// nativeReviewBudget bounds one background review. The reviewer stops at 60
// seconds itself; the margin only covers starting its CLI.
const nativeReviewBudget = 90 * time.Second

// nativeReviewPoll is how long one hook request waits for the verdict before
// answering pending, below the hook's 20-second request timeout.
const nativeReviewPoll = 10 * time.Second

// nativeApprovalWait bounds how long a command waits for the speaker. It
// stays below Kimi's 600-second hook cap, past which Kimi runs the tool.
const nativeApprovalWait = 9 * time.Minute

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
