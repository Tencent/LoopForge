//go:build unix

package runtime

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A workspace path swapped for a symlink to an outside directory while it is
// being removed must not give that directory owner write permission.
func TestRemoveWorkspaceTreeKeepsOutsideDirectoryWhenRootIsSwapped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(outside, "sub"), outside} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(outside, 0o700)
		_ = os.Chmod(filepath.Join(outside, "sub"), 0o700)
	})
	workspace := filepath.Join(parent, "ws")
	moved := filepath.Join(parent, "ws-moved")
	link := filepath.Join(parent, "ws-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = RemoveWorkspaceTree(workspace)
		_ = RemoveWorkspaceTree(moved)
	})
	makeWorkspace := func() {
		locked := filepath.Join(workspace, "locked")
		if err := os.MkdirAll(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	outsideChanged := func() string {
		for _, dir := range []string{outside, filepath.Join(outside, "sub")} {
			if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o555 {
				return dir
			}
		}
		return ""
	}

	deadline := time.Now().Add(3 * time.Second)
	for iteration := 0; time.Now().Before(deadline); iteration++ {
		makeWorkspace()
		var stop atomic.Bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			for !stop.Load() {
				// ws -> moved, ws-link -> ws, then put everything back.
				if os.Rename(workspace, moved) != nil {
					continue
				}
				_ = os.Rename(link, workspace)
				_ = os.Rename(workspace, link)
				_ = os.Rename(moved, workspace)
			}
		}()
		_ = RemoveWorkspaceTree(workspace)
		stop.Store(true)
		<-done
		if dir := outsideChanged(); dir != "" {
			t.Fatalf("iteration %d: directory outside the workspace changed: %s", iteration, dir)
		}
		// Restore the fixture for the next iteration.
		if _, err := os.Lstat(link); err != nil {
			if info, err := os.Lstat(workspace); err == nil && info.Mode()&os.ModeSymlink != 0 {
				_ = os.Rename(workspace, link)
			} else if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := os.Lstat(moved); err == nil {
			_ = RemoveWorkspaceTree(workspace)
			_ = os.Rename(moved, workspace)
		}
		_ = RemoveWorkspaceTree(workspace)
	}
}
