package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func selectedBranchTestRemote(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "init", "-b", "master")
	mustGit(t, source, "config", "user.name", "Test")
	mustGit(t, source, "config", "user.email", "test@example.com")
	gitCommitFile(t, source, "version.txt", "master baseline\n")
	mustGit(t, source, "checkout", "-b", "feature/continue")
	gitCommitFile(t, source, "version.txt", "existing feature work\n")
	mustGit(t, source, "checkout", "master")
	gitCommitFile(t, source, "master-only.txt", "new master change\n")
	remote := filepath.Join(root, "remote.git")
	mustGit(t, "", "clone", "--bare", source, remote)
	return source, remote
}

func TestClarifyWorkspaceUsesSelectedBranchAndRefreshesAcrossBranches(t *testing.T) {
	t.Parallel()
	source, remote := selectedBranchTestRemote(t)
	cache := filepath.Join(t.TempDir(), "checkout")
	repository := domain.Repository{ID: "repo", CloneURL: remote, DefaultBranch: "master"}
	ctx := context.Background()
	if _, err := PrepareClarifyWorkspace(ctx, cache, repository, "test-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareClarifyWorkspaceAtBranch(ctx, cache, repository, "feature/continue", "test-token"); err != nil {
		t.Fatal(err)
	}
	assertContent := func(want string) {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(cache, "version.txt"))
		if err != nil || string(content) != want {
			t.Fatalf("selected branch content = %q, err=%v; want %q", content, err, want)
		}
	}
	assertContent("existing feature work\n")
	if _, err := os.Stat(filepath.Join(cache, "master-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("selected branch unexpectedly includes newer master change: %v", err)
	}
	mustGit(t, source, "checkout", "feature/continue")
	gitCommitFile(t, source, "version.txt", "latest feature work\n")
	mustGit(t, source, "push", remote, "feature/continue")
	if _, err := PrepareClarifyWorkspaceAtBranch(ctx, cache, repository, "feature/continue", "test-token"); err != nil {
		t.Fatal(err)
	}
	assertContent("latest feature work\n")
	if _, err := PrepareClarifyWorkspaceAtBranch(ctx, cache, repository, "feature/missing", "test-token"); err == nil {
		t.Fatal("missing selected branch must fail instead of falling back to master")
	}
}

