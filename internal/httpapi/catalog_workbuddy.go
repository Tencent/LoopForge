package httpapi

import (
	"net/http"
	"strings"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func (h *catalogHandlers) validateWorkBuddyRoleCredential(c *gin.Context, role domain.AgentDefinition) bool {
	user, ok := h.requestUser(c.Request, true)
	use, _ := h.projectCredentialAccess(c.Request, role.ProjectID, user.ID)
	if !ok || !use {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权使用该项目的凭据"})
		return false
	}
	items, err := h.repo.AvailableProjectCredentialMetadata(c.Request.Context(), role.ProjectID, []string{strings.TrimSpace(role.WorkBuddy.CredentialID)})
	if err != nil || len(items) != 1 || items[0].Kind != domain.ProjectCredentialAKSK {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择有效的项目 AK/SK 凭据"})
		return false
	}
	return true
}
