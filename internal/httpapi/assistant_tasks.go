package httpapi

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

type assistantTask = domain.AssistantTask

type assistantDueTask struct {
	UserID int64
	Task   assistantTask
}

var errAssistantBusy = errors.New("assistant busy")

const (
	assistantTaskTickInterval = 30 * time.Second
	assistantTaskRunStale     = 4 * time.Minute
	assistantTaskMinInterval  = 60
	assistantTaskMaxInterval  = 7 * 24 * 3600
)

func (h *assistantHandlers) listTasks(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	items, err := listAssistantTasks(h.repo, current.ID)
	if err != nil {
		c.JSON(500, gin.H{"error": "读取定时任务失败"})
		return
	}
	if projectID := strings.TrimSpace(c.Query("projectId")); projectID != "" {
		filtered := make([]assistantTask, 0, len(items))
		for _, item := range items {
			if item.ProjectID == projectID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	c.JSON(200, items)
}

func (h *assistantHandlers) putTask(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	var in assistantTask
	if !decode(c, &in) {
		return
	}
	existing, _ := loadAssistantTask(h.repo, current.ID, in.ID)
	task, err := normalizeAssistantTask(in, existing)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if task.ProjectID == "" {
		c.JSON(400, gin.H{"error": "请先在顶栏选择项目后再保存定时任务"})
		return
	}
	if _, ok := h.repo.Project(task.ProjectID); !ok {
		c.JSON(404, gin.H{"error": "项目不存在"})
		return
	}

	if len(task.Notifications) > 0 || len(existing.Notifications) > 0 {
		if h.notifications == nil || !h.notifications.access(c, task.ProjectID, true) {
			return
		}
		if existing.ID != "" && existing.ProjectID != task.ProjectID && !h.notifications.access(c, existing.ProjectID, true) {
			return
		}
	}
	task.NotificationSnapshot = existing.NotificationSnapshot
	task.NotificationResult = existing.NotificationResult
	if existing.ID != "" {
		task.LastStatus = existing.LastStatus
		task.LastRunAt = existing.LastRunAt
		task.LastMessage = existing.LastMessage
		task.LastError = existing.LastError
		task.SessionID = existing.SessionID
	}
	if err := saveAssistantTask(h.repo, current.ID, task); err != nil {
		c.JSON(500, gin.H{"error": "保存定时任务失败"})
		return
	}
	c.JSON(200, task)
}

func (h *assistantHandlers) deleteTask(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	if err := deleteAssistantTask(h.repo, current.ID, c.Param("id")); err != nil {
		c.JSON(404, gin.H{"error": "定时任务不存在"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *assistantHandlers) runTaskNow(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	user, ok := h.repo.User(current.ID)
	if !ok || strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		c.JSON(400, gin.H{"error": "请先在个人设置中配置自己的 CodeBuddy API Key"})
		return
	}
	task, err := loadAssistantTask(h.repo, current.ID, c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": "定时任务不存在"})
		return
	}
	if _, loaded := h.active.Load(current.ID); loaded {
		c.JSON(429, gin.H{"error": "已有对话正在生成，请稍后重试"})
		return
	}
	go h.runStoredAssistantTask(user, task)
	c.JSON(202, task)
}

func (h *assistantHandlers) startTaskTicker() {
	if flag.Lookup("test.v") != nil || os.Getenv("AGENTFLOW_ASSISTANT_TASKS") == "0" {
		return
	}
	go func() {
		ticker := time.NewTicker(assistantTaskTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.tickAssistantTasks()
		}
	}()
}

func (h *assistantHandlers) tickAssistantTasks() {
	if h == nil || h.repo == nil {
		return
	}
	for _, item := range listDueAssistantTasks(h.repo, time.Now().UTC()) {
		user, ok := h.repo.User(item.UserID)
		if !ok {
			continue
		}
		h.runStoredAssistantTask(user, item.Task)
	}
}

func (h *assistantHandlers) runStoredAssistantTask(user domain.User, task assistantTask) {
	ctx, cancel := context.WithTimeout(context.Background(), assistantChatTimeout)
	defer cancel()
	if err := h.executeAssistantTask(ctx, user, &task); err != nil {
		if errors.Is(err, errAssistantBusy) {
			return
		}
		log.Printf("assistant task %s user=%d failed", task.ID, user.ID)
	}
	_ = h.repo.SaveAssistantTaskResult(user.ID, task)
}

func (h *assistantHandlers) executeAssistantTask(ctx context.Context, user domain.User, task *assistantTask) error {
	if _, loaded := h.active.LoadOrStore(user.ID, true); loaded {
		return errAssistantBusy
	}
	defer h.active.Delete(user.ID)
	if _, ok := ctx.Value(assistantExecModeKey{}).(assistantExecMode); !ok {
		ctx = withAssistantExecMode(ctx, assistantExecMode{AutoWrite: true})
	}
	mode := assistantExecModeFrom(ctx)

	task.NotificationResult = ""
	task.LastStatus = "running"
	task.LastError = ""
	task.LastRunAt = time.Now().UTC()
	if strings.TrimSpace(task.ID) != "" {
		_ = saveAssistantTask(h.repo, user.ID, *task)
	}

	if strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		task.LastStatus = "error"
		task.LastError = "未配置 CodeBuddy API Key"
		return errors.New(task.LastError)
	}
	catalog := loadAssistantModels(ctx, user.CodeBuddyInternetEnvironment)
	model := assistantAllowedModel(task.Model, catalog)
	prevSession := strings.TrimSpace(task.SessionID)
	sessionID := newAssistantSessionID()
	sourceRoot := h.resolvedSourceRoot()
	modelCtx, cancelModel := context.WithCancel(ctx)
	defer cancelModel()
	var toolFiles agentToolFiles
	if h.generate == nil {
		sess, files, err := h.openScheduledToolSession(modelCtx, user, task.ProjectID, mode, cancelModel)
		if err != nil {
			logAgentToolEvent("session-fallback", "mode=scheduled", "err="+truncateRunes(err.Error(), 160))
			task.LastStatus = "error"
			task.LastError = "无法启动工具会话"
			return err
		}
		toolFiles = files
		defer sess.Close()
		defer files.Cleanup()
	}
	// Scheduled runs are unattended. Keep the turn reminder aligned with
	// the auto-write gateway and scheduled system prompt.
	req := assistantRequest{
		ApprovalMode: AssistantApprovalAuto,
		ProjectID:    task.ProjectID,
		SessionID:    sessionID,
		Model:        model,
		Messages:     []assistantMessage{{Role: "user", Content: scheduledAssistantPrompt(*task, mode)}},
	}
	fullPrompt := h.turnPrompt(req, sourceRoot, false)
	turn := fullPrompt
	systemPrompt := assistantScheduledSystemPromptText(mode)
	generate := h.generate
	if generate == nil {
		resume := false
		generate = func(ctx context.Context, prompt string, u domain.User) (string, error) {
			cliReq := assistantCLIRequest{Prompt: prompt, Model: model, SessionID: sessionID, Resume: resume, UserID: u.ID, SystemPrompt: systemPrompt, Native: true, MCPConfig: toolFiles.MCPConfig, Settings: toolFiles.Settings}
			raw, _, err := runAssistantCLI(ctx, cliReq, u, nil)
			resume = true
			return raw, err
		}
	}
	failGenerate := func(err error) error {
		task.LastStatus = "error"
		task.LastError = "生成失败或超时"
		if ctx.Err() != nil {
			task.LastError = "生成已取消或超时"
		}
		if msg := strings.TrimSpace(err.Error()); msg != "" {
			task.LastError += "：" + msg
		}
		log.Printf("assistant task %s generate failed: %v", task.ID, err)
		return err
	}
	var reply assistantReply
	if ctx.Err() != nil {
		task.LastStatus = "error"
		task.LastError = "生成已取消或超时"
		return ctx.Err()
	}
	raw, err := generate(modelCtx, turn, user)
	if err != nil {
		return failGenerate(err)
	}
	message := strings.TrimSpace(redactAssistantSecrets(strings.ReplaceAll(raw, user.CodeBuddyAPIKey, "[redacted]"), user))
	if message == "" {
		task.LastStatus = "error"
		task.LastError = "未返回结果"
		return errors.New(task.LastError)
	}
	notificationResult := message
	_, cleaned, hasNotification := domain.ParseNotificationResult(message)
	if hasNotification && strings.TrimSpace(cleaned) != "" {
		message = cleaned
	}
	reply = assistantReply{Message: message, SessionID: sessionID}
	if strings.TrimSpace(reply.Message) == "" && len(reply.Drafts) == 0 {
		if task.LastStatus == "error" {
			return errors.New(task.LastError)
		}
		task.LastStatus = "error"
		task.LastError = "未返回结果"
		return errors.New(task.LastError)
	}
	if len(reply.Drafts) > 0 {
		reply.Drafts = h.applyAssistantDrafts(ctx, user, req.ProjectID, reply.Drafts)
		for _, draft := range reply.Drafts {
			if !draft.Saved {
				task.LastStatus = "error"
				task.LastError = "部分平台写入未落库"
				break
			}
		}
	}
	stored := append([]assistantMessage{}, req.Messages...)
	stored = append(stored, assistantMessage{Role: "assistant", Content: jsonObject(gin.H{"message": reply.Message, "drafts": reply.Drafts})})
	_ = saveAssistantConversation(h.repo, user.ID, assistantConversation{
		ID:        sessionID,
		Title:     "定时 · " + task.Name,
		ProjectID: task.ProjectID,
		Model:     model,
		Messages:  stored,
	})
	if prevSession != "" && prevSession != sessionID {
		_ = deleteAssistantConversation(h.repo, user.ID, prevSession)
	}
	task.SessionID = sessionID
	task.Model = model
	if task.LastStatus != "error" {
		task.LastStatus = "ok"
		task.LastError = ""
	}
	task.NotificationResult = domain.NotificationText(notificationResult, 18000)
	task.LastMessage = truncateRunes(reply.Message, 400)
	if task.LastStatus == "error" {
		return errors.New(task.LastError)
	}
	return nil
}

func scheduledAssistantPrompt(task assistantTask, mode assistantExecMode) string {
	prefix := "这是当前项目「" + task.ProjectID + "」下的无人值守定时任务「" + task.Name + "」。直接调用当前项目和个人已安装的 MCP，以及 agentflow__* 平台工具，读写立即生效。issues_save 把需求 id 放在 body.id（也可同时放 path.id）。全部完成后用自然语言汇报结果。"
	if mode.DeferSOP {
		prefix += " 不要调用 issues_dispatch、runs_start、schedule_jobs_run、schedule_jobs_save；需要派发 SOP 时请单独创建触发任务。"
	} else {
		prefix += " 若提示词要求开工，可以使用 issues_dispatch。需要扫描需求、基线门禁并派发 SOP 时，创建平台触发任务，不要写进 AI 定时任务。"
	}
	return prefix + " 按下面的提示词原文执行。\n" + task.Prompt + domain.NotificationAIInstructions(task.Notifications)
}

func normalizeAssistantTask(in assistantTask, existing assistantTask) (assistantTask, error) {
	in.ID = assistantTaskID(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	in.Prompt = strings.TrimSpace(in.Prompt)
	in.Model = strings.TrimSpace(in.Model)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.Cron = strings.TrimSpace(in.Cron)
	if in.Name == "" || in.Prompt == "" {
		return in, errors.New("请填写任务名称和指令")
	}
	if in.ProjectID == "" {
		return in, errors.New("请先在顶栏选择项目后再保存定时任务")
	}
	if len([]rune(in.Name)) > 80 {
		return in, errors.New("任务名称过长")
	}
	if len(in.Prompt) > 4000 {
		return in, errors.New("任务指令过长")
	}
	if in.Cron != "" {
		if _, err := parseAssistantCron(in.Cron); err != nil {
			return in, errors.New("Cron 表达式无效，需为 5 段（分 时 日 月 周，东八区），例如 0 * * * *")
		}
		in.IntervalSec = 0
	} else {
		if in.IntervalSec < assistantTaskMinInterval {
			return in, errors.New("间隔至少 60 秒，或改用 Cron 表达式")
		}
		if in.IntervalSec > assistantTaskMaxInterval {
			return in, errors.New("间隔过长")
		}
	}
	now := time.Now().UTC()
	if existing.ID == "" {
		if in.ID == "" {
			in.ID = newAssistantTaskID()
		}
		in.CreatedAt = now
		in.LastRunAt = now
		in.LastStatus = "idle"
	} else {
		if in.ID == "" {
			in.ID = existing.ID
		}
		in.CreatedAt = existing.CreatedAt
		in.SessionID = firstNonEmpty(in.SessionID, existing.SessionID)
		if in.LastRunAt.IsZero() {
			in.LastRunAt = existing.LastRunAt
		}
		if in.LastStatus == "" {
			in.LastStatus = existing.LastStatus
		}
		if in.LastMessage == "" {
			in.LastMessage = existing.LastMessage
		}
		if in.LastError == "" && in.LastStatus != "ok" {
			in.LastError = existing.LastError
		}
	}
	in.UpdatedAt = now
	return in, nil
}

func newAssistantTaskID() string {
	return "at" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func assistantTaskID(id string) string {
	return assistantRecordID(id)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateRunes(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

func saveAssistantTask(st *repo.Store, userID int64, task assistantTask) error {
	if st == nil {
		return os.ErrInvalid
	}
	if task.ID == "" {
		task.ID = newAssistantTaskID()
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = time.Now().UTC()
	}
	return st.PutAssistantTask(userID, task)
}

func loadAssistantTask(st *repo.Store, userID int64, id string) (assistantTask, error) {
	if st == nil {
		return assistantTask{}, os.ErrNotExist
	}
	task, ok := st.AssistantTask(userID, id)
	if !ok {
		return assistantTask{}, os.ErrNotExist
	}
	return task, nil
}

func listAssistantTasks(st *repo.Store, userID int64) ([]assistantTask, error) {
	if st == nil {
		return nil, os.ErrInvalid
	}
	return st.ListAssistantTasks(userID), nil
}

func deleteAssistantTask(st *repo.Store, userID int64, id string) error {
	if st == nil {
		return os.ErrNotExist
	}
	if err := st.DeleteAssistantTask(userID, id); err != nil {
		return os.ErrNotExist
	}
	return nil
}

func listDueAssistantTasks(st *repo.Store, now time.Time) []assistantDueTask {
	if st == nil {
		return nil
	}
	due := []assistantDueTask{}
	for _, task := range st.EnabledAssistantTasks() {
		if !assistantTaskDue(task, now) {
			continue
		}
		due = append(due, assistantDueTask{UserID: task.UserID, Task: task})
	}
	return due
}

func assistantTaskDue(task assistantTask, now time.Time) bool {
	if !task.Enabled {
		return false
	}
	cron := strings.TrimSpace(task.Cron)
	if cron == "" && task.IntervalSec < assistantTaskMinInterval {
		return false
	}
	if task.LastStatus == "running" && !task.LastRunAt.IsZero() && now.Sub(task.LastRunAt) < assistantTaskRunStale {
		return false
	}
	if task.LastRunAt.IsZero() {
		return true
	}
	if cron != "" {
		next, err := nextAssistantCron(cron, task.LastRunAt)
		return err == nil && !next.After(now.UTC())
	}
	return now.Sub(task.LastRunAt) >= time.Duration(task.IntervalSec)*time.Second
}
