package gitwork

import (
	"context"
	"strings"
	"testing"
)

func TestVerifyOffWorkBranch(t *testing.T) {
	ctx := context.Background()
	t.Run("generated branch is skipped", func(t *testing.T) {
		_, checkout := workBranchFixture(t)
		if err := verifyOffWorkBranch(ctx, checkout, "feature/missing", false); err != nil {
			t.Fatalf("non-strict check must pass: %v", err)
		}
	})
	t.Run("strict rejects missing remote", func(t *testing.T) {
		_, checkout := workBranchFixture(t)
		err := verifyOffWorkBranch(ctx, checkout, "feature/missing", true)
		if err == nil || !strings.Contains(err.Error(), "无法读取已有远端分支 feature/missing") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("strict rejects other checkout", func(t *testing.T) {
		_, checkout := workBranchFixture(t)
		workBranchGit(t, checkout, "checkout", "-q", "main")
		err := verifyOffWorkBranch(ctx, checkout, "feature/existing", true)
		if err == nil || err.Error() != "工作区未检出已有工作分支 feature/existing" {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("strict accepts checked-out branch", func(t *testing.T) {
		_, checkout := workBranchFixture(t)
		if err := verifyOffWorkBranch(ctx, checkout, "feature/existing", true); err != nil {
			t.Fatalf("err=%v", err)
		}
	})
}

// Push and pull share the off-branch check, so both must reject an explicit
// branch that is not checked out and both must skip a generated one.
func TestWorkBranchSyncOffBranchParity(t *testing.T) {
	ctx := context.Background()
	_, checkout := workBranchFixture(t)
	workBranchGit(t, checkout, "checkout", "-q", "main")
	want := "工作区未检出已有工作分支 feature/existing"
	if _, err := PushExistingWorkBranch(ctx, checkout, "feature/existing", "must fail"); err == nil || err.Error() != want {
		t.Fatalf("strict push err=%v", err)
	}
	if _, err := PullExistingWorkBranch(ctx, checkout, "feature/existing"); err == nil || err.Error() != want {
		t.Fatalf("strict pull err=%v", err)
	}
	if head, err := PushWorkBranch(ctx, checkout, "feature/existing", "skip"); err != nil || head != "" {
		t.Fatalf("generated push head=%q err=%v", head, err)
	}
	if head, err := PullWorkBranch(ctx, checkout, "feature/existing"); err != nil || head != "" {
		t.Fatalf("generated pull head=%q err=%v", head, err)
	}
}
