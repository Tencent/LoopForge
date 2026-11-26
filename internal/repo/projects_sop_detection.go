package repo

import (
	"agentflow/internal/domain"

	"gorm.io/gorm"
)

type SOPDetectionProject struct {
	ID     string
	Config domain.SOPDetectionConfig
}

func (r *ProjectsRepo) SOPDetectionProjects() ([]SOPDetectionProject, error) {
	var rows []projectRow
	// JSON true is returned as text on MySQL and an integer on SQLite.
	enabled := r.sqlPayloadJSONText("sopDetection.enabled")
	if err := r.DB.Select("id", "payload").Where(enabled+" = ? OR "+enabled+" = ?", "true", 1).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]SOPDetectionProject, 0, len(rows))
	for _, row := range rows {
		var p domain.Project
		if jsonUnmarshal(row.Payload, &p) == nil && p.ArchivedAt == nil && p.SOPDetection != nil && p.SOPDetection.Enabled {
			out = append(out, SOPDetectionProject{row.ID, *p.SOPDetection})
		}
	}
	return out, nil
}

func (r *ProjectsRepo) PutSOPDetection(projectID string, setting domain.SOPDetectionConfig) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row projectRow
		if err := r.locking(tx).Where("id = ?", projectID).Take(&row).Error; err != nil {
			return err
		}
		var project domain.Project
		if err := jsonUnmarshal(row.Payload, &project); err != nil {
			return err
		}
		if project.ArchivedAt != nil {
			return gorm.ErrRecordNotFound
		}
		if current := project.SOPDetection; current != nil && current.Enabled && setting.Enabled {
			setting.EnabledAt = current.EnabledAt
		}
		project.SOPDetection = &setting
		return tx.Model(&projectRow{}).Where("id = ?", projectID).Update("payload", mustJSON(project)).Error
	})
}
