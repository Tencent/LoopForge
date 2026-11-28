package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func decodeAgentContentChanges(raw json.RawMessage) (domain.AgentContentChanges, error) {
	var changes domain.AgentContentChanges
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&changes); err != nil {
		return changes, fmt.Errorf("角色修改只接受 name、description 和 prompt：%w", err)
	}
	return changes, changes.Validate()
}

func (h *catalogHandlers) editAgentContent(c *gin.Context, id, projectID string, updatedAt *time.Time, rawChanges json.RawMessage) {
	current, exists := h.repo.Agent(id)
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "角色不存在"})
		return
	}
	if projectID == "" || current.ProjectID != projectID {
		c.JSON(http.StatusConflict, gin.H{"error": "只能修改当前项目的角色"})
		return
	}
	if !h.requireAgentWriter(c, current) {
		return
	}
	if updatedAt == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供审阅时读取的角色 updatedAt"})
		return
	}
	if !current.UpdatedAt.Equal(*updatedAt) {
		c.JSON(http.StatusConflict, gin.H{"error": "角色已更新，请重新审阅修改"})
		return
	}
	changes, err := decodeAgentContentChanges(rawChanges)
	if err != nil {
		respond(c, err)
		return
	}
	updated, err := h.repo.EditAgentContent(current, changes)
	if err != nil {
		respond(c, err)
		return
	}
	h.recordAgentPromptEdit(c, current, updated)
	c.JSON(http.StatusOK, updated)
}

func (h *catalogHandlers) recordAgentPromptEdit(c *gin.Context, previous, updated domain.AgentDefinition) {
	if previous.Prompt == updated.Prompt {
		return
	}
	source, note := catalogEditSource(c.Request.Context())
	var uid int64
	if user, ok := h.requestUser(c.Request, true); ok {
		uid = user.ID
	}
	_, _, _ = h.repo.RecordAssetEdit(domain.EvoAssetAgent, updated.ID, previous.Prompt, updated.Prompt, source, note, uid)
}
