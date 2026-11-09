package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// sessionSandbox changes only the environment preference, never the transcript
// or the already-frozen environment of an existing workspace.
func (h *assistantHandlers) sessionSandbox(c *gin.Context) {
	u, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	ownerID, _, err := h.conversationScope(c, c.Param("id"), "", "", u.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	s, ok := h.repo.AssistantSession(ownerID, c.Param("id"))
	if !ok || s.ProjectID == "" || s.Shared || s.RunID != "" {
		c.JSON(403, gin.H{"error": "仅普通项目会话可以选择运行环境"})
		return
	}
	var body struct {
		SandboxProfileID *string `json:"sandboxProfileId"`
	}
	if !decode(c, &body) {
		return
	}
	if body.SandboxProfileID == nil {
		c.JSON(400, gin.H{"error": "请提供 sandboxProfileId，空字符串表示跟随项目默认"})
		return
	}
	id := strings.TrimSpace(*body.SandboxProfileID)
	if id != "" {
		if _, err := h.repo.ConversationSandbox(s.ProjectID, id); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}
	if err := h.repo.SetAssistantSessionSandbox(ownerID, s.ID, id); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	stored, _ := h.repo.AssistantSession(ownerID, s.ID)
	c.JSON(200, stored)
}
