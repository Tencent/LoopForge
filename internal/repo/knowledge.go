package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeRepo owns knowledge control metadata and the review queue. The
// review body itself belongs to the content plane; content is injected here so
// callers keep a single Put/Get API while payload never carries the body.
type KnowledgeRepo struct {
	*deps
	content *KnowledgeContentRepo
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
	ids := make([]string, 0, len(out))
	for _, item := range out {
		ids = append(ids, item.ID)
	}
	if writebacks := r.KnowledgeWritebacksBySubmissions(ids); len(writebacks) > 0 {
		for i := range out {
			applyKnowledgeWritebackView(&out[i], writebacks[out[i].ID])
		}
	}
	return out
}

func (r *KnowledgeRepo) KnowledgeSubmission(id string) (domain.KnowledgeSubmission, bool) {
	var row knowledgeSubmissionRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.KnowledgeSubmission{}, false
	}
	out := knowledgeSubmissionFromRow(row)
	out.Content = ""
	if r.content != nil {
		if body, ok := r.content.KnowledgeSubmissionContentVersion(out.ID, row.ContentKey); ok {
			out.Content = body
		}
	}
	// A non-retract review item always has a body at creation time. If the
	// content plane cannot return it, keep the item readable for authorization
	// and retraction decisions but mark the body as missing so publish/edit
	// paths refuse to write an empty document over the hosted copy.
	if !strings.EqualFold(strings.TrimSpace(out.Action), domain.KnowledgeSubmissionActionRetract) && strings.TrimSpace(out.Content) == "" {
		out.ContentMissing = true
	}
	if wb, ok := r.KnowledgeWritebackBySubmission(out.ID); ok {
		applyKnowledgeWritebackView(&out, wb)
	}
	return out, true
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
	// Stage an immutable body version, then publish its reference with the
	// control CAS. Losing writers cannot mutate the winner's selected body.
	body := item.Content
	item.Content = ""
	if strings.TrimSpace(body) != "" {
		if r.content == nil {
			return errors.New("knowledge content store is not configured")
		}
		sum := sha256.Sum256([]byte(body))
		item.ContentKey = hex.EncodeToString(sum[:])
		if err := r.content.PutKnowledgeSubmissionContentVersion(item.ID, item.ContentKey, body); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	created := item.CreatedAt
	row := knowledgeSubmissionRow{
		ID: item.ID, ProjectID: item.ProjectID, RunID: item.RunID, ArtifactID: item.ArtifactID, ContentKey: item.ContentKey,
		Status: item.Status, Payload: payload, Created: &created, Updated: &now, Version: item.Version + 1,
	}
	if item.Status == domain.KnowledgeSubmissionPublishing {
		until := now.Add(2 * time.Minute)
		row.PublishUntil = &until
	}
	if len(item.SedimentSignals) > 0 && item.Fingerprint != "" && (item.Status == domain.KnowledgeSubmissionPending || item.Status == domain.KnowledgeSubmissionPublishing || item.Status == domain.KnowledgeSubmissionPublished) {
		fingerprint := item.Fingerprint
		row.Fingerprint = &fingerprint
	}
	if item.Version == 0 {
		res := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return apperr.New(apperr.Conflict, "审核条目或内容指纹已存在")
		}
		return nil
	}
	q := r.DB.Model(&knowledgeSubmissionRow{}).Where("id = ? AND version = ?", item.ID, item.Version)
	if item.ExpectedStatus != "" {
		q = q.Where("status = ?", item.ExpectedStatus)
	}
	updates := map[string]any{"status": row.Status, "payload": row.Payload, "version": row.Version, "fingerprint": row.Fingerprint, "updated_at": now, "publish_until": nil}
	if item.Status == domain.KnowledgeSubmissionPublishing {
		if r.isMySQL() {
			updates["publish_until"] = gorm.Expr("TIMESTAMPADD(SECOND,120,UTC_TIMESTAMP(6))")
		} else {
			updates["publish_until"] = row.PublishUntil
		}
	}
	if row.ContentKey != "" {
		updates["content_key"] = row.ContentKey
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apperr.New(apperr.Conflict, "审核条目已被其他操作修改")
	}
	return nil
}

