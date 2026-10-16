package repo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sopHandoffJobRow struct {
	RunID             string     `gorm:"column:run_id;primaryKey;size:64"`
	ID                string     `gorm:"column:id;size:64;uniqueIndex:uq_sop_handoff_job"`
	NodeName          string     `gorm:"column:node_name;type:text"`
	SummaryPreview    string     `gorm:"column:summary_preview;type:text"`
	Summary           []byte     `gorm:"column:summary;type:longblob"`
	ModelSourceSHA    string     `gorm:"column:model_source_sha;size:64"`
	NodeID            string     `gorm:"column:node_id;size:128"`
	ActorID           int64      `gorm:"column:actor_id"`
	Status            string     `gorm:"column:status;size:24;index:idx_sop_handoff_runnable,priority:1"`
	Phase             string     `gorm:"column:phase;size:24"`
	Error             string     `gorm:"column:error;type:text"`
	SummaryPath       string     `gorm:"column:summary_path;type:text"`
	SummaryArtifactID string     `gorm:"column:summary_artifact_id;size:128"`
	Created           time.Time  `gorm:"column:created_at"`
	Updated           time.Time  `gorm:"column:updated_at"`
	Owner             string     `gorm:"column:owner;size:128"`
	LeaseUntil        *time.Time `gorm:"column:lease_until;index:idx_sop_handoff_runnable,priority:2"`
	Attempt           int        `gorm:"column:attempt"`
}

func (sopHandoffJobRow) TableName() string { return "sop_handoff_jobs" }

func sopHandoffJobFromRow(row sopHandoffJobRow) domain.SOPHandoffJob {
	return domain.SOPHandoffJob{ID: row.ID, RunID: row.RunID, NodeID: row.NodeID,
		NodeName: row.NodeName, SummaryPreview: row.SummaryPreview, Summary: string(row.Summary), ModelSourceSHA: row.ModelSourceSHA,
		ActorID: row.ActorID, Status: row.Status, Phase: row.Phase, Error: row.Error,
		SummaryPath: row.SummaryPath, SummaryArtifactID: row.SummaryArtifactID,
		CreatedAt: row.Created, UpdatedAt: row.Updated, Owner: row.Owner,
		LeaseUntil: row.LeaseUntil, Attempt: row.Attempt}
}

func (r *RunsRepo) SOPHandoffJob(runID string) (domain.SOPHandoffJob, bool, error) {
	var row sopHandoffJobRow
	err := r.DB.Where("run_id = ?", runID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.SOPHandoffJob{}, false, nil
	}
	if err != nil {
		return domain.SOPHandoffJob{}, false, err
	}
	return sopHandoffJobFromRow(row), true, nil
}

// BeginSOPHandoffJob atomically creates a new operation or returns the existing
// active/failed operation. Failures require explicit retry; duplicate submits
// cannot erase a saved summary or schedule the SOP twice.
func (r *RunsRepo) BeginSOPHandoffJob(job domain.SOPHandoffJob) (domain.SOPHandoffJob, bool, error) {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.RunID) == "" || strings.TrimSpace(job.NodeID) == "" || job.ActorID <= 0 {
		return domain.SOPHandoffJob{}, false, fmt.Errorf("handoff job id, run, node and actor are required")
	}
	now := job.CreatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	fresh := sopHandoffJobRow{ID: job.ID, RunID: job.RunID, NodeID: job.NodeID,
		NodeName: job.NodeName, ActorID: job.ActorID, Status: domain.SOPHandoffJobPending,
		Phase: domain.SOPHandoffPhaseSummarizing, Created: now, Updated: now}
	var saved sopHandoffJobRow
	created := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		runRow, err := r.takeRunForUpdate(tx, job.RunID)
		if err != nil {
			return err
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&fresh)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			saved = fresh
			created = true
			return nil
		}
		if err := r.locking(tx).Where("run_id = ?", job.RunID).Take(&saved).Error; err != nil {
			return err
		}
		run := runFromRow(runRow)
		replaceable := saved.Status == domain.SOPHandoffJobSucceeded || saved.Status == domain.SOPHandoffJobFailed && run.WorkflowSnapshot.CollaborationMode == "group" && run.WorkflowSnapshot.GroupSOPHandoff != nil && run.WorkflowSnapshot.GroupSOPHandoff.ID == saved.ID
		if saved.ID == job.ID || !replaceable {
			return nil
		}
		res = tx.Model(&sopHandoffJobRow{}).Where("run_id = ? AND id = ? AND status = ?", job.RunID, saved.ID, saved.Status).
			Select("*").Updates(&fresh)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			saved = fresh
			created = true
			return nil
		}
		return tx.Where("run_id = ?", job.RunID).Take(&saved).Error
	})
	return sopHandoffJobFromRow(saved), created, err
}

