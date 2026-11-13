package httpapi

import (
	"fmt"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"agentflow/internal/domain"
)

// preferences handles GET/PUT /api/assistant/preferences: the signed-in
// user's Main Bot settings.
func (h *assistantHandlers) preferences(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	if c.Request.Method == "GET" {
		c.JSON(200, h.repo.AssistantPreferences(user.ID))
		return
	}
	var body domain.AssistantPreferences
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "设置格式不正确"})
		return
	}
	if utf8.RuneCountInString(body.Instructions) > domain.AssistantPreferencesInstructionsMax {
		c.JSON(400, gin.H{"error": fmt.Sprintf("自定义规则最多 %d 字", domain.AssistantPreferencesInstructionsMax)})
		return
	}
	saved, err := h.repo.PutAssistantPreferences(user.ID, body)
	if err != nil {
		c.JSON(500, gin.H{"error": "保存设置失败，请重试"})
		return
	}
	c.JSON(200, saved)
}
