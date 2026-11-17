package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"agentflow/internal/repo"
)

// SOP 编辑租约：同一时刻只允许一个人编辑同一条 SOP。
//
//	GET    /api/workflows/:id/edit-lock            → {held, holder, expiresAt, mine}
//	POST   /api/workflows/:id/edit-lock            {takeover:true}  → 取租约（接管需显式）
//	DELETE /api/workflows/:id/edit-lock            → 释放
//
// 设计取舍：租约很短（默认 3 分钟）且每次保存会续期，所以"人走了"最多阻塞
// 3 分钟；想立刻编辑就显式接管，不会有人被永久锁死。
func (h *catalogHandlers) workflowEditLock(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	wf, ok := h.repo.Workflow(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "workflow not found"})
		return
	}
	_ = wf
	user, ok := h.requestUser(c.Request, true)
	if !ok || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		h.respondLock(c, id, user.ID)
	case http.MethodDelete:
		if err := h.repo.ReleaseWorkflowLock(id, user.ID); err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, gin.H{"released": true})
	case http.MethodPost:
		var req struct {
			Takeover bool `json:"takeover"`
		}
		_ = c.ShouldBindJSON(&req)
		holder, owned, err := h.repo.AcquireWorkflowLock(id, user.ID, user.LoginName, repo.DefaultWorkflowLockTTL, req.Takeover)
		if err != nil {
			respond(c, err)
			return
		}
		if !owned {
			c.JSON(http.StatusConflict, gin.H{
				"error":     "SOP 正在被他人编辑",
				"holder":    holder.UserLogin,
				"expiresAt": holder.ExpiresAt,
				"hint":      "等对方保存完（租约 3 分钟自动过期），或用 takeover:true 接管；接管后对方的保存会被拒绝。",
			})
			return
		}
		h.respondLock(c, id, user.ID)
	default:
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}

func (h *catalogHandlers) respondLock(c *gin.Context, workflowID string, userID int64) {
	holder, held := h.repo.WorkflowLock(workflowID)
	if !held {
		c.JSON(200, gin.H{"held": false, "mine": false})
		return
	}
	c.JSON(200, gin.H{
		"held": true, "mine": holder.UserID == userID,
		"holder": holder.UserLogin, "expiresAt": holder.ExpiresAt,
		"secondsLeft": int(time.Until(holder.ExpiresAt).Seconds()),
	})
}

// currentUserID is the signed-in user, or 0 for service-token callers.
func (h *catalogHandlers) currentUserID(c *gin.Context) int64 {
	user, ok := h.requestUser(c.Request, true)
	if !ok || user == nil {
		return 0
	}
	return user.ID
}
