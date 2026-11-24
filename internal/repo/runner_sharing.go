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

func (r *RunnersRepo) writableRunnerProject(projectID string, userID int64) (domain.Project, bool) {
	if projectID == "" || userID <= 0 {
		return domain.Project{}, false
	}
	project, ok := r.projects.Project(projectID)
	return project, ok && project.ID == projectID && project.ArchivedAt == nil && project.HasWritableMember(userID)
}

func runnerSharedByWritableOwner(runner domain.Runner, project domain.Project) bool {
	return runner.SharedWithProject(project.ID) && project.HasWritableMember(runner.OwnerUserID)
}

func (r *RunnersRepo) CanUseRunner(runner domain.Runner, projectID string, userID int64) bool {
	if projectID == "" {
		return userID > 0 && runner.OwnerUserID == userID
	}
	project, writable := r.writableRunnerProject(projectID, userID)
	return writable && runnerSharedByWritableOwner(runner, project)
}

type RunnerProjectView struct {
	domain.Runner
	CanUseInProject      bool `json:"canUseInProject,omitempty"`
	SupportsConversation bool `json:"supportsConversation"`
}

func (r *RunnersRepo) RunnersForUser(userID int64, projectID string) []RunnerProjectView {
	project, writable := r.writableRunnerProject(projectID, userID)
	items := r.Runners()
	out := make([]RunnerProjectView, 0, len(items))
	for _, runner := range items {
		canUse := writable && runnerSharedByWritableOwner(runner, project)
		if runner.OwnerUserID == userID || canUse {
			out = append(out, RunnerProjectView{
				Runner: runner, CanUseInProject: canUse, SupportsConversation: runner.SupportsConversationCLI(),
			})
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
		if _, writable := r.writableRunnerProject(projectID, userID); !writable {
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

// ProjectDefaultRunner rechecks both memberships and the grant on each resolution
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

type RunnerDefaults struct {
	Runner           domain.Runner
	PersonalRunnerID string
	ProjectRunnerID  string
}

// ResolveRunnerDefaults chooses the effective machine and returns the preference
// IDs used by the machine settings, even when a project cannot use the personal default.
func (r *RunnersRepo) ResolveRunnerDefaults(userID int64, projectID string) RunnerDefaults {
	return r.resolveRunnerDefaults(userID, projectID, false)
}

// ResolveConversationRunnerDefaults applies the conversation runtime requirement
// while preserving the saved preference IDs used by the machine settings.
func (r *RunnersRepo) ResolveConversationRunnerDefaults(userID int64, projectID string) RunnerDefaults {
	return r.resolveRunnerDefaults(userID, projectID, true)
}

func (r *RunnersRepo) resolveRunnerDefaults(userID int64, projectID string, conversation bool) RunnerDefaults {
	defaults := RunnerDefaults{PersonalRunnerID: r.DefaultRunner(userID)}
	if runner := r.ProjectDefaultRunner(userID, projectID); runner.ID != "" {
		defaults.ProjectRunnerID = runner.ID
		if !conversation || runner.SupportsConversationCLI() {
			defaults.Runner = runner
			return defaults
		}
	}
	if defaults.PersonalRunnerID == "" {
		return defaults
	}
	if runner, ok := r.Runner(defaults.PersonalRunnerID); ok {
		if conversation && !runner.SupportsConversationCLI() {
			return defaults
		}
		// A personal SOP can still use a legacy ownerless default.
		if (!conversation && projectID == "" && runner.OwnerUserID == 0) || r.CanUseRunner(runner, projectID, userID) {
			defaults.Runner = runner
		}
	}
	return defaults
}

func (r *RunnersRepo) SetProjectDefaultRunner(userID int64, projectID, runnerID string) error {
	if userID <= 0 || projectID == "" {
		return ErrRunnerProjectDenied
	}
	if runnerID == "" {
		return r.DB.Where("user_id = ? AND project_id = ?", userID, projectID).Delete(&userProjectRunnerPrefRow{}).Error
	}
	project, writable := r.writableRunnerProject(projectID, userID)
	if !writable {
		return ErrRunnerProjectDenied
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.lockRunner(tx, runnerID)
		if err != nil {
			return err
		}
		if !runnerSharedByWritableOwner(runnerFromRow(row), project) {
			return ErrRunnerProjectDenied
		}
		return upsert(tx, "user_project_runner_prefs", map[string]any{
			"user_id": userID, "project_id": projectID, "default_runner_id": runnerID,
		})
	})
}
