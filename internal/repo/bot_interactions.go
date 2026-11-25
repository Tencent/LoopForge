package repo

import (
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BotInteractionsRepo struct{ *deps }

func BotConversationID(botID, kind, chatID string) string {
	return hexSHA256(botID + "\x00" + kind + "\x00" + chatID)
}
func BotMessageID(botID, msgID string) string {
	return hexSHA256(botID + "\x00" + msgID)
}
func newBotSessionID() string {
	return "bot-" + randomHex(16)
}
func randomHex(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}

// Lease and delivery timing. All comparisons use the database clock.
const (
	BotConnectionLeaseUS = int64(45 * time.Second / time.Microsecond)
	// A send starts only when the connection lease has at least this much left.
	BotSendMinLeaseUS = int64(15 * time.Second / time.Microsecond)
	// One SDK call is bounded by this timeout.
	BotSendTimeoutUS = int64(10 * time.Second / time.Microsecond)
	// The send lease blocks another owner from recovering an in-flight send.
	BotSendLeaseUS = int64(20 * time.Second / time.Microsecond)
	// Same-key recovery window for platforms with request de-duplication
	// (Feishu uuid: one hour; 55 minutes leaves margin).
	BotIdempotentWindowUS = int64(55 * time.Minute / time.Microsecond)
	// Retries after an explicit not-delivered outcome.
	BotUndeliveredMaxRetries = 3
)

var botRetryBackoffUS = []int64{int64(5 * time.Second / time.Microsecond), int64(30 * time.Second / time.Microsecond), int64(2 * time.Minute / time.Microsecond)}

func botBackoffUS(attempt int) int64 {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(botRetryBackoffUS) {
		return botRetryBackoffUS[len(botRetryBackoffUS)-1]
	}
	return botRetryBackoffUS[attempt]
}

var (
	// ErrBotLeaseLost: the caller no longer owns the connection lease or the
	// interaction, or the configuration stopped receiving. Stop local work.
	ErrBotLeaseLost = errors.New("bot_lease_lost")
	// ErrBotLeaseLow: the lease is valid but too short to start a send.
	ErrBotLeaseLow = errors.New("bot_lease_low")
	// ErrBotStateConflict: the interaction is not in the expected state.
	ErrBotStateConflict = errors.New("bot_state_conflict")
	// ErrBotDeleted / ErrBotRouteExpired: manual resend is impossible.
	ErrBotDeleted      = errors.New("bot_deleted")
	ErrBotRouteExpired = errors.New("route_expired")
)

// BotLease identifies one ownership period of a receiving connection.
type BotLease struct {
	Key   string
	Owner string
	Epoch int64
}

// ClaimBotConnection takes the lease when it is free or expired, incrementing
// the epoch for a new owner. The same live owner keeps its epoch.
func (r *BotInteractionsRepo) ClaimBotConnection(key, owner string) (BotLease, bool, error) {
	row := domain.BotConnection{BotID: key, State: "connecting", LeaseUntil: time.Now().UTC().Add(-time.Second)}
	if err := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return BotLease{}, false, err
	}
	now := r.dbNowUSSQL()
	// Assignment order matters on MySQL (left to right, later terms see new
	// values): compute epoch and changes from the previous owner first.
	result := r.DB.Exec(`UPDATE bot_connections SET
		epoch = CASE WHEN owner = ? AND lease_until_us > `+now+` THEN epoch ELSE epoch + 1 END,
		owner_changes = CASE WHEN owner = ? THEN owner_changes ELSE owner_changes + 1 END,
		owner = ?, lease_until_us = `+now+` + ?, lease_until = ?, updated_at = ?
		WHERE bot_id = ? AND (owner = ? OR lease_until_us <= `+now+`)`,
		owner, owner, owner, BotConnectionLeaseUS, time.Now().UTC().Add(45*time.Second), time.Now().UTC(), key, owner)
	if result.Error != nil || result.RowsAffected != 1 {
		return BotLease{}, false, result.Error
	}
	var claimed domain.BotConnection
	if err := r.DB.First(&claimed, "bot_id = ?", key).Error; err != nil {
		return BotLease{}, false, err
	}
	if claimed.Owner != owner {
		return BotLease{}, false, nil
	}
	return BotLease{Key: key, Owner: owner, Epoch: claimed.Epoch}, true, nil
}

