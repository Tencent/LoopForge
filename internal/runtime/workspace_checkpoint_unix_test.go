//go:build unix

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWorkspaceCheckpointRestoresRestrictedDirectoryModes(t *testing.T) {
	original := t.TempDir()
	parent := filepath.Join(original, "module-cache")
	nested := filepath.Join(parent, "version")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module example.org/fixture\n"), 0444); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{{nested, 0500}, {parent, 0555}} {
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory.path, 0700) })
	}
	archive, err := CreateWorkspaceCheckpoint(original)
	if err != nil {
		t.Fatal(err)
	}
	validationTemp := t.TempDir()
	t.Setenv("TMPDIR", validationTemp)
	if err := ValidateWorkspaceCheckpoint(archive); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(validationTemp); err != nil || len(entries) != 0 {
		t.Fatalf("validation left temporary entries: %v, %v", entries, err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := RestoreWorkspaceCheckpoint(destination, archive); err != nil {
		t.Fatal(err)
	}
	restoredParent := filepath.Join(destination, "module-cache")
	restoredNested := filepath.Join(restoredParent, "version")
	t.Cleanup(func() {
		_ = os.Chmod(restoredParent, 0700)
		_ = os.Chmod(restoredNested, 0700)
	})
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{{restoredParent, 0555}, {restoredNested, 0500}} {
		if info, err := os.Stat(directory.path); err != nil || info.Mode().Perm() != directory.mode {
			t.Fatalf("unexpected directory mode at %s: %v, %v", directory.path, info, err)
		}
	}
	file := filepath.Join(restoredNested, "go.mod")
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("unexpected file mode: %v, %v", info, err)
	}
	if body, err := os.ReadFile(file); err != nil || string(body) != "module example.org/fixture\n" {
		t.Fatalf("unexpected restored content: %q, %v", body, err)
	}
}

func TestCreateWorkspaceCheckpointRejectsGitIndexFIFOImmediately(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cache", "x"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(repo, ".git", "index")
	if err := syscall.Mkfifo(indexPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := CreateWorkspaceCheckpointContext(ctx, root)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unsupported checkpoint file") {
			t.Fatalf("expected unsupported checkpoint file error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateWorkspaceCheckpointContext hung on FIFO .git/index")
	}
}
