package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func checkoutWorkBranchT(t *testing.T, repository domain.RunnerRepository) string {
	t.Helper()
	workspace := t.TempDir()
	if err := checkoutRepository(context.Background(), workspace, &repository); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return workspace
}

func remoteHeadT(t *testing.T, remote, branch string) string {
	t.Helper()
	return runGitT(t, remote, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
}

// 普通模式：推送会新建远程工作分支并返回推送的提交，拉取在远程没有新提交时返回空。
func TestSyncWorkBranchPushThenPull(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	workspace := checkoutWorkBranchT(t, domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/sync"})
	if err := os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := syncWorkBranch(context.Background(), workspace, "feature/sync", "sync work", true, false)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if head == "" || remoteHeadT(t, remote, "feature/sync") != head {
		t.Fatalf("pushed head = %q, remote = %q", head, remoteHeadT(t, remote, "feature/sync"))
	}
	pulled, err := syncWorkBranch(context.Background(), workspace, "feature/sync", "", false, false)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled != "" {
		t.Fatalf("pull of an up-to-date branch moved HEAD to %q", pulled)
	}
}

// 严格模式（继续已有分支）：远程分支不存在时，推送和拉取都报错，且不会新建远程分支。
func TestSyncWorkBranchStrictRefusesMissingRemote(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	workspace := checkoutWorkBranchT(t, domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/gone"})
	for _, push := range []bool{true, false} {
		if _, err := syncWorkBranch(context.Background(), workspace, "feature/gone", "msg", push, true); err == nil {
			t.Fatalf("strict sync push=%v accepted a missing remote branch", push)
		}
	}
	if got := remoteHeadT(t, remote, "feature/gone"); got != "" {
		t.Fatalf("strict sync created the remote branch at %s", got)
	}
}

// 直推：推送后的工作分支合并进已有的共享分支，返回推到远程的合并提交。
func TestPushSharedTargetMergesWorkBranch(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	remote := bareRemoteWithBaseline(t)
	repository := domain.RunnerRepository{Name: "app", CloneURL: remote, Ref: "master", WorkBranch: "feature/shared", SharedPushTarget: "master"}
	workspace := checkoutWorkBranchT(t, repository)
	if err := os.WriteFile(filepath.Join(workspace, "feature.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncWorkBranch(context.Background(), workspace, repository.WorkBranch, "sync work", true, false); err != nil {
		t.Fatalf("push: %v", err)
	}
	merged, err := pushSharedTarget(context.Background(), workspace, repository)
	if err != nil {
		t.Fatalf("shared push: %v", err)
	}
	if merged == "" || remoteHeadT(t, remote, "master") != merged {
		t.Fatalf("merged = %q, remote master = %q", merged, remoteHeadT(t, remote, "master"))
	}
}

// 直推目标在远程不存在时，报错带上仓库名和目标分支，文案与拆分前一致。
func TestPushSharedTargetReportsMissingTarget(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	repository := domain.RunnerRepository{Name: "app", CloneURL: remote, Ref: "master", WorkBranch: "feature/shared", SharedPushTarget: "release/missing"}
	workspace := checkoutWorkBranchT(t, repository)
	_, err := pushSharedTarget(context.Background(), workspace, repository)
	if err == nil || !strings.HasPrefix(err.Error(), "app: 直推 release/missing: ") {
		t.Fatalf("err = %v", err)
	}
}
