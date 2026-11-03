package httpapi

import "github.com/gin-gonic/gin"

func (h *runsHandlers) projectRunUser(c *gin.Context, project string) (int64, bool) {
	u, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return 0, false
	}
	access := assistantHandlers{deps: h.deps}
	if project == "" || !access.workbenchProjectAllowed(c, project, u.ID) {
		c.JSON(403, gin.H{"error": "无权访问任务项目"})
		return 0, false
	}
	return u.ID, true
}
