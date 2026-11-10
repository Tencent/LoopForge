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

// KnowledgeRepo owns knowledge control metadata and the review queue.
type KnowledgeRepo struct {
	*deps
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

// 内置知识引擎控制面：知识库、来源、项目绑定、同步任务都由 KnowledgeRepo 拥有，
// 只写主库，不存正文。

func (r *KnowledgeRepo) PutKnowledgeBase(base domain.KnowledgeBase) error {
	now := time.Now().UTC()
	created := base.CreatedAt
	if created.IsZero() {
		created = now
	}
	row := knowledgeBaseRow{
		ID: base.ID, Name: base.Name, Layer: base.Layer, OwnerUserID: base.OwnerUserID,
		Created: &created, Updated: &now,
	}
	if base.ProjectID != "" {
		projectID := base.ProjectID
		row.ProjectID = &projectID
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"project_id", "name", "layer", "owner_user_id", "updated_at"}),
	}).Create(&row).Error
}

func (r *KnowledgeRepo) KnowledgeBase(id string) (domain.KnowledgeBase, bool) {
	var row knowledgeBaseRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.KnowledgeBase{}, false
	}
	return knowledgeBaseFromRow(row), true
}

func (r *KnowledgeRepo) KnowledgeBases(projectID string) []domain.KnowledgeBase {
	out := make([]domain.KnowledgeBase, 0)
	q := r.DB.Model(&knowledgeBaseRow{})
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	var rows []knowledgeBaseRow
	if err := q.Order("created_at DESC, id").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeBaseFromRow(row))
	}
	return out
}

func knowledgeBaseFromRow(row knowledgeBaseRow) domain.KnowledgeBase {
	out := domain.KnowledgeBase{ID: row.ID, Name: row.Name, Layer: row.Layer, OwnerUserID: row.OwnerUserID}
	if row.ProjectID != nil {
		out.ProjectID = *row.ProjectID
	}
	if row.Created != nil {
		out.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}

func (r *KnowledgeRepo) PutKnowledgeBinding(binding domain.KnowledgeBinding) error {
	row := knowledgeBindingRow{ProjectID: binding.ProjectID, BaseID: binding.BaseID, Permission: binding.Permission}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "base_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"permission"}),
	}).Create(&row).Error
}

func (r *KnowledgeRepo) KnowledgeBindings(projectID string) []domain.KnowledgeBinding {
	out := make([]domain.KnowledgeBinding, 0)
	var rows []knowledgeBindingRow
	if err := r.DB.Where("project_id = ?", strings.TrimSpace(projectID)).Order("base_id").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, domain.KnowledgeBinding{ProjectID: row.ProjectID, BaseID: row.BaseID, Permission: row.Permission})
	}
	return out
}

func (r *KnowledgeRepo) PutKnowledgeSource(source domain.KnowledgeSource) error {
	row := knowledgeSourceRow{
		ID: source.ID, BaseID: source.BaseID, Kind: source.Kind, Name: source.Name,
		GitURL: source.GitURL, Branch: source.Branch, PathPrefix: source.PathPrefix,
		HeadCommit: source.HeadCommit, HeadRevision: source.HeadRevision,
		Status: source.Status, IndexStatus: source.IndexStatus, DocCount: source.DocCount,
		LastError: source.LastError, LastSyncedAt: source.LastSyncedAt,
		LeaseOwner: source.LeaseOwner, LeaseUntil: source.LeaseUntil,
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"base_id", "kind", "name", "git_url", "branch", "path_prefix",
			"head_commit", "head_revision", "status", "index_status", "doc_count",
			"last_error", "last_synced_at", "lease_owner", "lease_until",
		}),
	}).Create(&row).Error
}

func (r *KnowledgeRepo) KnowledgeSource(id string) (domain.KnowledgeSource, bool) {
	var row knowledgeSourceRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.KnowledgeSource{}, false
	}
	return knowledgeSourceFromRow(row), true
}

func (r *KnowledgeRepo) KnowledgeSources(baseID string) []domain.KnowledgeSource {
	out := make([]domain.KnowledgeSource, 0)
	q := r.DB.Model(&knowledgeSourceRow{})
	if baseID = strings.TrimSpace(baseID); baseID != "" {
		q = q.Where("base_id = ?", baseID)
	}
	var rows []knowledgeSourceRow
	if err := q.Order("id").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeSourceFromRow(row))
	}
	return out
}

func (r *KnowledgeRepo) DeleteKnowledgeSource(id string) error {
	res := r.DB.Where("id = ?", strings.TrimSpace(id)).Delete(&knowledgeSourceRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func knowledgeSourceFromRow(row knowledgeSourceRow) domain.KnowledgeSource {
	return domain.KnowledgeSource{
		ID: row.ID, BaseID: row.BaseID, Kind: row.Kind, Name: row.Name,
		GitURL: row.GitURL, Branch: row.Branch, PathPrefix: row.PathPrefix,
		HeadCommit: row.HeadCommit, HeadRevision: row.HeadRevision,
		Status: row.Status, IndexStatus: row.IndexStatus, DocCount: row.DocCount,
		LastError: row.LastError, LastSyncedAt: row.LastSyncedAt,
		LeaseOwner: row.LeaseOwner, LeaseUntil: row.LeaseUntil,
	}
}
