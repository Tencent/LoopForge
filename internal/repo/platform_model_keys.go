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

// Shared model credentials. A platform administrator configures one API Key
// per assistant engine under 平台管理 → 共享模型 Key; members who have not
// configured their own key for that engine chat and run LOOPs on the shared
// one, limited to the models the administrator allowed. It covers chat
// conversations and LOOP runs, so a new user can start working before
// configuring anything; scheduled prompts, reports, bots and run evaluation
// keep requiring the user's own key.
//
// Older deployments lent the CodeBuddy key of the account named by
// AGENTFLOW_ASSISTANT_SPONSOR_LOGIN instead, limited to
// SponsoredCodeBuddyModel. That still applies while no shared CodeBuddy key
// is configured.
const (
	CodeBuddySponsorLoginEnv = "AGENTFLOW_ASSISTANT_SPONSOR_LOGIN"
	SponsoredCodeBuddyModel  = "deepseek-v4.1-flash"
	// MaxSharedModels bounds the allow list of one shared credential.
	MaxSharedModels = 32
	// MaxSharedCodeBuddyModels is the older name of MaxSharedModels.
	MaxSharedCodeBuddyModels = MaxSharedModels
)

// SharedModelKey is the credential users without their own borrow for one
// engine.
type SharedModelKey struct {
	Provider string
	APIKey   string
	// BaseURL is the gateway the key belongs to; empty means the official
	// endpoint. CodeBuddy addresses its editions through
	// InternetEnvironment instead.
	BaseURL             string
	InternetEnvironment string
	// Models are the models it may run; the first is the default.
	Models []string
	// SponsorID is the lending account under AGENTFLOW_ASSISTANT_SPONSOR_LOGIN,
	// 0 for a key configured under 平台管理.
	SponsorID int64
}

