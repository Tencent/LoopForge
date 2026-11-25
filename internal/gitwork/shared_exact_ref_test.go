package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedBranchMergeIgnoresSameNamedTag(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	const workBranch = "feature/existing"
	workBranchGit(t, checkout, "tag", workBranch, "HEAD")
	if err := os.WriteFile(filepath.Join(checkout, "deliver.txt"), []byte("delivery\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "deliver.txt")
	workBranchGit(t, checkout, "commit", "-qm", "deliver")
	head, conflicts, err := MergeWorkIntoSharedBranch(context.Background(), checkout, workBranch, "main")
	if err != nil || len(conflicts) != 0 || head == "" {
		t.Fatalf("want pushed work branch merge, got head=%q conflicts=%v err=%v", head, conflicts, err)
	}
	if content := workBranchGit(t, remote, "show", "refs/heads/main:deliver.txt"); content != "delivery" {
		t.Fatalf("shared branch content = %q", content)
	}
}
