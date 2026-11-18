package repo

import (
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Bot secrets are stored per configuration ID. New values are sealed with
// the credential keyring (credcrypt, "lf1:" prefix); legacy plaintext rows
// stay readable until the explicit -credential-action reencrypt migrates them.
type botCredentialRow struct {
	BotID  string `gorm:"primaryKey;size:191"`
	Secret string `gorm:"column:ciphertext;type:text;not null"`
}

func (botCredentialRow) TableName() string { return "user_bot_credentials" }

// ErrCredentialKeyMissing is returned when a secret must be sealed but no
// credential key is configured; HTTP maps it to 503 credential_key_missing.
var ErrCredentialKeyMissing = credcrypt.ErrKeyMissing

type botCredentialPayload struct {
	Secret string `json:"secret"`
}

// SetCredentialKeyring installs the keyring used to seal and open Bot
// credentials. A nil keyring keeps legacy plaintext readable but refuses new
// secrets.
func (r *UsersRepo) SetCredentialKeyring(k *credcrypt.Keyring) {
	r.keyringMu.Lock()
	defer r.keyringMu.Unlock()
	r.keyring = k
}

func (r *UsersRepo) credentialKeyring() *credcrypt.Keyring {
	r.keyringMu.RLock()
	defer r.keyringMu.RUnlock()
	return r.keyring
}

// SealBotRoute encrypts authorization-bearing reply routing (the DingTalk
// sessionWebhook) bound to one configuration and interaction, so a copied
// value cannot be opened for another interaction.
func (r *UsersRepo) SealBotRoute(configID, interactionID, route string) (string, error) {
	if r.credentialKeyring() == nil {
		return "", ErrCredentialKeyMissing
	}
	return r.credentialKeyring().Seal([]byte(route), botRouteAAD(configID, interactionID))
}

// OpenBotRoute reverses SealBotRoute. Values that are not sealed are routing
// data without authority (Feishu message IDs) and are returned unchanged.
func (r *UsersRepo) OpenBotRoute(configID, interactionID, stored string) (string, error) {
	if !credcrypt.IsSealed(stored) {
		return stored, nil
	}
	plain, err := r.credentialKeyring().Open(stored, botRouteAAD(configID, interactionID))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func botRouteAAD(configID, interactionID string) string {
	return "bot-route|" + configID + "|" + interactionID + "|session_webhook"
}

func botCredentialAAD(id, channel string) string {
	return "bot-credential|" + id + "|" + domain.NotificationBot{Channel: channel}.ChannelKind()
}

// botChannel reads the immutable channel of a configuration for credential
// binding. Rows not yet backfilled by migration 036 are legacy WeCom.
func (r *UsersRepo) botChannel(db *gorm.DB, id string) string {
	var channel string
	db.Table("notification_bots").Select("channel").Where("id = ?", id).Scan(&channel)
	return channel
}

func (r *UsersRepo) sealBotSecret(id, channel, secret string) (string, error) {
	plain, _ := json.Marshal(botCredentialPayload{Secret: secret})
	return r.credentialKeyring().Seal(plain, botCredentialAAD(id, channel))
}

// openBotSecret returns legacy plaintext as-is; a sealed value must open.
func (r *UsersRepo) openBotSecret(id, channel, stored string) (string, error) {
	if !credcrypt.IsSealed(stored) {
		return stored, nil
	}
	plain, err := r.credentialKeyring().Open(stored, botCredentialAAD(id, channel))
	if err != nil {
		return "", err
	}
	var payload botCredentialPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return "", credcrypt.ErrDecrypt
	}
	return payload.Secret, nil
}

func (r *UsersRepo) BotCredential(botID string) (string, error) {
	var row botCredentialRow
	if err := r.DB.Where("bot_id = ?", botID).First(&row).Error; err != nil {
		return "", err
	}
	return r.openBotSecret(botID, r.botChannel(r.DB, botID), row.Secret)
}

// botCredentialFormats reports plaintext/encrypted per configuration ID.
func (r *UsersRepo) botCredentialFormats(ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	var rows []botCredentialRow
	r.DB.Where("bot_id IN ?", ids).Find(&rows)
	for _, row := range rows {
		if credcrypt.IsSealed(row.Secret) {
			out[row.BotID] = "encrypted"
		} else {
			out[row.BotID] = "plaintext"
		}
	}
	return out
}

func (r *UsersRepo) putBotCredential(tx *gorm.DB, id, channel, secret string) error {
	sealed, err := r.sealBotSecret(id, channel, secret)
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&botCredentialRow{BotID: id, Secret: sealed}).Error
}
func (r *UsersRepo) deleteBotCredential(tx *gorm.DB, id string) error {
	return tx.Where("bot_id = ?", id).Delete(&botCredentialRow{}).Error
}

