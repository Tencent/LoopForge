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
	if bot.ChannelKind() == domain.BotChannelDingTalk {
		for i := range records {
			records[i].PushResendAvailable = h.pushResendTarget(bot, records[i]) == nil
		}
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
	if strings.HasSuffix(c.Request.URL.Path, "/push-resend") {
		if botErr != nil || bot.ChannelKind() != domain.BotChannelDingTalk {
			c.JSON(409, gin.H{"error": "只有钉钉的过期回复可以改用主动单聊发送", "code": "state_conflict"})
			return
		}
		user, targetErr := h.pushResendUser(bot, item)
		if targetErr != nil {
			c.JSON(409, gin.H{"error": targetErr.Error(), "code": "push_target_unverified"})
			return
		}
		out, err = h.repo.PushResendBotInteraction(item.ID, actor, user.ID, req.ActionKey, truncateRunes(strings.TrimSpace(req.Reason), 60))
		if errors.Is(err, repo.ErrBotPushTargetUnverified) {
			c.JSON(409, gin.H{"error": "原发送者的绑定已变化或尚未通过主动发送试发，无法改用主动单聊", "code": "push_target_unverified"})
			return
		}
	} else if strings.HasSuffix(c.Request.URL.Path, "/resend") {
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

// pushResendUser checks that the original sender of an expired DingTalk
// reply is still bound to this configuration, has passed a trial proactive
// send and keeps execution permission on the project. The repository
// re-checks the binding inside the resend transaction.
func (h *botInteractionHandlers) pushResendUser(bot domain.NotificationBot, item domain.BotInteraction) (domain.User, error) {
	binding, err := h.repo.BotPushTarget(bot, item.SenderID)
	if err != nil || binding.TargetVerifiedAt == nil {
		return domain.User{}, errors.New("原发送者还不是已试发成功的主动发送目标：请让其完成绑定，并在通知接收人中试发一次")
	}
	assistant := &assistantHandlers{deps: h.deps}
	user, err := assistant.botExecutionUser(bot, item.SenderID)
	if err != nil {
		return user, err
	}
	if user.ID != binding.UserID {
		return user, errors.New("原发送者的绑定已变化")
	}
	return user, nil
}

func (h *botInteractionHandlers) pushResendTarget(bot domain.NotificationBot, item domain.BotInteraction) error {
	if item.Status != domain.BotReplyFailed || item.TerminalReason != "route_expired" || item.BodyPurgedAt != nil {
		return errors.New("not expired")
	}
	_, err := h.pushResendUser(bot, item)
	return err
}
