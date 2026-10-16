package httpapi

import (
	"agentflow/internal/domain"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (h *assistantHandlers) forkSession(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	owner, shared, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	source, exists := h.repo.AssistantSession(owner, c.Param("id"))
	if !exists {
		c.JSON(404, gin.H{"error": "原会话不存在"})
		return
	}
	if !h.workbenchProjectAllowed(c, source.ProjectID, user.ID) {
		c.JSON(403, gin.H{"error": "无权访问项目"})
		return
	}
	var body struct {
		MessageIndex *int      `json:"messageIndex"`
		Content      string    `json:"content"`
		UpdatedAt    time.Time `json:"updatedAt"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	if !decode(c, &body) {
		return
	}
	if body.MessageIndex == nil || *body.MessageIndex < 0 || *body.MessageIndex >= len(source.Messages) || source.Messages[*body.MessageIndex].Role != "user" || strings.TrimSpace(body.Content) == "" || len(body.Content) > 64<<10 {
		c.JSON(400, gin.H{"error": "请选择用户消息并填写内容（最多 64 KB）"})
		return
	}
	if !body.UpdatedAt.Equal(source.UpdatedAt) {
		c.JSON(409, gin.H{"error": "原会话已更新，请重新打开后编辑"})
		return
	}
	// Recover stale jobs before the transactional active-generation check.
	if job, ok := h.repo.AssistantGeneration(owner, source.ID); ok && job.Status == "running" {
		c.JSON(409, gin.H{"error": "请先停止原会话的生成，再创建分支"})
		return
	}
	index := *body.MessageIndex
	edited := source.Messages[index]
	edited.Attachments = append([]domain.AssistantAttachment(nil), edited.Attachments...)
	edited.Content = strings.TrimSpace(body.Content)
	edited.AuthorID = user.ID
	edited.AuthorName = user.LoginName
	edited.SpeakerName = ""
	edited.SpeakerAgentID = ""
	for i, a := range edited.Attachments {
		attachmentOwner := source.Messages[index].AuthorID
		if attachmentOwner == 0 {
			attachmentOwner = owner
		}
		if attachmentOwner != user.ID {
			copied, err := copyForkAttachment(attachmentOwner, user.ID, a.ID, source.ProjectID)
			if err != nil {
				c.JSON(400, gin.H{"error": "无法复制原消息附件"})
				return
			}
			edited.Attachments[i] = copied
		}
	}
	// Inherited drafts are history only, never approval buttons in the new branch.
	prefix := append([]domain.AssistantMessage(nil), source.Messages[:index]...)
	for i, m := range prefix {
		if m.Role != "assistant" {
			continue
		}
		var value map[string]any
		if json.Unmarshal([]byte(m.Content), &value) != nil {
			continue
		}
		drafts, _ := value["drafts"].([]any)
		for _, d := range drafts {
			if v, ok := d.(map[string]any); ok {
				v["inherited"] = true
			}
		}
		if len(drafts) > 0 {
			raw, _ := json.Marshal(value)
			prefix[i].Content = string(raw)
		}
	}
	branch := domain.AssistantSession{ID: newAssistantSessionID(), UserID: user.ID, ProjectID: source.ProjectID, Model: source.Model, Messages: append(prefix, edited), Shared: shared, RunID: source.RunID, ParentSessionID: source.ID, RootSessionID: source.RootSessionID, ForkMessageIndex: index, Title: "分支 · " + truncateRunes(edited.Content, 28)}
	if branch.RootSessionID == "" {
		branch.RootSessionID = source.ID
	}
	if shared {
		branch.ID = "sop-branch-" + branch.ID
	}
	if err := h.repo.CreateAssistantSessionBranch(source, branch); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	saved, _ := h.repo.AssistantSession(user.ID, branch.ID)
	c.JSON(201, saved)
}
func copyForkAttachment(from, to int64, id, project string) (domain.AssistantAttachment, error) {
	record, path, err := loadWorkbenchAttachment(from, id)
	if err != nil || record.ProjectID != project {
		return domain.AssistantAttachment{}, os.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return domain.AssistantAttachment{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, workbenchAttachmentLimit+1))
	if err != nil || len(data) > workbenchAttachmentLimit {
		return domain.AssistantAttachment{}, os.ErrInvalid
	}
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return domain.AssistantAttachment{}, err
	}
	record.ID = hex.EncodeToString(token)
	dir := workbenchAttachmentDir(to)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return domain.AssistantAttachment{}, err
	}
	if err = os.WriteFile(filepath.Join(dir, record.ID+".bin"), data, 0600); err != nil {
		return domain.AssistantAttachment{}, err
	}
	raw, _ := json.Marshal(record)
	if err = os.WriteFile(filepath.Join(dir, record.ID+".json"), raw, 0600); err != nil {
		return domain.AssistantAttachment{}, err
	}
	return record.AssistantAttachment, nil
}

// editSession replaces the main transcript from the edited user turn onward.
// Executed effects and their idempotency receipts remain in their own stores.
func (h *assistantHandlers) editSession(c *gin.Context) {
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
	source, ok := h.repo.AssistantSession(owner, c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "会话不存在"})
		return
	}
	if !h.workbenchProjectAllowed(c, source.ProjectID, user.ID) {
		c.JSON(403, gin.H{"error": "无权访问项目"})
		return
	}
	var body struct {
		MessageIndex *int      `json:"messageIndex"`
		Content      string    `json:"content"`
		UpdatedAt    time.Time `json:"updatedAt"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	if !decode(c, &body) {
		return
	}
	if body.MessageIndex == nil || *body.MessageIndex < 0 || *body.MessageIndex >= len(source.Messages) || source.Messages[*body.MessageIndex].Role != "user" || strings.TrimSpace(body.Content) == "" || len(body.Content) > 64<<10 {
		c.JSON(400, gin.H{"error": "请选择用户消息并填写内容"})
		return
	}
	if !body.UpdatedAt.Equal(source.UpdatedAt) {
		c.JSON(409, gin.H{"error": "会话已更新，请重新打开后编辑"})
		return
	}
	edited := source.Messages[*body.MessageIndex]
	edited.Content = strings.TrimSpace(body.Content)
	edited.AuthorID = user.ID
	edited.AuthorName = user.LoginName
	edited.Attachments = append([]domain.AssistantAttachment(nil), edited.Attachments...)
	for i, a := range edited.Attachments {
		attachmentOwner := source.Messages[*body.MessageIndex].AuthorID
		if attachmentOwner == 0 {
			attachmentOwner = owner
		}
		if attachmentOwner != user.ID {
			copy, err := copyForkAttachment(attachmentOwner, user.ID, a.ID, source.ProjectID)
			if err != nil {
				c.JSON(400, gin.H{"error": "无法复制消息附件"})
				return
			}
			edited.Attachments[i] = copy
		}
	}
	var target []domain.AssistantSession
	resultOwner, resultID := owner, source.ID
	if source.Shared && source.ParentSessionID != "" {
		main, ok := h.repo.SharedAssistantSession(source.RootSessionID)
		if !ok {
			c.JSON(409, gin.H{"error": "主讨论不存在"})
			return
		}
		target = append(target, main)
		resultOwner, resultID = main.UserID, main.ID
	}
	if err := h.repo.ReplaceAssistantTurn(source, *body.MessageIndex, edited, target...); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	saved, _ := h.repo.AssistantSession(resultOwner, resultID)
	c.JSON(200, saved)
}
