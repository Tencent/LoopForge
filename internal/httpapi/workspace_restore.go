package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *runsHandlers) workspaceRestore(c *gin.Context) {
	run, _, ok := h.workspaceEditActor(c)
	if !ok {
		return
	}
	if strings.Contains(c.GetHeader("Prefer"), "respond-async") && !run.WorkspaceCleanedAt.IsZero() {
		meta, _ := h.engine.ConversationWorkspaceStatus(run.ID)
		if meta.SavedAt == nil {
			c.JSON(409, gin.H{"error": "没有已保存的工作区快照，无法恢复"})
			return
		}
		if h.prepareRunActivationWorkspace(c, run.ID) {
			return
		}
		c.JSON(200, gin.H{"restored": true, "idleTimeoutSeconds": 3600})
		return
	}
	token, err := h.engine.BeginConversationWorkspaceUse(run.ID)
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	defer func() {
		ctx, stop := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 30*time.Second)
		defer stop()
		_ = h.engine.EndConversationWorkspaceUse(ctx, run.ID, token)
	}()
	if err = h.engine.EnsureConversationWorkspace(c.Request.Context(), run.ID, token); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"restored": true, "idleTimeoutSeconds": 3600})
}

// Async callers poll the durable restore separately, then repeat their control
// decision once ready. A 202 acknowledges restoration, never SOP execution.
func (h *runsHandlers) prepareRunActivationWorkspace(c *gin.Context, id string) bool {
	if !strings.Contains(c.GetHeader("Prefer"), "respond-async") {
		if err := h.engine.RestoreConversationWorkspaceIfNeeded(c.Request.Context(), id); err != nil {
			respond(c, err)
			return true
		}
		return false
	}
	state, err := h.engine.StartWorkspaceRestore(id)
	if err != nil {
		if errors.Is(err, repo.ErrWorkspaceInUse) {
			c.Header("Retry-After", "1")
			c.JSON(409, gin.H{"code": domain.WorkspaceErrorBusy, "error": err.Error(), "retryAfterMs": 1000})
			return true
		}
		respondWorkspaceError(c, err)
		return true
	}
	if state.Status == "restoring" {
		c.Header("Preference-Applied", "respond-async")
		c.Header("Retry-After", "1")
		c.JSON(202, gin.H{"workspaceRestore": state, "retryAfterMs": 1000})
		return true
	}
	if state.Status == "failed" {
		c.JSON(409, gin.H{"error": state.Error, "workspaceRestore": state})
		return true
	}
	return false
}

func (h *runsHandlers) workspaceRestoreStatus(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "运行不存在"})
		return
	}
	if _, allowed := h.projectRunUser(c, run.ExecutionContext.ProjectID); !allowed {
		return
	}
	state, err := h.engine.WorkspaceRestoreStatus(run.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, state)
}

func (h *runsHandlers) workspaceLifecycleInfo(c *gin.Context, run domain.Run, payload map[string]any) {
	payload["idleTimeoutSeconds"] = int(engine.ConversationWorkspaceIdleTTL.Seconds())
	meta, ok := h.engine.ConversationWorkspaceStatus(run.ID)
	if !ok {
		return
	}
	payload["restorable"] = meta.SavedAt != nil && meta.SHA256 != "" && h.workspaceWritable(c, domain.Run{Status: "succeeded", ExecutionContext: run.ExecutionContext})
	payload["checkpointSavedAt"] = meta.SavedAt
	if state, err := h.engine.WorkspaceRestoreStatus(run.ID); err == nil && (state.Status == "restoring" || state.Status == "failed") {
		payload["restore"] = state
	}
	if meta.FollowUp {
		payload["idleExpiresAt"] = meta.LastUsedAt.Add(engine.ConversationWorkspaceIdleTTL)
	}
	if meta.Error != "" {
		payload["checkpointWarning"] = "工作区保存未完成，暂缓回收：" + meta.Error
		payload["checkpointRetryAt"] = meta.SaveRetryAt
	}
}

func (h *runsHandlers) touchWorkspaceBrowse(c *gin.Context, run domain.Run) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		return
	}
	access := assistantHandlers{deps: h.deps}
	if access.workbenchProjectAllowed(c, run.ExecutionContext.ProjectID, user.ID) {
		_ = h.repo.TouchWorkspaceUse(run.ID, time.Now().UTC())
	}
}
func (h *runsHandlers) workspaceActivity(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(404, gin.H{"error": "运行不存在"})
		return
	}
	if _, allowed := h.projectRunUser(c, run.ExecutionContext.ProjectID); !allowed {
		return
	}
	if err := h.repo.TouchWorkspaceUse(run.ID, time.Now().UTC()); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"idleTimeoutSeconds": 3600})
}
