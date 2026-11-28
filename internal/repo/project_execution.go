package repo

import (
	"context"
	"errors"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type projectExecutionTargetRow struct {
	ProjectID string `gorm:"primaryKey;size:128"`
	Pool      string `gorm:"size:24"`
	RunnerID  string `gorm:"size:80"`
	ClusterID string `gorm:"size:80"`
}

// Keep the preview table so existing project cluster defaults still apply.
func (projectExecutionTargetRow) TableName() string { return "project_execution_cluster_defaults" }

func (r *ProjectsRepo) ProjectExecutionTarget(ctx context.Context, projectID string) (domain.ExecutionTarget, error) {
	if projectID == "" {
		return domain.ExecutionTarget{}, nil
	}
	var row projectExecutionTargetRow
	err := r.DB.WithContext(ctx).Take(&row, "project_id = ?", projectID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ExecutionTarget{}, nil
	}
	if row.Pool == "" && row.ClusterID != "" {
		row.Pool = domain.ExecutionTargetCluster
	}
	return domain.ExecutionTarget{Pool: row.Pool, RunnerID: row.RunnerID, ClusterID: row.ClusterID}, err
}

func (r *ProjectsRepo) SetProjectExecutionTarget(ctx context.Context, projectID string, target domain.ExecutionTarget) error {
	row := projectExecutionTargetRow{ProjectID: projectID, Pool: target.Pool, RunnerID: target.RunnerID, ClusterID: target.ClusterID}
	return r.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}}, DoUpdates: clause.AssignmentColumns([]string{"pool", "runner_id", "cluster_id"}),
	}).Create(&row).Error
}
