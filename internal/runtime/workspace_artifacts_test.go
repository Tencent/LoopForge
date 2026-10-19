package runtime

import (
	"agentflow/internal/domain"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRestoreMissingArtifactsPreservesExistingAndRaces(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "change-report.md"), []byte("new group edits"), 0644); err != nil {
		t.Fatal(err)
	}
	files := []domain.WorkspaceArtifactFile{{Path: "docs/design.md", Content: "archived design", SHA256: WorkspaceContentSHA("archived design")}, {Path: "change-report.md", Content: "old report", SHA256: WorkspaceContentSHA("old report")}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := RestoreMissingWorkspaceArtifacts(root, files); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for name, want := range map[string]string{"docs/design.md": "archived design", "change-report.md": "new group edits"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %s %v", name, got, err)
		}
	}
	again, err := RestoreMissingWorkspaceArtifacts(root, files)
	if err != nil || len(again) != 0 {
		t.Fatalf("not idempotent: %v %v", again, err)
	}
}
func TestRestoreMissingArtifactsRejectsUnsafeAndCorruptFiles(t *testing.T) {
	for _, path := range []string{"../escape", "/tmp/escape", ".git/config", "sub/.env"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			_, err := RestoreMissingWorkspaceArtifacts(root, []domain.WorkspaceArtifactFile{{Path: path, Content: "x", SHA256: WorkspaceContentSHA("x")}})
			if err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	root := t.TempDir()
	_, err := RestoreMissingWorkspaceArtifacts(root, []domain.WorkspaceArtifactFile{{Path: "first.md", Content: "valid", SHA256: WorkspaceContentSHA("valid")}, {Path: "bad.md", Content: "corrupted", SHA256: WorkspaceContentSHA("original")}})
	if err == nil {
		t.Fatal("bad digest accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "first.md")); !os.IsNotExist(err) {
		t.Fatal("validation made a partial write")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	_, _ = RestoreMissingWorkspaceArtifacts(root, []domain.WorkspaceArtifactFile{{Path: "link/report.md", Content: "x", SHA256: WorkspaceContentSHA("x")}})
	if _, err := os.Stat(filepath.Join(outside, "report.md")); !os.IsNotExist(err) {
		t.Fatal("escaped through symlink")
	}
}
