package repo

import (
	"encoding/json"
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
	out.ProactiveRoles = row.ProactiveRoles
	out.ProactiveSOPs = row.ProactiveSOPs
	out.SteerOnSend = row.SteerOnSend
	out.Instructions = row.Instructions
	out.ApprovalReviewMode = strings.TrimSpace(row.ApprovalReviewMode)
	out.ApprovalReviewModel = strings.TrimSpace(row.ApprovalReviewModel)
	if row.ApprovalReviewAPI != nil && strings.TrimSpace(*row.ApprovalReviewAPI) != "" {
		var cfg domain.ApprovalReviewAPIConfig
		if json.Unmarshal([]byte(*row.ApprovalReviewAPI), &cfg) == nil {
			cfg.Protocol = strings.TrimSpace(cfg.Protocol)
			cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
			cfg.Model = strings.TrimSpace(cfg.Model)
			cfg.APIKey = strings.TrimSpace(cfg.APIKey)
			cfg.APIKeyConfigured = cfg.APIKey != ""
			cfg.APIKeyClear = false
			if cfg.Protocol != "" || cfg.BaseURL != "" || cfg.Model != "" || cfg.APIKey != "" {
				out.ApprovalReviewAPI = &cfg
			}
		}
	}
	out.ApprovalReviewMode = out.EffectiveApprovalReviewMode()
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
	var apiJSON *string
	if p.ApprovalReviewAPI != nil {
		clean := domain.ApprovalReviewAPIConfig{
			Protocol: strings.TrimSpace(p.ApprovalReviewAPI.Protocol),
			BaseURL:  strings.TrimSpace(p.ApprovalReviewAPI.BaseURL),
			Model:    strings.TrimSpace(p.ApprovalReviewAPI.Model),
			APIKey:   strings.TrimSpace(p.ApprovalReviewAPI.APIKey),
		}
		if clean.Protocol != "" || clean.BaseURL != "" || clean.Model != "" || clean.APIKey != "" {
			if raw, err := json.Marshal(clean); err == nil {
				s := string(raw)
				apiJSON = &s
			}
		}
	}
	row := assistantPreferenceRow{
		UserID:              userID,
		ProactiveRoles:      p.ProactiveRoles,
		ProactiveSOPs:       p.ProactiveSOPs,
		SteerOnSend:         p.SteerOnSend,
		Instructions:        strings.TrimSpace(p.Instructions),
		ApprovalReviewMode:  p.EffectiveApprovalReviewMode(),
		ApprovalReviewModel: strings.TrimSpace(p.ApprovalReviewModel),
		ApprovalReviewAPI:   apiJSON,
		UpdatedAt:           &now,
	}
	if err := r.DB.Save(&row).Error; err != nil {
		return p, err
	}
	return r.AssistantPreferences(userID), nil
}
