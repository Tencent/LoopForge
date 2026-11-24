package notification

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Feishu and DingTalk notifications are sent through the channel PushSender
// to a bound private-chat sender that passed a trial send. They work for
// notification-only configurations (interaction disabled).

// pushIdempotencyWindow is how long an unknown Feishu send may be retried
// with the same uuid (Feishu de-duplicates for one hour).
const pushIdempotencyWindow = 55 * time.Minute

// ErrPushTargetUnverified: the target is not a bound sender of this
// configuration or no trial send succeeded.
var ErrPushTargetUnverified = repo.ErrNotificationTargetUnverified

// PushTargetError is a failed trial send with the classified evidence.
type PushTargetError struct{ Result botinteraction.SendResult }

func (e *PushTargetError) Error() string {
	if e.Result.SafeDetail != "" {
		return e.Result.SafeDetail
	}
	return "试发失败：" + string(e.Result.Outcome)
}

func pushChannel(bot domain.NotificationBot) bool {
	return bot.ChannelKind() != domain.BotChannelWeCom
}

// pushTargets lists bound senders for Feishu/DingTalk configurations.
func (s *Service) pushTargets(bot domain.NotificationBot) ([]domain.BotChat, error) {
	if bot.TenantState != domain.BotTenantLocked {
		return nil, errors.New("企业尚未确认，暂时不能选择通知接收人")
	}
	return s.Store.BotPushTargets(bot)
}

// checkPushTarget re-reads the target binding before every send.
func (s *Service) checkPushTarget(bot domain.NotificationBot, sender string, requireVerified bool) (domain.BotUserBinding, error) {
	if bot.TenantState != domain.BotTenantLocked {
		return domain.BotUserBinding{}, errors.New("企业尚未确认")
	}
	binding, err := s.Store.BotPushTarget(bot, sender)
	if err != nil {
		return binding, ErrPushTargetUnverified
	}
	if requireVerified && binding.TargetVerifiedAt == nil {
		return binding, ErrPushTargetUnverified
	}
	if !botinteraction.ProjectVisible(s.Store, bot.ProjectID, binding.UserID) {
		return binding, errors.New("接收成员的平台账号已没有该项目的权限")
	}
	return binding, nil
}

// VerifyPushTarget sends a trial message to a bound sender and, on a
// confirmed delivery, records target_verified_at. The trial is recorded as a
// test delivery. Unknown outcomes do not verify the target.
func (s *Service) VerifyPushTarget(ctx context.Context, bot domain.NotificationBot, sender string) (domain.BotChat, error) {
	if !bot.Enabled {
		return domain.BotChat{}, errors.New("机器人已停用")
	}
	binding, err := s.checkPushTarget(bot, sender, false)
	if err != nil {
		return domain.BotChat{}, err
	}
	var target domain.BotChat
	targets, err := s.Store.BotPushTargets(bot)
	if err != nil {
		return target, err
	}
	for _, t := range targets {
		if t.ID == sender {
			target = t
		}
	}
	if target.ID == "" {
		return target, ErrPushTargetUnverified
	}
	now := time.Now().UTC()
	content := "平台通知测试\n机器人：" + bot.Name + "\n这是一条试发消息，用于确认可以向你发送项目通知，不代表实际任务状态。"
	item := domain.NotificationDelivery{ID: "test-" + randomToken(), ProjectID: bot.ProjectID, BotID: bot.ID, EventType: "test", TargetID: target.ID, TargetName: target.Name, TargetType: target.Type, Content: content, Status: "sending", Attempts: 1, LeaseToken: randomToken(), CreatedAt: now, NextAttemptAt: now}
	until := now.Add(2 * time.Minute)
	item.LeaseUntil = &until
	if err := s.Store.CreateNotificationDelivery(item); err != nil {
		return target, err
	}
	begun, _, _, err := s.Store.BeginNotificationPush(item)
	if err != nil {
		return target, err
	}
	res := s.Interactions.Push(ctx, bot, botinteraction.OutboundMessage{InteractionID: item.ID, ChatID: target.ID, ChatType: "single", Text: content, SendKey: begun.SendKey})
	finish := repo.NotificationPushResult{Status: "failed", Detail: res.SafeDetail, Next: now}
	switch res.Outcome {
	case botinteraction.Delivered:
		finish = repo.NotificationPushResult{Status: "sent", Next: now, PlatformMessageID: res.PlatformMessageID}
	case botinteraction.Unknown:
		finish.Status = "send_unknown"
	}
	if err := s.Store.FinishNotificationPush(item, finish); err != nil {
		return target, err
	}
	if res.Outcome != botinteraction.Delivered {
		return target, &PushTargetError{Result: res}
	}
	if err := s.Store.MarkBotPushTargetVerified(bot, sender, binding.UserID, now); err != nil {
		return target, err
	}
	target.VerifiedAt = &now
	return target, nil
}

