package repo

import (
	"agentflow/internal/domain"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"strings"
	"time"
)

var ErrBotBindingChallenge = errors.New("绑定请求已失效，请回到聊天中重新发送消息获取链接")

// ErrBotBindingPending: a valid link was already sent; point to it instead of
// issuing another. ErrBotBindingRateLimited: too many links recently.
var (
	ErrBotBindingPending     = errors.New("binding_link_pending")
	ErrBotBindingRateLimited = errors.New("binding_link_rate_limited")
)

// Link issuance limits.
const (
	botBindingGuideWindow   = 10 * time.Minute
	botBindingGuideMax      = 3
	botBindingExplicitEvery = time.Minute
)

// MaskBotSender shows enough of a platform user ID to recognize it.
func MaskBotSender(sender string) string {
	r := []rune(sender)
	if len(r) <= 4 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-2:])
}

var ErrBotBindingCode = errors.New("确认码无效、已过期或不属于当前发送者，请重新确认")

func botIdentityID(bot, sender string) string { return BotMessageID(bot, sender) }
func botBindingNonce(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (r *UsersRepo) BotUserBinding(bot, sender string) (domain.BotUserBinding, error) {
	var out domain.BotUserBinding
	err := r.DB.Where("id = ? AND user_id > 0", botIdentityID(bot, sender)).First(&out).Error
	return out, err
}

// Serialize all challenge creation/claim/confirmation/revocation on the same
// stable binding row, including while the user has not yet completed binding.
func (r *UsersRepo) botBindingRow(tx *gorm.DB, id string) (domain.BotUserBinding, error) {
	var row domain.BotUserBinding
	err := r.locking(tx).First(&row, "id = ?", id).Error
	return row, err
}

// NewBotBindingChallenge issues a binding link for an unbound sender of bot.
// explicit is true when the sender asked for a new link. While a valid link
// exists, implicit requests get ErrBotBindingPending; explicit ones replace
// it at most once a minute. At most three links are issued per ten minutes.
// Counters are updated under the binding row lock.
func (r *UsersRepo) NewBotBindingChallenge(bot domain.NotificationBot, sender string, now time.Time, explicit bool) (string, error) {
	scope := bot.IdentityScope()
	if scope == "" || sender == "" {
		return "", ErrBotBindingChallenge
	}
	token, err := botBindingNonce(32)
	if err != nil {
		return "", err
	}
	id := botIdentityID(scope, sender)
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		row := domain.BotUserBinding{ID: id, BotID: scope, SenderID: sender, ConfigID: bot.ID, Channel: bot.ChannelKind(), CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		existing, err := r.botBindingRow(tx, id)
		if err != nil {
			return err
		}
		if existing.UserID > 0 {
			return errors.New("当前聊天账号已绑定，请在个人配置解除后重新绑定")
		}
		var active int64
		if err := tx.Model(&domain.BotBindingChallenge{}).Where("binding_id = ? AND status IN ? AND expires_at > ?", id, []string{"pending", "claimed"}, now).Count(&active).Error; err != nil {
			return err
		}
		if active > 0 && !explicit {
			return ErrBotBindingPending
		}
		if explicit && existing.LastLinkAt != nil && now.Sub(*existing.LastLinkAt) < botBindingExplicitEvery {
			return ErrBotBindingRateLimited
		}
		start, count := existing.GuideWindowStart, existing.GuideCount
		if start == nil || now.Sub(*start) >= botBindingGuideWindow {
			start, count = &now, 0
		}
		if count >= botBindingGuideMax {
			return ErrBotBindingRateLimited
		}
		if err := tx.Model(&domain.BotUserBinding{}).Where("id = ?", id).Updates(map[string]any{"guide_window_start": start, "guide_count": count + 1, "last_link_at": now, "config_id": bot.ID, "channel": bot.ChannelKind(), "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.BotBindingChallenge{}).Where("binding_id = ? AND status IN ?", id, []string{"pending", "claimed"}).Update("status", "expired").Error; err != nil {
			return err
		}
		return tx.Create(&domain.BotBindingChallenge{ID: hexSHA256(token), BindingID: id, ConfigID: bot.ID, Channel: bot.ChannelKind(), SenderMask: MaskBotSender(sender), Status: "pending", ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now}).Error
	})
	return token, err
}

// BotBindingChallengeInfo returns the owning configuration, channel and
// masked sender of a link for the confirmation page.
func (r *UsersRepo) BotBindingChallengeInfo(token string) (configID, channel, senderMask string, err error) {
	var out domain.BotBindingChallenge
	if err := r.DB.First(&out, "id = ?", hexSHA256(token)).Error; err != nil {
		return "", "", "", ErrBotBindingChallenge
	}
	return out.ConfigID, out.Channel, out.SenderMask, nil
}

func (r *UsersRepo) BotBindingChallengeStatus(token string, userID int64, now time.Time) (string, error) {
	var out domain.BotBindingChallenge
	if err := r.DB.First(&out, "id = ?", hexSHA256(token)).Error; err != nil {
		return "", ErrBotBindingChallenge
	}
	if !out.ExpiresAt.After(now) || out.Status == "expired" {
		return "", ErrBotBindingChallenge
	}
	if out.UserID != 0 && out.UserID != userID {
		return "", errors.New("链接已由其他平台账号确认，请回企业微信重新获取绑定链接")
	}
	return out.Status, nil
}
func (r *UsersRepo) ClaimBotBindingChallenge(token string, userID int64, now time.Time) (string, error) {
	if userID <= 0 {
		return "", ErrBotBindingChallenge
	}
	// Derive a stable 80-bit code from the browser-only random nonce and account.
	// Only its hash is stored; reopening a request does not rotate the code.
	code := hexSHA256(token + "\x00" + strconv.FormatInt(userID, 10))[:20]
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var challenge domain.BotBindingChallenge
		if err := tx.First(&challenge, "id = ?", hexSHA256(token)).Error; err != nil {
			return ErrBotBindingChallenge
		}
		binding, err := r.botBindingRow(tx, challenge.BindingID)
		if err != nil {
			return err
		}
		if err := r.locking(tx).First(&challenge, "id = ?", challenge.ID).Error; err != nil {
			return err
		}
		if binding.UserID > 0 || !challenge.ExpiresAt.After(now) || challenge.Status == "expired" || challenge.Status == "confirmed" {
			return ErrBotBindingChallenge
		}
		if challenge.UserID != 0 && challenge.UserID != userID {
			return errors.New("链接已由其他平台账号确认，请回到聊天中重新获取绑定链接")
		}
		if challenge.Status == "claimed" {
			return nil
		}
		return tx.Model(&challenge).Updates(map[string]any{"user_id": userID, "code_hash": hexSHA256(code), "status": "claimed", "attempts": 0}).Error
	})
	return strings.ToUpper(code), err
}

