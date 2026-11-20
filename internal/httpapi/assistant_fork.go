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
	owner, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	source, exists := h.repo.AssistantSession(owner, c.Param("id"))
	if !exists {
		c.JSON(404, gin.H{"error": "原会话不存在"})
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
	if job, ok := h.readGeneration(owner, source.ID); ok && job.Status == "running" {
		c.JSON(409, gin.H{"error": "请先停止原会话的生成，再创建分支"})
		return
	}
	index := *body.MessageIndex
	edited := source.Messages[index]
	edited.Attachments = append([]domain.AssistantAttachment(nil), edited.Attachments...)
	edited.Content = strings.TrimSpace(body.Content)
	edited.AuthorID = user.ID
	edited.AuthorName = user.LoginName
	edited.CreatedAt = time.Now().UTC()
	edited.SpeakerName = ""
	edited.SpeakerAgentID = ""
	edited.Model = ""
	edited.Usage = nil
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
	executor := h.forkExecutor(c, source, user.ID)
	if executor == "" && source.Model != "" {
		executor, _ = assistantModelEngine(source.Model)
	}
	branch := domain.AssistantSession{ID: newAssistantSessionID(), UserID: user.ID, ProjectID: source.ProjectID, IssueID: source.IssueID, Model: source.Model, Effort: source.Effort, Executor: executor, Messages: append(prefix, edited), Shared: source.Shared, RunID: source.RunID, ParentSessionID: source.ID, RootSessionID: source.RootSessionID, ForkMessageIndex: index, Title: "分支 · " + truncateRunes(edited.Content, 28)}
	if branch.RootSessionID == "" {
		branch.RootSessionID = source.ID
	}
	if source.Shared {
		branch.ID = "sop-branch-" + branch.ID
	}
	if err := h.repo.CreateAssistantSessionBranch(source, branch); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	saved, _ := h.repo.AssistantSession(user.ID, branch.ID)
	c.JSON(201, saved)
}

// Older forks stored the model and transcript without the source executor.
// A readable ancestor can supply it if it still describes the forked turn.
func (h *assistantHandlers) forkExecutor(c *gin.Context, session domain.AssistantSession, userID int64) string {
	if session.Executor != "" || session.ParentSessionID == "" {
		return session.Executor
	}
	if assistantModelHasEnginePrefix(session.Model) {
		engine, _ := assistantModelEngine(session.Model)
		return engine
	}
	for _, id := range []string{session.ParentSessionID, session.RootSessionID} {
		if id == "" || id == session.ID {
			continue
		}
		owner, _, _, err := h.conversationReadScope(c, id, userID)
		if err != nil {
			continue
		}
		parent, ok := h.repo.AssistantSession(owner, id)
		if !ok || parent.ProjectID != session.ProjectID || parent.RunID != session.RunID || parent.Shared != session.Shared {
			continue
		}
		if !matchesForkHistory(session, parent) {
			continue
		}
		if parent.Executor != "" {
			return parent.Executor
		}
		if assistantModelHasEnginePrefix(parent.Model) {
			engine, _ := assistantModelEngine(parent.Model)
			return engine
		}
	}
	return ""
}

func matchesForkHistory(fork, parent domain.AssistantSession) bool {
	if parent.Model == fork.Model && !fork.CreatedAt.IsZero() && !parent.UpdatedAt.After(fork.CreatedAt) {
		return true
	}
	// A parent can change CLI after an edit removes its earlier replies.
	// The latest inherited reply identifies the CLI's surviving history.
	for i := min(fork.ForkMessageIndex, len(fork.Messages)) - 1; i >= 0; i-- {
		message := fork.Messages[i]
		if message.Role != "assistant" || message.SpeakerAgentID != "" || message.Model == "" {
			continue
		}
		if i >= len(parent.Messages) {
			return false
		}
		other := parent.Messages[i]
		if other.Role != message.Role || other.SpeakerAgentID != "" || other.Model != message.Model {
			return false
		}
		if message.GenerationID != "" {
			return message.GenerationID == other.GenerationID
		}
		return !message.CreatedAt.IsZero() && message.CreatedAt.Equal(other.CreatedAt)
	}
	return false
}

func copyForkAttachment(from, to int64, id, project string) (domain.AssistantAttachment, error) {
	record, file, err := openWorkbenchAttachment(from, id)
	if err != nil {
		return domain.AssistantAttachment{}, err
	}
	defer file.Close()
	if record.ProjectID != project {
		return domain.AssistantAttachment{}, os.ErrNotExist
	}
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
	edited.CreatedAt = time.Now().UTC()
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
	// Resolve a legacy branch's executor while its inherited replies are still
	// available. A replacement can remove the reply that identifies its CLI.
	source.Executor = h.forkExecutor(c, source, user.ID)
	var target []domain.AssistantSession
	resultOwner, resultID := owner, source.ID
	if source.Shared && source.ParentSessionID != "" {
		main, ok := h.repo.SharedAssistantSession(source.RootSessionID)
		if !ok {
			c.JSON(409, gin.H{"error": "主讨论不存在"})
			return
		}
		// The copied prefix has to be resumable using the main conversation's
		// CLI. Refuse an incompatible edit before replacing its transcript.
		inherited := assistantConversationEngine(source.Messages[:*body.MessageIndex], source.Executor)
		mainExecutor := h.forkExecutor(c, main, user.ID)
		mainEngine := assistantConversationEngine(main.Messages, mainExecutor)
		if mainEngine == "" && (mainExecutor != "" || main.Model != "") {
			mainEngine, _ = assistantModelEngine(assistantRequestModel(mainExecutor, main.Model))
		}
		if inherited != "" && mainEngine != "" && inherited != mainEngine {
			c.JSON(409, gin.H{"error": "分支与主讨论使用不同的 CLI，无法将分支消息编辑到主讨论"})
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
	saved.Executor = h.forkExecutor(c, saved, user.ID)
	c.JSON(200, saved)
}
