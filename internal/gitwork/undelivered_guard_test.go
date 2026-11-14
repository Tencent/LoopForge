package gitwork

import (
	"fmt"
	"os/exec"
	"testing"
)

// Work already pushed to some remote branch is delivered; only the commits
// after the closest remote ancestor are reported. A remote branch that
// diverged from HEAD is never used as the comparison base, even when it
// sorts first and is just as "close" by commit count.
func TestUndeliveredChangesComparesAgainstClosestRemoteAncestor(t *testing.T) {
	_, checkout := workBranchFixture(t)
	writeUndeliveredFile(t, checkout, "pushed.txt", "already on the remote\n")
	workBranchGit(t, checkout, "add", "pushed.txt")
	workBranchGit(t, checkout, "commit", "-qm", "pushed")
	workBranchGit(t, checkout, "push", "-q", "origin", "HEAD:refs/heads/feature/existing")

	// A sibling of the next local commit, pushed under a name that sorts
	// before feature/existing.
	workBranchGit(t, checkout, "checkout", "-q", "-b", "diverged")
	writeUndeliveredFile(t, checkout, "diverged.txt", "someone else's branch\n")
	workBranchGit(t, checkout, "add", "diverged.txt")
	workBranchGit(t, checkout, "commit", "-qm", "diverged")
	workBranchGit(t, checkout, "push", "-q", "origin", "HEAD:refs/heads/aaa-diverged")
	workBranchGit(t, checkout, "checkout", "-q", "feature/existing")

	writeUndeliveredFile(t, checkout, "local.txt", "committed only here\n")
	workBranchGit(t, checkout, "add", "local.txt")
	workBranchGit(t, checkout, "commit", "-qm", "local only")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "A local.txt" {
		t.Fatalf("files=%s, want only the unpushed commit", got)
	}
}

// A freshly initialised checkout without any commit has nothing to compare
// against; it must not error or report the untracked files.
func TestUndeliveredChangesUnbornHead(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	writeUndeliveredFile(t, dir, "draft.go", "package draft\n")
	files, err := UndeliveredChanges(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("unborn head: files=%v err=%v", files, err)
	}
}

// Without a remote and without a recorded baseline, local commits cannot be
// told apart from history; only uncommitted edits are reported.
func TestUndeliveredChangesWithoutRemoteOrBaselineReportsOnlyEdits(t *testing.T) {
	_, checkout := workBranchFixture(t)
	workBranchGit(t, checkout, "remote", "remove", "origin")
	writeUndeliveredFile(t, checkout, "committed.txt", "local history\n")
	workBranchGit(t, checkout, "add", "committed.txt")
	workBranchGit(t, checkout, "commit", "-qm", "local")
	writeUndeliveredFile(t, checkout, "README.md", "baseline\nedited\n")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "M README.md" {
		t.Fatalf("files=%s, want only the uncommitted edit", got)
	}
}

// The check is read-only: intent-adding untracked files for the diff must
// happen in a scratch index, so `git status` of the checkout (and therefore
// the workspace snapshot taken afterwards) is unchanged.
func TestUndeliveredChangesLeavesCheckoutIndexUntouched(t *testing.T) {
	_, checkout := workBranchFixture(t)
	writeUndeliveredFile(t, checkout, "README.md", "baseline\nedited\n")
	writeUndeliveredFile(t, checkout, "src/new.go", "package src\n")
	before := workBranchGit(t, checkout, "status", "--porcelain")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "M README.md,A src/new.go" {
		t.Fatalf("files=%s", got)
	}
	if after := workBranchGit(t, checkout, "status", "--porcelain"); after != before {
		t.Fatalf("git status changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Many remote refs (ancestors, diverged siblings and unrelated tips) still
// resolve to the closest delivered commit.
func TestUndeliveredChangesWithManyRemoteRefs(t *testing.T) {
	_, checkout := workBranchFixture(t)
	writeUndeliveredFile(t, checkout, "pushed.txt", "on the remote\n")
	workBranchGit(t, checkout, "add", "pushed.txt")
	workBranchGit(t, checkout, "commit", "-qm", "pushed")
	workBranchGit(t, checkout, "push", "-q", "origin", "HEAD:refs/heads/feature/existing")
	for i := 0; i < 30; i++ {
		workBranchGit(t, checkout, "push", "-q", "origin", fmt.Sprintf("HEAD~1:refs/heads/old/%02d", i))
	}
	workBranchGit(t, checkout, "checkout", "-q", "-b", "sibling")
	for i := 0; i < 5; i++ {
		writeUndeliveredFile(t, checkout, fmt.Sprintf("sibling%d.txt", i), "x\n")
		workBranchGit(t, checkout, "add", ".")
		workBranchGit(t, checkout, "commit", "-qm", "sibling")
		workBranchGit(t, checkout, "push", "-q", "origin", fmt.Sprintf("HEAD:refs/heads/sibling/%d", i))
	}
	workBranchGit(t, checkout, "checkout", "-q", "feature/existing")
	workBranchGit(t, checkout, "fetch", "-q", "origin")
	writeUndeliveredFile(t, checkout, "local.txt", "only here\n")
	workBranchGit(t, checkout, "add", "local.txt")
	workBranchGit(t, checkout, "commit", "-qm", "local only")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "A local.txt" {
		t.Fatalf("files=%s, want only the unpushed commit", got)
	}
}
