package gitwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeaseRejected(t *testing.T) {
	cases := map[string]bool{
		"! [rejected] main -> main (stale info)":               true,
		"! [remote rejected] main -> main (deny updating)":     true,
		"! [Remote Rejected] main -> main (pre-receive hook)":  true,
		"! [rejected] main -> main (non-fast-forward)":         false,
		"fatal: unable to access 'https://example.com/x.git/'": false,
	}
	for message, want := range cases {
		if got := leaseRejected(errors.New(message)); got != want {
			t.Errorf("leaseRejected(%q) = %v, want %v", message, got, want)
		}
	}
}

func TestRetryPushSucceedsWithoutConsultingReject(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "feature.txt")
	workBranchGit(t, checkout, "commit", "-qm", "work")
	head := workBranchGit(t, checkout, "rev-parse", "HEAD")

	calls := 0
	stop, pushErr := retryPush(context.Background(), checkout, []string{"push", "origin", "HEAD:refs/heads/feature/existing"}, func(error) error {
		calls++
		return nil
	})
	if stop != nil || pushErr != nil || calls != 0 {
		t.Fatalf("stop=%v pushErr=%v reject calls=%d", stop, pushErr, calls)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing"); got != head {
		t.Fatalf("remote head=%s want %s", got, head)
	}
}

func TestRetryPushStopsAtFirstRejection(t *testing.T) {
	_, checkout := workBranchFixture(t)
	rejected := errors.New("rejected")
	calls := 0
	stop, pushErr := retryPush(context.Background(), checkout, []string{"push", "missing-remote", "HEAD:refs/heads/feature/existing"}, func(err error) error {
		calls++
		if err == nil {
			t.Fatal("reject must receive the push error")
		}
		return rejected
	})
	if !errors.Is(stop, rejected) || pushErr != nil || calls != 1 {
		t.Fatalf("stop=%v pushErr=%v reject calls=%d", stop, pushErr, calls)
	}
}

func TestRetryPushReturnsContextErrorBeforeRetrying(t *testing.T) {
	_, checkout := workBranchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	stop, pushErr := retryPush(ctx, checkout, []string{"push", "missing-remote", "HEAD:refs/heads/feature/existing"}, func(error) error {
		calls++
		return nil
	})
	if !errors.Is(stop, context.Canceled) || pushErr != nil || calls != 1 {
		t.Fatalf("stop=%v pushErr=%v reject calls=%d", stop, pushErr, calls)
	}
}

func TestMergeWorkIntoSharedBranchRefusesConcurrentlyMovedTarget(t *testing.T) {
	remote, checkout := sharedBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "feature.txt")
	workBranchGit(t, checkout, "commit", "-qm", "work commit")
	workBranchGit(t, checkout, "push", "-q", "origin", "feature/existing")
	moved := workBranchGit(t, checkout, "rev-parse", "HEAD")
	// pre-push runs after the merge's lease is fixed; moving the target here
	// simulates somebody else pushing the shared branch in between.
	hook := "#!/bin/sh\ngit --git-dir='" + strings.ReplaceAll(remote, "'", "'\\''") + "' update-ref refs/heads/preview " + moved + "\n"
	if err := os.WriteFile(filepath.Join(checkout, ".git", "hooks", "pre-push"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}

	head, conflicts, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "preview")
	if err == nil || head != "" || len(conflicts) != 0 {
		t.Fatalf("merge over a moved target: head=%q conflicts=%v err=%v", head, conflicts, err)
	}
	if !strings.Contains(err.Error(), "共享分支 preview 已被别人推送") || !leaseRejected(errors.Unwrap(err)) {
		t.Fatalf("moved target must be reported as a lease rejection, got %v", err)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/preview"); got != moved {
		t.Fatalf("remote preview=%s, the concurrent push %s must be kept", got, moved)
	}
}