// RenewBotConnection extends a live lease held by the same owner and epoch.
// A non-empty state also records the connection state for display.
func (r *BotInteractionsRepo) RenewBotConnection(lease BotLease, state, detail string) (bool, error) {
	updates := map[string]any{"lease_until_us": r.nowUSExpr(BotConnectionLeaseUS), "lease_until": time.Now().UTC().Add(45 * time.Second), "updated_at": time.Now().UTC()}
	if state != "" {
		updates["state"] = state
		updates["last_error"] = detail
	}
	result := r.DB.Model(&domain.BotConnection{}).Where("bot_id = ? AND owner = ? AND epoch = ? AND lease_until_us > "+r.dbNowUSSQL(), lease.Key, lease.Owner, lease.Epoch).Updates(updates)
	return result.RowsAffected == 1, result.Error
}

func (r *BotInteractionsRepo) ReleaseBotConnection(lease BotLease) error {
	return r.DB.Model(&domain.BotConnection{}).Where("bot_id = ? AND owner = ? AND epoch = ?", lease.Key, lease.Owner, lease.Epoch).Updates(map[string]any{"owner": "", "lease_until_us": 0, "lease_until": time.Now().UTC().Add(-time.Second), "state": "offline"}).Error
}

// BotConnectionState reports an expired lease as offline.
func (r *BotInteractionsRepo) BotConnectionState(key string) (domain.BotConnection, error) {
	var out domain.BotConnection
	if err := r.DB.First(&out, "bot_id = ?", key).Error; err != nil {
		return out, err
	}
	if now, err := r.dbNowUS(r.DB); err == nil && out.LeaseUntilUS <= now {
		out.State = "offline"
	}
	return out, nil
}

// NoteBotConnectionEvent records the last event time and counts ignored group
// messages for diagnostics. Best effort; it never fences.
func (r *BotInteractionsRepo) NoteBotConnectionEvent(key string, ignoredGroup bool) {
	updates := map[string]any{"last_event_at": time.Now().UTC()}
	if ignoredGroup {
		updates["ignored_group_messages"] = gorm.Expr("ignored_group_messages + 1")
	}
	r.DB.Model(&domain.BotConnection{}).Where("bot_id = ?", key).Updates(updates)
}

// botFenceConfig is the part of notification_bots that fences interaction
// writes. It is read here, in the lock order config -> connection ->
// interaction, so this repo does not depend on NotificationsRepo (which
// already depends on it).
type botFenceConfig struct {
	ID                 string
	Enabled            bool
	InteractionEnabled bool
	ConfigVersion      int64
}

func (r *BotInteractionsRepo) lockBotConfig(tx *gorm.DB, configID string) (botFenceConfig, error) {
	var cfg botFenceConfig
	err := r.locking(tx.Table("notification_bots")).Select("id, enabled, interaction_enabled, config_version").Where("id = ?", configID).Take(&cfg).Error
	return cfg, err
}

func (r *BotInteractionsRepo) lockConnection(tx *gorm.DB, key string) (domain.BotConnection, error) {
	var conn domain.BotConnection
	err := r.locking(tx).Where("bot_id = ?", key).First(&conn).Error
	return conn, err
}

// fence locks the configuration and the connection (in that order) and
// verifies that the caller still owns a live lease with at least minRemainUS
// left. It returns the database clock read inside the transaction.
func (r *BotInteractionsRepo) fence(tx *gorm.DB, configID string, lease BotLease, minRemainUS int64) (int64, botFenceConfig, error) {
	cfg, err := r.lockBotConfig(tx, configID)
	if err != nil || !cfg.Enabled || !cfg.InteractionEnabled {
		return 0, cfg, ErrBotLeaseLost
	}
	conn, err := r.lockConnection(tx, lease.Key)
	if err != nil {
		return 0, cfg, ErrBotLeaseLost
	}
	now, err := r.dbNowUS(tx)
	if err != nil {
		return 0, cfg, err
	}
	if conn.Owner != lease.Owner || conn.Epoch != lease.Epoch || conn.LeaseUntilUS <= now {
		return now, cfg, ErrBotLeaseLost
	}
	if conn.LeaseUntilUS-now < minRemainUS {
		return now, cfg, ErrBotLeaseLow
	}
	return now, cfg, nil
}

// fencedUpdate applies updates to one interaction owned by lease in one of
// the given states. Zero affected rows is a lease or state conflict.
func (r *BotInteractionsRepo) fencedUpdate(tx *gorm.DB, id string, lease BotLease, from []string, updates map[string]any) error {
	updates["updated_at"] = time.Now().UTC()
	result := tx.Model(&domain.BotInteraction{}).Where("id = ? AND status IN ? AND lease_owner = ? AND lease_epoch = ?", id, from, lease.Owner, lease.Epoch).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrBotLeaseLost
	}
	return nil
}

