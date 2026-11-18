package verification

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BaselineDiff is AegisOS's four-set comparison between the base commit's test
// verdicts and the working tree's. Tests that fail in both are pre-existing;
// only newly failing tests block.
type BaselineDiff struct {
	NewlyFailing []string `json:"newlyFailing,omitempty"`
	NewlyPassing []string `json:"newlyPassing,omitempty"`
	StillFailing []string `json:"stillFailing,omitempty"`
	Disappeared  []string `json:"disappeared,omitempty"`
}

// Blocking returns the newly failing tests that are not flaky. AegisOS blocks
// on exactly this set.
func (d BaselineDiff) Blocking(flaky map[string]bool) []string {
	var blocking []string
	for _, name := range d.NewlyFailing {
		if !flaky[name] {
			blocking = append(blocking, name)
		}
	}
	return blocking
}

// CompareBaseline compares base and current per-test verdicts (true = failed).
func CompareBaseline(base, current map[string]bool) BaselineDiff {
	var diff BaselineDiff
	for name, failed := range current {
		was, existed := base[name]
		switch {
		case failed && (!existed || !was):
			diff.NewlyFailing = append(diff.NewlyFailing, name)
		case !failed && existed && was:
			diff.NewlyPassing = append(diff.NewlyPassing, name)
		case failed && existed && was:
			diff.StillFailing = append(diff.StillFailing, name)
		}
	}
	for name := range base {
		if _, ok := current[name]; !ok {
			diff.Disappeared = append(diff.Disappeared, name)
		}
	}
	sort.Strings(diff.NewlyFailing)
	sort.Strings(diff.NewlyPassing)
	sort.Strings(diff.StillFailing)
	sort.Strings(diff.Disappeared)
	return diff
}

// FlakyTests separates tests whose verdicts disagree across at least three
// runs (flaky) from tests that failed in every run (always failing). Two runs
// cannot prove either way.
func FlakyTests(histories map[string][]bool) (flaky, alwaysFailing []string) {
	for name, verdicts := range histories {
		if len(verdicts) < 3 {
			continue
		}
		anyFailed := false
		anyPassed := false
		for _, failed := range verdicts {
			if failed {
				anyFailed = true
			} else {
				anyPassed = true
			}
		}
		if anyFailed && anyPassed {
			flaky = append(flaky, name)
		} else if anyFailed {
			alwaysFailing = append(alwaysFailing, name)
		}
	}
	sort.Strings(flaky)
	sort.Strings(alwaysFailing)
	return flaky, alwaysFailing
}

// PrepareBaseDir materialises the base commit in a detached worktree so the
// baseline run grades the base sources, not the working tree.
func PrepareBaseDir(ctx context.Context, repo, base string) (string, func(), error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil, fmt.Errorf("base commit is required")
	}
	dir, err := os.MkdirTemp("", "loopforge-baseline-*")
	if err != nil {
		return "", nil, err
	}
	_ = os.Remove(dir) // git worktree add wants to create it
	cleanup := func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(removeCtx, "git", "-C", repo, "worktree", "remove", "--force", dir).Run()
		_ = os.RemoveAll(dir)
	}
	addCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(addCtx, "git", "-C", filepath.Clean(repo), "worktree", "add", "--detach", dir, base).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("prepare base worktree at %s: %v: %s", base, err, strings.TrimSpace(string(output)))
	}
	return dir, cleanup, nil
}