// CredentialReport summarizes stored Bot credentials for -credential-action.
type CredentialReport struct {
	CurrentKID string         `json:"currentKid"`
	Total      int            `json:"total"`
	Plaintext  int            `json:"plaintext"`
	ByKID      map[string]int `json:"byKid"`
	Rewrapped  int            `json:"rewrapped"`
	Skipped    int            `json:"skipped"`
	Failed     []string       `json:"failed,omitempty"`
}

// String prints counts and key ids only, never values.
func (c CredentialReport) String() string {
	parts := []string{fmt.Sprintf("current_kid=%s total=%d plaintext=%d", c.CurrentKID, c.Total, c.Plaintext)}
	kids := make([]string, 0, len(c.ByKID))
	for kid := range c.ByKID {
		kids = append(kids, kid)
	}
	sort.Strings(kids)
	for _, kid := range kids {
		parts = append(parts, fmt.Sprintf("kid_%s=%d", kid, c.ByKID[kid]))
	}
	parts = append(parts, fmt.Sprintf("rewrapped=%d skipped=%d failed=%d", c.Rewrapped, c.Skipped, len(c.Failed)))
	return strings.Join(parts, " ")
}

// BotCredentialStatus counts plaintext rows and sealed rows per key id.
func (r *UsersRepo) BotCredentialStatus() (CredentialReport, error) {
	report := CredentialReport{CurrentKID: r.credentialKeyring().CurrentKID(), ByKID: map[string]int{}}
	var rows []botCredentialRow
	if err := r.DB.Order("bot_id").Find(&rows).Error; err != nil {
		return report, err
	}
	for _, row := range rows {
		report.Total++
		if kid := credcrypt.KID(row.Secret); kid != "" {
			report.ByKID[kid]++
		} else {
			report.Plaintext++
		}
	}
	return report, nil
}

// ReencryptBotCredentials seals legacy plaintext and rewraps values under old
// key ids with the current key, one row per transaction. It is re-entrant:
// rows already under the current key are skipped, and a failed row is
// reported without stopping the rest.
func (r *UsersRepo) ReencryptBotCredentials() (CredentialReport, error) {
	keyring := r.credentialKeyring()
	report := CredentialReport{CurrentKID: keyring.CurrentKID(), ByKID: map[string]int{}}
	if keyring.CurrentKID() == "" {
		return report, ErrCredentialKeyMissing
	}
	var ids []string
	if err := r.DB.Model(&botCredentialRow{}).Order("bot_id").Pluck("bot_id", &ids).Error; err != nil {
		return report, err
	}
	for _, id := range ids {
		report.Total++
		rewrapped := false
		err := r.DB.Transaction(func(tx *gorm.DB) error {
			var row botCredentialRow
			if err := r.locking(tx).Where("bot_id = ?", id).First(&row).Error; err != nil {
				return err
			}
			if !keyring.NeedsRewrap(row.Secret) {
				return nil
			}
			channel := r.botChannel(tx, id)
			secret, err := r.openBotSecret(id, channel, row.Secret)
			if err != nil {
				return err
			}
			sealed, err := r.sealBotSecret(id, channel, secret)
			if err != nil {
				return err
			}
			rewrapped = true
			return tx.Model(&botCredentialRow{}).Where("bot_id = ?", id).Update("ciphertext", sealed).Error
		})
		switch {
		case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
			report.Failed = append(report.Failed, id+": "+err.Error())
		case rewrapped:
			report.Rewrapped++
		default:
			report.Skipped++
		}
	}
	final, err := r.BotCredentialStatus()
	if err == nil {
		report.Plaintext, report.ByKID = final.Plaintext, final.ByKID
	}
	return report, err
}