func (r *BotInteractionsRepo) EnqueueBotInteraction(item domain.BotInteraction, kind, chat string) (bool, error) {
	inserted := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		conversation := domain.BotConversation{ID: BotConversationID(item.BotID, kind, chat), BotID: item.BotID, ChatID: chat, ChatType: kind, SessionID: newBotSessionID(), UpdatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&conversation).Error; err != nil {
			return err
		}
		item.ConversationID = conversation.ID
		item.Status = domain.BotQueued
		item.CreatedAt = time.Now().UTC()
		item.UpdatedAt = item.CreatedAt
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item)
		inserted = result.RowsAffected == 1
		if result.Error != nil {
			return result.Error
		}
		if inserted {
			return tx.Model(&conversation).Update("updated_at", item.CreatedAt).Error
		}
		return nil
	})
	return inserted, err
}

// BotSendPolicy describes the channel's delivery guarantees.
type BotSendPolicy struct {
	// Idempotent: the platform de-duplicates retries carrying the same key
	// within BotIdempotentWindowUS (Feishu uuid). Unknown outcomes are then
	// retried with the same key instead of becoming reply_unknown.
	Idempotent bool
}

// ClaimBotInteraction returns the next queued item (now running) or a ready
// item whose next send is due, stamping it with the lease. A send whose send
// lease expired (the sender stopped mid-call) is resolved as an unknown
// outcome first.
func (r *BotInteractionsRepo) ClaimBotInteraction(configID string, lease BotLease, policy BotSendPolicy) (*domain.BotInteraction, error) {
	var item domain.BotInteraction
	resolved := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		now, cfg, err := r.fence(tx, configID, lease, 0)
		if err != nil {
			return err
		}
		if err := r.locking(tx).Where("bot_id = ? AND (status = ? OR (status = ? AND next_send_at_us <= ?) OR (status = ? AND send_lease_until_us <= ?))", configID, domain.BotQueued, domain.BotReady, now, domain.BotSending, now).Order("created_at, id").First(&item).Error; err != nil {
			return err
		}
		switch item.Status {
		case domain.BotSending:
			resolved = true
			return r.resolveUnknown(tx, &item, now, policy, "发送过程中断，平台未返回结果")
		case domain.BotReady:
			item.LeaseOwner, item.LeaseEpoch = lease.Owner, lease.Epoch
			return tx.Model(&item).Updates(map[string]any{"lease_owner": lease.Owner, "lease_epoch": lease.Epoch, "updated_at": time.Now().UTC()}).Error
		}
		var conv domain.BotConversation
		if err := tx.First(&conv, "id = ?", item.ConversationID).Error; err != nil {
			return err
		}
		item.SessionID = conv.SessionID
		item.Status = domain.BotRunning
		item.LeaseOwner, item.LeaseEpoch, item.ConfigVersion = lease.Owner, lease.Epoch, cfg.ConfigVersion
		return tx.Model(&item).Updates(map[string]any{"session_id": item.SessionID, "status": domain.BotRunning, "lease_owner": lease.Owner, "lease_epoch": lease.Epoch, "config_version": cfg.ConfigVersion, "updated_at": time.Now().UTC()}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if resolved {
		// Resolved an interrupted send; the caller polls again.
		return nil, nil
	}
	return &item, nil
}

// resolveUnknown applies the unknown-outcome rule to a sending item: same-key
// retry inside the idempotency window, otherwise reply_unknown.
func (r *BotInteractionsRepo) resolveUnknown(tx *gorm.DB, item *domain.BotInteraction, now int64, policy BotSendPolicy, detail string) error {
	updates := map[string]any{"send_lease_until_us": 0, "updated_at": time.Now().UTC()}
	if policy.Idempotent && item.SendStartedAtUS > 0 && now+BotSendTimeoutUS < item.SendStartedAtUS+BotIdempotentWindowUS {
		retries := item.SendAttempts - item.UndeliveredRetries - 1
		updates["status"], updates["next_send_at_us"] = domain.BotReady, now+botBackoffUS(retries)
		updates["last_error"] = truncateBotDetail(detail + "，将使用同一请求标识重试")
	} else {
		updates["status"], updates["terminal_reason"] = domain.BotReplyUnknown, "delivery_unknown"
		updates["last_error"] = truncateBotDetail(detail + "，送达结果待核对")
	}
	item.Status = updates["status"].(string)
	return tx.Model(&domain.BotInteraction{}).Where("id = ? AND status = ?", item.ID, domain.BotSending).Updates(updates).Error
}

// RecoverBotInteractions runs when a new lease starts. A running model turn
// is never replayed (its platform tools may have committed): it becomes ready
// with an interruption notice. Sends whose send lease ended are resolved as
// unknown outcomes; ready items keep their saved replies.
func (r *BotInteractionsRepo) RecoverBotInteractions(configID string, lease BotLease, policy BotSendPolicy) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		now, _, err := r.fence(tx, configID, lease, 0)
		if err != nil {
			return err
		}
		var running []domain.BotInteraction
		if err := r.locking(tx).Where("bot_id = ? AND status = ?", configID, domain.BotRunning).Order("id").Find(&running).Error; err != nil {
			return err
		}
		for _, item := range running {
			updates := map[string]any{"status": domain.BotReady, "reply": "上一轮处理已中断，执行结果可能已保存。请先查询状态，再决定是否继续操作。", "last_error": "执行中断", "next_send_at_us": 0, "updated_at": time.Now().UTC()}
			if item.SendKey == "" {
				updates["send_key"], updates["delivery_generation"] = randomHex(16), 1
			}
			if err := tx.Model(&domain.BotInteraction{}).Where("id = ? AND status = ?", item.ID, domain.BotRunning).Updates(updates).Error; err != nil {
				return err
			}
		}
		var sending []domain.BotInteraction
		if err := r.locking(tx).Where("bot_id = ? AND status = ? AND send_lease_until_us <= ?", configID, domain.BotSending, now).Order("id").Find(&sending).Error; err != nil {
			return err
		}
		for i := range sending {
			if err := r.resolveUnknown(tx, &sending[i], now, policy, "发送过程中断，平台未返回结果"); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveBotReply stores the generated reply of a running turn owned by lease
// and fixes the send key of the first delivery generation.
func (r *BotInteractionsRepo) SaveBotReply(item domain.BotInteraction, lease BotLease, reply, detail string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, _, err := r.fence(tx, item.BotID, lease, 0); err != nil {
			return err
		}
		return r.fencedUpdate(tx, item.ID, lease, []string{domain.BotRunning}, map[string]any{"status": domain.BotReady, "reply": reply, "last_error": detail, "send_key": randomHex(16), "delivery_generation": 1, "next_send_at_us": 0, "send_started_at_us": 0, "send_attempts": 0, "undelivered_retries": 0})
	})
}

