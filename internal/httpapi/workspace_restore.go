package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *runsHandlers) workspaceRestore(c *gin.Context) {
	run, err := h.repo.RunHeader(c.Param("id"))
	if err != nil || run.ID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "运行不存在"})
		return
	}
	owner, ok := h.workspaceOwner(run)
	if !ok {
		c.JSON(http.StatusGone, gin.H{"error": "共享工作区所属运行已不可用"})
		return
	}
	if _, status, message := h.workspaceOwnerAccess(c.Request, owner); status != 0 {
		c.JSON(status, gin.H{"error": message})
		return
	}
	if runnerID := h.engine.RunExecutionRunner(owner); runnerID != "" {
		if status, reason := h.probeRunnerWorkspace(c.Request.Context(), owner, runnerID); status != 0 {
			c.JSON(status, gin.H{"error": reason})
			return
		}
	}
	if strings.Contains(c.GetHeader("Prefer"), "respond-async") && !owner.WorkspaceCleanedAt.IsZero() {
		meta, _ := h.engine.ConversationWorkspaceStatus(owner.ID)
		if meta.SavedAt == nil {
			c.JSON(409, gin.H{"error": "没有已保存的工作区快照，无法恢复"})
			return
		}
		if h.prepareWorkspaceRestore(c, owner.ID) {
			return
		}
		c.JSON(200, gin.H{"restored": true, "idleTimeoutSeconds": 3600})
		return
	}
	token, err := h.engine.BeginConversationWorkspaceUse(owner.ID)
	if err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	defer func() {
		ctx, stop := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 30*time.Second)
		defer stop()
		_ = h.engine.EndConversationWorkspaceUse(ctx, owner.ID, token)
	}()
	if _, err = h.engine.PrepareConversationWorkspace(c.Request.Context(), owner.ID, token); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"restored": true, "idleTimeoutSeconds": 3600})
}

func (h *runsHandlers) prepareRunActivationWorkspace(c *gin.Context, id string) bool {
	if run, ok := h.repo.RunState(id); ok && h.engine.ShouldDeferRestoreUntilAdmitted(run) {
		return false
	}
	return h.prepareWorkspaceRestore(c, id)
}

// Async callers poll the durable restore separately, then repeat their control
// decision once ready. A 202 acknowledges restoration, never SOP execution.
func (h *runsHandlers) prepareWorkspaceRestore(c *gin.Context, id string) bool {
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
	owner, ok := h.workspaceOwner(run)
	if !ok {
		c.JSON(http.StatusGone, gin.H{"error": "共享工作区所属运行已不可用"})
		return
	}
	state, err := h.engine.WorkspaceRestoreStatus(owner.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, state)
}

func (h *runsHandlers) workspaceLifecycleInfo(c *gin.Context, run domain.Run, payload map[string]any, allowRestore bool) {
	payload["idleTimeoutSeconds"] = int(engine.ConversationWorkspaceIdleTTL.Seconds())
	meta, ok := h.engine.ConversationWorkspaceStatus(run.ID)
	if !ok {
		return
	}
	restorable := false
	if allowRestore && meta.SavedAt != nil && meta.SHA256 != "" {
		_, status, _ := h.workspaceOwnerAccess(c.Request, run)
		restorable = status == 0
	}
	payload["restorable"] = restorable
	payload["checkpointSavedAt"] = meta.SavedAt
	if allowRestore {
		if state, err := h.engine.WorkspaceRestoreStatus(run.ID); err == nil && (state.Status == "restoring" || state.Status == "failed") {
			payload["restore"] = state
		}
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
	owner, ok := h.workspaceOwner(run)
	if !ok {
		c.JSON(http.StatusGone, gin.H{"error": "共享工作区所属运行已不可用"})
		return
	}
	if err := h.repo.TouchWorkspaceUse(owner.ID, time.Now().UTC()); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"idleTimeoutSeconds": 3600})
}
