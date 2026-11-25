package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestMySQLRunnerSharingPreservesGrantsAndClearsRevokedDefaults(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prefix := fmt.Sprintf("runner-sharing-%d", time.Now().UnixNano())
	runner := domain.Runner{ID: prefix, OwnerUserID: 1, Name: "Shared runner"}
	projects := []string{prefix + "-one", prefix + "-two"}
	t.Cleanup(func() {
		_ = store.DeregisterRunner(runner.ID, time.Now().UTC())
		_ = store.DB.Where("project_id IN ?", projects).Delete(&projectMemberRow{}).Error
		_ = store.DB.Where("id IN ?", projects).Delete(&projectRow{}).Error
	})
	for _, id := range projects {
		if err := store.PutProject(domain.Project{ID: id, Name: id, OwnerID: 1,
			Members: []domain.ProjectMember{{UserID: 2, Role: "member"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultRunner(1, runner.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunnerSharedProjects(runner.ID, 1, projects); err != nil {
		t.Fatal(err)
	}
	runner.Status = domain.RunnerStatusOnline
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	for _, id := range projects {
		if err := store.SetProjectDefaultRunner(2, id, runner.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.SetProjectDefaultRunner(2, id, runner.ID); err != nil {
			t.Fatalf("repeat selection: %v", err)
		}
	}
	if err := store.SetRunnerSharedProjects(runner.ID, 1, projects[1:]); err != nil {
		t.Fatal(err)
	}
	if defaults := store.ResolveRunnerDefaults(1, projects[0]); defaults.Runner.ID != "" || defaults.PersonalRunnerID != runner.ID {
		t.Fatalf("revoked project returned an invalid default: %+v", defaults)
	}
	if defaults := store.ResolveRunnerDefaults(1, projects[1]); defaults.Runner.ID != runner.ID {
		t.Fatalf("eligible personal default was not selected: %+v", defaults)
	}
	if err := store.SetRunnerSharedProjects(runner.ID, 1, projects); err != nil {
		t.Fatal(err)
	}
	if got := store.ProjectDefaultRunner(2, projects[0]); got.ID != "" {
		t.Fatalf("regrant restored revoked default: %+v", got)
	}
	if got := store.ProjectDefaultRunner(2, projects[1]); got.ID != runner.ID {
		t.Fatalf("revoking one project changed the other's default: %+v", got)
	}
	if err := store.DeregisterRunner(runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := store.DB.Model(&userProjectRunnerPrefRow{}).Where("default_runner_id = ?", runner.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deregister retained %d project defaults: %v", count, err)
	}
}
