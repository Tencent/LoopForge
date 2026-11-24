package httpapi

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/schedule"

	"github.com/gin-gonic/gin"
)

type scheduleHandlers struct {
	*deps
	notifications *notificationHandlers
	assistant     *assistantHandlers
}

// 路由: g.GET("/api/schedule-jobs", h.scheduleJobs)
//
//	g.PUT("/api/schedule-jobs", h.scheduleJobs)
//	g.POST("/api/schedule-jobs", h.scheduleJobs)
func (h *scheduleHandlers) scheduleJobs(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(200, h.visibleScheduleJobs(c.Request, h.repo.ScheduleJobs()))
	case http.MethodPut, http.MethodPost:
		var job domain.ScheduleJob
		if !decode(c, &job) {
			return
		}
		existingJob, _ := h.repo.ScheduleJob(job.ID)
		if job.ProjectID == "" {
			job.ProjectID = "platform"
		}
		// Saving by id must not let a writer of one project take over (or move)
		// another project's job.
		if existingJob.ID != "" {
			if !h.requireScheduleJob(c, existingJob, true) {
				return
			}
			if existingJob.ProjectID != job.ProjectID && !h.requireScheduleProject(c, job.ProjectID, true) {
				return
			}
		} else if !h.requireScheduleProject(c, job.ProjectID, true) {
			return
		}
		if !h.bindSharedSchedule(c, &job, existingJob) {
			return
		}
		if !job.SkipDispatch && strings.TrimSpace(job.Name) == "" {
			job.Name = "触发配置"
		}
		if strings.TrimSpace(job.Name) == "" {
			c.JSON(400, map[string]string{"error": "name is required"})
			return
		}
		job.Cron = strings.TrimSpace(job.Cron)
		job.AssistantPrompt = strings.TrimSpace(job.AssistantPrompt)
		job.AssistantModel = strings.TrimSpace(job.AssistantModel)
		job.References = domain.CloneAssistantReferences(job.References)
		job.WorkflowID = strings.TrimSpace(job.WorkflowID)
		source, err := domain.NormalizeScheduleSource(job.Source)
		if err != nil {
			c.JSON(400, map[string]string{"error": err.Error()})
			return
		}
		job.Source = source
		if job.SkipDispatch {
			if job.AssistantPrompt == "" && job.SharedTaskID == "" {
				c.JSON(400, map[string]string{"error": "AI 定时任务必须填写提示词"})
				return
			}
			job.TimeoutMinutes, err = domain.NormalizeScheduleTimeoutMinutes(job.TimeoutMinutes)
			if err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			job.WorkflowID = ""
			job.UseIssueWorkflow = false
			job.IssueStatuses = nil
			job.MaxDispatch = 0
			job.Source = ""
			if len(job.References) > 0 && job.SharedTaskID == "" {
				if h.assistant == nil {
					c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "AI 定时任务引用功能未就绪"})
					return
				}
				refs, err := h.assistant.normalizeScheduledReferences(job.ProjectID, job.References)
				if err != nil {
					c.JSON(400, map[string]string{"error": err.Error()})
					return
				}
				job.References = refs
			}
		} else {
			job.References = nil
			if job.MaxDispatch < 0 || job.MaxDispatch > domain.MaxScheduleDispatch {
				c.JSON(400, map[string]string{"error": "单次最多触发需求数必须为 1–10，不配置时默认为 10"})
				return
			}
			if job.WorkflowID == "" {
				c.JSON(400, map[string]string{"error": "触发任务必须选择默认 SOP"})
				return
			}
			job.AssistantPrompt = ""
			job.AssistantModel = ""
			job.TimeoutMinutes = 0
			job.IssueStatuses = domain.NormalizeScheduleIssueStatuses(job.IssueStatuses)
			if len(job.IssueStatuses) == 0 {
				job.IssueStatuses = append([]string{}, domain.DefaultScheduleIssueStatuses...)
			}
		}
		if job.WorkflowID != "" {
			if _, ok := h.repo.Workflow(job.WorkflowID); !ok {
				c.JSON(400, map[string]string{"error": "workflow not found"})
				return
			}
		}
		if job.Cron != "" {
			if _, err := parseAssistantCron(job.Cron); err != nil {
				c.JSON(400, map[string]string{"error": "Cron 表达式无效，需为 5 段（分 时 日 月 周，东八区），例如 0 * * * *"})
				return
			}
			job.IntervalSec = 0
		} else if job.IntervalSec < 30 {
			job.IntervalSec = 300
		}
		requestedModel := job.AssistantModel
		if job.SkipDispatch {
			job.AssistantModel = assistantScheduledModel(job.AssistantModel, assistantFixedCatalog())
		}
		if job.ProjectID == "" {
			job.ProjectID = "platform"
		}
		// The job runs with its owner's key and identity; never take that from
		// the request.
		if project, ok := h.repo.Project(job.ProjectID); ok && project.OwnerID > 0 {
			job.OwnerUserID = project.OwnerID
		} else if job.SharedTaskID == "" {
			job.OwnerUserID = existingJob.OwnerUserID
		}
		if !h.bindScheduleRunAs(c, &job, existingJob, requestedModel) {
			return
		}
		if job.ID == "" {
			job.ID = "job-" + time.Now().UTC().Format("20060102150405.000000000")
		} else if existing, ok := h.repo.ScheduleJob(job.ID); ok {
			if job.LastRunAt.IsZero() {
				job.LastRunAt = existing.LastRunAt
				job.LastStatus = existing.LastStatus
				job.LastError = existing.LastError
				job.LastPulled = existing.LastPulled
				job.LastDispatched = existing.LastDispatched
				job.LastAssistantMessage = existing.LastAssistantMessage
			}
			if job.CreatedAt.IsZero() {
				job.CreatedAt = existing.CreatedAt
			}
			if job.OwnerUserID == 0 && job.SharedTaskID == "" {
				job.OwnerUserID = existing.OwnerUserID
			}
			if job.Blockers == nil {
				job.Blockers = existing.Blockers
			}
		}
		if job.Cron == "" && job.IntervalSec < 30 {
			job.IntervalSec = 300
		}

		prior, _ := h.repo.ScheduleJob(job.ID)
		if len(job.Notifications) > 0 || len(prior.Notifications) > 0 {
			if h.notifications == nil || !h.notifications.access(c, job.ProjectID, true) {
				return
			}
			if prior.ID != "" && prior.ProjectID != job.ProjectID && !h.notifications.access(c, prior.ProjectID, true) {
				return
			}
		}
		// Runtime fields and generated content cannot be forged by a config save.
		job.LastRunAt = prior.LastRunAt
		job.LastStatus = prior.LastStatus
		job.LastError = prior.LastError
		job.LastPulled = prior.LastPulled
		job.LastDispatched = prior.LastDispatched
		job.LastAssistantMessage = prior.LastAssistantMessage
		job.LastTraceID = prior.LastTraceID
		job.LastSessionID = prior.LastSessionID
		// The pinned conversation is chosen by the first fixed-mode run, never
		// by a save; leaving fixed mode unpins it, so re-enabling starts fresh.
		job.FixedSessionID = ""
		if job.SkipDispatch && strings.TrimSpace(job.SessionMode) == domain.ScheduleSessionFixed {
			job.SessionMode = domain.ScheduleSessionFixed
			if prior.UsesFixedSession() && prior.ProjectID == job.ProjectID {
				job.FixedSessionID = prior.FixedSessionID
			}
		} else {
			job.SessionMode = ""
		}
		// SessionKeep only limits jobs that open a conversation per execution;
		// a fixed-mode job keeps its choice for when it leaves fixed mode.
		if job.SkipDispatch {
			keep, err := domain.NormalizeScheduleSessionKeep(job.SessionKeep)
			if err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			job.SessionKeep = keep
		} else {
			job.SessionKeep = 0
		}
		job.NotificationSnapshot = prior.NotificationSnapshot
		job.NotificationResult = prior.NotificationResult
		var saveErr error
		if job.SkipDispatch {
			saveErr = h.repo.PutScheduleJob(job)
		} else {
			saveErr = h.repo.PutProjectTriggerConfig(job)
		}
		if errors.Is(saveErr, repo.ErrProjectTriggerConfigExists) {
			c.JSON(http.StatusConflict, gin.H{"error": saveErr.Error()})
			return
		}
		if saveErr != nil {
			respond(c, saveErr)
			return
		}
		stored, _ := h.repo.ScheduleJob(job.ID)
		visible := h.visibleScheduleJobs(c.Request, []domain.ScheduleJob{stored})
		if len(visible) == 1 {
			c.JSON(200, visible[0])
			return
		}
		c.JSON(200, stored)
	}
}

