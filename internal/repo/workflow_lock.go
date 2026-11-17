package repo

import (
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WorkflowEditLock is a short lease that keeps two people from editing the same
// SOP at once. Optimistic revisions already stop a *stale* save, but they do not
// stop two tabs from both starting at r7 and one silently overwriting the other;
// a lease makes the conflict visible before anyone types.
type WorkflowEditLock struct {
	WorkflowID string    `gorm:"column:workflow_id;primaryKey;size:64" json:"workflowId"`
	UserID     int64     `gorm:"column:user_id" json:"userId"`
	UserLogin  string    `gorm:"column:user_login;size:64" json:"userLogin"`
	ExpiresAt  time.Time `gorm:"column:expires_at" json:"expiresAt"`
	UpdatedAt  time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (WorkflowEditLock) TableName() string { return "workflow_edit_locks" }

// DefaultWorkflowLockTTL is how long a lease lasts without a renewal.
const DefaultWorkflowLockTTL = 3 * time.Minute

// AcquireWorkflowLock takes or renews the lease. It returns the current holder
// and whether the caller owns it now. takeover steals an active lease.
//
// Implemented as SELECT ... FOR UPDATE + write inside one transaction rather than
// a conditional upsert: MySQL ignores ON CONFLICT ... WHERE, so an upsert would
// silently overwrite another user's lease.
func (r *CatalogRepo) AcquireWorkflowLock(workflowID string, userID int64, login string, ttl time.Duration, takeover bool) (WorkflowEditLock, bool, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" || userID <= 0 {
		return WorkflowEditLock{}, false, nil
	}
	if ttl <= 0 {
		ttl = DefaultWorkflowLockTTL
	}
	var result WorkflowEditLock
	owned := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var row WorkflowEditLock
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workflow_id = ?", workflowID).Take(&row).Error
		switch {
		case findErr == gorm.ErrRecordNotFound:
			result = WorkflowEditLock{WorkflowID: workflowID, UserID: userID, UserLogin: login, ExpiresAt: now.Add(ttl), UpdatedAt: now}
			if err := tx.Create(&result).Error; err != nil {
				return err
			}
			owned = true
			return nil
		case findErr != nil:
			return findErr
		}
		if row.ExpiresAt.After(now) && row.UserID != userID && !takeover {
			result = row
			owned = false
			return nil
		}
		result = WorkflowEditLock{WorkflowID: workflowID, UserID: userID, UserLogin: login, ExpiresAt: now.Add(ttl), UpdatedAt: now}
		if err := tx.Model(&WorkflowEditLock{}).Where("workflow_id = ?", workflowID).Updates(map[string]any{
			"user_id": userID, "user_login": login, "expires_at": result.ExpiresAt, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		owned = true
		return nil
	})
	if err != nil {
		return WorkflowEditLock{}, false, err
	}
	return result, owned, nil
}

// WorkflowLock returns the live lease of a workflow (zero value when free).
func (r *CatalogRepo) WorkflowLock(workflowID string) (WorkflowEditLock, bool) {
	var row WorkflowEditLock
	if err := r.DB.Where("workflow_id = ?", strings.TrimSpace(workflowID)).Take(&row).Error; err != nil {
		return WorkflowEditLock{}, false
	}
	if row.ExpiresAt.Before(time.Now().UTC()) {
		return WorkflowEditLock{}, false
	}
	return row, true
}

// ReleaseWorkflowLock drops the lease when the caller owns it.
func (r *CatalogRepo) ReleaseWorkflowLock(workflowID string, userID int64) error {
	q := r.DB.Where("workflow_id = ?", strings.TrimSpace(workflowID))
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	return q.Delete(&WorkflowEditLock{}).Error
}
