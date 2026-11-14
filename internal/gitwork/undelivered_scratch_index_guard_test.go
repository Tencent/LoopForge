package gitwork

import (
	"os"
	"path/filepath"
	"testing"
)

// A checkout whose index file is missing (e.g. removed by a tool or never
// written) must still be checked against HEAD's tree: the scratch index is
// built with read-tree instead of copying an empty file, and the check must
// not create the checkout's own index as a side effect.
func TestUndeliveredChangesWithoutIndexFileUsesHeadTree(t *testing.T) {
	_, checkout := workBranchFixture(t)
	index := filepath.Join(checkout, ".git", "index")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	writeUndeliveredFile(t, checkout, "README.md", "baseline\nedited\n")
	writeUndeliveredFile(t, checkout, "src/new.go", "package src\n")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "M README.md,A src/new.go" {
		t.Fatalf("files=%s", got)
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("checkout index must stay absent after the read-only check, stat err=%v", err)
	}
}

// The scratch index copy is temporary: it is removed after the check, both
// when changes are reported and when the diff is empty.
func TestUndeliveredChangesRemovesScratchIndex(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	_, checkout := workBranchFixture(t)

	if files, err := UndeliveredChanges(checkout); err != nil || len(files) != 0 {
		t.Fatalf("clean checkout: files=%v err=%v", files, err)
	}
	assertNoScratchIndex(t, tmp)

	writeUndeliveredFile(t, checkout, "src/new.go", "package src\n")
	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "A src/new.go" {
		t.Fatalf("files=%s", got)
	}
	assertNoScratchIndex(t, tmp)
}

// In a linked worktree `.git` is a file and the index lives under
// .git/worktrees/<name>; the scratch copy must come from that index, and
// neither the worktree's nor the main checkout's `git status` may change.
func TestUndeliveredChangesInLinkedWorktree(t *testing.T) {
	_, checkout := workBranchFixture(t)
	linked := filepath.Join(filepath.Dir(checkout), "linked")
	workBranchGit(t, checkout, "worktree", "add", "-q", "-b", "linked", linked, "feature/existing")
	writeUndeliveredFile(t, linked, "README.md", "baseline\nedited in worktree\n")
	writeUndeliveredFile(t, linked, "src/new.go", "package src\n")
	linkedBefore := workBranchGit(t, linked, "status", "--porcelain")
	mainBefore := workBranchGit(t, checkout, "status", "--porcelain")

	files, err := UndeliveredChanges(linked)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "M README.md,A src/new.go" {
		t.Fatalf("files=%s", got)
	}
	if after := workBranchGit(t, linked, "status", "--porcelain"); after != linkedBefore {
		t.Fatalf("worktree status changed:\nbefore:\n%s\nafter:\n%s", linkedBefore, after)
	}
	if after := workBranchGit(t, checkout, "status", "--porcelain"); after != mainBefore {
		t.Fatalf("main checkout status changed:\nbefore:\n%s\nafter:\n%s", mainBefore, after)
	}
}

func assertNoScratchIndex(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, "loopforge-undelivered-index-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("scratch index left behind: %v", left)
	}
}

// The scratch index starts as a copy of the checkout's index, so files the
// Agent staged explicitly are still compared, including a force-added file
// that an ignore rule would otherwise hide from the untracked listing.
func TestUndeliveredChangesKeepsForceAddedIgnoredFiles(t *testing.T) {
	_, checkout := workBranchFixture(t)
	writeUndeliveredFile(t, checkout, ".git/info/exclude", "*.local\n")
	writeUndeliveredFile(t, checkout, "config.local", "staged on purpose\n")
	workBranchGit(t, checkout, "add", "-f", "config.local")

	files, err := UndeliveredChanges(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if got := undeliveredPaths(files); got != "A config.local" {
		t.Fatalf("files=%s, want the force-added file", got)
	}
}
