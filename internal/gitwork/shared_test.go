package gitwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sharedBranchFixture(t *testing.T) (string, string) {
	t.Helper()
	remote, checkout := workBranchFixture(t)
	workBranchGit(t, remote, "branch", "preview", "main")
	return remote, checkout
}

func TestMergeWorkIntoSharedBranchPushesMergeCommit(t *testing.T) {
	remote, checkout := sharedBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "feature.txt")
	workBranchGit(t, checkout, "commit", "-qm", "work commit")
	workHead := workBranchGit(t, checkout, "rev-parse", "HEAD")
	base := workBranchGit(t, remote, "rev-parse", "refs/heads/preview")

	head, conflicts, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "preview")
	if err != nil || len(conflicts) > 0 {
		t.Fatalf("merge: head=%q conflicts=%v err=%v", head, conflicts, err)
	}
	if head == "" {
		t.Fatal("merge must report the pushed commit")
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/preview"); got != head {
		t.Fatalf("remote preview=%s want %s", got, head)
	}
	if subject := workBranchGit(t, remote, "log", "-1", "--format=%s", "preview"); subject != "Merge branch 'feature/existing' into preview" {
		t.Fatalf("merge subject = %q", subject)
	}
	parents := strings.Fields(workBranchGit(t, remote, "log", "-1", "--format=%P", "preview"))
	if len(parents) != 2 || parents[0] != base || parents[1] != workHead {
		t.Fatalf("merge parents = %v, want [%s %s]", parents, base, workHead)
	}
	if branch := workBranchGit(t, checkout, "branch", "--show-current"); branch != "feature/existing" {
		t.Fatalf("run checkout must stay on its work branch, got %q", branch)
	}
	if status := workBranchGit(t, checkout, "status", "--porcelain"); status != "" {
		t.Fatalf("run checkout must stay clean: %s", status)
	}
	if worktrees := workBranchGit(t, checkout, "worktree", "list"); strings.Count(worktrees, "\n") != 0 {
		t.Fatalf("temporary worktree must be removed: %s", worktrees)
	}
	if again, _, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "preview"); err != nil || again != "" {
		t.Fatalf("second merge must be a no-op: head=%q err=%v", again, err)
	}
}

func TestMergeWorkIntoSharedBranchReportsConflicts(t *testing.T) {
	remote, checkout := sharedBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("work side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "README.md")
	workBranchGit(t, checkout, "commit", "-qm", "work side")

	preview := filepath.Join(t.TempDir(), "preview")
	if out, err := exec.Command("git", "clone", "-q", "-b", "preview", remote, preview).CombinedOutput(); err != nil {
		t.Fatalf("clone preview: %v %s", err, out)
	}
	workBranchGit(t, preview, "config", "user.name", "Test")
	workBranchGit(t, preview, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(preview, "README.md"), []byte("preview side\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, preview, "commit", "-qam", "preview side")
	workBranchGit(t, preview, "push", "-q", "origin", "preview")
	before := workBranchGit(t, remote, "rev-parse", "refs/heads/preview")

	head, conflicts, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "preview")
	if err != nil {
		t.Fatal(err)
	}
	if head != "" {
		t.Fatalf("conflicted merge must not push, head=%q", head)
	}
	if len(conflicts) != 1 || conflicts[0] != "README.md" {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/preview"); got != before {
		t.Fatalf("remote preview moved on conflict: %s != %s", got, before)
	}
	if branch := workBranchGit(t, checkout, "branch", "--show-current"); branch != "feature/existing" {
		t.Fatalf("run checkout left the work branch: %q", branch)
	}
	if status := workBranchGit(t, checkout, "status", "--porcelain"); status != "" {
		t.Fatalf("conflict leaked into the run checkout: %s", status)
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("run checkout was left mid-merge")
	}
}

func TestMergeWorkIntoSharedBranchNeedsExistingTarget(t *testing.T) {
	_, checkout := sharedBranchFixture(t)
	if _, _, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "nope"); err == nil || !strings.Contains(err.Error(), "拉取共享分支") {
		t.Fatalf("missing remote branch must be reported: %v", err)
	}
	if _, _, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "feature/existing"); err == nil || !strings.Contains(err.Error(), "不能直推") {
		t.Fatalf("pushing a branch into itself must be refused: %v", err)
	}
}
