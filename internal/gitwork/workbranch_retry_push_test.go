package gitwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
