package repo

import (
	"encoding/json"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm/clause"
)

const knowledgeSettingsID = "default"

// KnowledgeSettings returns the stored settings including the upstream token.
// Callers that render API responses must use Public().
func (r *KnowledgeRepo) KnowledgeSettings() domain.KnowledgeSettings {
	var row knowledgeSettingsRow
	if err := r.DB.Where("id = ?", knowledgeSettingsID).Take(&row).Error; err != nil {
		return domain.KnowledgeSettings{}
	}
	var out domain.KnowledgeSettings
	_ = json.Unmarshal(row.Payload, &out)
	out.Token = ""
	if row.Token != nil {
		out.Token = *row.Token
	}
	out.TokenConfigured = strings.TrimSpace(out.Token) != ""
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}

func (r *KnowledgeRepo) PutKnowledgeSettings(settings domain.KnowledgeSettings) error {
	now := time.Now().UTC()
	token := strings.TrimSpace(settings.Token)
	settings.Token = ""
	settings.TokenConfigured = false
	settings.UpdatedAt = now
	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	row := knowledgeSettingsRow{ID: knowledgeSettingsID, Payload: payload, Updated: &now}
	if token != "" {
		row.Token = &token
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"payload", "token", "updated_at"}),
	}).Create(&row).Error
}
