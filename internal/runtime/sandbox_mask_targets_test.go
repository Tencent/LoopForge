package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDockerMaskTargetsCreatesTargets(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := ensureDockerMaskTargets(workspace); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	dir, err := os.Lstat(filepath.Join(workspace, ".agentflow", "isolation", "empty-dir"))
	if err != nil || !dir.IsDir() {
		t.Fatalf("empty-dir target: %v, %v", dir, err)
	}
	file, err := os.Lstat(filepath.Join(workspace, ".agentflow", "isolation", "empty-file"))
	if err != nil || !file.Mode().IsRegular() {
		t.Fatalf("empty-file target: %v, %v", file, err)
	}
	if file.Mode().Perm() != 0o600 {
		t.Fatalf("empty-file mode = %v, want 0600", file.Mode().Perm())
	}
}

func TestEnsureDockerMaskTargetsKeepsExistingFile(t *testing.T) {
	workspace := t.TempDir()
	if err := ensureDockerMaskTargets(workspace); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, ".agentflow", "isolation", "empty-file")
	if err := os.WriteFile(target, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDockerMaskTargets(workspace); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "kept" {
		t.Fatalf("existing empty-file was rewritten: %q, %v", data, err)
	}
}

func TestEnsureDockerMaskTargetsRejectsNonRegularFile(t *testing.T) {
	workspace := t.TempDir()
	if _, err := EnsureControlDirectory(workspace, "isolation", "empty-file"); err != nil {
		t.Fatal(err)
	}
	err := ensureDockerMaskTargets(workspace)
	if err == nil || !strings.Contains(err.Error(), "sandbox mask target is not a regular file") {
		t.Fatalf("ensureDockerMaskTargets() error = %v, want non-regular file error", err)
	}
}
