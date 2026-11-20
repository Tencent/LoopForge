package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareBaselineFourSets(t *testing.T) {
	base := map[string]bool{
		"a.Still#fails":  true,
		"a.Fixed#passes": true,
		"a.Gone#deleted": true,
		"a.Pass#passes":  false,
	}
	current := map[string]bool{
		"a.Still#fails":  true,
		"a.Fixed#passes": false,
		"a.Pass#passes":  false,
		"a.New#fails":    true,
	}
	diff := CompareBaseline(base, current)
	if strings.Join(diff.NewlyFailing, ",") != "a.New#fails" {
		t.Fatalf("newly failing = %v", diff.NewlyFailing)
	}
	if strings.Join(diff.NewlyPassing, ",") != "a.Fixed#passes" {
		t.Fatalf("newly passing = %v", diff.NewlyPassing)
	}
	if strings.Join(diff.StillFailing, ",") != "a.Still#fails" {
		t.Fatalf("still failing = %v", diff.StillFailing)
	}
	if strings.Join(diff.Disappeared, ",") != "a.Gone#deleted" {
		t.Fatalf("disappeared = %v", diff.Disappeared)
	}
	if blocking := diff.Blocking(map[string]bool{"a.New#fails": true}); len(blocking) != 0 {
		t.Fatalf("flaky test still blocked: %v", blocking)
	}
	if blocking := diff.Blocking(nil); len(blocking) != 1 {
		t.Fatalf("blocking = %v", blocking)
	}
}

func TestFlakyTestsNeedThreeRuns(t *testing.T) {
	flaky, always := FlakyTests(map[string][]bool{
		"Flaky#t":   {true, false, true},
		"Always#t":  {true, true, true},
		"Passing#t": {false, false, false},
		"Two#t":     {true, false},
	})
	if strings.Join(flaky, ",") != "Flaky#t" {
		t.Fatalf("flaky = %v", flaky)
	}
	if strings.Join(always, ",") != "Always#t" {
		t.Fatalf("always failing = %v", always)
	}
}

func TestPrepareBaseDirMaterialisesBaseSources(t *testing.T) {
	repo := testRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "base")
	base := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := PrepareBaseDir(context.Background(), repo, base)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(content) != "base\n" {
		t.Fatalf("base worktree content = %q (%v)", content, err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("base worktree not removed: %v", err)
	}
	_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
}
