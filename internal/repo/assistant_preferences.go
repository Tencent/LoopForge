package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// AssistantPreferences returns the user's Main Bot settings; the zero value
// when they never saved any.
func (r *AssistantRepo) AssistantPreferences(userID int64) domain.AssistantPreferences {
	var out domain.AssistantPreferences
	if userID <= 0 {
		return out
	}
	var row assistantPreferenceRow
	if err := r.DB.Where("user_id = ?", userID).Take(&row).Error; err != nil {
		return out
	}
	out.ProactiveRoles, out.ProactiveSOPs, out.Instructions = row.ProactiveRoles, row.ProactiveSOPs, row.Instructions
	if row.UpdatedAt != nil {
		out.UpdatedAt = *row.UpdatedAt
	}
	return out
}

// PutAssistantPreferences replaces the user's Main Bot settings.
func (r *AssistantRepo) PutAssistantPreferences(userID int64, p domain.AssistantPreferences) (domain.AssistantPreferences, error) {
	if userID <= 0 {
		return p, errors.New("user id is required")
	}
	now := time.Now().UTC()
	row := assistantPreferenceRow{UserID: userID, ProactiveRoles: p.ProactiveRoles, ProactiveSOPs: p.ProactiveSOPs, Instructions: strings.TrimSpace(p.Instructions), UpdatedAt: &now}
	if err := r.DB.Save(&row).Error; err != nil {
		return p, err
	}
	return r.AssistantPreferences(userID), nil
}
