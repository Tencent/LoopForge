package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"agentflow/internal/attachment"
	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// A runner can download only files of the requirement linked to its current,
// unexpired task lease. The URL never selects an uploader or a disk path.
func (h *runnersHandlers) runnerRequirementAttachment(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	task, ok := h.repo.RunnerTask(c.Param("id"))
	attempt, err := strconv.Atoi(c.GetHeader("X-Task-Attempt"))
	if !ok || err != nil || attempt != task.Attempt || task.LeaseOwner != runner.ID || task.LeaseUntil == nil || !task.LeaseUntil.After(time.Now()) || task.DesiredState == domain.RunnerTaskDesiredCancel || (task.Status != domain.RunnerTaskLeased && task.Status != domain.RunnerTaskRunning) {
		c.JSON(409, gin.H{"error": "task lease lost"})
		return
	}
	run, ok := h.repo.RunState(task.RunID)
	if !ok || task.Invocation == nil {
		c.JSON(404, gin.H{"error": "attachment unavailable"})
		return
	}
	issue, ok := h.repo.Issue(run.ExecutionContext.IssueID)
	if !ok || issue.ProjectID != run.ExecutionContext.ProjectID {
		c.JSON(404, gin.H{"error": "attachment unavailable"})
		return
	}
	id := c.Param("attachmentId")
	var allowed bool
	for _, ref := range task.Invocation.RequirementAttachments {
		if ref.ID == id {
			allowed = true
			break
		}
	}
	if allowed {
		for _, a := range issue.Attachments {
			if a.ID == id {
				data, err := attachment.Read(a, issue.ProjectID)
				if err != nil {
					c.JSON(404, gin.H{"error": "attachment unavailable"})
					return
				}
				c.Header("Cache-Control", "no-store")
				c.Header("X-Content-Type-Options", "nosniff")
				c.Data(http.StatusOK, "application/octet-stream", data)
				return
			}
		}
	}
	c.JSON(404, gin.H{"error": "attachment unavailable"})
}
