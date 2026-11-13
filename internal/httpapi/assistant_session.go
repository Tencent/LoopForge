package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// readOnlyConversation marks a project conversation opened by a non-member,
// so the client hides the composer and other write actions. ProjectName is
// included because the viewer's project list does not contain the project.
type readOnlyConversation struct {
	domain.AssistantSession
	ReadOnly    bool   `json:"readOnly"`
	ProjectName string `json:"projectName,omitempty"`
}

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
	shared, projectChats := h.repo.ListCrossUserAssistantSessions(current.ID)
	sharedIDs := make(map[string]bool, len(shared))
	runIDs, projectIDs := []string{}, []string{}
	seenRuns, seenProjects := map[string]bool{}, map[string]bool{}
	addProject := func(id string) {
		if id != "" && !seenProjects[id] {
			seenProjects[id] = true
			projectIDs = append(projectIDs, id)
		}
	}
	for _, item := range shared {
		sharedIDs[item.ID] = true
		if !seenRuns[item.RunID] {
			seenRuns[item.RunID] = true
			runIDs = append(runIDs, item.RunID)
		}
		addProject(item.ProjectID)
	}
	for _, item := range projectChats {
		addProject(item.ProjectID)
	}
	runProjects, err := h.repo.RunProjectIDs(runIDs)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取对话失败"})
		return
	}
	projects, err := h.repo.ProjectMemberships(projectIDs)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取对话失败"})
		return
	}
	allowedProjects := make(map[string]bool, len(projects))
	if len(projects) > 0 {
		platformAdmin := h.isPlatformAdmin(c.Request)
		for id, project := range projects {
			allowedProjects[id] = workbenchProjectAllowsUser(project, current.ID, platformAdmin)
		}
	}
	out := visibleAssistantConversations(append(append(items, shared...), projectChats...), current.ID, sharedIDs, runProjects, allowedProjects)
	ids := make([]string, 0, len(out))
	for _, item := range out {
		ids = append(ids, item.ID)
	}
	statuses := h.repo.AssistantGenerationStatuses(current.ID, ids)
	for i := range out {
		out[i].GenerationStatus = "idle"
		out[i].GenerationID = ""
		out[i].Unread = false
		if state, ok := statuses[out[i].ID]; ok {
			out[i].GenerationStatus = state.Status
			out[i].GenerationID = state.ID
			// A reply that completed while the reader was elsewhere stays
			// unread until they open the conversation (Codex's blue dot).
			out[i].Unread = state.Status == "completed" && state.FinishedAt != nil && !state.Seen &&
				assistantSessionIncludesUser(out[i], current.ID)
		}
	}
	if issueID := c.GetString("assistantIssueID"); issueID != "" {
		filtered := make([]assistantConversation, 0)
		for _, item := range out {
			linked := item.IssueID == issueID
			if item.Shared && item.RunID != "" {
				if run, ok := h.repo.RunState(item.RunID); ok {
					linked = run.ExecutionContext.IssueID == issueID
				}
			}
			if linked {
				filtered = append(filtered, item)
			}
		}
		out = filtered
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	c.JSON(200, decorateRunSessions(h.repo, out))
}

// visibleAssistantConversations deduplicates candidates by ID (keeping the
// first occurrence) and drops the ones userID may not see in the session list.
// sharedIDs holds the IDs of cross-user shared records, runProjects maps a run
// to its owning project and allowedProjects records project access.
func visibleAssistantConversations(candidates []assistantConversation, userID int64, sharedIDs map[string]bool, runProjects map[string]string, allowedProjects map[string]bool) []assistantConversation {
	seen := map[string]bool{}
	out := make([]assistantConversation, 0, len(candidates))
	for _, item := range candidates {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		if item.Shared {
			// As in conversationScope, only valid shared records use project
			// authorization; other owned rows retain the private-session fallback.
			if sharedIDs[item.ID] && (item.ProjectID == "" || runProjects[item.RunID] != item.ProjectID || !allowedProjects[item.ProjectID]) {
				continue
			}
		} else if item.RunID != "" {
			if sharedIDs[repo.RunAssistantSessionID(item.RunID)] {
				continue
			}
		} else if item.UserID != userID && !allowedProjects[item.ProjectID] {
			// Other members' project conversations require current membership.
			continue
		}
		out = append(out, item)
	}
	return out
}