// BeginBotSend moves a ready item owned by lease to sending. It refuses when
// the lease has less than BotSendMinLeaseUS left, fails an expired reply
// route, and closes the idempotency window before it would be exceeded.
func (r *BotInteractionsRepo) BeginBotSend(id string, lease BotLease, policy BotSendPolicy) (domain.BotInteraction, error) {
	var item domain.BotInteraction
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := r.locking(tx).First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		now, _, err := r.fence(tx, item.BotID, lease, BotSendMinLeaseUS)
		if err != nil {
			return err
		}
		if item.Status != domain.BotReady || item.LeaseOwner != lease.Owner || item.LeaseEpoch != lease.Epoch {
			return ErrBotLeaseLost
		}
		if item.RouteExpiresAtUS > 0 && now+BotSendTimeoutUS >= item.RouteExpiresAtUS {
			item.Status = domain.BotReplyFailed
			return r.fencedUpdate(tx, id, lease, []string{domain.BotReady}, map[string]any{"status": domain.BotReplyFailed, "terminal_reason": "route_expired", "last_error": "会话回复授权已过期，结果可在记录页查看", "reply_route": clearedRoute(item.ReplyRoute)})
		}
		if policy.Idempotent && item.SendStartedAtUS > 0 && now+BotSendTimeoutUS >= item.SendStartedAtUS+BotIdempotentWindowUS {
			item.Status = domain.BotReplyUnknown
			return r.fencedUpdate(tx, id, lease, []string{domain.BotReady}, map[string]any{"status": domain.BotReplyUnknown, "terminal_reason": "window_expired", "last_error": "已超过平台去重窗口，送达结果待核对"})
		}
		if item.SendKey == "" {
			item.SendKey = randomHex(16)
		}
		if item.DeliveryGeneration == 0 {
			item.DeliveryGeneration = 1
		}
		if item.SendStartedAtUS == 0 {
			item.SendStartedAtUS = now
		}
		item.SendAttempts++
		item.Status = domain.BotSending
		item.SendLeaseUntilUS = now + BotSendLeaseUS
		return r.fencedUpdate(tx, id, lease, []string{domain.BotReady}, map[string]any{"status": domain.BotSending, "send_key": item.SendKey, "delivery_generation": item.DeliveryGeneration, "send_started_at_us": item.SendStartedAtUS, "send_attempts": item.SendAttempts, "send_lease_until_us": item.SendLeaseUntilUS})
	})
	if err != nil {
		return item, err
	}
	if item.Status != domain.BotSending {
		return item, ErrBotStateConflict
	}
	return item, nil
}

