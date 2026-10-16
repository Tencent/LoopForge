package repo

import (
	"agentflow/internal/domain"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

var ErrBusinessAssignmentForbidden = errors.New("项目 Owner 或归属已变化，当前用户不能设置首次归属")

var ErrBusinessAssignmentConflict = errors.New("项目归属已变化，请刷新后重试")

// SetProjectBusinessDomain atomically validates the active target and records a compare-and-swap assignment.
func (r *ProjectsRepo) SetProjectBusinessDomain(projectID, expectedID, targetID string, actorID int64, reason string, ownerOnly bool) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var project projectRow
		if err := r.locking(tx).Where("id = ?", projectID).Take(&project).Error; err != nil {
			return err
		}
		if ownerOnly && (actorID <= 0 || project.OwnerUserID == nil || *project.OwnerUserID != actorID || project.BusinessDomainID != "") {
			return ErrBusinessAssignmentForbidden
		}
		if project.BusinessDomainID != expectedID {
			return ErrBusinessAssignmentConflict
		}
		if expectedID == targetID {
			return nil
		}
		if targetID != "" {
			var target businessDomainRow
			if err := r.locking(tx).Where("id = ?", targetID).Take(&target).Error; err != nil {
				return err
			}
			if !target.Active {
				return errors.New("已停用的业务域不能接收项目")
			}
		}
		now := time.Now().UTC()
		if err := tx.Model(&projectRow{}).Where("id = ?", projectID).Updates(map[string]any{"business_domain_id": targetID, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Create(&businessDomainAssignmentRow{ProjectID: projectID, FromDomainID: expectedID, ToDomainID: targetID, ActorID: actorID, Reason: strings.TrimSpace(reason), CreatedAt: now}).Error
	})
}
func (r *ProjectsRepo) ProjectBusinessDomainHistory(projectID string) ([]domain.BusinessDomainAssignment, error) {
	var rows []businessDomainAssignmentRow
	if err := r.DB.Where("project_id = ?", projectID).Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.BusinessDomainAssignment, 0, len(rows))
	for _, v := range rows {
		out = append(out, domain.BusinessDomainAssignment{ID: v.ID, ProjectID: v.ProjectID, FromDomainID: v.FromDomainID, ToDomainID: v.ToDomainID, ActorID: v.ActorID, Reason: v.Reason, CreatedAt: v.CreatedAt})
	}
	return out, nil
}
