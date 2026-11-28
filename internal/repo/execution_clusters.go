package repo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"agentflow/internal/apperr"
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// Keep the URL-bound encryption purpose shared with the single-connection trial.
const openSandboxKeyPurpose = "loopforge/platform-opensandbox/v1"

type executionClusterRow struct {
	ID           string `gorm:"primaryKey;size:80"`
	Name         string `gorm:"size:100"`
	URL          string `gorm:"type:text"`
	APIKey       string `gorm:"type:text"`
	SecureAccess bool
	Enabled      bool
	ProjectIDs   []string `gorm:"serializer:json;type:text"`
}

func (executionClusterRow) TableName() string { return "execution_clusters" }

func (r *ExecutionClustersRepo) ConfigureOpenSandboxEncryption(secret string) error {
	if secret == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(openSandboxKeyPurpose))
	ring, err := credcrypt.Parse("platform:" + base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if err == nil {
		r.openSandboxKeyring = ring
	}
	return err
}

func (row executionClusterRow) cluster() domain.ExecutionCluster {
	return domain.ExecutionCluster{
		ID: row.ID, Name: row.Name, Enabled: row.Enabled, ProjectIDs: row.ProjectIDs,
		OpenSandboxConnection: domain.OpenSandboxConnection{URL: row.URL, SecureAccess: row.SecureAccess, HasAPIKey: row.APIKey != ""},
	}
}

func (r *ExecutionClustersRepo) ExecutionClusters(ctx context.Context) ([]domain.ExecutionCluster, error) {
	var rows []executionClusterRow
	if err := r.DB.WithContext(ctx).Order("name, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	clusters := make([]domain.ExecutionCluster, 0, len(rows))
	for _, row := range rows {
		clusters = append(clusters, row.cluster())
	}
	return clusters, nil
}

func (r *ExecutionClustersRepo) executionClusterRow(ctx context.Context, id string) (executionClusterRow, error) {
	var row executionClusterRow
	err := r.DB.WithContext(ctx).Take(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = apperr.New(apperr.NotFound, "执行集群不存在")
	}
	return row, err
}

func (r *ExecutionClustersRepo) AllowedExecutionCluster(ctx context.Context, id, projectID string, forNew bool) (domain.ExecutionCluster, error) {
	row, err := r.executionClusterRow(ctx, id)
	if err != nil {
		return domain.ExecutionCluster{}, err
	}
	cluster := row.cluster()
	if !cluster.AllowsProject(projectID) {
		return domain.ExecutionCluster{}, apperr.New(apperr.Forbidden, "当前项目无权使用此执行集群")
	}
	if forNew && !cluster.Enabled {
		return domain.ExecutionCluster{}, apperr.New(apperr.Conflict, "执行集群已停用")
	}
	return cluster, nil
}

func (r *ExecutionClustersRepo) OpenSandboxClusterConnection(ctx context.Context, id, projectID string) (domain.OpenSandboxConnection, error) {
	row, err := r.executionClusterRow(ctx, id)
	if err != nil {
		return domain.OpenSandboxConnection{}, err
	}
	if !row.cluster().AllowsProject(projectID) {
		return domain.OpenSandboxConnection{}, apperr.New(apperr.Forbidden, "当前项目无权使用此执行集群")
	}
	return r.openSandboxConnection(row)
}

func (r *ExecutionClustersRepo) AdminOpenSandboxClusterConnection(ctx context.Context, id string) (domain.OpenSandboxConnection, error) {
	row, err := r.executionClusterRow(ctx, id)
	if err != nil {
		return domain.OpenSandboxConnection{}, err
	}
	return r.openSandboxConnection(row)
}

func (r *ExecutionClustersRepo) openSandboxConnection(row executionClusterRow) (domain.OpenSandboxConnection, error) {
	config := row.cluster().OpenSandboxConnection
	if config.HasAPIKey {
		key, err := r.openSandboxKeyring.Open(row.APIKey, openSandboxKeyPurpose+"\x00"+config.URL)
		if err != nil {
			return config, errors.New("无法读取 OpenSandbox API Key，请重新保存密钥")
		}
		defer clear(key)
		config.APIKey = string(key)
	}
	return config, nil
}

func (r *ExecutionClustersRepo) SaveExecutionCluster(ctx context.Context, cluster domain.ExecutionCluster, create, keepKey, keepSecureAccess bool) (domain.ExecutionCluster, error) {
	row := executionClusterRow{
		ID: cluster.ID, Name: cluster.Name, URL: cluster.URL, Enabled: cluster.Enabled,
		ProjectIDs: cluster.ProjectIDs, SecureAccess: cluster.SecureAccess,
	}
	if cluster.APIKey != "" {
		sealed, err := r.openSandboxKeyring.Seal([]byte(cluster.APIKey), openSandboxKeyPurpose+"\x00"+cluster.URL)
		if err != nil {
			return domain.ExecutionCluster{}, fmt.Errorf("无法加密 OpenSandbox API Key：%w", err)
		}
		row.APIKey = sealed
	}
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if create {
			return tx.Create(&row).Error
		}
		var previous executionClusterRow
		if err := r.locking(tx).Take(&previous, "id = ?", row.ID).Error; err != nil {
			return err
		}
		if row.URL != previous.URL {
			return apperr.New(apperr.Conflict, "更换服务地址时请添加新的执行集群，以保留已有工作区的连接")
		}
		if keepKey {
			row.APIKey = previous.APIKey
		}
		if keepSecureAccess {
			row.SecureAccess = previous.SecureAccess
		}
		return tx.Model(&executionClusterRow{}).Where("id = ?", row.ID).Omit("id", "url").Select("*").Updates(&row).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = apperr.New(apperr.NotFound, "执行集群不存在")
	}
	return row.cluster(), err
}
