package gitwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Checkout is a git checkout found in a workspace: its remote URLs and the
// commits its local branches point at.
type Checkout struct {
	Dir     string   `json:"dir"`
	Remotes []string `json:"remotes"`
	Heads   []string `json:"heads"`
}

const (
	checkoutScanDepth = 2
	checkoutScanLimit = 50
)

// Checkouts lists the git checkouts at most two directory levels below root
// (root itself included). Agents clone wherever they like, so the bound
// repository list says nothing about them. The files may belong to a sandbox
// user, hence safe.directory; see checkoutGit for why that stays safe on the
// host that scans them.
func Checkouts(ctx context.Context, root string) []Checkout {
	dirs := []string{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if len(dirs) >= checkoutScanLimit {
			return
		}
		// Only a real .git directory: a .git file or symlink could point
		// the scan at a repository outside the workspace.
		if info, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
			return
		}
		if depth == checkoutScanDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() || strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	walk(root, 0)
	out := []Checkout{}
	for _, dir := range dirs {
		remotes, _ := checkoutGit(ctx, dir, "config", "--local", "--no-includes", "--get-regexp", `^remote\..*\.url$`)
		heads, err := checkoutGit(ctx, dir, "for-each-ref", "--format=%(objectname)", "refs/heads")
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, dir)
		checkout := Checkout{Dir: filepath.ToSlash(rel), Heads: uniqueLines(heads)}
		for _, line := range strings.Split(remotes, "\n") {
			if _, url, ok := strings.Cut(strings.TrimSpace(line), " "); ok && strings.TrimSpace(url) != "" {
				checkout.Remotes = append(checkout.Remotes, strings.TrimSpace(url))
			}
		}
		out = append(out, checkout)
	}
	return out
}

// checkoutGit reads a checkout the sandbox user controls from the platform or
// execution-machine host. safe.directory=* alone would let that repository's
// config run programs here, so it pins the repository to <dir>/.git, ignores
// the system config, and disables the config keys git executes (fsmonitor,
// hooks, pager, editor, askpass, credential helpers); config reads skip
// includes. Only read-only plumbing is run.
func checkoutGit(ctx context.Context, dir string, args ...string) (string, error) {
	base := []string{
		"--git-dir=" + filepath.Join(dir, ".git"), "--work-tree=" + dir,
		"-c", "safe.directory=*",
		"-c", "core.fsmonitor=",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.pager=cat",
		"-c", "core.editor=true",
		"-c", "core.askPass=",
		"-c", "credential.helper=",
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_ASKPASS=", "SSH_ASKPASS=")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func uniqueLines(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}
