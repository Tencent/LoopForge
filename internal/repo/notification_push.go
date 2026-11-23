package repo

import (
	"agentflow/internal/domain"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Proactive notification sends of Feishu and DingTalk. Targets are bound
// private-chat senders of the configuration; a trial send must succeed
// before one can be selected. Deliveries keep their frozen target and use a
// persisted send key with the same two-phase rule as conversation replies:
// SendStartedAtUS is set (database clock) before the network call, so a
// lease reclaimed later knows a message may already have gone out.

// ErrNotificationTargetUnverified: the sender is not bound to this
// configuration or no trial send has succeeded.
var ErrNotificationTargetUnverified = errors.New("接收成员未绑定或未完成试发验证")

// BotPushTargets lists the bound senders of a Feishu/DingTalk configuration
// as notification targets. Name shows the platform account; VerifiedAt is
// the last successful trial send.
func (r *NotificationsRepo) BotPushTargets(bot domain.NotificationBot) ([]domain.BotChat, error) {
	var rows []domain.BotUserBinding
	if err := r.DB.Where("bot_id = ? AND config_id = ? AND user_id > 0", bot.IdentityScope(), bot.ID).Order("updated_at DESC").Limit(500).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.BotChat, 0, len(rows))
	for _, row := range rows {
		name := MaskBotSender(row.SenderID)
		if user, ok := r.users.User(row.UserID); ok {
			label := user.ChineseName
			if label == "" {
				label = user.LoginName
			} else if user.LoginName != "" {
				label += "（" + user.LoginName + "）"
			}
			name = label + " · " + name
		}
		out = append(out, domain.BotChat{ID: row.SenderID, Name: name, Type: "single", VerifiedAt: row.TargetVerifiedAt})
	}
	return out, nil
}

// BotPushTarget returns the active binding of a target sender of bot.
func (r *NotificationsRepo) BotPushTarget(bot domain.NotificationBot, sender string) (domain.BotUserBinding, error) {
	binding, err := r.users.BotUserBinding(bot.IdentityScope(), sender)
	if err != nil || binding.ConfigID != bot.ID {
		return binding, ErrNotificationTargetUnverified
	}
	return binding, nil
}

// MarkBotPushTargetVerified records a successful trial send. The binding
// must still belong to the same platform user (revocation clears it).
func (r *NotificationsRepo) MarkBotPushTargetVerified(bot domain.NotificationBot, sender string, userID int64, at time.Time) error {
	result := r.DB.Model(&domain.BotUserBinding{}).Where("id = ? AND config_id = ? AND user_id = ?", botIdentityID(bot.IdentityScope(), sender), bot.ID, userID).Update("target_verified_at", at)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotificationTargetUnverified
	}
	return nil
}

// BeginNotificationPush is the first phase of a proactive send. It gives the
// claimed delivery a send key (first generation) and stamps the start time
// once per generation. It returns the updated row and how long ago (database
// clock) the generation started; resumed is true when an earlier attempt of
// this generation may already have reached the platform.
func (r *NotificationsRepo) BeginNotificationPush(item domain.NotificationDelivery) (out domain.NotificationDelivery, elapsedUS int64, resumed bool, err error) {
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		var row domain.NotificationDelivery
		if err := r.locking(tx).First(&row, "id = ? AND status = ? AND lease_token = ?", item.ID, "sending", item.LeaseToken).Error; err != nil {
			return err
		}
		now, err := r.dbNowUS(tx)
		if err != nil {
			return err
		}
		updates := map[string]any{}
		if row.SendKey == "" {
			row.SendKey = randomHex(16)
			updates["send_key"] = row.SendKey
		}
		if row.DeliveryGeneration == 0 {
			row.DeliveryGeneration = 1
			updates["delivery_generation"] = 1
		}
		resumed = row.SendStartedAtUS > 0
		if !resumed {
			row.SendStartedAtUS = now
			updates["send_started_at_us"] = now
		}
		elapsedUS = now - row.SendStartedAtUS
		out = row
		if len(updates) == 0 {
			return nil
		}
		return tx.Model(&domain.NotificationDelivery{}).Where("id = ? AND lease_token = ?", row.ID, row.LeaseToken).Updates(updates).Error
	})
	return out, elapsedUS, resumed, err
}

