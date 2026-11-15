package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsGitIndexLockDoesNotAssumeUnixOwnership(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "index.lock")
	if err := os.WriteFile(lockPath, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if inUse, confirmed, err := gitIndexLockHeldUnder(t.TempDir(), lockPath, 0); inUse || confirmed || err == nil {
		t.Fatalf("unsupported ownership was treated as a stale lock: inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
	if content, err := os.ReadFile(lockPath); err != nil || string(content) != "keep" {
		t.Fatalf("unconfirmed lock was changed: %q %v", content, err)
	}
}