// processPush sends one claimed Feishu/DingTalk delivery.
func (s *Service) processPush(ctx context.Context, item *domain.NotificationDelivery, bot domain.NotificationBot, content string, beforeSend func() error) (bool, error) {
	now := time.Now().UTC()
	done := func(res repo.NotificationPushResult) (bool, error) {
		if res.Next.IsZero() {
			res.Next = now
		}
		return true, s.Store.FinishNotificationPush(*item, res)
	}
	if _, err := s.checkDeliveryTarget(bot, *item); err != nil {
		return done(repo.NotificationPushResult{Status: "target_unavailable", Detail: err.Error()})
	}
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			return done(repo.NotificationPushResult{Status: "expired", Detail: err.Error()})
		}
	}
	begun, elapsed, resumed, err := s.Store.BeginNotificationPush(*item)
	if err != nil {
		return true, err
	}
	idempotent := botinteraction.PushIdempotent(bot)
	if resumed && (!idempotent || time.Duration(elapsed)*time.Microsecond > pushIdempotencyWindow) {
		// An earlier attempt of this generation may have reached the
		// platform and cannot be de-duplicated: stop for manual review.
		return done(repo.NotificationPushResult{Status: "send_unknown", Detail: "上一次发送中断，无法确认是否送达，请核对后确认收到或再次发送"})
	}
	msg := botinteraction.OutboundMessage{InteractionID: item.ID, ChatID: item.TargetID, ChatType: "single", Text: plainNotificationText(content), SendKey: begun.SendKey}
	if item.TargetType == "group" {
		msg.ChatType = "group"
	}
	// Waiting and failed nodes get an action card when the Bot receives
	// messages (the click comes back on its connection).
	if bot.InteractionEnabled && item.NodeID != "" && (item.EventType == "waiting" || item.EventType == "failed") {
		msg.Card = s.Interactions.PushCardFor(*item, content, s.runURL(item.RunID))
	}
	res := s.Interactions.Push(ctx, bot, msg)
	retryAt := now.Add(notificationBackoff(item.Attempts))
	switch res.Outcome {
	case botinteraction.Delivered:
		return done(repo.NotificationPushResult{Status: "sent", PlatformMessageID: res.PlatformMessageID})
	case botinteraction.NotDelivered:
		// Nothing was sent this time, but an earlier attempt of this
		// generation (resumed) may have been: keep its start time.
		if item.Attempts >= 5 {
			if resumed {
				return done(repo.NotificationPushResult{Status: "send_unknown", Detail: "首次发送结果未知，后续重试未成功，请核对后确认收到或再次发送"})
			}
			return done(repo.NotificationPushResult{Status: "failed", Detail: "多次发送未成功：" + res.SafeDetail})
		}
		return done(repo.NotificationPushResult{Status: "pending", Detail: res.SafeDetail, Next: retryAt, KeepStarted: resumed})
	case botinteraction.Rejected:
		if resumed {
			return done(repo.NotificationPushResult{Status: "send_unknown", Detail: "此前发送结果未知，重试请求被拒绝，请核对后确认收到或再次发送"})
		}
		status := "failed"
		if res.Code == "invalid_staff" || res.Code == "target_missing" {
			status = "target_unavailable"
		}
		return done(repo.NotificationPushResult{Status: status, Detail: res.SafeDetail})
	default:
		if idempotent && item.Attempts < 5 && time.Duration(elapsed)*time.Microsecond < pushIdempotencyWindow {
			return done(repo.NotificationPushResult{Status: "pending", Detail: res.SafeDetail + "，将用同一发送 key 重试", Next: now.Add(5 * time.Second), KeepStarted: true})
		}
		return done(repo.NotificationPushResult{Status: "send_unknown", Detail: res.SafeDetail})
	}
}

// checkDeliveryTarget checks the receiver of a delivery before each send.
// SOP notifications go to the configured, trial-verified member. Messages
// back to the chat a run was started from need no trial send: a private
// origin must still be a bound sender with access to the project; a group
// origin needs the confirmed enterprise.
func (s *Service) checkDeliveryTarget(bot domain.NotificationBot, item domain.NotificationDelivery) (domain.BotUserBinding, error) {
	if item.Origin != domain.NotificationOriginChat {
		return s.checkPushTarget(bot, item.TargetID, true)
	}
	if item.TargetType == "group" {
		if bot.TenantState != domain.BotTenantLocked {
			return domain.BotUserBinding{}, errors.New("企业尚未确认")
		}
		return domain.BotUserBinding{}, nil
	}
	return s.checkPushTarget(bot, item.TargetID, false)
}

func notificationBackoff(attempts int) time.Duration {
	delay := time.Duration(1<<min(attempts, 6)) * 10 * time.Second
	jitter := make([]byte, 1)
	_, _ = rand.Read(jitter)
	return delay + time.Duration(jitter[0]%10)*time.Second
}

var (
	plainLinkPattern    = regexp.MustCompile(`\[([^\]\n]*)\]\((https?://[^)\s]+)\)`)
	plainHeadingPattern = regexp.MustCompile(`(?m)^#{1,6}[ \t]+`)
	plainQuotePattern   = regexp.MustCompile(`(?m)^>[ \t]?`)
	plainFontPattern    = regexp.MustCompile(`</?font[^>]*>`)
)

// plainNotificationText turns the WeCom Markdown of rendered notifications
// into plain text for Feishu and DingTalk text messages. Links keep the URL
// on its own line so chat clients do not swallow surrounding characters.
func plainNotificationText(content string) string {
	out := plainLinkPattern.ReplaceAllStringFunc(content, func(m string) string {
		parts := plainLinkPattern.FindStringSubmatch(m)
		label := strings.TrimSpace(parts[1])
		if label == "" || label == parts[2] {
			return "\n" + parts[2] + "\n"
		}
		return label + "：\n" + parts[2] + "\n"
	})
	out = plainHeadingPattern.ReplaceAllString(out, "")
	out = plainQuotePattern.ReplaceAllString(out, "")
	out = plainFontPattern.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "**", "")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(out)
}

// runURL is the run page on the platform, or empty without a public URL.
func (s *Service) runURL(runID string) string {
	base, err := url.Parse(s.PublicURL)
	if runID == "" || err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return ""
	}
	return strings.TrimRight(s.PublicURL, "/") + "/#/runs/" + url.PathEscape(runID)
}
