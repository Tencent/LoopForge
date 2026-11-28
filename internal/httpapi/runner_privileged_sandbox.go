package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func decodePrivilegedSandboxSetting(c *gin.Context) (bool, bool) {
	var input struct {
		Allowed *bool `json:"allowed"`
	}
	if !decode(c, &input) {
		return false, false
	}
	if input.Allowed == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "allowed 需为布尔值"})
		return false, false
	}
	return *input.Allowed, true
}

func (h *runnersHandlers) platformRunnerPrivilegedSandboxSet(c *gin.Context) {
	if !h.platformAdminWrite(c) {
		return
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "只有平台管理员可以执行该操作"})
		return
	}
	allowed, ok := decodePrivilegedSandboxSetting(c)
	if !ok {
		return
	}
	if err := h.repo.SetPlatformRunnerPrivilegedSandbox(c.Request.Context(), allowed, user.ID); err != nil {
		respond(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *runnersHandlers) runnerPrivilegedSandboxSet(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后管理执行机器"})
		return
	}
	if !requireWriteOrigin(c) {
		return
	}
	runner, found := h.repo.Runner(strings.TrimSpace(c.Param("id")))
	if !found {
		runnerPreferenceError(c, repo.ErrRunnerNotFound)
		return
	}
	if runner.OwnerUserID == 0 || runner.OwnerUserID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能设置自己绑定的执行机器"})
		return
	}
	if !runner.SupportsPrivilegedSandboxSettings() {
		c.JSON(http.StatusConflict, gin.H{"error": "请先升级 Runner 后再配置特权 Docker 沙箱"})
		return
	}
	allowed, ok := decodePrivilegedSandboxSetting(c)
	if !ok {
		return
	}
	if err := h.repo.SetRunnerPrivilegedSandbox(c.Request.Context(), runner.ID, user.ID, allowed); err != nil {
		if errors.Is(err, repo.ErrRunnerNotFound) {
			runnerPreferenceError(c, err)
		} else {
			respond(c, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *runnersHandlers) runnerPrivilegedSandboxSetting(c *gin.Context) {
	runner, ok := h.runnerSessionAuth(c)
	if !ok {
		return
	}
	allowed, err := h.repo.RunnerPrivilegedSandbox(c.Request.Context(), runner)
	if err != nil {
		respond(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"allowed": allowed})
}
