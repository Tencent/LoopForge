package httpapi

import (
	"net/http"
	"strconv"
	"strings"

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
	c.JSON(http.StatusOK, h.repo.ScheduleJobExecutions(repo.ScheduleJobExecutionQuery{
		JobIDs:  ids,
		Status:  strings.TrimSpace(c.Query("status")),
		TraceID: strings.TrimSpace(c.Query("traceId")),
		Limit:   limit,
	}))
}
