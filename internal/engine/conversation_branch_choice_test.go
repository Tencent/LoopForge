package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestAssignWorkBranchesKeepsExplicitBranchesToTheirRepository(t *testing.T) {
	generated := func() string { return "feature/generated" }
	repositories := []domain.RepositoryContext{
		{RepositoryID: "api", WorkBranch: "feature/review", RequireExistingWorkBranch: true},
		{RepositoryID: "web"},
		{RepositoryID: "docs", ReadOnly: true, WorkBranch: "stale"},
	}
	assignWorkBranches(repositories, generated)
	if repositories[0].WorkBranch != "feature/review" || repositories[1].WorkBranch != "feature/generated" || repositories[2].WorkBranch != "" {
		t.Fatalf("assigned = %+v", repositories)
	}
	created := []domain.RepositoryContext{{RepositoryID: "api", WorkBranch: "feature/new", CreateNewWorkBranch: true}, {RepositoryID: "web"}}
	assignWorkBranches(created, generated)
	if created[1].WorkBranch != "feature/generated" {
		t.Fatalf("a created branch leaked into another repository: %+v", created)
	}
	plain := []domain.RepositoryContext{{RepositoryID: "api", WorkBranch: "feature/shared"}, {RepositoryID: "web"}}
	assignWorkBranches(plain, generated)
	if plain[1].WorkBranch != "feature/shared" {
		t.Fatalf("an unflagged first branch is still the run's branch: %+v", plain)
	}
}

func TestPrepareRepositoryReadOnlyChecksOutSharedBranch(t *testing.T) {
	ctx := context.Background()
	remote := newBranchRemote(t)
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	mustGit(t, "", "clone", "-q", remote, seed)
	mustGit(t, seed, "config", "user.email", "test@example.com")
	mustGit(t, seed, "config", "user.name", "Test")
	mustGit(t, seed, "checkout", "-q", "-b", "preview")
	if err := os.WriteFile(filepath.Join(seed, "preview.txt"), []byte("preview\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-qm", "preview only")
	mustGit(t, seed, "push", "-q", "origin", "preview")

	workspace := filepath.Join(root, "workspace")
	repository := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, BaseBranch: "preview", DefaultBranch: "master", ReadOnly: true}
	if err := prepareRepository(ctx, workspace, repository); err != nil {
		t.Fatalf("read-only checkout of a shared branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "preview.txt")); err != nil {
		t.Fatalf("read-only checkout is not on preview: %v", err)
	}
	if branch, _ := gitC(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(branch) != "agentflow-base" {
		t.Fatalf("read-only checkout created a work branch: %q", branch)
	}
}

func TestPushConversationWorkspaceRefusesAnotherCheckedOutBranch(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("bare: %v %s", err, out)
	}
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := New(st, runtime.NewManager(filepath.Join(root, "workspaces")))
	run := domain.Run{
		ID:               "run-drifted",
		Kind:             domain.RunKindWorkspace,
		Status:           "waiting",
		Input:            "改文案",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ExecutionMode: domain.ExecutionModeCloud},
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/recorded", WorkspaceDir: "app"},
			{RepositoryID: "repo-2", Name: "docs", CloneURL: remote, BaseBranch: "master", ReadOnly: true, WorkspaceDir: "docs"},
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(e.sandboxes.RunWorkspace(run.ID), "app")
	if out, err := exec.Command("git", "clone", remote, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	testGit(t, checkout, "checkout", "-b", "feature/recorded")
	// The agent switched branches by hand and committed there.
	testGit(t, checkout, "checkout", "-b", "feature/manual")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PushConversationWorkspace(context.Background(), run.ID, "token", "alice", "alice@tencent.com", "msg", ""); err == nil || !strings.Contains(err.Error(), "feature/manual") || !strings.Contains(err.Error(), "feature/recorded") {
		t.Fatalf("push from another branch must be refused: %v", err)
	}
	if out, _ := exec.Command("git", "-C", remote, "branch", "--list", "feature/*").CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("nothing may be pushed from a drifted checkout: %s", out)
	}
	testGit(t, checkout, "checkout", "feature/recorded")
	heads, err := e.PushConversationWorkspace(context.Background(), run.ID, "token", "alice", "alice@tencent.com", "msg", "")
	if err != nil || len(heads) != 1 || heads[0].WorkBranch != "feature/recorded" {
		t.Fatalf("back on the recorded branch the push goes through, read-only repositories stay out: %+v %v", heads, err)
	}
}
