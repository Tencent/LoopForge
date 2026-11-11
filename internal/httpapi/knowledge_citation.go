package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// 路由: g.GET("/api/knowledge/citations", h.citationUsage)
//
// 按项目返回统计窗口内的引用次数，以及有正文但没有引用的文档。
// available 为 false 时调用方不要把空列表当成「从未命中」。
func (h *knowledgeHandlers) citationUsage(c *gin.Context) {
	projectID := c.Query("projectId")
	if !h.requireManagementScope(c, projectID) {
		return
	}
	usage, ok := h.svc.CitationUsage(projectID)
	if !ok {
		render(c, gin.H{"available": false, "cited": []any{}, "uncited": []any{}}, nil)
		return
	}
	render(c, usage, nil)
}

// 路由: g.POST("/api/knowledge/citations/freshness", h.citationFreshness)
//
// 展示出处时比对来源当前正文。其他项目的来源一律记为 deleted，不返回当前版本。
func (h *knowledgeHandlers) citationFreshness(c *gin.Context) {
	var body struct {
		ProjectID string   `json:"projectId"`
		Citations []string `json:"citations"`
	}
	if !decode(c, &body) {
		return
	}
	if !h.requireManagementScope(c, body.ProjectID) {
		return
	}
	citations := body.Citations
	if len(citations) > 50 {
		citations = citations[:50]
	}
	cleaned := make([]string, 0, len(citations))
	for _, item := range citations {
		item = strings.TrimSpace(item)
		if item != "" {
			cleaned = append(cleaned, item)
		}
	}
	render(c, gin.H{"items": h.svc.JudgeCitations(body.ProjectID, cleaned)}, nil)
}
