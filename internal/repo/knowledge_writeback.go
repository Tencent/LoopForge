package repo

import (
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------- Git writeback records ----------

func knowledgeWritebackFromRow(row knowledgeWritebackRow) domain.KnowledgeWriteback {
	out := domain.KnowledgeWriteback{
		ID: row.ID, SubmissionID: "", SourceID: row.SourceID, ProjectID: row.ProjectID,
		Path: row.Path, Branch: row.Branch, Commit: row.Commit,
		MRURL: row.MRURL, MRProvider: row.MRProvider, MRIID: row.MRIID, MRState: row.MRState,
		Status: row.Status, Note: row.Note, Error: row.Error, Attempts: row.Attempts,
	}
	if row.SubmissionID != nil {
		out.SubmissionID = *row.SubmissionID
	}
	if row.CheckedAt != nil {
		at := row.CheckedAt.UTC()
		out.CheckedAt = &at
	}
	if row.Created != nil {
		out.CreatedAt = row.Created.UTC()
	}
	if row.Updated != nil {
		out.UpdatedAt = row.Updated.UTC()
	}
	return out
}

// PutKnowledgeWriteback inserts or replaces one writeback. A missing
// submission ID is stored as NULL, not an empty string: the unique index then
// permits more than one row without a review item (manual/legacy records).
func (r *KnowledgeRepo) PutKnowledgeWriteback(item domain.KnowledgeWriteback) error {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		return errors.New("knowledge writeback id is required")
	}
	item.SubmissionID = strings.TrimSpace(item.SubmissionID)
	if item.Status == "" {
		item.Status = domain.KnowledgeWritebackPending
	}
	var submissionID *string
	if item.SubmissionID != "" {
		submissionID = &item.SubmissionID
	}
	now := time.Now().UTC()
	row := knowledgeWritebackRow{
		ID: item.ID, SubmissionID: submissionID, SourceID: item.SourceID, ProjectID: item.ProjectID,
		Path: item.Path, Branch: item.Branch, Commit: item.Commit,
		MRURL: item.MRURL, MRProvider: item.MRProvider, MRIID: item.MRIID, MRState: item.MRState,
		Status: item.Status, Note: item.Note, Error: item.Error, Attempts: item.Attempts,
		CheckedAt: item.CheckedAt, Created: &now, Updated: &now,
	}
	return r.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"submission_id", "source_id", "project_id", "path", "branch", "commit",
			"mr_url", "mr_provider", "mr_iid", "mr_state", "status", "note",
			"error_text", "attempts", "checked_at", "updated_at",
		}),
	}).Create(&row).Error
}

func (r *KnowledgeRepo) KnowledgeWriteback(id string) (domain.KnowledgeWriteback, bool) {
	var row knowledgeWritebackRow
	if err := r.DB.Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return domain.KnowledgeWriteback{}, false
	}
	return knowledgeWritebackFromRow(row), true
}

func (r *KnowledgeRepo) KnowledgeWritebackBySubmission(submissionID string) (domain.KnowledgeWriteback, bool) {
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		return domain.KnowledgeWriteback{}, false
	}
	var row knowledgeWritebackRow
	if err := r.DB.Where("submission_id = ?", submissionID).Take(&row).Error; err != nil {
		return domain.KnowledgeWriteback{}, false
	}
	return knowledgeWritebackFromRow(row), true
}

// KnowledgeWritebacksBySubmissions is the bulk read used when listing the
// review queue; a missing entry simply stays absent.
func (r *KnowledgeRepo) KnowledgeWritebacksBySubmissions(submissionIDs []string) map[string]domain.KnowledgeWriteback {
	out := map[string]domain.KnowledgeWriteback{}
	if len(submissionIDs) == 0 {
		return out
	}
	var rows []knowledgeWritebackRow
	if err := r.DB.Where("submission_id IN ?", submissionIDs).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		if row.SubmissionID == nil {
			continue
		}
		out[*row.SubmissionID] = knowledgeWritebackFromRow(row)
	}
	return out
}

