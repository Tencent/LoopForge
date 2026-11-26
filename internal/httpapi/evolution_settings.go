package httpapi

import (
	"agentflow/internal/domain"
	"os"
	"strconv"
)

func evolutionAutoEnabled() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("AGENTFLOW_EVOLUTION_AUTO_ENABLED"))
	return enabled
}

func privateEvolutionAsset(asset evoAsset, projectID string) error {
	if asset.Global || asset.ProjectID == "" || asset.ProjectID != projectID {
		return evoConflict("当前仅支持当前项目的私有角色和规则；共享或平台资产仅供诊断，请先复制为项目私有资产")
	}
	return nil
}

func (h *evolutionHandlers) autoEvolution(asset evoAsset) bool {
	return evolutionAutoEnabled() && privateEvolutionAsset(asset, asset.ProjectID) == nil && h.repo.EvoLevel(asset.Kind, asset.ID) >= domain.EvoLevelAuto
}
