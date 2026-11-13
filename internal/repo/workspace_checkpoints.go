package repo

import (
	"agentflow/internal/domain"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WorkspaceCheckpoint struct {
	ArchiveID      string     `gorm:"size:64" json:"-"`
	ArchiveBytes   int64      `json:"-"`
	ArchiveChunks  int        `json:"-"`
	SaveFailures   int        `json:"-"`
	SaveRetryAt    *time.Time `json:"nextSaveAttemptAt,omitempty"`
	RunID          string     `gorm:"primaryKey;size:64" json:"-"`
	Archive        []byte     `gorm:"type:mediumblob" json:"-"`
	SHA256         string     `gorm:"size:64" json:"-"`
	LastUsedAt     time.Time  `json:"lastUsedAt"`
	SavedAt        *time.Time `json:"savedAt,omitempty"`
	FollowUp       bool       `json:"followUp"`
	Error          string     `gorm:"type:text" json:"error,omitempty"`
	LeaseHandoffID string     `gorm:"size:64" json:"-"`
	LeaseToken     string     `gorm:"size:64" json:"-"`
	LeaseUntil     *time.Time `json:"-"`
	RestoreStatus  string     `gorm:"size:32" json:"-"`
	RestoreStarted *time.Time `json:"-"`
	RestoreError   string     `gorm:"type:text" json:"-"`
	// ReleasedAt is when a conversation switched away from the workspace; until
	// it is used again it is reclaimed without waiting out the idle hour.
	ReleasedAt *time.Time `json:"-"`
}

func (WorkspaceCheckpoint) TableName() string { return "run_workspace_checkpoints" }

// Released reports whether the workspace was released after its last use.
func (c WorkspaceCheckpoint) Released() bool {
	return c.ReleasedAt != nil && !c.LastUsedAt.After(*c.ReleasedAt)
}

var ErrWorkspaceInUse = errors.New("工作区正在使用或保存，请稍后重试")
var ErrWorkspaceCheckpointBackoff = errors.New("workspace checkpoint save is backing off")

func (r *RunsRepo) WorkspaceCheckpoint(runID string) (WorkspaceCheckpoint, bool) {
	var item WorkspaceCheckpoint
	err := r.DB.Omit("archive").Where("run_id = ?", runID).Take(&item).Error
	return item, err == nil
}
func (r *RunsRepo) WorkspaceCheckpointArchive(runID string) ([]byte, error) {
	source, err := r.OpenWorkspaceCheckpointArchive(runID)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return io.ReadAll(source)
}
func (r *RunsRepo) BeginWorkspaceUse(runID string, now time.Time) (string, error) {
	return r.beginWorkspaceLease(runID, now, true, false)
}

// MarkNodeCheckpointRestorePending fences workspace use if an automatic
// full-redo restore is interrupted before its new starting checkpoint is saved.
func (r *RunsRepo) MarkNodeCheckpointRestorePending(runID, token string, now time.Time) error {
	result := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ? AND (lease_handoff_id = ? OR lease_handoff_id IS NULL)", runID, token, now, "").Update("lease_handoff_id", "node-auto-restore-pending")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWorkspaceInUse
	}
	return nil
}

