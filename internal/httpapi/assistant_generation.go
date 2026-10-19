package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
	"sync"
	"time"
)

type assistantGenerationSinkKey struct{}
type assistantFreshCLIKey struct{}

func (h *assistantHandlers) startGeneration(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	user, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(400, gin.H{"error": "请先配置个人 CodeBuddy API Key"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
	var req assistantRequest
	if !decode(c, &req) {
		return
	}
	if err := validateAssistantProvider(&req, user); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
		c.JSON(400, gin.H{"error": "请提供用户消息"})
		return
	}
	for i := range req.Messages {
		req.Messages[i].Process = domain.SanitizeAssistantProcess(req.Messages[i].Process)
	}
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" {
			c.JSON(400, gin.H{"error": "消息无效"})
			return
		}
	}
	for _, message := range req.Messages {
		if message.Role == "user" && (message.Trigger != "" || message.TriggerID != "") {
			c.JSON(400, gin.H{"error": "不能伪造系统触发标记"})
			return
		}
	}
	if req.SessionID == "" {
		req.SessionID = newAssistantSessionID()
	}
	if assistantRecordID(req.SessionID) != req.SessionID || len(req.SessionID) > 64 {
		c.JSON(400, gin.H{"error": "会话 ID 无效"})
		return
	}
	ownerID, shared, err := h.conversationScope(c, req.SessionID, req.RunID, req.ProjectID, user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	if err := h.validateAssistantTriggerHistory(ownerID, req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if shared {
		if old, ok := h.repo.AssistantSession(ownerID, req.SessionID); ok {
			if !sharedTranscriptPrefix(old.Messages, req.Messages) {
				c.JSON(409, gin.H{"error": "共享会话已更新，请重新打开后发送"})
				return
			}
			for i := len(old.Messages); i < len(req.Messages); i++ {
				if req.Messages[i].Role != "user" {
					c.JSON(400, gin.H{"error": "不能伪造共享回复"})
					return
				}
				if req.Messages[i].CreatedAt.IsZero() {
					req.Messages[i].CreatedAt = time.Now().UTC()
				}
				req.Messages[i].AuthorID = user.ID
				req.Messages[i].AuthorName = user.LoginName
				req.Messages[i].SpeakerName = ""
				req.Messages[i].SpeakerAgentID = ""
			}
		}
	}
	if err := h.prepareWorkbenchContext(c, &req, user); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	freshCLI := false
	if old, ok := h.repo.AssistantSession(ownerID, req.SessionID); ok {
		freshCLI = assistantExecutor(old.Executor) != req.Executor
	}
	if old, ok := h.repo.AssistantSession(ownerID, req.SessionID); ok {
		if len(req.Messages) < len(old.Messages) {
			c.JSON(409, gin.H{"error": "会话已更新，请重新打开后发送"})
			return
		}
		for i, m := range old.Messages {
			a, _ := json.Marshal(m)
			b, _ := json.Marshal(req.Messages[i])
			if !bytes.Equal(a, b) {
				c.JSON(409, gin.H{"error": "会话已更新，请重新打开后发送"})
				return
			}
		}
	}
	old, _ := h.repo.AssistantSession(ownerID, req.SessionID)
	for i := len(old.Messages); i < len(req.Messages); i++ {
		if req.Messages[i].Role == "user" {
			req.Messages[i].AuthorID = user.ID
			req.Messages[i].AuthorName = user.LoginName
		}
	}
	g := domain.AssistantGeneration{StartedAt: time.Now().UTC(), ActorID: user.ID, ActorName: user.LoginName, ID: newAssistantSessionID(), SessionID: req.SessionID, UserID: ownerID, ProjectID: req.ProjectID, Status: "running", Events: []domain.AssistantGenerationEvent{}}
	pending := assistantConversation{ID: req.SessionID, Shared: shared, ProjectID: req.ProjectID, Model: req.Model, Executor: req.Executor, RunID: req.RunID, Messages: req.Messages}
	for _, message := range req.Messages {
		if message.Role == "user" {
			title := []rune(strings.TrimSpace(message.Content))
			if len(title) > 36 {
				title = append(title[:36], '…')
			}
			pending.Title = string(title)
			break
		}
	}
	if err := h.waitForAutomaticFollowUp(c.Request.Context(), ownerID, req.SessionID); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if req.RunID != "" && !req.consultation && h.engine != nil {
		token, err := h.engine.BeginConversationWorkspaceUse(req.RunID)
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		req.workspaceToken = token
	}
	var beginErr error
	if shared && req.groupMode {
		run, _ := h.repo.RunState(req.RunID)
		handled, _ := json.Marshal(groupCompletedKeys(run))
		beginErr = h.repo.BeginAuthorizedGroupGeneration(g, pending, repo.AssistantGroupFollowUp{RunID: req.RunID, SessionID: req.SessionID, ProjectID: req.ProjectID, OwnerID: ownerID, ActorID: user.ID, Model: req.Model, Handled: handled})
	} else {
		beginErr = h.repo.BeginAssistantGeneration(g, pending)
	}
	if err := beginErr; err != nil {
		if req.workspaceToken != "" {
			_ = h.engine.EndConversationWorkspaceUse(c.Request.Context(), req.RunID, req.workspaceToken)
		}
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	// Keep the authenticated identity, but disconnect model execution from the
	// lifetime of the browser request. Only explicit cancellation stops a turn.
	parent := context.WithValue(context.WithoutCancel(c.Request.Context()), assistantFreshCLIKey{}, freshCLI)
	go h.executeGeneration(parent, req, g, current)
	c.JSON(http.StatusAccepted, gin.H{"id": g.ID, "sessionId": g.SessionID, "status": g.Status})
}
func (h *assistantHandlers) generation(c *gin.Context) {
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
	g, ok := h.repo.AssistantGeneration(ownerID, c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "生成不存在"})
		return
	}
	if !h.workbenchProjectAllowed(c, g.ProjectID, user.ID) {
		c.JSON(403, gin.H{"error": "无权访问项目"})
		return
	}
	if c.Request.Method == "POST" {
		if g.Trigger != "group_followup" && g.ActorID != 0 && g.ActorID != user.ID {
			c.JSON(403, gin.H{"error": "只有本轮发起人可以停止回复"})
			return
		}
		if err := h.repo.CancelAssistantGeneration(ownerID, g.SessionID, c.Query("generationId")); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"ok": true})
		return
	}
	after := parseQueryInt64(c.Request, "after")
	events := make([]domain.AssistantGenerationEvent, 0)
	for _, ev := range g.Events {
		if int64(ev.Sequence) > after {
			events = append(events, ev)
		}
	}
	g.Events = events
	c.Header("Cache-Control", "no-store")
	c.JSON(200, g)
}

type generationHTTPWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *generationHTTPWriter) Header() http.Header  { return w.header }
func (w *generationHTTPWriter) WriteHeader(code int) { w.status = code }
func (w *generationHTTPWriter) Write(p []byte) (int, error) {
	if w.body.Len() < 16384 {
		_, _ = w.body.Write(p)
	}
	return len(p), nil
}
func (w *generationHTTPWriter) Flush() {}

func (h *assistantHandlers) executeGeneration(parent context.Context, req assistantRequest, g domain.AssistantGeneration, actor *auth.User) {
	ctx, cancel := context.WithTimeout(parent, assistantChatTimeout)
	defer cancel()
	var mu sync.Mutex
	sequence := 0
	eventBytes := 0
	sink := func(kind string, value any) {
		mu.Lock()
		defer mu.Unlock()
		raw, _ := json.Marshal(value)
		switch kind {
		case "thought":
			if len(raw) > 256<<10 {
				return
			}
			sequence++
			eventBytes += len(raw)
			g.Events = append(g.Events, domain.AssistantGenerationEvent{Sequence: sequence, Data: raw})
			for len(g.Events) > 500 || eventBytes > 2<<20 {
				eventBytes -= len(g.Events[0].Data)
				g.Events = g.Events[1:]
			}
		case "result":
			g.Reply = raw
		case "error":
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(raw, &e)
			g.Error = e.Error
		}
	}
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				if req.followUp != nil {
					if h.repo.RunControlRequested(req.RunID) {
						cancel()
					}
					revision, exists := h.repo.RunRevision(req.RunID)
					if !exists {
						cancel()
					} else if revision != req.followUp.RunRevision {
						run, exists := h.repo.RunState(req.RunID)
						if !exists || run.WorkflowSnapshot.CollaborationMode != "group" || domain.TerminalRunStatus(run.Status) || run.PauseRequested || run.CancelRequested {
							cancel()
						}
						// Revisions are reread after changes; the immutable trigger is shared with chat.
					}
				}
				if req.workspaceToken != "" {
					if err := h.engine.RenewConversationWorkspaceUse(req.RunID, req.workspaceToken); err != nil {
						cancel()
					}
				}
				old, ok := h.repo.AssistantGeneration(g.UserID, g.SessionID)
				if !ok || old.ID != g.ID || old.Status != "running" || old.CancelRequested {
					cancel()
				}
				mu.Lock()
				copy := g
				copy.Events = append([]domain.AssistantGenerationEvent(nil), g.Events...)
				mu.Unlock()
				if err := h.repo.CheckpointAssistantGeneration(copy); err != nil {
					cancel()
				}
			}
		}
	}()
	writer := &generationHTTPWriter{header: make(http.Header), status: 200}
	func() {
		defer func() {
			if recover() != nil {
				sink("error", gin.H{"error": "生成异常中断，请重试"})
			}
		}()
		body, _ := json.Marshal(req)
		chatCtx := context.WithValue(ctx, assistantGenerationSinkKey{}, sink)
		chatCtx = engine.WithConversationWorkspaceToken(chatCtx, req.workspaceToken, req.RunID)
		if req.followUp != nil {
			chatCtx = context.WithValue(chatCtx, assistantGroupFollowUpKey{}, req.followUp)
		}
		if actor != nil {
			chatCtx = context.WithValue(chatCtx, requestUserContextKey{}, actor)
		}
		request, _ := http.NewRequestWithContext(chatCtx, "POST", "/api/assistant/chat", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router := gin.New()
		router.POST("/api/assistant/chat", h.chat)
		router.ServeHTTP(writer, request)
	}()
	close(stopped)
	<-done
	if req.workspaceToken != "" {
		saveCtx, stop := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
		if err := h.engine.EndConversationWorkspaceUse(saveCtx, req.RunID, req.workspaceToken); err != nil {
			g.WorkspaceWarning = "工作区保存未完成，暂缓回收：" + err.Error()
		}
		stop()
	}
	if writer.status >= 400 && g.Error == "" {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(writer.body.Bytes(), &result)
		g.Error = result.Error
	}
	if len(g.Reply) > 0 {
		g.Status = "completed"
	} else {
		g.Status = "failed"
		if g.Error == "" {
			g.Error = "生成未完成，请重试"
		}
		if ctx.Err() != nil {
			g.Status = "cancelled"
			g.Error = "生成已停止"
		}
	}
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		fmt.Printf("assistant generation checkpoint failed: %T\n", err)
	}
}
