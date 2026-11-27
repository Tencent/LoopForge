package gitwork

import (
	"context"
	"fmt"
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
	Origin  string   `json:"origin,omitempty"`
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
	return checkouts(ctx, root, false)
}

// CheckoutsIncludingNested also scans below each checkout. A conversation can
// clone an independent repository inside an existing checkout for a LOOP.
func CheckoutsIncludingNested(ctx context.Context, root string) []Checkout {
	return checkouts(ctx, root, true)
}

func checkouts(ctx context.Context, root string, includeNested bool) []Checkout {
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
			if !includeNested {
				return
			}
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
			if key, url, ok := strings.Cut(strings.TrimSpace(line), " "); ok && strings.TrimSpace(url) != "" {
				url = strings.TrimSpace(url)
				checkout.Remotes = append(checkout.Remotes, url)
				if key == "remote.origin.url" {
					checkout.Origin = url
				}
			}
		}
		out = append(out, checkout)
	}
	return out
}

// SameCheckoutOrigin compares the configured URL or local path with origin,
// without requiring the same spelling of the optional .git suffix.
func SameCheckoutOrigin(origin, remote string) bool {
	origin, remote = strings.TrimSpace(origin), strings.TrimSpace(remote)
	if filepath.IsAbs(origin) || filepath.IsAbs(remote) {
		return filepath.Clean(origin) == filepath.Clean(remote)
	}
	normalize := func(raw string) string {
		return strings.TrimSuffix(strings.TrimRight(raw, "/"), ".git")
	}
	origin, remote = normalize(origin), normalize(remote)
	return origin != "" && origin == remote
}

// ExistingSOPCheckout checks an occupied checkout target before LOOP checkout
// is allowed to modify it. An unrelated repository or a nonempty directory
// is left intact.
func ExistingSOPCheckout(ctx context.Context, dir, remote string) (bool, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("检出路径 %q 不是普通目录", filepath.Base(dir))
	}
	if gitDir, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && gitDir.IsDir() {
		origin, err := checkoutGit(ctx, dir, "config", "--local", "--no-includes", "--get", "remote.origin.url")
		if err != nil || !SameCheckoutOrigin(origin, remote) {
			return false, fmt.Errorf("检出目录 %q 已有其他来源的仓库", filepath.Base(dir))
		}
		return true, nil
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) > 0 {
		err = fmt.Errorf("检出目录 %q 已有文件，请整理工作区后重试", filepath.Base(dir))
	}
	return false, err
}

// ContinueSOPCheckout starts the generated work branch at the conversation's
// current HEAD. Use a regular checkout so staged and unstaged edits survive.
func ContinueSOPCheckout(ctx context.Context, dir, branch string) error {
	if branch == "" {
		return nil
	}
	current, _ := checkoutGit(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	if current == "refs/heads/"+branch {
		return nil
	}
	args := []string{"checkout", "--quiet", "-b", branch}
	if _, err := checkoutGit(ctx, dir, "show-ref", "--verify", "refs/heads/"+branch); err == nil {
		args = []string{"checkout", "--quiet", branch}
	}
	if _, err := checkoutGit(ctx, dir, args...); err != nil {
		return fmt.Errorf("保留对话改动时无法切换到工作分支 %s：%w", branch, err)
	}
	return nil
}

// checkoutGit reads a checkout the sandbox user controls from the platform or
// execution-machine host. safe.directory=* alone would let that repository's
// config run programs here, so it pins the repository to <dir>/.git, ignores
// the system config, and disables the config keys git executes (fsmonitor,
// hooks, pager, editor, askpass, credential helpers); config reads skip
// includes. Checkout uses the same restrictions and never forces a switch.
func checkoutGit(ctx context.Context, dir string, args ...string) (string, error) {
	if run := workspaceExecutor(ctx); run != nil {
		out, err := run(ctx, dir, WorkspaceCommand{Args: append([]string{"--git-dir=.git", "--work-tree=."}, args...), StdoutOnly: true})
		return strings.TrimSpace(out), err
	}
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
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_ASKPASS=", "SSH_ASKPASS=")
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
