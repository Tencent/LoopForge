package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type issueDeletionView struct {
	domain.Issue
	DeletionAllowed bool                     `json:"deletionAllowed"`
	DeletionBlock   *repo.IssueDeletionBlock `json:"deletionBlock,omitempty"`
	ClosureAllowed  bool                     `json:"closureAllowed"`
}

func (h issuesHandlers) issueDeletionViews(issues []domain.Issue) ([]issueDeletionView, error) {
	blocks, err := h.repo.IssueDeletionBlocks(issues)
	if err != nil {
		return nil, err
	}
	closed, err := h.repo.IssueClosureAllowed(issues)
	if err != nil {
		return nil, err
	}
	if issues == nil {
		return nil, nil
	}
	out := make([]issueDeletionView, 0, len(issues))
	for _, issue := range issues {
		view := issueDeletionView{Issue: issue, DeletionAllowed: true, ClosureAllowed: closed[issue.ID]}
		if block, ok := blocks[issue.ID]; ok {
			view.DeletionAllowed = false
			view.DeletionBlock = &block
		}
		out = append(out, view)
	}
	return out, nil
}

func (h issuesHandlers) writeIssueView(c *gin.Context, status int, issue domain.Issue) {
	views, err := h.issueDeletionViews([]domain.Issue{issue})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(status, views[0])
}
