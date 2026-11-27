package httpapi

import (
	"net/http"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func (h *projectsHandlers) setExecutionTarget(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return
	}
	projectID := c.Param("id")
	project, found := h.repo.Project(projectID)
	if !found || project.ArchivedAt != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "请选择有效的项目"})
		return
	}
	if !h.isPlatformAdmin(c.Request) && !h.projectMemberWrites(projectID, user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权修改此项目的执行调度"})
		return
	}
	if !requireWriteOrigin(c) {
		return
	}
	var target domain.ExecutionTarget
	if !decodeLimit(c, &target, 4096) {
		return
	}
	if !target.ValidSelection() || target.Pool == domain.ExecutionTargetProject {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择一种有效的项目执行方式"})
		return
	}
	ctx := c.Request.Context()
	switch target.Pool {
	case domain.ExecutionTargetCluster:
		if _, err := h.repo.AllowedExecutionCluster(ctx, target.ClusterID, projectID, true); err != nil {
			respond(c, err)
			return
		}
	case domain.ExecutionModeRunner:
		runner, found := h.repo.Runner(target.RunnerID)
		if !found || !runner.SharedWithProject(projectID) || !project.HasWritableMember(runner.OwnerUserID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "请选择已共享到此项目的执行机器"})
			return
		}
		if reason := runner.DefaultSelectionBlocker(); reason != "" {
			c.JSON(http.StatusConflict, gin.H{"error": reason})
			return
		}
	}
	if err := h.repo.SetProjectExecutionTarget(ctx, projectID, target); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, target)
}
