package httpapi

import (
	"errors"
	"fmt"
	"log"
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
}

// 路由: g.GET("/api/schedule-jobs", h.scheduleJobs)
//
//	g.PUT("/api/schedule-jobs", h.scheduleJobs)
//	g.POST("/api/schedule-jobs", h.scheduleJobs)
func (h *scheduleHandlers) scheduleJobs(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(200, h.repo.ScheduleJobs())
	case http.MethodPut, http.MethodPost:
		var job domain.ScheduleJob
		if !decode(c, &job) {
			return
		}
		if strings.TrimSpace(job.Name) == "" {
			c.JSON(400, map[string]string{"error": "name is required"})
			return
		}
		job.Cron = strings.TrimSpace(job.Cron)
		job.AssistantPrompt = strings.TrimSpace(job.AssistantPrompt)
		job.AssistantModel = strings.TrimSpace(job.AssistantModel)
		job.WorkflowID = strings.TrimSpace(job.WorkflowID)
		source, err := domain.NormalizeScheduleSource(job.Source)
		if err != nil {
			c.JSON(400, map[string]string{"error": err.Error()})
			return
		}
		job.Source = source
		if job.SkipDispatch {
			if job.AssistantPrompt == "" {
				c.JSON(400, map[string]string{"error": "AI 定时任务必须填写提示词"})
				return
			}
			job.WorkflowID = ""
			job.UseIssueWorkflow = false
			job.IssueStatuses = nil
			job.Source = ""
		} else {
			if job.WorkflowID == "" {
				c.JSON(400, map[string]string{"error": "触发任务必须选择默认 SOP"})
				return
			}
			job.AssistantPrompt = ""
			job.AssistantModel = ""
			job.IssueStatuses = domain.NormalizeScheduleIssueStatuses(job.IssueStatuses)
			if len(job.IssueStatuses) == 0 {
				job.IssueStatuses = append([]string{}, domain.DefaultScheduleIssueStatuses...)
			}
		}
		if len(job.AssistantPrompt) > 4000 {
			c.JSON(400, map[string]string{"error": "assistantPrompt is too long"})
			return
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
		if job.SkipDispatch {
			job.AssistantModel = assistantAllowedModel(job.AssistantModel, loadAssistantModels(c.Request.Context(), ""))
		}
		if job.ProjectID == "" {
			job.ProjectID = "platform"
		}
		if project, ok := h.repo.Project(job.ProjectID); ok && project.OwnerID > 0 {
			job.OwnerUserID = project.OwnerID
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
			if job.OwnerUserID == 0 {
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
		job.NotificationSnapshot = prior.NotificationSnapshot
		job.NotificationResult = prior.NotificationResult
		if err := h.repo.PutScheduleJob(job); err != nil {
			respond(c, err)
			return
		}
		stored, _ := h.repo.ScheduleJob(job.ID)
		c.JSON(200, stored)
	}
}

// 路由: g.DELETE("/api/schedule-jobs/:id", h.scheduleJob)
func (h *scheduleHandlers) scheduleJob(c *gin.Context) {
	// master 子树语义：仅 DELETE 单段路径执行删除，其余方法（含 GET 单个）
	// 落到 handler 末尾 http.NotFound（404 text/plain）。
	if c.Request.Method != http.MethodDelete {
		http.NotFound(c.Writer, c.Request)
		return
	}
	if err := h.repo.DeleteScheduleJob(c.Param("id")); err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, map[string]bool{"ok": true})
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
	_, err := dispatchIssue(h.deps, dispatchRequest{IssueID: issue.ID, WorkflowID: job.ResolveWorkflow(issue), ScheduleJobID: job.ID, TriggerOrigin: triggerOrigin})
	return err
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
		err := h.engine.AlignBaseline(ctx, engine.ResolveClarifyGitToken(h.repo, responsible.ID))
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
1. 在当前工作分支执行 git fetch origin %s && git merge origin/%s
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

const baselineAlignWorkflowVersion = 4

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
const baselineAlignVerifyScript = `set -e
if git diff --name-only --diff-filter=U | grep -q .; then echo "仍有未解决冲突（git status 显示 unmerged）"; exit 1; fi
p=$(git log -1 --format=%P 2>/dev/null || true)
case "$p" in *\ *) ;; *) echo "HEAD 不是合并提交：尚未实际执行 git merge，只输出了计划"; exit 1;; esac
if git diff --check HEAD^ HEAD | grep -q .; then echo "提交中残留冲突标记，请逐块解决后再提交"; exit 1; fi
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
	if _, ok := h.repo.Sandbox("local"); !ok {
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
		SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "resolve", Name: "解决冲突", Type: "agent", AgentID: engine.BaselineAlignAgentID, MaxAttempts: 2, MaxReworks: 2, RejectTo: "resolve", Position: domain.Position{X: 80, Y: 80}},
			{ID: "verify", Name: "校验真实合并", Type: "script", ScriptLanguage: "shell", Script: baselineAlignVerifyScript, MaxAttempts: 1, MaxReworks: 2, OnFailure: "reject", RejectTo: "resolve", Position: domain.Position{X: 360, Y: 80}},
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
