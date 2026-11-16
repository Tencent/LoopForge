package repo

import (
	"encoding/json"
	"errors"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

var ErrInsightConflict = errors.New("record ID or execution ID already exists with different content")

type InsightsRepo struct{ *deps }

type insightSourceRow struct {
	ID        string `gorm:"primaryKey;size:64"`
	ProjectID string `gorm:"size:128;index"`
	TokenHash string `gorm:"size:64;uniqueIndex"`
	Payload   []byte `gorm:"type:json"`
	ExpiresAt time.Time
	RevokedAt *time.Time
}

func (insightSourceRow) TableName() string { return "insight_sources" }

type insightExecutionRow struct {
	// Hashes give case-sensitive, bounded unique keys on MySQL's default collation.
	ID           string    `gorm:"primaryKey;size:64"`
	ExecutionKey string    `gorm:"size:64;uniqueIndex"`
	SourceID     string    `gorm:"size:64;index"`
	ProjectID    string    `gorm:"size:128;index:idx_insight_project_finished,priority:1"`
	UserID       int64     `gorm:"index"`
	Payload      []byte    `gorm:"type:json"`
	ContentHash  string    `gorm:"size:64"`
	FinishedAt   time.Time `gorm:"index:idx_insight_project_finished,priority:2"`
	ReceivedAt   time.Time
}

func (insightExecutionRow) TableName() string { return "insight_executions" }

func (r *InsightsRepo) CreateInsightSource(source domain.InsightSource, tokenHash string) error {
	return r.DB.Create(&insightSourceRow{ID: source.ID, ProjectID: source.ProjectID, TokenHash: tokenHash, Payload: jsonBytes(source), ExpiresAt: source.ExpiresAt}).Error
}
func (r *InsightsRepo) InsightSource(id string) (domain.InsightSource, error) {
	var row insightSourceRow
	if err := r.DB.First(&row, "id = ?", id).Error; err != nil {
		return domain.InsightSource{}, err
	}
	return decodeInsightSource(row)
}
func decodeInsightSource(row insightSourceRow) (domain.InsightSource, error) {
	var source domain.InsightSource
	err := json.Unmarshal(row.Payload, &source)
	source.RevokedAt = row.RevokedAt
	return source, err
}
func (r *InsightsRepo) InsightSources(projectID string) ([]domain.InsightSource, error) {
	var rows []insightSourceRow
	if err := r.DB.Where("project_id = ?", projectID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []domain.InsightSource{}
	for _, row := range rows {
		s, err := decodeInsightSource(row)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
func (r *InsightsRepo) ActiveInsightSource(tokenHash string, now time.Time) (domain.InsightSource, error) {
	var row insightSourceRow
	if err := r.DB.Where("token_hash = ? AND revoked_at IS NULL AND expires_at > ?", tokenHash, now).First(&row).Error; err != nil {
		return domain.InsightSource{}, err
	}
	return decodeInsightSource(row)
}
func (r *InsightsRepo) RevokeInsightSource(id string, now time.Time) error {
	return r.DB.Model(&insightSourceRow{}).Where("id = ? AND revoked_at IS NULL", id).Update("revoked_at", now).Error
}

// IngestInsightExecution atomically commits an immutable completed execution.
// Both record and execution IDs are unique within a source; retries cannot double count.
func (r *InsightsRepo) IngestInsightExecution(source domain.InsightSource, execution domain.InsightExecution) (bool, error) {
	payload, err := json.Marshal(execution)
	if err != nil {
		return false, err
	}
	row := insightExecutionRow{ID: hexSHA256(source.ID + "\x00" + execution.ID), ExecutionKey: hexSHA256(source.ID + "\x00" + execution.ExternalExecutionID), SourceID: source.ID, ProjectID: source.ProjectID, UserID: source.UserMappings[execution.ExternalUserID], Payload: payload, ContentHash: hexSHA256(string(payload)), FinishedAt: execution.FinishedAt, ReceivedAt: time.Now().UTC()}
	// Serialize against revocation across instances. SQLite test DB has one connection.
	duplicate := false
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		var credential insightSourceRow
		if err := r.locking(tx).First(&credential, "id = ? AND revoked_at IS NULL AND expires_at > ?", source.ID, time.Now().UTC()).Error; err != nil {
			return err
		}
		var existing insightExecutionRow
		err := tx.Where("id = ? OR execution_key = ?", row.ID, row.ExecutionKey).First(&existing).Error
		if err == nil {
			if existing.ID == row.ID && existing.ContentHash == row.ContentHash {
				duplicate = true
				return nil
			}
			return ErrInsightConflict
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&row).Error
	})
	return duplicate, err
}

func (r *InsightsRepo) insightExecutions(filter domain.AnalyticsFilter) ([]insightExecutionRow, error) {
	rows := []insightExecutionRow{}
	q := r.DB.Model(&insightExecutionRow{})
	if filter.ProjectID != "" {
		q = q.Where("project_id = ?", filter.ProjectID)
	}
	if filter.ProjectIDs != nil {
		if len(filter.ProjectIDs) == 0 {
			return rows, nil
		}
		q = q.Where("project_id IN ?", filter.ProjectIDs)
	}
	if filter.UserID != "" {
		q = q.Where("user_id = ?", filter.UserID)
	}
	if !filter.From.IsZero() {
		q = q.Where("finished_at >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		q = q.Where("finished_at <= ?", filter.To)
	}
	err := q.Order("finished_at, id").Find(&rows).Error
	return rows, err
}