// FailInterruptedNodeCheckpointRestore parks a live run when an automatic
// restore failed after its durable fence was written. An interrupted process
// that cannot reach the database remains fenced for operator recovery.
func (r *RunsRepo) FailInterruptedNodeCheckpointRestore(runID, workspaceID, token, nodeID string, attempt, generation int, message string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if workspaceID != runID {
			if _, err := r.takeRunForUpdate(tx, workspaceID); err != nil {
				return err
			}
		}
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil || row.Generation != generation || row.Status != "running" {
			return ErrRunLeaseLost
		}
		var count int64
		if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_handoff_id = ? AND lease_until > ?", workspaceID, token, "node-auto-restore-pending", time.Now().UTC()).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrWorkspaceInUse
		}
		var node runNodeRow
		if err := tx.Where("run_id = ? AND node_id = ?", runID, nodeID).Take(&node).Error; err != nil {
			return err
		}
		var state domain.NodeRun
		if err := json.Unmarshal(node.Payload, &state); err != nil {
			return err
		}
		if state.Attempt != attempt || state.Status != "running" {
			return ErrRunDecisionStale
		}
		state.Status, state.Error, state.Activity = "failed", message, ""
		state.FinishedAt = time.Now().UTC()
		if err := tx.Model(&runNodeRow{}).Where("run_id = ? AND node_id = ?", runID, nodeID).Updates(map[string]any{"status": state.Status, "payload": mustJSON(state)}).Error; err != nil {
			return err
		}
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{"status": "failed", "revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()}).Error
	})
}

// ParkOrphanNodeCheckpointRestore makes a crashed automatic restore
// recoverable after both the run and workspace leases have expired.
func (r *RunsRepo) ParkOrphanNodeCheckpointRestore(runID string, now time.Time) (bool, error) {
	parked := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if row.Status != "running" || row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			return nil
		}
		var cp WorkspaceCheckpoint
		if err := tx.Omit("archive").Where("run_id = ? AND lease_handoff_id = ?", runID, "node-auto-restore-pending").Take(&cp).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if cp.LeaseUntil != nil && cp.LeaseUntil.After(now) {
			return nil
		}
		var nodes []runNodeRow
		if err := tx.Where("run_id = ? AND status = ?", runID, "running").Find(&nodes).Error; err != nil {
			return err
		}
		for _, node := range nodes {
			var state domain.NodeRun
			if err := json.Unmarshal(node.Payload, &state); err != nil {
				return err
			}
			state.Status = "failed"
			state.Error = "工作区恢复中断，请通过节点回滚恢复之前保存的检查点"
			state.Activity = ""
			state.FinishedAt = now
			if err := tx.Model(&runNodeRow{}).Where("run_id = ? AND node_id = ?", runID, node.NodeID).Updates(map[string]any{"status": state.Status, "payload": mustJSON(state)}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&runRow{}).Where("id = ?", runID).Updates(map[string]any{"status": "failed", "owner_instance_id": "", "lease_until": nil, "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		parked = true
		return nil
	})
	return parked, err
}

// BeginNodeRewind fences both workspace users and run resumption while a
// checkpoint is being restored. The state check and lease acquisition share
// the same run-row lock as run updates.
func (r *RunsRepo) BeginNodeRewind(runID string, now time.Time) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if row.WorkspaceCleaned != nil || row.CancelRequested || (row.Status != "waiting" && row.Status != "failed") {
			return fmt.Errorf("运行必须暂停、等待人工确认或失败，且工作区尚未回收")
		}
		if row.OwnerInstanceID != "" && row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			return fmt.Errorf("运行实例仍在退出，请稍后重试")
		}
		var count int64
		if err := tx.Model(&runNodeRow{}).Where("run_id = ? AND status IN ?", runID, []string{"running", "pending_input"}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("仍有节点正在执行")
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&WorkspaceCheckpoint{RunID: runID, LastUsedAt: now}).Error; err != nil {
			return err
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND (lease_until IS NULL OR lease_until < ?)", runID, now).Updates(map[string]any{
			"lease_token": token, "lease_until": now.Add(15 * time.Minute), "lease_handoff_id": gorm.Expr("CASE WHEN lease_handoff_id IN ? THEN ? ELSE ? END", []string{"node-rewind-pending", "node-auto-restore-pending"}, "node-rewind-pending", "node-rewind"), "last_used_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return nil
	})
	return token, err
}

func nodeRewindActive(tx *gorm.DB, runID string, now time.Time) (bool, error) {
	var count int64
	err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND (lease_handoff_id IN ? OR (lease_handoff_id = ? AND lease_until > ?))", runID, []string{"node-rewind-pending", "node-auto-restore-pending"}, "node-rewind", now).Count(&count).Error
	return count != 0, err
}

