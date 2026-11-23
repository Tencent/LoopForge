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

type repositoryCacheKey struct{}

// WithRepositoryCache lets clones in ctx start from the bare repository cache
// under root. The host fetches and copies reachable objects before running
// commands in the sandbox.
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

// CloneFromRepositoryCache authenticates to the remote and makes an
// independent checkout of branch using the refreshed cache as the transport.
// The destination must be empty. The cache is never needed at runtime.
func CloneFromRepositoryCache(ctx context.Context, root, repositoryID, remote, branch, workspace string, singleBranch bool, watch procwatch.Options) error {
	dir := RepositoryCachePath(root, repositoryID)
	if dir == "" {
		return fmt.Errorf("仓库 %q 不能用作缓存目录名", repositoryID)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("repository cache checkout requires a ref")
	}
	if err := ValidateRemoteURL(remote, true); err != nil {
		return err
	}
	if err := requireEmptyCloneDirectory(workspace); err != nil {
		return err
	}
	return procwatch.WithinTimeout(ctx, watch, func(ctx context.Context) (err error) {
		unlock, err := lockRepositoryCache(ctx, dir)
		if err != nil {
			return err
		}
		defer unlock()
		if err := refreshLockedCache(ctx, dir, remote, watch); err != nil {
			return err
		}
		if err := requireEmptyCloneDirectory(workspace); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				_ = emptyDirectory(workspace)
			}
		}()

		// Use Git's transport so the worktree gets its own objects and only
		// reaches the refs advertised in the authenticated fetch.
		args := []string{"clone", "--progress", "--no-local", "--branch", branch}
		if singleBranch {
			args = append(args, "--single-branch")
		}
		args = append(args, "--", dir, ".")
		watch.Redact = func(line string) string {
			return redactContextGitSecrets(ctx, Redact(line, TokenFrom(ctx)))
		}
		tail, err := procwatch.Run(ctx, func(ctx context.Context) *exec.Cmd {
			return localGitCommand(ctx, "-C", workspace, args...)
		}, watch)
		if err != nil {
			return fmt.Errorf("git clone from repository cache: %w (%s)", err, tail)
		}
		return localGit(ctx, "-C", workspace, "remote", "set-url", "origin", remote)
	})
}

func refreshLockedCache(ctx context.Context, dir, remote string, watch procwatch.Options) error {
	configured, err := localGitOutput(ctx, "--git-dir", dir, "config", "--local", "--get", "remote.origin.url")
	if err := ctx.Err(); err != nil {
		return err
	}
	if err != nil || configured != remote {
		return replaceLockedCache(ctx, dir, remote, watch)
	}
	watch.Label = strings.Replace(watch.Label, "克隆", "更新", 1)
	if _, err := Watch(ctx, dir, watch, "-C", dir, "fetch", "--progress", "--prune", "--force", "--", remote, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	// Fetch updates refs but leaves the bare repository's symbolic HEAD intact.
	// The subsequent clone uses it to construct origin/HEAD.
	advertised, err := Watch(ctx, dir, watch, "-C", dir, "ls-remote", "--symref", "--", remote, "HEAD")
	if err != nil {
		return fmt.Errorf("git ls-remote HEAD: %w", err)
	}
	for _, line := range strings.Split(advertised, "\n") {
		head, ok := strings.CutSuffix(line, "\tHEAD")
		if !ok || !strings.HasPrefix(head, "ref: refs/heads/") {
			continue
		}
		ref := strings.TrimPrefix(head, "ref: ")
		if err := localGit(ctx, "--git-dir", dir, "show-ref", "--verify", "--quiet", ref); err != nil {
			return err
		}
		return localGit(ctx, "--git-dir", dir, "symbolic-ref", "HEAD", ref)
	}
	return fmt.Errorf("remote does not advertise a symbolic HEAD")
}

func replaceLockedCache(ctx context.Context, dir, remote string, watch procwatch.Options) error {
	staging, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if _, err := Watch(ctx, filepath.Dir(dir), watch, "clone", "--bare", "--no-local", "--progress", "--", remote, staging); err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}
	// Use an explicit Git directory for hosts with safe.bareRepository=explicit.
	if err := localGit(ctx, "--git-dir", staging, "config", "gc.auto", "0"); err != nil {
		return err
	}
	if err := localGit(ctx, "--git-dir", staging, "config", "maintenance.auto", "false"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

func requireEmptyCloneDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("repository checkout directory is not empty")
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
	_, err := localGitOutput(ctx, dirOption, dir, args...)
	return err
}

func localGitCommand(ctx context.Context, dirOption, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{dirOption, dir}, args...)...)
	cmd.Env = WithEnvValue(Env(""), "GIT_CONFIG_NOSYSTEM", "1")
	cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_GLOBAL", os.DevNull)
	cmd.Env = WithEnvValue(cmd.Env, "GIT_CONFIG_PARAMETERS", "")
	return cmd
}

func localGitOutput(ctx context.Context, dirOption, dir string, args ...string) (string, error) {
	out, err := localGitCommand(ctx, dirOption, dir, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", args[0], err, text)
	}
	return text, nil
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
