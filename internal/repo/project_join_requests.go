package repo

import (
	"errors"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------- Project join requests ----------

func joinRequestFromRow(row projectJoinRequestRow) domain.ProjectJoinRequest {
	v := domain.ProjectJoinRequest{
		ID: row.ID, ProjectID: row.ProjectID, UserID: row.UserID, Role: row.Role,
		Reason: row.Reason, Status: row.Status, ReviewedAt: row.Reviewed,
	}
	if row.ReviewerID != nil {
		v.ReviewerID = *row.ReviewerID
	}
	if row.Created != nil {
		v.CreatedAt = *row.Created
	}
	if row.Updated != nil {
		v.UpdatedAt = *row.Updated
	}
	return v
}

// CreateProjectJoinRequest records a pending request. A user can hold at most
// one pending request per project and cannot apply to a project they already
// own or belong to.
func (r *ProjectsRepo) CreateProjectJoinRequest(projectID string, userID int64, role, reason string) (domain.ProjectJoinRequest, error) {
	var out projectJoinRequestRow
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var project projectRow
		if err := r.locking(tx).Where("id = ?", projectID).Take(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.New(apperr.NotFound, "项目不存在")
			}
			return err
		}
		var archived struct {
			ArchivedAt *time.Time `json:"archivedAt"`
		}
		_ = jsonUnmarshal(project.Payload, &archived)
		if archived.ArchivedAt != nil {
			return apperr.New(apperr.NotFound, "项目不存在或已删除")
		}
		if project.OwnerUserID != nil && *project.OwnerUserID == userID {
			return apperr.New(apperr.Conflict, "你已经是该项目的 Owner")
		}
		var members int64
		if err := tx.Model(&projectMemberRow{}).Where("project_id = ? AND user_id = ?", projectID, userID).Count(&members).Error; err != nil {
			return err
		}
		if members > 0 {
			return apperr.New(apperr.Conflict, "你已经是该项目的成员")
		}
		var pending projectJoinRequestRow
		err := tx.Where("project_id = ? AND user_id = ? AND status = ?", projectID, userID, domain.JoinRequestPending).Take(&pending).Error
		if err == nil {
			return apperr.New(apperr.Conflict, "已有待审批的申请，请等待处理").With("requestId", pending.ID)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := timePtr(time.Now().UTC())
		out = projectJoinRequestRow{ProjectID: projectID, UserID: userID, Role: role, Reason: reason,
			Status: domain.JoinRequestPending, Created: now, Updated: now}
		return tx.Create(&out).Error
	})
	if err != nil {
		return domain.ProjectJoinRequest{}, err
	}
	return joinRequestFromRow(out), nil
}

func (r *ProjectsRepo) ProjectJoinRequest(id int64) (domain.ProjectJoinRequest, bool) {
	var row projectJoinRequestRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.ProjectJoinRequest{}, false
	}
	return joinRequestFromRow(row), true
}

// ProjectJoinRequests lists requests newest first. Empty projectIDs and a
// zero userID match every project and applicant; status "" matches all.
func (r *ProjectsRepo) ProjectJoinRequests(projectIDs []string, userID int64, status string) ([]domain.ProjectJoinRequest, error) {
	q := r.DB.Order("id DESC")
	if len(projectIDs) > 0 {
		q = q.Where("project_id IN ?", projectIDs)
	}
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []projectJoinRequestRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.ProjectJoinRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, joinRequestFromRow(row))
	}
	return out, nil
}

// ReviewProjectJoinRequest settles a pending request. Approval adds the
// applicant with role in the same transaction; an applicant who became a
// member meanwhile keeps the existing role.
func (r *ProjectsRepo) ReviewProjectJoinRequest(id, reviewerID int64, approve bool, role string) (domain.ProjectJoinRequest, error) {
	var out projectJoinRequestRow
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := r.locking(tx).Where("id = ?", id).Take(&out).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.New(apperr.NotFound, "申请不存在")
			}
			return err
		}
		if out.Status != domain.JoinRequestPending {
			return apperr.New(apperr.Conflict, "申请已处理，请刷新后查看")
		}
		if err := r.lockProjectInTx(tx, out.ProjectID); err != nil {
			return err
		}
		now := timePtr(time.Now().UTC())
		out.Status, out.ReviewerID, out.Reviewed, out.Updated = domain.JoinRequestRejected, &reviewerID, now, now
		if approve {
			out.Status, out.Role = domain.JoinRequestApproved, role
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&projectMemberRow{
				ProjectID: out.ProjectID, UserID: out.UserID, Role: role, Created: now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&projectRow{}).Where("id = ?", out.ProjectID).Update("updated_at", now).Error; err != nil {
				return err
			}
		}
		return tx.Save(&out).Error
	})
	if err != nil {
		return domain.ProjectJoinRequest{}, err
	}
	return joinRequestFromRow(out), nil
}

// WithdrawProjectJoinRequest lets the applicant cancel a pending request.
func (r *ProjectsRepo) WithdrawProjectJoinRequest(id, userID int64) (domain.ProjectJoinRequest, error) {
	var out projectJoinRequestRow
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		if err := r.locking(tx).Where("id = ?", id).Take(&out).Error; err != nil || out.UserID != userID {
			if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.New(apperr.NotFound, "申请不存在")
			}
			return err
		}
		if out.Status != domain.JoinRequestPending {
			return apperr.New(apperr.Conflict, "申请已处理，无法撤回")
		}
		now := timePtr(time.Now().UTC())
		out.Status, out.Updated = domain.JoinRequestWithdrawn, now
		return tx.Save(&out).Error
	})
	if err != nil {
		return domain.ProjectJoinRequest{}, err
	}
	return joinRequestFromRow(out), nil
}
