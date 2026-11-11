package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// Preflight every GitLab repository before committing or pushing any of them.
// Host-scoped auth alone is insufficient: origin may point to a different
// project on the same host, or have an additional pushurl.
func requireTaskGitLabOrigins(ctx context.Context, workspace string, inv *domain.RunnerInvocation) error {
	for _, repository := range inv.Repositories {
		if !repository.SyncWorkBranch || strings.TrimSpace(repository.WorkBranch) == "" || repository.GitAuthBaseURL == "" {
			continue
		}
		expected, ok := domain.ParseGitLabRepositoryURL(repository.CloneURL, repository.GitAuthBaseURL)
		if !ok || strings.TrimSpace(repository.GitToken) == "" {
			return fmt.Errorf("GitLab repository credentials do not match the configured repository")
		}
		dir := workspace
		if rel := safeRunnerRepoDir(repository.Dir); rel != "" {
			dir = filepath.Join(workspace, rel)
		}
		repoCtx := gitContextForRunnerRepository(ctx, &repository)
		if err := gitwork.RequireOriginURLs(repoCtx, dir, func(actual string) bool {
			info, ok := domain.ParseGitLabRepositoryURL(actual, repository.GitAuthBaseURL)
			return ok && info.Project == expected.Project
		}); err != nil {
			return err
		}
	}
	return nil
}
