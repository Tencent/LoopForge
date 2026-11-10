package repo

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// ClaimKnowledgeSync uses database time so workers need not share a clock.
// Expired syncing rows are reclaimable after a process crash.
func (r *KnowledgeRepo) ClaimKnowledgeSync(id, owner string, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 {
		return false, errors.New("owner and positive TTL required")
	}
	q := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND kind = ? AND status IN ?", id, "git", []string{"pending", "syncing"}).Where("lease_until IS NULL OR lease_until <= UTC_TIMESTAMP(6)")
	res := q.Updates(map[string]any{"lease_owner": owner, "lease_until": gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()), "status": "syncing", "index_status": "indexing"})
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) RenewKnowledgeSync(id, owner string, ttl time.Duration) (bool, error) {
	res := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", id, owner).Update("lease_until", gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()))
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) FinishKnowledgeSync(id, owner, head string, count int, lastError string) (bool, error) {
	updates := map[string]any{"lease_owner": "", "lease_until": nil, "last_error": lastError, "status": "error", "index_status": "error"}
	if lastError == "" {
		updates["status"] = "ready"
		updates["index_status"] = "ready"
		updates["head_commit"] = head
		updates["doc_count"] = count
		updates["last_synced_at"] = gorm.Expr("UTC_TIMESTAMP(6)")
	}
	res := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", id, owner).Updates(updates)
	return res.RowsAffected == 1, res.Error
}

// PublishKnowledgeSync holds the control row lock during content publication.
// Even if this process pauses beyond TTL, another claimant cannot publish until
// this transaction releases the row. A stale owner is rejected before writing.
// The content transaction remains independent; a crash between commits causes
// a retry, never a stale writer overwriting a later claimant's snapshot.
func (r *KnowledgeRepo) PublishKnowledgeSync(ctx context.Context, id, owner, head string, count int, write func(context.Context) error) (bool, error) {
	published := false
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row knowledgeSourceRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		var valid int64
		if err := tx.Model(&knowledgeSourceRow{}).Where("id = ? AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", id, owner).Count(&valid).Error; err != nil {
			return err
		}
		if valid != 1 {
			return nil
		}
		if err := write(ctx); err != nil {
			return err
		}
		if err := tx.Model(&knowledgeSourceRow{}).Where("id = ?", id).Updates(map[string]any{"head_commit": head, "doc_count": count, "status": "ready", "index_status": "ready", "last_error": "", "last_synced_at": gorm.Expr("UTC_TIMESTAMP(6)"), "lease_owner": "", "lease_until": nil}).Error; err != nil {
			return err
		}
		published = true
		return nil
	})
	return published, err
}
