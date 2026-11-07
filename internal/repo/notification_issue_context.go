package repo

import (
	"agentflow/internal/domain"
	"gorm.io/gorm"
)

func (r *IssuesRepo) issueInTx(tx *gorm.DB, id string) (domain.Issue, bool) {
	return payloadOne[domain.Issue](tx, "issues", id)
}