// CompleteNodeCheckpointRestore commits the new start snapshot and removes
// its durable fence in a single transaction.
func (r *RunsRepo) CompleteNodeCheckpointRestore(runID, workspaceID, token, nodeID string, attempt, generation int, checkpoint domain.NodeCheckpoint, clearSession bool, reset []string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if workspaceID != runID {
			if _, err := r.takeRunForUpdate(tx, workspaceID); err != nil {
				return err
			}
		}
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if row.Generation != generation || row.Status != "running" {
			return ErrRunLeaseLost
		}
		var node runNodeRow
		if err := tx.Where("run_id = ? AND node_id = ?", runID, nodeID).Take(&node).Error; err != nil {
			return err
		}
		var state domain.NodeRun
		if err := json.Unmarshal(node.Payload, &state); err != nil {
			return err
		}
		if state.Attempt != attempt || state.Status != "running" {
			return ErrRunDecisionStale
		}
		state.Checkpoints = append(state.Checkpoints, checkpoint)
		state.RestoreCheckpoint, state.Rewound = false, false
		if clearSession {
			state.ResumeSessionID = ""
		}
		if err := tx.Model(&runNodeRow{}).Where("run_id = ? AND node_id = ?", runID, nodeID).Update("payload", mustJSON(state)).Error; err != nil {
			return err
		}
		for _, id := range reset {
			var downstream runNodeRow
			if err := tx.Where("run_id = ? AND node_id = ?", runID, id).Take(&downstream).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			} else if err != nil {
				return err
			}
			var value domain.NodeRun
			if err := json.Unmarshal(downstream.Payload, &value); err != nil {
				return err
			}
			value.Rewound, value.RestoreCheckpoint = true, false
			if err := tx.Model(&runNodeRow{}).Where("run_id = ? AND node_id = ?", runID, id).Update("payload", mustJSON(value)).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_handoff_id = ? AND lease_until > ?", workspaceID, token, "node-auto-restore-pending", time.Now().UTC()).Update("lease_handoff_id", "")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return nil
	})
}

func (r *RunsRepo) ValidateNodeRewind(runID, token string, now time.Time) error {
	var count int64
	err := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_handoff_id IN ? AND lease_until > ?", runID, token, []string{"node-rewind", "node-rewind-pending"}, now).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrWorkspaceInUse
	}
	return nil
}

// MarkNodeRewindPending durably fences run resumption before any workspace
// mutation. It remains set after a crash or expired lease until a subsequent
// rewind succeeds, so an interrupted restore cannot silently resume a run.
func (r *RunsRepo) MarkNodeRewindPending(runID, token string, now time.Time) error {
	result := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_handoff_id IN ? AND lease_until > ?", runID, token, []string{"node-rewind", "node-rewind-pending"}, now).Update("lease_handoff_id", "node-rewind-pending")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWorkspaceInUse
	}
	return nil
}

// CompleteNodeRewind removes the durable fence only after the caller has
// persisted the reset node state and audit event.
func (r *RunsRepo) CompleteNodeRewind(runID, token string, now time.Time) error {
	result := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_handoff_id = ? AND lease_until > ?", runID, token, "node-rewind-pending", now).Update("lease_handoff_id", "node-rewind")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWorkspaceInUse
	}
	return nil
}

