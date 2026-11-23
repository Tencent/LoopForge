package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// addAgentWorktree adds a worktree the way an agent in a local workspace
// does: from the machine's main checkout into a first-level directory.
func addAgentWorktree(t *testing.T, main, target, branch string) {
	t.Helper()
	runGitT(t, main, "fetch", "-q", "origin")
	runGitT(t, main, "worktree", "add", "-q", "-b", branch, target, "origin/master")
}

// Cleanup gets the run directory and finds worktrees at
// <run>/workspace/<repo>; the main checkout keeps the branch and the owner's
// own edits.
func TestLocalWorktreeCleanupKeepsBranchAndMainCheckout(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	main := filepath.Join(t.TempDir(), "service")
	runGitT(t, filepath.Dir(main), "clone", "-q", remote, main)
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("owner's unsaved edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	addAgentWorktree(t, main, filepath.Join(workspace, "service"), "feature/local")

	common := localWorktreeCommonDirs(context.Background(), runDir)
	if len(common) != 1 || filepath.Dir(common[0]) != resolvedPath(t, main) {
		t.Fatalf("common dirs = %v", common)
	}
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatal(err)
	}
	pruneLocalWorktrees(context.Background(), common)
	if list := runGitT(t, main, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("removed worktree still registered:\n%s", list)
	}
	if got := runGitT(t, main, "branch", "--list", "feature/local"); got == "" {
		t.Fatal("deleting the workspace must keep its branch")
	}
	if data, _ := os.ReadFile(filepath.Join(main, "README.md")); string(data) != "owner's unsaved edit\n" {
		t.Fatalf("main checkout edit lost: %q", data)
	}
}

// Deleting a conversation workspace runs the run cleanup task, which must
// unregister the worktrees it removes from <run>/workspace/<repo>, also when
// the machine's git refuses implicit bare repositories.
func TestRunCleanupUnregistersLocalWorktrees(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	main := filepath.Join(t.TempDir(), "service")
	runGitT(t, filepath.Dir(main), "clone", "-q", remote, main)
	root := t.TempDir()
	addAgentWorktree(t, main, filepath.Join(root, "runs", "run-1", "workspace", "service"), "feature/cleanup")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}, client: NewClient(server.URL), runSandboxes: map[string]*runSandbox{}}
	sandbox := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: domain.SandboxDriverLocal}}
	result := d.executeRunCleanup(context.Background(), domain.RunnerTask{ID: "cleanup", RunID: "run-1", Workspace: "runs/run-1", Sandbox: sandbox})
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("result=%+v", result)
	}
	if list := runGitT(t, main, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("removed worktree still registered:\n%s", list)
	}
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
