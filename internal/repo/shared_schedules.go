package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

var ErrSharedScheduleVersionConflict = errors.New("共享任务已有新版本，请刷新后再发布")

func (r *CatalogRepo) SharedScheduleVersions(id string) ([]domain.SharedScheduleVersion, error) {
	var rows []sharedScheduleVersionRow
	if err := r.DB.Where("shared_task_id = ?", id).Order("version DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.SharedScheduleVersion, 0, len(rows))
	for _, row := range rows {
		var v domain.SharedScheduleVersion
		if err := json.Unmarshal(row.Payload, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (r *CatalogRepo) SharedScheduleVersion(id string, version int) (domain.SharedScheduleVersion, error) {
	var row sharedScheduleVersionRow
	if err := r.DB.Where("shared_task_id = ? AND version = ?", id, version).First(&row).Error; err != nil {
		return domain.SharedScheduleVersion{}, err
	}
	var v domain.SharedScheduleVersion
	err := json.Unmarshal(row.Payload, &v)
	return v, err
}
func (r *CatalogRepo) SharedScheduleTasks() ([]domain.SharedScheduleVersion, error) {
	var rows []sharedScheduleVersionRow
	if err := r.DB.Raw("SELECT v.* FROM shared_schedule_versions v JOIN (SELECT shared_task_id, MAX(version) AS version FROM shared_schedule_versions GROUP BY shared_task_id) latest ON v.shared_task_id = latest.shared_task_id AND v.version = latest.version ORDER BY v.created_at DESC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.SharedScheduleVersion, 0, len(rows))
	for _, row := range rows {
		var v domain.SharedScheduleVersion
		if err := json.Unmarshal(row.Payload, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (r *CatalogRepo) PublishSharedSchedule(jobID, projectID string, v domain.SharedScheduleVersion, baseVersion int) (domain.SharedScheduleVersion, error) {
	if err := v.Validate(); err != nil {
		return v, err
	}
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var source scheduleJobRow
		if err := r.locking(tx).Where("id = ?", jobID).First(&source).Error; err != nil {
			return err
		}
		var job domain.ScheduleJob
		if err := json.Unmarshal(source.Payload, &job); err != nil {
			return err
		}
		if !job.SkipDispatch || job.SharedTaskID != "" || job.ProjectID != projectID {
			return fmt.Errorf("只能从当前项目的独立 AI 定时任务发布共享版本")
		}
		// Read the direct references from the same transaction as the locked
		// source job, never from publish JSON. Nested dependencies remain owned
		// by the referenced asset's existing resolver.
		catalog := *r
		catalog.deps = &deps{DB: tx}
		sum := sha256.Sum256([]byte(projectID + "\x00" + jobID))
		id := "shared-" + hex.EncodeToString(sum[:16])
		var latest sharedScheduleVersionRow
		err := tx.Where("shared_task_id = ?", id).Order("version DESC").First(&latest).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if baseVersion != latest.Version {
			return ErrSharedScheduleVersionConflict
		}
		refs, err := catalog.directScheduleReferences(job.References)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if err := catalog.shareScheduleReference(ref, projectID); err != nil {
				return err
			}
		}
		assets, err := catalog.sharedScheduleAssets(job.References)
		if err != nil {
			return err
		}
		v.Assets = assets
		v.References = make([]domain.AssistantReference, 0, len(assets))
		for _, asset := range assets {
			ref, shared, ok := catalog.sharedScheduleReference(domain.AssistantReference{Type: asset.Type, ID: asset.ID})
			if !ok || !shared || ref.ProjectID != asset.ProjectID {
				return fmt.Errorf("%w：%s/%s 的权限或归属已变更", ErrSharedScheduleReferences, asset.Type, asset.ID)
			}
			v.References = append(v.References, ref)
		}
		v.ID, v.Version, v.SourceJobID, v.SourceProjectID = id, latest.Version+1, jobID, projectID
		v.CreatedAt = time.Now().UTC()
		return tx.Create(&sharedScheduleVersionRow{ID: fmt.Sprintf("%s-v%d", id, v.Version), SharedTaskID: id, Version: v.Version, Payload: mustJSON(v), Created: timePtr(v.CreatedAt)}).Error
	})
	return v, err
}