// Model returns the model a shared-key turn runs: the requested model when it
// is allowed, the default when none is requested, and false otherwise.
func (k SharedModelKey) Model(requested string) (string, bool) {
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

// The key is stored like personal keys: the database is the trust boundary,
// and the key is never returned by the API. One row per engine.
type platformModelKeyRow struct {
	Provider            string     `gorm:"column:provider;primaryKey;size:32"`
	APIKey              string     `gorm:"column:api_key;type:text;not null"`
	BaseURL             string     `gorm:"column:base_url;type:text;not null"`
	InternetEnvironment string     `gorm:"column:internet_environment;size:64;not null;default:''"`
	Models              string     `gorm:"column:models;type:text;not null"`
	UpdatedBy           int64      `gorm:"column:updated_by;not null;default:0"`
	Updated             *time.Time `gorm:"column:updated_at"`
}

func (platformModelKeyRow) TableName() string { return "platform_model_keys" }

// platformCodeBuddyKeyRow is the CodeBuddy-only table older releases wrote.
// It stays readable so an upgraded deployment keeps lending a key configured
// before the per-engine table existed; migrations/050 copies it.
type platformCodeBuddyKeyRow struct {
	ID                  int64      `gorm:"column:id;primaryKey;autoIncrement:false"`
	APIKey              string     `gorm:"column:api_key;type:text;not null"`
	InternetEnvironment string     `gorm:"column:internet_environment;size:64;not null;default:''"`
	Models              string     `gorm:"column:models;type:text;not null"`
	UpdatedBy           int64      `gorm:"column:updated_by;not null;default:0"`
	Updated             *time.Time `gorm:"column:updated_at"`
}

func (platformCodeBuddyKeyRow) TableName() string { return "platform_codebuddy_keys" }

// The legacy table holds at most this one row.
const platformCodeBuddyKeyID = 1

// PlatformModelKeyStatus is what administrators see; it never carries the key.
type PlatformModelKeyStatus struct {
	Provider            string     `json:"provider"`
	Configured          bool       `json:"configured"`
	BaseURL             string     `json:"baseUrl,omitempty"`
	InternetEnvironment string     `json:"internetEnvironment"`
	Models              []string   `json:"models"`
	UpdatedAt           *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy           string     `json:"updatedBy,omitempty"`
	// LegacySponsor is the account named by AGENTFLOW_ASSISTANT_SPONSOR_LOGIN
	// whose key is lent while no shared CodeBuddy key is configured here.
	LegacySponsor      string `json:"legacySponsor,omitempty"`
	LegacySponsorModel string `json:"legacySponsorModel,omitempty"`
}

// PlatformCodeBuddyKeyStatus is the CodeBuddy view of PlatformModelKeyStatus.
type PlatformCodeBuddyKeyStatus = PlatformModelKeyStatus

// PlatformModelKeyUpdate replaces one engine's shared key. An empty APIKey
// keeps the stored key. Models must already be validated.
type PlatformModelKeyUpdate struct {
	Provider            string
	APIKey              string
	BaseURL             string
	InternetEnvironment string
	Models              []string
}

// PlatformCodeBuddyKeyUpdate is the CodeBuddy-only form of
// PlatformModelKeyUpdate; Provider may be left empty.
type PlatformCodeBuddyKeyUpdate = PlatformModelKeyUpdate

var (
	ErrSharedModelKeyRequired = errors.New("shared model API Key is required")
	ErrSharedModelsRequired   = errors.New("at least one shared model is required")
	ErrSharedModelKeyProvider = errors.New("unsupported shared model provider")
	// ErrSharedModelKeyBaseURLChanged rejects moving a stored key to another
	// gateway without typing it again: the key is write-only, so whoever can
	// edit the Base URL must not be able to redirect it somewhere they read.
	ErrSharedModelKeyBaseURLChanged = errors.New("shared model API Key must be re-entered when the Base URL changes")
	// ErrSharedCodeBuddyKeyRequired is the older name of
	// ErrSharedModelKeyRequired.
	ErrSharedCodeBuddyKeyRequired    = ErrSharedModelKeyRequired
	ErrSharedCodeBuddyModelsRequired = ErrSharedModelsRequired
)

func (r *UsersRepo) platformModelKey(provider string) (platformModelKeyRow, bool) {
	var row platformModelKeyRow
	if r == nil || r.deps == nil || r.DB == nil || provider == "" {
		return row, false
	}
	result := r.DB.Where("provider = ?", provider).Limit(1).Find(&row)
	return row, result.Error == nil && result.RowsAffected > 0
}

func (r *UsersRepo) legacyPlatformCodeBuddyKey() (platformCodeBuddyKeyRow, bool) {
	var row platformCodeBuddyKeyRow
	if r == nil || r.deps == nil || r.DB == nil {
		return row, false
	}
	result := r.DB.Where("id = ?", platformCodeBuddyKeyID).Limit(1).Find(&row)
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
func (r *UsersRepo) legacyCodeBuddySponsor() (domain.User, bool) {
	login := strings.TrimSpace(os.Getenv(CodeBuddySponsorLoginEnv))
	if login == "" || r == nil {
		return domain.User{}, false
	}
	sponsor, ok := r.UserByLoginName(login)
	if !ok || strings.TrimSpace(sponsor.CodeBuddyAPIKey) == "" {
		return domain.User{}, false
	}
	return sponsor, true
}

// SharedModelKey returns the credential users without their own borrow for
// one engine: the key configured under 平台管理, else, for CodeBuddy, the
// legacy table and the legacy sponsor account.
func (r *UsersRepo) SharedModelKey(provider string) (SharedModelKey, bool) {
	provider, ok := domain.NormalizeSharedModelProvider(provider)
	if !ok {
		return SharedModelKey{}, false
	}
	if row, found := r.platformModelKey(provider); found {
		models := decodeSharedModels(row.Models)
		if strings.TrimSpace(row.APIKey) != "" && len(models) > 0 {
			return SharedModelKey{
				Provider: provider, APIKey: row.APIKey, BaseURL: row.BaseURL,
				InternetEnvironment: row.InternetEnvironment, Models: models,
			}, true
		}
	}
	if provider != domain.SharedModelProviderCodeBuddy {
		return SharedModelKey{}, false
	}
	if row, found := r.legacyPlatformCodeBuddyKey(); found {
		models := decodeSharedModels(row.Models)
		if strings.TrimSpace(row.APIKey) != "" && len(models) > 0 {
			return SharedModelKey{
				Provider: provider, APIKey: row.APIKey,
				InternetEnvironment: row.InternetEnvironment, Models: models,
			}, true
		}
	}
	sponsor, found := r.legacyCodeBuddySponsor()
	if !found {
		return SharedModelKey{}, false
	}
	return SharedModelKey{
		Provider: provider, APIKey: sponsor.CodeBuddyAPIKey,
		InternetEnvironment: sponsor.CodeBuddyInternetEnvironment,
		Models:              []string{SponsoredCodeBuddyModel}, SponsorID: sponsor.ID,
	}, true
}

// SharedModelKeys returns every shared credential, keyed by engine.
func (r *UsersRepo) SharedModelKeys() map[string]SharedModelKey {
	keys := map[string]SharedModelKey{}
	for _, provider := range domain.SharedModelProviders {
		if key, ok := r.SharedModelKey(provider); ok {
			keys[provider] = key
		}
	}
	return keys
}

// SharedCodeBuddyKey returns the CodeBuddy credential users without their own
// borrow.
func (r *UsersRepo) SharedCodeBuddyKey() (SharedModelKey, bool) {
	return r.SharedModelKey(domain.SharedModelProviderCodeBuddy)
}

// SharedCodeBuddyModels returns the models users without their own CodeBuddy
// key may use, the default first, or nil when there is no shared key.
func (r *UsersRepo) SharedCodeBuddyModels() []string {
	if key, ok := r.SharedCodeBuddyKey(); ok {
		return key.Models
	}
	return nil
}

// PlatformModelKeyStatus describes one engine's shared key for
// administrators.
func (r *UsersRepo) PlatformModelKeyStatus(provider string) PlatformModelKeyStatus {
	provider, _ = domain.NormalizeSharedModelProvider(provider)
	status := PlatformModelKeyStatus{Provider: provider, Models: []string{}}
	if row, ok := r.platformModelKey(provider); ok && strings.TrimSpace(row.APIKey) != "" {
		status.Configured = true
		status.BaseURL = row.BaseURL
		status.InternetEnvironment = row.InternetEnvironment
		if models := decodeSharedModels(row.Models); models != nil {
			status.Models = models
		}
		status.UpdatedAt = row.Updated
		if user, ok := r.User(row.UpdatedBy); ok {
			status.UpdatedBy = user.LoginName
		}
	} else if provider == domain.SharedModelProviderCodeBuddy {
		if row, ok := r.legacyPlatformCodeBuddyKey(); ok && strings.TrimSpace(row.APIKey) != "" {
			status.Configured = true
			status.InternetEnvironment = row.InternetEnvironment
			if models := decodeSharedModels(row.Models); models != nil {
				status.Models = models
			}
			status.UpdatedAt = row.Updated
			if user, ok := r.User(row.UpdatedBy); ok {
				status.UpdatedBy = user.LoginName
			}
		}
	}
	if provider != domain.SharedModelProviderCodeBuddy {
		return status
	}
	if sponsor, ok := r.legacyCodeBuddySponsor(); ok {
		status.LegacySponsor = sponsor.LoginName
		status.LegacySponsorModel = SponsoredCodeBuddyModel
	}
	return status
}

// PlatformModelKeyStatuses describes every engine's shared key, in
// domain.SharedModelProviders order.
func (r *UsersRepo) PlatformModelKeyStatuses() []PlatformModelKeyStatus {
	statuses := make([]PlatformModelKeyStatus, 0, len(domain.SharedModelProviders))
	for _, provider := range domain.SharedModelProviders {
		statuses = append(statuses, r.PlatformModelKeyStatus(provider))
	}
	return statuses
}

// SetPlatformModelKey saves one engine's shared key settings.
func (r *UsersRepo) SetPlatformModelKey(update PlatformModelKeyUpdate, actorID int64) error {
	provider, ok := domain.NormalizeSharedModelProvider(update.Provider)
	if !ok {
		return ErrSharedModelKeyProvider
	}
	if len(update.Models) == 0 {
		return ErrSharedModelsRequired
	}
	row, _ := r.platformModelKey(provider)
	if key := strings.TrimSpace(update.APIKey); key != "" {
		row.APIKey = key
	} else if strings.TrimSpace(row.APIKey) != "" && !SameSharedModelBaseURL(row.BaseURL, update.BaseURL) {
		return ErrSharedModelKeyBaseURLChanged
	}
	if strings.TrimSpace(row.APIKey) == "" && provider == domain.SharedModelProviderCodeBuddy {
		// Editing only the models of a key saved before the per-engine table
		// existed must not require typing it again.
		if legacy, ok := r.legacyPlatformCodeBuddyKey(); ok {
			row.APIKey = legacy.APIKey
		}
	}
	if strings.TrimSpace(row.APIKey) == "" {
		return ErrSharedModelKeyRequired
	}
	models, err := json.Marshal(update.Models)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	row.Provider = provider
	row.BaseURL = strings.TrimSpace(update.BaseURL)
	row.InternetEnvironment = update.InternetEnvironment
	row.Models = string(models)
	row.UpdatedBy = actorID
	row.Updated = &now
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider"}},
		DoUpdates: clause.AssignmentColumns([]string{"api_key", "base_url", "internet_environment", "models", "updated_by", "updated_at"}),
	}).Create(&row).Error
}

