//go:build unix

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A reclaim that stops at a read-only directory leaves a partial tree, and the
// next idle sweep would replace the complete checkpoint with it.
func TestConversationWorkspaceReclaimsAndRestoresReadOnlyDirectories(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	root := e.RunWorkspace(id)
	cache := filepath.Join(root, "gomod", "cache", "download")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "go.mod"), []byte("module fixture\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{cache, filepath.Dir(cache), filepath.Join(root, "gomod")} {
		if err := os.Chmod(directory, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, directory := range []string{filepath.Join(root, "gomod"), filepath.Join(root, "gomod", "cache"), filepath.Join(root, "gomod", "cache", "download")} {
			_ = os.Chmod(directory, 0o700)
		}
	})
	want := map[string]string{"README.md": "uncommitted", "notes.txt": "keep", "gomod/cache/download/go.mod": "module fixture\n"}
	check := func(stage string) {
		t.Helper()
		for name, content := range want {
			if body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))); err != nil || string(body) != content {
				t.Fatalf("%s: %s = %q, %v", stage, name, body, err)
			}
		}
		if info, err := os.Stat(filepath.Join(root, "gomod", "cache")); err != nil || info.Mode().Perm() != 0o555 {
			t.Fatalf("%s: directory mode %v, %v", stage, info, err)
		}
	}
	reclaim := func(stage string, at time.Time) {
		t.Helper()
		r, _ := e.repo.RunState(id)
		e.cleanupWorkspace(r, at)
		if r, _ = e.repo.RunState(id); r.WorkspaceCleanedAt.IsZero() {
			t.Fatalf("%s: workspace was not reclaimed", stage)
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatalf("%s: workspace left on disk: %v", stage, err)
		}
	}
	restore := func(stage string) {
		t.Helper()
		token, err := e.BeginConversationWorkspaceUse(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if err := e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
			t.Fatal(err)
		}
		check(stage)
	}
	reclaim("first reclaim", time.Now().UTC())
	restore("first restore")
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	reclaim("second reclaim", meta.LastUsedAt.Add(time.Hour))
	restore("second restore")
}
