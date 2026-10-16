package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *runsHandlers) workspaceRestore(c *gin.Context) {
	run, _, ok := h.workspaceEditActor(c)
	if !ok {
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

func (h *runsHandlers) workspaceLifecycleInfo(c *gin.Context, run domain.Run, payload map[string]any) {
	payload["idleTimeoutSeconds"] = int(engine.ConversationWorkspaceIdleTTL.Seconds())
	meta, ok := h.engine.ConversationWorkspaceStatus(run.ID)
	if !ok {
		return
	}
	payload["restorable"] = meta.SavedAt != nil && meta.SHA256 != "" && h.workspaceWritable(c, domain.Run{Status: "succeeded", ExecutionContext: run.ExecutionContext})
	payload["checkpointSavedAt"] = meta.SavedAt
	if meta.FollowUp {
		payload["idleExpiresAt"] = meta.LastUsedAt.Add(engine.ConversationWorkspaceIdleTTL)
	}
	if meta.Error != "" {
		payload["checkpointWarning"] = "工作区保存未完成，暂缓回收：" + meta.Error
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
	if _, allowed := h.groupUser(c, run.ExecutionContext.ProjectID); !allowed {
		return
	}
	if err := h.repo.TouchWorkspaceUse(run.ID, time.Now().UTC()); err != nil {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"idleTimeoutSeconds": 3600})
}