func (r *KnowledgeRepo) DeleteKnowledgeSubmission(id string) error {
	id = strings.TrimSpace(id)
	if r.content != nil {
		if err := r.content.DeleteKnowledgeSubmissionContent(id); err != nil {
			return err
		}
	}
	res := r.DB.Where("id = ?", id).Delete(&knowledgeSubmissionRow{})
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
	out.Version, out.ExpectedStatus, out.ContentKey = row.Version, row.Status, row.ContentKey
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
	mode := strings.TrimSpace(base.SedimentPublishMode)
	if mode == "" {
		mode = domain.KnowledgeSedimentReview
	}
	var rules []byte
	if base.SedimentRules != nil {
		encoded, err := json.Marshal(base.SedimentRules)
		if err != nil {
			return err
		}
		rules = encoded
	}
	row := knowledgeBaseRow{
		ID: base.ID, Name: base.Name, Layer: base.Layer, OwnerUserID: base.OwnerUserID,
		VectorEnabled: base.VectorEnabled, EmbeddingModel: base.EmbeddingModel,
		EmbeddingDim: base.EmbeddingDim, QdrantCollection: base.QdrantCollection,
		SedimentPublishMode: mode, SedimentRules: rules,
		Created: &created, Updated: &now,
	}
	if base.ProjectID != "" {
		projectID := base.ProjectID
		row.ProjectID = &projectID
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"project_id", "name", "layer", "owner_user_id",
			"vector_enabled", "embedding_model", "embedding_dim", "qdrant_collection",
			"sediment_publish_mode", "sediment_rules", "updated_at",
		}),
	}).Create(&row).Error
}

// SetKnowledgeVectorConfig 只更新一行的向量配置，避免用整行 upsert 覆盖
// 其它字段。启用/停用向量、换模型后重建 collection 都走这里。
func (r *KnowledgeRepo) SetKnowledgeVectorConfig(id string, enabled bool, model string, dim int, collection string) error {
	return r.DB.Model(&knowledgeBaseRow{}).Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{
			"vector_enabled":    enabled,
			"embedding_model":   model,
			"embedding_dim":     dim,
			"qdrant_collection": collection,
			"updated_at":        time.Now().UTC(),
		}).Error
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
	out := domain.KnowledgeBase{
		ID: row.ID, Name: row.Name, Layer: row.Layer, OwnerUserID: row.OwnerUserID,
		VectorEnabled: row.VectorEnabled, EmbeddingModel: row.EmbeddingModel,
		EmbeddingDim: row.EmbeddingDim, QdrantCollection: row.QdrantCollection,
		SedimentPublishMode: row.SedimentPublishMode,
	}
	if row.ProjectID != nil {
		out.ProjectID = *row.ProjectID
	}
	if len(row.SedimentRules) > 0 {
		if err := json.Unmarshal(row.SedimentRules, &out.SedimentRules); err != nil {
			out.SedimentRules = nil
		}
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
		MetadataChecks: true,
	}
	columns := []string{"base_id", "kind", "name", "git_url", "branch", "path_prefix", "last_error"}
	if source.Kind != "git" {
		columns = append(columns, "head_commit", "head_revision", "index_status", "doc_count", "last_synced_at")
	}
	result := r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: sourceAssignments(columns, r.isMySQL()),
	}).Create(&row).Error
	if result != nil {
		return result
	}
	var current knowledgeSourceRow
	if err := r.DB.Select("status").Where("id = ?", source.ID).Take(&current).Error; err != nil {
		return err
	}
	if current.Status == "deleting" {
		return apperr.New(apperr.Conflict, "来源正在删除，请稍后重新登记")
	}
	return nil
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
		LeaseVersion: row.LeaseVersion, CredentialCiphertext: row.CredentialCiphertext, CredentialConfigured: row.CredentialCiphertext != "",
		GitURL: row.GitURL, Branch: row.Branch, PathPrefix: row.PathPrefix,
		HeadCommit: row.HeadCommit, HeadRevision: row.HeadRevision,
		Status: row.Status, IndexStatus: row.IndexStatus, DocCount: row.DocCount,
		LastError: row.LastError, LastSyncedAt: row.LastSyncedAt,
		LeaseOwner: row.LeaseOwner, LeaseUntil: row.LeaseUntil,
		MetadataChecks: row.MetadataChecks, SyncStage: row.SyncStage,
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
		return tx.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting'", source.ID).Update("credential_ciphertext", ciphertext).Error
	})
}

// SetKnowledgeSourceMetadataChecks 只改来源的元数据检查开关，不动同步状态。
// 来源不存在时返回 ErrRecordNotFound。
func (r *KnowledgeRepo) SetKnowledgeSourceMetadataChecks(id string, enabled bool) error {
	result := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting'", strings.TrimSpace(id)).Update("metadata_checks", enabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var n int64
		if err := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting'", strings.TrimSpace(id)).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return gorm.ErrRecordNotFound
		}
	}
	return nil
}