func (h *assistantHandlers) archiveSession(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	ownerID, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
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
	if err := h.repo.SetAssistantSessionArchived(ownerID, c.Param("id"), *body.Archived); err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// markSessionRead records the reply the reader has seen in a conversation, so
// the history list stops marking it unread on every device. An older reply
// is ignored: only the latest one carries a mark.
func (h *assistantHandlers) markSessionRead(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	ownerID, _, _, err := h.conversationReadScope(c, id, current.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	var body struct {
		GenerationID string `json:"generationId"`
	}
	if !decode(c, &body) {
		return
	}
	body.GenerationID = strings.TrimSpace(body.GenerationID)
	if body.GenerationID == "" || len(body.GenerationID) > 64 {
		c.JSON(400, gin.H{"error": "请提供回复 ID"})
		return
	}
	recorded, err := h.repo.MarkAssistantSessionRead(current.ID, ownerID, id, body.GenerationID)
	if errors.Is(err, repo.ErrAssistantReplyNotFound) {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "记录已读失败"})
		return
	}
	c.JSON(200, gin.H{"ok": true, "recorded": recorded})
}

// assistantSessionIncludesUser matches the sidebar's "仅我参与的对话": the
// people a transcript names, or its owner while it names nobody. A
// colleague's conversation the reader never joined is not news to them.
func assistantSessionIncludesUser(item domain.AssistantSession, userID int64) bool {
	if len(item.Participants) == 0 {
		return item.UserID == userID
	}
	for _, participant := range item.Participants {
		if participant.ID == userID {
			return true
		}
	}
	return false
}