// KnowledgeWritebacksNeedingRefresh returns open writebacks whose state check
// is stale. Expired pending attempts are recoverable after a crash.
func (r *KnowledgeRepo) KnowledgeWritebacksNeedingRefresh(before time.Time, limit int) ([]domain.KnowledgeWriteback, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []knowledgeWritebackRow
	if err := r.DB.
		Where("((status IN ? AND (checked_at IS NULL OR checked_at < ?)) OR (status = ? AND updated_at < ?))", []string{domain.KnowledgeWritebackBranchPushed, domain.KnowledgeWritebackMROpen}, before.UTC(), domain.KnowledgeWritebackPending, before.UTC()).
		Order("checked_at IS NULL DESC, checked_at ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.KnowledgeWriteback, 0, len(rows))
	for _, row := range rows {
		out = append(out, knowledgeWritebackFromRow(row))
	}
	return out, nil
}

// UpdateKnowledgeWritebackState refreshes only lifecycle fields. Branch,
// commit and MR identity stay from the successful push.
func (r *KnowledgeRepo) UpdateKnowledgeWritebackState(id, status, mrURL, mrProvider, mrState, note, errText string, mrIID int, checkedAt time.Time, claim ...string) error {
	updates := map[string]any{"status": status, "mr_state": mrState, "checked_at": checkedAt.UTC(), "updated_at": time.Now().UTC()}
	if strings.TrimSpace(mrURL) != "" {
		updates["mr_url"] = mrURL
	}
	if strings.TrimSpace(mrProvider) != "" {
		updates["mr_provider"] = mrProvider
	}
	if mrIID > 0 {
		updates["mr_iid"] = mrIID
	}
	if note != "" {
		updates["note"] = note
	}
	updates["error_text"] = errText
	q := r.DB.Model(&knowledgeWritebackRow{}).Where("id = ?", strings.TrimSpace(id)).Where("status NOT IN ?", []string{domain.KnowledgeWritebackMerged, domain.KnowledgeWritebackClosed})
	if len(claim) > 0 && claim[0] != "" {
		q = q.Where("claim_owner = ? AND claimed_until > CURRENT_TIMESTAMP", claim[0])
		if r.isMySQL() {
			updates["checked_at"] = gorm.Expr("UTC_TIMESTAMP(6)")
		} else {
			updates["checked_at"] = gorm.Expr("CURRENT_TIMESTAMP")
		}
	} else {
		q = q.Where("checked_at IS NULL OR checked_at <= ?", checkedAt.UTC())
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteKnowledgeWritebacksBySource drops the writeback history with a deleted
// source; Service deletes content first and control rows last.
func (r *KnowledgeRepo) DeleteKnowledgeWritebacksBySource(sourceID string) error {
	return r.DB.Where("source_id = ?", strings.TrimSpace(sourceID)).Delete(&knowledgeWritebackRow{}).Error
}

// DeleteKnowledgeWritebackBySubmission removes the writeback left by a review
// item that is being deleted before publication.
func (r *KnowledgeRepo) DeleteKnowledgeWritebackBySubmission(submissionID string) error {
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		return nil
	}
	return r.DB.Where("submission_id = ?", submissionID).Delete(&knowledgeWritebackRow{}).Error
}

// ---------- write lease (same columns as sync, separate app-time claim) ----------

// ClaimKnowledgeWrite takes the source row lease used by both sync and
// writeback. It never changes the source status: a pending sync stays pending
// and is picked up after the write lease is released. MySQL uses database time
// (like the sync lease) so instances do not depend on synchronized clocks; the
// sqlite test schema falls back to application time in a transaction.
func (r *KnowledgeRepo) ClaimKnowledgeWrite(id, owner string, ttl time.Duration) (bool, error) {
	id, owner = strings.TrimSpace(id), strings.TrimSpace(owner)
	if id == "" || owner == "" || ttl <= 0 {
		return false, errors.New("source id, owner and positive TTL required")
	}
	if r.isMySQL() {
		res := r.DB.Model(&knowledgeSourceRow{}).
			Where("id = ? AND status <> 'deleting' AND kind = ? AND (lease_owner = '' OR lease_until IS NULL OR lease_until <= UTC_TIMESTAMP(6))", id, "git").
			Where("NOT EXISTS (SELECT 1 FROM knowledge_writebacks w WHERE BINARY w.source_id = BINARY knowledge_sources.id AND w.claimed_until > UTC_TIMESTAMP(6))").
			Updates(map[string]any{
				"lease_owner": owner,
				"lease_until": gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()),
			})
		return res.RowsAffected == 1, res.Error
	}
	claimed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row knowledgeSourceRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status <> 'deleting' AND kind = ?", id, "git").Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		now := time.Now().UTC()
		if row.LeaseOwner != "" && row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			return nil
		}
		if err := tx.Model(&knowledgeSourceRow{}).Where("id = ?", id).Updates(map[string]any{
			"lease_owner": owner,
			"lease_until": now.Add(ttl),
		}).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

func (r *KnowledgeRepo) RenewKnowledgeWrite(id, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, errors.New("positive TTL required")
	}
	if r.isMySQL() {
		res := r.DB.Model(&knowledgeSourceRow{}).
			Where("id = ? AND kind = ? AND lease_owner = ? AND lease_until > UTC_TIMESTAMP(6)", strings.TrimSpace(id), "git", strings.TrimSpace(owner)).
			Update("lease_until", gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()))
		return res.RowsAffected == 1, res.Error
	}
	now := time.Now().UTC()
	res := r.DB.Model(&knowledgeSourceRow{}).
		Where("id = ? AND kind = ? AND lease_owner = ? AND lease_until > ?", strings.TrimSpace(id), "git", strings.TrimSpace(owner), now).
		Update("lease_until", now.Add(ttl))
	return res.RowsAffected == 1, res.Error
}