func (r *KnowledgeRepo) SetKnowledgeSourceCredential(id, ciphertext string) error {
	result := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting' AND kind = ?", id, "git").Updates(map[string]any{
		"credential_ciphertext": ciphertext, "status": "pending", "last_error": "",
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteKnowledgeBinding is idempotent; unloading never deletes shared content.
func (r *KnowledgeRepo) DeleteKnowledgeBinding(projectID, baseID string) error {
	return r.DB.Where("project_id = ? AND base_id = ?", projectID, baseID).Delete(&knowledgeBindingRow{}).Error
}

// ---------- 撤回墓碑（控制面） ----------

// KnowledgeTombstone 按项目加内容指纹读取一条墓碑。指纹相同即视为同内容，
// 不再区分来源和路径。
func (r *KnowledgeRepo) KnowledgeTombstone(projectID, fingerprint string) (domain.KnowledgeTombstone, bool) {
	projectID, fingerprint = strings.TrimSpace(projectID), strings.TrimSpace(fingerprint)
	if projectID == "" || fingerprint == "" {
		return domain.KnowledgeTombstone{}, false
	}
	var row knowledgeTombstoneRow
	if err := r.DB.Where("project_id = ? AND fingerprint = ?", projectID, fingerprint).Take(&row).Error; err != nil {
		return domain.KnowledgeTombstone{}, false
	}
	return knowledgeTombstoneFromRow(row), true
}

// PutKnowledgeTombstone upserts a tombstone; retracting the same content twice
// keeps one row.
func (r *KnowledgeRepo) PutKnowledgeTombstone(item domain.KnowledgeTombstone) error {
	now := time.Now().UTC()
	created := item.CreatedAt
	if created.IsZero() {
		created = now
	}
	row := knowledgeTombstoneRow{
		ProjectID: item.ProjectID, Fingerprint: item.Fingerprint,
		BaseID: item.BaseID, SourceID: item.SourceID, Path: item.Path,
		SubmissionID: item.SubmissionID, Title: clipActivity(item.Title, 512),
		Reason: clipActivity(item.Reason, 255), CreatedBy: clipActivity(item.CreatedBy, 255),
		Created: &created,
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "fingerprint"}},
		DoUpdates: clause.AssignmentColumns([]string{"base_id", "source_id", "path", "submission_id", "title", "reason", "created_by", "created_at"}),
	}).Create(&row).Error
}

// KnowledgeTombstones lists a project's tombstones newest first. Empty project
// lists every project; it is only used by background diagnostics.
func (r *KnowledgeRepo) KnowledgeTombstones(projectID string) []domain.KnowledgeTombstone {
	out := make([]domain.KnowledgeTombstone, 0)
	q := r.DB.Model(&knowledgeTombstoneRow{})
	if projectID = strings.TrimSpace(projectID); projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	var rows []knowledgeTombstoneRow
	if err := q.Order("created_at DESC").Limit(500).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeTombstoneFromRow(row))
	}
	return out
}

func knowledgeTombstoneFromRow(row knowledgeTombstoneRow) domain.KnowledgeTombstone {
	out := domain.KnowledgeTombstone{
		ProjectID: row.ProjectID, Fingerprint: row.Fingerprint,
		BaseID: row.BaseID, SourceID: row.SourceID, Path: row.Path,
		SubmissionID: row.SubmissionID, Title: row.Title,
		Reason: row.Reason, CreatedBy: row.CreatedBy,
	}
	if row.Created != nil {
		out.CreatedAt = *row.Created
	}
	return out
}

// DeleteKnowledgeSubmissionCAS cannot delete an item claimed by a publisher.
func (r *KnowledgeRepo) DeleteKnowledgeSubmissionCAS(item domain.KnowledgeSubmission) error {
	res := r.DB.Where("id = ? AND version = ? AND status = ?", item.ID, item.Version, item.ExpectedStatus).Delete(&knowledgeSubmissionRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return apperr.New(apperr.Conflict, "审核条目已被其他操作修改")
	}
	if r.content != nil {
		return r.content.DeleteKnowledgeSubmissionContent(item.ID)
	}
	return nil
}

func (r *KnowledgeRepo) KnowledgeSubmissionByFingerprint(project, fingerprint string) (domain.KnowledgeSubmission, bool) {
	var row knowledgeSubmissionRow
	if err := r.DB.Where("project_id = ? AND fingerprint = ?", project, fingerprint).Take(&row).Error; err != nil {
		return domain.KnowledgeSubmission{}, false
	}
	return knowledgeSubmissionFromRow(row), true
}
func (r *KnowledgeRepo) UpdateKnowledgeSourceSummary(id string, revision int64, count int) error {
	expr := "MAX(head_revision, ?)"
	if r.isMySQL() {
		expr = "GREATEST(head_revision, ?)"
	}
	return r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting'", id).Updates(map[string]any{"head_revision": gorm.Expr(expr, revision), "doc_count": count}).Error
}

