package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	id := c.Param("id")
	task, _ := h.repo.AssistantTask(current.ID, id)
	if err := deleteAssistantTask(h.repo, current.ID, id); err != nil {
		c.JSON(404, gin.H{"error": "定时任务不存在"})
		return
	}
	if strings.TrimSpace(task.SessionID) != "" {
		_ = h.deleteConversation(current.ID, task.SessionID)
	}
	h.deleteScheduledSessionsForTask(id)
	c.JSON(200, gin.H{"ok": true})
}

func (h *assistantHandlers) runTaskNow(c *gin.Context) {
	current, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录后使用 AI 助手"})
		return
	}
	user, ok := h.repo.User(current.ID)
	task, err := loadAssistantTask(h.repo, current.ID, c.Param("id"))
	engine, _ := assistantModelEngine(task.Model)
	if key, _ := assistantEngineCredential(user, engine); !ok || key == "" {
		msg := "请先在个人配置中配置自己的 CodeBuddy API Key"
		if engine != assistantEngineCodeBuddy {
			msg = assistantEngineMissingKey(engine)
		}
		c.JSON(400, gin.H{"error": msg})
		return
	}
	if err != nil {
		c.JSON(404, gin.H{"error": "定时任务不存在"})
		return
	}
	if _, loaded := h.active.Load(assistantTaskActiveKey(task.ID, current.ID)); loaded {
		c.JSON(429, gin.H{"error": "该定时任务正在运行，请稍后重试"})
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
	defer inflightTasks.Add()()
	ctx := withAssistantSessionOrigin(context.Background(), domain.AssistantSourceScheduled, task.ID)
	if err := h.executeAssistantTask(ctx, user, &task); err != nil {
		if errors.Is(err, errAssistantBusy) {
			return
		}
		log.Printf("assistant task %s user=%d failed", task.ID, user.ID)
	}
	_ = h.repo.SaveAssistantTaskResult(user.ID, task)
}

// executeAssistantTask runs one scheduled task as an ordinary chat turn: it
// opens a conversation with the task's prompt as the user message, runs it
// through the same generation path as the chat page (streaming, stop/steer,
// skills, CLIs, retries, drafts) and reads the outcome back into the task.
// Only the trigger lives here: who runs it, what is asked, the session's
// metadata and how the result is reported.
func (h *assistantHandlers) executeAssistantTask(ctx context.Context, user domain.User, task *assistantTask) error {
	origin := assistantSessionOriginFrom(ctx)
	if origin.Source == "" {
		origin.Source = domain.AssistantSourceScheduled
	}
	if origin.TaskID == "" {
		origin.TaskID = strings.TrimSpace(task.ID)
	}
	// Each task runs in its own conversation, so one owner's loops run side
	// by side; only the same task is kept from overlapping itself.
	activeKey := assistantTaskActiveKey(origin.TaskID, user.ID)
	if _, loaded := h.active.LoadOrStore(activeKey, true); loaded {
		return errAssistantBusy
	}
	defer h.active.Delete(activeKey)
	ctx = withAssistantExecMode(ctx, assistantExecModeForApproval(AssistantApprovalAuto))
	mode := assistantExecModeFrom(ctx)

	task.NotificationResult = ""
	task.LastStatus = "running"
	task.LastError = ""
	task.LastRunAt = time.Now().UTC()
	if strings.TrimSpace(task.ID) != "" {
		_ = saveAssistantTask(h.repo, user.ID, *task)
	}
	fail := func(msg string) error {
		task.LastStatus, task.LastError = "error", msg
		return errors.New(msg)
	}

	// The task's model picks the CLI; a scheduled run needs the runner's own
	// key for it (no sponsor), checked before a conversation is opened.
	engine, _ := assistantModelEngine(task.Model)
	if key, _ := assistantEngineCredential(user, engine); key == "" {
		if engine != assistantEngineCodeBuddy {
			return fail(assistantEngineMissingKey(engine))
		}
		return fail("未配置 CodeBuddy API Key")
	}
	model, err := scheduledRunModel(ctx, task.Model, user)
	if err != nil {
		return fail(err.Error())
	}
	cliEngine, _ := assistantModelEngine(model)
	prevSession := strings.TrimSpace(task.SessionID)
	sessionID := newAssistantSessionID()
	now := time.Now().UTC()
	req := assistantRequest{
		ApprovalMode: AssistantApprovalAuto,
		ProjectID:    task.ProjectID,
		SessionID:    sessionID,
		Model:        model,
		Executor:     cliEngine,
		Messages:     []assistantMessage{{Role: "user", Content: scheduledAssistantPrompt(*task, mode), AuthorID: user.ID, AuthorName: user.LoginName, CreatedAt: now}},
	}
	g := domain.AssistantGeneration{StartedAt: now, ActorID: user.ID, ActorName: user.LoginName, ID: newAssistantSessionID(), SessionID: sessionID, UserID: user.ID, ProjectID: task.ProjectID, Status: "running", Events: []domain.AssistantGenerationEvent{}, MessageCount: len(req.Messages)}
	req.Messages[0].TraceID = g.ID
	pending := assistantConversation{ID: sessionID, Title: "定时 · " + task.Name, ProjectID: task.ProjectID, Model: model, Executor: cliEngine, Source: origin.Source, TaskID: origin.TaskID, Messages: req.Messages}
	if err := h.repo.BeginAssistantGeneration(g, pending); err != nil {
		_ = fail("无法创建定时任务会话")
		return err
	}
	task.SessionID = sessionID
	task.Model = model

	// Like a chat turn, a scheduled run has no time limit: it ends when the
	// CLI finishes or someone stops it from the conversation page.
	h.executeGeneration(ctx, req, g, domainUserToAuth(user))
	return h.readScheduledTurn(user, task, g, len(req.Messages), prevSession, origin.TaskID)
}