func (r *RunsRepo) RunnableSOPHandoffJobs(now time.Time, limit int) ([]domain.SOPHandoffJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var rows []sopHandoffJobRow
	err := r.DB.Where("status = ? OR (status = ? AND (lease_until IS NULL OR lease_until <= ?))",
		domain.SOPHandoffJobPending, domain.SOPHandoffJobRunning, now.UTC()).Order("created_at ASC").Order("run_id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]domain.SOPHandoffJob, len(rows))
	for i, row := range rows {
		out[i] = sopHandoffJobFromRow(row)
	}
	return out, nil
}

// ClaimSOPHandoffJob uses a caller-generated unique owner token for each
// claim. A crashed worker is recoverable after expiry without allowing an old
// request to publish progress for its successor.
func (r *RunsRepo) ClaimSOPHandoffJob(runID, jobID, owner string, now time.Time, ttl time.Duration) (domain.SOPHandoffJob, bool, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 {
		return domain.SOPHandoffJob{}, false, fmt.Errorf("handoff lease owner and positive ttl are required")
	}
	now = now.UTC()
	var row sopHandoffJobRow
	claimed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		res := tx.Model(&sopHandoffJobRow{}).Where("run_id = ? AND id = ? AND (status = ? OR (status = ? AND (lease_until IS NULL OR lease_until <= ?)))",
			runID, jobID, domain.SOPHandoffJobPending, domain.SOPHandoffJobRunning, now).
			Updates(map[string]any{"status": domain.SOPHandoffJobRunning, "owner": owner,
				"lease_until": now.Add(ttl), "attempt": gorm.Expr("attempt + 1"), "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Where("run_id = ? AND id = ?", runID, jobID).Take(&row).Error; err != nil {
			return err
		}
		// Remove only a matching expired handoff lease. The next worker must
		// acquire a fresh workspace token; a stale worker cannot finish it.
		if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_handoff_id = ? AND (lease_until IS NULL OR lease_until <= ?)", runID, jobID, now).
			Updates(map[string]any{"lease_token": "", "lease_until": nil, "lease_handoff_id": ""}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return sopHandoffJobFromRow(row), claimed, err
}

func (r *RunsRepo) sopHandoffOwned(runID, jobID, owner string, now time.Time) *gorm.DB {
	return r.DB.Model(&sopHandoffJobRow{}).Where("run_id = ? AND id = ? AND owner = ? AND status = ? AND lease_until > ?",
		runID, jobID, owner, domain.SOPHandoffJobRunning, now.UTC())
}

func (r *RunsRepo) RenewSOPHandoffJob(runID, jobID, owner string, now, leaseUntil time.Time) (bool, error) {
	if owner == "" || !leaseUntil.After(now) {
		return false, fmt.Errorf("handoff lease owner and future expiry are required")
	}
	renewed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		res := tx.Model(&sopHandoffJobRow{}).Where("run_id = ? AND id = ? AND owner = ? AND status = ? AND lease_until > ?", runID, jobID, owner, domain.SOPHandoffJobRunning, now.UTC()).
			Updates(map[string]any{"lease_until": leaseUntil.UTC(), "updated_at": now.UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_handoff_id = ?", runID, jobID).
			Update("lease_until", leaseUntil.UTC()).Error; err != nil {
			return err
		}
		renewed = true
		return nil
	})
	return renewed, err
}

func validSOPHandoffPhase(phase string) bool {
	switch phase {
	case domain.SOPHandoffPhaseSummarizing, domain.SOPHandoffPhaseSaving, domain.SOPHandoffPhaseStarting, domain.SOPHandoffPhaseDone:
		return true
	}
	return false
}

func sopHandoffProgress(job domain.SOPHandoffJob, now time.Time) map[string]any {
	return map[string]any{"phase": job.Phase, "error": job.Error, "node_name": job.NodeName, "summary_preview": job.SummaryPreview, "summary": []byte(job.Summary), "model_source_sha": job.ModelSourceSHA,
		"summary_path": job.SummaryPath, "summary_artifact_id": job.SummaryArtifactID, "updated_at": now.UTC()}
}

func (r *RunsRepo) UpdateSOPHandoffJob(job domain.SOPHandoffJob, owner string, now time.Time) (bool, error) {
	if owner == "" || !validSOPHandoffPhase(job.Phase) {
		return false, fmt.Errorf("invalid handoff phase or owner")
	}
	res := r.sopHandoffOwned(job.RunID, job.ID, owner, now).Where("attempt = ?", job.Attempt).Updates(sopHandoffProgress(job, now))
	return res.RowsAffected == 1, res.Error
}

func (r *RunsRepo) FinishSOPHandoffJob(job domain.SOPHandoffJob, owner string, now time.Time) (bool, error) {
	if owner == "" || !validSOPHandoffPhase(job.Phase) || (job.Status != domain.SOPHandoffJobSucceeded && job.Status != domain.SOPHandoffJobFailed) {
		return false, fmt.Errorf("invalid handoff terminal state or owner")
	}
	updates := sopHandoffProgress(job, now)
	updates["status"] = job.Status
	updates["owner"] = ""
	updates["lease_until"] = nil
	res := r.sopHandoffOwned(job.RunID, job.ID, owner, now).Where("attempt = ?", job.Attempt).Updates(updates)
	return res.RowsAffected == 1, res.Error
}

// RetrySOPHandoffJob preserves the last durable phase. In particular, a
// starting failure never discards the saved summary or asks the model again.
func (r *RunsRepo) RetrySOPHandoffJob(runID, jobID string, now time.Time, actorID ...int64) (domain.SOPHandoffJob, bool, error) {
	var row sopHandoffJobRow
	retried := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		updates := map[string]any{"status": domain.SOPHandoffJobPending, "error": "", "owner": "", "lease_until": nil, "updated_at": now.UTC()}
		if len(actorID) > 0 && actorID[0] > 0 {
			updates["actor_id"] = actorID[0]
		}
		res := tx.Model(&sopHandoffJobRow{}).Where("run_id = ? AND id = ? AND status = ?", runID, jobID, domain.SOPHandoffJobFailed).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Where("run_id = ? AND id = ?", runID, jobID).Take(&row).Error; err != nil {
			return err
		}
		retried = true
		return nil
	})
	return sopHandoffJobFromRow(row), retried, err
}

// CompleteObservedSOPHandoffJob resolves a stale start failure only when the
// persisted run proves this exact handoff has already left its pause checkpoint.
func (r *RunsRepo) CompleteObservedSOPHandoffJob(runID, jobID string, now time.Time) (bool, error) {
	changed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		run := runFromRow(row)
		ref := run.WorkflowSnapshot.GroupSOPHandoff
		if ref == nil || ref.ID != jobID || run.WorkflowSnapshot.CollaborationMode == "group" || run.PauseRequested || run.Status == "cancelled" || (run.Status != "running" && !domain.TerminalRunStatus(run.Status)) {
			return nil
		}
		res := tx.Model(&sopHandoffJobRow{}).Where("run_id = ? AND id = ? AND status = ? AND phase = ?", runID, jobID, domain.SOPHandoffJobFailed, domain.SOPHandoffPhaseStarting).
			Updates(map[string]any{"status": domain.SOPHandoffJobSucceeded, "phase": domain.SOPHandoffPhaseDone, "error": "", "updated_at": now.UTC()})
		changed = res.RowsAffected == 1
		return res.Error
	})
	return changed, err
}
func (r *RunsRepo) SOPHandoffJobsForProject(projectID string) ([]domain.SOPHandoffJob, error) {
	jobs := []domain.SOPHandoffJob{}
	if strings.TrimSpace(projectID) == "" {
		return jobs, nil
	}
	var rows []sopHandoffJobRow
	// Project lives in the execution context inside the run's JSON header.
	query := r.DB.Table("sop_handoff_jobs AS j").Select("j.run_id,j.id,j.node_id,j.node_name,j.actor_id,j.status,j.phase,j.error,j.summary_path,j.summary_artifact_id,j.summary_preview,j.created_at,j.updated_at,j.attempt").Joins("JOIN runs AS r ON r.id = j.run_id")
	if r.isMySQL() {
		query = query.Where("JSON_UNQUOTE(JSON_EXTRACT(r.execution_context, ?)) = ?", "$.projectId", projectID)
	} else {
		query = query.Where("json_extract(r.execution_context, ?) = ?", "$.projectId", projectID)
	}
	if err := query.Order("j.created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		jobs = append(jobs, sopHandoffJobFromRow(row))
	}
	return jobs, nil
}
