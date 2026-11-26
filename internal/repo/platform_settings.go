package repo

import (
	"encoding/json"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm/clause"
)

const platformSettingsID = "default"

// PlatformSettingsRepo owns the platform-wide settings a platform
// administrator maintains on the management page. They are stored as one row
// and read on use, so a change applies without a restart.
type PlatformSettingsRepo struct {
	*deps
}

// PlatformSettings returns the stored settings, or the zero value when none
// were ever saved. A read failure is also the zero value: callers fall back to
// their environment configuration, which must not be blocked by this table.
func (r *PlatformSettingsRepo) PlatformSettings() domain.PlatformSettings {
	var row platformSettingsRow
	if err := r.DB.Where("id = ?", platformSettingsID).Take(&row).Error; err != nil {
		return domain.PlatformSettings{}
	}
	var out domain.PlatformSettings
	if json.Unmarshal(row.Payload, &out) != nil {
		return domain.PlatformSettings{}
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}

// PutPlatformSettings replaces the stored settings and stamps who changed
// them. An empty PublicURL clears the setting, which makes the platform fall
// back to AGENTFLOW_PUBLIC_URL.
func (r *PlatformSettingsRepo) PutPlatformSettings(settings domain.PlatformSettings) error {
	now := time.Now().UTC()
	settings.PublicURL = strings.TrimSpace(settings.PublicURL)
	settings.UpdatedBy = strings.TrimSpace(settings.UpdatedBy)
	settings.UpdatedAt = now
	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	row := platformSettingsRow{ID: platformSettingsID, Payload: payload, Updated: &now}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"payload", "updated_at"}),
	}).Create(&row).Error
}
