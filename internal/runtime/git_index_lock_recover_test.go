package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testIndexLockConflictOutput = "fatal: Unable to create '/repo/.git/index.lock': File exists."

func TestRecoverGitIndexLockConflictReportsNonLockFailureWithoutRetry(t *testing.T) {
	calls := 0
	run := func() ([]byte, error) {
		calls++
		return nil, nil
	}
	err := recoverGitIndexLockConflict(context.Background(), t.TempDir(), "--skip-worktree", run, []byte("  fatal: bad path  \n"), errors.New("exit status 128"))
	if err == nil || err.Error() != "git update-index --skip-worktree: exit status 128: fatal: bad path" {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("run called %d times, want 0", calls)
	}
}

func staleIndexLockWorkspace(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	lockPath := filepath.Join(workspace, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace, lockPath
}

func TestRecoverGitIndexLockConflictRemovesStaleLockAndRetries(t *testing.T) {
	workspace, lockPath := staleIndexLockWorkspace(t)
	calls := 0
	run := func() ([]byte, error) {
		calls++
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("retry ran before stale lock removal: %v", err)
		}
		return nil, nil
	}
	err := recoverGitIndexLockConflict(context.Background(), workspace, "--skip-worktree", run, []byte(testIndexLockConflictOutput), errors.New("exit status 128"))
	if err != nil {
		t.Fatalf("recover error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("run called %d times, want 1", calls)
	}
}

func TestRecoverGitIndexLockConflictReportsFailedRetryAfterCleanup(t *testing.T) {
	workspace, _ := staleIndexLockWorkspace(t)
	run := func() ([]byte, error) {
		return []byte("fatal: still broken\n"), errors.New("exit status 1")
	}
	err := recoverGitIndexLockConflict(context.Background(), workspace, "--no-skip-worktree", run, []byte(testIndexLockConflictOutput), errors.New("exit status 128"))
	want := "git update-index --no-skip-worktree after stale index lock cleanup: exit status 1: fatal: still broken"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestRecoverGitIndexLockConflictReportsLockPathResolutionFailure(t *testing.T) {
	calls := 0
	run := func() ([]byte, error) {
		calls++
		return nil, nil
	}
	notRepo := t.TempDir()
	err := recoverGitIndexLockConflict(context.Background(), notRepo, "--skip-worktree", run, []byte(testIndexLockConflictOutput), errors.New("exit status 128"))
	if err == nil || !strings.Contains(err.Error(), "(resolve index lock:") {
		t.Fatalf("error = %v, want lock path resolution failure", err)
	}
	if calls != 0 {
		t.Fatalf("run called %d times, want 0", calls)
	}
}
