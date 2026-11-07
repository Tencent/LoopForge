package repo

import (
	"encoding/json"
	"errors"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

var ErrProjectTriggerConfigExists = errors.New("当前项目已有触发配置，请刷新后编辑已有配置")

// PutProjectTriggerConfig serializes config writes on the existing project row.
// Locking the parent also protects the first insert across API instances.
func (r *CatalogRepo) PutProjectTriggerConfig(v domain.ScheduleJob) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		if err := r.projects.lockProjectInTx(tx, v.ProjectID); errors.Is(err, gorm.ErrRecordNotFound) {
			// An existing job can outlive its project record. Preserve in-place
			// edits (and its stored owner), but never create or move a config into
			// a missing project. With no new inserts allowed, its own row is a
			// sufficient lock for the compatibility update.
			var row scheduleJobRow
			if err := r.locking(tx).Where("id = ?", v.ID).First(&row).Error; err != nil {
				return err
			}
			var prior domain.ScheduleJob
			if err := json.Unmarshal(row.Payload, &prior); err != nil {
				return err
			}
			if prior.ProjectID != v.ProjectID || prior.SkipDispatch {
				return gorm.ErrRecordNotFound
			}
		} else if err != nil {
			return err
		}
		var rows []scheduleJobRow
		if err := tx.Where(r.sqlJSONText("$.projectId")+" = ?", "$.projectId", v.ProjectID).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var existing domain.ScheduleJob
			if err := json.Unmarshal(row.Payload, &existing); err != nil {
				return err
			}
			if !existing.SkipDispatch && existing.ID != v.ID {
				return ErrProjectTriggerConfigExists
			}
		}
		writer := *r
		writer.deps = &deps{DB: tx}
		return writer.PutScheduleJob(v)
	})
}