// FinishNodeRewind atomically publishes the node reset and its audit event
// before removing the durable workspace fence.
func (r *RunsRepo) FinishNodeRewind(run domain.Run, token string, revision int) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, run.ID)
		if err != nil {
			return err
		}
		if row.Revision != revision || row.Generation != run.Generation || (row.Status != "waiting" && row.Status != "failed") {
			return ErrRunDecisionStale
		}
		var lease WorkspaceCheckpoint
		if err := tx.Omit("archive").Where("run_id = ? AND lease_token = ? AND lease_handoff_id = ? AND lease_until > ?", run.ID, token, "node-rewind-pending", time.Now().UTC()).Take(&lease).Error; err != nil {
			return ErrWorkspaceInUse
		}
		for id, node := range run.Nodes {
			if node == nil {
				continue
			}
			if err := upsert(tx, "run_nodes", map[string]any{"run_id": run.ID, "node_id": id, "status": node.Status, "attempt": node.Attempt, "payload": mustJSON(node)}); err != nil {
				return err
			}
		}
		if len(run.Events) == 0 {
			return fmt.Errorf("rewind event is missing")
		}
		event := run.Events[len(run.Events)-1]
		if event.Type != "workspace.rewound" {
			return fmt.Errorf("rewind event is missing")
		}
		var seq int64
		if err := tx.Raw("SELECT COALESCE(MAX(seq), -1) + 1 FROM run_events WHERE run_id = ?", run.ID).Scan(&seq).Error; err != nil {
			return err
		}
		if err := upsert(tx, "run_events", map[string]any{"run_id": run.ID, "seq": seq, "event_id": event.ID, "node_id": event.NodeID, "type": event.Type, "message": event.Message, "payload": mustJSON(event), "ts": timePtr(event.Timestamp)}); err != nil {
			return err
		}
		if err := tx.Model(&runRow{}).Where("id = ?", run.ID).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_handoff_id = ? AND lease_until > ?", run.ID, token, "node-rewind-pending", time.Now().UTC()).Update("lease_handoff_id", "node-rewind")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return nil
	})
}

func (r *RunsRepo) ValidateWorkspaceUse(runID, token string, now time.Time) error {
	if token == "" {
		return ErrWorkspaceInUse
	}
	var count int64
	err := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, now).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrWorkspaceInUse
	}
	return nil
}
func (r *RunsRepo) BeginWorkspaceCheckpointSave(runID string, now time.Time) (string, error) {
	return r.beginWorkspaceLease(runID, now, false, false)
}
func (r *RunsRepo) beginWorkspaceLease(runID string, now time.Time, touch, restoring bool) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		run, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		var checkpoint WorkspaceCheckpoint
		if err := tx.Omit("archive").Where("run_id = ?", runID).Take(&checkpoint).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if checkpoint.LeaseHandoffID == "node-rewind-pending" || checkpoint.LeaseHandoffID == "node-auto-restore-pending" {
			return fmt.Errorf("节点回滚尚未完成，请先重新执行回滚或恢复回滚前检查点")
		}
		// An unfinished outcome on a bound workspace is retried, never assumed.
		unfinished := checkpoint.RestoreStatus == "failed" || checkpoint.RestoreStatus == "restoring"
		if restoring && run.WorkspaceCleaned == nil && !unfinished {
			token = ""
			return nil
		}
		if !touch && checkpoint.SaveRetryAt != nil && now.Before(*checkpoint.SaveRetryAt) {
			return ErrWorkspaceCheckpointBackoff
		}
		until := now.Add(15 * time.Minute)
		lastUsed := now
		if !touch && run.Updated != nil {
			lastUsed = *run.Updated
		}
		row := WorkspaceCheckpoint{RunID: runID, LastUsedAt: lastUsed}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		values := map[string]any{"lease_token": token, "lease_until": until, "lease_handoff_id": ""}
		if touch {
			values["last_used_at"] = now
		}
		if restoring {
			values["restore_status"], values["restore_started"], values["restore_error"] = "restoring", now, ""
		} else if checkpoint.RestoreStatus == "restoring" {
			// Taking over an expired restore lease fences that restore out, so
			// record it as interrupted. A later binding under this lease can
			// then tell a stale outcome from a restore still in progress.
			values["restore_status"], values["restore_error"] = "failed", WorkspaceRestoreInterrupted
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND (lease_until IS NULL OR lease_until < ?)", runID, now).Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return nil
	})
	return token, err
}
func (r *RunsRepo) RenewWorkspaceUse(runID, token string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		var checkpoint WorkspaceCheckpoint
		if err := tx.Omit("archive").Where("run_id = ? AND lease_token = ?", runID, token).Take(&checkpoint).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorkspaceInUse
			}
			return err
		}
		// The row is locked through the run and was just read with this token;
		// an unchanged lease_until (same-millisecond renewal) is still a renewal.
		until := now.Add(15 * time.Minute)
		return tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Update("lease_until", until).Error
	})
}

