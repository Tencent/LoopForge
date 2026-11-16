package repo

import (
	"encoding/json"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// MaxScheduleJobExecutionsPerJob bounds the stored history of each job; the
// oldest executions are pruned when a new one is recorded.
const MaxScheduleJobExecutionsPerJob = 200

// ScheduleJobExecutionQuery filters the execution history. A non-nil empty
// JobIDs matches nothing (no visible jobs).
type ScheduleJobExecutionQuery struct {
	JobIDs    []string
	ProjectID string
	Status    string
	TraceID   string
	Limit     int
}

// PutScheduleJobExecution inserts or replaces one execution.
func (r *CatalogRepo) PutScheduleJobExecution(e domain.ScheduleJobExecution) error {
	if strings.TrimSpace(e.ID) == "" {
		e.ID = domain.ScheduleJobExecutionID(e.TraceID)
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&scheduleJobExecutionRow{}).Where("id = ?", e.ID).Count(&existing).Error; err != nil {
			return err
		}
		if err := upsert(tx, "schedule_job_executions", map[string]any{
			"id":         e.ID,
			"job_id":     e.JobID,
			"project_id": e.ProjectID,
			"trace_id":   e.TraceID,
			"status":     e.Status,
			"payload":    mustJSON(e),
			"started_at": timePtr(e.StartedAt),
			"updated_at": timePtr(time.Now().UTC()),
		}); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		// Pruning on every insert keeps a job at most one row over the cap, so
		// listing its IDs is cheap. Skip in Go: MySQL rejects OFFSET without
		// LIMIT, and gorm renders Offset alone as a bare OFFSET there.
		var ids []string
		if err := tx.Model(&scheduleJobExecutionRow{}).Where("job_id = ?", e.JobID).
			Order("started_at DESC").Order("id DESC").Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) <= MaxScheduleJobExecutionsPerJob {
			return nil
		}
		return tx.Where("id IN ?", ids[MaxScheduleJobExecutionsPerJob:]).Delete(&scheduleJobExecutionRow{}).Error
	})
}

func (r *CatalogRepo) ScheduleJobExecution(id string) (domain.ScheduleJobExecution, bool) {
	return payloadOne[domain.ScheduleJobExecution](r.DB, "schedule_job_executions", id)
}

// ScheduleJobExecutions lists executions newest first.
func (r *CatalogRepo) ScheduleJobExecutions(q ScheduleJobExecutionQuery) []domain.ScheduleJobExecution {
	if q.JobIDs != nil && len(q.JobIDs) == 0 {
		return []domain.ScheduleJobExecution{}
	}
	db := r.DB.Model(&scheduleJobExecutionRow{})
	if q.JobIDs != nil {
		db = db.Where("job_id IN ?", q.JobIDs)
	}
	if q.ProjectID != "" {
		db = db.Where("project_id = ?", q.ProjectID)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.TraceID != "" {
		db = db.Where("trace_id = ?", q.TraceID)
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []scheduleJobExecutionRow
	if err := db.Order("started_at DESC").Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return []domain.ScheduleJobExecution{}
	}
	out := make([]domain.ScheduleJobExecution, 0, len(rows))
	for _, row := range rows {
		var e domain.ScheduleJobExecution
		if json.Unmarshal(row.Payload, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// FailRunningScheduleJobExecutions marks executions left running by a
// stopped process as failed. Only executions started before cutoff change.
func (r *CatalogRepo) FailRunningScheduleJobExecutions(cutoff time.Time, reason string) error {
	var rows []scheduleJobExecutionRow
	if err := r.DB.Where("status = ? AND started_at < ?", "running", cutoff.UTC()).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var e domain.ScheduleJobExecution
		if err := json.Unmarshal(row.Payload, &e); err != nil {
			continue
		}
		finished := cutoff.UTC()
		e.Status = "failed"
		if e.Error == "" {
			e.Error = reason
		}
		e.FinishedAt = &finished
		if !e.StartedAt.IsZero() {
			e.DurationMs = finished.Sub(e.StartedAt).Milliseconds()
		}
		if err := r.PutScheduleJobExecution(e); err != nil {
			return err
		}
	}
	return nil
}
