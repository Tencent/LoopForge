package httpapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func (h *assistantHandlers) listSessions(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	items, err := listAssistantConversations(h.repo, current.ID)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取对话失败"})
		return
	}
	seen := map[string]bool{}
	out := make([]assistantConversation, 0, len(items))
	for _, item := range append(items, h.repo.ListSharedAssistantSessions()...) {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		if item.Shared {
			if _, _, err := h.conversationScope(c, item.ID, "", "", current.ID); err != nil {
				continue
			}
		} else if item.RunID != "" {
			if _, ok := h.repo.SharedAssistantSession(repo.RunAssistantSessionID(item.RunID)); ok {
				continue
			}
		}
		item.GenerationStatus = "idle"
		out = append(out, item)
	}
	ids := make([]string, 0, len(out))
	for _, item := range out {
		ids = append(ids, item.ID)
	}
	statuses := h.repo.AssistantGenerationStatuses(ids)
	for i := range out {
		if status, ok := statuses[out[i].ID]; ok {
			out[i].GenerationStatus = status
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	c.JSON(200, out)
}

func (h *assistantHandlers) archiveSession(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	if _, ok := h.repo.SharedAssistantSession(c.Param("id")); ok {
		c.JSON(403, gin.H{"error": "共享 SOP 会话不能个人归档"})
		return
	}
	var body struct {
		Archived *bool `json:"archived"`
	}
	if !decode(c, &body) {
		return
	}
	if body.Archived == nil {
		c.JSON(400, gin.H{"error": "请提供归档状态"})
		return
	}
	if err := h.repo.SetAssistantSessionArchived(user.ID, c.Param("id"), *body.Archived); err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *assistantHandlers) session(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	ownerID, shared, err := h.conversationScope(c, id, "", "", current.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	switch c.Request.Method {
	case "GET":
		item, err := loadAssistantConversation(h.repo, ownerID, id)
		if err != nil {
			c.JSON(404, gin.H{"error": "对话不存在"})
			return
		}
		c.JSON(200, item)
	case "PUT":
		if job, ok := h.repo.AssistantGeneration(ownerID, id); ok && job.Status == "running" {
			c.JSON(409, gin.H{"error": "生成期间不能改写会话"})
			return
		}
		existing, _ := loadAssistantConversation(h.repo, ownerID, id)
		var conv assistantConversation
		if !decode(c, &conv) {
			return
		}
		conv.ID = assistantRecordID(id)
		conv.Shared = shared
		if shared {
			if err := sharedReceiptUpdate(existing, conv, current.ID, current.LoginName); err != nil {
				c.JSON(409, gin.H{"error": err.Error()})
				return
			}
			conv.RunID = existing.RunID
			conv.ProjectID = existing.ProjectID
			conv.Title = existing.Title
			conv.UpdatedAt = time.Time{}
		}
		conv.UpdatedAt = time.Time{}
		if conv.Title == "" {
			conv.Title = existing.Title
		}
		if conv.ProjectID == "" {
			conv.ProjectID = existing.ProjectID
		}
		if conv.RunID == "" {
			conv.RunID = existing.RunID
		}
		if conv.Executor == "" {
			conv.Executor = existing.Executor
		}
		if conv.Model == "" {
			conv.Model = existing.Model
		}
		var saveErr error
		if shared {
			saveErr = h.repo.PutSharedAssistantReceipt(ownerID, conv, existing)
		} else {
			saveErr = saveAssistantConversation(h.repo, ownerID, conv)
		}
		if saveErr != nil {
			c.JSON(500, gin.H{"error": "保存对话失败"})
			return
		}
		stored, err := loadAssistantConversation(h.repo, ownerID, conv.ID)
		if err != nil {
			c.JSON(500, gin.H{"error": "保存对话失败"})
			return
		}
		c.JSON(200, stored)
	case "DELETE":
		if shared {
			c.JSON(403, gin.H{"error": "共享 SOP 会话随运行保留，不能个人删除"})
			return
		}
		if job, ok := h.repo.AssistantGeneration(ownerID, id); ok && job.Status == "running" {
			c.JSON(409, gin.H{"error": "请先停止生成后删除会话"})
			return
		}
		if err := deleteAssistantConversation(h.repo, ownerID, id); err != nil {
			c.JSON(404, gin.H{"error": "对话不存在"})
			return
		}
		c.JSON(200, gin.H{"ok": true})
	default:
		c.JSON(405, gin.H{"error": "method not allowed"})
	}
}

func assistantSessionBase() string {
	if override := strings.TrimSpace(os.Getenv("AGENTFLOW_ASSISTANT_SESSION_DIR")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".agentflow", "assistant")
}

func assistantSessionRoot(userID int64) string {
	return filepath.Join(assistantSessionBase(), strconv.FormatInt(userID, 10))
}

func assistantCLIHome(userID int64) string {
	return filepath.Join(assistantSessionRoot(userID), "home")
}

func assistantCLIWorkspaceForUser(userID int64) string {
	return filepath.Join(assistantSessionRoot(userID), "ws")
}

func ensureAssistantSessionDirs(userID int64) error {
	for _, dir := range []string{assistantCLIHome(userID), filepath.Join(assistantCLIHome(userID), "tmp"), assistantCLIWorkspaceForUser(userID)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func newAssistantSessionID() string {
	return "af" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func assistantRecordID(id string) string {
	id = filepath.Base(strings.TrimSpace(id))
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return id
}

func saveAssistantConversation(st *repo.Store, userID int64, conv assistantConversation) error {
	if st == nil {
		return os.ErrInvalid
	}
	if conv.ID == "" {
		conv.ID = newAssistantSessionID()
	}
	if strings.TrimSpace(conv.Title) == "" {
		for _, m := range conv.Messages {
			if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
				conv.Title = strings.TrimSpace(m.Content)
				break
			}
		}
		if conv.Title == "" {
			conv.Title = "新对话"
		}
	}
	if runes := []rune(conv.Title); len(runes) > 36 {
		conv.Title = string(runes[:36]) + "…"
	}
	return st.PutAssistantSession(userID, conv)
}

func loadAssistantConversation(st *repo.Store, userID int64, id string) (assistantConversation, error) {
	if st == nil {
		return assistantConversation{}, os.ErrNotExist
	}
	item, ok := st.AssistantSession(userID, id)
	if !ok {
		return assistantConversation{}, os.ErrNotExist
	}
	return item, nil
}

func listAssistantConversations(st *repo.Store, userID int64) ([]assistantConversation, error) {
	if st == nil {
		return nil, os.ErrInvalid
	}
	return st.ListAssistantSessions(userID), nil
}

func deleteAssistantConversation(st *repo.Store, userID int64, id string) error {
	id = assistantRecordID(id)
	if st == nil || id == "" {
		return os.ErrNotExist
	}
	err := st.DeleteAssistantSession(userID, id)
	deleteAssistantCLISession(userID, id)
	if err != nil {
		return os.ErrNotExist
	}
	return nil
}

func deleteAssistantCLISession(userID int64, id string) {
	if id == "" || id == "." || id == ".." {
		return
	}
	for _, root := range []string{
		filepath.Join(assistantCLIHome(userID), ".codebuddy", "projects"),
		filepath.Join(assistantCLIHome(userID), ".codebuddy", "sessions"),
	} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				return nil
			}
			if path == root {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !assistantCLISessionName(d.Name(), id) {
				return nil
			}
			_ = os.RemoveAll(path)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		})
	}
}

func assistantCLISessionName(name, id string) bool {
	return name == id || strings.HasPrefix(name, id+".")
}
