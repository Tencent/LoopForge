package httpapi

// 长期治理接口：治理面板读取、立即健康检查、反馈与问题标记、从面板发起
// 更新/下线。handler 只做权限、解码与渲染，业务规则在 knowledge.Service。

import (
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// 路由: g.GET("/api/knowledge/governance", h.governanceOverview)
func (h *knowledgeHandlers) governanceOverview(c *gin.Context) {
	projectID := c.Query("projectId")
	if !h.requireKnowledgeReader(c, projectID) {
		return
	}
	out, err := h.svc.GovernanceOverview(projectID)
	render(c, out, err)
}

// 路由: g.POST("/api/knowledge/governance/scan", h.governanceScan)
func (h *knowledgeHandlers) governanceScan(c *gin.Context) {
	var input struct {
		ProjectID string `json:"projectId"`
	}
	if !decodeOptional(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	out, err := h.svc.ScanGovernance(c.Request.Context(), input.ProjectID)
	render(c, out, err)
}

// 路由: g.POST("/api/knowledge/feedback", h.knowledgeFeedback)
func (h *knowledgeHandlers) knowledgeFeedback(c *gin.Context) {
	var input knowledge.FeedbackInput
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	var userID int64
	if user, ok := h.requestUser(c.Request, true); ok && user != nil {
		userID = user.ID
	}
	// 浏览器只能提交知识中心或 Main Bot 回答反馈；Agent 身份只由签名工具
	// 令牌带入，不接受客户端自报。
	input.AgentID, input.RunID = "", ""
	if input.Surface != "main_bot" {
		input.Surface = "knowledge_center"
	}
	item, err := h.svc.RecordFeedback(input, h.actor(c.Request), userID)
	render(c, item, err)
}

// 路由: g.POST("/api/knowledge/governance/gaps", h.governanceGap)
func (h *knowledgeHandlers) governanceGap(c *gin.Context) {
	var input struct {
		ProjectID string `json:"projectId"`
		Query     string `json:"query"`
		Status    string `json:"status"`
	}
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	err := h.svc.SetGapStatus(input.ProjectID, input.Query, input.Status)
	render(c, gin.H{"ok": err == nil, "status": input.Status}, err)
}

// 路由: g.POST("/api/knowledge/governance/dispose", h.governanceDispose)
func (h *knowledgeHandlers) governanceDispose(c *gin.Context) {
	var input knowledge.GovernanceDispositionInput
	if !decode(c, &input) {
		return
	}
	if !h.requireKnowledgeManager(c, input.ProjectID) {
		return
	}
	item, err := h.svc.DisposeGovernanceDocument(input, h.actor(c.Request))
	render(c, item, err)
}
