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

func localWorktreeRequest(profile domain.SandboxProfile, repos ...domain.RunnerRepository) domain.RunnerWorkspaceRequest {
	profile.Driver = domain.SandboxDriverLocal
	return domain.RunnerWorkspaceRequest{
		Sandbox:               &domain.RunnerSandboxSpec{Profile: profile},
		PreparationInvocation: &domain.RunnerInvocation{Repositories: repos},
	}
}

// A configured checkout gets a worktree on the work branch; the checkout's own
// branch and uncommitted files stay as the owner left them.
func TestLocalWorktreeUsesConfiguredCheckoutWithoutTouchingIt(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	main := filepath.Join(t.TempDir(), "service")
	runGitT(t, filepath.Dir(main), "clone", "-q", remote, main)
	if err := os.WriteFile(filepath.Join(main, "README.md"), []byte("owner's unsaved edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	workspace := filepath.Join(runDir, "workspace")
	repo := domain.RunnerRepository{RepositoryID: "repo-1", Name: "service", CloneURL: remote, Dir: "service", Ref: "master", WorkBranch: "feature/local"}
	request := localWorktreeRequest(domain.SandboxProfile{RepositoryPaths: map[string]string{"repo-1": main}}, repo)
	if err := checkoutWorkspaceRepositories(context.Background(), workspace, request, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "service")
	if got := runGitT(t, target, "branch", "--show-current"); got != "feature/local" {
		t.Fatalf("worktree branch = %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(target, "README.md")); string(data) != "baseline\n" {
		t.Fatalf("worktree content = %q", data)
	}
	if info, err := os.Lstat(filepath.Join(target, ".git")); err != nil || info.IsDir() {
		t.Fatalf("workspace is not a worktree: %v", err)
	}
	if got := runGitT(t, main, "branch", "--show-current"); got != "master" {
		t.Fatalf("main checkout switched to %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(main, "README.md")); string(data) != "owner's unsaved edit\n" {
		t.Fatalf("main checkout edit lost: %q", data)
	}
	if err := os.WriteFile(filepath.Join(target, "work.txt"), []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkoutWorkspaceRepositories(context.Background(), workspace, request, true); err != nil {
		t.Fatalf("reopening an existing worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "work.txt")); err != nil {
		t.Fatalf("reopening discarded work: %v", err)
	}

	// Cleanup gets the run directory; the worktree sits at <run>/workspace/<repo>.
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
}

// Deleting a conversation workspace runs the run cleanup task, which must
// unregister the worktrees it removes from <run>/workspace/<repo>, also when
// the machine's git refuses implicit bare repositories.
func TestRunCleanupUnregistersLocalWorktrees(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
	t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	remote := bareRemoteWithBaseline(t)
	main := filepath.Join(t.TempDir(), "service")
	runGitT(t, filepath.Dir(main), "clone", "-q", remote, main)
	root := t.TempDir()
	repo := domain.RunnerRepository{RepositoryID: "repo-1", Name: "service", CloneURL: remote, Dir: "service", Ref: "master", WorkBranch: "feature/cleanup"}
	request := localWorktreeRequest(domain.SandboxProfile{RepositoryPaths: map[string]string{"repo-1": main}}, repo)
	if err := checkoutWorkspaceRepositories(context.Background(), filepath.Join(root, "runs", "run-1", "workspace"), request, false); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}, client: NewClient(server.URL), runSandboxes: map[string]*runSandbox{}}
	result := d.executeRunCleanup(context.Background(), domain.RunnerTask{ID: "cleanup", RunID: "run-1", Workspace: "runs/run-1", Sandbox: request.Sandbox})
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("result=%+v", result)
	}
	if list := runGitT(t, main, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("removed worktree still registered:\n%s", list)
	}
}

// A worktree directory removed without a prune (an interrupted cleanup) must
// not keep its branch from the next workspace.
func TestLocalWorktreeReusesABranchLeftByAMissingWorktree(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	main := filepath.Join(t.TempDir(), "service")
	runGitT(t, filepath.Dir(main), "clone", "-q", remote, main)
	repo := domain.RunnerRepository{RepositoryID: "repo-1", Name: "service", CloneURL: remote, Ref: "master", WorkBranch: "feature/again"}
	request := localWorktreeRequest(domain.SandboxProfile{RepositoryPaths: map[string]string{"repo-1": main}}, repo)
	first := t.TempDir()
	if err := checkoutWorkspaceRepositories(context.Background(), first, request, false); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	if err := checkoutWorkspaceRepositories(context.Background(), second, request, false); err != nil {
		t.Fatalf("branch held by a removed worktree: %v", err)
	}
	if got := runGitT(t, second, "branch", "--show-current"); got != "feature/again" {
		t.Fatalf("worktree branch = %q", got)
	}
}

// An unconfigured repository is cloned once into the base directory, and later
// workspaces reuse that clone.
func TestLocalWorktreeClonesUnconfiguredRepositoryIntoBaseDir(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	base := t.TempDir()
	repo := domain.RunnerRepository{RepositoryID: "repo-1", Name: "service", CloneURL: remote, Ref: "master"}
	request := localWorktreeRequest(domain.SandboxProfile{BaseDir: base}, repo)
	first, second := t.TempDir(), t.TempDir()
	for _, workspace := range []string{first, second} {
		if err := checkoutWorkspaceRepositories(context.Background(), workspace, request, false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
			t.Fatalf("worktree missing files: %v", err)
		}
	}
	main := filepath.Join(base, "service")
	if list := runGitT(t, main, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 3 || strings.Count(list, "detached") != 2 {
		t.Fatalf("both workspaces must share one clone:\n%s", list)
	}
}

func TestLocalWorktreeBranchRules(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	seedIntegrationBranch(t, remote, "existing", "other.txt", "remote work\n")
	main := filepath.Join(t.TempDir(), "service")
	runGitT(t, filepath.Dir(main), "clone", "-q", remote, main)
	profile := domain.SandboxProfile{RepositoryPaths: map[string]string{"repo-1": main}}
	checkout := func(repo domain.RunnerRepository) (string, error) {
		repo.RepositoryID, repo.Name, repo.CloneURL, repo.Ref = "repo-1", "service", remote, "master"
		workspace := t.TempDir()
		return workspace, checkoutWorkspaceRepositories(context.Background(), workspace, localWorktreeRequest(profile, repo), false)
	}

	workspace, err := checkout(domain.RunnerRepository{WorkBranch: "existing", RequireExistingWorkBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "other.txt")); err != nil {
		t.Fatalf("continuing a remote branch must check it out: %v", err)
	}
	if upstream := runGitT(t, workspace, "rev-parse", "--abbrev-ref", "@{upstream}"); upstream != "origin/existing" {
		t.Fatalf("upstream = %q", upstream)
	}
	for name, repo := range map[string]domain.RunnerRepository{
		"new branch already on remote": {WorkBranch: "existing-2", CreateNewWorkBranch: true},
		"missing branch to continue":   {WorkBranch: "nowhere", RequireExistingWorkBranch: true},
		"option-like branch":           {WorkBranch: "-x"},
		"invalid branch":               {WorkBranch: "bad..name"},
	} {
		if name == "new branch already on remote" {
			seedIntegrationBranch(t, remote, "existing-2", "x.txt", "x\n")
		}
		if _, err := checkout(repo); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	missing := domain.SandboxProfile{RepositoryPaths: map[string]string{"repo-1": filepath.Join(t.TempDir(), "absent")}}
	err = checkoutWorkspaceRepositories(context.Background(), t.TempDir(), localWorktreeRequest(missing, domain.RunnerRepository{RepositoryID: "repo-1", Name: "service", CloneURL: remote}), false)
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("a configured path that does not exist must not be cloned: %v", err)
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
