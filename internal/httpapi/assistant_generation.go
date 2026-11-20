package httpapi

import (
	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"bytes"
	"context"
	"crypto/rand"
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
	var body struct {
		assistantRequest
		ClientRequestID string `json:"clientRequestId"`
	}
	if !decodeLimit(c, &body, assistantRequestBodyLimit) {
		return
	}
	req := body.assistantRequest
	if id := body.ClientRequestID; id != "" && (len(id) > 64 || assistantRecordID(id) != id) {
		c.JSON(400, gin.H{"error": "发送请求 ID 无效"})
		return
	}
	if !requireAssistantApprovalUser(c, current.ID, req.ApprovalUserID) {
		return
	}
	req.Model = assistantRequestModel(req.Executor, req.Model)
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
	ownerID, shared, err := h.conversationScope(c, req.SessionID, req.RunID, req.ProjectID, stored.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	old, _ := h.repo.AssistantSession(ownerID, req.SessionID)
	if old.ID != "" && old.ExecutionLocked() {
		if req.ExecutionTarget != nil && *req.ExecutionTarget != old.Execution() {
			c.JSON(409, gin.H{"error": "会话执行机器已固定，不能更换"})
			return
		}
		target := old.Execution()
		req.ExecutionTarget = &target
	} else {
		var targetErr error
		req.ExecutionTarget, targetErr = h.newExecutionTarget(stored.ID, req.ProjectID, draftExecutionTarget(old, req.ExecutionTarget))
		if targetErr != nil {
			c.JSON(400, gin.H{"error": targetErr.Error()})
			return
		}
	}
	cliEngine, _ := assistantModelEngine(req.Model)
	loginRunner := h.assistantLoginRunner(stored.ID, req.ExecutionTarget, cliEngine)
	runnerLogin := loginRunner.ID != ""
	user, model, refusal := h.assistantTurnUser(stored, req.Model, runnerLogin)
	if refusal != "" {
		c.JSON(400, gin.H{"error": refusal})
		return
	}
	req.Model = model
	if err := h.validateAssistantTriggerHistory(ownerID, req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if shared {
		if old.ID != "" {
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
	if err := h.authorizeWorkbenchTurn(c, &req, user.ID); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	catalogEngine, _ := assistantModelEngine(req.Model)
	catalog := assistantChatCatalog(c.Request.Context(), user.CodeBuddyInternetEnvironment, nil)
	if catalogEngine == assistantEngineCodeBuddy {
		catalog = h.turnCatalog(c.Request.Context(), user, req.Model)
	}
	if !runnerLogin {
		selected, err := assistantTurnCatalogModel(req.Model, catalog, user, req.RequireExactModel)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		req.Model = selected
	}
	cliEngine, _ = assistantModelEngine(req.Model)
	req.Executor = cliEngine
	cliModel, err := h.resolveAssistantTurnModel(c.Request.Context(), user, req.Model, req.Effort, loginRunner)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	executor := h.forkExecutor(c, old, user.ID)
	if refusal := assistantEngineSwitchRefusal(assistantConversationEngine(req.Messages, executor), req.Model); refusal != "" {
		c.JSON(409, gin.H{"error": refusal})
		return
	}
	if err := h.buildWorkbenchContext(c, &req, user); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	freshCLI := old.ID != "" && assistantConversationEngine(old.Messages, executor) != cliEngine
	if old.ID != "" {
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
	g := domain.AssistantGeneration{StartedAt: time.Now().UTC(), ActorID: user.ID, ActorName: user.LoginName, ID: newAssistantSessionID(), ClientRequestID: body.ClientRequestID, SessionID: req.SessionID, UserID: ownerID, ProjectID: req.ProjectID, Status: "running", Events: []domain.AssistantGenerationEvent{}, MessageCount: len(req.Messages)}
	// The issue this turn linked from its @-mention is the conversation's
	// issue now; any other change since old was read is still refused.
	issueID := old.IssueID
	if issueID == "" && req.linkedIssueID != "" {
		issueID = req.linkedIssueID
	}
	pending := assistantConversation{ExecutionTarget: req.ExecutionTarget, ID: req.SessionID, Shared: old.Shared, ProjectID: req.ProjectID, IssueID: issueID, Model: req.Model, Effort: req.Effort, Executor: req.Executor, RunID: req.RunID, Messages: req.Messages, TitleEdited: old.TitleEdited, TitleGenerated: old.TitleGenerated}
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
			_ = h.repo.FinishWorkspaceUse(req.RunID, req.workspaceToken, time.Now().UTC(), nil, "")
		}
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	h.recordGenerationWorkspaceLease(g, req.RunID, req.workspaceToken)
	// Keep the authenticated identity, but disconnect model execution from the
	// lifetime of the browser request. Only explicit cancellation stops a turn.
	parent := context.WithValue(context.WithoutCancel(c.Request.Context()), assistantFreshCLIKey{}, freshCLI)
	if runnerLogin && cliEngine == runtime.AgentCLIAntigravity {
		parent = context.WithValue(parent, assistantRunnerTurnModelKey{}, assistantRunnerTurnModel{
			runnerID: loginRunner.ID, model: req.Model, effort: req.Effort, resolved: cliModel,
		})
	}
	_, sharedModels, _ := h.assistantChatUser(stored)
	h.scheduleAssistantTitle(h.helperExecutionContext(parent, ownerID, req.ExecutionTarget), ownerID, user, old, req.SessionID, req.Messages, req.Model, sharedModels)
	done := inflightTasks.Add()
	go func() {
		defer done()
		h.executeGeneration(parent, req, g, current)
	}()
	c.JSON(http.StatusAccepted, gin.H{"id": g.ID, "sessionId": g.SessionID, "status": g.Status, "executionTarget": req.ExecutionTarget})
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
		if value, ok := assistantLiveGenerations.Load(g.ID); ok {
			if stop := value.(*assistantLiveGeneration).cancel; stop != nil {
				stop()
			}
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
	rec := newAssistantGenerationRecorder(g, cancel)
	defer rec.close()
	ctx = context.WithValue(ctx, assistantDiagnosticsKey{}, rec.diagnostics)
	diagUser, _ := h.repo.User(g.UserID)
	if actor != nil && actor.ID != 0 && actor.ID != g.UserID {
		if u, ok := h.repo.User(actor.ID); ok {
			diagUser = u
		}
	}
	sink := rec.sink
	stopped := make(chan struct{})
	done := make(chan struct{})
	renewed := make(chan struct{})
	// The lease renews on its own ticker. A heartbeat commit that stalls on the
	// generation row must not also stop the renewal: a replica that reaps this
	// turn meanwhile releases its lease only once renewal has stopped for 45s.
	go func() {
		defer close(renewed)
		if req.workspaceToken == "" {
			return
		}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				if err := h.engine.RenewConversationWorkspaceUse(req.RunID, req.workspaceToken); err != nil {
					rec.diagnostics.terminate("WORKSPACE_LEASE_LOST", "会话工作区续租失败："+err.Error(), diagUser)
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				old, ok := h.repo.PeekAssistantGeneration(g.UserID, g.SessionID)
				if ok && old.ID == g.ID && old.Steer != nil {
					rec.mu.Lock()
					rec.g.Steer = old.Steer
					rec.mu.Unlock()
				}
				if !ok || old.ID != g.ID {
					rec.diagnostics.terminate("GENERATION_SUPERSEDED", "当前生成记录已被替换或移除", diagUser)
					rec.live.cancel()
				} else if old.Status != "running" {
					rec.diagnostics.terminate("GENERATION_STATUS_CHANGED", "生成状态已变更为 "+old.Status, diagUser)
					rec.live.cancel()
				} else if old.CancelRequested {
					if old.Steer != nil {
						rec.diagnostics.terminate("USER_STEER", "用户在生成途中发送新指令转向下一轮", diagUser)
					} else {
						rec.diagnostics.terminate("USER_CANCELLED", "用户主动停止了本轮生成", diagUser)
					}
					rec.live.cancel()
				}
				snapshot := rec.snapshot()
				if err := h.checkpointRunningGeneration(snapshot, rec.diagnostics); err != nil {
					rec.diagnostics.terminate("CHECKPOINT_FAILED", "保存生成检查点失败："+err.Error(), diagUser)
					cancel()
				}
			}
		}
	}()
	writer := &generationHTTPWriter{header: make(http.Header), status: 200}
	func() {
		defer func() {
			if recover() != nil {
				rec.diagnostics.terminate("SERVER_PANIC", "服务端处理生成时发生未捕获异常", diagUser)
				failure := &domain.AssistantFailure{Code: "INTERNAL_ERROR", Phase: "server", Message: "生成异常中断，请重试", Suggestion: "请携带诊断编号联系管理员排查服务端日志。"}
				sink("error", gin.H{"error": failure.Message, "failure": failure})
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
	// Stop renewal before handing the use lease to the asynchronous save.
	// The turn is complete once its execution has exited, without archive I/O.
	close(stopped)
	<-done
	<-renewed
	workspaceWarning := ""
	if req.workspaceToken != "" {
		if result, err := h.engine.EndConversationWorkspaceUseAsync(ctx, req.RunID, req.workspaceToken); err != nil {
			workspaceWarning = "工作区保存未完成，暂缓回收：" + err.Error()
		} else {
			trackAssistantWorkspaceSave(req.RunID, result)
		}
	}
	g = rec.snapshot()
	if workspaceWarning != "" {
		g.WorkspaceWarning = workspaceWarning
	}
	if writer.status >= 400 && g.Error == "" {
		var result struct {
			Error   string                   `json:"error"`
			Failure *domain.AssistantFailure `json:"failure"`
		}
		_ = json.Unmarshal(writer.body.Bytes(), &result)
		g.Error = result.Error
		g.Failure = result.Failure
	}
	if len(g.Reply) > 0 {
		g.Status = "completed"
	} else {
		g.Status = "failed"
		if g.Error == "" {
			g.Error = "生成未完成，请重试"
		}
		if ctx.Err() != nil {
			if term := rec.diagnostics.termination(); term != nil && term.Code == "WORKSPACE_LEASE_LOST" {
				g.Status = "failed"
				g.Error = "会话工作区续租失败，生成已中断"
				g.Failure = &domain.AssistantFailure{Code: "WORKSPACE_LEASE_LOST", Phase: "server", Message: g.Error, Suggestion: "检查已执行操作及工作区状态后再重试；可下载诊断信息排查。"}
			} else {
				g.Status = "cancelled"
				g.Error = "生成已停止"
				g.Failure = nil
			}
		}
	}
	if g.Status == "failed" && g.Failure == nil {
		g.Failure = rec.diagnostics.failureForMessage(g.Error)
	}
	steer, finishErr := h.repo.CloseAssistantGenerationSteering(g.UserID, g.SessionID, g.ID)
	if finishErr != nil {
		fmt.Printf("assistant generation steering close failed: %T\n", finishErr)
	} else {
		g.Steer = steer
		g.CancelRequested = g.CancelRequested || steer != nil
		// A stop or a mid-turn redirect keeps the paragraphs and tool rows
		// written so far. The completed path already stored its own reply.
		if g.Status != "completed" || steer != nil {
			if err := h.commitTurnTranscript(g, steer, g.Status != "completed" && steer == nil); err != nil {
				fmt.Printf("assistant transcript keep failed: %T\n", err)
				steer = nil
			}
		}
		finishErr = h.repo.CheckpointAssistantGeneration(g, rec.diagnostics.snapshot(g))
		if finishErr != nil {
			fmt.Printf("assistant generation checkpoint failed: %T\n", finishErr)
		}
	}
	rec.mu.Lock()
	rec.g = g
	rec.mu.Unlock()
	rec.live.publish()
	if finishErr != nil {
		return
	}
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
	var body struct {
		domain.AssistantSteer
		ApprovalUserID int64 `json:"approvalUserId"`
	}
	if !decode(c, &body) || !requireAssistantApprovalUser(c, current.ID, body.ApprovalUserID) {
		return
	}
	stored, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(400, gin.H{"error": "请先配置个人 CodeBuddy API Key"})
		return
	}
	ownerID, _, err := h.conversationScope(c, c.Param("id"), "", "", stored.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
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
	g, ok := h.readGeneration(ownerID, c.Param("id"))
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
	// The saved conversation selects the CLI for steering. Bare model IDs
	// use that CLI, and an explicit CLI prefix must match it.
	executor := h.forkExecutor(c, session, stored.ID)
	cliEngine := assistantConversationEngine(session.Messages, executor)
	if cliEngine == "" {
		cliEngine, _ = assistantModelEngine(assistantRequestModel(executor, session.Model))
	}
	model := strings.TrimSpace(firstNonEmpty(body.Model, session.Model))
	if !assistantModelHasEnginePrefix(model) {
		model = assistantRequestModel(cliEngine, model)
	} else if id, explicit := strings.CutPrefix(model, assistantEngineCodeBuddy+":"); explicit {
		model = strings.TrimSpace(id)
	}
	// Refuse a switch before recording cancellation, even without credentials
	// for the requested CLI. The current reply must keep running on refusal.
	if refusal := assistantEngineSwitchRefusal(cliEngine, model); refusal != "" {
		c.JSON(409, gin.H{"error": refusal})
		return
	}
	target := session.Execution()
	loginRunner := h.assistantLoginRunner(stored.ID, &target, cliEngine)
	runnerLogin := loginRunner.ID != ""
	user, model, refusal := h.assistantTurnUser(stored, model, runnerLogin)
	if refusal != "" {
		c.JSON(400, gin.H{"error": refusal})
		return
	}
	if engine, id := assistantModelEngine(model); engine != assistantEngineCodeBuddy && !assistantValidEngineModel(id) {
		c.JSON(400, gin.H{"error": "模型 ID 无效"})
		return
	}
	// A CodeBuddy model this instance has not listed yet refreshes the list
	// first, like a new turn, instead of silently becoming the default.
	catalog := assistantChatCatalog(c.Request.Context(), user.CodeBuddyInternetEnvironment, nil)
	if engine, _ := assistantModelEngine(model); engine == assistantEngineCodeBuddy {
		catalog = h.turnCatalog(c.Request.Context(), user, model)
	}
	if !runnerLogin {
		selected, err := assistantTurnCatalogModel(model, catalog, user, body.RequireExactModel)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		model = selected
	}
	// The catalog can substitute a configured default. Check that model too.
	if refusal := assistantEngineSwitchRefusal(cliEngine, model); refusal != "" {
		c.JSON(409, gin.H{"error": refusal})
		return
	}
	if _, err := h.resolveAssistantTurnModel(c.Request.Context(), user, model, body.Effort, loginRunner); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	probe := assistantRequest{ProjectID: session.ProjectID, SessionID: session.ID, RunID: session.RunID, Messages: []assistantMessage{{Role: "user", Content: content, Attachments: body.Attachments, References: body.References}}}
	if err := h.prepareWorkbenchContext(c, &probe, user); err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	message := probe.Messages[0]
	steer := domain.AssistantSteer{
		Content:           message.Content,
		Attachments:       message.Attachments,
		References:        message.References,
		Model:             model,
		RequireExactModel: body.RequireExactModel,
		Effort:            body.Effort,
		ApprovalMode:      normalizeAssistantApprovalMode(body.ApprovalMode),
		ActorID:           user.ID,
		ActorName:         user.LoginName,
		TriggerID:         rand.Text(),
	}
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
	var trigger, triggerID string
	if steer != nil {
		trigger = firstNonEmpty(steer.Trigger, "steer")
		// Generations written before steers had an ID still have a stable ID.
		triggerID = firstNonEmpty(steer.TriggerID, g.ID)
		for _, message := range session.Messages {
			if triggerID != "" && message.Role == "user" && message.Trigger == trigger && message.TriggerID == triggerID {
				return nil
			}
		}
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
		session.Messages = append(session.Messages, domain.AssistantMessage{
			Role:        "user",
			Content:     content,
			AuthorID:    steer.ActorID,
			AuthorName:  steer.ActorName,
			CreatedAt:   time.Now().UTC(),
			Attachments: steer.Attachments,
			References:  steer.References,
			Trigger:     trigger,
			TriggerID:   triggerID,
		})
		changed = true
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
	if !ok || len(session.Messages) == 0 {
		return
	}
	last := session.Messages[len(session.Messages)-1]
	if last.Role != "user" || (steer.TriggerID != "" && last.TriggerID != steer.TriggerID) {
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
	// Empty effort selects the follow-up model's default.
	session.Effort = steer.Effort
	req := assistantRequest{
		ProjectID:         session.ProjectID,
		SessionID:         session.ID,
		RunID:             session.RunID,
		Executor:          session.Executor,
		ExecutionTarget:   session.ExecutionTarget,
		Model:             session.Model,
		RequireExactModel: steer.RequireExactModel,
		Effort:            session.Effort,
		ApprovalMode:      steer.ApprovalMode,
		Messages:          session.Messages,
	}
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
			_ = h.repo.FinishWorkspaceUse(req.RunID, req.workspaceToken, time.Now().UTC(), nil, "")
		}
		return
	}
	h.recordGenerationWorkspaceLease(next, req.RunID, req.workspaceToken)
	h.executeGeneration(parent, req, next, actor)
}