// readScheduledTurn reports the finished turn back onto the task. The reply
// is the message right after the prompt; a steer from the page may have
// continued the conversation after it, which does not change this outcome.
func (h *assistantHandlers) readScheduledTurn(user domain.User, task *assistantTask, g domain.AssistantGeneration, prompts int, prevSession, taskID string) error {
	fail := func(msg string) error {
		task.LastStatus, task.LastError = "error", msg
		return errors.New(msg)
	}
	session, ok := h.repo.AssistantSession(user.ID, g.SessionID)
	var reply *assistantMessage
	if ok && len(session.Messages) > prompts && session.Messages[prompts].Role == "assistant" {
		reply = &session.Messages[prompts]
	}
	if reply == nil || reply.Unfinished != "" {
		msg := ""
		if reply != nil {
			msg = reply.Unfinished
		}
		if latest, found := h.repo.AssistantGeneration(user.ID, g.SessionID); found && latest.ID == g.ID {
			msg = firstNonEmpty(msg, latest.Error)
		}
		return fail(firstNonEmpty(strings.TrimSpace(msg), "未返回结果"))
	}
	var content struct {
		Message string           `json:"message"`
		Drafts  []assistantDraft `json:"drafts"`
	}
	_ = json.Unmarshal([]byte(reply.Content), &content)
	raw := strings.TrimSpace(content.Message)
	message := raw
	// The notification block is for the Bot; the transcript keeps the prose.
	if _, cleaned, has := domain.ParseNotificationResult(raw); has && strings.TrimSpace(cleaned) != "" {
		message = strings.TrimSpace(cleaned)
		var payload map[string]any
		if json.Unmarshal([]byte(reply.Content), &payload) == nil {
			payload["message"] = message
			reply.Content = jsonObject(payload)
			if err := saveAssistantConversation(h.repo, user.ID, session); err != nil {
				log.Printf("scheduled assistant notification strip session=%s: %v", g.SessionID, err)
			}
		}
	}
	task.NotificationResult = domain.NotificationText(raw, 18000)
	task.LastMessage = truncateRunes(message, 400)
	if message == "" && len(content.Drafts) == 0 {
		return fail("未返回结果")
	}
	// Nobody confirms an unattended run: a write left as a card failed.
	for _, draft := range content.Drafts {
		if !draft.Saved {
			return fail("部分平台写入未落库")
		}
	}
	task.LastStatus, task.LastError = "ok", ""
	if prevSession != "" && prevSession != g.SessionID {
		_ = h.deleteConversation(user.ID, prevSession)
	}
	if taskID != "" {
		h.deleteScheduledSessionsExcept(taskID, g.SessionID)
	}
	return nil
}

func scheduledAssistantPrompt(task assistantTask, mode assistantExecMode) string {
	prefix := "这是当前项目「" + task.ProjectID + "」下的无人值守定时任务「" + task.Name + "」。直接调用当前项目和个人已安装的 MCP，以及 agentflow__* 平台工具，读写立即生效。issues_save 把需求 id 放在 body.id（也可同时放 path.id）。" +
		"运行期间没有人回答问题或确认操作：不要向用户提问或等待确认，有歧义时按最合理、影响最小的理解执行并在汇报里写明假设；某项条件不满足时跳过该项并说明原因，继续处理其余项。" +
		"除非提示词明确要求，不要为完成任务新建或修改沙箱、角色、SOP、项目等平台配置。" +
		"GitHub 的查看、审查、合并 PR 和关闭 Issue 使用 workbench_github_* 工具，不要在终端里读取令牌直接调用 GitHub API。" +
		"全部完成后用自然语言汇报结果。"
	if mode.DeferSOP {
		prefix += " 不要调用 issues_dispatch、runs_start、schedule_jobs_run、schedule_jobs_save；需要派发 SOP 时请单独创建触发任务。"
	} else {
		prefix += " 与主页 AI 对话的完全权限模式使用相同的工具和执行权限；按提示词调用 SOP、平台工具、工作区及浏览器工具完成任务。"
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

func assistantTaskActiveKey(taskID string, userID int64) string {
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		return "task/" + taskID
	}
	return "task-user/" + strconv.FormatInt(userID, 10)
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

// scheduledRunModel resolves the model a scheduled run uses. The CodeBuddy
// model list is an in-memory cache that is empty after a restart, so it is
// refreshed with the owner's key before a model is rejected. A run never
// silently switches to another, possibly more expensive, model.
func scheduledRunModel(ctx context.Context, requested string, user domain.User) (string, error) {
	requested = strings.TrimSpace(requested)
	env := user.CodeBuddyInternetEnvironment
	model := assistantAllowedUserModel(requested, loadAssistantModels(ctx, env), user)
	if requested == "" || model == requested {
		return model, nil
	}
	if engine, _ := assistantModelEngine(requested); engine == assistantEngineCodeBuddy {
		refreshCLIModels(ctx, env, user.CodeBuddyAPIKey)
		if model = assistantAllowedUserModel(requested, loadAssistantModels(ctx, env), user); model == requested {
			return model, nil
		}
	}
	return "", fmt.Errorf("定时任务指定的模型 %s 在负责人的账号下不可用，本次没有运行；请在定时任务里改选可用的模型", requested)
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
