package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

type issueDeletionView struct {
	domain.Issue
	DeletionAllowed       bool                     `json:"deletionAllowed"`
	DeletionRequiresForce bool                     `json:"deletionRequiresForce,omitempty"`
	DeletionBlock         *repo.IssueDeletionBlock `json:"deletionBlock,omitempty"`
	ClosureAllowed        bool                     `json:"closureAllowed"`
}

func (h issuesHandlers) issueDeletionViews(issues []domain.Issue) ([]issueDeletionView, error) {
	states, err := h.repo.IssueDeletionStates(issues)
	if err != nil {
		return nil, err
	}
	if issues == nil {
		return nil, nil
	}
	out := make([]issueDeletionView, 0, len(issues))
	for _, issue := range issues {
		state := states[issue.ID]
		out = append(out, issueDeletionView{
			Issue:                 issue,
			DeletionAllowed:       state.Block == nil,
			DeletionRequiresForce: state.RequiresForce(),
			DeletionBlock:         state.Block,
			ClosureAllowed:        state.Block == nil,
		})
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
