package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func setGitIdentityT(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

// commitOnRemoteT pushes one commit that writes file=content onto branch
// from a separate clone, simulating another run or a teammate.
func commitOnRemoteT(t *testing.T, remote, branch, file, content string) string {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed")
	runGitT(t, filepath.Dir(seed), "clone", "-q", "-b", branch, remote, seed)
	if err := os.WriteFile(filepath.Join(seed, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "add", file)
	runGitT(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "remote "+file)
	runGitT(t, seed, "push", "-q", "origin", branch)
	return runGitT(t, seed, "rev-parse", "HEAD")
}

// 普通模式拉取只跟上远程，不能把本地未提交的改动提交并推上去。
func TestSyncWorkBranchPlainPullNeverPushes(t *testing.T) {
	setGitIdentityT(t)
	remote := bareRemoteWithBaseline(t)
	workspace := checkoutWorkBranchT(t, domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/pull"})
	if err := os.WriteFile(filepath.Join(workspace, "first.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncWorkBranch(context.Background(), workspace, "feature/pull", "first", true, false); err != nil {
		t.Fatalf("initial push: %v", err)
	}
	remoteCommit := commitOnRemoteT(t, remote, "feature/pull", "other.txt", "other run\n")
	if err := os.WriteFile(filepath.Join(workspace, "local.txt"), []byte("not for the remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pulled, err := syncWorkBranch(context.Background(), workspace, "feature/pull", "", false, false)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled != remoteCommit || runGitT(t, workspace, "rev-parse", "HEAD") != remoteCommit {
		t.Fatalf("pulled = %q, local HEAD = %q, want remote commit %q", pulled, runGitT(t, workspace, "rev-parse", "HEAD"), remoteCommit)
	}
	if got := remoteHeadT(t, remote, "feature/pull"); got != remoteCommit {
		t.Fatalf("pull moved the remote branch to %s", got)
	}
	if status := runGitT(t, workspace, "status", "--porcelain", "--", "local.txt"); status != "?? local.txt" {
		t.Fatalf("pull touched local work: %q", status)
	}
}

// 严格模式推送：远程分支已存在时，本地改动被提交并推上去，返回远程的新 head。
func TestSyncWorkBranchStrictPushAdvancesExistingRemote(t *testing.T) {
	setGitIdentityT(t)
	remote := bareRemoteWithBaseline(t)
	runGitT(t, remote, "branch", "feature/existing", "master")
	before := remoteHeadT(t, remote, "feature/existing")
	workspace := checkoutWorkBranchT(t, domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/existing", RequireExistingWorkBranch: true})
	if err := os.WriteFile(filepath.Join(workspace, "strict.txt"), []byte("strict work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, err := syncWorkBranch(context.Background(), workspace, "feature/existing", "strict work", true, true)
	if err != nil {
		t.Fatalf("strict push: %v", err)
	}
	after := remoteHeadT(t, remote, "feature/existing")
	if head == "" || head == before || after != head {
		t.Fatalf("strict push head = %q, remote before = %q, after = %q", head, before, after)
	}
	if files := runGitT(t, remote, "ls-tree", "--name-only", head); !strings.Contains(files, "strict.txt") {
		t.Fatalf("pushed commit is missing the local change: %s", files)
	}
}

// 整条推送链路：工作分支推送后再直推共享目标，返回的 head 记录两个分支在远程的实际位置。
func TestSyncTaskWorkBranchesPushRecordsSharedHead(t *testing.T) {
	setGitIdentityT(t)
	remote := bareRemoteWithBaseline(t)
	repository := domain.RunnerRepository{RepositoryID: "r1", Name: "app", CloneURL: remote, Ref: "master", WorkBranch: "feature/task", SyncWorkBranch: true, SharedPushTarget: "master"}
	workspace := checkoutWorkBranchT(t, repository)
	if err := os.WriteFile(filepath.Join(workspace, "task.txt"), []byte("task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invocation := &domain.RunnerInvocation{NodeID: "n1", RunID: "run1", Repositories: []domain.RunnerRepository{repository}}
	heads, err := syncTaskWorkBranches(context.Background(), workspace, invocation, true, "task work")
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(heads) != 1 {
		t.Fatalf("heads = %+v", heads)
	}
	h := heads[0]
	if h.RepositoryID != "r1" || h.Name != "app" || h.WorkBranch != "feature/task" || h.Head == "" || h.Head != remoteHeadT(t, remote, "feature/task") {
		t.Fatalf("work head = %+v, remote = %q", h, remoteHeadT(t, remote, "feature/task"))
	}
	if h.SharedBranch != "master" || h.SharedHead == "" || h.SharedHead != remoteHeadT(t, remote, "master") {
		t.Fatalf("shared head = %+v, remote master = %q", h, remoteHeadT(t, remote, "master"))
	}
}

// 直推目标与工作分支冲突：报错列出冲突文件、文案与拆分前一致，共享分支保持不动，
// 而且 syncTaskWorkBranches 必须把这个错误传出去，不能当作成功上报。
func TestPushSharedTargetConflictIsReportedAndNotPushed(t *testing.T) {
	setGitIdentityT(t)
	remote := bareRemoteWithBaseline(t)
	repository := domain.RunnerRepository{Name: "app", CloneURL: remote, Ref: "master", WorkBranch: "feature/conflict", SyncWorkBranch: true, SharedPushTarget: "master"}
	workspace := checkoutWorkBranchT(t, repository)
	sharedBefore := commitOnRemoteT(t, remote, "master", "README.md", "shared change\n")
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("work change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invocation := &domain.RunnerInvocation{NodeID: "n1", RunID: "run1", Repositories: []domain.RunnerRepository{repository}}
	heads, err := syncTaskWorkBranches(context.Background(), workspace, invocation, true, "conflicting work")
	want := "app: 合并 feature/conflict 进 master 有冲突，没有推送：README.md"
	if err == nil || err.Error() != want {
		t.Fatalf("sync err = %v, want %q", err, want)
	}
	if len(heads) != 0 {
		t.Fatalf("conflicting repository reported as delivered: %+v", heads)
	}
	if got := remoteHeadT(t, remote, "master"); got != sharedBefore {
		t.Fatalf("conflict pushed the shared branch: %s, want %s", got, sharedBefore)
	}

	merged, err := pushSharedTarget(context.Background(), workspace, repository)
	if err == nil || err.Error() != want || merged != "" {
		t.Fatalf("pushSharedTarget = %q, %v; want %q", merged, err, want)
	}
	if got := remoteHeadT(t, remote, "master"); got != sharedBefore {
		t.Fatalf("conflict pushed the shared branch: %s, want %s", got, sharedBefore)
	}
}

// 直推失败时保留底层 git 错误（%w），调用方还能看到具体原因。
func TestPushSharedTargetWrapsUnderlyingError(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	repository := domain.RunnerRepository{Name: "app", CloneURL: remote, Ref: "master", WorkBranch: "feature/shared", SharedPushTarget: "release/missing"}
	workspace := checkoutWorkBranchT(t, repository)
	_, err := pushSharedTarget(context.Background(), workspace, repository)
	if err == nil {
		t.Fatal("missing shared target accepted")
	}
	inner := errors.Unwrap(err)
	if inner == nil || strings.TrimSpace(inner.Error()) == "" || err.Error() != "app: 直推 release/missing: "+inner.Error() {
		t.Fatalf("err = %v, unwrapped = %v", err, inner)
	}
}
