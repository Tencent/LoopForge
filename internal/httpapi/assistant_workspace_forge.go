package httpapi

import (
	"context"
	"net/url"
	"slices"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
	"agentflow/internal/gitwork"
)

// workspacePullRepositories lets an existing SSH checkout use newly configured
// Forgejo delivery for the same binding. Git and branch settings stay frozen.
func (h *agentToolGateway) workspacePullRepositories(projectID string, repos []domain.RepositoryContext) []domain.RepositoryContext {
	if projectID == "" {
		return repos
	}
	out := slices.Clone(repos)
	for i, rc := range out {
		if rc.ReadOnly || rc.Provider != domain.RepositoryProviderSSH || rc.RepositoryID == "" {
			continue
		}
		current, ok := h.repo.Repository(rc.RepositoryID)
		if !ok || current.ProjectID != projectID || !domain.IsForgeRepositoryProvider(current.Provider) {
			continue
		}
		frozen, frozenErr := gitwork.ParseSSHRemote(rc.CloneURL)
		live, liveErr := gitwork.ParseSSHRemote(current.CloneURL)
		if frozenErr != nil || liveErr != nil || frozen.URL != live.URL {
			continue
		}
		rc.Provider, rc.WebURL, rc.GongfengProjectID = current.Provider, current.WebURL, current.GongfengProjectID
		out[i] = rc
	}
	return out
}

func (h *agentToolGateway) submitWorkspaceForgePull(ctx context.Context, userID int64, rc domain.RepositoryContext, result *workspaceMRResult, title, body string) {
	ref, err := forge.RunRepository(rc)
	if err != nil {
		result.Error = err.Error()
		result.CreateURL = rc.WebURL
		return
	}
	result.CreateURL = ref.WebURL() + "/compare/" + url.PathEscape(result.TargetBranch) + "..." + url.PathEscape(result.SourceBranch)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := forge.OpenRepository(ctx, ref, h.repo.ForgeToken(userID, ref.Instance))
	if err != nil {
		result.Error = err.Error()
		return
	}
	pr, err := client.SubmitPull(ctx, result.SourceBranch, result.TargetBranch, result.Commit, title, body, true)
	if err != nil {
		result.Error = err.Error()
		return
	}
	result.MRURL, result.MRIID = ref.PullURL(pr.Pull.Number), int64(pr.Pull.Number)
	result.Existing, result.Updated, result.UpdateError = pr.Existing, pr.Updated, pr.UpdateError
}
