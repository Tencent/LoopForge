package repo

import (
	"errors"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

var (
	ErrRunnerShareForbidden = errors.New("只有机器绑定人可以管理共享项目")
	ErrRunnerProjectDenied  = errors.New("执行机器未向该项目共享，或你没有该项目的执行权限")
)

func (r *RunnersRepo) lockRunner(tx *gorm.DB, id string) (runnerRow, error) {
	var row runnerRow
	err := r.locking(tx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrRunnerNotFound
	}
	return row, err
}

func (r *RunnersRepo) projectRunnerAccess(projectID string, userID int64) bool {
	if projectID == "" || userID <= 0 {
		return false
	}
	project, ok := r.projects.Project(projectID)
	return ok && project.ID == projectID && project.ArchivedAt == nil && project.HasWritableMember(userID)
}

func (r *RunnersRepo) CanUseRunner(runner domain.Runner, projectID string, userID int64) bool {
	if projectID == "" {
		return userID > 0 && runner.OwnerUserID == userID
	}
	return runner.SharedWithProject(projectID) && r.projectRunnerAccess(projectID, userID)
}

type RunnerProjectView struct {
	domain.Runner
	CanUseInProject bool `json:"canUseInProject,omitempty"`
}

func (r *RunnersRepo) RunnersForUser(userID int64, projectID string) []RunnerProjectView {
	writable := r.projectRunnerAccess(projectID, userID)
	items := r.Runners()
	out := make([]RunnerProjectView, 0, len(items))
	for _, runner := range items {
		canUse := writable && runner.SharedWithProject(projectID)
		if runner.OwnerUserID == userID || canUse {
			out = append(out, RunnerProjectView{Runner: runner, CanUseInProject: canUse})
		}
	}
	return out
}

// SetRunnerSharedProjects replaces the grant list and clears preferences for
// revoked projects in the same transaction.
func (r *RunnersRepo) SetRunnerSharedProjects(id string, userID int64, projectIDs []string) error {
	projectIDs = append([]string(nil), projectIDs...)
	for i, id := range projectIDs {
		projectIDs[i] = strings.TrimSpace(id)
	}
	slices.Sort(projectIDs)
	projectIDs = slices.Compact(projectIDs)
	for _, projectID := range projectIDs {
		if !r.projectRunnerAccess(projectID, userID) {
			return ErrRunnerProjectDenied
		}
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.lockRunner(tx, id)
		if err != nil {
			return err
		}
		if userID <= 0 || row.OwnerUserID == nil || *row.OwnerUserID != userID {
			return ErrRunnerShareForbidden
		}
		if err := tx.Model(&runnerRow{}).Where("id = ?", id).Updates(map[string]any{
			"shared_project_ids": jsonBytes(projectIDs),
			"updated_at":         time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		revoked := tx.Where("default_runner_id = ?", id)
		if len(projectIDs) > 0 {
			revoked = revoked.Where("project_id NOT IN ?", projectIDs)
		}
		return revoked.Delete(&userProjectRunnerPrefRow{}).Error
	})
}

// ProjectDefaultRunner rechecks membership and the grant on each resolution
// so a stale preference cannot allocate a new run on a revoked machine.
func (r *RunnersRepo) ProjectDefaultRunner(userID int64, projectID string) domain.Runner {
	if projectID == "" || userID <= 0 {
		return domain.Runner{}
	}
	var pref userProjectRunnerPrefRow
	if err := r.DB.Where("user_id = ? AND project_id = ?", userID, projectID).Take(&pref).Error; err != nil {
		return domain.Runner{}
	}
	runner, ok := r.Runner(pref.DefaultRunnerID)
	if !ok || !r.CanUseRunner(runner, projectID, userID) {
		return domain.Runner{}
	}
	return runner
}

func (r *RunnersRepo) DefaultRunnerForProject(userID int64, projectID string) string {
	if runner := r.ProjectDefaultRunner(userID, projectID); runner.ID != "" {
		return runner.ID
	}
	return r.DefaultRunner(userID)
}

func (r *RunnersRepo) SetProjectDefaultRunner(userID int64, projectID, runnerID string) error {
	if userID <= 0 || projectID == "" {
		return ErrRunnerProjectDenied
	}
	if runnerID == "" {
		return r.DB.Where("user_id = ? AND project_id = ?", userID, projectID).Delete(&userProjectRunnerPrefRow{}).Error
	}
	if !r.projectRunnerAccess(projectID, userID) {
		return ErrRunnerProjectDenied
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.lockRunner(tx, runnerID)
		if err != nil {
			return err
		}
		if !runnerFromRow(row).SharedWithProject(projectID) {
			return ErrRunnerProjectDenied
		}
		return upsert(tx, "user_project_runner_prefs", map[string]any{
			"user_id": userID, "project_id": projectID, "default_runner_id": runnerID,
		})
	})
}
