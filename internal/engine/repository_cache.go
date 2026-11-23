package engine

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

// cloneFromRepositoryCache makes a self-contained checkout using the host
// cache. False with no error asks the caller to try a direct clone.
func cloneFromRepositoryCache(ctx context.Context, workspace string, repository domain.RepositoryContext, branch string) (bool, error) {
	root, remote := gitwork.RepositoryCacheFrom(ctx), strings.TrimSpace(repository.CloneURL)
	if remote == "" || strings.TrimSpace(branch) == "" || gitwork.RepositoryCachePath(root, repository.RepositoryID) == "" {
		return false, nil
	}
	if err := gitwork.CloneFromRepositoryCache(ctx, root, repository.RepositoryID, remote, branch, workspace, true, cloneWatch(repository)); err != nil {
		if procwatch.Stopped(err) || ctx.Err() != nil {
			return false, err
		}
		log.Printf("repository %s: repository cache unavailable, cloning fully: %v", repository.RepositoryID, err)
		return false, nil
	}
	return true, nil
}
