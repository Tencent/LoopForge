package repo

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"agentflow/internal/domain"
)

const maxRunEvalsPerRun = 10

func (r *RunsRepo) PutRunEval(v domain.RunEvalRecord) error {
	if v.ID == "" || v.RunID == "" {
		return fmt.Errorf("run eval id and runId are required")
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := upsert(r.DB, "run_evals", map[string]any{
		"id": v.ID, "run_id": v.RunID, "status": v.Status, "score": v.Score, "passed": v.Passed,
		"model": v.Model, "created_by": v.CreatedBy, "payload": payload,
		"created_at": timePtr(v.CreatedAt), "finished_at": timePtr(v.FinishedAt),
	}); err != nil {
		return err
	}
	return r.trimRunEvals(v.RunID)
}

func (r *RunsRepo) RunEval(id string) (domain.RunEvalRecord, bool) {
	var row runEvalRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.RunEvalRecord{}, false
	}
	return runEvalFromRow(row)
}

func (r *RunsRepo) LatestRunEval(runID string) (domain.RunEvalRecord, bool) {
	var row runEvalRow
	if err := r.DB.Where("run_id = ?", runID).Order("created_at DESC, id DESC").Limit(1).Take(&row).Error; err != nil {
		return domain.RunEvalRecord{}, false
	}
	return runEvalFromRow(row)
}

func (r *RunsRepo) ListRunEvals(runID string) []domain.RunEvalRecord {
	var rows []runEvalRow
	if err := r.DB.Where("run_id = ?", runID).Order("created_at DESC, id DESC").Limit(maxRunEvalsPerRun).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.RunEvalRecord, 0, len(rows))
	for _, row := range rows {
		if rec, ok := runEvalFromRow(row); ok {
			out = append(out, rec)
		}
	}
	return out
}

func (r *RunsRepo) ActiveRunEval(runID string) (domain.RunEvalRecord, bool) {
	var row runEvalRow
	if err := r.DB.Where("run_id = ? AND status = ?", runID, domain.RunEvalRunning).Order("created_at DESC").Limit(1).Take(&row).Error; err != nil {
		return domain.RunEvalRecord{}, false
	}
	return runEvalFromRow(row)
}

func (r *RunsRepo) trimRunEvals(runID string) error {
	var ids []string
	// MySQL rejects a bare OFFSET. Pair it with an explicit LIMIT, the same way
	// definition revisions are trimmed.
	var err error
	if r.isMySQL() {
		err = r.DB.Raw(
			"SELECT id FROM run_evals WHERE run_id = ? ORDER BY created_at DESC, id DESC LIMIT 18446744073709551615 OFFSET ?",
			runID, maxRunEvalsPerRun).Scan(&ids).Error
	} else {
		err = r.DB.Model(&runEvalRow{}).Where("run_id = ?", runID).Order("created_at DESC, id DESC").Limit(math.MaxInt64).Offset(maxRunEvalsPerRun).Pluck("id", &ids).Error
	}
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return r.DB.Where("id IN ?", ids).Delete(&runEvalRow{}).Error
}

func runEvalFromRow(row runEvalRow) (domain.RunEvalRecord, bool) {
	var rec domain.RunEvalRecord
	if json.Unmarshal(row.Payload, &rec) != nil {
		rec = domain.RunEvalRecord{ID: row.ID, RunID: row.RunID}
	}
	rec.ID = row.ID
	rec.RunID = row.RunID
	rec.Status = row.Status
	rec.Score = row.Score
	rec.Passed = row.Passed
	rec.Model = row.Model
	rec.CreatedBy = row.CreatedBy
	if row.Created != nil {
		rec.CreatedAt = *row.Created
	}
	if row.Finished != nil {
		rec.FinishedAt = *row.Finished
	}
	return rec, true
}
