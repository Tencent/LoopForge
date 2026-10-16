package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"strings"
)

// Storage retains a stable slot; authorization and tool credentials always use
// the caller, never the user who first opened a run conversation.
func assistantStorageOwner(st *repo.Store, id string, fallback int64) int64 {
	if s, ok := st.SharedAssistantSession(id); ok {
		return s.UserID
	}
	return fallback
}
func (h *assistantHandlers) conversationScope(c *gin.Context, id, runID, projectID string, userID int64) (int64, bool, error) {
	if s, ok := h.repo.SharedAssistantSession(id); ok {
		r, exists := h.repo.RunState(s.RunID)
		if s.ProjectID == "" || !exists || r.ExecutionContext.ProjectID != s.ProjectID || !h.workbenchProjectAllowed(c, s.ProjectID, userID) {
			return 0, false, fmt.Errorf("无权访问共享 SOP 会话")
		}
		if (runID != "" && runID != s.RunID) || (projectID != "" && projectID != s.ProjectID) {
			return 0, false, fmt.Errorf("会话与 SOP 运行不匹配")
		}
		return s.UserID, true, nil
	}
	if runID != "" {
		return 0, false, fmt.Errorf("请从任务运行打开共享 SOP 会话")
	}
	return userID, false, nil
}
func (h *assistantHandlers) runConversation(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	r, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "运行不存在"})
		return
	}
	project := r.ExecutionContext.ProjectID
	if project == "" {
		c.JSON(403, gin.H{"error": "运行未绑定项目，不能创建共享会话"})
		return
	}
	if !h.workbenchProjectAllowed(c, project, user.ID) {
		c.JSON(403, gin.H{"error": "无权访问项目"})
		return
	}
	title := r.WorkflowSnapshot.Name
	if title == "" {
		title = r.WorkflowID
	}
	session, err := h.repo.EnsureRunAssistantSession(r.ID, project, title, user.ID)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取共享会话失败"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, session)
}
func sharedTranscriptPrefix(old []assistantMessage, next []assistantMessage) bool {
	if len(next) < len(old) {
		return false
	}
	for i, m := range old {
		a, _ := json.Marshal(m)
		b, _ := json.Marshal(next[i])
		if !bytes.Equal(a, b) {
			return false
		}
	}
	return true
}
func assistantRequestApprovalMode(req assistantRequest) string {
	if req.groupMode {
		return AssistantApprovalAuto
	}
	if req.consultation {
		return AssistantApprovalConfirm
	}
	return normalizeAssistantApprovalMode(req.ApprovalMode)
}
func groupMentionRecipients(req assistantRequest) []domain.AssistantReference {
	if req.followUp != nil || !req.groupMode || len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
		return nil
	}
	out := []domain.AssistantReference{}
	seen := map[string]bool{}
	for _, ref := range req.Messages[len(req.Messages)-1].References {
		if ref.Type == "agent" && !seen[ref.ID] {
			seen[ref.ID] = true
			out = append(out, ref)
		}
	}
	return out
}

func (h *assistantHandlers) dispatchGroupMentions(req assistantRequest) (string, error) {
	if !req.groupMode || len(groupMentionRecipients(req)) == 0 {
		return "", fmt.Errorf("当前消息没有群成员接收人")
	}
	if h.engine == nil {
		return "", fmt.Errorf("群协作执行服务不可用")
	}
	names := []string{}
	for _, ref := range groupMentionRecipients(req) {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s/%s", req.SessionID, len(req.Messages), ref.ID, lastAssistantUserContent(req.Messages))))
		task := "群成员 @你：请以自己的身份直接回应并处理这条消息，不要替其他成员发言。\n" + lastAssistantUserContent(req.Messages)
		if _, err := h.engine.DispatchGroupTask(req.RunID, "", engine.GroupTaskRequest{AgentID: ref.ID, Prompt: task, RequestID: fmt.Sprintf("chat-%x", sum[:20])}); err != nil {
			return "", err
		}
		names = append(names, ref.Name)
	}
	return jsonObject(gin.H{"message": "已通知 " + strings.Join(names, "、") + "。", "drafts": []assistantDraft{}}), nil
}

