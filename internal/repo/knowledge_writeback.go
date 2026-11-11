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
// is stale. Pending rows are excluded because that attempt is still running.
func (r *KnowledgeRepo) KnowledgeWritebacksNeedingRefresh(before time.Time, limit int) ([]domain.KnowledgeWriteback, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []knowledgeWritebackRow
	if err := r.DB.
		Where("status IN ? AND (checked_at IS NULL OR checked_at < ?)", []string{domain.KnowledgeWritebackBranchPushed, domain.KnowledgeWritebackMROpen}, before.UTC()).
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
func (r *KnowledgeRepo) UpdateKnowledgeWritebackState(id, status, mrURL, mrProvider, mrState, note, errText string, mrIID int, checkedAt time.Time) error {
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
	res := r.DB.Model(&knowledgeWritebackRow{}).Where("id = ?", strings.TrimSpace(id)).Updates(updates)
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
			Where("id = ? AND kind = ? AND (lease_owner = '' OR lease_until IS NULL OR lease_until <= UTC_TIMESTAMP(6))", id, "git").
			Updates(map[string]any{
				"lease_owner": owner,
				"lease_until": gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds()),
			})
		return res.RowsAffected == 1, res.Error
	}
	claimed := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row knowledgeSourceRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND kind = ?", id, "git").Take(&row).Error; err != nil {
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
// writeback merged. An active lease means another worker is already touching
// the source; the caller leaves the writeback open and retries later.
func (r *KnowledgeRepo) QueueKnowledgeResync(sourceID string) (bool, error) {
	sourceID = strings.TrimSpace(sourceID)
	updates := map[string]any{"status": "pending", "last_error": "", "lease_owner": "", "lease_until": nil}
	var res *gorm.DB
	if r.isMySQL() {
		res = r.DB.Model(&knowledgeSourceRow{}).
			Where("id = ? AND kind = ? AND (lease_owner = '' OR lease_until IS NULL OR lease_until <= UTC_TIMESTAMP(6))", sourceID, "git").
			Updates(updates)
	} else {
		res = r.DB.Model(&knowledgeSourceRow{}).
			Where("id = ? AND kind = ? AND (lease_owner = '' OR lease_until IS NULL OR lease_until <= ?)", sourceID, "git", time.Now().UTC()).
			Updates(updates)
	}
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 1 {
		return true, nil
	}
	// Already pending means the resync is queued; that is success for the caller.
	var row knowledgeSourceRow
	if err := r.DB.Select("status").Where("id = ? AND kind = ?", sourceID, "git").Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.Status == "pending", nil
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
