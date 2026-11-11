package httpapi

// 自动沉淀的管理接口：知识库发布模式与敏感规则、审核条目撤回。业务规则在
// knowledge.Service，handler 只做权限、解码与渲染。

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

var knowledgeRetractReasons = map[string]bool{
	"内容错误": true, "包含敏感信息": true, "不再适用": true, "重复": true, "其他": true,
}

// 路由: g.GET("/api/knowledge/bases/sediment", h.knowledgeSedimentConfig)
func (h *knowledgeHandlers) knowledgeSedimentConfig(c *gin.Context) {
	if !h.requireManagementScope(c, c.Query("projectId")) {
		return
	}
	config, err := h.svc.SedimentConfig(c.Query("projectId"), c.Query("baseId"))
	render(c, config, err)
}

// 路由: g.PUT("/api/knowledge/bases/sediment", h.knowledgeSedimentConfigUpdate)
func (h *knowledgeHandlers) knowledgeSedimentConfigUpdate(c *gin.Context) {
	var input struct {
		ProjectID string                     `json:"projectId"`
		BaseID    string                     `json:"baseId"`
		Mode      string                     `json:"mode"`
		Rules     []domain.KnowledgeGateRule `json:"rules"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	base, err := h.svc.SetSedimentConfig(input.ProjectID, input.BaseID, input.Mode, input.Rules)
	render(c, base, err)
}

// 路由: g.POST("/api/knowledge/submissions/:id/retract", h.retractSubmission)
func (h *knowledgeHandlers) retractSubmission(c *gin.Context) {
	item, err := h.svc.Submission(c.Param("id"))
	if err != nil {
		respond(c, err)
		return
	}
	if !h.requireKnowledgeManager(c, item.ProjectID) {
		return
	}
	var input struct {
		ReasonCategory string `json:"reasonCategory"`
		ReasonDetail   string `json:"reasonDetail"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	category := strings.TrimSpace(input.ReasonCategory)
	detail := strings.TrimSpace(input.ReasonDetail)
	if category == "" {
		category = "不再适用"
	}
	if !knowledgeRetractReasons[category] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "撤回原因必须是内容错误、包含敏感信息、不再适用、重复或其他"})
		return
	}
	if category == "其他" && detail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "选择“其他”时请填写撤回说明"})
		return
	}
	if utf8.RuneCountInString(detail) > 300 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "撤回说明不能超过 300 个字"})
		return
	}
	out, err := h.svc.Retract(c.Request.Context(), item.ID, category, detail, h.actor(c.Request))
	render(c, out, err)
}
