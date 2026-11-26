package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func (h *assistantHandlers) authorizeSessionProjectCredentials(c *gin.Context, session domain.AssistantSession, userID int64) bool {
	use, _ := h.projectCredentialAccess(c.Request, session.ProjectID, userID)
	if (len(session.ProjectCredentialIDs) > 0 && !use) || session.Shared || session.RunID != "" || session.Source == domain.AssistantSourceSOPDesigner {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅有权限的普通项目会话可以绑定项目凭据"})
		return false
	}
	return true
}

func (h *assistantHandlers) validateSessionProjectCredentials(c *gin.Context, session domain.AssistantSession, userID int64) bool {
	if !h.authorizeSessionProjectCredentials(c, session, userID) {
		return false
	}
	if _, err := h.repo.ProjectCredentialVariableNames(c.Request.Context(), session.ProjectID, session.ProjectCredentialIDs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func (h *assistantHandlers) sessionProjectCredentialContext(c *gin.Context, session domain.AssistantSession, userID int64) (string, map[string]domain.AssistantProjectCredentialObservation, bool) {
	if !h.authorizeSessionProjectCredentials(c, session, userID) {
		return "", nil, false
	}
	items, err := h.repo.AvailableProjectCredentialMetadata(c.Request.Context(), session.ProjectID, session.ProjectCredentialIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return "", nil, false
	}
	note, observations := conversationProjectCredentialContext(session, items)
	return note, observations, true
}

func conversationProjectCredentialContext(session domain.AssistantSession, items []domain.ProjectCredential) (string, map[string]domain.AssistantProjectCredentialObservation) {
	observations := make(map[string]domain.AssistantProjectCredentialObservation, len(items))
	var available, updated, unavailable []string
	for _, item := range items {
		observations[item.ID] = domain.AssistantProjectCredentialObservation{Variables: item.Variables, UpdatedAt: item.UpdatedAt}
		available = append(available, item.Variables...)
		if previous, ok := session.ProjectCredentialObservations[item.ID]; ok && !previous.UpdatedAt.Equal(item.UpdatedAt) {
			updated = append(updated, item.Variables...)
			// Renamed variables from this binding may still be in the model's history.
			unavailable = append(unavailable, previous.Variables...)
		}
	}
	unknownBinding := false
	for _, id := range session.ProjectCredentialIDs {
		if _, ok := observations[id]; ok {
			continue
		}
		previous := session.ProjectCredentialObservations[id]
		unavailable = append(unavailable, previous.Variables...)
		unknownBinding = unknownBinding || len(previous.Variables) == 0
	}
	unavailable = slices.DeleteFunc(unavailable, func(name string) bool { return slices.Contains(available, name) })
	slices.Sort(available)
	slices.Sort(updated)
	slices.Sort(unavailable)
	unavailable = slices.Compact(unavailable)
	var notes []string
	if len(available) > 0 {
		notes = append(notes, "项目凭据的环境变量为 "+strings.Join(available, "、")+"。需要时在项目工作区通过进程环境使用，以进程实际环境为准，避免输出值。")
	}
	if len(updated) > 0 {
		notes = append(notes, "项目凭据环境变量 "+strings.Join(updated, "、")+" 的配置已更新，请使用当前环境中的值。")
	}
	if len(unavailable) > 0 {
		notes = append(notes, "项目凭据环境变量 "+strings.Join(unavailable, "、")+" 本轮已不可用。")
	}
	if unknownBinding {
		notes = append(notes, "有已绑定的项目凭据本轮不可用。")
	}
	if len(updated) > 0 || len(unavailable) > 0 || unknownBinding {
		notes = append(notes, "继续处理用户请求，仅在任务确实需要不可用的凭据时说明。")
	}
	return "\n" + strings.Join(notes, "\n") + "\n", observations
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
	if !h.validateSessionProjectCredentials(c, session, user.ID) {
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
