package repo

import (
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type knowledgeOperationLeaseRow struct {
	Dirty bool       `gorm:"column:dirty;not null;default:false"`
	ID    string     `gorm:"column:id;primaryKey;size:191"`
	Owner string     `gorm:"column:owner;size:255"`
	Until *time.Time `gorm:"column:lease_until"`
}

func (knowledgeOperationLeaseRow) TableName() string { return "knowledge_operation_leases" }

func (r *KnowledgeRepo) ClaimKnowledgeOperation(id, owner string, ttl time.Duration) (bool, error) {
	if owner == "" || ttl <= 0 {
		return false, errors.New("owner and positive TTL required")
	}
	if err := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&knowledgeOperationLeaseRow{ID: id}).Error; err != nil {
		return false, err
	}
	q := r.DB.Model(&knowledgeOperationLeaseRow{}).Where("id = ?", id)
	updates := map[string]any{"owner": owner}
	if r.isMySQL() {
		q = q.Where("lease_until IS NULL OR lease_until <= UTC_TIMESTAMP(6)")
		updates["lease_until"] = gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds())
	} else {
		now := time.Now().UTC()
		q = q.Where("lease_until IS NULL OR lease_until <= ?", now)
		updates["lease_until"] = now.Add(ttl)
	}
	res := q.Updates(updates)
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) RenewKnowledgeOperation(id, owner string, ttl time.Duration) (bool, error) {
	q := r.DB.Model(&knowledgeOperationLeaseRow{}).Where("id = ? AND owner = ?", id, owner)
	var until any
	if r.isMySQL() {
		q = q.Where("lease_until > UTC_TIMESTAMP(6)")
		until = gorm.Expr("TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(6))", ttl.Microseconds())
	} else {
		now := time.Now().UTC()
		q = q.Where("lease_until > ?", now)
		until = now.Add(ttl)
	}
	res := q.Update("lease_until", until)
	return res.RowsAffected == 1, res.Error
}
func (r *KnowledgeRepo) ReleaseKnowledgeOperation(id, owner string) error {
	return r.DB.Model(&knowledgeOperationLeaseRow{}).Where("id = ? AND owner = ?", id, owner).Updates(map[string]any{"owner": "", "lease_until": nil}).Error
}

// BeginKnowledgeSourceDelete fences future claims before any content cleanup.
// A source already marked deleting can resume an interrupted cleanup.
func (r *KnowledgeRepo) BeginKnowledgeSourceDelete(id, owner string) (bool, error) {
	q := r.DB.Model(&knowledgeSourceRow{}).Where("id = ? AND kind = 'git'", id)
	if r.isMySQL() {
		q = q.Where("lease_until IS NULL OR lease_until <= UTC_TIMESTAMP(6)")
	} else {
		q = q.Where("lease_until IS NULL OR lease_until <= ?", time.Now().UTC())
	}
	updates := map[string]any{"status": "deleting", "lease_owner": owner}
	if r.isMySQL() {
		updates["lease_until"] = gorm.Expr("TIMESTAMPADD(SECOND,60,UTC_TIMESTAMP(6))")
		updates["lease_version"] = gorm.Expr("GREATEST(lease_version + 1, CAST(UNIX_TIMESTAMP(UTC_TIMESTAMP(6)) * 1000000 AS UNSIGNED))")
	} else {
		updates["lease_until"] = time.Now().UTC().Add(time.Minute)
		updates["lease_version"] = time.Now().UnixMicro()
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
func (r *KnowledgeRepo) DeleteKnowledgeSourceMarked(id, owner string) error {
	q := r.DB.Where("id = ? AND status = 'deleting' AND lease_owner = ?", id, owner)
	if r.isMySQL() {
		q = q.Where("lease_until > UTC_TIMESTAMP(6)")
	} else {
		q = q.Where("lease_until > ?", time.Now().UTC())
	}
	res := q.Delete(&knowledgeSourceRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *KnowledgeRepo) SetKnowledgeOperationDirty(id, owner string, dirty bool) error {
	q := r.DB.Model(&knowledgeOperationLeaseRow{}).Where("id = ? AND owner = ?", id, owner)
	updates := map[string]any{"dirty": dirty}
	if r.isMySQL() {
		q = q.Where("lease_until > UTC_TIMESTAMP(6)")
		updates["lease_until"] = gorm.Expr("TIMESTAMPADD(MICROSECOND,1000,lease_until)")
	} else {
		q = q.Where("lease_until > ?", time.Now().UTC())
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("operation lease lost")
	}
	return nil
}
func (r *KnowledgeRepo) KnowledgeOperationDirty(id string) (bool, error) {
	var row knowledgeOperationLeaseRow
	err := r.DB.Select("dirty").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Dirty, err
}
