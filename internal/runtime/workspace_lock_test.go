package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkspaceLockExcludesOtherHandlesAndHonorsCancellation(t *testing.T) {
	m := &Manager{root: t.TempDir()}
	first, err := m.acquireLock(context.Background(), "workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFile(first)
	if !m.workspaceInUse("workspace") {
		t.Fatal("locked workspace reported available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if second, err := m.acquireLock(ctx, "workspace"); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			_ = unlockFile(second)
		}
		t.Fatalf("contending lock did not honor cancellation: %v", err)
	}
	if err := unlockFile(first); err != nil {
		t.Fatal(err)
	}
	if m.workspaceInUse("workspace") {
		t.Fatal("released workspace reported locked")
	}
	second, err := m.acquireLock(context.Background(), "workspace")
	if err != nil {
		t.Fatalf("released lock could not be acquired: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if m.workspaceInUse("workspace") {
		t.Fatal("closing a lock handle did not release the OS lock")
	}
}