// bindScheduleRunAs binds an AI job to the user who saves it: the creator, and
// later whoever edits it, so nobody can make a prompt run with another
// member's credentials. The binding is never taken from the request. The saver's
// default machine login or own key is used, else the platform's shared CodeBuddy key
// (limited to the models it allows, its default when requestedModel is empty);
// with neither the save is refused, since the job could not run. Without a
// signed-in user the job stays on the project owner's keys. A saver without
// a usable key may still stop the job when nothing it runs changes; only
// enabled is then turned off, since the name and notifications also go into
// the prompt and a stopped job can be run by hand. That is checked again
// under the row lock against the stored job, which another save may have
// edited or moved meanwhile; then the save gets 409. A false return means the
// response has been written.
func (h *scheduleHandlers) bindScheduleRunAs(c *gin.Context, job *domain.ScheduleJob, prior domain.ScheduleJob, requestedModel string) bool {
	job.RunAsUserID, job.RunAsName = 0, ""
	if !job.SkipDispatch {
		return true
	}
	current, ok := h.requestUser(c.Request, true)
	if !ok || current == nil || current.ID <= 0 {
		return true
	}
	self, ok := h.repo.User(current.ID)
	if !ok {
		return true
	}
	model := scheduledAssistantDefault(h.repo, self, requestedModel)
	if model != requestedModel && !scheduleRunConfigUnchanged(prior, *job) {
		job.AssistantModel = model
	}
	engine, _ := assistantModelEngine(job.AssistantModel)
	stopOnly := func() bool {
		if job.Enabled || !scheduleRunConfigUnchanged(prior, *job) {
			return false
		}
		stored, err := h.repo.DisableScheduleJob(prior.ID, func(current domain.ScheduleJob) bool {
			return scheduleStopAllowed(prior, *job, current)
		})
		if errors.Is(err, repo.ErrScheduleJobChanged) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return true
		}
		if err != nil {
			respond(c, err)
			return true
		}
		c.JSON(200, stored)
		return true
	}
	_, shared, ok := scheduleCredentialUser(h.repo, self, engine)
	if !ok {
		if stopOnly() {
			return false
		}
		if assistantModelUnspecified(model) {
			c.JSON(400, gin.H{"error": errFeatureCredentials.Error()})
			return false
		}
		c.JSON(400, gin.H{"error": scheduleMissingKeyError(engine)})
		return false
	}
	if shared != nil {
		model, allowed := assistantSponsoredModelFor(shared, scheduleSharedRequestedModel(requestedModel))
		if !allowed {
			if stopOnly() {
				return false
			}
			c.JSON(400, gin.H{"error": "你还没有配置个人 CodeBuddy API Key，定时任务会使用平台共享 Key，只能选平台管理员允许的模型（" + strings.Join(shared, "、") + "）；要用其他模型，请先在个人配置中填写自己的 Key"})
			return false
		}
		job.AssistantModel = model
	}
	job.RunAsUserID, job.RunAsName = self.ID, scheduleUserName(self)
	return true
}

