package gitwork

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/procwatch"
)

// RepositoryCacheEnv names the repository cache root inside a native sandbox.
const RepositoryCacheEnv = "LOOPFORGE_REPOSITORY_CACHE"

type repositoryCacheKey struct{}

// WithRepositoryCache lets clones in ctx start from the bare repository cache
// under root. Only workspaces that see the host filesystem (native sandboxes)
// may use it: a shared clone refers to the cache's objects by host path.
func WithRepositoryCache(ctx context.Context, root string) context.Context {
	if strings.TrimSpace(root) == "" {
		return ctx
	}
	return context.WithValue(ctx, repositoryCacheKey{}, root)
}

// RepositoryCacheFrom is the cache root WithRepositoryCache put in ctx.
func RepositoryCacheFrom(ctx context.Context) string {
	root, _ := ctx.Value(repositoryCacheKey{}).(string)
	return root
}

var repositoryCacheName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// RepositoryCachePath is the bare cache of one repository record under root.
// Empty when the ID cannot be a directory name.
func RepositoryCachePath(root, repositoryID string) string {
	id := strings.TrimSpace(repositoryID)
	if strings.TrimSpace(root) == "" || !repositoryCacheName.MatchString(id) || strings.Contains(id, "..") {
		return ""
	}
	return filepath.Join(root, id+".git")
}

// RefreshRepositoryCache creates or fetches the bare cache of repositoryID
// from remote and returns its path. Branches mirror the remote's heads; the
// cache never runs gc, because shared clones borrow its objects.
func RefreshRepositoryCache(ctx context.Context, root, repositoryID, remote string, watch procwatch.Options) (string, error) {
	dir := RepositoryCachePath(root, repositoryID)
	if dir == "" {
		return "", fmt.Errorf("仓库 %q 不能用作缓存目录名", repositoryID)
	}
	if err := ValidateRemoteURL(remote, true); err != nil {
		return "", err
	}
	unlock, err := lockRepositoryCache(ctx, dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	return dir, refreshLockedCache(ctx, dir, remote, watch)
}

// CloneFromRepositoryCache refreshes the cache, then makes the empty
// workspace a shared clone of it whose origin is remote again. branch empty
// checks out the remote's default branch. On failure workspace is emptied
// again, ready for a full clone.
func CloneFromRepositoryCache(ctx context.Context, root, repositoryID, remote, branch, workspace string, watch procwatch.Options) (err error) {
	dir := RepositoryCachePath(root, repositoryID)
	if dir == "" {
		return fmt.Errorf("仓库 %q 不能用作缓存目录名", repositoryID)
	}
	if err := ValidateRemoteURL(remote, true); err != nil {
		return err
	}
	unlock, err := lockRepositoryCache(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := refreshLockedCache(ctx, dir, remote, watch); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = emptyDirectory(workspace)
		}
	}()
	args := []string{"clone", "--quiet", "--shared"}
	if branch = strings.TrimSpace(branch); branch != "" {
		args = append(args, "--branch", branch)
	}
	if err := localGit(ctx, "-C", workspace, append(args, "--", dir, ".")...); err != nil {
		return err
	}
	return localGit(ctx, "-C", workspace, "remote", "set-url", "origin", remote)
}

func refreshLockedCache(ctx context.Context, dir, remote string, watch procwatch.Options) error {
	if out, err := exec.CommandContext(ctx, "git", "--git-dir", dir, "rev-parse", "--is-bare-repository").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".tmp-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if _, err := Watch(ctx, filepath.Dir(dir), watch, "clone", "--bare", "--progress", "--", remote, tmp); err != nil {
			return fmt.Errorf("git clone --bare: %w", err)
		}
		// Name the bare Git directory explicitly for hosts with
		// safe.bareRepository=explicit.
		for _, setting := range [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}, {"gc.pruneExpire", "never"}, {"fetch.prune", "false"}} {
			if err := localGit(ctx, "--git-dir", tmp, "config", setting[0], setting[1]); err != nil {
				return err
			}
		}
		return os.Rename(tmp, dir)
	}
	if err := localGit(ctx, "--git-dir", dir, "remote", "set-url", "origin", remote); err != nil {
		return err
	}
	watch.Label = strings.Replace(watch.Label, "克隆", "更新", 1)
	if _, err := Watch(ctx, dir, watch, "-C", dir, "fetch", "--progress", "--prune", "--force", "--", remote, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	return nil
}

// emptyDirectory removes the contents of dir but keeps dir itself, whose
// inode a sandbox may have bound.
func emptyDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func localGit(ctx context.Context, dirOption, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{dirOption, dir}, args...)...)
	cmd.Env = WithEnvValue(Env(""), "GIT_CONFIG_NOSYSTEM", "1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w (%s)", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func lockRepositoryCache(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
