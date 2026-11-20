package verification

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PrepareRunDir copies repo to a temporary directory and, when a base commit
// and test roots are given, restores those roots from the base commit there.
// Tests then run against the original test sources, so an agent cannot weaken
// them in the working tree before grading. The source repo is never modified.
func PrepareRunDir(ctx context.Context, repo, baseCommit string, testRoots []string) (string, func(), error) {
	base := strings.TrimSpace(baseCommit)
	if base != "" {
		if _, err := git(ctx, repo, "cat-file", "-e", base+"^{commit}"); err != nil {
			return "", nil, fmt.Errorf("base commit %s not found: %w", base, err)
		}
	}
	dir, err := os.MkdirTemp("", "loopforge-verify-run-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	copyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	copyCmd := exec.CommandContext(copyCtx, "cp", "-R", filepath.Clean(repo)+"/.", dir)
	if output, err := copyCmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy repo for verification: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if base != "" && len(testRoots) > 0 {
		checkoutCtx, cancelCheckout := context.WithTimeout(ctx, time.Minute)
		defer cancelCheckout()
		cmd := exec.CommandContext(checkoutCtx, "git", append([]string{"-C", dir, "checkout", base, "--"}, testRoots...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("restore test roots from %s: %v: %s", base, err, strings.TrimSpace(string(output)))
		}
	}
	return dir, cleanup, nil
}