// scheduleStopAllowed reports whether a keyless stop request, authorized
// against prior, may still turn off current, the job as stored under the lock:
// it must be in the project write access was checked for and run exactly what
// the request was checked against.
func scheduleStopAllowed(prior, request, current domain.ScheduleJob) bool {
	return current.ProjectID == prior.ProjectID && scheduleRunConfigUnchanged(current, request)
}

// scheduleRunConfigUnchanged reports whether job still runs exactly what the
// stored prior runs. A run can be started by hand while a job is stopped, so
// the binding may only be kept when nothing that is executed changed.
func scheduleRunConfigUnchanged(prior, job domain.ScheduleJob) bool {
	return prior.ID != "" && prior.ID == job.ID && prior.SkipDispatch &&
		prior.ProjectID == job.ProjectID &&
		prior.AssistantPrompt == job.AssistantPrompt &&
		prior.AssistantModel == job.AssistantModel &&
		prior.SharedTaskID == job.SharedTaskID &&
		prior.SharedTaskVersion == job.SharedTaskVersion &&
		maps.Equal(prior.ParameterValues, job.ParameterValues)
}

// scheduleSharedRequestedModel treats the CLI-default placeholder as "no
// model chosen", which the shared key resolves to its own default.
func scheduleSharedRequestedModel(model string) string {
	engine, id := assistantModelEngine(model)
	if engine == assistantEngineCodeBuddy {
		if codeBuddyIsDefaultModel(id) {
			return ""
		}
		return id
	}
	return strings.TrimSpace(model)
}

