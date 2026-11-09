package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// spaceAITimeout settles a reply whose generation never reported back.
const spaceAITimeout = 20 * time.Minute

const spaceAIAuthor = "LoopForge AI"

// spaceDeliverablePrompt tells the model that this page is viewed in a
// browser on a cloud host: a filesystem path is not a deliverable.
const spaceDeliverablePrompt = "用户在云端网页里看这个会话，看不到服务器上的文件。页面、PPT、图片和文档必须调用 workbench_publish_artifact，把内容本身传进去，不要回复 /tmp、/data、/home 这类路径。可交互页面用 kind=prototype、format=html；幻灯片用 kind=ppt、format=markdown，每页之间用单独一行 --- 分隔；图片用 imageBase64（png、jpeg、gif 或 webp 的 base64 或 data URL）；已经公开的 https 页面用 kind=browser 和 url。发布后只告诉用户标题，侧边栏会打开。"

func withSpaceDeliverable(req assistantRequest, prompt string) string {
	if strings.TrimSpace(req.SpaceID) == "" {
		return prompt
	}
	return prompt + "\n" + spaceDeliverablePrompt
}

func spaceDeliverableSuffix(session *AgentToolSession) string {
	if session == nil || strings.TrimSpace(session.SpaceID) == "" {
		return ""
	}
	return "\n" + spaceDeliverablePrompt
}

// internalCall replays a request against the API router with the caller's
// credentials, so the target endpoint applies its own auth and validation.
func (h *spacesHandlers) internalCall(c *gin.Context, method, path string, body any) (int, []byte) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(c.Request.Context(), method, path, bytes.NewReader(raw))
	if err != nil {
		return http.StatusInternalServerError, []byte(`{"error":"内部请求失败"}`)
	}
	for key, values := range c.Request.Header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Host = c.Request.Host
	req.RemoteAddr = c.Request.RemoteAddr
	rec := httptest.NewRecorder()
	h.api.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func apiError(raw []byte, fallback string) string {
	var out struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &out) == nil && strings.TrimSpace(out.Error) != "" {
		return out.Error
	}
	return fallback
}

// askAI starts an assistant turn for msg and posts a placeholder reply that
// reconcileAI fills in when the generation settles. model and approvalMode
// are the same Codex controls as the in-page assistant; sessionID keeps one
// native session per conversation so later turns resume instead of starting over.
func (h *spacesHandlers) askAI(c *gin.Context, room domain.ChatRoom, space domain.RequirementSpace, msg domain.ChatMessage, model, approvalMode, sessionID string) domain.ChatMessage {
	reply := domain.ChatMessage{Kind: domain.ChatKindAI, Author: spaceAIAuthor, ThreadID: msg.ThreadID, AIStatus: domain.AIStatusRunning, AIRequesterID: msg.UserID,
		CreatedAt: msg.CreatedAt.Add(time.Millisecond)}
	sessionID = assistantRecordID(sessionID)
	if sessionID == "" || len(sessionID) > 64 {
		sessionID = newAssistantSessionID()
	}
	userID, _ := h.rooms.roomActor(c)
	var prior []assistantMessage
	if saved, err := loadAssistantConversation(h.repo, userID, sessionID); err == nil {
		for _, m := range saved.Messages {
			if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" || (m.Role == "user" && (m.Trigger != "" || m.TriggerID != "")) {
				continue
			}
			m.Attachments = nil
			prior = append(prior, m)
		}
	}
	if len(prior) > 180 {
		prior = prior[len(prior)-180:]
	}
	content := h.aiPrompt(room, space, msg)
	if len(prior) > 0 {
		content = fmt.Sprintf("会话里又有人发言。%s：%s%s\n请接着这个会话回复。需要写入平台时按当前审批模式处理：先问我就提交草稿，帮我批准就直接保存低风险内容，完全访问就直接执行。%s", msg.Author, msg.Body, attachmentNote(msg.Attachments), spaceDeliverablePrompt)
	}
	prior = append(prior, assistantMessage{Role: "user", Content: content, Attachments: msg.Attachments})
	status, raw := h.internalCall(c, http.MethodPost, "/api/assistant/generations", gin.H{
		"projectId":    room.ProjectID,
		"sessionId":    sessionID,
		"model":        model,
		"approvalMode": approvalMode,
		"spaceId":      room.ID,
		"messages":     prior,
	})
	if status >= 400 {
		reply.AIStatus = domain.AIStatusFailed
		reply.AIError = apiError(raw, fmt.Sprintf("AI 暂时不可用（HTTP %d）", status))
	} else {
		reply.AISessionID = sessionID
	}
	stored, err := h.post(room, reply)
	if err != nil {
		reply.AIStatus, reply.AIError = domain.AIStatusFailed, err.Error()
		return reply
	}
	return stored
}

