package engine

import (
	"errors"
	"os"
	"testing"
	"time"
)

// sandboxRoot returns a sandbox root for an engine under test.
//
// Terminal workspace cleanup keeps running after a run is already reported
// finished, and it moves a checkout to a .loopforge-reclaimed-* sibling before
// deleting it. A test that returns while that rename is in flight makes the
// single RemoveAll of t.TempDir fail with "directory not empty". Remove the
// root here first and retry until it stays gone.
func sandboxRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(t.TempDir(), "sandboxes-")
	if err != nil {
		t.Fatal(err)
	}
	// Registered after t.TempDir, so it runs before the parent is removed.
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			err := os.RemoveAll(root)
			if err == nil {
				// A late cleanup may still recreate the lock directory.
				time.Sleep(50 * time.Millisecond)
				if _, statErr := os.Lstat(root); errors.Is(statErr, os.ErrNotExist) {
					return
				}
				err = errors.New("recreated after removal")
			}
			if time.Now().After(deadline) {
				t.Errorf("remove sandbox root %s: %v", root, err)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return root
}
