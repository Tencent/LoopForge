package runner

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
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
