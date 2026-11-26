package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeReturnsFailureWhenGitNeverStartsMerge(t *testing.T) {
	for _, mode := range []string{"remote", "integration"} {
		t.Run(mode, func(t *testing.T) {
			_, checkout := workBranchFixture(t)
			workBranchGit(t, checkout, "checkout", "--orphan", "independent")
			workBranchGit(t, checkout, "rm", "-rf", "--", ".")
			if err := os.WriteFile(filepath.Join(checkout, "other.txt"), []byte("independent\n"), 0600); err != nil {
				t.Fatal(err)
			}
			workBranchGit(t, checkout, "add", "other.txt")
			workBranchGit(t, checkout, "commit", "-qm", "independent root")
			workBranchGit(t, checkout, "push", "-q", "origin", "independent")
			workBranchGit(t, checkout, "checkout", "feature/existing")
			before := workBranchGit(t, checkout, "rev-parse", "HEAD")

			var conflicts []string
			var err error
			ctx := context.Background()
			if mode == "remote" {
				conflicts, err = MergeLatestRemoteBranch(ctx, checkout, "independent")
			} else {
				conflicts, err = MergeLatestIntegration(ctx, checkout, "feature/existing", "independent", "test")
			}
			if err == nil || len(conflicts) != 0 {
				t.Fatalf("want merge failure without conflicts, got conflicts=%v err=%v", conflicts, err)
			}
			if after := workBranchGit(t, checkout, "rev-parse", "HEAD"); after != before {
				t.Fatalf("failed merge changed HEAD from %s to %s", before, after)
			}
		})
	}
}
