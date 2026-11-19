package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

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
		UseSaved bool   `json:"useSaved"`
	}
	if !decode(c, &req) {
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if !runnerUsesLocalLogin(runner, provider) {
		c.JSON(http.StatusConflict, cliModelProbeResult{Error: "请先为该 CLI 启用账号登录"})
		return
	}
	var saved []domain.CLIModel
	if provider == runtime.AgentCLIAntigravity && req.UseSaved {
		stored, _ := h.repo.User(user.ID)
		saved = assistantEngineCustomModels(stored, provider)
	}
	status, result := runnerLoginModelCatalog(c.Request.Context(), h.runnerControls, runner, provider, saved)
	c.JSON(status, result)
}

func runnerLoginModelCatalog(ctx context.Context, controls *runnerControlHub, runner domain.Runner, provider string, saved []domain.CLIModel) (int, cliModelProbeResult) {
	if models := runnerLoginModelsWithEfforts(provider, saved); len(models) > 0 {
		return http.StatusOK, cliModelProbeResult{Models: models, Source: "saved"}
	}
	if runner.Status != domain.RunnerStatusOnline || !runner.SupportsProvider(provider) || controls == nil {
		return http.StatusServiceUnavailable, cliModelProbeResult{Error: "执行机器或所选 CLI 当前不可用"}
	}
	if !runner.SupportsCLISettings() {
		return http.StatusConflict, cliModelProbeResult{Error: fmt.Sprintf("获取账号模型需要 Runner %s 或以上版本", domain.RunnerVersionCLISettings)}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := controls.requestWorkspace(ctx, runner.ID, domain.RunnerWorkspaceRequest{
		Operation: domain.RunnerWorkspaceOperationLoginModels, ModelProvider: provider,
	})
	if err != nil {
		return http.StatusServiceUnavailable, cliModelProbeResult{Error: "无法连接执行机器，请稍后重试"}
	}
	if response.Error != "" {
		return http.StatusBadGateway, cliModelProbeResult{Error: response.Error}
	}
	var models []domain.CLIModel
	if err := json.Unmarshal(response.Payload, &models); err != nil {
		return http.StatusBadGateway, cliModelProbeResult{Error: "执行机器返回了无效的模型列表"}
	}
	models = runnerLoginModelsWithEfforts(provider, models)
	if len(models) == 0 {
		return http.StatusOK, cliModelProbeResult{Error: "账号没有返回可用模型，可以手动添加模型"}
	}
	return http.StatusOK, cliModelProbeResult{Models: models, Source: "runner"}
}

func runnerLoginModelsWithEfforts(provider string, models []domain.CLIModel) []domain.CLIModel {
	models = slices.DeleteFunc(slices.Clone(models), func(model domain.CLIModel) bool {
		return model.ID == assistantDefaultModel || model.ID == provider+":"+assistantDefaultModel || !assistantValidEngineModel(model.ID)
	})
	if provider != runtime.AgentCLIAntigravity {
		return cliModelsWithEfforts(provider, models, "")
	}
	models = runtime.FoldAgyAccountModels(models)
	for i, model := range models {
		levels := append([]string{}, assistantModelEffortsInCatalog(provider+":"+model.ID, models, true)...)
		if model.ReasoningEfforts != nil {
			models[i].ReasoningEfforts = &levels
		} else {
			models[i].DiscoveredReasoningEfforts = &levels
		}
	}
	return models
}
