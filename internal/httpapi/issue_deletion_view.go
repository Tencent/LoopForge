package httpapi

import (
	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

type issueDeletionView struct {
	domain.Issue
	DeletionAllowed bool `json:"deletionAllowed"`
	ClosureAllowed  bool `json:"closureAllowed"`
}

func (h issuesHandlers) issueDeletionViews(issues []domain.Issue) ([]issueDeletionView, error) {
	allowed, err := h.repo.IssueDeletionAllowed(issues)
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
		out = append(out, issueDeletionView{Issue: issue, DeletionAllowed: allowed[issue.ID], ClosureAllowed: closed[issue.ID]})
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
