package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type botInteractionHandlers struct {
	*deps
	notifications *notificationHandlers
}

func (h *botInteractionHandlers) records(c *gin.Context) {
	bot, ok := h.notifications.find(c, true)
	if !ok {
		return
	}
	conn, _ := h.repo.BotConnectionState(bot.LeaseKey())
	chats, err := h.repo.BotConversations(bot.ID)
	if err != nil {
		respond(c, err)
		return
	}
	records, err := h.repo.BotInteractionRecords(bot.ID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, gin.H{"connection": conn, "conversations": chats, "messages": records})
}

// pending lists interactions of a project awaiting manual handling
// (reply_unknown, reply_failed), including those of deleted Bots, for
// project managers.
func (h *botInteractionHandlers) pending(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.projectManager(c, projectID) {
		return
	}
	items, err := h.repo.BotInteractionsAwaitingReview(projectID)
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, items)
}

// projectManager allows platform admins, the project owner and maintainers.
func (h *botInteractionHandlers) projectManager(c *gin.Context, projectID string) bool {
	if !h.notifications.access(c, projectID, true) {
		return false
	}
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	project, _ := h.repo.Project(projectID)
	if ok && user != nil {
		if project.OwnerID == user.ID {
			return true
		}
		for _, m := range project.Members {
			if m.UserID == user.ID && strings.EqualFold(strings.TrimSpace(m.Role), "maintainer") {
				return true
			}
		}
	}
	c.JSON(403, gin.H{"error": "只有机器人创建者或项目管理员可以处理待核对记录"})
	return false
}

// act serves the manual "confirm received" and "send again" actions. An
// existing Bot requires its managers; a deleted Bot's records stay reachable
// by project managers through the project snapshot. The action key makes a
// repeated click return the same result.
func (h *botInteractionHandlers) act(c *gin.Context) {
	item, err := h.repo.BotInteraction(c.Param("interactionId"))
	if err != nil {
		c.JSON(404, gin.H{"error": "记录不存在"})
		return
	}
	bot, botErr := h.repo.NotificationBot(item.BotID)
	switch {
	case botErr == nil:
		if !h.notifications.managed(c, bot) {
			return
		}
	case errors.Is(botErr, gorm.ErrRecordNotFound) && item.ProjectID != "":
		if !h.projectManager(c, item.ProjectID) {
			return
		}
	default:
		c.JSON(404, gin.H{"error": "记录不存在"})
		return
	}
	if !botBindingSameOrigin(c) {
		c.JSON(403, gin.H{"error": "请从平台页面操作"})
		return
	}
	var req struct {
		ActionKey string `json:"actionKey"`
		Reason    string `json:"reason"`
	}
	if !decode(c, &req) {
		return
	}
	req.ActionKey = strings.TrimSpace(req.ActionKey)
	if req.ActionKey == "" || len(req.ActionKey) > 64 {
		c.JSON(400, gin.H{"error": "缺少操作标识"})
		return
	}
	actor := int64(0)
	if user, ok := h.requestUser(c.Request, true); ok && user != nil {
		actor = user.ID
	}
	var out domain.BotInteraction
	if strings.HasSuffix(c.Request.URL.Path, "/resend") {
		out, err = h.repo.ResendBotInteraction(item.ID, actor, req.ActionKey, truncateRunes(strings.TrimSpace(req.Reason), 60))
	} else {
		out, err = h.repo.ConfirmBotInteraction(item.ID, actor, req.ActionKey)
	}
	switch {
	case errors.Is(err, repo.ErrBotDeleted):
		c.JSON(409, gin.H{"error": "机器人已删除，无法再次发送；可确认已收到", "code": "bot_deleted"})
	case errors.Is(err, repo.ErrBotRouteExpired):
		c.JSON(409, gin.H{"error": "会话回复授权已过期，无法在原会话再次发送", "code": "route_expired"})
	case errors.Is(err, repo.ErrBotStateConflict):
		c.JSON(409, gin.H{"error": "记录状态已变化，请刷新后再试", "code": "state_conflict"})
	case err != nil:
		respond(c, err)
	default:
		c.JSON(200, out)
	}
}
