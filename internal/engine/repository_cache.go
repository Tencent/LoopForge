package engine

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
)

// workspaceCacheRepositories are the project repositories a native
// conversation workspace keeps warm in its execution machine's repository
// cache: its Agent clones them on demand.
func (e *Engine) workspaceCacheRepositories(run domain.Run) []domain.RunnerRepository {
	projectID := strings.TrimSpace(run.ExecutionContext.ProjectID)
	if !run.IsNativeWorkspace() || projectID == "" {
		return nil
	}
	var out []domain.RunnerRepository
	for _, repository := range e.repo.Repositories() {
		cloneURL := strings.TrimSpace(repository.CloneURL)
		if repository.ProjectID != projectID || cloneURL == "" {
			continue
		}
		out = append(out, domain.RunnerRepository{
			RepositoryID: repository.ID,
			Name:         repository.Name,
			CloneURL:     cloneURL,
			UseSSH:       repository.Provider == domain.RepositoryProviderSSH || gitwork.IsSSHReference(cloneURL),
		})
	}
	return out
}

// warmRepositoryCaches warms the platform's own cache for a native
// conversation workspace it hosts. ctx carries the run's Git credentials.
func (e *Engine) warmRepositoryCaches(ctx context.Context, run domain.Run) {
	runtime.WarmRepositoryCaches(ctx, e.workspaceCacheRepositories(run))
}

// cloneFromRepositoryCache checks repository out at branch as a shared clone
// of the repository cache in ctx. False leaves workspace empty for a full
// clone: there is no cache for this workspace, or it could not be used.
func cloneFromRepositoryCache(ctx context.Context, workspace string, repository domain.RepositoryContext, branch string) bool {
	root, remote := gitwork.RepositoryCacheFrom(ctx), strings.TrimSpace(repository.CloneURL)
	if remote == "" || gitwork.RepositoryCachePath(root, repository.RepositoryID) == "" {
		return false
	}
	if err := gitwork.CloneFromRepositoryCache(ctx, root, repository.RepositoryID, remote, branch, workspace, cloneWatch(repository)); err != nil {
		log.Printf("repository %s: repository cache unavailable, cloning fully: %v", repository.RepositoryID, err)
		return false
	}
	return true
}
