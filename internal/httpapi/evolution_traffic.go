package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *evolutionHandlers) adjustCanaryTraffic(c *gin.Context) {
	var req struct {
		Percent int `json:"percent"`
	}
	if !decode(c, &req) {
		return
	}
	if req.Percent < 1 || req.Percent > 99 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "灰度流量必须在 1% 至 99% 之间"})
		return
	}
	p, ok := h.repo.EvoProposal(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "提案不存在"})
		return
	}
	if !h.requireProjectReader(c, p.ProjectID) {
		return
	}
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产不存在"})
		return
	}
	if !h.requireAssetWriter(c, asset) {
		return
	}
	if err := privateEvolutionAsset(asset, p.ProjectID); err != nil {
		respondEvoError(c, err)
		return
	}
	out, err := h.repo.AdjustEvoCanaryTraffic(p, req.Percent)
	if err != nil {
		respondEvoError(c, err)
		return
	}
	c.JSON(http.StatusOK, evoProposalView(out, false))
}
