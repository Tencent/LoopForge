package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

// scheduleJobExecutions lists the execution history of the schedule jobs the
// caller may read, newest first. Filters: projectId, jobId, status, traceId,
// limit (default 100, max 500).
// 路由: g.GET("/api/schedule-job-executions", h.scheduleJobExecutions)
func (h *scheduleHandlers) scheduleJobExecutions(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	jobID := strings.TrimSpace(c.Query("jobId"))
	ids := []string{}
	for _, job := range h.visibleScheduleJobs(c.Request, h.repo.ScheduleJobs()) {
		if projectID != "" && job.ProjectID != projectID {
			continue
		}
		if jobID != "" && job.ID != jobID {
			continue
		}
		ids = append(ids, job.ID)
	}
	if jobID != "" && len(ids) == 0 {
		if _, exists := h.repo.ScheduleJob(jobID); exists {
			c.JSON(http.StatusForbidden, gin.H{"error": "没有该项目的定时任务访问权限"})
			return
		}
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	items := h.repo.ScheduleJobExecutions(repo.ScheduleJobExecutionQuery{
		JobIDs:  ids,
		Status:  strings.TrimSpace(c.Query("status")),
		TraceID: strings.TrimSpace(c.Query("traceId")),
		Limit:   limit,
	})
	c.JSON(http.StatusOK, h.scheduleExecutionViews(items))
}

// scheduleExecutionDashboard is one page of the execution dashboard. Total
// counts the executions matching the filters; Overview covers the job filter
// only; Records and RecentFailed cover the whole project (the tab badge).
type scheduleExecutionDashboard struct {
	Items        []scheduleJobExecutionView `json:"items"`
	Total        int                        `json:"total"`
	Overview     scheduleExecutionOverview  `json:"overview"`
	Records      int                        `json:"records"`
	RecentFailed int                        `json:"recentFailed"`
}

type scheduleExecutionOverview struct {
	Current  domain.ScheduleExecutionWindow `json:"current"`
	Previous domain.ScheduleExecutionWindow `json:"previous"`
	// Since starts the current window; windows are counted by the hour an
	// execution started.
	Since time.Time `json:"since"`
}

// scheduleJobExecutionDashboard filters, counts and pages the executions of a
// project on the server, so neither the overview nor the filters depend on
// how many executions a page loads. Query: projectId (required), jobId,
// status, since (RFC 3339), q, offset, limit (default 20, max 100).
// 路由: g.GET("/api/schedule-job-executions/dashboard", h.scheduleJobExecutionDashboard)
func (h *scheduleHandlers) scheduleJobExecutionDashboard(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定项目"})
		return
	}
	var since time.Time
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "since 需要 RFC 3339 时间"})
			return
		}
		since = parsed
	}
	jobID := strings.TrimSpace(c.Query("jobId"))
	project := []string{}
	scoped := []string{}
	for _, job := range h.visibleScheduleJobs(c.Request, h.repo.ScheduleJobs()) {
		if job.ProjectID != projectID {
			continue
		}
		project = append(project, job.ID)
		if jobID == "" || job.ID == jobID {
			scoped = append(scoped, job.ID)
		}
	}
	if jobID != "" && len(scoped) == 0 {
		if job, exists := h.repo.ScheduleJob(jobID); exists && !h.scheduleJobAllowed(c.Request, job, false) {
			c.JSON(http.StatusForbidden, gin.H{"error": "没有该项目的定时任务访问权限"})
			return
		}
	}
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, total := h.repo.ScheduleJobExecutionPage(repo.ScheduleJobExecutionPageQuery{
		JobIDs: scoped,
		Status: strings.TrimSpace(c.Query("status")),
		Since:  since,
		Search: c.Query("q"),
		Offset: offset,
		Limit:  limit,
	})
	now := time.Now().UTC()
	current, previous := h.repo.ScheduleJobExecutionOverview(scoped, now)
	recentFailed := current.Failed
	if jobID != "" {
		whole, _ := h.repo.ScheduleJobExecutionOverview(project, now)
		recentFailed = whole.Failed
	}
	c.JSON(http.StatusOK, scheduleExecutionDashboard{
		Items:        h.scheduleExecutionViews(items),
		Total:        total,
		Overview:     scheduleExecutionOverview{Current: current, Previous: previous, Since: now.Truncate(time.Hour).Add(-167 * time.Hour)},
		Records:      h.repo.CountScheduleJobExecutions(project),
		RecentFailed: recentFailed,
	})
}

// scheduleJobExecutionView is an execution as listed. SessionGone is computed
// on every read, never stored: executions outlive the conversations a job
// keeps, so an old row may point at a conversation that has been cleaned up.
type scheduleJobExecutionView struct {
	domain.ScheduleJobExecution
	SessionGone bool `json:"sessionGone,omitempty"`
}

func (h *scheduleHandlers) scheduleExecutionViews(items []domain.ScheduleJobExecution) []scheduleJobExecutionView {
	var sessionIDs []string
	for _, e := range items {
		// A running execution may record its conversation just before saving it.
		if e.SessionID != "" && e.Status != "running" {
			sessionIDs = append(sessionIDs, e.SessionID)
		}
	}
	exists := map[string]bool{}
	if len(sessionIDs) > 0 {
		exists = h.repo.ExistingAssistantSessionIDs(sessionIDs)
	}
	out := make([]scheduleJobExecutionView, len(items))
	for i, e := range items {
		out[i] = scheduleJobExecutionView{ScheduleJobExecution: e}
		if alive, checked := exists[e.SessionID]; checked && e.Status != "running" {
			out[i].SessionGone = !alive
		}
	}
	return out
}