// SameSharedModelBaseURL reports whether two Base URLs name the same gateway,
// ignoring surrounding space and a trailing slash.
func SameSharedModelBaseURL(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// SetPlatformCodeBuddyKey saves the shared CodeBuddy key settings.
func (r *UsersRepo) SetPlatformCodeBuddyKey(update PlatformCodeBuddyKeyUpdate, actorID int64) error {
	update.Provider = domain.SharedModelProviderCodeBuddy
	return r.SetPlatformModelKey(update, actorID)
}

// ClearPlatformModelKey removes one engine's shared key; users without their
// own key for it need one again (CodeBuddy falls back to the legacy sponsor
// account).
func (r *UsersRepo) ClearPlatformModelKey(provider string) error {
	provider, ok := domain.NormalizeSharedModelProvider(provider)
	if !ok {
		return ErrSharedModelKeyProvider
	}
	if err := r.DB.Where("provider = ?", provider).Delete(&platformModelKeyRow{}).Error; err != nil {
		return err
	}
	if provider == domain.SharedModelProviderCodeBuddy {
		// Clearing means "stop lending this key": drop the legacy row too so
		// the older key does not resurface.
		return r.DB.Where("id = ?", platformCodeBuddyKeyID).Delete(&platformCodeBuddyKeyRow{}).Error
	}
	return nil
}

// ClearPlatformCodeBuddyKey removes the shared CodeBuddy key.
func (r *UsersRepo) ClearPlatformCodeBuddyKey() error {
	return r.ClearPlatformModelKey(domain.SharedModelProviderCodeBuddy)
}
