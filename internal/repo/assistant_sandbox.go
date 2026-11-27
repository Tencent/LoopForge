package repo

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SetAssistantSessionSandbox uses the same session → generation lock order as
// BeginAssistantGeneration, so a selection cannot race workspace creation.
func (r *AssistantRepo) SetAssistantSessionSandbox(userID int64, id, sandboxID string) error {
	return r.updateAssistantWorkspaceSettings(userID, id, func(s *domain.AssistantSession) error {
		s.SandboxProfileID = strings.TrimSpace(sandboxID)
		return nil
	})
}

func (r *AssistantRepo) SetAssistantSessionProjectCredentials(userID int64, id, projectID string, ids []string) error {
	return r.updateAssistantWorkspaceSettings(userID, id, func(s *domain.AssistantSession) error {
		if s.ProjectID != projectID {
			return ErrProjectCredentialUnavailable
		}
		s.ProjectCredentialIDs = ids
		for id := range s.ProjectCredentialObservations {
			if !slices.Contains(ids, id) {
				delete(s.ProjectCredentialObservations, id)
			}
		}
		return nil
	})
}

func mergeAssistantCredentialObservations(stored, incoming domain.AssistantSession) map[string]domain.AssistantProjectCredentialObservation {
	observations := stored.ProjectCredentialObservations
	if stored.ProjectID != incoming.ProjectID || !slices.Equal(stored.ProjectCredentialIDs, incoming.ProjectCredentialIDs) {
		return observations
	}
	// Transcript saves can arrive out of order. Keep only observations for
	// the bound credentials and prefer the most recent check, including loss of access.
	for _, id := range stored.ProjectCredentialIDs {
		state, ok := incoming.ProjectCredentialObservations[id]
		if !ok || !state.ObservedAt.After(observations[id].ObservedAt) {
			continue
		}
		if observations == nil {
			observations = make(map[string]domain.AssistantProjectCredentialObservation, len(stored.ProjectCredentialIDs))
		}
		observations[id] = state
	}
	return observations
}

func (r *AssistantRepo) updateAssistantWorkspaceSettings(userID int64, id string, update func(*domain.AssistantSession) error) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return fmt.Errorf("会话不存在")
		}
		s := decodeAssistantSession(row)
		if s.Shared || s.RunID != "" {
			return fmt.Errorf("任务会话使用任务的运行环境，不能切换")
		}
		var generation assistantGenerationRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ?", row.ID).Take(&generation).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && generation.Status == "running" {
			return fmt.Errorf("生成期间不能切换运行环境")
		}
		if err := update(&s); err != nil {
			return err
		}
		return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, userID).Update("payload", encodeAssistantSession(s)).Error
	})
}
