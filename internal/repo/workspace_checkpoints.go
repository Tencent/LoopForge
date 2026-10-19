package repo

import (
	"agentflow/internal/domain"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WorkspaceCheckpoint struct {
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
}

func (WorkspaceCheckpoint) TableName() string { return "run_workspace_checkpoints" }

var ErrWorkspaceInUse = errors.New("工作区正在使用或保存，请稍后重试")

func (r *RunsRepo) WorkspaceCheckpoint(runID string) (WorkspaceCheckpoint, bool) {
	var item WorkspaceCheckpoint
	err := r.DB.Omit("archive").Where("run_id = ?", runID).Take(&item).Error
	return item, err == nil
}
func (r *RunsRepo) WorkspaceCheckpointArchive(runID string) ([]byte, error) {
	var item WorkspaceCheckpoint
	if err := r.DB.Where("run_id = ?", runID).Take(&item).Error; err != nil {
		return nil, err
	}
	if len(item.Archive) == 0 {
		return nil, fmt.Errorf("该任务尚无可恢复的工作区快照；未保存的历史文件无法恢复")
	}
	sum := sha256.Sum256(item.Archive)
	if hex.EncodeToString(sum[:]) != item.SHA256 {
		return nil, fmt.Errorf("工作区快照校验失败")
	}
	return item.Archive, nil
}
func (r *RunsRepo) BeginWorkspaceUse(runID string, now time.Time, allowedHandoffID ...string) (string, error) {
	return r.beginWorkspaceLease(runID, now, true, allowedHandoffID...)
}
func (r *RunsRepo) BeginWorkspaceCheckpointSave(runID string, now time.Time) (string, error) {
	return r.beginWorkspaceLease(runID, now, false)
}
func (r *RunsRepo) beginWorkspaceLease(runID string, now time.Time, touch bool, allowedHandoffID ...string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := r.takeRunForUpdate(tx, runID); err != nil {
			return err
		}
		var handoff sopHandoffJobRow
		q := tx.Where("run_id = ? AND status IN ?", runID, []string{domain.SOPHandoffJobPending, domain.SOPHandoffJobRunning}).Take(&handoff)
		if q.Error != nil && !errors.Is(q.Error, gorm.ErrRecordNotFound) {
			return q.Error
		}
		handoffID := ""
		until := now.Add(15 * time.Minute)
		if q.Error == nil {
			if len(allowedHandoffID) == 0 || allowedHandoffID[0] != handoff.ID || handoff.Status != domain.SOPHandoffJobRunning || handoff.LeaseUntil == nil || !handoff.LeaseUntil.After(now) {
				return ErrWorkspaceInUse
			}
			handoffID = handoff.ID
			until = *handoff.LeaseUntil
		} else if len(allowedHandoffID) > 0 && allowedHandoffID[0] != "" {
			return ErrWorkspaceInUse
		}
		row := WorkspaceCheckpoint{RunID: runID, LastUsedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		values := map[string]any{"lease_token": token, "lease_until": until, "lease_handoff_id": handoffID}
		if touch {
			values["last_used_at"] = now
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
		if checkpoint.LeaseHandoffID != "" {
			var handoff sopHandoffJobRow
			if err := tx.Where("run_id = ? AND id = ? AND status = ? AND lease_until > ?", runID, checkpoint.LeaseHandoffID, domain.SOPHandoffJobRunning, now).
				Take(&handoff).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrWorkspaceInUse
				}
				return err
			}
			until = *handoff.LeaseUntil
		}
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
	values := map[string]any{"lease_token": "", "lease_until": nil, "lease_handoff_id": "", "last_used_at": now, "error": saveError}
	if archive != nil {
		sum := sha256.Sum256(archive)
		values["archive"] = archive
		values["sha256"] = hex.EncodeToString(sum[:])
		values["saved_at"] = now
	}
	result := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWorkspaceInUse
	}
	return nil
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
	if len(archive) == 0 {
		return fmt.Errorf("empty workspace checkpoint")
	}
	sum := sha256.Sum256(archive)
	result := r.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND lease_token = ?", runID, token).Updates(map[string]any{"archive": archive, "sha256": hex.EncodeToString(sum[:]), "saved_at": now, "error": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWorkspaceInUse
	}
	return nil
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
		return tx.Model(&WorkspaceCheckpoint{}).Where("run_id = ? AND (lease_until IS NULL OR lease_until < ?)", runID, now).Updates(map[string]any{"last_used_at": now, "follow_up": true}).Error
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
