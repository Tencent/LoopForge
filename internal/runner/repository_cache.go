package runner

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

// cloneFromRepositoryCache checks repo out using the host cache. False asks
// the caller to try the usual direct clone.
func cloneFromRepositoryCache(ctx context.Context, workspace string, repo *domain.RunnerRepository, watch procwatch.Options) bool {
	root := gitwork.RepositoryCacheFrom(ctx)
	if strings.TrimSpace(repo.Ref) == "" || gitwork.RepositoryCachePath(root, repo.RepositoryID) == "" {
		return false
	}
	err := gitwork.CloneFromRepositoryCache(ctx, root, repo.RepositoryID, strings.TrimSpace(repo.CloneURL), repo.Ref, workspace, false, watch)
	if err != nil {
		log.Printf("runner checkout: repository cache of %s unavailable, cloning fully: %s", repo.RepositoryID, cloneOutput([]byte(err.Error()), repo.GitToken))
		return false
	}
	return true
}