// BotSendOutcome is the classified result of one send attempt.
type BotSendOutcome struct {
	Outcome           string // delivered, not_delivered, rejected, unknown
	PlatformMessageID string
	Code              string
	Detail            string
}

// FinishBotSend records a send result on a sending item owned by lease:
// delivered -> replied; rejected -> reply_failed; not delivered -> retry by
// count (reply_failed when exhausted); unknown -> same-key retry inside the
// idempotency window for idempotent channels, else reply_unknown.
func (r *BotInteractionsRepo) FinishBotSend(id string, lease BotLease, policy BotSendPolicy, out BotSendOutcome) (domain.BotInteraction, error) {
	var item domain.BotInteraction
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := r.locking(tx).First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		now, _, err := r.fence(tx, item.BotID, lease, 0)
		if err != nil {
			return err
		}
		if item.Status != domain.BotSending || item.LeaseOwner != lease.Owner || item.LeaseEpoch != lease.Epoch {
			return ErrBotLeaseLost
		}
		updates := map[string]any{"send_lease_until_us": 0}
		// SendAttempts includes the current attempt. The remaining earlier
		// attempts were either counted as undelivered or may have gone out.
		priorSendUnknown := item.SendAttempts > item.UndeliveredRetries+1
		switch out.Outcome {
		case "delivered":
			updates["status"], updates["platform_message_id"], updates["receipt_source"] = domain.BotReplied, out.PlatformMessageID, "platform"
			updates["terminal_reason"], updates["reply_route"] = "", clearedRoute(item.ReplyRoute)
		case "rejected":
			if priorSendUnknown {
				return r.resolveUnknown(tx, &item, now, BotSendPolicy{}, out.Detail+"，此前发送结果待核对")
			}
			reason := "rejected"
			if out.Code == domain.BotTerminalTargetUnavailable || out.Code == domain.BotTerminalNotificationExpired {
				reason = out.Code
			}
			updates["status"], updates["terminal_reason"], updates["last_error"] = domain.BotReplyFailed, reason, truncateBotDetail(out.Detail)
		case "not_delivered":
			if item.UndeliveredRetries < BotUndeliveredMaxRetries {
				updates["status"], updates["next_send_at_us"] = domain.BotReady, now+botBackoffUS(item.UndeliveredRetries)
				updates["undelivered_retries"], updates["last_error"] = item.UndeliveredRetries+1, truncateBotDetail(out.Detail)
			} else if priorSendUnknown {
				return r.resolveUnknown(tx, &item, now, BotSendPolicy{}, out.Detail+"，此前发送结果待核对")
			} else {
				updates["status"], updates["terminal_reason"], updates["last_error"] = domain.BotReplyFailed, "undelivered_exhausted", truncateBotDetail("多次发送未成功："+out.Detail)
			}
		default:
			item.Status = domain.BotSending
			return r.resolveUnknown(tx, &item, now, policy, out.Detail)
		}
		item.Status = updates["status"].(string)
		return r.fencedUpdate(tx, id, lease, []string{domain.BotSending}, updates)
	})
	return item, err
}

// clearedRoute drops an authorization-bearing (sealed) route once it can no
// longer be used; plain routing data such as a Feishu message ID is kept.
func clearedRoute(route string) string {
	if credcrypt.IsSealed(route) {
		return ""
	}
	return route
}

func truncateBotDetail(s string) string {
	runes := []rune(s)
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return s
}

// NewBotConversation rotates the conversation session for a /new command
// owned by lease.
func (r *BotInteractionsRepo) NewBotConversation(item domain.BotInteraction, lease BotLease) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, _, err := r.fence(tx, item.BotID, lease, 0); err != nil {
			return err
		}
		session := newBotSessionID()
		if err := r.fencedUpdate(tx, item.ID, lease, []string{domain.BotRunning}, map[string]any{"session_id": session, "action": "new"}); err != nil {
			return err
		}
		return tx.Model(&domain.BotConversation{}).Where("id = ?", item.ConversationID).Updates(map[string]any{"session_id": session, "updated_at": time.Now().UTC()}).Error
	})
}
func (r *BotInteractionsRepo) BotConversation(id string) (domain.BotConversation, error) {
	var out domain.BotConversation
	err := r.DB.First(&out, "id = ?", id).Error
	return out, err
}
func (r *BotInteractionsRepo) BotHistory(sessionID string) ([]domain.BotInteraction, error) {
	var rows []domain.BotInteraction
	err := r.DB.Where("session_id = ? AND status = ? AND action = ?", sessionID, domain.BotReplied, "").Order("created_at desc, id desc").Limit(20).Find(&rows).Error
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, err
}
func (r *BotInteractionsRepo) BotInteractionRecords(botID string) ([]domain.BotInteraction, error) {
	var rows []domain.BotInteraction
	err := r.DB.Where("bot_id = ?", botID).Order("created_at desc, id desc").Limit(50).Find(&rows).Error
	return rows, err
}

