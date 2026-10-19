package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const WorkspaceEditTTL = 15 * time.Minute // retained for API compatibility; leases are no longer exclusive

func workspacePaused(row runRow) bool {
	return ((row.Status == "waiting" && row.PauseRequested && !row.CancelRequested) || row.Status == "succeeded" || row.Status == "failed" || row.Status == "cancelled") && row.WorkspaceCleaned == nil
}

// AcquireWorkspaceEditor pauses-compatible marker for callers that still hit
// /workspace/takeover. It no longer grants an exclusive lease — any project
// writer may edit once the run is stopped.
func (r *RunsRepo) AcquireWorkspaceEditor(runID string, userID int64) error {
	if userID <= 0 {
		return fmt.Errorf("请先登录")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if !workspacePaused(row) {
			return fmt.Errorf("请先停止执行后再启用编辑")
		}
		var count int64
		if err = tx.Model(&runNodeRow{}).Where("run_id = ? AND status = ?", runID, "running").Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("节点仍在停止，请稍后启用编辑")
		}
		// Clear any stale lease columns so resume paths stay clean.
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{
			"workspace_editor_id": 0, "workspace_edit_until": nil, "revision": gorm.Expr("revision + 1"),
		}).Error
	})
}

// WithWorkspaceEditor runs fn while the run is stopped for human editing.
// Exclusive editor identity is no longer required; agents re-read before write.
func (r *RunsRepo) WithWorkspaceEditor(runID string, userID int64, fn func(domain.Run) error) error {
	return r.withWorkspaceWriter(runID, userID, false, fn)
}

// Main Bot may collaborate with group members in the shared workspace. SOP
// execution still requires a stopped run, just like the IDE.
func (r *RunsRepo) WithMainBotWorkspace(runID string, userID int64, fn func(domain.Run) error) error {
	return r.withWorkspaceWriter(runID, userID, true, fn)
}
func (r *RunsRepo) withWorkspaceWriter(runID string, userID int64, group bool, fn func(domain.Run) error) error {
	if userID <= 0 {
		return fmt.Errorf("请先登录")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		run := runFromRow(row)
		collaborating := group && run.WorkflowSnapshot.CollaborationMode == "group" && !run.CancelRequested && row.WorkspaceCleaned == nil && (run.Status == "running" || run.Status == "waiting")
		if !workspacePaused(row) && !collaborating {
			return fmt.Errorf("请先停止 SOP 执行后再编辑工作区")
		}
		return fn(run)
	})
}

func (r *RunsRepo) ReleaseWorkspaceEditor(runID string, userID int64, beforeRelease ...func(domain.Run) error) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		for _, close := range beforeRelease {
			if err := close(runFromRow(row)); err != nil {
				return err
			}
		}
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{
			"workspace_editor_id": 0, "workspace_edit_until": nil, "revision": gorm.Expr("revision + 1"),
		}).Error
	})
}
