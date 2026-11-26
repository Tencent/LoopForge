package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// PutKnowledgeCitation 追加一条引用，并按项目裁掉窗口外和超出上限的旧行。
// 调用方已经拿到工具结果，写入失败只能当诊断，不能当成工具失败。
func (r *KnowledgeRepo) PutKnowledgeCitation(item domain.KnowledgeCitation) error {
	projectID := strings.TrimSpace(item.ProjectID)
	if projectID == "" {
		return errors.New("knowledge citation project id is required")
	}
	at := item.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	row := knowledgeCitationRow{
		ProjectID: clipActivity(projectID, 64),
		AgentID:   clipActivity(item.AgentID, 64),
		UserID:    item.UserID,
		RunID:     clipActivity(item.RunID, 64),
		SessionID: clipActivity(item.SessionID, 64),
		Tool:      clipActivity(item.Tool, 64),
		SourceID:  clipActivity(item.SourceID, 64),
		Path:      clipActivity(item.Path, 512),
		Commit:    clipActivity(item.Commit, 128),
		StartLine: item.StartLine,
		EndLine:   item.EndLine,
		ChunkID:   item.ChunkID,
		BaseID:    clipActivity(item.BaseID, 64),
		Layer:     clipActivity(item.Layer, 32),
		At:        &at,
	}
	if err := r.DB.Create(&row).Error; err != nil {
		return err
	}
	return r.trimKnowledgeCitations(row.ProjectID, domain.KnowledgeCitationLimit, time.Now().UTC().Add(-domain.KnowledgeCitationWindow))
}

// trimKnowledgeCitations 先删掉窗口外的行，再按 id 丢掉超出 keep 的最旧行。
// keep <= 0 时使用默认上限。
func (r *KnowledgeRepo) trimKnowledgeCitations(projectID string, keep int, olderThan time.Time) error {
	projectID = clipActivity(projectID, 64)
	if projectID == "" {
		return nil
	}
	if !olderThan.IsZero() {
		if err := r.DB.Where("project_id = ? AND created_at < ?", projectID, olderThan.UTC()).Delete(&knowledgeCitationRow{}).Error; err != nil {
			return err
		}
	}
	if keep <= 0 {
		keep = domain.KnowledgeCitationLimit
	}
	var cutoff knowledgeCitationRow
	err := r.DB.Select("id").Where("project_id = ?", projectID).
		Order("id DESC").Offset(keep).Limit(1).Take(&cutoff).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.DB.Where("project_id = ? AND id <= ?", projectID, cutoff.ID).Delete(&knowledgeCitationRow{}).Error
}

// KnowledgeCitations 返回一个项目最新的引用。空项目不返回任何行。
func (r *KnowledgeRepo) KnowledgeCitations(projectID string, limit int) []domain.KnowledgeCitation {
	out := make([]domain.KnowledgeCitation, 0)
	projectID = clipActivity(projectID, 64)
	if projectID == "" {
		return out
	}
	if limit <= 0 || limit > domain.KnowledgeCitationLimit {
		limit = domain.KnowledgeCitationLimit
	}
	var rows []knowledgeCitationRow
	if err := r.DB.Where("project_id = ?", projectID).Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeCitationFromRow(row))
	}
	return out
}

// KnowledgeCitationCounts 按来源和路径统计 since 之后的引用次数。
// since 为零值时只按项目过滤。空项目返回空。
func (r *KnowledgeRepo) KnowledgeCitationCounts(projectID string, since time.Time) []domain.KnowledgeCitationCount {
	out := make([]domain.KnowledgeCitationCount, 0)
	projectID = clipActivity(projectID, 64)
	if projectID == "" {
		return out
	}
	q := r.DB.Model(&knowledgeCitationRow{}).
		Select("source_id, path, COUNT(*) as count").
		Where("project_id = ?", projectID)
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since.UTC())
	}
	var rows []struct {
		SourceID string
		Path     string
		Count    int
	}
	if err := q.Group("source_id, path").Order("count DESC, path ASC").Scan(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		if row.Count <= 0 || row.SourceID == "" || row.Path == "" {
			continue
		}
		out = append(out, domain.KnowledgeCitationCount{SourceID: row.SourceID, Path: row.Path, Count: row.Count})
	}
	return out
}

func knowledgeCitationFromRow(row knowledgeCitationRow) domain.KnowledgeCitation {
	out := domain.KnowledgeCitation{
		ID: row.ID, Tool: row.Tool, ProjectID: row.ProjectID, AgentID: row.AgentID, UserID: row.UserID,
		RunID: row.RunID, SessionID: row.SessionID, SourceID: row.SourceID, Path: row.Path,
		Commit: row.Commit, StartLine: row.StartLine, EndLine: row.EndLine, ChunkID: row.ChunkID,
		BaseID: row.BaseID, Layer: row.Layer,
	}
	if row.At != nil {
		out.At = *row.At
	}
	return out
}
