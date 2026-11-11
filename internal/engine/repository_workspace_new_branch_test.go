package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// newBranchRemote builds a bare remote whose master branch has one commit and
// that also carries feature/existing, so a create-mode workspace can be asked
// to reuse a name that is already taken.
func newBranchRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "init", "-q", "-b", "master")
	mustGit(t, source, "config", "user.email", "test@example.com")
	mustGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "add", ".")
	mustGit(t, source, "commit", "-qm", "base")
	mustGit(t, source, "branch", "feature/existing")
	mustGit(t, "", "clone", "--bare", source, remote)
	return remote
}

func TestPrepareRepositoryCreatesCallerNamedBranchOnly(t *testing.T) {
	ctx := context.Background()
	remote := newBranchRemote(t)
	base := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, BaseBranch: "master", DefaultBranch: "master"}

	create := base
	create.WorkBranch = "feature/caller-named"
	create.CreateNewWorkBranch = true
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := prepareRepository(ctx, workspace, create); err != nil {
		t.Fatalf("create a new work branch: %v", err)
	}
	if branch, _ := gitC(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(branch) != create.WorkBranch {
		t.Fatalf("work branch = %q, want %q", branch, create.WorkBranch)
	}
	if _, ok := gitRevParse(ctx, workspace, "origin/"+create.WorkBranch); ok {
		t.Fatal("a new work branch must not be published by the checkout")
	}

	// Reusing a name that already exists remotely is an error, never a silent
	// continuation on someone else's branch.
	existing := base
	existing.WorkBranch = "feature/existing"
	existing.CreateNewWorkBranch = true
	if err := prepareRepository(ctx, filepath.Join(t.TempDir(), "taken"), existing); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("existing remote branch accepted as new: %v", err)
	}

	// Shared baselines are never a work branch, even a freshly named one.
	shared := base
	shared.WorkBranch = "master"
	shared.CreateNewWorkBranch = true
	if err := prepareRepository(ctx, filepath.Join(t.TempDir(), "shared"), shared); err == nil {
		t.Fatal("shared branch accepted as a new work branch")
	}

	// Continuing and creating are mutually exclusive.
	both := existing
	both.RequireExistingWorkBranch = true
	if err := prepareRepository(ctx, filepath.Join(t.TempDir(), "both"), both); err == nil {
		t.Fatal("continue+create accepted")
	}

	// Reusing the checkout keeps the branch this workspace created; the remote
	// copy (if the workspace was pushed) is ours and is not treated as a clash.
	if err := prepareRepository(ctx, workspace, create); err != nil {
		t.Fatalf("resume a created work branch: %v", err)
	}
}

// #80: a cached workspace that has only checked out the base must apply the
// same new-branch contract as a fresh clone when it is resumed.
func TestResumeRejectsRemoteBranchCreatedElsewhere(t *testing.T) {
	ctx := context.Background()
	remote := newBranchRemote(t)
	base := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, BaseBranch: "master", DefaultBranch: "master"}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := prepareRepository(ctx, workspace, base); err != nil {
		t.Fatalf("prepare base-only workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("local work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Someone else publishes the name after the workspace was cached.
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, "", "clone", "-q", remote, other)
	mustGit(t, other, "config", "user.email", "other@example.com")
	mustGit(t, other, "config", "user.name", "Other")
	mustGit(t, other, "checkout", "-q", "-b", "feature/later")
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("not ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, other, "add", ".")
	mustGit(t, other, "commit", "-qm", "unrelated work")
	mustGit(t, other, "push", "-q", "origin", "feature/later")

	create := base
	create.WorkBranch = "feature/later"
	create.CreateNewWorkBranch = true
	if err := prepareRepository(ctx, workspace, create); err == nil || !strings.Contains(err.Error(), "远端已存在分支") {
		t.Fatalf("resume attached to a remote branch created elsewhere: %v", err)
	}
	if branch, _ := gitC(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(branch) == create.WorkBranch {
		t.Fatal("resume switched to the colliding branch")
	}
	if _, err := os.Stat(filepath.Join(workspace, "theirs.txt")); err == nil {
		t.Fatal("resume checked out someone else's files")
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "notes.txt")); err != nil || string(body) != "local work\n" {
		t.Fatalf("resume discarded local work: %q %v", body, err)
	}

	// A fresh name still works on resume, and the branch this workspace then
	// created and pushed is resumed rather than reported as a collision.
	fresh := base
	fresh.WorkBranch = "feature/mine"
	fresh.CreateNewWorkBranch = true
	if err := prepareRepository(ctx, workspace, fresh); err != nil {
		t.Fatalf("resume creating a fresh branch: %v", err)
	}
	mustGit(t, workspace, "config", "user.email", "test@example.com")
	mustGit(t, workspace, "config", "user.name", "Test")
	mustGit(t, workspace, "add", "notes.txt")
	mustGit(t, workspace, "commit", "-qm", "mine")
	mustGit(t, workspace, "push", "-q", "origin", "feature/mine")
	if err := prepareRepository(ctx, workspace, fresh); err != nil {
		t.Fatalf("resume of the branch this workspace created: %v", err)
	}
	if branch, _ := gitC(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(branch) != fresh.WorkBranch {
		t.Fatalf("work branch = %q, want %q", branch, fresh.WorkBranch)
	}

	// Continuing an existing branch is unchanged.
	continued := base
	continued.WorkBranch = "feature/later"
	continued.RequireExistingWorkBranch = true
	if err := prepareRepository(ctx, workspace, continued); err != nil {
		t.Fatalf("continue existing branch on resume: %v", err)
	}
	if branch, _ := gitC(ctx, workspace, "branch", "--show-current"); strings.TrimSpace(branch) != continued.WorkBranch {
		t.Fatalf("continued branch = %q", branch)
	}
}
