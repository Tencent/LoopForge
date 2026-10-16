package httpapi

import (
	"agentflow/internal/repo"
	"context"
	"errors"
	"time"

	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

// withPreparedWorkspace prepares before any run-row editor transaction. Shell
// progress events update the run and must never nest inside that row lock.
func (h *runsHandlers) withPreparedWorkspace(c *gin.Context, runID string, fn func(runtime.WorkspacePreparationStatus) error) error {
	ctx := c.Request.Context()
	token := engine.ConversationWorkspaceToken(ctx)
	if token == "" {
		var err error
		token, err = h.engine.BeginConversationWorkspaceUse(runID)
		if err != nil {
			return err
		}
		defer func() {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = h.engine.EndConversationWorkspaceUse(saveCtx, runID, token)
		}()
	}
	originalRequest := c.Request
	defer func() { c.Request = originalRequest }()
	ctx = engine.WithConversationWorkspaceToken(ctx, token)
	c.Request = c.Request.WithContext(ctx)
	if err := h.engine.EnsureConversationWorkspace(ctx, runID, token); err != nil {
		return err
	}
	state, err := h.engine.EnsureWorkspaceReady(ctx, runID)
	if err != nil {
		return err
	}
	return fn(state)
}

func (h *runsHandlers) workspacePreparation(c *gin.Context) {
	run, exists := h.repo.RunState(c.Param("id"))
	if !exists {
		c.JSON(404, gin.H{"error": "运行不存在"})
		return
	}
	if c.Request.Method == "GET" {
		if _, ok := h.groupUser(c, run.ExecutionContext.ProjectID); !ok {
			return
		}
		state, err := h.engine.WorkspacePreparationStatus(c.Request.Context(), run.ID)
		if err != nil {
			c.JSON(409, gin.H{"status": "unprepared", "error": err.Error(), "steps": []any{}})
			return
		}
		c.JSON(200, runtime.PublicWorkspacePreparationStatus(state))
		return
	}
	if _, _, ok := h.workspaceEditActor(c); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	var state runtime.WorkspacePreparationStatus
	err := h.withPreparedWorkspace(c, run.ID, func(prepared runtime.WorkspacePreparationStatus) error { state = prepared; return nil })
	if err != nil {
		if errors.Is(err, repo.ErrWorkspaceInUse) {
			c.Header("Retry-After", "2")
			c.JSON(409, gin.H{"status": "busy", "code": "workspace_busy", "retryAfterMs": 2000, "error": "工作区正在使用或保存，稍后自动重试", "steps": []any{}})
			return
		}
		state, _ = h.engine.WorkspacePreparationStatus(c.Request.Context(), run.ID)
		state.Status = "failed"
		state.Error = err.Error()
		c.JSON(409, runtime.PublicWorkspacePreparationStatus(state))
		return
	}
	c.JSON(200, runtime.PublicWorkspacePreparationStatus(state))
}
