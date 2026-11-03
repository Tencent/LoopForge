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

func TestPushConversationWorkspaceCommitsAndPushesWorkBranch(t *testing.T) {
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
		ID:               "run-direct-push",
		Status:           "waiting",
		Input:            "改文案",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "free-collaboration", Name: "自由协作", CollaborationMode: "group"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ExecutionMode: domain.ExecutionModeCloud},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/direct", WorkspaceDir: "app"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(e.sandboxes.RunWorkspace(run.ID), "app")
	if out, err := exec.Command("git", "clone", remote, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	testGit(t, checkout, "checkout", "-b", "feature/direct")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.PushConversationWorkspace(context.Background(), run.ID, "", "alice", "alice@example.com", "msg"); err == nil || !strings.Contains(err.Error(), "Git Token") {
		t.Fatalf("missing token must explain the fix: %v", err)
	}
	heads, err := e.PushConversationWorkspace(context.Background(), run.ID, "token", "alice", "alice@example.com", "fix: 改文案")
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].WorkBranch != "feature/direct" || heads[0].Head == "" {
		t.Fatalf("heads = %+v", heads)
	}
	if got := testGit(t, remote, "rev-parse", "refs/heads/feature/direct"); got != heads[0].Head {
		t.Fatalf("remote %s != pushed %s", got, heads[0].Head)
	}
	if msg := testGit(t, remote, "log", "-1", "--format=%s|%an", "feature/direct"); msg != "fix: 改文案|alice" {
		t.Fatalf("commit = %q", msg)
	}
	again, err := e.PushConversationWorkspace(context.Background(), run.ID, "token", "alice", "alice@example.com", "noop")
	if err != nil || len(again) != 1 || again[0].Head != heads[0].Head {
		t.Fatalf("re-push without changes must report the same head: %+v %v", again, err)
	}
}
