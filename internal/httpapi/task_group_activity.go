package httpapi

import (
	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

// groupActivity provides execution facts without exposing raw tool arguments,
// artifact contents or archived output prose. Register as GET group/activity.
func (h *runsHandlers) groupActivity(c *gin.Context) {
	run, ok := h.repo.RunView(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	if _, ok = h.groupUser(c, run.ExecutionContext.ProjectID); !ok {
		return
	}
	full, ok := h.repo.RunGroupActivityState(run.ID)
	if !ok {
		c.JSON(404, gin.H{"error": "任务不存在"})
		return
	}
	c.JSON(200, domain.BuildGroupActivity(full, h.repo.Agents()))
}