// ConfirmBotUserBinding completes a binding from the trusted event of the
// original sender. The claimed challenge must belong to this configuration
// (legacy WeCom challenges without a configuration are accepted by scope).
func (r *UsersRepo) ConfirmBotUserBinding(bot domain.NotificationBot, sender, code string, now time.Time) (domain.BotUserBinding, error) {
	var binding domain.BotUserBinding
	denied := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		binding, err = r.botBindingRow(tx, botIdentityID(bot.IdentityScope(), sender))
		if err != nil {
			return ErrBotBindingCode
		}
		if binding.UserID > 0 {
			return errors.New("当前账号已经绑定，请在个人配置查看")
		}
		var challenge domain.BotBindingChallenge
		if err = tx.Where("binding_id = ? AND status = ?", binding.ID, "claimed").Order("created_at DESC").First(&challenge).Error; err != nil {
			return ErrBotBindingCode
		}
		if !challenge.ExpiresAt.After(now) || challenge.Attempts >= 5 || (challenge.ConfigID != "" && challenge.ConfigID != bot.ID) {
			return ErrBotBindingCode
		}
		if subtle.ConstantTimeCompare([]byte(challenge.CodeHash), []byte(hexSHA256(strings.ToLower(strings.TrimSpace(code))))) != 1 {
			denied = true
			updates := map[string]any{"attempts": challenge.Attempts + 1}
			if challenge.Attempts+1 >= 5 {
				updates["status"] = "expired"
			}
			return tx.Model(&challenge).Updates(updates).Error
		}
		if err = tx.Model(&binding).Updates(map[string]any{"user_id": challenge.UserID, "config_id": bot.ID, "channel": bot.ChannelKind(), "guide_count": 0, "target_verified_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		binding.UserID, binding.ConfigID, binding.Channel = challenge.UserID, bot.ID, bot.ChannelKind()
		binding.UpdatedAt = now
		return tx.Model(&domain.BotBindingChallenge{}).Where("binding_id = ? AND status IN ?", binding.ID, []string{"pending", "claimed"}).Updates(map[string]any{"status": "confirmed", "code_hash": ""}).Error
	})
	if denied {
		return binding, ErrBotBindingCode
	}
	return binding, err
}
func (r *UsersRepo) BotUserBindings(userID int64) ([]domain.BotUserBinding, error) {
	out := []domain.BotUserBinding{}
	err := r.DB.Where("user_id = ?", userID).Order("updated_at DESC").Find(&out).Error
	return out, err
}
func (r *UsersRepo) RevokeBotUserBinding(id string, userID int64, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.botBindingRow(tx, id)
		if err != nil {
			return err
		}
		if row.UserID != userID || userID <= 0 {
			return gorm.ErrRecordNotFound
		}
		if err = tx.Model(&row).Updates(map[string]any{"user_id": 0, "target_verified_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err = cancelBotDraftsWhere(tx, "账号绑定已解除", "identity_scope = ? AND sender_id = ?", row.BotID, row.SenderID); err != nil {
			return err
		}
		return tx.Model(&domain.BotBindingChallenge{}).Where("binding_id = ?", id).Updates(map[string]any{"status": "expired", "code_hash": ""}).Error
	})
}

