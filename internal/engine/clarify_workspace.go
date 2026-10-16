package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
)

// PrepareClarifyWorkspace ensures a local, up-to-date checkout of the given
// repository exists at cacheDir for use during requirement clarification.
//
// Behaviour:
//   - If cacheDir already holds a git checkout, it is refreshed to the tip of
//     the base branch (equivalent to a clean "git pull").
//   - Otherwise the repository is shallow-cloned from its clone URL.
//
// The base branch is repo.DefaultBranch when set, falling back to "master".
// Authentication reuses the same git-token plumbing as SOP execution, so the
// caller only needs to supply the resolved token (see ResolveClarifyGitToken).
//
// On success the checkout directory (cacheDir) is returned.
func PrepareClarifyWorkspace(ctx context.Context, cacheDir string, repo domain.Repository, token string) (string, error) {
	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		return "", fmt.Errorf("repository %q has no cloneUrl", repo.ID)
	}
	baseBranch := strings.TrimSpace(repo.DefaultBranch)
	if baseBranch == "" {
		baseBranch = "master"
	}

	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("需求责任人未配置 Git Token（未指定责任人时用项目 Owner），请让该账号到「个人设置 → 个人凭证」填写。新建需求分析拉代码用的是责任人凭证，不是当前点击的人")
	}
	ctx = withGitToken(ctx, token)

	if _, err := os.Stat(filepath.Join(cacheDir, ".git")); err == nil {
		// Existing checkout: refresh to the latest base branch.
		if err := refreshClarifyWorkspace(ctx, cacheDir, cloneURL, baseBranch); err != nil {
			return "", err
		}
		return cacheDir, nil
	}

	// Fresh checkout.
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o700); err != nil {
		return "", err
	}
	// Clear any stale non-git leftovers before cloning.
	_ = os.RemoveAll(cacheDir)

	args := []string{
		"clone", "--no-hardlinks", "--single-branch", "--depth", "1",
		"--branch", baseBranch,
		gitwork.RewriteRemoteForToken(cloneURL, token), cacheDir,
	}
	if _, err := runGit(ctx, cacheDir, args...); err != nil {
		_ = os.RemoveAll(cacheDir)
		return "", fmt.Errorf("clone clarify workspace: %w", err)
	}
	if err := applyWorkspaceGitAuth(ctx, cacheDir); err != nil {
		return "", err
	}
	if _, err := gitC(ctx, cacheDir, "remote", "set-url", "origin", cloneURL); err != nil {
		return "", fmt.Errorf("set origin url: %w", err)
	}
	return cacheDir, nil
}

// refreshClarifyWorkspace performs a clean "git pull" of the base branch on an
// existing checkout: re-apply auth, re-point origin, fetch and hard-reset.
func refreshClarifyWorkspace(ctx context.Context, cacheDir, cloneURL, baseBranch string) error {
	if err := applyWorkspaceGitAuth(ctx, cacheDir); err != nil {
		return err
	}
	if _, err := gitC(ctx, cacheDir, "remote", "set-url", "origin", cloneURL); err != nil {
		return fmt.Errorf("set origin url: %w", err)
	}
	steps := [][]string{
		{"fetch", "--prune", "--depth", "1", "origin", baseBranch},
		{"checkout", "--force", "-B", baseBranch, "origin/" + baseBranch},
		{"reset", "--hard", "origin/" + baseBranch},
	}
	for _, args := range steps {
		if _, err := gitC(ctx, cacheDir, args...); err != nil {
			return fmt.Errorf("refresh clarify workspace (%s): %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// ResolveClarifyGitToken resolves the token for the responsible user.
func ResolveClarifyGitToken(s *repo.Store, responsibleUserID int64) string {
	return resolveGitToken(s, responsibleUserID)
}
