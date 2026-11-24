package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSOPReusesConversationCheckoutAfterTurn(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const projectID = "conversation-workspace-project"
	if err := st.PutProject(domain.Project{ID: projectID, Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	en := New(st, runtime.NewManager(t.TempDir()))
	owner, err := en.StartConversationWorkspace("Prepare a fix", domain.TelemetryContext{
		ProjectID: projectID, ExecutionPinned: true,
	}, repo.TestNativeSandboxID)
	if err != nil {
		t.Fatal(err)
	}
	root := en.RunWorkspace(owner.ID)
	remote := t.TempDir()
	gitInit(t, remote)
	gitCommitFile(t, remote, "tracked.txt", "baseline")
	const checkout = "code/checkout"
	dir := filepath.Join(root, checkout)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(at string, args ...string) string {
		t.Helper()
		out, err := gitC(context.Background(), at, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	git(root, "clone", remote, dir)
	git(dir, "config", "user.email", "test@example.com")
	git(dir, "config", "user.name", "Test")
	gitCommitFile(t, dir, "local-commit.txt", "unpushed")
	head := git(dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("unstaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "handoff.txt"), []byte("conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := domain.WorkflowDefinition{ID: "child-sop", Name: "Child SOP", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: `
test "$(cat handoff.txt)" = conversation &&
test "$(cat code/checkout/local-commit.txt)" = unpushed &&
test "$(cat code/checkout/tracked.txt)" = unstaged &&
printf sop > result.txt`, MaxAttempts: 1}},
	}
	wf := domain.WorkflowDefinition{ID: "workspace-sop", Name: "SOP", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "child", Name: "Child SOP", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1}},
	}
	for _, workflow := range []domain.WorkflowDefinition{child, wf} {
		if err := st.PutWorkflow(workflow); err != nil {
			t.Fatal(err)
		}
	}
	token, err := en.BeginConversationWorkspaceUse(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	ec := domain.TelemetryContext{ProjectID: projectID, WorkspaceRunID: owner.ID}
	repositories := []domain.RepositoryContext{{RepositoryID: "repo", Name: "Configured repo name", LocalPath: remote, BaseBranch: "master"}}
	started, err := en.StartWithRepositoriesContext(wf.ID, "Continue work", repositories, ec)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, st, started.ID, 5*time.Second, func(run domain.Run) bool {
		return run.Status == "running" && run.Nodes["child"].Status == "running"
	})
	if _, err := en.StartWithRepositoriesContext(wf.ID, "Another SOP", repositories, ec); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("concurrent SOP = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("SOP wrote before the conversation released the workspace: %v", err)
	}
	if err := st.FinishWorkspaceUse(owner.ID, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, st, started.ID, 10*time.Second, func(run domain.Run) bool {
		return domain.TerminalRunStatus(run.Status)
	})
	if done.Status != "succeeded" {
		t.Fatalf("SOP %s: %+v", done.Status, done.Nodes["child"])
	}
	childRun, _ := st.RunState(done.Nodes["child"].ChildRunID)
	if en.RunWorkspace(done.ID) != root || en.RunWorkspace(childRun.ID) != root ||
		done.Repository.WorkspaceDir != checkout || childRun.Repository.WorkspaceDir != checkout {
		t.Fatalf("SOP and child must use %s and %s, got %+v and %+v", root, checkout, done.Repository, childRun.Repository)
	}
	if got := git(dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("SOP changed the checkout's starting commit: %s, want %s", got, head)
	}
	if got := git(dir, "branch", "--show-current"); got != done.Repository.WorkBranch {
		t.Fatalf("checked-out branch = %q, want %q", got, done.Repository.WorkBranch)
	}
	if data, err := os.ReadFile(filepath.Join(root, "result.txt")); err != nil || string(data) != "sop" {
		t.Fatalf("SOP output in original workspace = %q, %v", data, err)
	}
	if err := en.ReleaseConversationWorkspace(owner.ID); err != nil {
		t.Fatal(err)
	}
	en.cleanupRunWorkspace(owner.ID)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("released checkout was not reclaimed: %v", err)
	}
	next, err := en.StartWithRepositoriesContext(wf.ID, "Continue from the reclaimed workspace", repositories, ec)
	if err != nil {
		t.Fatal(err)
	}
	restored := waitRun(t, st, next.ID, 10*time.Second, func(run domain.Run) bool {
		return domain.TerminalRunStatus(run.Status)
	})
	if restored.Status != "succeeded" || en.RunWorkspace(restored.ID) != root {
		t.Fatalf("SOP did not continue from the restored checkout: %+v", restored.Nodes["child"])
	}
}
