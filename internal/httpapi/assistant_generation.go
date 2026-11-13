package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"

	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
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
	stored, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(400, gin.H{"error": "请先配置个人 CodeBuddy API Key"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)
	var req assistantRequest
	if !decode(c, &req) {
		return
	}
	req.Model = assistantRequestModel(req.Executor, req.Model)
	user, model, refusal := h.assistantTurnUser(stored, req.Model)
	if refusal != "" {
		c.JSON(400, gin.H{"error": refusal})
		return
	}
	req.Model = model
	cliEngine, _ := assistantModelEngine(req.Model)
	req.Executor = cliEngine
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
		c.JSON(400, gin.H{"error": "请提供用户消息"})
		return
	}
	for _, m := range req.Messages {
		if (m.Role != "user" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" {
			c.JSON(400, gin.H{"error": "消息无效"})
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
				req.Messages[i].AuthorID = user.ID
				req.Messages[i].AuthorName = user.LoginName
				req.Messages[i].SpeakerName = ""
				req.Messages[i].SpeakerAgentID = ""
				req.Messages[i].Model = ""
				req.Messages[i].Usage = nil
			}
		}
	}
	if err := h.prepareWorkbenchContext(c, &req, user); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	catalogEngine, _ := assistantModelEngine(req.Model)
	catalog := assistantChatCatalog(c.Request.Context(), user.CodeBuddyInternetEnvironment, nil)
	if catalogEngine == assistantEngineCodeBuddy {
		catalog = h.chatCatalog(c.Request.Context(), user)
	}
	req.Model = assistantAllowedUserModel(req.Model, catalog, user)
	cliEngine, _ = assistantModelEngine(req.Model)
	req.Executor = cliEngine
	if err := validateAssistantEffort(user, req.Model, req.Effort); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	legacyExecutor := ""
	if old, ok := h.repo.AssistantSession(ownerID, req.SessionID); ok {
		legacyExecutor = old.Executor
	}
	if refusal := assistantEngineSwitchRefusal(assistantConversationEngine(req.Messages, legacyExecutor), req.Model); refusal != "" {
		c.JSON(409, gin.H{"error": refusal})
		return
	}
	freshCLI := false
	if old, ok := h.repo.AssistantSession(ownerID, req.SessionID); ok {
		freshCLI = assistantConversationEngine(old.Messages, old.Executor) != cliEngine
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
	// Every message shows when it was sent. The server clock stamps the new
	// question, so a skewed or missing browser clock cannot misdate it.
	sentAt := time.Now().UTC()
	for i := len(old.Messages); i < len(req.Messages); i++ {
		if req.Messages[i].Role == "user" {
			req.Messages[i].AuthorID = user.ID
			req.Messages[i].AuthorName = user.LoginName
			req.Messages[i].CreatedAt = sentAt
		}
	}
	g := domain.AssistantGeneration{StartedAt: time.Now().UTC(), ActorID: user.ID, ActorName: user.LoginName, ID: newAssistantSessionID(), SessionID: req.SessionID, UserID: ownerID, ProjectID: req.ProjectID, Status: "running", Events: []domain.AssistantGenerationEvent{}, MessageCount: len(req.Messages)}
	pending := assistantConversation{ID: req.SessionID, Shared: old.Shared, ProjectID: req.ProjectID, IssueID: old.IssueID, Model: req.Model, Effort: req.Effort, Executor: req.Executor, RunID: req.RunID, Messages: req.Messages, TitleEdited: old.TitleEdited, TitleGenerated: old.TitleGenerated}
	if old.TitleEdited || old.TitleGenerated {
		pending.Title = old.Title
	} else {
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
	}
	if req.RunID != "" && h.engine != nil {
		token, err := h.engine.BeginConversationWorkspaceUse(req.RunID)
		if err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		req.workspaceToken = token
	}
	beginErr := h.repo.BeginAssistantGeneration(g, pending)
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
	_, sharedModels, _ := h.assistantChatUser(stored)
	h.scheduleAssistantTitle(parent, ownerID, user, old, req.SessionID, req.Messages, req.Model, sharedModels)
	done := inflightTasks.Add()
	go func() {
		defer done()
		h.executeGeneration(parent, req, g, current)
	}()
	c.JSON(http.StatusAccepted, gin.H{"id": g.ID, "sessionId": g.SessionID, "status": g.Status})
}
func (h *assistantHandlers) generation(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	var ownerID int64
	var err error
	if c.Request.Method == "POST" {
		ownerID, _, err = h.conversationScope(c, c.Param("id"), "", "", user.ID)
	} else {
		ownerID, _, _, err = h.conversationReadScope(c, c.Param("id"), user.ID)
	}
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	g, ok := h.currentGeneration(ownerID, c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "生成不存在"})
		return
	}
	if c.Request.Method == "POST" {
		if err := h.repo.CancelAssistantGeneration(ownerID, g.SessionID, c.Query("generationId")); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"ok": true})
		return
	}
	if c.Query("stream") == "1" {
		serveAssistantGenerationStream(c, g, func() (domain.AssistantGeneration, bool) {
			return h.currentGeneration(ownerID, g.SessionID)
		})
		return
	}
	after := parseQueryInt64(c.Request, "after")
	if after > 0 && len(g.Events) > 0 && after >= int64(g.Events[0].Sequence-1) {
		g.Stream = nil
	}
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
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	rec := newAssistantGenerationRecorder(g)
	defer rec.close()
	sink := rec.sink
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
				if req.workspaceToken != "" {
					if err := h.engine.RenewConversationWorkspaceUse(req.RunID, req.workspaceToken); err != nil {
						cancel()
					}
				}
				old, ok := h.repo.AssistantGeneration(g.UserID, g.SessionID)
				if ok && old.ID == g.ID && old.Steer != nil {
					rec.mu.Lock()
					rec.g.Steer = old.Steer
					rec.mu.Unlock()
				}
				if !ok || old.ID != g.ID || old.Status != "running" || old.CancelRequested {
					cancel()
				}
				if err := h.repo.CheckpointAssistantGeneration(rec.snapshot()); err != nil {
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
		chatCtx = context.WithValue(chatCtx, assistantControlScopeKey{}, assistantControlScope{RunID: req.RunID, SessionID: g.SessionID, GenerationID: g.ID, MessageIndex: len(req.Messages) - 1})
		chatCtx = engine.WithConversationWorkspaceToken(chatCtx, req.workspaceToken, req.RunID)
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
	rec.mu.Lock()
	g = rec.g
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
	rec.g = g
	g = rec.snapshotLocked()
	steer := g.Steer
	rec.mu.Unlock()
	if steer == nil {
		if latest, ok := h.repo.AssistantGeneration(g.UserID, g.SessionID); ok && latest.ID == g.ID && latest.Steer != nil {
			rec.mu.Lock()
			g.Steer = latest.Steer
			steer = latest.Steer
			rec.mu.Unlock()
		}
	}
	// A stop or a mid-turn redirect keeps the paragraphs and tool rows written
	// so far. The completed path already stored its own reply.
	if g.Status != "completed" || steer != nil {
		if err := h.commitTurnTranscript(g, steer, g.Status != "completed" && steer == nil); err != nil {
			fmt.Printf("assistant transcript keep failed: %T\n", err)
			steer = nil
		}
	}
	if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
		fmt.Printf("assistant generation checkpoint failed: %T\n", err)
	}
	rec.live.publish()
	if steer != nil {
		h.followSteer(parent, g, steer, actor)
	} else if g.Status == "completed" {
		h.expireProposalLater(parent, g, req, actor)
	}
}

