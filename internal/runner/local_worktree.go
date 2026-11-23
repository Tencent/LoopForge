package runner

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func localGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, text)
	}
	return text, nil
}

// localWorktreeCommonDirs lists the main repositories whose worktrees live
// under a run directory, so cleanup can prune them after removing it. A local
// conversation workspace keeps them at <run>/workspace/<repo>, two levels down.
func localWorktreeCommonDirs(ctx context.Context, root string) []string {
	var out []string
	var visit func(dir string, depth int)
	visit = func(dir string, depth int) {
		if common := localWorktreeCommonDir(ctx, dir); common != "" {
			out = append(out, common)
			return
		}
		if depth == 0 {
			return
		}
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if entry.IsDir() {
				visit(filepath.Join(dir, entry.Name()), depth-1)
			}
		}
	}
	visit(root, 2)
	slices.Sort(out)
	return slices.Compact(out)
}

func localWorktreeCommonDir(ctx context.Context, dir string) string {
	file, err := os.Open(filepath.Join(dir, ".git"))
	if err != nil {
		return ""
	}
	line, _ := bufio.NewReader(file).ReadString('\n')
	file.Close()
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	common, err := localGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	return common
}

func pruneLocalWorktrees(ctx context.Context, commonDirs []string) {
	for _, common := range commonDirs {
		// An explicit --git-dir also works under safe.bareRepository=explicit,
		// which refuses running inside the common directory.
		if _, err := localGit(ctx, filepath.Dir(common), "--git-dir="+common, "worktree", "prune"); err != nil {
			log.Printf("prune local worktrees of %s: %v", common, err)
		}
	}
}
