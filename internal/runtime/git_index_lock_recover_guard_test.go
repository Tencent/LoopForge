package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// briefLiveIndexLock keeps .git/index.lock open in this process (a live
// holder) and releases it after a short delay. The lock is unlinked before the
// descriptor closes, so the waiter never sees an existing-but-unheld lock.
func briefLiveIndexLock(t *testing.T) (string, string) {
	t.Helper()
	requireLinuxProc(t)
	workspace, lockPath := staleIndexLockWorkspace(t)
	wait, poll := gitIndexLockWait, gitIndexLockPoll
	gitIndexLockWait, gitIndexLockPoll = 5*time.Second, 20*time.Millisecond
	t.Cleanup(func() { gitIndexLockWait, gitIndexLockPoll = wait, poll })
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(150 * time.Millisecond)
		os.Remove(lockPath)
		lock.Close()
	}()
	t.Cleanup(func() { <-released })
	return workspace, lockPath
}

// After a live holder releases the lock, a retry that fails for a reason other
// than the lock must be reported at once: no further retries, no cleanup pass.
func TestRecoverGitIndexLockConflictReportsNonLockFailureAfterLiveHolderReleases(t *testing.T) {
	workspace, lockPath := briefLiveIndexLock(t)
	calls := 0
	run := func() ([]byte, error) {
		calls++
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Errorf("retry ran while the live index lock still existed: %v", err)
		}
		return []byte("  fatal: bad object  \n"), errors.New("exit status 128")
	}
	err := recoverGitIndexLockConflict(context.Background(), workspace, "--skip-worktree", run, []byte(testIndexLockConflictOutput), errors.New("exit status 128"))
	want := "git update-index --skip-worktree: exit status 128: fatal: bad object"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if calls != 1 {
		t.Fatalf("run called %d times, want 1", calls)
	}
}

// When the retry after a live holder's release collides with another index.lock
// writer, recovery keeps going instead of failing on the second collision.
func TestRecoverGitIndexLockConflictRetriesAgainAfterRepeatedCollision(t *testing.T) {
	workspace, lockPath := briefLiveIndexLock(t)
	calls := 0
	run := func() ([]byte, error) {
		calls++
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Errorf("retry %d ran while the live index lock still existed: %v", calls, err)
		}
		if calls == 1 {
			return []byte(testIndexLockConflictOutput), errors.New("exit status 128")
		}
		return nil, nil
	}
	err := recoverGitIndexLockConflict(context.Background(), workspace, "--no-skip-worktree", run, []byte(testIndexLockConflictOutput), errors.New("exit status 128"))
	if err != nil {
		t.Fatalf("recover error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("run called %d times, want 2", calls)
	}
}

// An index.lock that cannot be inspected (here: not a regular file) must be
// left alone and reported; recovery must neither remove it nor retry.
func TestRecoverGitIndexLockConflictKeepsUninspectableLock(t *testing.T) {
	requireLinuxProc(t)
	workspace, lockPath := staleIndexLockWorkspace(t)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	run := func() ([]byte, error) {
		calls++
		return nil, nil
	}
	err := recoverGitIndexLockConflict(context.Background(), workspace, "--skip-worktree", run, []byte(testIndexLockConflictOutput), errors.New("exit status 128"))
	if err == nil || !strings.Contains(err.Error(), "(cannot safely inspect index lock:") {
		t.Fatalf("error = %v, want uninspectable lock failure", err)
	}
	if calls != 0 {
		t.Fatalf("run called %d times, want 0", calls)
	}
	if info, statErr := os.Stat(lockPath); statErr != nil || !info.IsDir() {
		t.Fatalf("uninspectable index lock was removed: %v", statErr)
	}
}