func TestPrepareRepositoryStartsWorkFromSelectedBranch(t *testing.T) {
	t.Parallel()
	_, remote := selectedBranchTestRemote(t)
	workspace := filepath.Join(t.TempDir(), "checkout")
	repository := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, DefaultBranch: "master", BaseBranch: "feature/continue", WorkBranch: "feature/new-requirement"}
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != "existing feature work\n" {
		t.Fatalf("work branch lost existing feature work: content=%q, err=%v", content, err)
	}
	branch, err := gitC(context.Background(), workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != repository.WorkBranch {
		t.Fatalf("work branch=%q, err=%v", branch, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "master-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("work branch unexpectedly includes newer master change: %v", err)
	}
	repository.BaseBranch = "feature/missing"
	if err := prepareRepository(context.Background(), filepath.Join(t.TempDir(), "missing"), repository); err == nil {
		t.Fatal("missing selected branch must fail instead of falling back to master")
	}
}

func TestClarifySelectedBranchRejectsTagOnlyRef(t *testing.T) {
	t.Parallel()
	source, remote := selectedBranchTestRemote(t)
	mustGit(t, source, "tag", "release-v1")
	mustGit(t, source, "push", remote, "refs/tags/release-v1")
	repository := domain.Repository{ID: "repo", CloneURL: remote, DefaultBranch: "master"}
	if _, err := PrepareClarifyWorkspaceAtBranch(context.Background(), filepath.Join(t.TempDir(), "checkout"), repository, "release-v1", "test-token"); err == nil {
		t.Fatal("tag-only ref must not be accepted as a selected branch")
	}
}

func TestPrepareRepositoryRejectsCustomBranchForNonGitSource(t *testing.T) {
	t.Parallel()
	repository := domain.RepositoryContext{RepositoryID: "repo", LocalPath: t.TempDir(), DefaultBranch: "master", BaseBranch: "feature/continue"}
	if err := prepareRepository(context.Background(), t.TempDir(), repository); err == nil || !strings.Contains(err.Error(), "not a Git repository") {
		t.Fatalf("non-Git source silently ignored custom branch: %v", err)
	}
}

func TestPrepareRepositoryUsesConfiguredDefaultWhenBaseBranchIsEmpty(t *testing.T) {
	t.Parallel()
	_, remote := selectedBranchTestRemote(t)
	workspace := filepath.Join(t.TempDir(), "checkout")
	repository := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, DefaultBranch: "feature/continue", WorkBranch: "feature/new-requirement"}
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != "existing feature work\n" {
		t.Fatalf("empty baseline ignored configured main branch: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "master-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("empty baseline silently used master: %v", err)
	}
	// A resumed checkout must retain the same configured fallback.
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	if repositoryBaseBranch(repository) != "feature/continue" {
		t.Fatal("resume fallback lost configured main branch")
	}
}

// A Gongfeng repository created a minute ago has no commits. Opening a
// workspace on it must publish the base branch instead of failing to clone,
// so the first file can be written and delivered.
func TestPrepareRepositorySeedsAnEmptyRemote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, base, integrate string }{
		{name: "default branch", base: ""},
		{name: "integration branch", base: "develop", integrate: "develop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := filepath.Join(t.TempDir(), "empty.git")
			mustGit(t, "", "init", "--bare", "-q", remote)
			workspace := filepath.Join(t.TempDir(), "checkout")
			repository := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, DefaultBranch: "master", BaseBranch: tc.base, IntegrateBranch: tc.integrate, WorkBranch: "feature/first"}
			if err := prepareRepository(context.Background(), workspace, repository); err != nil {
				t.Fatal(err)
			}
			branch, err := gitC(context.Background(), workspace, "branch", "--show-current")
			if err != nil || strings.TrimSpace(branch) != "feature/first" {
				t.Fatalf("work branch = %q, %v", branch, err)
			}
			heads, err := gitC(context.Background(), workspace, "ls-remote", "--heads", "origin")
			if err != nil || !strings.Contains(heads, "refs/heads/master") {
				t.Fatalf("default branch not published: %q, %v", heads, err)
			}
			if tc.integrate != "" && !strings.Contains(heads, "refs/heads/"+tc.integrate) {
				t.Fatalf("integration branch not published: %q", heads)
			}
			if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# OKR\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			mustGit(t, workspace, "add", "README.md")
			mustGit(t, workspace, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "readme")
			mustGit(t, workspace, "push", "-q", "origin", "feature/first")
		})
	}
	// A remote that has branches but not the requested one still fails.
	remote := filepath.Join(t.TempDir(), "busy.git")
	mustGit(t, "", "init", "--bare", "-q", remote)
	source := t.TempDir()
	mustGit(t, source, "init", "-q")
	mustGit(t, source, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "x")
	mustGit(t, source, "push", "-q", remote, "HEAD:refs/heads/main")
	if err := prepareRepository(context.Background(), filepath.Join(t.TempDir(), "c"), domain.RepositoryContext{RepositoryID: "r", CloneURL: remote, DefaultBranch: "master", WorkBranch: "w"}); err == nil {
		t.Fatal("a non-empty remote without the base branch was seeded")
	}
}

func TestClarifyGitLabLocalPathClonesWithoutHostCredential(t *testing.T) {
	t.Parallel()
	source, _ := selectedBranchTestRemote(t)
	cache := filepath.Join(t.TempDir(), "checkout")
	repository := domain.Repository{ID: "gitlab-local", Provider: "gitlab", CloneURL: source, DefaultBranch: "master"}
	if _, err := PrepareClarifyWorkspace(context.Background(), cache, repository, ""); err != nil {
		t.Fatalf("GitLab repository with a local path must be analyzable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "master-only.txt")); err != nil {
		t.Fatalf("local GitLab checkout missing master content: %v", err)
	}
}
