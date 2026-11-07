package repo

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm/clause"
)

// Users who have not configured their own CodeBuddy API Key use the shared
// key a platform administrator configured under 平台管理, limited to the
// models the administrator allowed. It covers chat conversations and SOP
// runs, so a new user can start working before configuring anything;
// scheduled prompts, reports, bots and run evaluation keep requiring the
// user's own key.
//
// Older deployments lent the key of the account named by
// AGENTFLOW_ASSISTANT_SPONSOR_LOGIN instead, limited to
// SponsoredCodeBuddyModel. That still applies while no shared key is
// configured.
const (
	CodeBuddySponsorLoginEnv = "AGENTFLOW_ASSISTANT_SPONSOR_LOGIN"
	SponsoredCodeBuddyModel  = "deepseek-v4.1-flash"
	// MaxSharedCodeBuddyModels bounds the allow list of the shared key.
	MaxSharedCodeBuddyModels = 32
)

// SharedCodeBuddyKey is the key users without their own borrow.
type SharedCodeBuddyKey struct {
	APIKey              string
	InternetEnvironment string
	// Models are the models it may run; the first is the default.
	Models []string
	// SponsorID is the lending account under AGENTFLOW_ASSISTANT_SPONSOR_LOGIN,
	// 0 for the key configured under 平台管理.
	SponsorID int64
}

// Model returns the model a shared-key turn runs: the requested model when it
// is allowed, the default when none is requested, and false otherwise.
func (k SharedCodeBuddyKey) Model(requested string) (string, bool) {
	if len(k.Models) == 0 {
		return "", false
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return k.Models[0], true
	}
	for _, id := range k.Models {
		if id == requested {
			return id, true
		}
	}
	return "", false
}

// The key is stored like personal CodeBuddy keys in user_configs: the
// database is the trust boundary, and the key is never returned by the API.
type platformCodeBuddyKeyRow struct {
	ID                  int64      `gorm:"column:id;primaryKey;autoIncrement:false"`
	APIKey              string     `gorm:"column:api_key;type:text;not null"`
	InternetEnvironment string     `gorm:"column:internet_environment;size:64;not null;default:''"`
	Models              string     `gorm:"column:models;type:text;not null"`
	UpdatedBy           int64      `gorm:"column:updated_by;not null;default:0"`
	Updated             *time.Time `gorm:"column:updated_at"`
}

func (platformCodeBuddyKeyRow) TableName() string { return "platform_codebuddy_keys" }

// The table holds at most this one row.
const platformCodeBuddyKeyID = 1

// PlatformCodeBuddyKeyStatus is what administrators see; it never carries the key.
type PlatformCodeBuddyKeyStatus struct {
	Configured          bool       `json:"configured"`
	InternetEnvironment string     `json:"internetEnvironment"`
	Models              []string   `json:"models"`
	UpdatedAt           *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy           string     `json:"updatedBy,omitempty"`
	// LegacySponsor is the account named by AGENTFLOW_ASSISTANT_SPONSOR_LOGIN
	// whose key is lent while no shared key is configured here.
	LegacySponsor      string `json:"legacySponsor,omitempty"`
	LegacySponsorModel string `json:"legacySponsorModel,omitempty"`
}

// PlatformCodeBuddyKeyUpdate replaces the shared key settings. An empty
// APIKey keeps the stored key. Models must already be validated.
type PlatformCodeBuddyKeyUpdate struct {
	APIKey              string
	InternetEnvironment string
	Models              []string
}

var (
	ErrSharedCodeBuddyKeyRequired    = errors.New("shared CodeBuddy API Key is required")
	ErrSharedCodeBuddyModelsRequired = errors.New("at least one shared CodeBuddy model is required")
)

func (s *Store) platformCodeBuddyKey() (platformCodeBuddyKeyRow, bool) {
	var row platformCodeBuddyKeyRow
	if s == nil || s.DB == nil {
		return row, false
	}
	result := s.DB.Where("id = ?", platformCodeBuddyKeyID).Limit(1).Find(&row)
	return row, result.Error == nil && result.RowsAffected > 0
}

func decodeSharedModels(raw string) []string {
	var models []string
	if json.Unmarshal([]byte(raw), &models) != nil {
		return nil
	}
	return models
}