func (h *assistantHandlers) steerGeneration(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	stored, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(400, gin.H{"error": "请先配置个人 CodeBuddy API Key"})
		return
	}
	user, sharedModels, ok := h.assistantChatUser(stored)
	if !ok {
		c.JSON(400, gin.H{"error": "请先配置个人 CodeBuddy API Key"})
		return
	}
	ownerID, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	var body domain.AssistantSteer
	if !decode(c, &body) {
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" && len(body.Attachments) == 0 {
		c.JSON(400, gin.H{"error": "请提供用户消息"})
		return
	}
	if content == "" {
		content = "请阅读本轮附件并回答。"
	}
	g, ok := h.repo.AssistantGeneration(ownerID, c.Param("id"))
	if !ok || g.Status != "running" {
		c.JSON(409, gin.H{"error": "当前没有进行中的回复"})
		return
	}
	if id := strings.TrimSpace(c.Query("generationId")); id != "" && id != g.ID {
		c.JSON(409, gin.H{"error": "这一轮已经结束，请重新发送"})
		return
	}
	session, ok := h.repo.AssistantSession(ownerID, g.SessionID)
	if !ok {
		c.JSON(404, gin.H{"error": "会话不存在"})
		return
	}
	model := body.Model
	if sharedModels != nil {
		forced, allowed := assistantSponsoredModelFor(sharedModels, model)
		if !allowed {
			c.JSON(400, gin.H{"error": errAssistantSponsoredModel(sharedModels)})
			return
		}
		model = forced
	}
	model = assistantAllowedModel(model, h.chatCatalog(c.Request.Context(), user))
	if err := validateAssistantEffort(user, model, body.Effort); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	probe := assistantRequest{ProjectID: session.ProjectID, SessionID: session.ID, RunID: session.RunID, Messages: []assistantMessage{{Role: "user", Content: content, Attachments: body.Attachments, References: body.References}}}
	if err := h.prepareWorkbenchContext(c, &probe, user); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	message := probe.Messages[0]
	steer := domain.AssistantSteer{Content: message.Content, Attachments: message.Attachments, References: message.References, Model: model, Effort: body.Effort, ApprovalMode: normalizeAssistantApprovalMode(body.ApprovalMode), ActorID: user.ID, ActorName: user.LoginName}
	if err := h.repo.SteerAssistantGeneration(ownerID, g.SessionID, g.ID, steer); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *assistantHandlers) commitTurnTranscript(g domain.AssistantGeneration, steer *domain.AssistantSteer, stopped bool) error {
	session, ok := h.repo.AssistantSession(g.UserID, g.SessionID)
	if !ok {
		return fmt.Errorf("会话不存在")
	}
	changed := false
	if message, worth := turnTranscriptMessage(g, session.Model, stopped); worth && (len(session.Messages) == 0 || session.Messages[len(session.Messages)-1].Role != "assistant") {
		session.Messages = append(session.Messages, message)
		changed = true
	}
	if steer != nil {
		content := strings.TrimSpace(steer.Content)
		if content == "" {
			content = "请阅读本轮附件并回答。"
		}
		last := len(session.Messages) - 1
		if last < 0 || session.Messages[last].Role != "user" || session.Messages[last].Content != content {
			session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: content, AuthorID: steer.ActorID, AuthorName: steer.ActorName, CreatedAt: time.Now().UTC(), Attachments: steer.Attachments, References: steer.References, Trigger: firstNonEmpty(steer.Trigger, "steer")})
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if session.Shared {
		return h.repo.CommitAssistantTranscript(g.UserID, g.ID, session)
	}
	return saveAssistantConversation(h.repo, g.UserID, session)
}

