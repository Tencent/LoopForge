package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFdDirHoldsFile(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Lstat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "other")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.lock")
	if err := os.Link(lockPath, alias); err != nil {
		t.Fatal(err)
	}
	// fdDir builds a fake /proc/<pid>/fd directory whose entries are symlinks.
	fdDir := func(targets ...string) string {
		dir := filepath.Join(t.TempDir(), "fd")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, target := range targets {
			if err := os.Symlink(target, filepath.Join(dir, string(rune('0'+i)))); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	for name, tc := range map[string]struct {
		targets []string
		held    bool
	}{
		"empty":          {nil, false},
		"pipes-sockets":  {[]string{"pipe:[123]", "socket:[456]", "anon_inode:[eventfd]"}, false},
		"other-file":     {[]string{other}, false},
		"dangling":       {[]string{filepath.Join(root, "gone")}, false},
		"same-path":      {[]string{"pipe:[1]", other, lockPath}, true},
		"same-inode-alt": {[]string{alias}, true},
	} {
		t.Run(name, func(t *testing.T) {
			held, err := fdDirHoldsFile(fdDir(tc.targets...), lockPath, lock)
			if err != nil || held != tc.held {
				t.Fatalf("held=%v err=%v, want held=%v", held, err, tc.held)
			}
		})
	}
	t.Run("unreadable-dir", func(t *testing.T) {
		held, err := fdDirHoldsFile(filepath.Join(root, "missing", "fd"), lockPath, lock)
		if held || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("held=%v err=%v, want not-exist error", held, err)
		}
	})
}
