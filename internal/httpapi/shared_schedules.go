package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// scheduleProjectAllowed guards every schedule job: AI jobs run as the project
// Owner with full access, so only the project's writers may create, edit, run
// or delete them, and only its members may read their prompts.
func (h *scheduleHandlers) scheduleProjectAllowed(r *http.Request, id string, write bool) bool {
	user, authenticated := h.requestUser(r, true)
	if !authenticated || user == nil {
		return !authRequired()
	}
	project, ok := h.repo.Project(id)
	if !ok {
		// A job can outlive its project (or use the default "platform" id);
		// only platform admins manage those.
		return h.isPlatformAdmin(r)
	}
	if h.isPlatformAdmin(r) || project.OwnerID == user.ID {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == user.ID && (!write || !strings.EqualFold(member.Role, "viewer")) {
			return true
		}
	}
	return false
}
func (h *scheduleHandlers) requireScheduleProject(c *gin.Context, id string, write bool) bool {
	if h.scheduleProjectAllowed(c.Request, id, write) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "没有该项目的定时任务访问权限"})
	return false
}

// scheduleJobAllowed also lets the stored owner keep managing a job whose
// project record is gone: such a job runs as that owner, nobody else.
func (h *scheduleHandlers) scheduleJobAllowed(r *http.Request, job domain.ScheduleJob, write bool) bool {
	if h.scheduleProjectAllowed(r, job.ProjectID, write) {
		return true
	}
	if _, exists := h.repo.Project(job.ProjectID); exists || job.OwnerUserID == 0 {
		return false
	}
	user, ok := h.requestUser(r, true)
	return ok && user != nil && user.ID == job.OwnerUserID
}
func (h *scheduleHandlers) requireScheduleJob(c *gin.Context, job domain.ScheduleJob, write bool) bool {
	if h.scheduleJobAllowed(c.Request, job, write) {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "没有该项目的定时任务访问权限"})
	return false
}
func (h *scheduleHandlers) visibleScheduleJobs(r *http.Request, jobs []domain.ScheduleJob) []domain.ScheduleJob {
	out := make([]domain.ScheduleJob, 0, len(jobs))
	for _, job := range jobs {
		if h.scheduleJobAllowed(r, job, false) {
			out = append(out, job)
		}
	}
	return out
}
func (h *scheduleHandlers) sharedScheduleTasks(c *gin.Context) {
	if authRequired() {
		if _, ok := h.requestUser(c.Request, true); !ok {
			c.JSON(401, gin.H{"error": "请先登录"})
			return
		}
	}
	items, err := h.repo.SharedScheduleTasks()
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, items)
}
func (h *scheduleHandlers) sharedScheduleVersions(c *gin.Context) {
	if authRequired() {
		if _, ok := h.requestUser(c.Request, true); !ok {
			c.JSON(401, gin.H{"error": "请先登录"})
			return
		}
	}
	items, err := h.repo.SharedScheduleVersions(c.Param("id"))
	if err != nil {
		respond(c, err)
		return
	}
	if len(items) == 0 {
		c.JSON(404, gin.H{"error": "共享任务不存在"})
		return
	}
	c.JSON(200, items)
}
func (h *scheduleHandlers) publishSharedSchedule(c *gin.Context) {
	job, ok := h.repo.ScheduleJob(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "定时任务不存在"})
		return
	}
	if _, exists := h.repo.Project(job.ProjectID); !exists {
		c.JSON(http.StatusForbidden, gin.H{"error": "没有该项目的定时任务访问权限"})
		return
	}
	if !h.requireScheduleProject(c, job.ProjectID, true) {
		return
	}
	if !job.SkipDispatch || job.SharedTaskID != "" {
		c.JSON(400, gin.H{"error": "仅独立 AI 定时任务可以发布共享版本"})
		return
	}
	var input struct {
		Name        string                     `json:"name"`
		Description string                     `json:"description"`
		Prompt      string                     `json:"prompt"`
		Parameters  []domain.ScheduleParameter `json:"parameters"`
		ChangeNote  string                     `json:"changeNote"`
		BaseVersion *int                       `json:"baseVersion"`
	}
	if !decode(c, &input) {
		return
	}
	if input.BaseVersion == nil || *input.BaseVersion < 0 {
		c.JSON(400, gin.H{"error": "请填写 baseVersion：首次发布为 0，之后为最新版本号"})
		return
	}
	if input.Parameters == nil {
		input.Parameters = []domain.ScheduleParameter{}
	}
	project, _ := h.repo.Project(job.ProjectID)
	v := domain.SharedScheduleVersion{Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), Prompt: strings.TrimSpace(input.Prompt), Parameters: input.Parameters, ChangeNote: strings.TrimSpace(input.ChangeNote), Model: job.AssistantModel, Cron: job.Cron, IntervalSec: job.IntervalSec, SourceProjectName: project.Name}
	if v.Name == "" {
		v.Name = job.Name
	}
	if v.Prompt == "" {
		v.Prompt = job.AssistantPrompt
	}
	published, err := h.repo.PublishSharedSchedule(job.ID, job.ProjectID, v, *input.BaseVersion)
	if errors.Is(err, repo.ErrSharedScheduleVersionConflict) {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(201, published)
}

// Validate the binding at save time; the scheduler resolves this same immutable
// version again at run time instead of trusting a client-supplied prompt copy.
func (h *scheduleHandlers) bindSharedSchedule(c *gin.Context, job *domain.ScheduleJob, prior domain.ScheduleJob) bool {
	if job.SharedTaskID == "" && prior.SharedTaskID == "" {
		return true
	}
	if job.ProjectID == "" {
		job.ProjectID = "platform"
	}
	// A subscription runs as the target project's Owner, so the project must exist.
	if _, exists := h.repo.Project(job.ProjectID); !exists {
		c.JSON(http.StatusForbidden, gin.H{"error": "没有该项目的定时任务访问权限"})
		return false
	}
	if !h.requireScheduleProject(c, job.ProjectID, true) {
		return false
	}
	if prior.ID != "" && prior.ProjectID != job.ProjectID && !h.requireScheduleProject(c, prior.ProjectID, true) {
		return false
	}
	if job.SharedTaskID == "" || !job.SkipDispatch || job.SharedTaskVersion < 1 {
		c.JSON(400, gin.H{"error": "订阅需要明确的共享任务和版本，且必须是 AI 定时任务"})
		return false
	}
	v, err := h.repo.SharedScheduleVersion(job.SharedTaskID, job.SharedTaskVersion)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "共享任务版本不存在"})
		return false
	}
	if err != nil {
		respond(c, err)
		return false
	}
	_, values, err := v.Render(job.ParameterValues)
	if err != nil {
		respond(c, err)
		return false
	}
	project, _ := h.repo.Project(job.ProjectID)
	job.OwnerUserID = project.OwnerID
	job.ParameterValues = values
	job.AssistantPrompt = ""
	if job.Name == "" {
		job.Name = v.Name
	}
	if job.AssistantModel == "" {
		job.AssistantModel = v.Model
	}
	if job.ID == "" && job.Cron == "" && job.IntervalSec == 0 {
		job.Cron = v.Cron
		job.IntervalSec = v.IntervalSec
	}
	return true
}
