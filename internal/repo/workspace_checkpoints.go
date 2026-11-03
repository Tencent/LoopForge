package repo

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
		if restoring && run.WorkspaceCleaned == nil {
			token = ""
			return nil
		}
		if !touch {
			var checkpoint WorkspaceCheckpoint
			if err := tx.Omit("archive").Where("run_id = ?", runID).Take(&checkpoint).Error; err == nil {
				if checkpoint.SaveRetryAt != nil && now.Before(*checkpoint.SaveRetryAt) {
					return ErrWorkspaceCheckpointBackoff
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
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
		until := now.Add(15 * time.Minute)
		res := tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Update("lease_until", until)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrWorkspaceInUse
		}
		return nil
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
		values := map[string]any{"lease_token": "", "lease_until": nil, "lease_handoff_id": "", "last_used_at": now}
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

func (r *RunsRepo) MarkWorkspaceFollowUp(runID, token string, now time.Time) error {
	result := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Updates(map[string]any{"follow_up": true, "last_used_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
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