// BotInteractionsAwaitingReview lists a project's interactions that need a
// manual decision, including records of deleted Bots (project snapshot).
func (r *BotInteractionsRepo) BotInteractionsAwaitingReview(projectID string) ([]domain.BotInteraction, error) {
	rows := []domain.BotInteraction{}
	err := r.DB.Where("project_id = ? AND status IN ?", projectID, []string{domain.BotReplyUnknown, domain.BotReplyFailed}).Order("updated_at desc, id desc").Limit(50).Find(&rows).Error
	return rows, err
}
func (r *BotInteractionsRepo) BotConversations(botID string) ([]domain.BotConversation, error) {
	var rows []domain.BotConversation
	err := r.DB.Where("bot_id = ?", botID).Order("updated_at desc").Limit(50).Find(&rows).Error
	return rows, err
}

func (r *BotInteractionsRepo) BotInteraction(id string) (domain.BotInteraction, error) {
	var item domain.BotInteraction
	err := r.DB.First(&item, "id = ?", id).Error
	return item, err
}

// ExpireBotCard closes a notification card whose node no longer waits.
func (r *BotInteractionsRepo) ExpireBotCard(id string) error {
	return r.DB.Model(&domain.BotInteraction{}).Where("id = ? AND action = ? AND status IN ?", id, "notification", []string{domain.BotQueued, domain.BotRunning, domain.BotReady}).Updates(map[string]any{"status": domain.BotReplied, "last_error": "该节点已不再等待本次操作", "updated_at": time.Now().UTC()}).Error
}

// FailBotReply ends a ready item owned by lease without sending (for example
// a capability the channel lacks).
func (r *BotInteractionsRepo) FailBotReply(id string, lease BotLease, detail string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var item domain.BotInteraction
		if err := tx.First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		if _, _, err := r.fence(tx, item.BotID, lease, 0); err != nil {
			return err
		}
		return r.fencedUpdate(tx, id, lease, []string{domain.BotReady}, map[string]any{"status": domain.BotReplyFailed, "terminal_reason": "unsupported", "last_error": truncateBotDetail(detail)})
	})
}

// RequeueBotCard re-sends a failed notification card or chat post as a new
// generation.
func (r *BotInteractionsRepo) RequeueBotCard(id string) error {
	return r.DB.Model(&domain.BotInteraction{}).Where("id = ? AND action IN ? AND status = ?", id, []string{"notification", "chat_post"}, domain.BotReplyFailed).Updates(map[string]any{"status": domain.BotReady, "last_error": "", "terminal_reason": "", "send_key": randomHex(16), "delivery_generation": gorm.Expr("delivery_generation + 1"), "send_started_at_us": 0, "send_attempts": 0, "undelivered_retries": 0, "next_send_at_us": 0, "updated_at": time.Now().UTC()}).Error
}

// Serializes registering the same external Bot across project configs. The
// receiver later claims this same connection row, avoiding duplicate sockets.
func (r *BotInteractionsRepo) lockBotDefinition(tx *gorm.DB, key string) error {
	row := domain.BotConnection{BotID: key, LeaseUntil: time.Now().UTC().Add(-time.Second), State: "offline"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	return r.locking(tx).Where("bot_id = ?", key).First(&row).Error
}

// SetBotCardTaskID stores the exact opaque task ID before wire send so
// callbacks cannot route by a guessed delivery prefix or a stale card.
func (r *BotInteractionsRepo) SetBotCardTaskID(id string, lease BotLease, taskID string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var item domain.BotInteraction
		if err := tx.First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		if _, _, err := r.fence(tx, item.BotID, lease, 0); err != nil {
			return err
		}
		return r.fencedUpdate(tx, id, lease, []string{domain.BotReady}, map[string]any{"card_task_id": taskID})
	})
}

