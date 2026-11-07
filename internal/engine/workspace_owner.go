package engine

import (
	"strings"

	"agentflow/internal/domain"
)

// The workspace of a run lives only on the host (or execution machine) that
// checked it out. Nodes do not push intermediate commits: the remote work
// branch receives code only from delivery steps (MR, integration). A child
// SOP works in the checkout of the run that started its SOP chain.

// workspaceRunID returns the run whose workspace r executes in.
func workspaceRunID(r domain.Run) string {
	if id := strings.TrimSpace(r.ExecutionContext.WorkspaceRunID); id != "" {
		return id
	}
	return r.ID
}

// sharesWorkspace reports whether r is a child SOP working in another run's
// checkout.
func sharesWorkspace(r domain.Run) bool {
	return workspaceRunID(r) != r.ID
}

// workspaceBind returns the (host, root) holding r's workspace. A child SOP
// follows the run that owns the checkout once that run has bound one.
func (e *Engine) workspaceBind(r domain.Run) (string, string) {
	if sharesWorkspace(r) {
		if owner, ok := e.repo.RunState(workspaceRunID(r)); ok && strings.TrimSpace(owner.WorkspaceRoot) != "" {
			return owner.WorkspaceHost, owner.WorkspaceRoot
		}
	}
	return r.WorkspaceHost, r.WorkspaceRoot
}

// hasCodeWorkspace reports whether losing r's workspace loses code changes:
// it checks out a repository, or works in another run's checkout.
func hasCodeWorkspace(r domain.Run) bool {
	if sharesWorkspace(r) {
		return true
	}
	for _, rc := range r.RepositoryContexts() {
		if strings.TrimSpace(rc.RepositoryID) != "" {
			return true
		}
	}
	return false
}

// hasWorkspaceProgress reports whether nodes other than nodeID already
// changed the workspace, so continuing on a fresh checkout would silently
// drop their work.
func hasWorkspaceProgress(r domain.Run, nodeID string) bool {
	if sharesWorkspace(r) {
		return true
	}
	for id, nr := range r.Nodes {
		if id != nodeID && nr != nil && nr.Status == "succeeded" {
			return true
		}
	}
	return false
}