// NotificationPushResult is the second phase: status, next attempt and the
// evidence of a proactive send. KeepStarted keeps the generation's start
// time (unknown outcome retried with the same key); otherwise a retry starts
// clean because the platform reported nothing was sent.
type NotificationPushResult struct {
	Status            string
	Detail            string
	Next              time.Time
	PlatformMessageID string
	KeepStarted       bool
}

func (r *NotificationsRepo) FinishNotificationPush(item domain.NotificationDelivery, res NotificationPushResult) error {
	updates := map[string]any{"status": res.Status, "last_error": truncateBotDetail(res.Detail), "next_attempt_at": res.Next, "lease_until": nil, "lease_token": ""}
	if res.Status == "sent" {
		updates["sent_at"] = time.Now().UTC()
		updates["receipt_source"] = "platform"
		updates["platform_message_id"] = res.PlatformMessageID
	}
	if res.Status == "pending" && !res.KeepStarted {
		updates["send_started_at_us"] = 0
	}
	return r.DB.Model(&domain.NotificationDelivery{}).Where("id = ? AND status = ? AND lease_token = ?", item.ID, "sending", item.LeaseToken).Updates(updates).Error
}

// ConfirmNotificationReceived records an administrator's check that a
// delivery with an unknown outcome was in fact received.
func (r *NotificationsRepo) ConfirmNotificationReceived(projectID, id string, actor int64) error {
	now := time.Now().UTC()
	result := r.DB.Model(&domain.NotificationDelivery{}).Where("id = ? AND project_id = ? AND status = ?", id, projectID, "send_unknown").Updates(map[string]any{"status": "sent", "sent_at": now, "receipt_source": "manual", "confirmed_by": actor, "confirmed_at": now, "last_error": "管理员已确认收到"})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// notificationPendingStatuses are the non-terminal states affected by a
// target switch.
var notificationPendingStatuses = []string{"pending", "awaiting_authorization"}

// queuedForOtherTargets selects the queued deliveries of bot to its
// configured target, other than target. Messages back to the conversation a
// run was started from keep their conversation; older rows have no origin.
func (r *NotificationsRepo) queuedForOtherTargets(botID, target string) *gorm.DB {
	return r.DB.Model(&domain.NotificationDelivery{}).Where("bot_id = ? AND status IN ? AND target_id <> ? AND (origin IS NULL OR origin <> ?)", botID, notificationPendingStatuses, target, domain.NotificationOriginChat)
}

// PendingNotificationsForOtherTargets counts queued deliveries of bot whose
// frozen target differs from target.
func (r *NotificationsRepo) PendingNotificationsForOtherTargets(botID, target string) (int64, error) {
	var n int64
	err := r.queuedForOtherTargets(botID, target).Count(&n).Error
	return n, err
}

// SwitchNotificationTarget applies the administrator's explicit choice for
// queued deliveries of an old target: "cancel" stops them, "migrate"
// re-targets them (a new generation and key, nothing was sent to the new
// target yet). Rows being sent right now are left alone.
func (r *NotificationsRepo) SwitchNotificationTarget(botID string, target domain.BotChat, action string) (int64, error) {
	q := r.queuedForOtherTargets(botID, target.ID)
	var result *gorm.DB
	switch action {
	case "cancel":
		result = q.Updates(map[string]any{"status": "cancelled", "last_error": "接收目标已切换，管理员取消了旧目标的待发通知"})
	case "migrate":
		result = q.Updates(map[string]any{"target_id": target.ID, "target_name": target.Name, "target_type": target.Type, "send_key": "", "delivery_generation": gorm.Expr("delivery_generation + 1"), "send_started_at_us": 0, "last_error": "接收目标已切换，管理员选择改发到新目标"})
	default:
		return 0, errors.New("unknown target switch action")
	}
	return result.RowsAffected, result.Error
}