// legacyCodeBuddySponsor returns the account named by
// AGENTFLOW_ASSISTANT_SPONSOR_LOGIN, if it exists and has a key.
func (s *Store) legacyCodeBuddySponsor() (domain.User, bool) {
	login := strings.TrimSpace(os.Getenv(CodeBuddySponsorLoginEnv))
	if login == "" || s == nil {
		return domain.User{}, false
	}
	sponsor, ok := s.UserByLoginName(login)
	if !ok || strings.TrimSpace(sponsor.CodeBuddyAPIKey) == "" {
		return domain.User{}, false
	}
	return sponsor, true
}

// SharedCodeBuddyKey returns the key users without their own borrow: the one
// configured under 平台管理, else the legacy sponsor account's.
func (s *Store) SharedCodeBuddyKey() (SharedCodeBuddyKey, bool) {
	if row, ok := s.platformCodeBuddyKey(); ok {
		models := decodeSharedModels(row.Models)
		if strings.TrimSpace(row.APIKey) != "" && len(models) > 0 {
			return SharedCodeBuddyKey{APIKey: row.APIKey, InternetEnvironment: row.InternetEnvironment, Models: models}, true
		}
	}
	sponsor, ok := s.legacyCodeBuddySponsor()
	if !ok {
		return SharedCodeBuddyKey{}, false
	}
	return SharedCodeBuddyKey{APIKey: sponsor.CodeBuddyAPIKey, InternetEnvironment: sponsor.CodeBuddyInternetEnvironment, Models: []string{SponsoredCodeBuddyModel}, SponsorID: sponsor.ID}, true
}

// SharedCodeBuddyModels returns the models users without their own key may
// use, the default first, or nil when there is no shared key.
func (s *Store) SharedCodeBuddyModels() []string {
	if key, ok := s.SharedCodeBuddyKey(); ok {
		return key.Models
	}
	return nil
}

// PlatformCodeBuddyKeyStatus describes the shared key for administrators.
func (s *Store) PlatformCodeBuddyKeyStatus() PlatformCodeBuddyKeyStatus {
	status := PlatformCodeBuddyKeyStatus{Models: []string{}}
	if row, ok := s.platformCodeBuddyKey(); ok && strings.TrimSpace(row.APIKey) != "" {
		status.Configured = true
		status.InternetEnvironment = row.InternetEnvironment
		if models := decodeSharedModels(row.Models); models != nil {
			status.Models = models
		}
		status.UpdatedAt = row.Updated
		if user, ok := s.User(row.UpdatedBy); ok {
			status.UpdatedBy = user.LoginName
		}
	}
	if sponsor, ok := s.legacyCodeBuddySponsor(); ok {
		status.LegacySponsor = sponsor.LoginName
		status.LegacySponsorModel = SponsoredCodeBuddyModel
	}
	return status
}

// SetPlatformCodeBuddyKey saves the shared key settings.
func (s *Store) SetPlatformCodeBuddyKey(update PlatformCodeBuddyKeyUpdate, actorID int64) error {
	if len(update.Models) == 0 {
		return ErrSharedCodeBuddyModelsRequired
	}
	row, _ := s.platformCodeBuddyKey()
	if key := strings.TrimSpace(update.APIKey); key != "" {
		row.APIKey = key
	}
	if strings.TrimSpace(row.APIKey) == "" {
		return ErrSharedCodeBuddyKeyRequired
	}
	models, err := json.Marshal(update.Models)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	row.ID = platformCodeBuddyKeyID
	row.InternetEnvironment = update.InternetEnvironment
	row.Models = string(models)
	row.UpdatedBy = actorID
	row.Updated = &now
	return s.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"api_key", "internet_environment", "models", "updated_by", "updated_at"}),
	}).Create(&row).Error
}

// ClearPlatformCodeBuddyKey removes the shared key; users without their own
// key need one again (or fall back to the legacy sponsor account).
func (s *Store) ClearPlatformCodeBuddyKey() error {
	return s.DB.Where("id = ?", platformCodeBuddyKeyID).Delete(&platformCodeBuddyKeyRow{}).Error
}