func sourceAssignments(columns []string, mysql bool) clause.Set {
	columns = append(columns, "status") // status last: MySQL evaluates assignments left to right.
	out := make(clause.Set, 0, len(columns))
	for _, col := range columns {
		incoming := "excluded.`" + col + "`"
		if mysql {
			incoming = "VALUES(`" + col + "`)"
		}
		out = append(out, clause.Assignment{Column: clause.Column{Name: col}, Value: gorm.Expr("CASE WHEN knowledge_sources.status = 'deleting' THEN knowledge_sources.`" + col + "` ELSE " + incoming + " END")})
	}
	return out
}

// RecoverExpiredKnowledgePublications includes hosted writes and attempts that
// crashed before or after creating a writeback. Version changes fence final CAS.
func (r *KnowledgeRepo) RecoverExpiredKnowledgePublications() error {
	expired := func() *gorm.DB {
		q := r.DB.Model(&knowledgeSubmissionRow{}).Where("status = ?", domain.KnowledgeSubmissionPublishing)
		if r.isMySQL() {
			return q.Where("publish_until IS NULL OR publish_until <= UTC_TIMESTAMP(6)")
		}
		return q.Where("publish_until IS NULL OR publish_until <= ?", time.Now().UTC())
	}
	var rows []knowledgeSubmissionRow
	if err := expired().Select("id", "version").Order("id").Limit(200).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var wb knowledgeWritebackRow
		err := r.DB.Where("submission_id = ?", row.ID).Take(&wb).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		durable := err == nil && wb.Commit != "" && (wb.Status == domain.KnowledgeWritebackBranchPushed || wb.Status == domain.KnowledgeWritebackMROpen || wb.Status == domain.KnowledgeWritebackMerged || wb.Status == domain.KnowledgeWritebackClosed)
		if durable {
			// A persisted push is the publication result. Complete its existing CAS
			// before making the entry editable; reconcile also uses this same path.
			if err := r.RecoverKnowledgeWritebackSubmission(row.ID); err != nil {
				if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.Conflict {
					return err
				}
			}
			continue
		}
		if err := expired().Where("id = ? AND version = ?", row.ID, row.Version).Updates(map[string]any{"status": domain.KnowledgeSubmissionPending, "publish_until": nil, "version": gorm.Expr("version + 1")}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *KnowledgeRepo) KnowledgePublicationValid(id string, version uint64) bool {
	q := r.DB.Model(&knowledgeSubmissionRow{}).Where("id = ? AND version = ? AND status = ?", id, version, domain.KnowledgeSubmissionPublishing)
	if r.isMySQL() {
		q = q.Where("publish_until > UTC_TIMESTAMP(6)")
	} else {
		q = q.Where("publish_until > ?", time.Now().UTC())
	}
	var n int64
	return q.Count(&n).Error == nil && n == 1
}

// KnowledgeSchemaVersion is the data-migration generation required by this
// binary. 039 renames legacy hosted source IDs and inserts version 39 only
// after both planes have been migrated. 040 adds governance noise-control
// columns and inserts version 40.
const KnowledgeSchemaVersion = 40

var ErrKnowledgeSchemaOutdated = errors.New("knowledge schema is not upgraded")

// KnowledgeSchemaVersion reads the control-plane migration ledger. A missing
// table or row means this installation predates the versioned knowledge schema.
// SQLite test stores do not run SQL migrations, so they are treated as current.
func (r *KnowledgeRepo) KnowledgeSchemaVersion() (int, error) {
	if r == nil || r.deps == nil || r.DB == nil || !r.isMySQL() {
		return KnowledgeSchemaVersion, nil
	}
	var tables int64
	if err := r.DB.Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_schema_versions'").Scan(&tables).Error; err != nil {
		return 0, err
	}
	if tables == 0 {
		return 0, nil
	}
	var version int
	if err := r.DB.Raw("SELECT COALESCE(MAX(version), 0) FROM knowledge_schema_versions").Scan(&version).Error; err != nil {
		return 0, err
	}
	return version, nil
}

// CheckKnowledgeSchema fails startup when the database has not completed the
// data migrations this binary depends on. It never repairs the schema from Go:
// the operator must stop every API/worker instance and run the explicit script.
func (r *KnowledgeRepo) CheckKnowledgeSchema() error {
	version, err := r.KnowledgeSchemaVersion()
	if err != nil {
		return fmt.Errorf("check knowledge schema version: %w", err)
	}
	if version >= KnowledgeSchemaVersion {
		return nil
	}
	return fmt.Errorf("%w: 当前版本 %d，需要 %d。升级前先停止所有 API/worker 实例（不只是本机 Compose 平台），执行 bash scripts/knowledge-schema.sh apply 完成 037–040 迁移，再启动服务", ErrKnowledgeSchemaOutdated, version, KnowledgeSchemaVersion)
}
