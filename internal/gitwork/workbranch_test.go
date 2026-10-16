package gitwork

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageChangesEmptyAndExcludedCheckouts(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		name := "empty"
		if tracked {
			name = "only-excluded-tracked-files"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			ctx := context.Background()
			if _, err := C(ctx, workspace, "init", "-q"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(workspace, "config", "mcporter.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tracked {
				if _, err := C(ctx, workspace, "add", "config/mcporter.json"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := C(ctx, workspace, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			staged, err := C(ctx, workspace, "diff", "--cached", "--name-only")
			if err != nil || strings.TrimSpace(staged) != "" {
				t.Fatalf("excluded content was staged: %q, %v", staged, err)
			}
			// A new source file must still be staged in either checkout.
			if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			// Preserve tracked updates and deletions, including the last source.
			if _, err := C(ctx, workspace, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "source"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("updated"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			if _, err := C(ctx, workspace, "diff", "--cached", "--exit-code"); err == nil {
				t.Fatal("source update was not staged")
			}
			if err := os.Remove(filepath.Join(workspace, "source.txt")); err != nil {
				t.Fatal(err)
			}
			if err := StageChanges(workspace); err != nil {
				t.Fatal(err)
			}
			deleted, err := C(ctx, workspace, "diff", "--cached", "--name-status")
			if err != nil || strings.TrimSpace(deleted) != "D\tsource.txt" {
				t.Fatalf("source deletion was not staged correctly: %q, %v", deleted, err)
			}
		})
	}
}
