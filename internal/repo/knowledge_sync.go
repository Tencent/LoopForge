package repo

import (
	knowledge "agentflow/internal/knowledge/publication"
	"context"
	"errors"
	"gorm.io/gorm"
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
	res := q.Updates(map[string]any{"lease_owner": owner, "lease_version": gorm.Expr("GREATEST(lease_version + 1, CAST(UNIX_TIMESTAMP(UTC_TIMESTAMP(6)) * 1000000 AS UNSIGNED))"), "lease_until": gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()), "status": "syncing", "index_status": "indexing"})
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) RenewKnowledgeSync(id, owner string, ttl time.Duration) (bool, error) {
	res := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting' AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", id, owner).Update("lease_until", gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()))
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) FinishKnowledgeSync(id, owner, head string, count int, lastError string) (bool, error) {
	updates := map[string]any{"lease_owner": "", "lease_until": nil, "last_error": lastError, "status": gorm.Expr("CASE WHEN status = 'pending' THEN 'pending' ELSE 'error' END"), "index_status": "error"}
	if lastError == "" {
		updates["status"] = gorm.Expr("CASE WHEN status = 'pending' THEN 'pending' ELSE 'ready' END")
		updates["index_status"] = "ready"
		updates["head_commit"] = head
		updates["doc_count"] = count
		updates["last_synced_at"] = gorm.Expr("UTC_TIMESTAMP(6)")
	}
	res := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND status <> 'deleting' AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", id, owner).Updates(updates)
	return res.RowsAffected == 1, res.Error
}

// PublishKnowledgeSync separates content publication from the short control CAS.
// The DB claim generation fences content commits, independently of the owner CAS.
func (r *KnowledgeRepo) PublishKnowledgeSync(ctx context.Context, id, owner, head string, count int, write func(context.Context) error) (bool, error) {
	var row knowledgeSourceRow
	err := r.DB.Where("id = ? AND status = 'syncing' AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", id, owner).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := write(knowledge.WithFence(ctx, id, row.LeaseVersion)); err != nil {
		return false, err
	}
	return r.FinishKnowledgeSync(id, owner, head, count, "")
}

// RequestKnowledgeSync changes only queue state, preserving the current lease
// and published snapshot even when a user queues another pass mid-sync.
func (r *KnowledgeRepo) RequestKnowledgeSync(id string) error {
	res := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND kind = 'git' AND status <> 'deleting'", id).Updates(map[string]any{"status": "pending", "last_error": ""})
	if res.Error != nil {
		return res.Error
	}
	var n int64
	if err := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND kind = 'git' AND status <> 'deleting'", id).Count(&n).Error; err != nil {
		return err
	}
	if n != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
