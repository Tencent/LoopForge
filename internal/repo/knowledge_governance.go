package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 知识治理（反馈、知识缺口、健康检查）属于控制面 KnowledgeRepo。结果不存
// 正文，项目/知识库是读取边界；健康检查按知识库整批替换。

func (r *KnowledgeRepo) PutKnowledgeFeedback(item domain.KnowledgeFeedback) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("knowledge feedback id is required")
	}
	created := item.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	row := knowledgeFeedbackRow{
		ID: item.ID, ProjectID: clipActivity(item.ProjectID, 64), BaseID: clipActivity(item.BaseID, 64),
		SourceID: clipActivity(item.SourceID, 64), Path: clipActivity(item.Path, 512),
		Title: clipActivity(item.Title, 512), Query: clipActivity(item.Query, 500),
		Kind: clipActivity(item.Kind, 32), Note: item.Note, Surface: clipActivity(item.Surface, 32),
		Actor: clipActivity(item.Actor, 255), UserID: item.UserID, AgentID: clipActivity(item.AgentID, 64),
		RunID: clipActivity(item.RunID, 64), SessionID: clipActivity(item.SessionID, 64),
		MessageID: clipActivity(item.MessageID, 64), Created: &created,
	}
	return r.DB.Create(&row).Error
}

func (r *KnowledgeRepo) KnowledgeFeedbacks(projectID string, limit int) []domain.KnowledgeFeedback {
	out := make([]domain.KnowledgeFeedback, 0)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return out
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var rows []knowledgeFeedbackRow
	if err := r.DB.Where("project_id = ?", projectID).Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeFeedbackFromRow(row))
	}
	return out
}

func knowledgeFeedbackFromRow(row knowledgeFeedbackRow) domain.KnowledgeFeedback {
	out := domain.KnowledgeFeedback{
		ID: row.ID, ProjectID: row.ProjectID, BaseID: row.BaseID, SourceID: row.SourceID,
		Path: row.Path, Title: row.Title, Query: row.Query, Kind: row.Kind, Note: row.Note,
		Surface: row.Surface, Actor: row.Actor, UserID: row.UserID, AgentID: row.AgentID,
		RunID: row.RunID, SessionID: row.SessionID, MessageID: row.MessageID,
	}
	if row.Created != nil {
		out.CreatedAt = *row.Created
	}
	return out
}

// PutKnowledgeGap 用 (project_id, normalized_query) 合并重复查询：第一次插入
// 计数为 1。同一用户或同一会话在短窗口内再查同一句只刷新最近时间，不增加
// 计数；换人、换会话或超过窗口才加一。已忽略或已处理的行保持原状态。
func (r *KnowledgeRepo) PutKnowledgeGap(item domain.KnowledgeGap) error {
	projectID := strings.TrimSpace(item.ProjectID)
	normalized := strings.TrimSpace(item.Normalized)
	if projectID == "" || normalized == "" {
		return errors.New("knowledge gap project and normalized query are required")
	}
	now := time.Now().UTC()
	if item.LastSeen.IsZero() {
		item.LastSeen = now
	}
	if item.FirstSeen.IsZero() {
		item.FirstSeen = item.LastSeen
	}
	item.LastSeen = item.LastSeen.UTC()
	item.FirstSeen = item.FirstSeen.UTC()
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row knowledgeGapRow
		q := tx.Where("project_id = ? AND normalized_query = ?", projectID, clipActivity(normalized, 500))
		if r.isMySQL() {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err := q.Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			created := knowledgeGapRow{
				ProjectID: projectID, Normalized: clipActivity(normalized, 500),
				Query: clipActivity(item.Query, 500), OccurrenceCount: 1, Status: domain.KnowledgeGapOpen,
				LastUserID: item.UserID, LastSessionID: clipActivity(item.SessionID, 64),
				FirstSeen: &item.FirstSeen, LastSeen: &item.LastSeen,
			}
			return tx.Create(&created).Error
		}
		if err != nil {
			return err
		}
		repeat := sameGapActor(item, row) && withinGapWindow(row.LastSeen, item.LastSeen)
		updates := map[string]any{
			"query_text":      clipActivity(item.Query, 500),
			"last_seen":       item.LastSeen,
			"last_user_id":    item.UserID,
			"last_session_id": clipActivity(item.SessionID, 64),
		}
		if !repeat {
			updates["occurrence_count"] = row.OccurrenceCount + 1
		}
		return tx.Model(&knowledgeGapRow{}).Where("project_id = ? AND normalized_query = ?", row.ProjectID, row.Normalized).Updates(updates).Error
	})
}

func sameGapActor(item domain.KnowledgeGap, row knowledgeGapRow) bool {
	if item.UserID != 0 && item.UserID == row.LastUserID {
		return true
	}
	session := strings.TrimSpace(item.SessionID)
	return session != "" && session == row.LastSessionID
}

func withinGapWindow(previous *time.Time, current time.Time) bool {
	if previous == nil || current.IsZero() {
		return false
	}
	delta := current.Sub(previous.UTC())
	return delta >= 0 && delta < domain.KnowledgeGapRepeatWindow
}

func (r *KnowledgeRepo) KnowledgeGaps(projectID string, limit int) []domain.KnowledgeGap {
	out := make([]domain.KnowledgeGap, 0)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return out
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []knowledgeGapRow
	if err := r.DB.Where("project_id = ? AND (status = ? OR status = '')", projectID, domain.KnowledgeGapOpen).
		Order("occurrence_count DESC, last_seen DESC").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeGapFromRow(row))
	}
	return out
}

