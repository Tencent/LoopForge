package repo

import (
	"agentflow/internal/domain"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

type businessDomainRow struct {
	ID        string    `gorm:"column:id;primaryKey;size:64"`
	Name      string    `gorm:"column:name;size:255;uniqueIndex:uq_business_domains_name"`
	Active    bool      `gorm:"column:active;not null"`
	Payload   []byte    `gorm:"column:payload;type:json"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (businessDomainRow) TableName() string { return "business_domains" }

type businessDomainAssignmentRow struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ProjectID    string    `gorm:"column:project_id;size:64;index:idx_business_assignment_project"`
	FromDomainID string    `gorm:"column:from_domain_id;size:64"`
	ToDomainID   string    `gorm:"column:to_domain_id;size:64"`
	ActorID      int64     `gorm:"column:actor_id"`
	Reason       string    `gorm:"column:reason;type:text"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (businessDomainAssignmentRow) TableName() string { return "business_domain_assignments" }

func (r *BusinessDomainsRepo) BusinessDomains() ([]domain.BusinessDomain, error) {
	var rows []businessDomainRow
	if err := r.DB.Order("name, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.BusinessDomain, 0, len(rows))
	for _, row := range rows {
		var v domain.BusinessDomain
		if err := jsonUnmarshal(row.Payload, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (r *BusinessDomainsRepo) BusinessDomain(id string) (domain.BusinessDomain, error) {
	var row businessDomainRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.BusinessDomain{}, err
	}
	var v domain.BusinessDomain
	err := jsonUnmarshal(row.Payload, &v)
	return v, err
}

var ErrBusinessDomainConflict = errors.New("业务域配置已变化，请刷新后重试")

func (r *BusinessDomainsRepo) PutBusinessDomain(v domain.BusinessDomain, expectedUpdatedAt *time.Time) error {
	v.ID = strings.TrimSpace(v.ID)
	v.Name = strings.TrimSpace(v.Name)
	if v.ID == "" || v.Name == "" {
		return errors.New("业务域标识和名称不能为空")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row businessDomainRow
		err := r.locking(tx).Where("id = ?", v.ID).Take(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var previous domain.BusinessDomain
		if err == nil {
			if decodeErr := jsonUnmarshal(row.Payload, &previous); decodeErr != nil {
				return decodeErr
			}
		}
		// MySQL DATETIME truncates precision; compare the exact payload version.
		if (expectedUpdatedAt == nil && err == nil) || (expectedUpdatedAt != nil && (err != nil || !previous.UpdatedAt.Equal(*expectedUpdatedAt))) {
			return ErrBusinessDomainConflict
		}
		now := time.Now().UTC()
		v.CreatedAt = now
		v.UpdatedAt = now
		if err == nil {
			v.CreatedAt = previous.CreatedAt
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&businessDomainRow{ID: v.ID, Name: v.Name, Active: v.Active, Payload: mustJSON(v), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}).Error
		}
		return tx.Model(&businessDomainRow{}).Where("id = ?", v.ID).Updates(map[string]any{"name": v.Name, "active": v.Active, "payload": mustJSON(v), "updated_at": v.UpdatedAt}).Error
	})
}
