package engine

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// cloneFromRepositoryCache makes a self-contained checkout using the host
// cache. False asks the caller to try the usual direct clone.
func cloneFromRepositoryCache(ctx context.Context, workspace string, repository domain.RepositoryContext, branch string) bool {
	root, remote := gitwork.RepositoryCacheFrom(ctx), strings.TrimSpace(repository.CloneURL)
	if remote == "" || strings.TrimSpace(branch) == "" || gitwork.RepositoryCachePath(root, repository.RepositoryID) == "" {
		return false
	}
	if err := gitwork.CloneFromRepositoryCache(ctx, root, repository.RepositoryID, remote, branch, workspace, true, cloneWatch(repository)); err != nil {
		log.Printf("repository %s: repository cache unavailable, cloning fully: %v", repository.RepositoryID, err)
		return false
	}
	return true
}