// SetKnowledgeGapStatus 把一条缺口标为忽略或已处理。不删除行。
func (r *KnowledgeRepo) SetKnowledgeGapStatus(projectID, normalized, status string) error {
	projectID = strings.TrimSpace(projectID)
	normalized = strings.TrimSpace(normalized)
	if projectID == "" || normalized == "" {
		return errors.New("knowledge gap project and normalized query are required")
	}
	var n int64
	if err := r.DB.Model(&knowledgeGapRow{}).Where("project_id = ? AND normalized_query = ?", projectID, normalized).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return gorm.ErrRecordNotFound
	}
	return r.DB.Model(&knowledgeGapRow{}).Where("project_id = ? AND normalized_query = ?", projectID, normalized).Update("status", status).Error
}

// CountHiddenKnowledgeGaps 统计已从面板隐藏、但仍留在表里的缺口。
func (r *KnowledgeRepo) CountHiddenKnowledgeGaps(projectID string) int {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return 0
	}
	var n int64
	if err := r.DB.Model(&knowledgeGapRow{}).Where("project_id = ? AND status <> ? AND status <> ''", projectID, domain.KnowledgeGapOpen).Count(&n).Error; err != nil {
		return 0
	}
	return int(n)
}

func knowledgeGapFromRow(row knowledgeGapRow) domain.KnowledgeGap {
	out := domain.KnowledgeGap{
		ProjectID: row.ProjectID, Query: row.Query, Normalized: row.Normalized,
		Count: row.OccurrenceCount, Status: row.Status, UserID: row.LastUserID, SessionID: row.LastSessionID,
	}
	if row.FirstSeen != nil {
		out.FirstSeen = *row.FirstSeen
	}
	if row.LastSeen != nil {
		out.LastSeen = *row.LastSeen
	}
	return out
}

// ReplaceKnowledgeGovernanceReport 在一个事务里更新知识库最近扫描摘要并整批
// 替换问题行。扫描没有问题时也保留摘要，面板可显示“最近检查时间”。
func (r *KnowledgeRepo) ReplaceKnowledgeGovernanceReport(baseID string, scan domain.KnowledgeGovernanceScan, issues []domain.KnowledgeGovernanceIssue) error {
	baseID = strings.TrimSpace(baseID)
	if baseID == "" {
		return errors.New("knowledge governance base id is required")
	}
	scannedAt := scan.ScannedAt
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		scanRow := knowledgeGovernanceScanRow{
			BaseID: baseID, ScannedAt: &scannedAt,
			DocCount: scan.DocCount, IssueCount: len(issues),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "base_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"scanned_at", "doc_count", "issue_count"}),
		}).Create(&scanRow).Error; err != nil {
			return err
		}
		if err := tx.Where("base_id = ?", baseID).Delete(&knowledgeGovernanceIssueRow{}).Error; err != nil {
			return err
		}
		if len(issues) == 0 {
			return nil
		}
		rows := make([]knowledgeGovernanceIssueRow, 0, len(issues))
		for _, issue := range issues {
			detected := issue.DetectedAt
			if detected.IsZero() {
				detected = scannedAt
			}
			rows = append(rows, knowledgeGovernanceIssueRow{
				BaseID: baseID, SourceID: clipActivity(issue.SourceID, 64), Path: clipActivity(issue.Path, 512),
				Title: clipActivity(issue.Title, 512), IssueType: clipActivity(issue.Type, 32),
				Line: issue.Line, Summary: issue.Summary, DetectedAt: &detected,
			})
		}
		return tx.CreateInBatches(&rows, 100).Error
	})
}

func (r *KnowledgeRepo) KnowledgeGovernanceIssues(baseIDs []string) []domain.KnowledgeGovernanceIssue {
	out := make([]domain.KnowledgeGovernanceIssue, 0)
	baseIDs = cleanKnowledgeIDs(baseIDs)
	if len(baseIDs) == 0 {
		return out
	}
	var rows []knowledgeGovernanceIssueRow
	if err := r.DB.Where("base_id IN ?", baseIDs).
		Order("base_id, path, issue_type, line").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeGovernanceIssueFromRow(row))
	}
	return out
}

func knowledgeGovernanceIssueFromRow(row knowledgeGovernanceIssueRow) domain.KnowledgeGovernanceIssue {
	out := domain.KnowledgeGovernanceIssue{
		BaseID: row.BaseID, SourceID: row.SourceID, Path: row.Path, Title: row.Title,
		Type: row.IssueType, Line: row.Line, Summary: row.Summary,
	}
	if row.DetectedAt != nil {
		out.DetectedAt = *row.DetectedAt
	}
	return out
}

func (r *KnowledgeRepo) KnowledgeGovernanceScans(baseIDs []string) []domain.KnowledgeGovernanceScan {
	out := make([]domain.KnowledgeGovernanceScan, 0)
	baseIDs = cleanKnowledgeIDs(baseIDs)
	if len(baseIDs) == 0 {
		return out
	}
	var rows []knowledgeGovernanceScanRow
	if err := r.DB.Where("base_id IN ?", baseIDs).Order("base_id").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		scan := domain.KnowledgeGovernanceScan{BaseID: row.BaseID, DocCount: row.DocCount, IssueCount: row.IssueCount}
		if row.ScannedAt != nil {
			scan.ScannedAt = *row.ScannedAt
		}
		out = append(out, scan)
	}
	return out
}

// DeleteKnowledgeGovernanceSource 在删除来源时清掉它的问题行；最近扫描摘要
// 保留，下一次扫描会把该库的问题重新算全。
func (r *KnowledgeRepo) DeleteKnowledgeGovernanceSource(sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil
	}
	return r.DB.Where("source_id = ?", sourceID).Delete(&knowledgeGovernanceIssueRow{}).Error
}
