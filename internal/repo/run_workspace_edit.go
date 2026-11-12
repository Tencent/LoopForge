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
		if !runFromRow(row).HumanEditable() {
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

// WithWorkspaceEditor runs fn for a person editing in the IDE, at any time the
// workspace is HumanEditable. Agents re-read before write; the hash guards saves.
func (r *RunsRepo) WithWorkspaceEditor(runID string, userID int64, fn func(domain.Run) error) error {
	return r.withWorkspaceWriter(runID, userID, false, fn)
}

// WithMainBotWorkspace lets Main Bot edit a live conversation workspace as is;
// a borrowed SOP workspace must be stopped first (workbench_workspace_sop).
func (r *RunsRepo) WithMainBotWorkspace(runID string, userID int64, fn func(domain.Run) error) error {
	return r.withWorkspaceWriter(runID, userID, true, fn)
}
func (r *RunsRepo) withWorkspaceWriter(runID string, userID int64, mainBot bool, fn func(domain.Run) error) error {
	if userID <= 0 {
		return fmt.Errorf("请先登录")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		run := runFromRow(row)
		allowed := run.HumanEditable()
		if mainBot {
			allowed = workspacePaused(row) || run.LiveWorkspace()
		}
		if !allowed {
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
