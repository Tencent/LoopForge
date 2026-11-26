package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReclaimExceptRemovesDetachedFilesAndKeepsRestoredCheckout(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	restored := manager.RunWorkspace("run-restored")
	detached := filepath.Join(root, reclaimedWorkspacePrefix+"earlier-failure")
	for _, directory := range []string{restored, detached} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := manager.ReclaimExcept([]string{"run-restored"})
	if err != nil || removed != 1 {
		t.Fatalf("swept %d workspaces: %v", removed, err)
	}
	if _, err := os.Stat(detached); !os.IsNotExist(err) {
		t.Fatalf("detached files remain: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(restored, "notes.txt")); err != nil || string(data) != "content" {
		t.Fatalf("restored checkout changed: %q, %v", data, err)
	}
}