// aiPrompt gives the assistant the conversation as context: it sees the
// same requirement, artifacts, SOPs and recent discussion the members see.
func (h *spacesHandlers) aiPrompt(room domain.ChatRoom, space domain.RequirementSpace, msg domain.ChatMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "你是会话「%s」里的 AI 协作者。回答要具体、简洁，用中文；不确定就直说。\n", room.Name)
	if req := h.requirementOf(space); req != nil {
		fmt.Fprintf(&b, "\n这个会话关联了平台需求 #%d「%s」，状态 %s，优先级 %s。\n", req.Number, req.Title, req.Status, req.Priority)
		if issue, ok := h.repo.Issue(req.ID); ok && issue.Description != "" {
			fmt.Fprintf(&b, "需求描述：\n%s\n", roomTrim(issue.Description, 2000))
		}
	} else {
		b.WriteString("\n这个会话还没有关联需求。用户想正式推进时，建议 TA 点右侧「建需求」或输入 /建需求，不要替 TA 决定。\n")
	}
	if space.Summary != "" {
		fmt.Fprintf(&b, "\n会话描述：\n%s\n", roomTrim(space.Summary, 2000))
	}
	if members := h.repo.SpaceMembers(room.ID); len(members) > 0 {
		b.WriteString("\n成员：")
		for i, m := range members {
			if i > 0 {
				b.WriteString("、")
			}
			b.WriteString(m.Name)
			if m.Title != "" {
				b.WriteString("（" + m.Title + "）")
			}
		}
		b.WriteString("\n")
	}
	if arts := h.repo.SpaceArtifacts(room.ID); len(arts) > 0 {
		b.WriteString("\n产物：\n")
		for _, a := range arts {
			fmt.Fprintf(&b, "- [%s] %s v%d", a.Kind, a.Title, a.Version)
			if a.Summary != "" {
				b.WriteString("：" + roomTrim(a.Summary, 200))
			}
			b.WriteString("\n")
			if a.Kind == "doc" || a.Kind == "api" {
				if body := roomTrim(a.Body, 3000); body != "" {
					b.WriteString(indent(body) + "\n")
				}
			}
		}
	}
	if lanes := h.lanes(room, space); len(lanes) > 0 {
		b.WriteString("\nSOP：\n")
		for _, l := range lanes {
			fmt.Fprintf(&b, "- %s：%s", l.WorkflowName, laneStatusText(l.Status))
			for _, n := range l.Nodes {
				if n.Status == "running" || n.Status == "waiting_approval" || n.Status == "failed" {
					fmt.Fprintf(&b, "，「%s」%s", n.Name, laneStatusText(n.Status))
				}
			}
			b.WriteString("\n")
		}
	}
	history := h.repo.TimelineMessages(room.ID, msg.ThreadID, 30)
	b.WriteString("\n最近的对话：\n")
	for _, m := range history {
		if m.ID == msg.ID || (m.Kind != "text" && m.Kind != domain.ChatKindAI && m.Kind != domain.ChatKindMilestone && m.Kind != domain.ChatKindDecision) {
			continue
		}
		if m.Kind == domain.ChatKindAI && m.AIStatus != domain.AIStatusCompleted {
			continue
		}
		fmt.Fprintf(&b, "%s：%s%s\n", m.Author, roomTrim(m.Body, 600), attachmentNote(m.Attachments))
	}
	fmt.Fprintf(&b, "\n现在 %s 对你说：\n%s%s\n\n%s", msg.Author, msg.Body, attachmentNote(msg.Attachments), spaceDeliverablePrompt)
	return b.String()
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func laneStatusText(status string) string {
	switch status {
	case "running":
		return "运行中"
	case "waiting_approval":
		return "等待审批"
	case "succeeded":
		return "已完成"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "pending", "queued":
		return "排队中"
	}
	return status
}

// reconcileAI settles AI placeholders whose generation finished since the
// last read. The generation store stays the source of truth.
func (h *spacesHandlers) reconcileAI(room domain.ChatRoom, messages []domain.ChatMessage) []domain.ChatMessage {
	for i := range messages {
		m := &messages[i]
		if m.Kind != domain.ChatKindAI || m.AIStatus != domain.AIStatusRunning {
			continue
		}
		g, ok := h.repo.AssistantGeneration(m.AIRequesterID, m.AISessionID)
		switch {
		case !ok && time.Since(m.CreatedAt) > time.Minute:
			m.AIStatus, m.AIError = domain.AIStatusFailed, "没有找到这次 AI 回复的记录"
		case !ok:
			continue
		case g.Status == "completed":
			var reply assistantReply
			_ = json.Unmarshal(g.Reply, &reply)
			if len(reply.Drafts) > 0 {
				raw, _ := json.Marshal(struct {
					Message string           `json:"message"`
					Drafts  []assistantDraft `json:"drafts"`
				}{Message: reply.Message, Drafts: reply.Drafts})
				m.Body = string(raw)
			} else {
				m.Body = strings.TrimSpace(reply.Message)
			}
			if strings.TrimSpace(m.Body) == "" {
				m.Body = "（AI 没有给出文字回复）"
			}
			m.AIStatus, m.AIProgress = domain.AIStatusCompleted, ""
		case g.Status == "failed" || g.Status == "cancelled":
			m.AIStatus, m.AIError = domain.AIStatusFailed, fallbackName(g.Error, "AI 回复失败")
		case time.Since(m.CreatedAt) > spaceAITimeout:
			m.AIStatus, m.AIError = domain.AIStatusFailed, "AI 回复超时"
		default:
			m.AIProgress = fmt.Sprintf("已处理 %d 步", len(g.Events))
			continue
		}
		_ = h.repo.PutMessage(*m)
	}
	return messages
}

func attachmentNote(list []domain.AssistantAttachment) string {
	if len(list) == 0 {
		return ""
	}
	names := make([]string, len(list))
	for i, a := range list {
		names[i] = a.Name
	}
	return "（附件：" + strings.Join(names, "、") + "）"
}