// ResendBotInteraction is the manual "send again" for reply_unknown or
// reply_failed: a new delivery generation with a new key, sending only the
// saved reply. The same actionKey returns the current state without a
// second generation. Lock order: configuration, then the interaction.
func (r *BotInteractionsRepo) ResendBotInteraction(id string, actor int64, actionKey, reason string) (domain.BotInteraction, error) {
	var item domain.BotInteraction
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		cfg, cfgErr := r.lockBotConfig(tx, item.BotID)
		if err := r.locking(tx).First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		if actionKey != "" && item.LastActionKey == actionKey {
			return nil
		}
		if errors.Is(cfgErr, gorm.ErrRecordNotFound) {
			return ErrBotDeleted
		}
		if cfgErr != nil {
			return cfgErr
		}
		if !cfg.Enabled || !cfg.InteractionEnabled {
			return ErrBotStateConflict
		}
		if item.Status != domain.BotReplyUnknown && item.Status != domain.BotReplyFailed {
			return ErrBotStateConflict
		}
		if item.BodyPurgedAt != nil {
			// The saved reply was removed by the retention cleanup.
			return ErrBotStateConflict
		}
		now, err := r.dbNowUS(tx)
		if err != nil {
			return err
		}
		if item.TerminalReason == "route_expired" || (item.RouteExpiresAtUS > 0 && now+BotSendTimeoutUS >= item.RouteExpiresAtUS) {
			return ErrBotRouteExpired
		}
		item.Status = domain.BotReady
		item.DeliveryGeneration++
		item.SendKey = randomHex(16)
		result := tx.Model(&domain.BotInteraction{}).Where("id = ? AND status IN ?", id, []string{domain.BotReplyUnknown, domain.BotReplyFailed}).Updates(map[string]any{"status": domain.BotReady, "delivery_generation": item.DeliveryGeneration, "send_key": item.SendKey, "send_started_at_us": 0, "send_attempts": 0, "undelivered_retries": 0, "next_send_at_us": 0, "send_lease_until_us": 0, "lease_owner": "", "lease_epoch": 0, "terminal_reason": "", "resent_by": actor, "last_action_key": actionKey, "last_error": truncateBotDetail("人工再次发送：" + reason), "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrBotStateConflict
		}
		return nil
	})
	return item, err
}

// ConfirmBotInteraction records that the recipient did receive a
// reply_unknown message. It works after the Bot is deleted as well.
func (r *BotInteractionsRepo) ConfirmBotInteraction(id string, actor int64, actionKey string) (domain.BotInteraction, error) {
	var item domain.BotInteraction
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		if _, err := r.lockBotConfig(tx, item.BotID); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := r.locking(tx).First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		if actionKey != "" && item.LastActionKey == actionKey {
			return nil
		}
		if item.Status != domain.BotReplyUnknown {
			return ErrBotStateConflict
		}
		at := time.Now().UTC()
		item.Status, item.ConfirmedBy, item.ConfirmedAt, item.ReceiptSource = domain.BotReplied, actor, &at, "manual"
		return tx.Model(&domain.BotInteraction{}).Where("id = ? AND status = ?", id, domain.BotReplyUnknown).Updates(map[string]any{"status": domain.BotReplied, "confirmed_by": actor, "confirmed_at": at, "receipt_source": "manual", "last_action_key": actionKey, "updated_at": at}).Error
	})
	return item, err
}

