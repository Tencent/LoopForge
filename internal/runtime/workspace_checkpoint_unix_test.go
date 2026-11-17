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
