package runner

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

// cloneFromRepositoryCache checks out repo using the host cache. False with no
// error asks the caller to try a direct clone.
func cloneFromRepositoryCache(ctx context.Context, workspace string, repo *domain.RunnerRepository, watch procwatch.Options) (bool, error) {
	root := gitwork.RepositoryCacheFrom(ctx)
	if strings.TrimSpace(repo.Ref) == "" || gitwork.RepositoryCachePath(root, repo.RepositoryID) == "" {
		return false, nil
	}
	err := gitwork.CloneFromRepositoryCache(ctx, root, repo.RepositoryID, strings.TrimSpace(repo.CloneURL), repo.Ref, workspace, false, watch)
	if err != nil {
		if procwatch.Stopped(err) || ctx.Err() != nil {
			return false, err
		}
		log.Printf("runner checkout: repository cache of %s unavailable, cloning fully: %s", repo.RepositoryID, cloneOutput([]byte(err.Error()), repo.GitToken))
		return false, nil
	}
	return true, nil
}