func assistantSystemPromptForRequest(req assistantRequest, mode string) string {
	if req.groupMode {
		return assistantSystemPromptForMode(AssistantApprovalAuto) + "\n你是这个共享群聊的 Main Bot。用户已授权自由协作直接执行：不要提交待确认草稿，不要请求批准。不要向用户解释审批档位或权限设置。像 IM 群聊简短接话，不写阶段计划表、派发报告、工具参数或重复转述成员的发言。邀请和分工直接调用工具，成员自己通过 group_message 发言；小改动由你直接通过 workbench_workspace_tree/read/write/command 完成并验证；较大任务再交给成员。你也负责需要协调的事情。用户 @成员时由真实角色实例回应，不能假扮其他成员或编造他们的回复。需要成员帮助时用 runs_group_invite / runs_group_dispatch；请求属于当前群与项目，不涉及冷冻 SOP。"
	}

	if req.consultationRole == nil {
		return assistantSystemPromptForMode(mode)
	}
	role := req.consultationRole
	if req.groupMode {
		return assistantSystemPromptForMode(mode) + "\n你是自由协作群里的 " + role.Name + "。职责：" + role.Instructions + "\n这是群聊，不是 SOP。需要动手时用 runs_group_dispatch 派发给真实角色实例（包括你自己）；用群消息和任务输出了解别人在做什么，没有执行记录就不要声称改过文件。"
	}
	return "你是 " + role.Name + "，这是该角色的独立只读咨询实例。按以下角色职责回答用户，不冒充 Main Bot，不声称实际 SOP 节点做过记录中不存在的操作。你可以查询平台和运行过程；禁止写入、派发、审批或提交写入草稿。需要修改时提出返工建议，由 Main Bot 和现有 SOP 引擎交接执行。\n角色定义：\n" + role.Instructions + "\n职责说明：\n" + role.Description
}

// Only approval receipts may update a shared transcript. Existing messages and
// attribution are immutable; the optimistic timestamp prevents stale overwrites.
func sharedReceiptUpdate(old, next domain.AssistantSession, userID int64, name string) error {
	if !next.UpdatedAt.Equal(old.UpdatedAt) {
		return fmt.Errorf("共享会话已更新，请重新打开")
	}
	if len(next.Messages) < len(old.Messages) {
		return fmt.Errorf("不能删除共享会话消息")
	}
	for i, m := range old.Messages {
		n := next.Messages[i]
		if m.Role == "assistant" && n.Content != m.Content {
			var a, b map[string]any
			if json.Unmarshal([]byte(m.Content), &a) != nil || json.Unmarshal([]byte(n.Content), &b) != nil {
				return fmt.Errorf("不能改写共享会话回复")
			}
			scrub := func(v map[string]any) {
				ds, _ := v["drafts"].([]any)
				for _, d := range ds {
					if x, ok := d.(map[string]any); ok {
						delete(x, "saved")
						delete(x, "savedId")
						delete(x, "savedResult")
					}
				}
			}
			scrub(a)
			scrub(b)
			aa, _ := json.Marshal(a)
			bb, _ := json.Marshal(b)
			if !bytes.Equal(aa, bb) {
				return fmt.Errorf("只能记录草稿执行回执")
			}
			n.Content = m.Content
		}
		aa, _ := json.Marshal(m)
		bb, _ := json.Marshal(n)
		if !bytes.Equal(aa, bb) {
			return fmt.Errorf("不能改写共享会话消息")
		}
	}
	for i := len(old.Messages); i < len(next.Messages); i++ {
		m := &next.Messages[i]
		if m.Role != "user" {
			return fmt.Errorf("只能追加用户回执")
		}
		if !bytes.HasPrefix([]byte(m.Content), []byte("已通过 MCP 执行 ")) {
			return fmt.Errorf("请通过发送接口追加讨论")
		}
		m.AuthorID = userID
		m.AuthorName = name
		m.SpeakerName = ""
		m.SpeakerAgentID = ""
	}
	return nil
}
