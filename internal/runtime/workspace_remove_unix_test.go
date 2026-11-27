//go:build unix

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveWorkspaceTreeRemovesRestrictedDirectoriesOnly(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o700) })
	workspace := filepath.Join(parent, "workspace")
	nested := filepath.Join(workspace, "pkg", "mod", "example.org", "fixture@v1.0.0")
	locked := filepath.Join(workspace, "locked")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(nested, "go.mod"), filepath.Join(locked, "secret")} {
		if err := os.WriteFile(file, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "pkg", "outside-link")); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{nested, 0o555}, {filepath.Dir(nested), 0o555}, {filepath.Join(workspace, "pkg", "mod"), 0o500},
		{filepath.Join(workspace, "pkg"), 0o555}, {locked, 0o000}, {workspace, 0o555},
	} {
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			t.Fatal(err)
		}
	}
	if os.Geteuid() != 0 {
		if err := os.RemoveAll(workspace); err == nil {
			t.Fatal("fixture should be unremovable without changing modes")
		}
	}
	if err := RemoveWorkspaceTree(workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("symlink target outside the workspace changed: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); err != nil {
		t.Fatalf("symlink target contents changed: %v", err)
	}
	if err := RemoveWorkspaceTree(workspace); err != nil {
		t.Fatalf("missing workspace: %v", err)
	}
}
