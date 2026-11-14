package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemEnvironmentIdentitySupportsLongWindowsPath(t *testing.T) {
	workspace := t.TempDir()
	for len(workspace) < 280 {
		workspace = filepath.Join(workspace, strings.Repeat("nested", 8))
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := filesystemEnvironmentIdentity(workspace, "local", nil)
	if err != nil || identity == "" {
		t.Fatalf("read identity for nested Windows path: %v", err)
	}
	again, err := filesystemEnvironmentIdentity(workspace, "local", nil)
	if err != nil || again != identity {
		t.Fatalf("nested workspace identity was unstable: %v", err)
	}
}
