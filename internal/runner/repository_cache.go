package runner

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
	"agentflow/internal/runtime"
)

// cloneFromRepositoryCache checks repo out as a shared clone of the
// repository cache in ctx. False means a full clone has to follow: there is
// no cache for this workspace, or it could not be used.
func cloneFromRepositoryCache(ctx context.Context, workspace string, repo *domain.RunnerRepository, watch procwatch.Options) bool {
	root := gitwork.RepositoryCacheFrom(ctx)
	if gitwork.RepositoryCachePath(root, repo.RepositoryID) == "" {
		return false
	}
	err := gitwork.CloneFromRepositoryCache(ctx, root, repo.RepositoryID, strings.TrimSpace(repo.CloneURL), repo.Ref, workspace, watch)
	if err != nil {
		log.Printf("runner checkout: repository cache of %s unavailable, cloning fully: %s", repo.RepositoryID, cloneOutput([]byte(err.Error()), repo.GitToken))
		return false
	}
	return true
}

// warmRepositoryCaches fetches the caches of the repositories a native
// conversation workspace may clone later.
func warmRepositoryCaches(ctx context.Context, inv *domain.RunnerInvocation) {
	if inv == nil || len(inv.CacheRepositories) == 0 {
		return
	}
	ctx = gitwork.WithSSHKeys(ctx, inv.GitSSHKeys)
	runtime.WarmRepositoryCaches(invocationGitHTTPContext(ctx, inv), inv.CacheRepositories)
}