func (h *assistantHandlers) followSteer(parent context.Context, prev domain.AssistantGeneration, steer *domain.AssistantSteer, actor *auth.User) {
	session, ok := h.repo.AssistantSession(prev.UserID, prev.SessionID)
	if !ok || len(session.Messages) == 0 || session.Messages[len(session.Messages)-1].Role != "user" {
		return
	}
	if steer.ActorID != 0 {
		if stored, found := h.repo.User(steer.ActorID); found {
			actor = domainUserToAuth(stored)
		}
	}
	if strings.TrimSpace(steer.Model) != "" {
		session.Model = steer.Model
	}
	if strings.TrimSpace(steer.Effort) != "" {
		session.Effort = steer.Effort
	}
	req := assistantRequest{ProjectID: session.ProjectID, SessionID: session.ID, RunID: session.RunID, Model: firstNonEmpty(steer.Model, session.Model), Effort: firstNonEmpty(steer.Effort, session.Effort), ApprovalMode: steer.ApprovalMode, Messages: session.Messages}
	if req.RunID != "" && h.engine != nil {
		token, err := h.engine.BeginConversationWorkspaceUse(req.RunID)
		if err != nil {
			return
		}
		req.workspaceToken = token
	}
	next := domain.AssistantGeneration{StartedAt: time.Now().UTC(), ActorID: steer.ActorID, ActorName: steer.ActorName, ID: newAssistantSessionID(), SessionID: req.SessionID, UserID: prev.UserID, ProjectID: req.ProjectID, Status: "running", Events: []domain.AssistantGenerationEvent{}, MessageCount: len(req.Messages)}
	if err := h.repo.BeginAssistantGeneration(next, session); err != nil {
		if req.workspaceToken != "" && h.engine != nil {
			_ = h.engine.EndConversationWorkspaceUse(parent, req.RunID, req.workspaceToken)
		}
		return
	}
	h.executeGeneration(parent, req, next, actor)
}
