package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

var ErrSharedScheduleVersionConflict = errors.New("共享任务已有新版本，请刷新后再发布")

// SharedScheduleAssetReference identifies a published version that still
// protects one of its direct assets from being unpublished or deleted.
type SharedScheduleAssetReference struct {
	SharedTaskID string `json:"sharedTaskId"`
	Name         string `json:"name"`
	Version      int    `json:"version"`
}

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

// SharedScheduleAssetReferences returns the effective direct references for
// an asset. Every shared task's latest version is effective, as is each older
// version still pinned by a subscription job. Nested dependencies remain
// governed by the referenced asset's own sharing and resolution logic.
func (r *CatalogRepo) SharedScheduleAssetReferences(kind, id string) ([]SharedScheduleAssetReference, error) {
	latest, err := r.SharedScheduleTasks()
	if err != nil {
		return nil, fmt.Errorf("读取共享任务最新版本失败: %w", err)
	}

	versions := make(map[string]domain.SharedScheduleVersion, len(latest))
	versionKey := func(taskID string, version int) string {
		return taskID + "\x00" + fmt.Sprintf("%d", version)
	}
	for _, item := range latest {
		versions[versionKey(item.ID, item.Version)] = item
	}

	var rows []scheduleJobRow
	if err := r.DB.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取共享任务订阅失败: %w", err)
	}
	for _, row := range rows {
		var job domain.ScheduleJob
		if err := json.Unmarshal(row.Payload, &job); err != nil {
			return nil, fmt.Errorf("解析定时任务 %q 失败: %w", row.ID, err)
		}
		taskID := strings.TrimSpace(job.SharedTaskID)
		if taskID == "" || job.SharedTaskVersion < 1 {
			continue
		}
		key := versionKey(taskID, job.SharedTaskVersion)
		if _, ok := versions[key]; ok {
			continue
		}
		item, err := r.SharedScheduleVersion(taskID, job.SharedTaskVersion)
		if err != nil {
			return nil, fmt.Errorf("读取订阅任务 %q 绑定的共享版本 %s v%d 失败: %w", row.Name, taskID, job.SharedTaskVersion, err)
		}
		versions[key] = item
	}

	out := make([]SharedScheduleAssetReference, 0)
	for _, item := range versions {
		if !item.HasAsset(kind, id) {
			continue
		}
		out = append(out, SharedScheduleAssetReference{
			SharedTaskID: item.ID,
			Name:         item.Name,
			Version:      item.Version,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].SharedTaskID != out[j].SharedTaskID {
			return out[i].SharedTaskID < out[j].SharedTaskID
		}
		return out[i].Version < out[j].Version
	})
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