// stopBotInteractions applies disable or delete semantics inside the caller's
// transaction, after it locked the configuration: queued and running items
// are cancelled; sending items become reply_unknown; ready items are kept on
// disable and cancelled on delete. The connection row is locked between the
// configuration and the interactions to keep the global lock order.
func (r *BotInteractionsRepo) stopBotInteractions(tx *gorm.DB, configID, leaseKey string, deleted bool) error {
	if leaseKey != "" {
		if _, err := r.lockConnection(tx, leaseKey); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	reason, sendingReason := "bot_disabled", "bot_disabled_during_send"
	cancel := []string{domain.BotQueued, domain.BotRunning}
	if deleted {
		reason, sendingReason = "bot_deleted", "bot_deleted_during_send"
		cancel = append(cancel, domain.BotReady)
	}
	now := time.Now().UTC()
	cancelled := map[string]any{"status": domain.BotCancelled, "terminal_reason": reason, "lease_owner": "", "send_lease_until_us": 0, "updated_at": now}
	if deleted {
		cancelled["reply_route"] = ""
	}
	if err := tx.Model(&domain.BotInteraction{}).Where("bot_id = ? AND status IN ?", configID, cancel).Updates(cancelled).Error; err != nil {
		return err
	}
	updates := map[string]any{"status": domain.BotReplyUnknown, "terminal_reason": sendingReason, "last_error": "发送过程中机器人被停用或删除，送达结果待核对", "lease_owner": "", "send_lease_until_us": 0, "updated_at": now}
	if deleted {
		updates["reply_route"] = ""
	}
	return tx.Model(&domain.BotInteraction{}).Where("bot_id = ? AND status = ?", configID, domain.BotSending).Updates(updates).Error
}

// ErrBotPushTargetUnverified: the original sender is not a verified
// proactive target of this configuration.
var ErrBotPushTargetUnverified = errors.New("push_target_unverified")

// BotSendRoutePush marks replies delivered as proactive private messages.
const BotSendRoutePush = "push"

// BotSendRoutePushChat marks messages sent into their conversation as a
// new proactive message (a group, or the sender's private chat) instead of
// a reply to an inbound message.
const BotSendRoutePushChat = "push_chat"

// PushResendBotInteraction switches a DingTalk reply whose session webhook
// expired to a proactive private message to the original sender. Under the
// configuration and interaction locks it re-checks that the reply failed
// for route_expired and that the sender's binding still belongs to
// expectedUser on this configuration with a successful trial send. The
// saved reply is sent again as a new generation with a new key; the action
// key makes a repeated click return the same result.
func (r *BotInteractionsRepo) PushResendBotInteraction(id string, actor, expectedUser int64, actionKey, reason string) (domain.BotInteraction, error) {
	var item domain.BotInteraction
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		cfg, cfgErr := r.lockBotConfig(tx, item.BotID)
		if err := r.locking(tx).First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		if actionKey != "" && item.LastActionKey == actionKey {
			return nil
		}
		if errors.Is(cfgErr, gorm.ErrRecordNotFound) {
			return ErrBotDeleted
		}
		if cfgErr != nil {
			return cfgErr
		}
		if !cfg.Enabled || !cfg.InteractionEnabled {
			return ErrBotStateConflict
		}
		if item.Status != domain.BotReplyFailed || item.TerminalReason != "route_expired" || item.BodyPurgedAt != nil {
			return ErrBotStateConflict
		}
		var binding domain.BotUserBinding
		err := r.locking(tx).Where("id = ? AND config_id = ? AND user_id = ? AND user_id > 0 AND target_verified_at IS NOT NULL", botIdentityID(item.BotID, item.SenderID), item.BotID, expectedUser).First(&binding).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBotPushTargetUnverified
		}
		if err != nil {
			return err
		}
		item.Status = domain.BotReady
		item.DeliveryGeneration++
		item.SendKey = randomHex(16)
		item.SendRoute = BotSendRoutePush
		result := tx.Model(&domain.BotInteraction{}).Where("id = ? AND status = ?", id, domain.BotReplyFailed).Updates(map[string]any{"status": domain.BotReady, "delivery_generation": item.DeliveryGeneration, "send_key": item.SendKey, "send_route": BotSendRoutePush, "reply_route": "", "route_expires_at_us": 0, "send_started_at_us": 0, "send_attempts": 0, "undelivered_retries": 0, "next_send_at_us": 0, "send_lease_until_us": 0, "lease_owner": "", "lease_epoch": 0, "terminal_reason": "", "resent_by": actor, "last_action_key": actionKey, "last_error": truncateBotDetail("人工改用主动单聊发送：" + reason), "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrBotStateConflict
		}
		return nil
	})
	return item, err
}

var botInteractionTerminal = []string{domain.BotReplied, domain.BotReplyFailed, domain.BotCancelled}

// PurgeBotInteractions applies the retention policy once to finished interactions.
func (r *BotInteractionsRepo) PurgeBotInteractions(ctx context.Context, now time.Time, policy BotRetentionPolicy) (BotRetentionResult, error) {
	run := newRetentionRun(ctx, policy)
	p, db := run.p, r.DB.WithContext(ctx)
	var out BotRetentionResult
	if p.Body > 0 {
		cut := now.Add(-p.Body)
		run.step(&out.InteractionBodies, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&domain.BotInteraction{}).Where("status IN ? AND updated_at < ? AND body_purged_at IS NULL", botInteractionTerminal, cut).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			// UpdateColumns keeps updated_at: the record horizon still
			// counts from completion.
			res := db.Model(&domain.BotInteraction{}).Where("id IN ? AND status IN ? AND updated_at < ? AND body_purged_at IS NULL", ids, botInteractionTerminal, cut).UpdateColumns(map[string]any{"content": "", "quote": "", "reply": "", "reply_route": "", "body_purged_at": now})
			return res.RowsAffected, res.Error
		})
	}
	if p.Record > 0 {
		cut := now.Add(-p.Record)
		run.step(&out.Interactions, func(limit int) ([]string, error) {
			var ids []string
			return ids, db.Model(&domain.BotInteraction{}).Where("status IN ? AND updated_at < ?", botInteractionTerminal, cut).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
		}, func(ids []string) (int64, error) {
			res := db.Where("id IN ? AND status IN ? AND updated_at < ?", ids, botInteractionTerminal, cut).Delete(&domain.BotInteraction{})
			return res.RowsAffected, res.Error
		})
	}
	return out, run.err
}
