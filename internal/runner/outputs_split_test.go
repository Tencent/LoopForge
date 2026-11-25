package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func TestPatchInventoryDirsDefaultsToWorkspaceRoot(t *testing.T) {
	for _, inv := range []*domain.RunnerInvocation{nil, {}} {
		if got := patchInventoryDirs(inv); !reflect.DeepEqual(got, []string{""}) {
			t.Fatalf("dirs = %q", got)
		}
	}
}

func TestPatchInventoryDirsListsRepositoryCheckouts(t *testing.T) {
	single := &domain.RunnerInvocation{Repository: &domain.RunnerRepository{Dir: "app"}}
	if got := patchInventoryDirs(single); !reflect.DeepEqual(got, []string{"app"}) {
		t.Fatalf("single repository dirs = %q", got)
	}
	multi := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{
		{Dir: "repos/api"}, {Dir: "../escape"}, {Dir: " "},
	}}
	if got := patchInventoryDirs(multi); !reflect.DeepEqual(got, []string{"repos/api", "", ""}) {
		t.Fatalf("multi repository dirs = %q", got)
	}
}

func TestReadOutputFilesKeepsNilWhenNothingQualifies(t *testing.T) {
	files, tooLarge := readOutputFiles(t.TempDir(), []string{"missing.md", "../outside.md"})
	if files != nil || tooLarge != nil {
		t.Fatalf("files = %v, tooLarge = %v", files, tooLarge)
	}
}

func TestCollectPatchInventoriesReportsOnlyChangedCheckouts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	workspace := t.TempDir()
	changed := filepath.Join(workspace, "changed")
	clean := filepath.Join(workspace, "clean")
	for _, dir := range []string{changed, clean} {
		initBaselineRepo(t, dir)
	}
	if err := os.WriteFile(filepath.Join(changed, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	patches := collectPatchInventories(workspace, []string{"changed", "clean", "plain"})
	if len(patches) != 1 || !strings.Contains(patches["changed"], "new.txt") {
		t.Fatalf("patches = %v", patches)
	}
	if got := collectPatchInventories(workspace, []string{"clean", "plain"}); got != nil {
		t.Fatalf("unchanged patches = %v", got)
	}
}

func initBaselineRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "add", "."},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "base"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := gitwork.RecordBaseline(dir, ""); err != nil {
		t.Fatal(err)
	}
}
