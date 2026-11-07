package repo

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const knowledgeSettingsID = "default"

// KnowledgeRepo owns the upstream knowledge base connection and the review
// queue of distilled documents waiting to be written back.
type KnowledgeRepo struct {
	*deps
}

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

// KnowledgeSubmissions lists submissions newest first. Empty filters match
// everything; content is omitted to keep list payloads small.
func (r *KnowledgeRepo) KnowledgeSubmissions(projectID, status string, limit int) []domain.KnowledgeSubmission {
	out := make([]domain.KnowledgeSubmission, 0)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := r.DB.Model(&knowledgeSubmissionRow{})
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []knowledgeSubmissionRow
	if err := q.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		item := knowledgeSubmissionFromRow(row)
		item.Content = ""
		out = append(out, item)
	}
	return out
}

func (r *KnowledgeRepo) KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool) {
	var row knowledgeSubmissionRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.KnowledgeSubmission{}, false
	}
	return knowledgeSubmissionFromRow(row), true
}

// KnowledgeSubmissionIDsByArtifact maps artifact ids to the submission that
// was created from them, so candidates can show "already queued".
func (r *KnowledgeRepo) KnowledgeSubmissionIDsByArtifact(artifactIDs []string) map[string]string {
	out := map[string]string{}
	if len(artifactIDs) == 0 {
		return out
	}
	var rows []knowledgeSubmissionRow
	if err := r.DB.Select("id", "artifact_id").Where("artifact_id IN ?", artifactIDs).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ArtifactID] = row.ID
	}
	return out
}

func (r *KnowledgeRepo) PutKnowledgeSubmission(item domain.KnowledgeSubmission) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("knowledge submission id is required")
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.Status == "" {
		item.Status = domain.KnowledgeSubmissionPending
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	created := item.CreatedAt
	row := knowledgeSubmissionRow{
		ID: item.ID, ProjectID: item.ProjectID, RunID: item.RunID, ArtifactID: item.ArtifactID,
		Status: item.Status, Payload: payload, Created: &created, Updated: &now,
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"project_id", "run_id", "artifact_id", "status", "payload", "updated_at"}),
	}).Create(&row).Error
}

func (r *KnowledgeRepo) DeleteKnowledgeSubmission(id string) error {
	res := r.DB.Where("id = ?", strings.TrimSpace(id)).Delete(&knowledgeSubmissionRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func knowledgeSubmissionFromRow(row knowledgeSubmissionRow) domain.KnowledgeSubmission {
	var out domain.KnowledgeSubmission
	_ = json.Unmarshal(row.Payload, &out)
	out.ID, out.ProjectID, out.RunID, out.ArtifactID, out.Status = row.ID, row.ProjectID, row.RunID, row.ArtifactID, row.Status
	if row.Created != nil {
		out.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}
