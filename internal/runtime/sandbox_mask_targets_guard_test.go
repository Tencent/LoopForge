package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlinked empty-file target must be rejected even when it points to a
// regular file inside the workspace: Docker would bind the mask over the link
// target instead of the placeholder, so the link target must stay untouched.
func TestEnsureDockerMaskTargetsRejectsSymlinkedFile(t *testing.T) {
	workspace := t.TempDir()
	isolation, err := EnsureControlDirectory(workspace, "isolation")
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(isolation, "real-file")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(isolation, "empty-file")); err != nil {
		t.Fatal(err)
	}
	err = ensureDockerMaskTargets(workspace)
	if err == nil || !strings.Contains(err.Error(), "workspace path is a symlink") {
		t.Fatalf("ensureDockerMaskTargets() error = %v, want symlink error", err)
	}
	if info, err := os.Lstat(filepath.Join(isolation, "empty-file")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlinked target was replaced: %v, %v", info, err)
	}
}

// The empty-dir target must be a directory; a regular file in its place is an
// error rather than silently skipped.
func TestEnsureDockerMaskTargetsRejectsFileAsEmptyDir(t *testing.T) {
	workspace := t.TempDir()
	isolation, err := EnsureControlDirectory(workspace, "isolation")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(isolation, "empty-dir"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDockerMaskTargets(workspace); err == nil {
		t.Fatal("ensureDockerMaskTargets accepted a regular file as the empty-dir target")
	}
}

// dockerAgentConfigMasks must stop on a broken mount target instead of
// returning masks Docker cannot mount.
func TestDockerAgentConfigMasksPropagatesTargetError(t *testing.T) {
	workspace := t.TempDir()
	if _, err := EnsureControlDirectory(workspace, "isolation", "empty-file"); err != nil {
		t.Fatal(err)
	}
	masks, err := dockerAgentConfigMasks(workspace)
	if err == nil || !strings.Contains(err.Error(), "sandbox mask target is not a regular file") || masks != nil {
		t.Fatalf("dockerAgentConfigMasks() = %v, %v; want nil masks and non-regular file error", masks, err)
	}
}
