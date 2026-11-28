package repo

import (
	"context"
	"errors"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// PlatformRunnerPrivilegedSandbox defaults to false until an administrator enables it.
func (r *RunnersRepo) PlatformRunnerPrivilegedSandbox(ctx context.Context) (bool, error) {
	var row platformRunnerSettingsRow
	err := r.DB.WithContext(ctx).Select("allow_privileged_sandbox").Take(&row, platformRunnerSettingsID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.AllowPrivilegedSandbox, err
}

// RunnerPrivilegedSandbox follows the platform policy on platform-owned
// runners, including those registered in the cloud pool.
func (r *RunnersRepo) RunnerPrivilegedSandbox(ctx context.Context, runner domain.Runner) (bool, error) {
	if runner.OwnerUserID == 0 {
		return r.PlatformRunnerPrivilegedSandbox(ctx)
	}
	return runner.AllowPrivilegedSandbox, nil
}

func (r *RunnersRepo) SetPlatformRunnerPrivilegedSandbox(ctx context.Context, allowed bool, actorUserID int64) error {
	return upsertColumns(r.DB.WithContext(ctx), "platform_runner_settings", map[string]any{
		"id": platformRunnerSettingsID, "run_limit": domain.DefaultRunnerRunLimit,
		"allow_privileged_sandbox": allowed, "updated_by": actorUserID, "updated_at": time.Now().UTC(),
	}, []string{"allow_privileged_sandbox", "updated_by", "updated_at"})
}

// SetRunnerPrivilegedSandbox changes only the owner's setting for this binding.
// Registration and heartbeat leave this permission intact.
func (r *RunnersRepo) SetRunnerPrivilegedSandbox(ctx context.Context, id string, ownerUserID int64, allowed bool) error {
	q := r.DB.WithContext(ctx).Model(&runnerRow{}).Where("id = ? AND owner_user_id = ?", id, ownerUserID)
	result := q.Updates(map[string]any{"allow_privileged_sandbox": allowed, "updated_at": time.Now().UTC()})
	if result.Error != nil || result.RowsAffected > 0 {
		return result.Error
	}
	if err := q.Select("id").Take(&runnerRow{}).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrRunnerNotFound
	} else {
		return err
	}
}
