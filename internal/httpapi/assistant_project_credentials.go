package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func (h *assistantHandlers) sessionProjectCredentialNames(c *gin.Context, session domain.AssistantSession, userID int64) ([]string, bool) {
	use, _ := h.projectCredentialAccess(c.Request, session.ProjectID, userID)
	if (len(session.ProjectCredentialIDs) > 0 && !use) || session.Shared || session.RunID != "" || session.Source == domain.AssistantSourceSOPDesigner {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅有权限的普通项目会话可以绑定项目凭据"})
		return nil, false
	}
	names, err := h.repo.ProjectCredentialVariableNames(c.Request.Context(), session.ProjectID, session.ProjectCredentialIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return names, true
}

func (h *assistantHandlers) sessionProjectCredentials(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return
	}
	session, ok := h.repo.AssistantSession(user.ID, c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	var body struct {
		IDs []string `json:"projectCredentialIds"`
	}
	if !decode(c, &body) {
		return
	}
	if body.IDs == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供 projectCredentialIds，空数组表示清除绑定"})
		return
	}
	session.ProjectCredentialIDs = body.IDs
	if _, ok := h.sessionProjectCredentialNames(c, session, user.ID); !ok {
		return
	}
	if err := h.repo.SetAssistantSessionProjectCredentials(user.ID, session.ID, session.ProjectID, body.IDs); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	stored, _ := h.repo.AssistantSession(user.ID, session.ID)
	c.JSON(http.StatusOK, stored)
}

func (s *AgentToolSession) recordProjectCredentialValues(env map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range env {
		if !slices.Contains(s.projectCredentialValues, value) {
			s.projectCredentialValues = append(s.projectCredentialValues, value)
		}
	}
}

func (s *AgentToolSession) projectSecrets() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.projectCredentialValues)
}

func maskProjectSecrets(text string, secrets []string) string {
	for _, value := range secrets {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	return text
}