// DeleteExpiredBotBindingChallenges removes a bounded batch after one day of
// retention. Binding rows are permanent and never removed by this cleanup.
// Expiry is immutable, so concurrent cleanup instances can safely select the
// same IDs: DELETE is idempotent and active requests cannot enter this batch.
func (r *UsersRepo) DeleteExpiredBotBindingChallenges(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-24 * time.Hour)
	db := r.DB.WithContext(ctx)
	var ids []string
	if err := db.Model(&domain.BotBindingChallenge{}).Where("expires_at <= ?", cutoff).Order("expires_at ASC").Limit(1000).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := db.Where("id IN ? AND expires_at <= ?", ids, cutoff).Delete(&domain.BotBindingChallenge{})
	return result.RowsAffected, result.Error
}

// revokeBotConfigIdentities runs inside DeleteNotificationBot after the
// configuration row is locked. New channels scope identities by the
// configuration ID, so all of them go. A legacy WeCom scope (the external Bot
// ID) is revoked only when no other configuration keeps using it.
func (r *UsersRepo) revokeBotConfigIdentities(tx *gorm.DB, bot domain.NotificationBot) error {
	scope := bot.IdentityScope()
	if err := cancelBotDraftsWhere(tx, "机器人已删除", "config_id = ?", bot.ID); err != nil {
		return err
	}
	if bot.ChannelKind() == domain.BotChannelWeCom {
		var others int64
		if err := tx.Table("notification_bots").Where("id <> ? AND external_app_id = ? AND (channel = ? OR channel = '')", bot.ID, bot.BotID, domain.BotChannelWeCom).Count(&others).Error; err != nil {
			return err
		}
		if others > 0 {
			return tx.Model(&domain.BotBindingChallenge{}).Where("config_id = ? AND status IN ?", bot.ID, []string{"pending", "claimed"}).Updates(map[string]any{"status": "expired", "code_hash": ""}).Error
		}
	}
	var ids []string
	if err := tx.Model(&domain.BotUserBinding{}).Where("bot_id = ?", scope).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Model(&domain.BotUserBinding{}).Where("id IN ?", ids).Updates(map[string]any{"user_id": 0, "target_verified_at": nil, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	return tx.Model(&domain.BotBindingChallenge{}).Where("binding_id IN ?", ids).Updates(map[string]any{"status": "expired", "code_hash": ""}).Error
}
