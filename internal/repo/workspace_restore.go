package repo

import (
	"time"

	"gorm.io/gorm"
)

// WorkspaceRestoreInterrupted is reported for a restore whose lease expired
// before it recorded an outcome, e.g. because its process exited.
const WorkspaceRestoreInterrupted = "工作区恢复已中断，请重新发起重跑"

// BeginWorkspaceRestore publishes the background operation and acquires its
// cross-instance lease in the same run-row transaction as other workspace uses.
// A workspace that is already bound needs no restore, unless the last restore
// failed or was interrupted after binding it; that one is retried so its
// outcome can become ready.
func (r *RunsRepo) BeginWorkspaceRestore(runID string, now time.Time) (string, error) {
	return r.beginWorkspaceLease(runID, now.UTC(), true, true)
}

// FinishWorkspaceRestore releases only this restoration's lease. Restoring an
// immutable snapshot does not require saving the same checkout again.
func (r *RunsRepo) FinishWorkspaceRestore(runID, token string, now time.Time, restoreError string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		run, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if restoreError == "" && run.WorkspaceCleaned != nil {
			restoreError = "工作区恢复尚未完成，请重试"
		}
		status := "ready"
		if restoreError != "" {
			status = "failed"
		}
		res := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND restore_status = ?", runID, token, "restoring").Updates(map[string]any{
			"restore_status": status, "restore_error": restoreError,
			"lease_token": "", "lease_until": nil, "lease_handoff_id": "", "last_used_at": now,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return nil
	})
}
