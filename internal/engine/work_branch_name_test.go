package engine

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Chat workspaces and free collaboration runs have no issue to name their
// branch after; two opened in the same minute must still get their own.
func TestDefaultWorkBranchKeepsUnrelatedRunsApart(t *testing.T) {
	now := time.Date(2026, 10, 30, 19, 38, 9, 0, time.Local)
	a, b := defaultWorkBranch(0, "", now), defaultWorkBranch(0, "", now)
	if a == b || !strings.HasPrefix(a, "feature/fix-20261030-1938-") || !gitBranchName(a) {
		t.Fatalf("same-minute branches %q and %q", a, b)
	}
	if got := defaultWorkBranch(12, "", now); got != "feature/fix-issue-12-20261030-1938" {
		t.Fatalf("issue branch changed: %q", got)
	}
}

func gitBranchName(name string) bool {
	return exec.Command("git", "check-ref-format", "refs/heads/"+name).Run() == nil
}

func TestSOPWorkBranchNamingStartsLeaseAfterGeneration(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := leaseEngine(t, store, "branch-lease")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Drain(ctx); err != nil {
			t.Error(err)
		}
	})
	repository := domain.Repository{
		ID: "branch-lease", ProjectID: "platform", Name: "example", LocalPath: t.TempDir(), DefaultBranch: "main",
		WorkBranchNaming: &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingAI, ExecutorID: "codebuddy", Model: "hy4std"},
	}
	if err := store.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{
		ID: "branch-lease", Name: "Branch lease", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", ScriptLanguage: "shell", Script: "true"}},
	}
	if err := store.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	var namedAt time.Time
	e.GenerateSOPWorkBranch = func(context.Context, SOPWorkBranchRequest) (string, error) {
		namedAt = time.Now().UTC()
		return "test-branch", nil
	}
	run, err := e.StartWithRepositoriesContext(workflow.ID, "Test", []domain.RepositoryContext{
		{RepositoryID: repository.ID, Name: repository.Name, LocalPath: repository.LocalPath, BaseBranch: repository.DefaultBranch},
	}, domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if namedAt.IsZero() || run.CreatedAt.Before(namedAt) || run.LeaseUntil.Before(namedAt.Add(e.leaseTTL)) {
		t.Fatalf("branch named at %v, run created at %v and leased until %v", namedAt, run.CreatedAt, run.LeaseUntil)
	}
}
