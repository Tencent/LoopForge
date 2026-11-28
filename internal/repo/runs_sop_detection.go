package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm/clause"
)

const sopDetectionIDPrefix = "sop-check-"

func SOPDetectionEvalID(runID string) string {
	hash := sha256.Sum256([]byte(runID))
	return sopDetectionIDPrefix + hex.EncodeToString(hash[:16])
}

func (r *RunsRepo) PendingSOPDetections(projectID string, since, staleBefore time.Time, limit int) ([]string, error) {
	return r.pendingSOPDetections(projectID, nil, nil, since, staleBefore, limit)
}

func (r *RunsRepo) PendingSOPDetectionsForWorkflows(projectID string, workflowIDs []string, since, staleBefore time.Time, limit int) ([]string, error) {
	return r.pendingSOPDetections(projectID, workflowIDs, nil, since, staleBefore, limit)
}

func (r *RunsRepo) PendingSOPDetectionsExcludingWorkflows(projectID string, excludeWorkflowIDs []string, since, staleBefore time.Time, limit int) ([]string, error) {
	return r.pendingSOPDetections(projectID, nil, excludeWorkflowIDs, since, staleBefore, limit)
}

func (r *RunsRepo) pendingSOPDetections(projectID string, includeWorkflowIDs, excludeWorkflowIDs []string, since, staleBefore time.Time, limit int) ([]string, error) {
	project := r.sqlExecutionContextJSONText("projectId")
	issue := r.sqlExecutionContextJSONText("issueId")
	purpose := r.sqlExecutionContextJSONText("purpose")
	snapshotMode := "JSON_EXTRACT(snapshot, '$.collaborationMode')"
	if r.isMySQL() {
		snapshotMode = "JSON_UNQUOTE(" + snapshotMode + ")"
	}
	var ids []string
	q := r.DB.Table("runs").
		Joins("LEFT JOIN run_evals AS checks ON checks.run_id = runs.id AND checks.id LIKE ?", sopDetectionIDPrefix+"%").
		Where("runs.status IN ?", []string{"succeeded", "failed"}).
		Where("runs.workflow_id <> '' AND COALESCE(runs.kind, '') <> ? AND runs.created_at >= ?", domain.RunKindWorkspace, since).
		Where("COALESCE("+snapshotMode+", '') <> ?", "group").
		Where("COALESCE("+purpose+", '') <> ?", domain.RunPurposeEvolutionReplay).
		Where("("+project+" = ? OR (COALESCE("+project+", '') = '' AND EXISTS (SELECT 1 FROM issues WHERE issues.id = "+issue+" AND issues.project_id = ?)))", projectID, projectID).
		Where("(checks.id IS NULL OR (checks.status = ? AND checks.created_at < ?))", domain.RunEvalRunning, staleBefore)
	if len(includeWorkflowIDs) > 0 {
		q = q.Where("runs.workflow_id IN ?", includeWorkflowIDs)
	}
	if len(excludeWorkflowIDs) > 0 {
		q = q.Where("runs.workflow_id NOT IN ?", excludeWorkflowIDs)
	}
	err := q.Order("runs.created_at, runs.id").Limit(limit).Pluck("runs.id", &ids).Error
	return ids, err
}

func (r *RunsRepo) ClaimSOPDetection(rec domain.RunEvalRecord, staleBefore time.Time) (bool, error) {
	row := runEvalRow{ID: rec.ID, RunID: rec.RunID, Status: rec.Status, Model: rec.Model, CreatedBy: rec.CreatedBy, Payload: mustJSON(rec), Created: timePtr(rec.CreatedAt)}
	result := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil || result.RowsAffected > 0 {
		return result.RowsAffected > 0, result.Error
	}
	result = r.DB.Model(&runEvalRow{}).Where("id = ? AND status = ? AND created_at < ?", rec.ID, domain.RunEvalRunning, staleBefore).
		Updates(map[string]any{"model": rec.Model, "created_by": rec.CreatedBy, "created_at": row.Created, "payload": row.Payload})
	return result.RowsAffected > 0, result.Error
}

func (r *RunsRepo) FinishSOPDetection(rec domain.RunEvalRecord) error {
	return r.DB.Model(&runEvalRow{}).
		Where("id = ? AND status = ? AND created_at = ?", rec.ID, domain.RunEvalRunning, rec.CreatedAt).
		Updates(map[string]any{"status": rec.Status, "score": rec.Score, "passed": rec.Passed, "payload": mustJSON(rec), "finished_at": timePtr(rec.FinishedAt)}).Error
}

func (r *RunsRepo) SOPDetectionEvaluations(runIDs []string) map[string]domain.RunEvalRecord {
	out := make(map[string]domain.RunEvalRecord, len(runIDs))
	if len(runIDs) == 0 {
		return out
	}
	var rows []runEvalRow
	if err := r.DB.Where("run_id IN ? AND id LIKE ? AND status = ?", runIDs, sopDetectionIDPrefix+"%", domain.RunEvalSucceeded).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		if rec, ok := runEvalFromRow(row); ok {
			out[rec.RunID] = rec
		}
	}
	return out
}

func (r *RunsRepo) SOPDetectionTools(runID string, limit int) ([]domain.TelemetryEvent, error) {
	var payloads [][]byte
	err := r.DB.Table("run_telemetry").Where("run_id = ? AND "+r.sqlPayloadJSONText("category")+" = ?", runID, "tool").
		Order("seq DESC").Limit(limit).Pluck("payload", &payloads).Error
	if err != nil {
		return nil, err
	}
	events := make([]domain.TelemetryEvent, 0, len(payloads))
	for _, raw := range payloads {
		var ev domain.TelemetryEvent
		if json.Unmarshal(raw, &ev) == nil {
			events = append(events, ev)
		}
	}
	return events, nil
}
