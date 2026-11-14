package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// newLocalPushCheckout creates a bare remote with one base commit on master
// and clones it to <root>/workspace/app, returning the workspace root, the
// checkout and the remote.
func newLocalPushCheckout(t *testing.T) (string, string, string) {
	t.Helper()
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
	workspace := filepath.Join(root, "workspace")
	checkout := filepath.Join(workspace, "app")
	if out, err := exec.Command("git", "clone", remote, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	return workspace, checkout, remote
}

func localPushContext() context.Context {
	return withGitIdentity(withGitToken(context.Background(), "token"), "alice", "alice@example.com")
}

func TestPushLocalConversationRepositorySkipsUnchangedCheckout(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	testGit(t, checkout, "checkout", "-b", "feature/local")
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/local", WorkspaceDir: "app"}

	entry, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "msg", "")
	if err != nil || pushed || entry != (domain.RunnerWorkBranchHead{}) {
		t.Fatalf("unchanged checkout must be skipped: entry=%+v pushed=%v err=%v", entry, pushed, err)
	}
	if out, _ := exec.Command("git", "-C", remote, "branch", "--list", "feature/*").CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("nothing may be pushed for an unchanged checkout: %s", out)
	}
}

func TestPushLocalConversationRepositoryPushesWorkBranch(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	testGit(t, checkout, "checkout", "-b", "feature/local")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/local", WorkspaceDir: "app"}

	entry, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "fix: 本地推送", "")
	if err != nil || !pushed {
		t.Fatalf("pushed=%v err=%v", pushed, err)
	}
	if entry.RepositoryID != "repo-1" || entry.Name != "app" || entry.WorkBranch != "feature/local" || entry.Head == "" || entry.SharedBranch != "" {
		t.Fatalf("entry = %+v", entry)
	}
	if got := testGit(t, remote, "rev-parse", "refs/heads/feature/local"); got != entry.Head {
		t.Fatalf("remote %s != pushed %s", got, entry.Head)
	}
	if msg := testGit(t, remote, "log", "-1", "--format=%s|%an", "feature/local"); msg != "fix: 本地推送|alice" {
		t.Fatalf("commit = %q", msg)
	}
}

func TestPushLocalConversationRepositoryRefusesDriftedBranch(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	testGit(t, checkout, "checkout", "-b", "feature/manual")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/recorded", WorkspaceDir: "app"}

	_, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "msg", "")
	if err == nil || pushed || !strings.HasPrefix(err.Error(), "app: ") || !strings.Contains(err.Error(), "feature/recorded") {
		t.Fatalf("drifted checkout must be refused with the repository name: pushed=%v err=%v", pushed, err)
	}
	if out, _ := exec.Command("git", "-C", remote, "branch", "--list", "feature/*").CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("nothing may be pushed from a drifted checkout: %s", out)
	}
}
