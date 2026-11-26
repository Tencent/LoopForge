//go:build unix

package runtime

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readOnlyModuleTree mimics a Go module cache inside a checkout: directories
// without owner write, so a plain os.RemoveAll fails with EACCES.
func readOnlyModuleTree(t *testing.T, dir string) {
	t.Helper()
	module := filepath.Join(dir, "pkg", "mod", "example.org", "fixture@v1.0.0")
	if err := os.MkdirAll(module, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.org/fixture\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{module, filepath.Dir(module)} {
		if err := os.Chmod(path, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(filepath.Dir(dir), func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
}

func leftoverReclaimed(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), reclaimedSandboxPrefix) {
			out = append(out, entry.Name())
		}
	}
	return out
}

func TestDestroyRunRemovesReadOnlyTreesWithoutRetiredLeftovers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	m := NewManager(root)
	checkout := m.RunWorkspace("run-readonly")
	readOnlyModuleTree(t, checkout)
	if err := m.DestroyRun("run-readonly"); err != nil {
		t.Fatalf("destroy a checkout with a read-only module cache: %v", err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout still exists: %v", err)
	}
	if left := leftoverReclaimed(t, root); len(left) != 0 {
		t.Fatalf("retired copies left on disk: %v", left)
	}
}

func TestReclaimExceptRemovesReadOnlyOrphansAndRetiredCopies(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	m := NewManager(root)
	orphan := m.RunWorkspace("run-orphan")
	retired := filepath.Join(root, reclaimedSandboxPrefix+rand.Text())
	readOnlyModuleTree(t, orphan)
	readOnlyModuleTree(t, retired)
	removed, err := m.ReclaimExcept(func() ([]string, error) { return nil, nil })
	if err != nil || removed != 2 {
		t.Fatalf("reclaimed %d read-only paths: %v", removed, err)
	}
	for _, path := range []string{orphan, retired} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
}
