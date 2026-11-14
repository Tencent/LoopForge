package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemEnvironmentIdentityDetectsWorkspaceReplacement(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := filesystemEnvironmentIdentity(workspace, "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := filesystemEnvironmentIdentity(workspace, "local", nil)
	if err != nil || again != before {
		t.Fatalf("unchanged workspace identity changed: %q %q %v", before, again, err)
	}
	// Keep the original directory alive so the filesystem cannot reuse its ID.
	if err := os.Rename(workspace, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := filesystemEnvironmentIdentity(workspace, "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("replacement workspace reused the previous environment identity")
	}
}