func TestPushWorkBranchReportsRemoteOnlyCommits(t *testing.T) {
	remote, checkout := workBranchFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	workBranchGit(t, filepath.Dir(other), "clone", "-q", "-b", "feature/existing", remote, other)
	workBranchGit(t, other, "config", "user.name", "Other")
	workBranchGit(t, other, "config", "user.email", "other@example.com")
	if err := os.WriteFile(filepath.Join(other, "manual.txt"), []byte("manual\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, other, "add", "manual.txt")
	workBranchGit(t, other, "commit", "-qm", "manual push")
	workBranchGit(t, other, "push", "-q", "origin", "feature/existing")
	manual := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing")

	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := PushWorkBranch(context.Background(), checkout, "feature/existing", "local work")
	if err == nil || head != "" {
		t.Fatalf("default push over remote-only commits: head=%q err=%v", head, err)
	}
	if !strings.Contains(err.Error(), "有本机没有的提交") || !IsNonFastForward(errors.Unwrap(err)) {
		t.Fatalf("remote-only commits must be reported as non-fast-forward, got %v", err)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing"); got != manual {
		t.Fatalf("remote head=%s, manual push %s must be kept", got, manual)
	}
}

func TestPushWorkBranchRetriesOtherFailuresThenWrapsLastError(t *testing.T) {
	// Waits through the real 2s+4s backoff between the three attempts.
	remote, checkout := workBranchFixture(t)
	before := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing")
	attempts := filepath.Join(t.TempDir(), "attempts")
	// A declining pre-receive hook reports "remote rejected", which only
	// strict pushes treat as a lease rejection; default pushes retry it.
	hook := "#!/bin/sh\necho attempt >> '" + strings.ReplaceAll(attempts, "'", "'\\''") + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	head, err := PushWorkBranch(context.Background(), checkout, "feature/existing", "local work")
	if err == nil || head != "" {
		t.Fatalf("declined push: head=%q err=%v", head, err)
	}
	if !strings.HasPrefix(err.Error(), "push work branch feature/existing: ") || strings.Contains(err.Error(), "拒绝覆盖") || !leaseRejected(errors.Unwrap(err)) {
		t.Fatalf("exhausted retries must wrap the last push error, got %v", err)
	}
	data, readErr := os.ReadFile(attempts)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(data), "attempt"); got != 3 {
		t.Fatalf("push attempts=%d want 3", got)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/feature/existing"); got != before {
		t.Fatalf("remote head moved to %s after declined pushes", got)
	}
}

func TestMergeWorkIntoSharedBranchStopsOnRemoteRejection(t *testing.T) {
	remote, checkout := sharedBranchFixture(t)
	if err := os.WriteFile(filepath.Join(checkout, "feature.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, checkout, "add", "feature.txt")
	workBranchGit(t, checkout, "commit", "-qm", "work commit")
	before := workBranchGit(t, remote, "rev-parse", "refs/heads/preview")
	attempts := filepath.Join(t.TempDir(), "attempts")
	hook := "#!/bin/sh\necho attempt >> '" + strings.ReplaceAll(attempts, "'", "'\\''") + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}

	head, conflicts, err := MergeWorkIntoSharedBranch(context.Background(), checkout, "feature/existing", "preview")
	if err == nil || head != "" || len(conflicts) != 0 {
		t.Fatalf("declined shared push: head=%q conflicts=%v err=%v", head, conflicts, err)
	}
	if !strings.Contains(err.Error(), "共享分支 preview 已被别人推送") || IsNonFastForward(errors.Unwrap(err)) {
		t.Fatalf("a remote rejection must stop as a lease rejection, got %v", err)
	}
	data, readErr := os.ReadFile(attempts)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(data), "attempt"); got != 1 {
		t.Fatalf("push attempts=%d, a rejected shared push must not be retried", got)
	}
	if got := workBranchGit(t, remote, "rev-parse", "refs/heads/preview"); got != before {
		t.Fatalf("remote preview moved to %s after a declined push", got)
	}
}
