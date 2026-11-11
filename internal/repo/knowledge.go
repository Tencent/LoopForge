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

// knowledgeActivityRetention 是每个项目保留的最近调用条数。按项目裁剪而不是
// 全局裁剪，避免活跃项目把其他项目的记录挤掉；项目数有界，总行数因此有界。
const knowledgeActivityRetention = domain.KnowledgeActivityLimit

// PutKnowledgeActivity records one Agent knowledge tool call and trims the
// project to its newest retention window. It runs after the tool call has
// already succeeded or failed, so callers must treat an error as diagnostic
// only and never fail the tool call because of it.
func (r *KnowledgeRepo) PutKnowledgeActivity(item domain.KnowledgeActivity) error {
	projectID := strings.TrimSpace(item.ProjectID)
	if projectID == "" {
		return errors.New("knowledge activity project id is required")
	}
	projectID = clipActivity(projectID, 64)
	at := item.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	row := knowledgeActivityRow{
		ProjectID: projectID,
		AgentID:   clipActivity(item.AgentID, 64),
		UserID:    item.UserID,
		RunID:     clipActivity(item.RunID, 64),
		SessionID: clipActivity(item.SessionID, 64),
		Tool:      clipActivity(item.Tool, 64),
		Query:     clipActivity(item.Query, 500),
		Hits:      item.Hits,
		OK:        item.OK,
		Error:     clipActivity(item.Error, 512),
		LatencyMS: item.LatencyMS,
		At:        &at,
	}
	if err := r.DB.Create(&row).Error; err != nil {
		return err
	}
	// Offset retention finds the first row outside the window; deleting by id
	// keeps the operation portable between MySQL and sqlite.
	var cutoff knowledgeActivityRow
	err := r.DB.Select("id").Where("project_id = ?", projectID).
		Order("id DESC").Offset(knowledgeActivityRetention).Limit(1).Take(&cutoff).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.DB.Where("project_id = ? AND id <= ?", projectID, cutoff.ID).Delete(&knowledgeActivityRow{}).Error
}

// KnowledgeActivities returns the newest calls of one project, newest first.
// An empty project returns nothing instead of every project's rows.
func (r *KnowledgeRepo) KnowledgeActivities(projectID string, limit int) []domain.KnowledgeActivity {
	out := make([]domain.KnowledgeActivity, 0)
	projectID = clipActivity(projectID, 64)
	if projectID == "" {
		return out
	}
	if limit <= 0 || limit > knowledgeActivityRetention {
		limit = knowledgeActivityRetention
	}
	var rows []knowledgeActivityRow
	if err := r.DB.Where("project_id = ?", projectID).Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeActivityFromRow(row))
	}
	return out
}

func knowledgeActivityFromRow(row knowledgeActivityRow) domain.KnowledgeActivity {
	out := domain.KnowledgeActivity{
		Tool: row.Tool, ProjectID: row.ProjectID, AgentID: row.AgentID, UserID: row.UserID,
		RunID: row.RunID, SessionID: row.SessionID, Query: row.Query,
		Hits: row.Hits, OK: row.OK, Error: row.Error, LatencyMS: row.LatencyMS,
	}
	if row.At != nil {
		out.At = *row.At
	}
	return out
}

func clipActivity(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
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
		CredentialCiphertext: row.CredentialCiphertext, CredentialConfigured: row.CredentialCiphertext != "",
		GitURL: row.GitURL, Branch: row.Branch, PathPrefix: row.PathPrefix,
		HeadCommit: row.HeadCommit, HeadRevision: row.HeadRevision,
		Status: row.Status, IndexStatus: row.IndexStatus, DocCount: row.DocCount,
		LastError: row.LastError, LastSyncedAt: row.LastSyncedAt,
		LeaseOwner: row.LeaseOwner, LeaseUntil: row.LeaseUntil,
	}
}

// PutKnowledgeSourceWithCredential atomically registers/replaces a source and
// its encrypted grant. Normal source updates never overwrite this column.
func (r *KnowledgeRepo) PutKnowledgeSourceWithCredential(source domain.KnowledgeSource, ciphertext string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		scoped := &KnowledgeRepo{deps: &deps{DB: tx}}
		if err := scoped.PutKnowledgeSource(source); err != nil {
			return err
		}
		return tx.Model(&knowledgeSourceRow{}).Where("id = ?", source.ID).Update("credential_ciphertext", ciphertext).Error
	})
}

func (r *KnowledgeRepo) SetKnowledgeSourceCredential(id, ciphertext string) error {
	result := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND kind = ?", id, "git").Updates(map[string]any{
		"credential_ciphertext": ciphertext, "status": "pending", "last_error": "", "lease_owner": "", "lease_until": nil,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