func (r *KnowledgeRepo) FinishKnowledgeWrite(id, owner string) (bool, error) {
	updates := map[string]any{"lease_owner": "", "lease_until": nil}
	res := r.DB.Model(&knowledgeSourceRow{}).
		Where("id = ? AND lease_owner = ?", strings.TrimSpace(id), strings.TrimSpace(owner)).
		Updates(updates)
	return res.RowsAffected == 1, res.Error
}

// QueueKnowledgeResync asks the sync worker to pull the source again after a
// writeback merged. Queueing preserves any active source lease.
func (r *KnowledgeRepo) QueueKnowledgeResync(sourceID string) (bool, error) {
	err := r.RequestKnowledgeSync(strings.TrimSpace(sourceID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ---------- review queue view overlay ----------

func applyKnowledgeWritebackView(item *domain.KnowledgeSubmission, wb domain.KnowledgeWriteback) {
	if item == nil || wb.ID == "" {
		return
	}
	if wb.Branch != "" {
		item.Branch = wb.Branch
	}
	if wb.Commit != "" {
		item.Commit = wb.Commit
	}
	item.MRURL = wb.MRURL
	item.MRProvider = wb.MRProvider
	item.MRStatus = wb.Status
	item.WriteNote = wb.Note
	item.WriteError = wb.Error
}

// ClaimKnowledgeWriteback excludes active publishers and competing reconcilers.
func (r *KnowledgeRepo) ClaimKnowledgeWriteback(id, owner string, ttl time.Duration) (bool, error) {
	q := r.DB.Model(&knowledgeWritebackRow{}).Where("id = ? AND status IN ?", id, []string{domain.KnowledgeWritebackPending, domain.KnowledgeWritebackBranchPushed, domain.KnowledgeWritebackMROpen})
	updates := map[string]any{"claim_owner": owner}
	if r.isMySQL() {
		q = q.Where("(claimed_until IS NULL OR claimed_until <= UTC_TIMESTAMP(6)) AND NOT EXISTS (SELECT 1 FROM knowledge_sources s WHERE BINARY s.id = BINARY knowledge_writebacks.source_id AND s.lease_until > UTC_TIMESTAMP(6) AND s.lease_owner <> ?)", owner)
		updates["claimed_until"] = gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds())
	} else {
		now := time.Now().UTC()
		q = q.Where("claimed_until IS NULL OR claimed_until <= ?", now)
		updates["claimed_until"] = now.Add(ttl)
	}
	res := q.Updates(updates)
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) ReleaseKnowledgeWriteback(id, owner string) error {
	return r.DB.Model(&knowledgeWritebackRow{}).Where("id = ? AND claim_owner = ?", id, owner).Updates(map[string]any{"claim_owner": "", "claimed_until": nil}).Error
}

// PutKnowledgeWritebackOwned fences every attempt update with the source lease.
// Only control-plane writes happen while this short transaction owns the row.
func (r *KnowledgeRepo) PutKnowledgeWritebackOwned(item domain.KnowledgeWriteback, owner string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&knowledgeSourceRow{}).Where("id = ? AND lease_owner = ? AND status <> 'deleting'", item.SourceID, owner)
		var extension any
		if r.isMySQL() {
			q = q.Where("lease_until > UTC_TIMESTAMP(6)")
			extension = gorm.Expr("TIMESTAMPADD(MICROSECOND, 1000, lease_until)")
		} else {
			q = q.Where("lease_until > ?", time.Now().UTC())
			extension = gorm.Expr("lease_until")
		}
		res := q.Update("lease_until", extension)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("writeback source lease lost")
		}
		return (&KnowledgeRepo{deps: &deps{DB: tx}}).PutKnowledgeWriteback(item)
	})
}

// RecoverKnowledgeWritebackSubmission makes a crashed publish retryable after
// the reconcile owner has converted its pending attempt to failed.
func (r *KnowledgeRepo) RecoverKnowledgeWritebackSubmission(id string) error {
	item, ok := r.KnowledgeSubmission(id)
	if !ok || item.Status != domain.KnowledgeSubmissionPublishing {
		return nil
	}
	wb, ok := r.KnowledgeWritebackBySubmission(id)
	if !ok {
		return nil
	}
	if wb.Status == domain.KnowledgeWritebackFailed {
		item.Status = domain.KnowledgeSubmissionPending
		item.Error = wb.Error
	} else {
		if wb.Commit == "" {
			return nil
		}
		item.Status = domain.KnowledgeSubmissionPublished
		item.Commit = wb.Commit
		item.Branch = wb.Branch
		item.MRURL = wb.MRURL
		item.MRProvider = wb.MRProvider
		item.MRStatus = wb.Status
		item.PublishedAt = time.Now().UTC()
	}
	return r.PutKnowledgeSubmission(item)
}
