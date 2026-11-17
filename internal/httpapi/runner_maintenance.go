package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func (h *runnersHandlers) runnerMaintenance(c *gin.Context) {
	user, authenticated := h.requestUser(c.Request, true)
	if !authenticated {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后管理执行机器"})
		return
	}
	runner, found := h.repo.Runner(strings.TrimSpace(c.Param("id")))
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "执行机器不存在"})
		return
	}
	if runner.OwnerUserID != user.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能更新自己绑定的执行机器"})
		return
	}
	if runner.Status != domain.RunnerStatusOnline || h.runnerControls == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "执行机器暂未连接，请稍后重试"})
		return
	}
	if !runner.SupportsCLISettings() {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("网页更新需要 Runner %s 或以上版本，请先在执行机器上升级", domain.RunnerVersionCLISettings)})
		return
	}
	input := domain.RunnerMaintenanceRequest{
		Provider: strings.TrimSpace(c.Query("provider")), Apply: c.Request.Method == http.MethodPost,
		StatusOnly: c.Query("status") == "1",
	}
	if input.Provider != "" {
		canUpdate := slices.ContainsFunc(runner.Runtimes, func(rt domain.RunnerRuntime) bool {
			return rt.Provider == input.Provider && (rt.SupportsCLIUpdate() || input.StatusOnly)
		})
		if !canUpdate {
			c.JSON(http.StatusConflict, gin.H{"error": "请在执行机器上使用该 CLI 原来的安装方式更新"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	response, err := h.runnerControls.requestWorkspace(ctx, runner.ID, domain.RunnerWorkspaceRequest{
		Operation: domain.RunnerWorkspaceOperationMaintenance, Maintenance: &input,
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法连接 Runner，请稍后重试"})
		return
	}
	if response.Error != "" {
		status := response.Status
		if status < http.StatusBadRequest || status > 599 {
			status = http.StatusBadGateway
		}
		c.JSON(status, gin.H{"error": response.Error})
		return
	}
	status := http.StatusOK
	if input.Apply {
		status = http.StatusAccepted
	}
	c.Header("Cache-Control", "no-store")
	c.Data(status, "application/json", response.Payload)
}
