package engine

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Chat workspaces and free collaboration runs have no issue to name their
// branch after; two opened in the same minute must still get their own.
func TestDefaultWorkBranchKeepsUnrelatedRunsApart(t *testing.T) {
	now := time.Date(2026, 10, 30, 19, 38, 9, 0, time.Local)
	a, b := defaultWorkBranch(0, "", now), defaultWorkBranch(0, "", now)
	if a == b || !strings.HasPrefix(a, "feature/fix-20261030-1938-") || !gitBranchName(a) {
		t.Fatalf("same-minute branches %q and %q", a, b)
	}
	if got := defaultWorkBranch(12, "", now); got != "feature/fix-issue-12-20261030-1938" {
		t.Fatalf("issue branch changed: %q", got)
	}
}

func gitBranchName(name string) bool {
	return exec.Command("git", "check-ref-format", "refs/heads/"+name).Run() == nil
}
