package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

var ErrWeComOriginUnavailable = errors.New("发起成员已解除原平台账号绑定或该账号已没有该项目的权限，未发回私聊")

// ProjectVisible is the read permission required to receive a notification.
func ProjectVisible(store *repo.Store, projectID string, userID int64) bool {
	user, ok := store.User(userID)
	if !ok {
		return false
	}
	project, ok := store.Project(projectID)
	if !ok {
		return false
	}
	if project.OwnerID == user.ID || store.IsPlatformAdmin(user.LoginName) {
		return true
	}
	for _, member := range project.Members {
		if member.UserID == user.ID {
			return true
		}
	}
	return false
}

func (s *Service) CheckWeComOriginTarget(bot domain.NotificationBot, delivery domain.NotificationDelivery) error {
	if delivery.Origin != domain.NotificationOriginChat || delivery.TargetType == "group" {
		return nil
	}
	binding, err := s.Store.BotUserBinding(bot.IdentityScope(), delivery.TargetID)
	if err != nil || binding.UserID == 0 ||
		(delivery.OriginUserID != 0 && binding.UserID != delivery.OriginUserID) ||
		!ProjectVisible(s.Store, delivery.ProjectID, binding.UserID) {
		return ErrWeComOriginUnavailable
	}
	return nil
}

// notificationInteractionID shares an interaction on automatic recovery and
// allocates a new one after an explicit notification resend. Generation zero
// keeps the IDs used by deliveries already in the queue.
func notificationInteractionID(botID, action string, delivery domain.NotificationDelivery) string {
	key := strings.ReplaceAll(action, "_", "-") + ":" + delivery.ID
	if delivery.DeliveryGeneration > 0 {
		key += ":" + strconv.Itoa(delivery.DeliveryGeneration)
	}
	return repo.BotMessageID(botID, key)
}

func (s *Service) notificationCardTaskMatches(botID string, delivery domain.NotificationDelivery, taskID string) bool {
	if taskID == "" {
		return false
	}
	card, err := s.Store.BotInteraction(notificationInteractionID(botID, "notification", delivery))
	if errors.Is(err, gorm.ErrRecordNotFound) && delivery.Status == "sent" && delivery.DeliveryGeneration > 0 {
		// Previously sent cards predate generation-scoped interaction IDs.
		delivery.DeliveryGeneration = 0
		card, err = s.Store.BotInteraction(notificationInteractionID(botID, "notification", delivery))
	}
	return err == nil && card.CardTaskID == taskID
}

// notificationSendRejection runs on the live worker immediately before sending
// a queued notification. Its target and generation may have changed in the queue.
func (s *Service) notificationSendRejection(bot domain.NotificationBot, item domain.BotInteraction, conv domain.BotConversation) *SendResult {
	delivery, err := s.Store.NotificationDelivery(item.DeliveryID)
	if err != nil || delivery.BotID != bot.ID || delivery.ProjectID != bot.ProjectID ||
		delivery.TargetID != conv.ChatID || delivery.TargetType != conv.ChatType ||
		item.ID != notificationInteractionID(bot.ID, item.Action, delivery) {
		return &SendResult{Outcome: Rejected, Code: domain.BotTerminalNotificationExpired, SafeDetail: "通知已失效"}
	}
	if err := s.CheckWeComOriginTarget(bot, delivery); err != nil {
		return &SendResult{Outcome: Rejected, Code: domain.BotTerminalTargetUnavailable, SafeDetail: err.Error()}
	}
	if item.Action == "notification_details" && len(ActionsForDelivery(s.Store, delivery)) == 0 {
		return &SendResult{Outcome: Rejected, Code: domain.BotTerminalNotificationExpired, SafeDetail: ErrCardExpired.Error()}
	}
	return nil
}
