package repo

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// KnowledgeRepo owns user-isolated knowledge connector configurations.
type KnowledgeRepo struct {
	*deps
}

// ListKnowledgeConnectors returns all connectors owned by ownerUserID,
// ordered from oldest to newest.
func (r *KnowledgeRepo) ListKnowledgeConnectors(ownerUserID int64) []domain.KnowledgeConnector {
	var rows []knowledgeConnectorRow
	if err := r.DB.Where("owner_user_id = ?", ownerUserID).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.KnowledgeConnector, 0, len(rows))
	for _, row := range rows {
		out = append(out, knowledgeConnectorFromRow(row))
	}
	return out
}

// GetKnowledgeConnector returns the connector identified by id only if it belongs
// to ownerUserID.
func (r *KnowledgeRepo) GetKnowledgeConnector(ownerUserID int64, id string) (domain.KnowledgeConnector, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.KnowledgeConnector{}, false
	}
	var row knowledgeConnectorRow
	if err := r.DB.Where("id = ? AND owner_user_id = ?", id, ownerUserID).Take(&row).Error; err != nil {
		return domain.KnowledgeConnector{}, false
	}
	return knowledgeConnectorFromRow(row), true
}

// PutKnowledgeConnector creates or updates a connector under conn.OwnerUserID.
// If another user already owns a connector with conn.ID, PutKnowledgeConnector
// refuses the write to prevent cross-user hijacking.
func (r *KnowledgeRepo) PutKnowledgeConnector(conn domain.KnowledgeConnector) error {
	conn.ID = strings.TrimSpace(conn.ID)
	if conn.ID == "" {
		return errors.New("knowledge connector id is required")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var existing knowledgeConnectorRow
		err := tx.Where("id = ?", conn.ID).Take(&existing).Error
		switch {
		case err == nil:
			if existing.OwnerUserID != conn.OwnerUserID {
				return errors.New("knowledge connector id belongs to another user")
			}
			now := time.Now().UTC()
			createdAt := now
			if existing.CreatedAt != nil && !existing.CreatedAt.IsZero() {
				createdAt = *existing.CreatedAt
			}
			cfgBytes, err := marshalConnectorConfig(conn.Config)
			if err != nil {
				return err
			}
			secret := strings.TrimSpace(conn.SecretEnc)
			var secretPtr *string
			if secret != "" {
				secretPtr = &secret
			}
			updates := map[string]any{
				"name":        strings.TrimSpace(conn.Name),
				"type":        strings.TrimSpace(conn.Type),
				"endpoint":    strings.TrimSpace(conn.Endpoint),
				"config_json": cfgBytes,
				"secret_enc":  secretPtr,
				"enabled":     conn.Enabled,
				"updated_at":  &now,
				"created_at":  &createdAt,
			}
			return tx.Model(&knowledgeConnectorRow{}).Where("id = ? AND owner_user_id = ?", conn.ID, conn.OwnerUserID).Updates(updates).Error

		case errors.Is(err, gorm.ErrRecordNotFound):
			now := time.Now().UTC()
			createdAt := now
			if !conn.CreatedAt.IsZero() {
				createdAt = conn.CreatedAt.UTC()
			}
			cfgBytes, err := marshalConnectorConfig(conn.Config)
			if err != nil {
				return err
			}
			secret := strings.TrimSpace(conn.SecretEnc)
			var secretPtr *string
			if secret != "" {
				secretPtr = &secret
			}
			row := knowledgeConnectorRow{
				ID:          conn.ID,
				OwnerUserID: conn.OwnerUserID,
				Name:        strings.TrimSpace(conn.Name),
				Type:        strings.TrimSpace(conn.Type),
				Endpoint:    strings.TrimSpace(conn.Endpoint),
				ConfigJSON:  cfgBytes,
				SecretEnc:   secretPtr,
				Enabled:     conn.Enabled,
				CreatedAt:   &createdAt,
				UpdatedAt:   &now,
			}
			return tx.Create(&row).Error

		default:
			return err
		}
	})
}

// DeleteKnowledgeConnector deletes id only when owned by ownerUserID.
func (r *KnowledgeRepo) DeleteKnowledgeConnector(ownerUserID int64, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	res := r.DB.Where("id = ? AND owner_user_id = ?", id, ownerUserID).Delete(&knowledgeConnectorRow{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func marshalConnectorConfig(cfg domain.KnowledgeConnectorConfig) ([]byte, error) {
	return json.Marshal(cfg)
}

func knowledgeConnectorFromRow(row knowledgeConnectorRow) domain.KnowledgeConnector {
	var cfg domain.KnowledgeConnectorConfig
	if len(row.ConfigJSON) > 0 && string(row.ConfigJSON) != "null" {
		_ = json.Unmarshal(row.ConfigJSON, &cfg)
	}
	secretEnc := ""
	if row.SecretEnc != nil {
		secretEnc = strings.TrimSpace(*row.SecretEnc)
	}
	out := domain.KnowledgeConnector{
		ID:               row.ID,
		OwnerUserID:      row.OwnerUserID,
		Name:             row.Name,
		Type:             row.Type,
		Endpoint:         row.Endpoint,
		Config:           cfg,
		SecretEnc:        secretEnc,
		SecretConfigured: secretEnc != "",
		Enabled:          row.Enabled,
	}
	if row.CreatedAt != nil {
		out.CreatedAt = *row.CreatedAt
	}
	if row.UpdatedAt != nil {
		out.UpdatedAt = *row.UpdatedAt
	}
	return out
}
