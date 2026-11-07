package repo

import (
	"agentflow/internal/domain"
	"gorm.io/gorm"
)

func (r *ProjectsRepo) projectInTx(tx *gorm.DB, id string) (domain.Project, bool) {
	return payloadOne[domain.Project](tx, "projects", id)
}
