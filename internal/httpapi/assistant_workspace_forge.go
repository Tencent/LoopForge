package httpapi

import (
	"context"
	"net/url"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
)

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
