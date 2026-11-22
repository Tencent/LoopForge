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
	"sync"

	"agentflow/internal/domain"
)

// localRepositoryLocks serializes clone, fetch and worktree changes per main
// repository: two workspaces of one machine may share it.
var localRepositoryLocks sync.Map

func lockLocalRepository(path string) func() {
	value, _ := localRepositoryLocks.LoadOrStore(path, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// checkoutWorkspaceRepositories prepares a workspace's repositories. A local
// sandbox opens git worktrees of the machine's own checkouts; every other
// sandbox clones into the workspace.
func checkoutWorkspaceRepositories(ctx context.Context, root string, request domain.RunnerWorkspaceRequest, refresh bool) error {
	if request.Sandbox != nil && request.Sandbox.Profile.Driver == domain.SandboxDriverLocal {
		return checkoutLocalWorktrees(ctx, root, request.PreparationInvocation, request.Sandbox.Profile)
	}
	return checkoutInvocationRepositories(ctx, root, request.PreparationInvocation, refresh)
}

// checkoutLocalWorktrees adds one worktree per repository. Git runs with the
// machine's own configuration and credentials, the same as its owner's shell;
// the main checkout's files are never touched.
func checkoutLocalWorktrees(ctx context.Context, workspace string, inv *domain.RunnerInvocation, profile domain.SandboxProfile) error {
	for _, repo := range invocationRepositories(inv) {
		if strings.TrimSpace(repo.CloneURL) == "" {
			continue
		}
		target := workspace
		if dir := safeRunnerRepoDir(repo.Dir); dir != "" {
			target = filepath.Join(workspace, dir)
		}
		if _, err := os.Lstat(filepath.Join(target, ".git")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		main, configured, err := localMainRepository(profile, repo)
		if err != nil {
			return err
		}
		if err := addLocalWorktree(ctx, main, configured, target, repo); err != nil {
			return fmt.Errorf("%s: %w", repositoryLabel(repo), err)
		}
	}
	return nil
}

func repositoryLabel(repo domain.RunnerRepository) string {
	if name := strings.TrimSpace(repo.Name); name != "" {
		return name
	}
	return domain.StripURLCredentials(repo.CloneURL)
}

// localMainRepository is the configured checkout of the repository, or
// <base dir>/<repository name>.
func localMainRepository(profile domain.SandboxProfile, repo domain.RunnerRepository) (string, bool, error) {
	if path := strings.TrimSpace(profile.RepositoryPaths[repo.RepositoryID]); path != "" {
		expanded, err := expandLocalPath(path)
		return expanded, true, err
	}
	base := strings.TrimSpace(profile.BaseDir)
	if base == "" {
		base = domain.LocalSandboxDefaultBaseDir
	}
	expanded, err := expandLocalPath(base)
	if err != nil {
		return "", false, err
	}
	name := filepath.Base(filepath.Clean(strings.TrimSpace(repo.Name)))
	if name == "." || name == ".." || name == string(filepath.Separator) || strings.HasPrefix(name, ".") {
		name = filepath.Base(filepath.Clean(strings.TrimSpace(repo.RepositoryID)))
	}
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
		return "", false, fmt.Errorf("仓库缺少可用作目录名的名称")
	}
	return filepath.Join(expanded, name), false, nil
}

func expandLocalPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("local 路径必须是绝对路径或以 ~/ 开头：%s", path)
	}
	return filepath.Clean(path), nil
}

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

func addLocalWorktree(ctx context.Context, main string, configured bool, target string, repo domain.RunnerRepository) error {
	unlock := lockLocalRepository(main)
	defer unlock()
	if err := ensureLocalMainRepository(ctx, main, configured, repo); err != nil {
		return err
	}
	if _, err := localGit(ctx, main, "fetch", "--prune", "origin"); err != nil {
		return err
	}
	// A worktree whose directory is gone (an interrupted cleanup, a manual
	// delete) still holds its branch until pruned.
	if _, err := localGit(ctx, main, "worktree", "prune"); err != nil {
		return err
	}
	base := "origin/HEAD"
	if ref := strings.TrimSpace(repo.Ref); ref != "" {
		base = "origin/" + ref
	}
	if _, err := localGit(ctx, main, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return fmt.Errorf("主仓库 %s 没有 %s", main, base)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	branch := strings.TrimSpace(repo.WorkBranch)
	if branch == "" {
		_, err := localGit(ctx, main, "worktree", "add", "--detach", target, base)
		return err
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("无效的工作分支 %q", branch)
	}
	if _, err := localGit(ctx, main, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("无效的工作分支 %q", branch)
	}
	if _, err := localGit(ctx, main, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err = localGit(ctx, main, "worktree", "add", target, branch)
		return err
	}
	_, remoteErr := localGit(ctx, main, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	remote := remoteErr == nil
	switch {
	case remote && repo.CreateNewWorkBranch:
		return fmt.Errorf("远端已存在同名分支 %s", branch)
	case remote:
		_, err := localGit(ctx, main, "worktree", "add", "--track", "-b", branch, target, "origin/"+branch)
		return err
	case repo.RequireExistingWorkBranch:
		return fmt.Errorf("远端不存在要继续的分支 %s", branch)
	default:
		_, err := localGit(ctx, main, "worktree", "add", "--no-track", "-b", branch, target, base)
		return err
	}
}

// ensureLocalMainRepository requires a configured path to be an existing
// checkout, and clones an unconfigured repository into the base directory once.
func ensureLocalMainRepository(ctx context.Context, main string, configured bool, repo domain.RunnerRepository) error {
	entries, err := os.ReadDir(main)
	switch {
	case err == nil && len(entries) > 0:
		if _, err := localGit(ctx, main, "rev-parse", "--git-dir"); err != nil {
			return fmt.Errorf("仓库路径 %s 不是 git 仓库", main)
		}
		return nil
	case configured:
		return fmt.Errorf("仓库路径 %s 不存在或为空", main)
	case err != nil && !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		return err
	}
	args := []string{"clone"}
	if ref := strings.TrimSpace(repo.Ref); ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, "--", strings.TrimSpace(repo.CloneURL), main)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %w (%s)", domain.StripURLCredentials(repo.CloneURL), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// localWorktreeCommonDirs lists the main repositories whose worktrees live
// under a run directory, so cleanup can prune them after removing it. A
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
		unlock := lockLocalRepository(filepath.Dir(common))
		// An explicit --git-dir also works under safe.bareRepository=explicit,
		// which refuses running inside the common directory.
		if _, err := localGit(ctx, filepath.Dir(common), "--git-dir="+common, "worktree", "prune"); err != nil {
			log.Printf("prune local worktrees of %s: %v", common, err)
		}
		unlock()
	}
}
