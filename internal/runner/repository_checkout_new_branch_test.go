package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func TestCloneRepositoryCreatesOnlyNewWorkBranch(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	repo := &domain.RunnerRepository{RepositoryID: "repo", CloneURL: remote, Ref: "master", WorkBranch: "feature/caller", CreateNewWorkBranch: true}
	workspace := t.TempDir()
	if err := cloneRepository(context.Background(), workspace, repo); err != nil {
		t.Fatalf("create new work branch: %v", err)
	}
	if branch := runGitT(t, workspace, "branch", "--show-current"); branch != "feature/caller" {
		t.Fatalf("work branch = %q", branch)
	}
	// Publish it, then ask for the same name as a new branch: the runner must
	// refuse instead of attaching to the branch it just pushed.
	runGitT(t, workspace, "push", "-q", "origin", "feature/caller")
	taken := &domain.RunnerRepository{RepositoryID: "repo", CloneURL: remote, Ref: "master", WorkBranch: "feature/caller", CreateNewWorkBranch: true}
	if err := cloneRepository(context.Background(), t.TempDir(), taken); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("existing remote branch accepted as new: %v", err)
	}
}

// The runner uses its cache and produces an independent checkout. A branch
// the repository lacks still fails after the direct clone fallback.
func TestCloneRepositoryStartsFromRepositoryCache(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	root := t.TempDir()
	ctx := gitwork.WithRepositoryCache(context.Background(), root)
	repo := &domain.RunnerRepository{RepositoryID: "repo", CloneURL: remote, Ref: "master", WorkBranch: "feature/cached", CreateNewWorkBranch: true}
	workspace := t.TempDir()
	if err := cloneRepository(ctx, workspace, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "repo.git", "objects")); err != nil {
		t.Fatalf("host cache was not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("checkout depends on the host cache: %v", err)
	}
	if origin := runGitT(t, workspace, "remote", "get-url", "origin"); origin != remote {
		t.Fatalf("origin = %q, want %q", origin, remote)
	}
	if branch := runGitT(t, workspace, "branch", "--show-current"); branch != "feature/cached" {
		t.Fatalf("work branch = %q", branch)
	}
	missing := &domain.RunnerRepository{RepositoryID: "repo", CloneURL: remote, Ref: "no-such-branch"}
	if err := cloneRepository(ctx, t.TempDir(), missing); err == nil {
		t.Fatal("missing branch cloned")
	}
}