func (h *assistantHandlers) session(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	var ownerID int64
	var shared, readOnly bool
	var err error
	if c.Request.Method == "GET" {
		ownerID, shared, readOnly, err = h.conversationReadScope(c, id, current.ID)
	} else {
		ownerID, shared, err = h.conversationScope(c, id, "", "", current.ID)
	}
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
		if readOnly {
			project, _ := h.repo.Project(item.ProjectID)
			c.JSON(200, readOnlyConversation{AssistantSession: item, ReadOnly: true, ProjectName: project.Name})
			return
		}
		if !item.Shared && !readOnly {
			if job, ok := h.repo.AssistantGeneration(ownerID, id); !ok || job.Status != "running" {
				if materializeWorkspaceImages(ownerID, &item) {
					if err := saveAssistantConversation(h.repo, ownerID, item); err == nil {
						if stored, err := loadAssistantConversation(h.repo, ownerID, id); err == nil {
							item = stored
						}
					}
				}
			}
		}
		if shared {
			if run, ok := h.repo.RunState(item.RunID); ok {
				item.IssueID = run.ExecutionContext.IssueID
			}
		}
		c.JSON(200, decorateRunSessionView(h.repo, item))
	case "PUT":
		if job, ok := h.repo.AssistantGeneration(ownerID, id); ok && job.Status == "running" {
			c.JSON(409, gin.H{"error": "生成期间不能改写会话"})
			return
		}
		existing, _ := loadAssistantConversation(h.repo, ownerID, id)
		var body struct {
			assistantConversation
			Effort *string `json:"effort"`
		}
		if !decode(c, &body) {
			return
		}
		conv := body.assistantConversation
		conv.RuntimeMode = existing.Mode()
		conv.ExecutionTargetLocked = existing.ExecutionLocked()
		if existing.ID != "" && existing.ExecutionLocked() {
			if conv.ExecutionTarget != nil && *conv.ExecutionTarget != existing.Execution() {
				c.JSON(409, gin.H{"error": repo.ErrAssistantExecutionLocked.Error()})
				return
			}
			conv.ExecutionTarget = existing.ExecutionTarget
		} else {
			var targetErr error
			conv.ExecutionTarget, targetErr = h.newExecutionTarget(current.ID, conv.ProjectID, draftExecutionTarget(existing, conv.ExecutionTarget))
			if targetErr != nil {
				c.JSON(400, gin.H{"error": targetErr.Error()})
				return
			}
		}
		// Origin markers are server-owned: a transcript update cannot relabel a
		// scheduled conversation as chat (or the other way around).
		conv.Source = existing.Source
		conv.TaskID = existing.TaskID
		// Bindings and existing environment choices are server-owned. This general
		// transcript endpoint cannot swap a workspace or bypass the sandbox setter.
		conv.WorkspaceRunID = existing.WorkspaceRunID
		if existing.ID != "" {
			conv.SandboxProfileID = existing.SandboxProfileID
		}
		if existing.ID == "" && conv.SandboxProfileID != "" {
			if _, err := h.repo.ConversationSandbox(conv.ProjectID, conv.SandboxProfileID); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
		}
		conv.Effort = existing.Effort
		if body.Effort != nil {
			conv.Effort = *body.Effort
		}
		if existing.ID != "" && conv.Title != "" && conv.Title != existing.Title {
			// A client that still has the first-prompt title must not undo the
			// summary or mark the session as renamed by the user.
			if existing.TitleGenerated && assistantPromptTitleMatches(existing, conv.Title) {
				conv.Title = existing.Title
				conv.TitleGenerated = true
			} else {
				conv.TitleEdited = true
			}
		}
		conv.ID = assistantRecordID(id)
		conv.Shared = existing.Shared
		if !existing.Shared && conv.ProjectID != "" {
			if !h.workbenchProjectAllowed(c, conv.ProjectID, current.ID) {
				c.JSON(403, gin.H{"error": "无权绑定此项目"})
				return
			}
			if existing.ProjectID != "" && existing.ProjectID != conv.ProjectID {
				c.JSON(409, gin.H{"error": "会话已绑定其他项目，请新建会话或创建分支"})
				return
			}
		}
		if shared {
			if err := sharedReceiptUpdate(existing, conv, current.ID, current.LoginName); err != nil {
				c.JSON(409, gin.H{"error": err.Error()})
				return
			}
			conv.RunID = existing.RunID
			conv.ProjectID = existing.ProjectID
			if existing.Shared {
				conv.Title = existing.Title
			}
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
		if existing.ID != "" {
			conv.IssueID = existing.IssueID
		}
		if err := h.validateSessionIssue(c, conv.ProjectID, conv.IssueID, current.ID); err != nil {
			c.JSON(403, gin.H{"error": err.Error()})
			return
		}
		// Existing author identities are immutable on receipt/title updates.
		for i := range conv.Messages {
			if i < len(existing.Messages) {
				conv.Messages[i].AuthorID = existing.Messages[i].AuthorID
				conv.Messages[i].AuthorName = existing.Messages[i].AuthorName
			} else if conv.Messages[i].Role == "user" {
				conv.Messages[i].AuthorID = current.ID
				conv.Messages[i].AuthorName = current.LoginName
			}
		}
		if conv.Model == "" {
			conv.Model = existing.Model
		}
		editor, _ := h.repo.User(current.ID)
		if err := validateAssistantEffort(editor, conv.Model, conv.Effort); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		var saveErr error
		if shared {
			saveErr = h.repo.PutSharedAssistantReceipt(ownerID, conv, existing)
		} else {
			saveErr = saveAssistantConversation(h.repo, ownerID, conv)
		}
		if saveErr != nil {
			if errors.Is(saveErr, repo.ErrAssistantExecutionLocked) {
				c.JSON(409, gin.H{"error": saveErr.Error()})
				return
			}
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
			_, project := h.repo.ProjectAssistantSession(id)
			if !project {
				c.JSON(403, gin.H{"error": "共享 SOP 会话随运行保留，不能个人删除"})
				return
			}
		}
		if job, ok := h.repo.AssistantGeneration(ownerID, id); ok && job.Status == "running" {
			c.JSON(409, gin.H{"error": "请先停止生成后删除会话"})
			return
		}
		if err := h.deleteConversation(ownerID, id); err != nil {
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

// assistantConversationBase holds the native conversation roots. They are
// bind-mounted into Docker workspaces, so a platform that reaches the daemon
// through docker.sock must keep them at a path the daemon's host has too.
func assistantConversationBase() string {
	if override := strings.TrimSpace(os.Getenv("AGENTFLOW_CONVERSATION_DIR")); override != "" {
		return override
	}
	return assistantSessionBase()
}

// assistantConversationRoot is the CLI root (home and ws) of one personal
// conversation. It is keyed by owner and session, so a reused session ID can
// never reach another user's native state.
func assistantConversationRoot(userID int64, sessionID string) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(userID, 10) + "/" + sessionID))
	return filepath.Join(assistantConversationBase(), "conversations", hex.EncodeToString(sum[:]))
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
	// Session ids are unique across owners, so once the owner's row is gone no
	// participant can open the shared root again.
	_ = os.RemoveAll(assistantSharedSessionRoot(id))
	return nil
}

func deleteAssistantCLISession(userID int64, id string) {
	if id == "" || id == "." || id == ".." {
		return
	}
	_ = os.RemoveAll(assistantConversationRoot(userID, id))
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