func (r *RunsRepo) FinishWorkspaceUse(runID, token string, now time.Time, archive []byte, saveError string) error {
	attemptedAt := time.Now().UTC()
	if now.After(attemptedAt) {
		attemptedAt = now
	}
	return r.finishWorkspaceUse(runID, token, now, attemptedAt, archive, saveError)
}
func (r *RunsRepo) FinishWorkspaceCheckpointSave(runID, token string, idleSince, attemptedAt time.Time, saveError string) error {
	return r.finishWorkspaceUse(runID, token, idleSince, attemptedAt, nil, saveError)
}
func (r *RunsRepo) finishWorkspaceUse(runID, token string, now, attemptedAt time.Time, archive []byte, saveError string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		var item WorkspaceCheckpoint
		if err := tx.Omit("archive").Where("run_id = ? AND lease_token = ?", runID, token).Take(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorkspaceInUse
			}
			return err
		}
		if item.LastUsedAt.After(now) {
			now = item.LastUsedAt
		}
		handoff := ""
		if item.LeaseHandoffID == "node-rewind-pending" || item.LeaseHandoffID == "node-auto-restore-pending" {
			handoff = item.LeaseHandoffID
		}
		values := map[string]any{"lease_token": "", "lease_until": nil, "lease_handoff_id": handoff, "last_used_at": now}
		if archive != nil {
			sum := sha256.Sum256(archive)
			values["archive"], values["archive_id"], values["archive_bytes"], values["archive_chunks"] = archive, "", len(archive), 0
			values["sha256"], values["saved_at"] = hex.EncodeToString(sum[:]), now
			values["error"], values["save_failures"], values["save_retry_at"] = "", 0, nil
		} else if saveError != "" {
			failures := min(item.SaveFailures+1, 12)
			delay := min(5*time.Minute*time.Duration(1<<uint(failures-1)), 6*time.Hour)
			next := attemptedAt.Add(delay)
			values["error"], values["save_failures"], values["save_retry_at"] = saveError, failures, next
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		if archive != nil {
			return tx.Where("run_id = ?", runID).Delete(&workspaceCheckpointChunk{}).Error
		}
		return nil
	})
}
func (r *RunsRepo) ClaimCheckpointWorkspaceCleanup(runID, token string, revision int, now time.Time) (bool, error) {
	claimed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if row.Revision != revision || row.Status == "running" || row.Status == "queued" || row.WorkspaceCleaned != nil || row.CleanupNotBefore != nil && now.Before(*row.CleanupNotBefore) {
			return nil
		}
		var checkpoint WorkspaceCheckpoint
		if err = tx.Omit("archive").Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, now).Take(&checkpoint).Error; err != nil {
			return err
		}
		if checkpoint.SavedAt == nil || checkpoint.SHA256 == "" || checkpoint.Error != "" {
			return fmt.Errorf("工作区未成功保存，暂缓回收")
		}
		if checkpoint.FollowUp && !checkpoint.Released() && now.Before(checkpoint.LastUsedAt.Add(time.Hour)) {
			return nil
		}
		var count int64
		if err = tx.Model(&runNodeRow{}).Where("run_id = ? AND status = ?", runID, "running").Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		result := tx.Model(&runRow{}).Where("id = ? AND revision = ?", runID, revision).Updates(map[string]any{"workspace_cleaned_at": now, "sandbox_id": ""})
		claimed = result.RowsAffected == 1
		return result.Error
	})
	return claimed, err
}
func (r *RunsRepo) RestoreCheckpointWorkspaceBinding(runID, token, host, root string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, now).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrWorkspaceInUse
		}
		// A restore lease keeps "restoring" until FinishWorkspaceRestore records
		// its outcome. Any other lease that binds the workspace supersedes an
		// earlier failed or interrupted restore.
		if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND restore_status = ?", runID, token, "failed").
			Updates(map[string]any{"restore_status": "ready", "restore_error": ""}).Error; err != nil {
			return err
		}
		values := map[string]any{"workspace_cleaned_at": nil, "revision": gorm.Expr("revision + 1")}
		if host != "" {
			values["workspace_host"] = host
			values["workspace_root"] = root
		}
		return tx.Model(&runRow{}).Where("id = ?", runID).Updates(values).Error
	})
}
func (r *RunsRepo) CheckpointWorkspaceRunIDs() []string {
	var ids []string
	r.DB.Model(&WorkspaceCheckpoint{}).Pluck("run_id", &ids)
	return ids
}