func scheduleMissingKeyError(engine string) string {
	if assistantSupportsRunnerLogin(engine) {
		return fmt.Sprintf("你还没有可用的 %s 凭据，请在默认执行机器启用账号登录（Runner %s 及以上），或在个人配置填写 API Key", assistantEngineLabel(engine), domain.RunnerVersionCLISettings)
	}
	if engine != assistantEngineCodeBuddy {
		return assistantEngineMissingKey(engine)
	}
	return "你还没有配置 CodeBuddy API Key，平台也没有配置共享 Key，定时任务无法执行：请先在个人配置中填写自己的 Key，或联系平台管理员配置共享 Key"
}

func scheduleUserName(user domain.User) string {
	return firstNonEmpty(strings.TrimSpace(user.LoginName), strings.TrimSpace(user.ChineseName), fmt.Sprintf("用户 %d", user.ID))
}

// 路由: g.DELETE("/api/schedule-jobs/:id", h.scheduleJob)
func (h *scheduleHandlers) scheduleJob(c *gin.Context) {
	// master 子树语义：仅 DELETE 单段路径执行删除，其余方法（含 GET 单个）
	// 落到 handler 末尾 http.NotFound（404 text/plain）。
	if c.Request.Method != http.MethodDelete {
		http.NotFound(c.Writer, c.Request)
		return
	}
	if job, ok := h.repo.ScheduleJob(c.Param("id")); ok && !h.requireScheduleJob(c, job, true) {
		return
	}
	if err := h.repo.DeleteScheduleJob(c.Param("id")); err != nil {
		respond(c, err)
		return
	}
	if h.assistant != nil {
		h.assistant.deleteScheduledSessionsForTask(c.Param("id"))
	}
	c.JSON(200, map[string]bool{"ok": true})
}

// scheduleJobEnabled starts or stops a job's schedule without a full save, so
// toggling never rewrites its prompt, key binding or session. A run already
// in progress finishes either way.
// 路由: g.POST("/api/schedule-jobs/:id/enabled", h.scheduleJobEnabled)
func (h *scheduleHandlers) scheduleJobEnabled(c *gin.Context) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(c, &body) {
		return
	}
	if body.Enabled == nil {
		c.JSON(400, gin.H{"error": "enabled is required"})
		return
	}
	job, ok := h.repo.ScheduleJob(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "schedule job not found"})
		return
	}
	if !h.requireScheduleJob(c, job, true) {
		return
	}
	saved, err := h.repo.SetScheduleJobEnabled(job.ID, *body.Enabled)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, saved)
}

// scheduleJobRun starts a scheduled job on the server and returns 202 with
// lastStatus=running. Closing the client does not cancel the run; poll GET
// /api/schedule-jobs until lastStatus leaves running.
// 路由: g.POST("/api/schedule-jobs/:id/run", h.scheduleJobRun)
func (h *scheduleHandlers) scheduleJobRun(c *gin.Context) {
	if !schedulerEnabled() {
		c.JSON(409, map[string]string{"error": "scheduler is disabled on this instance"})
		return
	}
	if h.schedule == nil {
		respond(c, fmt.Errorf("scheduler is unavailable"))
		return
	}
	if job, ok := h.repo.ScheduleJob(c.Param("id")); ok && !h.requireScheduleJob(c, job, true) {
		return
	}
	job, err := h.schedule.StartNow(c.Param("id"))
	if errors.Is(err, schedule.ErrAlreadyRunning) {
		c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusAccepted, job)
}

