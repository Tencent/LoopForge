package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func (h *runnersHandlers) probeRunnerModels(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, cliModelProbeResult{Error: "请先登录后获取模型"})
		return
	}
	runner, found := h.repo.Runner(strings.TrimSpace(c.Param("id")))
	if !found {
		c.JSON(http.StatusNotFound, cliModelProbeResult{Error: "执行机器不存在"})
		return
	}
	if runner.OwnerUserID != user.ID {
		c.JSON(http.StatusForbidden, cliModelProbeResult{Error: "只能从自己绑定的执行机器获取账号模型"})
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if !decode(c, &req) {
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if !runnerUsesLocalLogin(runner, provider) {
		c.JSON(http.StatusConflict, cliModelProbeResult{Error: "请先为该 CLI 启用账号登录"})
		return
	}
	if runner.Status != domain.RunnerStatusOnline || !runner.SupportsProvider(provider) || h.runnerControls == nil {
		c.JSON(http.StatusServiceUnavailable, cliModelProbeResult{Error: "执行机器或所选 CLI 当前不可用"})
		return
	}
	if !runner.SupportsCLISettings() {
		c.JSON(http.StatusConflict, cliModelProbeResult{Error: fmt.Sprintf("获取账号模型需要 Runner %s 或以上版本", domain.RunnerVersionCLISettings)})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	response, err := h.runnerControls.requestWorkspace(ctx, runner.ID, domain.RunnerWorkspaceRequest{
		Operation: domain.RunnerWorkspaceOperationLoginModels, ModelProvider: provider,
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, cliModelProbeResult{Error: "无法连接执行机器，请稍后重试"})
		return
	}
	if response.Error != "" {
		c.JSON(http.StatusBadGateway, cliModelProbeResult{Error: response.Error})
		return
	}
	var models []domain.CLIModel
	if err := json.Unmarshal(response.Payload, &models); err != nil {
		c.JSON(http.StatusBadGateway, cliModelProbeResult{Error: "执行机器返回了无效的模型列表"})
		return
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, cliModelProbeResult{Error: "账号没有返回可用模型，可以手动添加模型"})
		return
	}
	c.JSON(http.StatusOK, cliModelProbeResult{Models: models, Source: "runner"})
}
