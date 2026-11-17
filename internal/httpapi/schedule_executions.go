package httpapi

import (
	"net/http"
	"strconv"
	"strings"

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
