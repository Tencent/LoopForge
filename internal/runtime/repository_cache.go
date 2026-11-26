package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

var repositoryCacheRoot atomic.Value

// SetRepositoryCacheRoot uses a private host temporary directory. The cache
// is outside the workspaces and hidden by native sandboxes. Namespacing by
// workspace root keeps repository IDs from different deployments separate.
func SetRepositoryCacheRoot(workspaceRoot string) error {
	repositoryCacheRoot.Store("")
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil
	}
	workspace, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return err
	}
	parent := fmt.Sprintf("/tmp/.loopforge-repository-cache-%d", os.Geteuid())
	if err := os.Mkdir(parent, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("repository cache directory must be private and owned by the running user")
	}
	scope := sha256.Sum256([]byte(workspace))
	repositoryCacheRoot.Store(filepath.Join(parent, fmt.Sprintf("%x", scope[:16])))
	return nil
}

// RepositoryCacheRoot is the host path selected by SetRepositoryCacheRoot.
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
// the host repository cache. Docker workspaces still use the remote.
func WithRepositoryCacheFor(ctx context.Context, driver string) context.Context {
	if !NativeSandboxDriver(driver) {
		return ctx
	}
	return gitwork.WithRepositoryCache(ctx, RepositoryCacheRoot())
}

// WithRepositoryCacheIn is WithRepositoryCacheFor the driver of sb.
func WithRepositoryCacheIn(ctx context.Context, sb Sandbox) context.Context {
	if sb == nil {
		return ctx
	}
	return WithRepositoryCacheFor(ctx, sandboxDriver(sb))
}
