package runtime

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

// RepositoryCacheDir is the repository cache under a workspace root. Workspace
// cleanup skips dot directories, so the cache outlives the runs that use it.
const RepositoryCacheDir = ".repository-cache"

var repositoryCacheRoot atomic.Value

// SetRepositoryCacheRoot names this process's repository cache: the platform
// and each execution machine keep their own, next to their workspaces.
func SetRepositoryCacheRoot(workspaceRoot string) {
	root := ""
	if strings.TrimSpace(workspaceRoot) != "" {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			root = filepath.Join(abs, RepositoryCacheDir)
		}
	}
	repositoryCacheRoot.Store(root)
}

// RepositoryCacheRoot is the root SetRepositoryCacheRoot chose, or empty.
func RepositoryCacheRoot() string {
	root, _ := repositoryCacheRoot.Load().(string)
	return root
}

// NativeSandboxDriver reports whether driver is the OS process sandbox, whose
// workspaces see the host filesystem read-only.
func NativeSandboxDriver(driver string) bool {
	switch strings.TrimSpace(driver) {
	case domain.SandboxDriverNative, domain.SandboxDriverSeatbelt, domain.SandboxDriverBwrap:
		return true
	}
	return false
}

// WithRepositoryCacheFor lets checkouts for a workspace in driver start from
// the repository cache. Docker workspaces keep full clones: the container
// cannot reach the cache by its host path.
func WithRepositoryCacheFor(ctx context.Context, driver string) context.Context {
	if !NativeSandboxDriver(driver) {
		return ctx
	}
	return gitwork.WithRepositoryCache(ctx, RepositoryCacheRoot())
}

const repositoryCacheWarmInterval = 5 * time.Minute

var warmedRepositoryCaches sync.Map

// WarmRepositoryCaches fetches the caches of repositories in the background,
// each at most once per interval, so an Agent's later shared clone of them
// needs no network. ctx carries the cache root and Git credentials.
func WarmRepositoryCaches(ctx context.Context, repositories []domain.RunnerRepository) {
	root := gitwork.RepositoryCacheFrom(ctx)
	if root == "" || len(repositories) == 0 {
		return
	}
	ctx = procwatch.WithObserver(context.WithoutCancel(ctx), func(procwatch.Event) {})
	go func() {
		for _, repository := range repositories {
			dir, remote := gitwork.RepositoryCachePath(root, repository.RepositoryID), strings.TrimSpace(repository.CloneURL)
			if dir == "" || remote == "" {
				continue
			}
			if last, ok := warmedRepositoryCaches.Load(dir); ok && time.Since(last.(time.Time)) < repositoryCacheWarmInterval {
				continue
			}
			warmedRepositoryCaches.Store(dir, time.Now())
			repositoryCtx := gitwork.WithHTTPRemote(ctx, remote)
			if repository.UseSSH {
				repositoryCtx = gitwork.WithSSHRemote(ctx, remote)
			}
			watch := procwatch.Options{
				Stage: "clone", Label: "更新仓库缓存 " + cmp.Or(strings.TrimSpace(repository.Name), repository.RepositoryID),
				Stall: procwatch.DurationEnv(gitwork.CloneStallEnv, 5*time.Minute), StallEnv: gitwork.CloneStallEnv,
				Timeout: procwatch.DurationEnv(gitwork.CloneTimeoutEnv, 30*time.Minute), TimeoutEnv: gitwork.CloneTimeoutEnv,
			}
			if _, err := gitwork.RefreshRepositoryCache(repositoryCtx, root, repository.RepositoryID, remote, watch); err != nil {
				warmedRepositoryCaches.Delete(dir)
				log.Printf("repository cache %s: %v", repository.RepositoryID, err)
			}
		}
	}()
}

// dissociateSharedClones copies the borrowed cache objects into each shared
// clone at root or one level below it, so a checkpoint of the workspace
// restores on a machine without that cache.
func dissociateSharedClones(ctx context.Context, root string) error {
	dirs := []string{root}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	for _, dir := range dirs {
		alternates := filepath.Join(dir, ".git", "objects", "info", "alternates")
		if info, err := os.Lstat(alternates); err != nil || !info.Mode().IsRegular() {
			continue
		}
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "repack", "-a", "-d", "-q")
		cmd.Env = gitwork.WithEnvValue(gitwork.Env(""), "GIT_CONFIG_NOSYSTEM", "1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("copy repository objects into %s: %w (%s)", filepath.Base(dir), err, strings.TrimSpace(string(out)))
		}
		if err := os.Remove(alternates); err != nil {
			return err
		}
	}
	return nil
}

// WithRepositoryCacheIn is WithRepositoryCacheFor the driver of sb.
func WithRepositoryCacheIn(ctx context.Context, sb Sandbox) context.Context {
	if sb == nil {
		return ctx
	}
	return WithRepositoryCacheFor(ctx, sandboxDriver(sb))
}