func (h *scheduleHandlers) dispatchScheduled(job domain.ScheduleJob, issue domain.Issue, triggerOrigin string) error {
	_, err := h.dispatchScheduledRun(job, issue, triggerOrigin)
	return err
}

// dispatchScheduledRun is dispatchScheduled reporting the started run ID for
// the execution record.
func (h *scheduleHandlers) dispatchScheduledRun(job domain.ScheduleJob, issue domain.Issue, triggerOrigin string) (string, error) {
	task, err := dispatchIssue(h.deps, dispatchRequest{IssueID: issue.ID, WorkflowID: job.ResolveWorkflow(issue), ScheduleJobID: job.ID, TriggerOrigin: triggerOrigin})
	return task.RunID, err
}

func (h *scheduleHandlers) guardScheduledBaseline(job domain.ScheduleJob, issues []domain.Issue) (map[string]domain.ScheduleJobBlocker, error) {
	blocked := map[string]domain.ScheduleJobBlocker{}
	seen := map[string]bool{}
	for _, issue := range issues {
		repo, ok := h.issueRepository(issue)
		if !ok || seen[repo.ID] {
			continue
		}
		seen[repo.ID] = true
		ctx := scheduledRepositoryContext(repo)
		responsible, responsibleErr := h.repo.ResponsibleUser(issue)
		if responsibleErr != nil {
			blocked[repo.ID] = domain.ScheduleJobBlocker{
				RepositoryID:   repo.ID,
				RepositoryName: repo.Name,
				Reason:         responsibleErr.Error(),
			}
			continue
		}
		err := h.engine.AlignBaselineForUser(ctx, responsible.ID)
		if err == nil {
			h.cancelStaleAlignRuns(repo.ID)
			continue
		}
		var conflict *engine.AlignConflictError
		if errors.As(err, &conflict) {
			blocked[repo.ID] = h.blockerFromConflict(job, repo, conflict, responsible.ID)
			continue
		}
		blocked[repo.ID] = domain.ScheduleJobBlocker{
			RepositoryID:   repo.ID,
			RepositoryName: repo.Name,
			Reason:         err.Error(),
		}
	}
	return blocked, nil
}

// blockerFromConflict turns an AlignConflictError into a structured blocker
// and makes sure an alignment run exists, linking its ID so the UI can jump
// to the auto-resolve run.
func (h *scheduleHandlers) blockerFromConflict(job domain.ScheduleJob, repo domain.Repository, conflict *engine.AlignConflictError, responsibleUserID int64) domain.ScheduleJobBlocker {
	blocker := domain.ScheduleJobBlocker{
		RepositoryID:      repo.ID,
		RepositoryName:    repo.Name,
		IntegrateBranch:   domain.NormalizeIntegrateBranch(repo.IntegrateBranch),
		DefaultBranch:     strings.TrimSpace(repo.DefaultBranch),
		ConflictFiles:     append([]string(nil), conflict.Files...),
		ConflictBlocks:    conflict.Blocks,
		Reason:            conflict.Error(),
		ResponsibleUserID: responsibleUserID,
	}
	if runID, err := h.startBaselineAlignRun(job, repo, conflict, responsibleUserID); err != nil {
		log.Printf("baseline align run start failed: repo=%s: %v", repo.ID, err)
		blocker.Reason = blocker.Reason + "；对齐 Run 启动失败：" + err.Error()
	} else if runID != "" {
		blocker.AlignRunID = runID
	} else if runID, ok := h.activeAlignRunID(repo.ID); ok {
		blocker.AlignRunID = runID
	}
	return blocker
}

func (h *scheduleHandlers) issueRepository(issue domain.Issue) (domain.Repository, bool) {
	id := strings.TrimSpace(issue.RepositoryID)
	if id == "" {
		if p, ok := h.repo.Project(issue.ProjectID); ok {
			id = strings.TrimSpace(p.DefaultRepositoryID)
		}
	}
	if id == "" {
		return domain.Repository{}, false
	}
	repo, ok := h.repo.Repository(id)
	if !ok || repo.ProjectID != issue.ProjectID {
		return domain.Repository{}, false
	}
	return repo, true
}