func (r *RunsRepo) SaveWorkspaceCheckpoint(runID, token string, now time.Time, archive []byte) error {
	if len(archive) == 0 || len(archive) > workspaceCheckpointInlineBytes {
		return fmt.Errorf("invalid inline workspace checkpoint size")
	}
	sum := sha256.Sum256(archive)
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		result := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, time.Now().UTC()).Updates(map[string]any{"archive": archive, "archive_id": "", "archive_bytes": len(archive), "archive_chunks": 0, "sha256": hex.EncodeToString(sum[:]), "saved_at": now, "error": "", "save_failures": 0, "save_retry_at": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return tx.Where("run_id = ?", runID).Delete(&workspaceCheckpointChunk{}).Error
	})
}

// MarkWorkspaceFollowUp validates the lease by a read, never by RowsAffected:
// MySQL reports changed rows, so two tools of one turn touching the workspace
// within the same millisecond (DATETIME(3)) would otherwise look like a lost
// lease and fail one of them with ErrWorkspaceInUse.
func (r *RunsRepo) MarkWorkspaceFollowUp(runID, token string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := holdsWorkspaceLease(tx, runID, token); err != nil {
			return err
		}
		return tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Updates(map[string]any{"follow_up": true, "last_used_at": now}).Error
	})
}

func holdsWorkspaceLease(tx *gorm.DB, runID, token string) error {
	if token == "" {
		return ErrWorkspaceInUse
	}
	var count int64
	if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrWorkspaceInUse
	}
	return nil
}

// ReleaseWorkspace marks a workspace no conversation is bound to any more, so
// the idle sweep saves and reclaims it on its next pass.
func (r *RunsRepo) ReleaseWorkspace(runID string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row := WorkspaceCheckpoint{RunID: runID, LastUsedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		return tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", runID).Update("released_at", now).Error
	})
}

func (r *RunsRepo) TouchWorkspaceUse(runID string, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		run, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if run.WorkspaceCleaned != nil {
			return ErrWorkspaceInUse
		}
		item := WorkspaceCheckpoint{RunID: runID, LastUsedAt: now, FollowUp: true}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
			return err
		}
		return tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", runID).Updates(map[string]any{"last_used_at": now, "follow_up": true}).Error
	})
}

// Explicit deletion uses the same lease and marks the directory unavailable
// before filesystem removal, preventing retry from reusing it during deletion.
func (r *RunsRepo) ClaimWorkspaceDeletion(runID, token string, revision int, now time.Time) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.takeRunForUpdate(tx, runID)
		if err != nil {
			return err
		}
		if row.Revision != revision || row.Status == "running" || row.Status == "waiting" || row.Status == "queued" {
			return ErrWorkspaceInUse
		}
		var count int64
		if err := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ? AND lease_until > ?", runID, token, now).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrWorkspaceInUse
		}
		return tx.Model(&runRow{}).Where("id = ?", runID).Update("workspace_cleaned_at", now).Error
	})
}
