package engine

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
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

func TestSOPWorkBranchNamingRetriesAnExistingRemoteName(t *testing.T) {
	const taken = "aicb/payment-timeout-retry"
	const available = "aicb/retry-payment-timeouts"
	source := t.TempDir()
	testGit(t, source, "init", "-b", "main")
	testGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	testGit(t, source, "branch", taken)
	remote := filepath.Join(t.TempDir(), "origin.git")
	testGit(t, source, "clone", "--bare", ".", remote)
	oldHead := testGit(t, remote, "rev-parse", "refs/heads/"+taken)

	e, store := retryEngine(t)
	e.ConfigureExecution("branch-probe", "test", time.Minute, false)
	repository := domain.Repository{
		ID: "branch-probe", ProjectID: "platform", Name: "Example", CloneURL: remote, DefaultBranch: "main",
		WorkBranchNaming: &domain.WorkBranchNaming{Mode: domain.WorkBranchNamingAI, ExecutorID: "codebuddy", Model: "hy4std", Prompt: "Use aicb/ and lowercase English words"},
	}
	if err := store.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "branch-probe", Name: "Branch probe", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", Script: "true"}},
	}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	for _, retry := range []string{available, taken} {
		t.Run(retry, func(t *testing.T) {
			calls := 0
			e.GenerateSOPWorkBranch = func(_ context.Context, request SOPWorkBranchRequest) (string, error) {
				calls++
				if calls == 1 {
					return taken, nil
				}
				if request.Prompt != repository.WorkBranchNaming.Prompt || !strings.Contains(request.Feedback, taken) {
					t.Fatalf("retry did not receive the collision and original naming requirements: %+v", request)
				}
				return retry, nil
			}
			r, err := e.StartWithRepositoriesContext(wf.ID, "Add retry for payment timeouts", []domain.RepositoryContext{
				{RepositoryID: repository.ID, Name: repository.Name, CloneURL: remote, BaseBranch: repository.DefaultBranch},
			}, domain.TelemetryContext{ProjectID: repository.ProjectID, ResponsibleUserID: 1})
			if calls != 2 {
				t.Fatalf("generator called %d times, want 2", calls)
			}
			if retry == taken {
				if !errors.Is(err, gitwork.ErrWorkBranchExists) || r.ID != "" {
					t.Fatalf("repeated collision created run %q with error %v", r.ID, err)
				}
				return
			}
			if err != nil || r.Repository.WorkBranch != available || !r.Repository.CreateNewWorkBranch {
				t.Fatalf("wanted a snapshot of the available branch, got %+v, %v", r.Repository, err)
			}
			workspace := t.TempDir()
			if err := prepareRepository(context.Background(), workspace, r.Repository); err != nil {
				t.Fatal(err)
			}
			if branch := testGit(t, workspace, "branch", "--show-current"); branch != available {
				t.Fatalf("checked out branch %q, want %q", branch, available)
			}
		})
	}
	if head := testGit(t, remote, "rev-parse", "refs/heads/"+taken); head != oldHead {
		t.Fatalf("existing branch head changed from %q to %q", oldHead, head)
	}
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