func scheduledRepositoryContext(repo domain.Repository) domain.RepositoryContext {
	integ := domain.NormalizeIntegrateBranch(repo.IntegrateBranch)
	base := integ
	if base == "" {
		base = strings.TrimSpace(repo.DefaultBranch)
	}
	return domain.RepositoryContext{
		RepositoryID:      repo.ID,
		Provider:          repo.Provider,
		Name:              repo.Name,
		GongfengProjectID: repo.GongfengProjectID,
		WebURL:            repo.WebURL,
		CloneURL:          repo.CloneURL,
		LocalPath:         repo.LocalPath,
		BaseBranch:        base,
		IntegrateBranch:   integ,
		DefaultBranch:     repo.DefaultBranch,
	}
}

func (h *scheduleHandlers) startBaselineAlignRun(job domain.ScheduleJob, repo domain.Repository, conflict *engine.AlignConflictError, responsibleUserID int64) (string, error) {
	if h.engine == nil {
		return "", fmt.Errorf("engine is not configured")
	}
	if h.alignRunActive(repo.ID) {
		if id, ok := h.activeAlignRunID(repo.ID); ok {
			return id, nil
		}
		return "", fmt.Errorf("align run already active")
	}
	if !h.ensureBaselineAlignWorkflow(job) {
		return "", fmt.Errorf("ensure align workflow failed")
	}
	files := "unknown"
	if conflict != nil && len(conflict.Files) > 0 {
		files = strings.Join(conflict.Files, ", ")
	}
	integ := domain.NormalizeIntegrateBranch(repo.IntegrateBranch)
	def := strings.TrimSpace(repo.DefaultBranch)
	var blocksMD string
	if conflict != nil {
		blocksMD = conflict.RenderBlocks()
	}
	if blocksMD == "" {
		blocksMD = "（冲突块内容未能提取，请以 git merge 实际输出为准）"
	}
	input := fmt.Sprintf(`对齐仓库 %s 的集成分支 %s 与默认分支 %s。冲突文件：%s。

## 执行要求（严格，禁止只输出计划）
1. 进入本次对齐仓库的检出目录，在当前工作分支执行 git fetch origin %s && git merge origin/%s
2. 逐个解决下方列出的冲突块：原则是保留双方有效实现；不确定取舍时选择对业务影响最小的方案并在提交说明里标注
3. 全部解决后 git add 并 git commit，提交说明必须列出冲突文件与每个文件的取舍
4. 不要 push（平台在校验通过后自动推送并创建基线 MR）；不要改动默认分支 %s
5. 后续 verify 节点会校验 HEAD 必须是合并提交、无未解决冲突和残留标记，未实际执行会被打回

## 冲突内容（ours=集成分支，theirs=默认分支）
%s`, repo.Name, integ, def, files, def, def, def, blocksMD)
	ctx := scheduledRepositoryContext(repo)
	ctx.AlignOnly = true
	ctx.WorkBranch = "agentflow/align-default"
	run, err := h.engine.StartWithExecutionContext(engine.BaselineAlignWorkflowID, input, ctx, domain.TelemetryContext{
		ProjectID:         repo.ProjectID,
		ResponsibleUserID: responsibleUserID,
		ScheduleJobID:     job.ID,
	})
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

const baselineAlignWorkflowVersion = 8

func alignWorkflowUsesPlatformAgent(wf domain.WorkflowDefinition) bool {
	for _, n := range wf.Nodes {
		if n.ID == "resolve" && n.Type == "agent" && n.AgentID == engine.BaselineAlignAgentID {
			return true
		}
	}
	return false
}

// baselineAlignVerifyScript is the hard gate after the resolve agent: the run
// only finishes (and the platform pushes the baseline) when a real merge
// commit exists and no unresolved conflicts / markers remain. Plan-only
// output is rejected back to the resolve node automatically.
// git diff --check also reports trailing whitespace; only leftover conflict
// markers fail the gate, so a whitespace-only warning cannot stall the schedule.
const baselineAlignVerifyScript = `set -e
if git diff --name-only --diff-filter=U | grep -q .; then echo "仍有未解决冲突（git status 显示 unmerged）"; exit 1; fi
p=$(git log -1 --format=%P 2>/dev/null || true)
case "$p" in *\ *) ;; *) echo "HEAD 不是合并提交：尚未实际执行 git merge，只输出了计划"; exit 1;; esac
check=$(git diff --check HEAD^ HEAD || true)
markers=$(printf '%s\n' "$check" | grep 'leftover conflict marker' || true)
if [ -n "$markers" ]; then
  echo "提交中残留冲突标记，请逐块解决后再提交"
  printf '%s\n' "$check"
  exit 1
fi
if [ -n "$check" ]; then
  echo "提示：合并提交含空白类 diff --check 告警，不阻断基线合入"
  printf '%s\n' "$check"
fi
echo "校验通过：HEAD 为合并提交，无未解决冲突与残留标记"`

func (h *scheduleHandlers) ensureBaselineAlignAgent() bool {
	if err := h.repo.PutAgent(repo.BaselineAlignResolverAgent()); err != nil {
		log.Printf("baseline align agent upsert failed: %v", err)
		return false
	}
	return true
}

func (h *scheduleHandlers) ensureBaselineAlignWorkflow(_ domain.ScheduleJob) bool {
	if !h.ensureBaselineAlignAgent() {
		return false
	}
	if wf, ok := h.repo.Workflow(engine.BaselineAlignWorkflowID); ok {
		if wf.Version >= baselineAlignWorkflowVersion && alignWorkflowUsesPlatformAgent(wf) {
			return true
		}
	}
	if _, ok := h.repo.Sandbox(repo.DefaultSandboxProfileID); !ok {
		return false
	}
	now := time.Now().UTC()
	wf := domain.WorkflowDefinition{
		ID:               engine.BaselineAlignWorkflowID,
		Name:             "对齐默认分支到基线",
		ProjectID:        "platform",
		Version:          baselineAlignWorkflowVersion,
		Status:           "published",
		Description:      "定时任务开工前把默认分支合入集成分支。全平台共用对齐角色实际执行 merge，平台校验后自动合入基线，无需人工确认。",
		SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{
			{ID: "resolve", Name: "解决冲突", Type: "agent", AgentID: engine.BaselineAlignAgentID, MaxAttempts: 2, MaxReworks: 2, RejectTo: "resolve", Position: domain.Position{X: 80, Y: 80}},
			{
				ID: "verify", Name: "校验真实合并", Type: "script",
				ScriptLanguage: "shell", Script: baselineAlignVerifyScript, ExecutionScope: domain.ExecutionScopeRepositories,
				MaxAttempts: 1, MaxReworks: 2, OnFailure: "reject", RejectTo: "resolve", Position: domain.Position{X: 360, Y: 80},
			},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "e-align-1", Source: "resolve", Target: "verify"},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if old, ok := h.repo.Workflow(engine.BaselineAlignWorkflowID); ok {
		wf.CreatedAt = old.CreatedAt
		wf.Revision = old.Revision
	}
	if err := h.repo.PutWorkflow(wf); err != nil {
		log.Printf("baseline align workflow upsert failed: %v", err)
		return false
	}
	return true
}

func (h *scheduleHandlers) alignRunActive(repoID string) bool {
	_, ok := h.activeAlignRunID(repoID)
	return ok
}

// activeAlignRunID returns the ID of the pending (running/waiting) baseline
// alignment run for a repository, if any.
func (h *scheduleHandlers) activeAlignRunID(repoID string) (string, bool) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" {
		return "", false
	}
	for _, run := range h.repo.Runs() {
		if run.WorkflowID != engine.BaselineAlignWorkflowID || run.Repository.RepositoryID != repoID {
			continue
		}
		if run.Status == "running" || run.Status == "waiting" {
			return run.ID, true
		}
	}
	return "", false
}

func (h *scheduleHandlers) cancelStaleAlignRuns(repoID string) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" || h.engine == nil {
		return
	}
	for _, run := range h.repo.Runs() {
		if run.WorkflowID != engine.BaselineAlignWorkflowID || run.Repository.RepositoryID != repoID {
			continue
		}
		if run.Status == "running" || run.Status == "waiting" {
			_ = h.engine.Cancel(run.ID)
		}
	}
}
