package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitStagedCompletesMergeResolvedToCurrentTree(t *testing.T) {
	_, checkout := workBranchFixture(t)
	path := filepath.Join(checkout, "README.md")
	for _, branch := range []string{"incoming", "feature/existing"} {
		if branch == "incoming" {
			workBranchGit(t, checkout, "checkout", "-b", branch)
		} else {
			workBranchGit(t, checkout, "checkout", branch)
		}
		if err := os.WriteFile(path, []byte(branch+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		workBranchGit(t, checkout, "commit", "-qam", branch)
	}
	ctx := context.Background()
	if _, err := C(ctx, checkout, "merge", "incoming"); err == nil {
		t.Fatal("expected merge conflict")
	}
	workBranchGit(t, checkout, "checkout", "--ours", "--", "README.md")
	workBranchGit(t, checkout, "add", "README.md")
	workBranchGit(t, checkout, "diff", "--cached", "--quiet")
	if err := CommitStaged(ctx, checkout, "finish merge"); err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(workBranchGit(t, checkout, "show", "-s", "--format=%P", "HEAD"))
	if len(parents) != 2 {
		t.Fatalf("expected a merge commit, got %d parents", len(parents))
	}
}
